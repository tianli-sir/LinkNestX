use crate::{
    audit_export::{
        AuditExportManager, AuditFormat, ExportOutcome, ExportRequest, NativeAuditSavePicker,
    },
    auth::{
        session::{AuthManager, Session, User},
        socket::SocketBridge,
    },
    events::EventSink,
    transfer::{
        models::{suggested_name, TaskSnapshot},
        service::TransferService,
    },
    Result, TransferError,
};
use serde_json::Value;
use std::{
    collections::HashSet,
    sync::{atomic::AtomicBool, Arc},
};
use tokio::sync::Mutex;

pub struct NativeState {
    pub audit_exports: Arc<AuditExportManager>,
    pub auth: Arc<AuthManager>,
    pub transfers: Arc<TransferService>,
    pub socket: SocketBridge,
    pub events: Arc<dyn EventSink>,
    pub exiting: AtomicBool,
    pub exit_ready: AtomicBool,
    pub lifecycle: Mutex<()>,
    pub attachments: Mutex<crate::attachments::Registry>,
    pub destinations: Mutex<HashSet<String>>,
}
impl NativeState {
    pub async fn expire_attachments(&self) {
        let scope = self.auth.selection_scope().await.ok();
        let mut registry = self.attachments.lock().await;
        registry.expire(crate::attachments::now());
        if scope.is_none() {
            registry.clear();
        }
    }

