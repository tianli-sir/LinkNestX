use crate::{
    auth::{
        session::{AuthManager, Session, User},
        socket::SocketBridge,
    },
    events::EventSink,
    transfer::{
        models::{suggested_name, SharingPolicy, TaskSnapshot},
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
    pub auth: Arc<AuthManager>,
    pub transfers: Arc<TransferService>,
    pub socket: SocketBridge,
    pub events: Arc<dyn EventSink>,
    pub exiting: AtomicBool,
    pub exit_ready: AtomicBool,
    pub lifecycle: Mutex<()>,
    pub uploads: Mutex<HashSet<String>>,
    pub destinations: Mutex<HashSet<String>>,
}
impl NativeState {
    pub async fn stop(&self) -> Result<()> {
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
    server_url: String,
    username: String,
    password: String,
    device_id: Option<String>,
) -> Result<Session> {
    let _guard = state.lifecycle.lock().await;
    state.socket.stop().await;
    state.transfers.pause_all().await?;
    let session = state
        .auth
        .login(&server_url, &username, &password, device_id)
        .await?;
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
    state.socket.stop().await;
    let paused = state.transfers.pause_all().await;
    state.transfers.set_peer_sources(vec![]).await;
    state.uploads.lock().await.clear();
    state.destinations.lock().await.clear();
    state
        .events
        .emit("sharing.changed", serde_json::json!({"enabled":false}));
    let logout = state.auth.logout().await;
    paused?;
    logout
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
) -> Result<String> {
    if !state.destinations.lock().await.remove(&destination) {
        return Err(TransferError::new("请通过保存文件对话框选择目标位置"));
    }
    state
        .transfers
        .start_download(version_id, destination)
        .await
}
#[tauri::command]
pub async fn start_upload(
    state: tauri::State<'_, NativeState>,
    group_id: String,
    source: String,
    file_id: Option<String>,
) -> Result<String> {
    if !state.uploads.lock().await.remove(&source) {
        return Err(TransferError::new("请通过文件对话框选择上传文件"));
    }
    state
        .transfers
        .start_upload(group_id, source, file_id)
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
pub async fn get_sharing_policy(state: tauri::State<'_, NativeState>) -> Result<SharingPolicy> {
    Ok(state.transfers.sharing_policy().await)
}
#[tauri::command]
pub async fn set_sharing_policy(
    app: tauri::AppHandle,
    state: tauri::State<'_, NativeState>,
    policy: SharingPolicy,
) -> Result<()> {
    policy.validate()?;
    crate::autostart::set(&app, policy.autostart)?;
    state.transfers.set_sharing_policy(policy).await
}
#[tauri::command]
pub async fn send_server_event(state: tauri::State<'_, NativeState>, event: Value) -> Result<()> {
    state.socket.send(event).await
}
#[tauri::command]
pub async fn pick_upload_file(state: tauri::State<'_, NativeState>) -> Result<Option<String>> {
    let file = rfd::AsyncFileDialog::new()
        .set_title("选择上传文件")
        .pick_file()
        .await;
    let path = file.map(|f| f.path().to_string_lossy().into_owned());
    if let Some(ref path) = path {
        state.uploads.lock().await.insert(path.clone());
    }
    Ok(path)
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
