use super::secrets;
use crate::{transfer::models::hash, Result, TransferError};
use futures_util::StreamExt;
use reqwest::{Client, Method, Response, Url};
use serde::{Deserialize, Serialize};
use serde_json::{json, Value};
use std::{
    path::{Path, PathBuf},
    time::Duration,
};
use tokio::sync::{Mutex, RwLock};

#[derive(Clone, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct Session {
    pub id: String,
    pub access_token: String,
    pub refresh_token: String,
    pub expires_at: String,
    pub user: User,
    pub device_id: String,
}
#[derive(Clone, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct User {
    pub id: String,
    pub username: String,
    #[serde(default)]
    pub account_number: String,
    #[serde(default)]
    pub display_name: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub avatar_url: Option<String>,
    #[serde(default)]
    pub email: String,
    pub roles: Vec<String>,
    pub disabled: bool,
    pub must_change_password: bool,
}
#[derive(Clone)]
pub struct NativeSession {
    pub origin: String,
    pub session: Session,
}
impl NativeSession {
    pub fn owner(&self) -> String {
        hash(format!("{}\0{}", self.origin, self.session.user.id).as_bytes())
    }
}

#[derive(Serialize, Deserialize)]
struct StoredSession {
    origin: String,
    session: Session,
}
/// The most recently used session file wins when several accounts are stored.
fn latest_session_file(root: &Path) -> Option<PathBuf> {
    let mut best: Option<(std::time::SystemTime, PathBuf)> = None;
    for entry in std::fs::read_dir(root).ok()?.flatten() {
        let Some(name) = entry.file_name().to_str().map(str::to_owned) else {
            continue;
        };
        if !(name.starts_with("session-") && name.ends_with(".json")) {
            continue;
        }
        let Ok(modified) = entry.metadata().and_then(|meta| meta.modified()) else {
            continue;
        };
        if best
            .as_ref()
            .is_none_or(|(time, _)| modified >= *time)
        {
            best = Some((modified, entry.path()));
        }
    }
    best.map(|(_, path)| path)
}
pub fn normalize_origin(value: &str) -> Result<String> {
    let text = value.trim();
    let with_scheme = if text.contains("://") {
        text.to_string()
    } else {
        format!("https://{text}")
    };
    let url = Url::parse(&with_scheme).map_err(|_| TransferError::new("服务器地址无效"))?;
    let loopback = matches!(url.host_str(), Some("localhost" | "127.0.0.1" | "[::1]"));
    let private_ipv4 = url
        .host_str()
        .and_then(|host| host.parse::<std::net::Ipv4Addr>().ok())
        .is_some_and(|address| address.is_private());
    if !(url.scheme() == "https" || url.scheme() == "http" && (loopback || private_ipv4))
        || url.host_str().is_none()
        || !url.username().is_empty()
        || url.password().is_some()
        || url.path() != "/"
        || url.query().is_some()
        || url.fragment().is_some()
    {
        return Err(TransferError::new(
            "公网地址需使用 HTTPS；局域网私有 IPv4 或本机地址可使用 HTTP",
        ));
    }
    Ok(url.origin().ascii_serialization())
}
pub fn api_url(origin: &str, path: &str) -> Result<Url> {
    let pathname = path.split('?').next().unwrap_or("");
    if !path.starts_with("/api/v1/")
        || path.contains('#')
        || path.chars().any(|c| c.is_control())
        || pathname.contains(['%', '\\'])
        || pathname.split('/').any(|part| part == "." || part == "..")
        || pathname.contains("//")
    {
        return Err(TransferError::new("API 请求路径无效"));
    }
    let url =
        Url::parse(&format!("{origin}{path}")).map_err(|_| TransferError::new("API 地址无效"))?;
    if url.origin().ascii_serialization() != origin || !url.path().starts_with("/api/v1/") {
        return Err(TransferError::new("API 请求必须发送到当前服务器"));
    }
    Ok(url)
}
pub struct AuthManager {
    pub activity: std::sync::Arc<crate::activity::ActivityGate>,
    fixed_origin: Option<&'static str>,
    client: Client,
    state: RwLock<Option<NativeSession>>,
    refresh: Mutex<()>,
    root: PathBuf,
}
impl AuthManager {
    pub fn new(root: &Path) -> Result<Self> {
        std::fs::create_dir_all(root)?;
        let client = Client::builder()
            .redirect(reqwest::redirect::Policy::none())
            .connect_timeout(Duration::from_secs(10))
            .timeout(Duration::from_secs(90))
            .build()?;
        Ok(Self {
            fixed_origin: None,
            activity: Default::default(),
            client,
            state: RwLock::new(None),
            refresh: Mutex::new(()),
            root: root.into(),
        })
    }
    pub fn production(root: &Path) -> Result<Self> {
        let mut auth = Self::new(root)?;
        auth.fixed_origin = Some(crate::deployment::SERVER_ORIGIN);
        Ok(auth)
    }
    pub(crate) fn login_origin(&self, requested: &str) -> Result<String> {
        normalize_origin(self.fixed_origin.unwrap_or(requested))
    }
    pub fn selection_scope_now(&self) -> Option<String> {
        self.state
            .try_read()
            .ok()?
            .as_ref()
            .map(|s| format!("{}:{}", s.owner(), s.session.id))
    }
    pub async fn selection_scope(&self) -> Result<String> {
        let s = self.current().await?;
        Ok(format!("{}:{}", s.owner(), s.session.id))
    }
    pub async fn current(&self) -> Result<NativeSession> {
        self.state
            .read()
            .await
            .clone()
            .ok_or_else(|| TransferError::new("请先登录"))
    }
    pub async fn owner(&self) -> Result<String> {
        Ok(self.current().await?.owner())
    }
    fn session_file(&self, owner: &str) -> PathBuf {
        self.root.join(format!("session-{owner}.json"))
    }
    /// Atomic write (temp file + fsync + persist) of the encrypted session,
    /// owner-scoped like the device file. Unix also pins 0600.
    fn persist_session(&self, current: &NativeSession) -> Result<()> {
        let payload = serde_json::to_vec(&StoredSession {
            origin: current.origin.clone(),
            session: current.session.clone(),
        })?;
        let sealed = secrets::protect(&payload)?;
        let path = self.session_file(&current.owner());
        let mut file = tempfile::NamedTempFile::new_in(&self.root)?;
        use std::io::Write;
        file.write_all(&sealed)?;
        file.as_file().sync_all()?;
        file.persist(&path)
            .map_err(|_| TransferError::new("无法保存登录状态"))?;
        #[cfg(unix)]
        {
            use std::os::unix::fs::PermissionsExt;
            let _ = std::fs::set_permissions(&path, std::fs::Permissions::from_mode(0o600));
        }
        Ok(())
    }
    /// Reads, validates and re-authenticates the newest stored login. Any
    /// rejection removes the file so the next start goes straight to the
    /// login screen instead of retrying dead credentials.
    pub async fn restore(&self) -> Result<Session> {
        let path = latest_session_file(&self.root)
            .ok_or_else(|| TransferError::new("没有可恢复的登录"))?;
        let stored: StoredSession = match std::fs::read(&path)
            .map_err(TransferError::from)
            .and_then(|sealed| secrets::unprotect(&sealed))
            .and_then(|plain| serde_json::from_slice(&plain).map_err(TransferError::from))
        {
            Ok(value) => value,
            Err(_) => {
                let _ = std::fs::remove_file(&path);
                return Err(TransferError::new("登录状态已失效"));
            }
        };
        if stored.session.user.id.is_empty()
            || stored.session.access_token.is_empty()
            || stored.session.refresh_token.is_empty()
            || stored.session.user.disabled
        {
            let _ = std::fs::remove_file(&path);
            return Err(TransferError::new("登录状态已失效"));
        }
        let origin = self.login_origin(&stored.origin)?;
        let _guard = self.refresh.lock().await;
        let candidate = NativeSession {
            origin,
            session: stored.session,
        };
        // rotated() validates identity/device against the server and persists
        // the refreshed tokens on success.
        match self.rotated(&candidate).await {
            Ok(current) => {
                *self.state.write().await = Some(current.clone());
                Ok(current.session)
            }
            Err(error) => {
                let _ = std::fs::remove_file(&path);
                Err(error)
            }
        }
    }
    pub async fn login(
        &self,
        origin: &str,
        username: &str,
        password: &str,
        device: Option<String>,
    ) -> Result<Session> {
        let origin = self.login_origin(origin)?;
        let _guard = self.refresh.lock().await;
        let device_path = self.root.join(format!(
            "device-{}.txt",
            hash(format!("{origin}\0{}", username.trim()).as_bytes())
        ));
        let device = std::fs::read_to_string(&device_path).ok().or(device);
        let response=self.client.post(api_url(&origin,"/api/v1/auth/login")?).json(&json!({"username":username.trim(),"password":password,"deviceId":device.unwrap_or_default()})).send().await?;
        let session: Session = serde_json::from_slice(&limited_body(response, 1_048_576).await?)?;
        if session.user.id.is_empty()
            || session.access_token.is_empty()
            || session.refresh_token.is_empty()
            || session.user.disabled
        {
            return Err(TransferError::new("服务器登录响应无效"));
        }
        let mut device_file = tempfile::NamedTempFile::new_in(&self.root)?;
        use std::io::Write;
        device_file.write_all(session.device_id.as_bytes())?;
        device_file.as_file().sync_all()?;
        device_file
            .persist(&device_path)
            .map_err(|_| TransferError::new("无法保存设备标识"))?;
        let native = NativeSession {
            origin,
            session: session.clone(),
        };
        self.persist_session(&native)?;
        *self.state.write().await = Some(native);
        Ok(session)
    }
    pub async fn logout(&self) -> Result<()> {
        let _guard = match tokio::time::timeout(Duration::from_secs(10), self.refresh.lock()).await
        {
            Ok(guard) => guard,
            Err(_) => {
                self.state.write().await.take();
                return Err(TransferError::new("本地已退出，服务器会话注销超时"));
            }
        };
        let old = self.state.write().await.take();
        let owner = old.as_ref().map(|native| native.owner());
        if let Some(old) = old {
            tokio::time::timeout(Duration::from_secs(10), async {
                let mut current = old;
                for attempt in 0..2 {
                    let response = self
                        .client
                        .post(api_url(&current.origin, "/api/v1/auth/logout")?)
                        .bearer_auth(&current.session.access_token)
                        .send()
                        .await?;
                    if response.status().as_u16() == 401 && attempt == 0 {
                        current = self.rotated(&current).await?;
                        continue;
                    }
                    if !response.status().is_success() {
                        return Err(TransferError::http(response.status().as_u16()));
                    }
                    return Ok(());
                }
                Err(TransferError::new("无法注销服务器会话"))
            })
            .await
            .map_err(|_| TransferError::new("本地已退出，服务器会话注销超时"))??;
        }
        // Rotation during the retry loop may have rewritten the file; remove
        // it after the server exchange so logout always ends clean.
        if let Some(owner) = owner {
            let _ = std::fs::remove_file(self.session_file(&owner));
        }
        Ok(())
    }
    async fn rotated(&self, current: &NativeSession) -> Result<NativeSession> {
        let response = self
            .client
            .post(api_url(&current.origin, "/api/v1/auth/refresh")?)
            .json(&json!({"refreshToken":current.session.refresh_token}))
            .send()
            .await?;
        let session: Session = serde_json::from_slice(&limited_body(response, 1_048_576).await?)?;
        if session.user.id != current.session.user.id
            || session.device_id != current.session.device_id
            || session.user.disabled
            || session.access_token.is_empty()
            || session.refresh_token.is_empty()
        {
            return Err(TransferError::new("会话身份无效"));
        }
        let next = NativeSession {
            origin: current.origin.clone(),
            session,
        };
        // Best effort: a failing disk must not fail an in-flight request, but
        // a stale file would send an expired refresh token on next start.
        let _ = self.persist_session(&next);
        Ok(next)
    }
    pub async fn update_user(&self, owner: &str, user: User) -> Result<()> {
        let _guard = self.refresh.lock().await;
        let mut state = self.state.write().await;
        let current = state
            .as_mut()
            .ok_or_else(|| TransferError::new("请先登录"))?;
        if current.owner() != owner || current.session.user.id != user.id {
            return Err(TransferError::new("登录账户已更改"));
        }
        current.session.user = user;
        Ok(())
    }
    pub async fn refresh_session(&self, old: &NativeSession) -> Result<NativeSession> {
        let _guard = self.refresh.lock().await;
        let current = self.current().await?;
        if current.owner() != old.owner() {
            return Err(TransferError::new("登录账户已更改"));
        }
        if current.session.access_token != old.session.access_token {
            return Ok(current);
        }
        let new = match self.rotated(&current).await {
            Ok(new) => new,
            Err(error) => {
                if matches!(error.status, Some(401 | 403)) {
                    *self.state.write().await = None;
                }
                return Err(error);
            }
        };
        let mut state = self.state.write().await;
        if state
            .as_ref()
            .is_none_or(|s| s.session.id != current.session.id || s.owner() != current.owner())
        {
            return Err(TransferError::new("登录账户已更改"));
        }
        *state = Some(new.clone());
        Ok(new)
    }
    pub async fn request_for(
        &self,
        owner: &str,
        method: &str,
        path: &str,
        body: Option<Vec<u8>>,
        json_body: bool,
    ) -> Result<Response> {
        self.request_for_transfer(owner, method, path, body, json_body, None)
            .await
    }
    pub async fn request_for_transfer(
        &self,
        owner: &str,
        method: &str,
        path: &str,
        body: Option<Vec<u8>>,
        json_body: bool,
        task: Option<&str>,
    ) -> Result<Response> {
        if let Some(id) = task {
            uuid::Uuid::parse_str(id).map_err(|_| TransferError::new("传输任务标识无效"))?;
        }
        let method = match method {
            "GET" => Method::GET,
            "POST" => Method::POST,
            "PUT" => Method::PUT,
            "PATCH" => Method::PATCH,
            "DELETE" => Method::DELETE,
            _ => return Err(TransferError::new("HTTP 方法无效")),
        };
        let mut current = self.current().await?;
        for attempt in 0..2 {
            if current.owner() != owner {
                return Err(TransferError::new("传输任务属于其他账户"));
            }
            let mut request = self
                .client
                .request(method.clone(), api_url(&current.origin, path)?)
                .bearer_auth(&current.session.access_token);
            if let Some(id) = task {
                request = request.header("X-Transfer-ID", id);
            }
            if let Some(ref body) = body {
                request = request
                    .header(
                        "content-type",
                        if json_body {
                            "application/json"
                        } else {
                            "application/octet-stream"
                        },
                    )
                    .header("content-length", body.len());
                if !json_body {
                    if let Some(live) =
                        task.and_then(|id| crate::transfer::metrics::observer(owner, id))
                    {
                        // Rebuild this stream for each authentication attempt; a 401 must not reuse a consumed body.
                        let pieces: Vec<bytes::Bytes> = body
                            .chunks(64 * 1024)
                            .map(bytes::Bytes::copy_from_slice)
                            .collect();
                        let stream =
                            futures_util::stream::iter(pieces.into_iter().map(move |piece| {
                                live.payload(piece.len());
                                Ok::<_, std::io::Error>(piece)
                            }));
                        request = request.body(reqwest::Body::wrap_stream(stream));
                    } else {
                        request = request.body(body.clone());
                    }
                } else {
                    request = request.body(body.clone());
                }
            }
            let response = request.send().await?;
            if response.status().as_u16() == 401 && attempt == 0 {
                current = self.refresh_session(&current).await?;
                continue;
            }
            if !response.status().is_success() {
                return Err(TransferError::http(response.status().as_u16()));
            }
            if self.owner().await? != owner {
                return Err(TransferError::new("登录账户已更改"));
            }
            return Ok(response);
        }
        Err(TransferError::new("登录已失效"))
    }
    pub async fn json(&self, method: &str, path: &str, body: Option<Value>) -> Result<Value> {
        let owner = self.owner().await?;
        self.json_for(&owner, method, path, body).await
    }
    pub async fn json_for(
        &self,
        owner: &str,
        method: &str,
        path: &str,
        body: Option<Value>,
    ) -> Result<Value> {
        let response = self
            .request_for(
                owner,
                method,
                path,
                body.map(|v| serde_json::to_vec(&v)).transpose()?,
                true,
            )
            .await?;
        let bytes = limited_body(response, 8 * 1024 * 1024).await?;
        if bytes.is_empty() {
            Ok(Value::Null)
        } else {
            Ok(serde_json::from_slice(&bytes)?)
        }
    }
}
pub async fn limited_body(response: Response, limit: usize) -> Result<Vec<u8>> {
    limited_body_observed(response, limit, None).await
}
pub async fn limited_body_observed(
    response: Response,
    limit: usize,
    observer: Option<std::sync::Arc<crate::transfer::metrics::Live>>,
) -> Result<Vec<u8>> {
    if !response.status().is_success() {
        return Err(TransferError::http(response.status().as_u16()));
    }
    if response.content_length().is_some_and(|n| n > limit as u64) {
        return Err(TransferError::new("服务器响应超出允许大小"));
    }
    let mut stream = response.bytes_stream();
    let mut bytes = Vec::new();
    while let Some(next) = stream.next().await {
        let next = next?;
        if bytes.len() + next.len() > limit {
            return Err(TransferError::new("服务器响应超出允许大小"));
        }
        if let Some(live) = &observer {
            live.payload(next.len());
        }
        bytes.extend_from_slice(&next)
    }
    Ok(bytes)
}