    pub async fn stop(&self) -> Result<()> {
        self.attachments.lock().await.clear();
        self.audit_exports.shutdown().await;
        self.socket.stop().await;
        self.transfers.shutdown().await
    }
    pub async fn api_request(
        &self,
        method: &str,
        path: &str,
        body: Option<Value>,
    ) -> Result<Value> {
        let route = path.split('?').next().unwrap_or("").trim_end_matches('/');
        if matches!(
            route,
            "/api/v1/auth/login" | "/api/v1/auth/refresh" | "/api/v1/auth/logout"
        ) {
            return Err(TransferError::new("请使用登录或退出登录操作"));
        }
        if route == "/api/v1/auth/password" && method == "POST" {
            let _guard = self.lifecycle.lock().await;
            let owner = self.auth.owner().await?;
            let result = self
                .auth
                .json_for(&owner, method, path, body.filter(|v| !v.is_null()))
                .await?;
            let user: User = serde_json::from_value(result)?;
            self.auth.update_user(&owner, user.clone()).await?;
            return Ok(serde_json::to_value(user)?);
        }
        self.auth
            .json(method, path, body.filter(|v| !v.is_null()))
            .await
    }
}
#[tauri::command]
pub fn app_version() -> &'static str {
    env!("CARGO_PKG_VERSION")
}
#[tauri::command]
pub async fn login(
    state: tauri::State<'_, NativeState>,
    username: String,
    password: String,
    device_id: Option<String>,
) -> Result<Session> {
    let _guard = state.lifecycle.lock().await;
    state.attachments.lock().await.clear();
    state.audit_exports.cancel_and_wait().await;
    state.socket.stop().await;
    state.transfers.peers.stop().await;
    state.transfers.pause_all().await?;
    let session = state
        .auth
        .login(
            crate::deployment::SERVER_ORIGIN,
            &username,
            &password,
            device_id,
        )
        .await?;
    state.events.emit("session.identity", serde_json::json!({"userId":session.user.id}));
    state
        .socket
        .start(state.auth.clone(), state.events.clone())
        .await;
    state.transfers.restore_after_login().await?;
    Ok(session)
}
#[tauri::command]
pub async fn logout(state: tauri::State<'_, NativeState>) -> Result<()> {
    let _guard = state.lifecycle.lock().await;
    state.audit_exports.cancel_and_wait().await;
    state.socket.stop().await;
    state.transfers.peers.stop().await;
    let paused = state.transfers.pause_all().await;
    state.transfers.set_peer_sources(vec![]).await;
    state.attachments.lock().await.clear();
    state.destinations.lock().await.clear();
    state
        .events
        .emit("sharing.changed", serde_json::json!({"enabled":false}));
    let logout = state.auth.logout().await;
    state.events.emit("session.identity", serde_json::json!({"userId":null}));
    paused?;
    logout
}
#[tauri::command]
pub fn reserve_audit_export(state: tauri::State<'_, NativeState>) -> Result<String> {
    state.audit_exports.reserve()
}
#[tauri::command]
pub async fn export_audit(
    state: tauri::State<'_, NativeState>,
    export_id: String,
    format: AuditFormat,
    action: String,
) -> Result<ExportOutcome> {
    let _activity = state.auth.activity.try_enter()?;
    let request = match ExportRequest::new(format, action) {
        Ok(request) => request,
        Err(error) => {
            state.audit_exports.cancel(&export_id).await;
            return Err(error);
        }
    };
    state
        .audit_exports
        .export_reserved(
            &export_id,
            &state.lifecycle,
            state.auth.clone(),
            request,
            &NativeAuditSavePicker,
        )
        .await
}
#[tauri::command]
pub async fn cancel_audit_export(
    state: tauri::State<'_, NativeState>,
    export_id: String,
) -> Result<()> {
    state.audit_exports.cancel(&export_id).await;
    Ok(())
}
#[tauri::command]
pub async fn api_request(
    state: tauri::State<'_, NativeState>,
    method: String,
    path: String,
    body: Option<Value>,
) -> Result<Value> {
    state.api_request(&method, &path, body).await
}
#[tauri::command]
pub async fn start_download(
    state: tauri::State<'_, NativeState>,
    version_id: String,
    destination: String,
    group_id: Option<String>,
    message_id: Option<String>,
) -> Result<String> {
    if !state.destinations.lock().await.remove(&destination) {
        return Err(TransferError::new("请通过保存文件对话框选择目标位置"));
    }
    state
        .transfers
        .start_download_for_message(version_id, destination, group_id, message_id)
        .await
}
#[tauri::command]
pub async fn pause_transfer(state: tauri::State<'_, NativeState>, task_id: String) -> Result<()> {
    state.transfers.pause(&task_id).await
}
#[tauri::command]
pub async fn resume_transfer(state: tauri::State<'_, NativeState>, task_id: String) -> Result<()> {
    state.transfers.resume(&task_id).await
}
#[tauri::command]
pub async fn list_transfers(state: tauri::State<'_, NativeState>) -> Result<Vec<TaskSnapshot>> {
    state.transfers.list().await
}
#[tauri::command]
pub async fn send_server_event(state: tauri::State<'_, NativeState>, event: Value) -> Result<()> {
    state.socket.send(event).await
}
#[tauri::command]
pub async fn pick_download_destination(
    state: tauri::State<'_, NativeState>,
    name: String,
) -> Result<Option<String>> {
    let file = rfd::AsyncFileDialog::new()
        .set_title("保存文件")
        .set_file_name(suggested_name(&name))
        .save_file()
        .await;
    let path = file.map(|f| f.path().to_string_lossy().into_owned());
    if let Some(ref path) = path {
        state.destinations.lock().await.insert(path.clone());
    }
    Ok(path)
}

#[tauri::command]
pub fn get_background_status<R: tauri::Runtime>(
    app: tauri::AppHandle<R>,
) -> crate::background::BackgroundSnapshot {
    use tauri::Manager;
    app.state::<crate::background::BackgroundState>().snapshot()
}

