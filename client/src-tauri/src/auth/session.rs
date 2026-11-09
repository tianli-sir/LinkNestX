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

pub fn normalize_origin(value: &str) -> Result<String> {
    let text = value.trim();
    let with_scheme = if text.contains("://") {
        text.to_string()
    } else {
        format!("https://{text}")
    };
    let url = Url::parse(&with_scheme).map_err(|_| TransferError::new("服务器地址无效"))?;
    let loopback = matches!(url.host_str(), Some("localhost" | "127.0.0.1" | "[::1]"));
    if !(url.scheme() == "https" || url.scheme() == "http" && loopback)
        || url.host_str().is_none()
        || !url.username().is_empty()
        || url.password().is_some()
        || url.path() != "/"
        || url.query().is_some()
        || url.fragment().is_some()
    {
        return Err(TransferError::new(
            "请输入 HTTPS 服务器地址；本机地址可使用 HTTP",
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
            client,
            state: RwLock::new(None),
            refresh: Mutex::new(()),
            root: root.into(),
        })
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
    pub async fn login(
        &self,
        origin: &str,
        username: &str,
        password: &str,
        device: Option<String>,
    ) -> Result<Session> {
        let origin = normalize_origin(origin)?;
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
        *self.state.write().await = Some(NativeSession {
            origin,
            session: session.clone(),
        });
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
        Ok(NativeSession {
            origin: current.origin.clone(),
            session,
        })
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
                    .body(body.clone())
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
        bytes.extend_from_slice(&next)
    }
    Ok(bytes)
}
