use crate::{
    attachments::{self, Registry},
    audit_export::{AuditExportManager, AuditFormat, AuditSavePicker, ExportRequest},
    auth::{
        session::{limited_body, AuthManager, User},
        socket::SocketBridge,
    },
    events::EventSink,
    transfer::{
        models::{segment, UploadTarget},
        service::{validate_destination, TransferService},
    },
    Result, TransferError,
};
use base64::Engine;
use serde::Deserialize;
use serde_json::{json, Value};
use std::{
    collections::HashMap,
    path::{Path, PathBuf},
    sync::Arc,
};
use tokio::sync::Mutex;

pub struct Host {
    pub auth: Arc<AuthManager>,
    pub transfers: Arc<TransferService>,
    events: Arc<dyn EventSink>,
    socket: SocketBridge,
    lifecycle: Mutex<()>,
    attachments: Mutex<Registry>,
    destinations: Mutex<HashMap<String, (String, String)>>,
    exports: Arc<AuditExportManager>,
}
impl Host {
    pub fn open(root: &Path, events: Arc<dyn EventSink>) -> Result<Arc<Self>> {
        Self::with_auth(
            root,
            events,
            Arc::new(AuthManager::production(&root.join("auth"))?),
        )
    }
    fn with_auth(
        root: &Path,
        events: Arc<dyn EventSink>,
        auth: Arc<AuthManager>,
    ) -> Result<Arc<Self>> {
        std::fs::create_dir_all(root)?;
        #[cfg(unix)]
        {
            use std::os::unix::fs::PermissionsExt;
            std::fs::set_permissions(root, std::fs::Permissions::from_mode(0o700))?;
        }
        Ok(Arc::new(Self {
            transfers: TransferService::open(root, auth.clone(), events.clone())?,
            auth,
            events,
            socket: SocketBridge::default(),
            lifecycle: Mutex::new(()),
            attachments: Mutex::new(Registry::new(root.join("attachments"))?),
            destinations: Mutex::new(HashMap::new()),
            exports: Arc::new(AuditExportManager::new()?),
        }))
    }
    pub async fn shutdown(&self) -> Result<()> {
        self.attachments.lock().await.clear();
        self.exports.shutdown().await;
        self.socket.stop().await;
        self.transfers.shutdown().await
    }
    async fn assert_scope(&self, expected: &str) -> Result<()> {
        if self.auth.selection_scope().await? != expected {
            return Err(TransferError::new("登录账户已更改，请重新选择"));
        }
        Ok(())
    }
    pub async fn dispatch(&self, command: &str, args: Value) -> Result<Value> {
        match command {
            "client_configuration" => Ok(
                json!({"serverOrigin":crate::deployment::SERVER_ORIGIN,"version":env!("CARGO_PKG_VERSION"),"platform":"windows-legacy","capabilities":{"fileUpload":true,"nativeTransfers":true,"clipboardAttachments":true,"avatarEdit":true,"updates":false,"p2p":true}}),
            ),
            "app_version" => Ok(json!(env!("CARGO_PKG_VERSION"))),
            "login" => {
                let a: Login = decode(args)?;
                if a.username.len() > 256 || a.password.len() > 1024 {
                    return Err(TransferError::new("账号或密码过长"));
                }
                let _guard = self.lifecycle.lock().await;
                self.attachments.lock().await.clear();
                self.destinations.lock().await.clear();
                self.exports.cancel_and_wait().await;
                self.socket.stop().await;
                self.transfers.peers.stop().await;
                self.transfers.pause_all().await?;
                let session = self
                    .auth
                    .login(
                        crate::deployment::SERVER_ORIGIN,
                        &a.username,
                        &a.password,
                        a.device_id,
                    )
                    .await?;
                self.events
                    .emit("session.identity", json!({"userId":session.user.id}));
                self.socket
                    .start(self.auth.clone(), self.events.clone())
                    .await;
                self.transfers.restore_after_login().await?;
                Ok(
                    json!({"id":session.id,"user":session.user,"deviceId":session.device_id,"expiresAt":session.expires_at}),
                )
            }
            "logout" => {
                let _guard = self.lifecycle.lock().await;
                self.attachments.lock().await.clear();
                self.destinations.lock().await.clear();
                self.exports.cancel_and_wait().await;
                self.socket.stop().await;
                self.transfers.peers.stop().await;
                let paused = self.transfers.pause_all().await;
                self.transfers.set_peer_sources(vec![]).await;
                let logout = self.auth.logout().await;
                self.events.emit("session.identity", json!({"userId":null}));
                paused?;
                logout?;
                Ok(Value::Null)
            }
            "session_restore" => {
                let _guard = self.lifecycle.lock().await;
                let session = self.auth.restore().await?;
                self.events
                    .emit("session.identity", json!({"userId":session.user.id}));
                self.socket
                    .start(self.auth.clone(), self.events.clone())
                    .await;
                self.transfers.restore_after_login().await?;
                // Same stripped shape as "login": tokens never reach the renderer.
                Ok(
                    json!({"id":session.id,"user":session.user,"deviceId":session.device_id,"expiresAt":session.expires_at}),
                )
            }
            "api_request" => {
                let a: ApiRequest = decode(args)?;
                if !matches!(
                    a.method.as_str(),
                    "GET" | "POST" | "PUT" | "PATCH" | "DELETE"
                ) {
                    return Err(TransferError::new("请求方法无效"));
                }
                let route = a.path.split('?').next().unwrap_or("").trim_end_matches('/');
                if matches!(
                    route,
                    "/api/v1/auth/login" | "/api/v1/auth/refresh" | "/api/v1/auth/logout"
                ) {
                    return Err(TransferError::new("请使用登录或退出登录操作"));
                }
                if route == "/api/v1/auth/password" && a.method == "POST" {
                    let _guard = self.lifecycle.lock().await;
                    let owner = self.auth.owner().await?;
                    let value = self
                        .auth
                        .json_for(&owner, &a.method, &a.path, a.body)
                        .await?;
                    let user: User = serde_json::from_value(value.clone())?;
                    self.auth.update_user(&owner, user).await?;
                    return Ok(value);
                }
                self.auth.json(&a.method, &a.path, a.body).await
            }
            "send_server_event" => {
                let a: SendEvent = decode(args)?;
                self.socket.send(a.event).await?;
                Ok(Value::Null)
            }
            "list_transfers" => Ok(serde_json::to_value(self.transfers.list().await?)?),
            "pause_transfer" | "resume_transfer" => {
                let a: TaskId = decode(args)?;
                if command == "pause_transfer" {
                    self.transfers.pause(&a.task_id).await?;
                } else {
                    self.transfers.resume(&a.task_id).await?;
                }
                Ok(Value::Null)
            }
            "selection_scope" => Ok(json!(self.auth.selection_scope().await?)),
            // Reliable main-process snapshot for recovery after the event pipe
            // overflows. Keep it off the renderer allowlist and omit credentials.
            "session_identity" => {
                let _guard = self.lifecycle.lock().await;
                Ok(match self.auth.current().await {
                    Ok(current) => json!({
                        "userId":current.session.user.id,
                        "scope":format!("{}:{}", current.owner(), current.session.id)
                    }),
                    Err(_) => json!({"userId":null,"scope":null}),
                })
            }
            // The following three commands are private to the shell main process. They are
            // deliberately absent from the preload/renderer command allowlist.
            "import_files" => {
                let a: ImportFiles = decode(args)?;
                let _guard = self.lifecycle.lock().await;
                self.assert_scope(&a.scope).await?;
                Ok(serde_json::to_value(
                    self.attachments.lock().await.prepare_files(
                        &a.scope,
                        a.paths,
                        attachments::now(),
                    )?,
                )?)
            }
            "import_clipboard_files" => {
                let a: Scope = decode(args)?;
                let _guard = self.lifecycle.lock().await;
                self.assert_scope(&a.scope).await?;
                let paths = tokio::task::spawn_blocking(crate::clipboard::files)
                    .await
                    .map_err(|_| TransferError::new("剪贴板读取已取消"))??;
                Ok(serde_json::to_value(
                    self.attachments.lock().await.prepare_files(
                        &a.scope,
                        paths,
                        attachments::now(),
                    )?,
                )?)
            }
            "import_clipboard_image" => {
                let a: ImportImage = decode(args)?;
                let png = base64::engine::general_purpose::STANDARD
                    .decode(a.png)
                    .map_err(|_| TransferError::new("剪贴板图片无效"))?;
                let _guard = self.lifecycle.lock().await;
                self.assert_scope(&a.scope).await?;
                Ok(serde_json::to_value(
                    self.attachments.lock().await.prepare_image(
                        &a.scope,
                        &png,
                        attachments::now(),
                    )?,
                )?)
            }
            "authorize_destination" => {
                let a: Destination = decode(args)?;
                let _guard = self.lifecycle.lock().await;
                self.assert_scope(&a.scope).await?;
                let path = validate_destination(&a.path)?;
                let id = uuid::Uuid::new_v4().to_string();
                let mut destinations = self.destinations.lock().await;
                if destinations.len() >= 100 {
                    return Err(TransferError::new("待保存任务过多"));
                }
                destinations.insert(id.clone(), (a.scope, path));
                Ok(json!(id))
            }
            "start_download" => {
                let a: Download = decode(args)?;
                let _guard = self.lifecycle.lock().await;
                let scope = self.auth.selection_scope().await?;
                let mut destinations = self.destinations.lock().await;
                if !destinations
                    .get(&a.destination)
                    .is_some_and(|(owner, _)| owner == &scope)
                {
                    return Err(TransferError::new("请通过保存对话框选择目标位置"));
                }
                let (_, path) = destinations.remove(&a.destination).unwrap();
                drop(destinations);
                Ok(json!(
                    self.transfers
                        .start_download_for_message(a.version_id, path, a.group_id, a.message_id)
                        .await?
                ))
            }
            "discard_attachment" => {
                let a: Handle = decode(args)?;
                let scope = self.auth.selection_scope().await?;
                self.attachments.lock().await.discard(
                    &scope,
                    &a.source_handle,
                    attachments::now(),
                )?;
                Ok(Value::Null)
            }
            "start_attachment_upload" | "start_document_attachment_upload" => {
                let a: Upload = decode(args)?;
                let target = match (command, a.group_id, a.document_id) {
                    ("start_attachment_upload", Some(id), None) => UploadTarget::Group(id),
                    ("start_document_attachment_upload", None, Some(id)) => {
                        UploadTarget::Document(id)
                    }
                    _ => return Err(TransferError::new("上传位置无效")),
                };
                target.validate()?;
                segment(&a.client_attachment_id)?;
                let _activity = self.auth.activity.try_enter()?;
                let scope = self.auth.selection_scope().await?;
                let (selection, root) = {
                    let mut r = self.attachments.lock().await;
                    (
                        r.take(&scope, &a.source_handle, attachments::now())?,
                        r.root.clone(),
                    )
                };
                let name = selection.name.clone();
                let staged = tokio::task::spawn_blocking(move || selection.materialize(&root))
                    .await
                    .map_err(|_| TransferError::new("附件准备中断"))??;
                let _guard = self.lifecycle.lock().await;
                self.assert_scope(&scope).await?;
                let path = staged
                    .path()
                    .to_str()
                    .ok_or_else(|| TransferError::new("附件路径无法识别"))?
                    .to_owned();
                let task = self
                    .transfers
                    .start_upload_to(
                        target,
                        path,
                        a.file_id,
                        Some(a.client_attachment_id),
                        true,
                        Some(name),
                    )
                    .await?;
                staged.commit();
                Ok(json!(task))
            }
            "get_transfer_preferences" => {
                let p = self.transfers.sharing_policy().await;
                Ok(
                    json!({"maxConcurrent":p.max_concurrent,"bandwidthLimitBps":p.bandwidth_limit_bps,"autostart":p.autostart}),
                )
            }
            "set_transfer_preferences" => {
                let a: PreferencesRequest = decode(args)?;
                let mut p = self.transfers.sharing_policy().await;
                p.max_concurrent = a.preferences.max_concurrent;
                p.bandwidth_limit_bps = a.preferences.bandwidth_limit_bps;
                p.autostart = a.preferences.autostart;
                p.validate()?;
                self.transfers.set_sharing_policy(p).await?;
                Ok(Value::Null)
            }
            "get_connection_status" => Ok(self.socket.status()),
            "reconnect_connection" => {
                let _guard = self.lifecycle.lock().await;
                self.auth.current().await?;
                self.socket
                    .start(self.auth.clone(), self.events.clone())
                    .await;
                Ok(Value::Null)
            }
            "get_background_status" => Ok(
                json!({"sharingEnabled":true,"activeUploads":self.transfers.peers.active_uploads(),"error":null,"lastCompletion":null}),
            ),
            "reserve_audit_export" => Ok(json!(self.exports.reserve()?)),
            "cancel_audit_export" => {
                let a: ExportId = decode(args)?;
                self.exports.cancel(&a.export_id).await;
                Ok(Value::Null)
            }
            "export_audit" => {
                let a: Export = decode(args)?;
                let result = async {
                    self.assert_scope(&a.scope).await?;
                    let _activity = self.auth.activity.try_enter()?;
                    let request = ExportRequest::new(a.format, a.action)?;
                    let picker = ShellSavePicker(a.destination.map(PathBuf::from));
                    Ok(serde_json::to_value(
                        self.exports
                            .export_reserved(
                                &a.export_id,
                                &self.lifecycle,
                                self.auth.clone(),
                                request,
                                &picker,
                            )
                            .await?,
                    )?)
                }
                .await;
                if result.is_err() {
                    self.exports.cancel(&a.export_id).await;
                }
                result
            }
            "set_profile_avatar" => {
                let a: Handle = decode(args)?;
                let _activity = self.auth.activity.try_enter()?;
                let scope = self.auth.selection_scope().await?;
                let selection = self.attachments.lock().await.take(
                    &scope,
                    &a.source_handle,
                    attachments::now(),
                )?;
                let bytes =
                    tokio::task::spawn_blocking(move || selection.read_bounded(2 * 1024 * 1024))
                        .await
                        .map_err(|_| TransferError::new("图片读取已取消"))??;
                avatar_mime(&bytes)?;
                let _guard = self.lifecycle.lock().await;
                self.assert_scope(&scope).await?;
                let owner = self.auth.owner().await?;
                let response = self
                    .auth
                    .request_for(&owner, "PUT", "/api/v1/profile/avatar", Some(bytes), false)
                    .await?;
                Ok(serde_json::from_slice(
                    &limited_body(response, 1024 * 1024).await?,
                )?)
            }
            "profile_avatar" => {
                let a: UserId = decode(args)?;
                segment(&a.user_id)?;
                let owner = self.auth.owner().await?;
                let response = self
                    .auth
                    .request_for(
                        &owner,
                        "GET",
                        &format!("/api/v1/profiles/{}/avatar", a.user_id),
                        None,
                        false,
                    )
                    .await?;
                let bytes = limited_body(response, 2 * 1024 * 1024).await?;
                if self.auth.owner().await? != owner {
                    return Err(TransferError::new("登录账户已更改"));
                }
                Ok(json!(format!(
                    "data:{};base64,{}",
                    avatar_mime(&bytes)?,
                    base64::engine::general_purpose::STANDARD.encode(bytes)
                )))
            }
            "document_attachment_preview" => {
                let a: Preview = decode(args)?;
                segment(&a.document_id)?;
                segment(&a.version_id)?;
                let owner = self.auth.owner().await?;
                let response = self
                    .auth
                    .request_for(
                        &owner,
                        "GET",
                        &format!(
                            "/api/v1/documents/{}/attachments/{}/preview",
                            a.document_id, a.version_id
                        ),
                        None,
                        false,
                    )
                    .await?;
                let bytes = limited_body(response, 10 * 1024 * 1024).await?;
                if self.auth.owner().await? != owner {
                    return Err(TransferError::new("登录账户已更改"));
                }
                Ok(json!(format!(
                    "data:{};base64,{}",
                    preview_mime(&bytes)?,
                    base64::engine::general_purpose::STANDARD.encode(bytes)
                )))
            }
            "file_attachment_preview" | "file_attachment_thumb" => {
                let a: VersionOnly = decode(args)?;
                segment(&a.version_id)?;
                let limit = if command == "file_attachment_preview" {
                    // The legacy protocol caps any response at 72 MiB and
                    // base64 inflates by 4/3, so anything above ~50 MiB of
                    // image bytes would fail the frame limit with an opaque
                    // error; reject earlier with a clear message instead.
                    50 * 1024 * 1024
                } else {
                    20 * 1024 * 1024
                };
                let tail = if command == "file_attachment_preview" {
                    "preview"
                } else {
                    "thumb"
                };
                let owner = self.auth.owner().await?;
                let response = self
                    .auth
                    .request_for(
                        &owner,
                        "GET",
                        &format!("/api/v1/files/{}/{}", a.version_id, tail),
                        None,
                        false,
                    )
                    .await?;
                let bytes = match limited_body(response, limit).await {
                    Ok(bytes) => bytes,
                    Err(_) => {
                        return Err(TransferError::new("图片过大，请下载后查看"));
                    }
                };
                if self.auth.owner().await? != owner {
                    return Err(TransferError::new("登录账户已更改"));
                }
                let mime = preview_mime(&bytes)?;
                Ok(json!(format!(
                    "data:{};base64,{}",
                    mime,
                    base64::engine::general_purpose::STANDARD.encode(bytes)
                )))
            }
            // Path resolution happens in the shared transfer module; the shell
            // main process receives the path and performs the OS launch itself.
            // The renderer never sees the path.
            "open_downloaded_file" | "reveal_downloaded_file" => {
                let a: TaskId = decode(args)?;
                let owner = self.auth.owner().await?;
                let path = self.transfers.download_task_path(&owner, &a.task_id)?;
                let exists = tokio::task::spawn_blocking({
                    let path = path.clone();
                    move || std::path::Path::new(&path).symlink_metadata().is_ok()
                })
                .await
                .map_err(|_| TransferError::new("打开操作已取消"))?;
                if !exists {
                    return Err(TransferError::new("文件已不在原位置"));
                }
                Ok(json!({"path": path}))
            }
            "check_downloaded_files" => {
                let a: TaskIds = decode(args)?;
                if a.task_ids.len() > 200 {
                    return Err(TransferError::new("检查任务过多"));
                }
                let owner = self.auth.owner().await?;
                let mut result = serde_json::Map::new();
                for id in a.task_ids {
                    let exists = self
                        .transfers
                        .download_task_path(&owner, &id)
                        .ok()
                        .filter(|path| std::path::Path::new(path).symlink_metadata().is_ok())
                        .is_some();
                    result.insert(id, serde_json::Value::Bool(exists));
                }
                Ok(serde_json::Value::Object(result))
            }
            "get_update_status" | "check_for_updates" | "defer_update" => Ok(
                json!({"phase":"unsupported","message":"Windows 7 兼容版请通过管理员提供的完整安装包手动更新。"}),
            ),
            "install_ready_update" => Err(TransferError::new(
                "兼容版需要手动安装完整更新包，请先完成传输后退出应用。",
            )),
            _ => Err(TransferError::new("不支持的桌面操作")),
        }
    }
}
fn decode<T: serde::de::DeserializeOwned>(v: Value) -> Result<T> {
    Ok(serde_json::from_value(v)?)
}
fn avatar_mime(bytes: &[u8]) -> Result<&'static str> {
    if bytes.starts_with(b"\x89PNG\r\n\x1a\n") {
        Ok("image/png")
    } else if bytes.starts_with(&[255, 216, 255]) {
        Ok("image/jpeg")
    } else {
        Err(TransferError::new("头像仅支持PNG或JPEG"))
    }
}
fn preview_mime(bytes: &[u8]) -> Result<&'static str> {
    avatar_mime(bytes).or_else(|_| {
        if bytes.starts_with(b"GIF87a") || bytes.starts_with(b"GIF89a") {
            Ok("image/gif")
        } else if bytes.starts_with(b"RIFF") && bytes.get(8..12) == Some(b"WEBP") {
            Ok("image/webp")
        } else {
            Err(TransferError::new("图片格式不可预览"))
        }
    })
}
macro_rules! args {($name:ident {$($field:ident:$ty:ty),*$(,)?})=>{#[derive(Deserialize)]#[serde(rename_all="camelCase",deny_unknown_fields)]struct $name{$($field:$ty),*}}}
args!(Login{username:String,password:String,device_id:Option<String>});
args!(ApiRequest{method:String,path:String,body:Option<Value>});
args!(SendEvent { event: Value });
args!(TaskId { task_id: String });
args!(ImportFiles{scope:String,paths:Vec<PathBuf>});
args!(Scope { scope: String });
args!(ImportImage {
    scope: String,
    png: String
});
args!(Destination {
    scope: String,
    path: String
});
args!(Download{version_id:String,destination:String,group_id:Option<String>,message_id:Option<String>});
args!(Handle {
    source_handle: String
});
args!(Upload{group_id:Option<String>,document_id:Option<String>,source_handle:String,client_attachment_id:String,file_id:Option<String>});
args!(Preferences {
    max_concurrent: usize,
    bandwidth_limit_bps: u64,
    autostart: bool
});
args!(PreferencesRequest {
    preferences: Preferences
});
args!(ExportId { export_id: String });
args!(Export{export_id:String,format:AuditFormat,action:String,scope:String,destination:Option<String>});
args!(UserId { user_id: String });
args!(Preview {
    document_id: String,
    version_id: String
});
args!(VersionOnly {
    version_id: String
});
args!(TaskIds { task_ids: Vec<String> });
struct ShellSavePicker(Option<PathBuf>);
#[async_trait::async_trait]
impl AuditSavePicker for ShellSavePicker {
    async fn pick(&self, _: AuditFormat) -> Result<Option<PathBuf>> {
        Ok(self.0.clone())
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    #[tokio::test]
    async fn native_boundary_refuses_unknown_commands_paths_and_session_tokens() {
        let root = tempfile::tempdir().unwrap();
        let host = Host::open(root.path(), Arc::new(crate::events::NoopEvents)).unwrap();
        assert_eq!(
            host.dispatch("session_identity", Value::Null)
                .await
                .unwrap(),
            json!({"userId":null,"scope":null})
        );
        assert!(host
            .dispatch("read_file", json!({"path":"/etc/passwd"}))
            .await
            .is_err());
        assert!(host
            .dispatch(
                "login",
                json!({"username":"a","password":"b","serverUrl":"https://evil.test"})
            )
            .await
            .is_err());
        assert!(host
            .dispatch(
                "api_request",
                json!({"method":"POST","path":"/api/v1/auth/refresh","body":{}})
            )
            .await
            .is_err());
        assert!(host
            .dispatch(
                "start_download",
                json!({"versionId":"id","destination":"/tmp/not-chosen"})
            )
            .await
            .is_err());
        assert_eq!(
            host.dispatch("client_configuration", Value::Null)
                .await
                .unwrap()["capabilities"]["updates"],
            false
        );
        host.shutdown().await.unwrap();
    }
    #[tokio::test]
    async fn session_restore_without_a_persisted_login_fails_closed() {
        let root = tempfile::tempdir().unwrap();
        let host = Host::open(root.path(), Arc::new(crate::events::NoopEvents)).unwrap();
        assert!(host.dispatch("session_restore", json!({})).await.is_err());
    }
    #[tokio::test]
    async fn file_launch_commands_require_a_logged_in_owner() {
        let root = tempfile::tempdir().unwrap();
        let host = Host::open(root.path(), Arc::new(crate::events::NoopEvents)).unwrap();
        for command in [
            "open_downloaded_file",
            "reveal_downloaded_file",
            "check_downloaded_files",
            "file_attachment_preview",
            "file_attachment_thumb",
        ] {
            assert!(host
                .dispatch(command, json!({"taskId":"t","taskIds":["t"],"versionId":"v"}))
                .await
                .is_err());
        }
    }
    #[test]
    fn attachment_previews_reject_active_content() {
        assert!(preview_mime(b"<svg onload=alert(1)>").is_err());
        assert_eq!(preview_mime(b"RIFF1234WEBPrest").unwrap(), "image/webp");
    }
    #[tokio::test]
    async fn file_authorizations_expire_with_the_login_that_created_them() {
        use axum::{routing::post, Json, Router};
        let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
        let origin = format!("http://{}", listener.local_addr().unwrap());
        let router = Router::new()
            .route("/api/v1/auth/login", post(|Json(body): Json<Value>| async move {
                let username = body["username"].as_str().unwrap();
                Json(json!({"id":format!("session-{username}"),"accessToken":"native-only-access","refreshToken":"native-only-refresh","deviceId":"device","expiresAt":"2099-01-01T00:00:00Z","user":{"id":username,"username":username,"roles":[],"disabled":false,"mustChangePassword":false}}))
            }))
            .route("/api/v1/auth/logout", post(|| async { axum::http::StatusCode::NO_CONTENT }));
        let server = tokio::spawn(async move {
            axum::serve(listener, router).await.unwrap();
        });
        let root = tempfile::tempdir().unwrap();
        let auth = Arc::new(AuthManager::new(&root.path().join("auth")).unwrap());
        let host = Host::with_auth(
            root.path(),
            Arc::new(crate::events::NoopEvents),
            auth.clone(),
        )
        .unwrap();
        auth.login(&origin, "first", "password", None)
            .await
            .unwrap();
        let scope = host.dispatch("selection_scope", Value::Null).await.unwrap();
        let identity = host
            .dispatch("session_identity", Value::Null)
            .await
            .unwrap();
        assert_eq!(identity, json!({"scope":scope,"userId":"first"}));
        assert!(!identity.to_string().contains("native-only"));
        let source = root.path().join("attachment.txt");
        std::fs::write(&source, b"content").unwrap();
        let selected = host
            .dispatch("import_files", json!({"scope":scope,"paths":[source]}))
            .await
            .unwrap();
        assert_eq!(selected.as_array().unwrap().len(), 1);
        let destination = root.path().join("download.bin");
        let token = host
            .dispatch(
                "authorize_destination",
                json!({"scope":scope,"path":destination}),
            )
            .await
            .unwrap();
        host.dispatch("logout", Value::Null).await.unwrap();
        assert_eq!(
            host.dispatch("session_identity", Value::Null)
                .await
                .unwrap(),
            json!({"userId":null,"scope":null})
        );
        auth.login(&origin, "second", "password", None)
            .await
            .unwrap();
        let identity = host
            .dispatch("session_identity", Value::Null)
            .await
            .unwrap();
        assert_eq!(identity["userId"], "second");
        assert_ne!(identity["scope"], scope);
        assert!(host
            .dispatch("import_files", json!({"scope":scope,"paths":[source]}))
            .await
            .is_err());
        assert!(host
            .dispatch("import_clipboard_files", json!({"scope":scope}))
            .await
            .is_err());
        assert!(host
            .dispatch(
                "start_download",
                json!({"versionId":"v1","destination":token})
            )
            .await
            .is_err());
        assert!(!destination.exists());
        host.shutdown().await.unwrap();
        server.abort();
    }
}