#[tauri::command]
pub fn client_configuration() -> Value {
    crate::deployment::configuration()
}
#[tauri::command]
pub fn get_connection_status(state: tauri::State<'_, NativeState>) -> Value {
    state.socket.status()
}
#[tauri::command]
pub async fn reconnect_connection(state: tauri::State<'_, NativeState>) -> Result<()> {
    let _guard = state.lifecycle.lock().await;
    state.auth.current().await?;
    state.socket.start(state.auth.clone(), state.events.clone()).await;
    Ok(())
}
#[derive(serde::Serialize, serde::Deserialize)]
#[serde(rename_all = "camelCase", deny_unknown_fields)]
pub struct TransferPreferences {
    pub max_concurrent: usize,
    pub bandwidth_limit_bps: u64,
    pub autostart: bool,
}
#[tauri::command]
pub async fn get_transfer_preferences(
    state: tauri::State<'_, NativeState>,
) -> Result<TransferPreferences> {
    let p = state.transfers.sharing_policy().await;
    Ok(TransferPreferences {
        max_concurrent: p.max_concurrent,
        bandwidth_limit_bps: p.bandwidth_limit_bps,
        autostart: p.autostart,
    })
}
#[tauri::command]
pub async fn set_transfer_preferences(
    app: tauri::AppHandle,
    state: tauri::State<'_, NativeState>,
    preferences: TransferPreferences,
) -> Result<()> {
    let mut p = state.transfers.sharing_policy().await;
    p.max_concurrent = preferences.max_concurrent;
    p.bandwidth_limit_bps = preferences.bandwidth_limit_bps;
    p.autostart = preferences.autostart;
    p.validate()?;
    crate::autostart::set(&app, p.autostart)?;
    state.transfers.set_sharing_policy(p).await
}
fn main_window(window: &tauri::WebviewWindow) -> Result<()> {
    if window.label() != "main" {
        return Err(TransferError::new("此窗口不能读取附件"));
    }
    Ok(())
}
#[tauri::command]
pub async fn pick_upload_files(
    window: tauri::WebviewWindow,
    state: tauri::State<'_, NativeState>,
) -> Result<Vec<crate::attachments::Attachment>> {
    main_window(&window)?;
    let scope = state.auth.selection_scope().await?;
    let files = rfd::AsyncFileDialog::new()
        .set_title("选择附件")
        .pick_files()
        .await
        .unwrap_or_default();
    let _guard = state.lifecycle.lock().await;
    if state.auth.selection_scope().await? != scope {
        return Err(TransferError::new("登录账户已更改，请重新选择"));
    }
    state.attachments.lock().await.prepare_files(
        &scope,
        files.into_iter().map(|f| f.path().to_owned()).collect(),
        crate::attachments::now(),
    )
}
#[tauri::command]
pub async fn read_clipboard_attachments(
    app: tauri::AppHandle,
    window: tauri::WebviewWindow,
    state: tauri::State<'_, NativeState>,
) -> Result<Vec<crate::attachments::Attachment>> {
    main_window(&window)?;
    if !window.is_focused().unwrap_or(false) {
        return Err(TransferError::new("请在当前窗口中粘贴附件"));
    }
    let scope = state.auth.selection_scope().await?;
    let (send, receive) = tokio::sync::oneshot::channel();
    app.run_on_main_thread(move || {
        let _ = send.send(crate::attachments::read_clipboard());
    })
    .map_err(|_| TransferError::new("无法读取剪贴板，请选择文件"))?;
    let content = receive
        .await
        .map_err(|_| TransferError::new("剪贴板读取已取消"))??;
    let _guard = state.lifecycle.lock().await;
    if state.auth.selection_scope().await? != scope {
        return Err(TransferError::new("登录账户已更改，请重新粘贴"));
    }
    let mut registry = state.attachments.lock().await;
    match content {
        crate::attachments::ClipboardContent::Files(paths) => {
            registry.prepare_files(&scope, paths, crate::attachments::now())
        }
        crate::attachments::ClipboardContent::Image(bytes) => {
            registry.prepare_image(&scope, &bytes, crate::attachments::now())
        }
        crate::attachments::ClipboardContent::Empty => Ok(vec![]),
    }
}
#[tauri::command]
pub async fn discard_attachment(
    state: tauri::State<'_, NativeState>,
    source_handle: String,
) -> Result<()> {
    let scope = state.auth.selection_scope().await?;
    state
        .attachments
        .lock()
        .await
        .discard(&scope, &source_handle, crate::attachments::now())
}
#[tauri::command]
pub async fn start_attachment_upload(
    state: tauri::State<'_, NativeState>,
    group_id: String,
    source_handle: String,
    client_attachment_id: String,
    file_id: Option<String>,
) -> Result<String> {
    start_owned_attachment_upload(
        state,
        crate::transfer::models::UploadTarget::Group(group_id),
        source_handle,
        client_attachment_id,
        file_id,
    )
    .await
}
#[tauri::command]
pub async fn start_document_attachment_upload(
    state: tauri::State<'_, NativeState>,
    document_id: String,
    source_handle: String,
    client_attachment_id: String,
    file_id: Option<String>,
) -> Result<String> {
    start_owned_attachment_upload(
        state,
        crate::transfer::models::UploadTarget::Document(document_id),
        source_handle,
        client_attachment_id,
        file_id,
    )
    .await
}
async fn start_owned_attachment_upload(
    state: tauri::State<'_, NativeState>,
    target: crate::transfer::models::UploadTarget,
    source_handle: String,
    client_attachment_id: String,
    file_id: Option<String>,
) -> Result<String> {
    target.validate()?;
    crate::transfer::models::segment(&client_attachment_id)?;
    let _activity = state.auth.activity.try_enter()?;
    let scope = state.auth.selection_scope().await?;
    let (selection, root) = {
        let mut registry = state.attachments.lock().await;
        (
            registry.take(&scope, &source_handle, crate::attachments::now())?,
            registry.root.clone(),
        )
    };
    let name = selection.name.clone();
    let staged = tokio::task::spawn_blocking(move || selection.materialize(&root))
        .await
        .map_err(|_| TransferError::new("附件准备已取消"))??;
    let _guard = state.lifecycle.lock().await;
    if state.auth.selection_scope().await? != scope {
        return Err(TransferError::new("登录账户已更改，请重新选择"));
    }
    let path = staged
        .path()
        .to_str()
        .ok_or_else(|| TransferError::new("附件路径无法识别"))?
        .to_owned();
    let task = state
        .transfers
        .start_upload_to(
            target,
            path,
            file_id,
            Some(client_attachment_id),
            true,
            Some(name),
        )
        .await?;
    staged.commit();
    Ok(task)
}
/// Called only from the trusted OS drag/drop callback, never exposed over IPC.
pub async fn native_drop(
    state: &NativeState,
    scope: String,
    paths: Vec<std::path::PathBuf>,
) -> Result<()> {
    let _guard = state.lifecycle.lock().await;
    if state.auth.selection_scope().await? != scope {
        return Err(TransferError::new("登录账户已更改，请重新拖入文件"));
    }
    let prepared =
        state
            .attachments
            .lock()
            .await
            .prepare_files(&scope, paths, crate::attachments::now())?;
    state
        .events
        .emit("attachments.prepared", serde_json::to_value(prepared)?);
    Ok(())
}
fn avatar_mime(bytes: &[u8]) -> Result<&'static str> {
    if bytes.starts_with(b"\x89PNG\r\n\x1a\n") {
        Ok("image/png")
    } else if bytes.starts_with(&[0xff, 0xd8, 0xff]) {
        Ok("image/jpeg")
    } else {
        Err(TransferError::new("头像仅支持PNG或JPEG图片"))
    }
}
#[tauri::command]
pub async fn set_profile_avatar(
    state: tauri::State<'_, NativeState>,
    source_handle: String,
) -> Result<Value> {
    let _activity = state.auth.activity.try_enter()?;
    let scope = state.auth.selection_scope().await?;
    let selected =
        state
            .attachments
            .lock()
            .await
            .take(&scope, &source_handle, crate::attachments::now())?;
    let bytes = tokio::task::spawn_blocking(move || selected.read_bounded(2 * 1024 * 1024))
        .await
        .map_err(|_| TransferError::new("头像读取已取消"))??;
    avatar_mime(&bytes)?;
    let _guard = state.lifecycle.lock().await;
    if state.auth.selection_scope().await? != scope {
        return Err(TransferError::new("登录账户已更改"));
    }
    let owner = state.auth.owner().await?;
    let response = state
        .auth
        .request_for(&owner, "PUT", "/api/v1/profile/avatar", Some(bytes), false)
        .await?;
    serde_json::from_slice(&crate::auth::session::limited_body(response, 1024 * 1024).await?)
        .map_err(Into::into)
}
#[tauri::command]
pub async fn profile_avatar(
    state: tauri::State<'_, NativeState>,
    user_id: String,
) -> Result<String> {
    use base64::Engine;
    crate::transfer::models::segment(&user_id)?;
    let owner = state.auth.owner().await?;
    let response = state
        .auth
        .request_for(
            &owner,
            "GET",
            &format!("/api/v1/profiles/{user_id}/avatar"),
            None,
            false,
        )
        .await?;
    let data = crate::auth::session::limited_body(response, 2 * 1024 * 1024).await?;
    if state.auth.owner().await? != owner {
        return Err(TransferError::new("登录账户已更改"));
    }
    Ok(format!(
        "data:{};base64,{}",
        avatar_mime(&data)?,
        base64::engine::general_purpose::STANDARD.encode(data)
    ))
}

