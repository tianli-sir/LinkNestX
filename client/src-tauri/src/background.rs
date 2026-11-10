//! Background status and privacy-preserving notifications live beyond React windows.
use serde::{Deserialize, Serialize};
use serde_json::Value;
use std::{collections::VecDeque, sync::Mutex};
use tauri::Manager;
#[derive(Default, Clone, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct BackgroundSnapshot {
    pub sharing_enabled: bool,
    pub active_uploads: usize,
    pub error: Option<String>,
    pub last_completion: Option<String>,
}
pub struct BackgroundState {
    inner: Mutex<Reducer>,
    pub changes: tokio::sync::broadcast::Sender<()>,
}
impl Default for BackgroundState {
    fn default() -> Self {
        Self {
            inner: Mutex::new(Reducer::default()),
            changes: tokio::sync::broadcast::channel(8).0,
        }
    }
}
#[derive(Default)]
struct Reducer {
    snapshot: BackgroundSnapshot,
    seen: VecDeque<String>,
}
impl Reducer {
    fn observe(&mut self, name: &str, value: &Value, hidden: bool) -> Option<&'static str> {
        let mut notice = None;
        let mut key = None;
        match name {
            "sharing.changed" => {
                self.snapshot.sharing_enabled = value["enabled"].as_bool().unwrap_or(false);
                if !self.snapshot.sharing_enabled {
                    self.snapshot.active_uploads = 0
                }
            }
            "sharing.activity" => {
                self.snapshot.active_uploads =
                    value["activeUploads"].as_u64().unwrap_or(0).min(32) as usize
            }
            "sharing.revoked" => {
                notice = Some("文件权限或有效期已变更，应用缓存已清理。");
                key = value["versionId"].as_str().map(|v| format!("revoke:{v}"));
            }
            "service.error" => {
                self.snapshot.error = Some(
                    value["error"]
                        .as_str()
                        .unwrap_or("后台服务出现问题，请打开应用查看。 ")
                        .chars()
                        .take(240)
                        .collect(),
                );
                notice = Some("后台服务需要处理，请打开 LinkNestX 查看状态。");
                key = Some(format!(
                    "error:{}",
                    self.snapshot.error.as_deref().unwrap_or("")
                ));
            }
            "transfer.updated" if value["state"] == "failed" => {
                self.snapshot.error = Some("传输失败，请打开传输任务查看原因并重试。".into());
                notice = Some("传输失败，请打开 LinkNestX 查看原因。");
                key = value["id"].as_str().map(|id| format!("failed:{id}"));
            }
            "transfer.updated" if value["state"] == "completed" => {
                self.snapshot.last_completion = value["id"].as_str().map(str::to_owned);
                notice = Some("传输已完成。");
                key = self
                    .snapshot
                    .last_completion
                    .as_ref()
                    .map(|id| format!("complete:{id}"));
            }
            "server.event" if value["type"] == "membership.changed" => {
                notice = Some("你的群组成员权限发生变化，请打开应用查看。");
                key = value["eventId"].as_str().map(|id| format!("member:{id}"));
            }
            _ => {}
        }
        if let Some(key) = key {
            if self.seen.contains(&key) {
                return None;
            }
            self.seen.push_back(key);
            while self.seen.len() > 256 {
                self.seen.pop_front();
            }
            if hidden {
                return notice;
            }
        }
        None
    }
}
impl BackgroundState {
    pub fn snapshot(&self) -> BackgroundSnapshot {
        self.inner.lock().unwrap().snapshot.clone()
    }
}
pub fn observe<R: tauri::Runtime>(app: &tauri::AppHandle<R>, name: &str, value: &Value) {
    use tauri::Emitter;
    let Some(state) = app.try_state::<BackgroundState>() else {
        return;
    };
    let hidden = app
        .get_webview_window("main")
        .is_none_or(|w| !w.is_visible().unwrap_or(true) || w.is_minimized().unwrap_or(false));
    let notice = {
        let mut inner = state.inner.lock().unwrap();
        inner.observe(name, value, hidden)
    };
    if name == "lifecycle.refresh" {
        let _ = state.changes.send(());
    }
    if name == "server.event"
        && matches!(
            value["type"].as_str(),
            Some(
                "session.ready"
                    | "membership.changed"
                    | "file.changed"
                    | "files.changed"
                    | "sharing.policy_changed"
            )
        )
    {
        let _ = state.changes.send(());
    }
    let snapshot = state.snapshot();
    let tooltip = if snapshot.error.is_some() {
        "LinkNestX · 后台服务需要处理".into()
    } else if snapshot.active_uploads > 0 {
        format!("LinkNestX · 正在共享 {} 个分块", snapshot.active_uploads)
    } else if snapshot.sharing_enabled {
        "LinkNestX · 共享已开启，当前空闲".into()
    } else {
        "LinkNestX · 共享已暂停".into()
    };
    if let Some(tray) = app.tray_by_id("linknestx") {
        let _ = tray.set_tooltip(Some(tooltip));
    }
    if matches!(
        name,
        "service.error"
            | "sharing.changed"
            | "sharing.activity"
            | "sharing.revoked"
            | "transfer.updated"
    ) {
        let _ = app.emit("service:status", &snapshot);
    }
    if app
        .try_state::<crate::commands::NativeState>()
        .is_some_and(|s| s.exiting.load(std::sync::atomic::Ordering::SeqCst))
    {
        return;
    }
    if let Some(body) = notice {
        let handle = app.clone();
        let failure = std::sync::Arc::new(move || {
            if let Some(state) = handle.try_state::<BackgroundState>() {
                state.inner.lock().unwrap().snapshot.error =
                    Some("系统通知不可用，请在系统设置中允许 LinkNestX 通知。".into());
                let _ = handle.emit("service:status", state.snapshot());
            }
        });
        crate::notifications::notify(body, failure);
    }
}
#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;
    #[test]
    fn hidden_notifications_are_once_and_visible_events_do_not_replay() {
        let mut r = Reducer::default();
        let v = json!({"id":"one","state":"completed"});
        assert!(r.observe("transfer.updated", &v, true).is_some());
        assert!(r.observe("transfer.updated", &v, true).is_none());
        let v = json!({"id":"two","state":"completed"});
        assert!(r.observe("transfer.updated", &v, false).is_none());
        assert!(r.observe("transfer.updated", &v, true).is_none());
    }
    #[test]
    fn status_counts_active_work_and_retains_errors() {
        let mut r = Reducer::default();
        r.observe("sharing.activity", &json!({"activeUploads":3}), false);
        assert_eq!(r.snapshot.active_uploads, 3);
        r.observe("sharing.activity", &json!({"activeUploads":0}), false);
        assert_eq!(r.snapshot.active_uploads, 0);
        assert!(r
            .observe("service.error", &json!({"error":"缓存检查失败"}), true)
            .is_some());
        assert!(r.snapshot.error.is_some());
    }
    #[test]
    fn membership_event_deduplication() {
        let mut r = Reducer::default();
        let e = json!({"type":"membership.changed","eventId":"x"});
        assert!(r.observe("server.event", &e, true).is_some());
        assert!(r.observe("server.event", &e, true).is_none());
    }
}
