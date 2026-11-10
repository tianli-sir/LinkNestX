use serde_json::Value;
pub trait EventSink: Send + Sync {
    fn emit(&self, name: &str, value: Value);
    fn subscribe(&self) -> Option<tokio::sync::broadcast::Receiver<()>> {
        None
    }
}
pub struct NoopEvents;
impl EventSink for NoopEvents {
    fn emit(&self, _: &str, _: Value) {}
}
pub struct DesktopEvents<R: tauri::Runtime = tauri::Wry>(pub tauri::AppHandle<R>);
impl<R: tauri::Runtime> EventSink for DesktopEvents<R> {
    fn subscribe(&self) -> Option<tokio::sync::broadcast::Receiver<()>> {
        use tauri::Manager;
        self.0
            .try_state::<crate::background::BackgroundState>()
            .map(|s| s.changes.subscribe())
    }
    fn emit(&self, name: &str, value: Value) {
        use tauri::Emitter;
        // EventSink keeps logical names; only the Tauri boundary uses colons.
        crate::background::observe(&self.0, name, &value);
        let wire_name = name.replace('.', ":");
        if let Err(error) = self.0.emit(&wire_name, value) {
            eprintln!("无法发送桌面事件 {name}：{error}");
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;
    use std::sync::{Arc, Mutex};
    use tauri::{Emitter, Listener};

    #[test]
    fn tauri_rejects_dotted_wire_names() {
        let app = tauri::test::mock_app();
        assert!(matches!(
            app.emit("server.event", json!({"type": "message.created"})),
            Err(tauri::Error::IllegalEventName(_))
        ));
    }

    #[test]
    fn desktop_sink_delivers_logical_events_on_valid_wire_channels() {
        let app = tauri::test::mock_app();
        let sink = DesktopEvents(app.handle().clone());
        for (logical_name, wire_name) in [
            ("server.event", "server:event"),
            ("connection.changed", "connection:changed"),
            ("transfer.updated", "transfer:updated"),
            ("chunk.verified", "chunk:verified"),
            ("sharing.changed", "sharing:changed"),
            ("sharing.revoked", "sharing:revoked"),
            ("service.error", "service:error"),
        ] {
            let received = Arc::new(Mutex::new(Vec::new()));
            let observed = received.clone();
            let listener = app.listen(wire_name, move |event| {
                observed
                    .lock()
                    .unwrap()
                    .push(serde_json::from_str::<Value>(event.payload()).unwrap());
            });
            let payload = json!({"type": "message.created", "body": "规范.v2.pdf"});

            sink.emit(logical_name, payload.clone());

            assert_eq!(*received.lock().unwrap(), vec![payload], "{logical_name}");
            app.unlisten(listener);
        }
    }
}