#[tauri::command]
pub async fn document_attachment_preview(
    state: tauri::State<'_, NativeState>,
    document_id: String,
    version_id: String,
) -> Result<String> {
    use base64::Engine;
    crate::transfer::models::segment(&document_id)?;
    crate::transfer::models::segment(&version_id)?;
    let owner = state.auth.owner().await?;
    let response = state
        .auth
        .request_for(
            &owner,
            "GET",
            &format!("/api/v1/documents/{document_id}/attachments/{version_id}/preview"),
            None,
            false,
        )
        .await?;
    let mime = response
        .headers()
        .get(reqwest::header::CONTENT_TYPE)
        .and_then(|v| v.to_str().ok())
        .unwrap_or("")
        .split(';')
        .next()
        .unwrap_or("")
        .to_owned();
    if !matches!(
        mime.as_str(),
        "image/png" | "image/jpeg" | "image/gif" | "image/webp"
    ) {
        return Err(TransferError::new("此附件不支持图片预览"));
    }
    let bytes = crate::auth::session::limited_body(response, 10 * 1024 * 1024).await?;
    if state.auth.owner().await? != owner {
        return Err(TransferError::new("登录账户已更改"));
    }
    Ok(format!(
        "data:{};base64,{}",
        mime,
        base64::engine::general_purpose::STANDARD.encode(bytes)
    ))
}

#[tauri::command]
pub fn get_update_status(
    updater: tauri::State<'_, Arc<crate::update::UpdateService>>,
) -> crate::update::UpdateStatus {
    updater.status()
}
#[tauri::command]
pub async fn check_for_updates(
    updater: tauri::State<'_, Arc<crate::update::UpdateService>>,
) -> Result<crate::update::UpdateStatus> {
    Ok(updater.check().await)
}
#[tauri::command]
pub fn defer_update(
    updater: tauri::State<'_, Arc<crate::update::UpdateService>>,
) -> crate::update::UpdateStatus {
    updater.defer()
}
#[tauri::command]
pub async fn install_ready_update(
    app: tauri::AppHandle,
    state: tauri::State<'_, NativeState>,
    updater: tauri::State<'_, Arc<crate::update::UpdateService>>,
) -> Result<crate::update::UpdateStatus> {
    if updater.begin_install().await? {
        use std::sync::atomic::Ordering;
        state.exiting.store(true, Ordering::SeqCst);
        // The trusted helper is ready and owns recovery. Always exit after irreversible shutdown.
        if let Err(error) = state.stop().await {
            state
                .events
                .emit("service.error", serde_json::json!({"error":error.error}));
        }
        state.exit_ready.store(true, Ordering::SeqCst);
        app.exit(0);
    }
    Ok(updater.status())
}

#[tauri::command]
pub async fn open_external_url(url: String) -> Result<()> {
    tokio::task::spawn_blocking(move || crate::external::open(&url))
        .await
        .map_err(|_| TransferError::new("链接打开已取消"))?
}
