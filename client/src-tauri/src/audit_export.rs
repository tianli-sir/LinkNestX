//! A renderer can request a format and filter, but only the native picker grants a path.
//! Bytes are published with no-clobber semantics only after the HTTP body ends cleanly.
use crate::{
    auth::session::{api_url, AuthManager},
    Result, TransferError,
};
use async_trait::async_trait;
use futures_util::StreamExt;
use reqwest::{Client, Response};
use serde::{Deserialize, Serialize};
use std::{
    path::{Path, PathBuf},
    sync::{
        atomic::{AtomicBool, Ordering},
        Arc, Mutex,
    },
    time::Duration,
};
use tokio::io::AsyncWriteExt;
use tokio_util::sync::CancellationToken;

#[derive(Clone, Copy, Deserialize)]
#[serde(rename_all = "lowercase")]
pub enum AuditFormat {
    Csv,
    Json,
}
impl AuditFormat {
    fn extension(self) -> &'static str {
        match self {
            Self::Csv => "csv",
            Self::Json => "json",
        }
    }
    fn content_type(self) -> &'static str {
        match self {
            Self::Csv => "text/csv",
            Self::Json => "application/json",
        }
    }
}
pub struct ExportRequest {
    format: AuditFormat,
    action: String,
}
impl ExportRequest {
    pub fn new(format: AuditFormat, action: String) -> Result<Self> {
        if action.len() > 256 || action.chars().any(char::is_control) {
            return Err(TransferError::new("审计操作筛选条件无效或过长"));
        }
        Ok(Self { format, action })
    }
    fn path(&self) -> String {
        // Fixed route and fixed keys. Encoding prevents a filter from injecting another query.
        let query = reqwest::Url::parse_with_params(
            "https://localhost/",
            [
                ("format", self.format.extension()),
                ("action", self.action.as_str()),
            ],
        )
        .expect("fixed URL");
        format!(
            "/api/v1/admin/audit/export?{}",
            query.query().unwrap_or_default()
        )
    }
}
#[derive(Debug, Serialize)]
#[serde(tag = "status", rename_all = "lowercase")]
pub enum ExportOutcome {
    Cancelled,
    Saved { path: String, bytes: u64 },
}

#[async_trait]
pub trait AuditSavePicker: Send + Sync {
    async fn pick(&self, format: AuditFormat) -> Result<Option<PathBuf>>;
}
#[cfg(not(feature = "legacy-host"))]
pub struct NativeAuditSavePicker;
#[cfg(not(feature = "legacy-host"))]
#[async_trait]
impl AuditSavePicker for NativeAuditSavePicker {
    async fn pick(&self, format: AuditFormat) -> Result<Option<PathBuf>> {
        Ok(rfd::AsyncFileDialog::new()
            .set_title("导出全部审计记录（请选择新文件名）")
            .set_file_name(format!("linknestx-audit.{}", format.extension()))
            .add_filter(format.extension().to_uppercase(), &[format.extension()])
            .save_file()
            .await
            .map(|file| file.path().to_path_buf()))
    }
}
struct ActiveExport {
    id: String,
    claimed: AtomicBool,
    cancel: CancellationToken,
    finished: CancellationToken,
}
#[derive(Default)]
struct ManagerState {
    active: Option<Arc<ActiveExport>>,
    stopped: bool,
}
pub struct AuditExportManager {
    state: Mutex<ManagerState>,
    client: Client,
}
impl AuditExportManager {
    pub fn new() -> Result<Self> {
        Ok(Self {
            state: Mutex::new(ManagerState::default()),
            client: Client::builder()
                .redirect(reqwest::redirect::Policy::none())
                .connect_timeout(Duration::from_secs(10))
                .read_timeout(Duration::from_secs(30))
                .build()?,
        })
    }
    /// Reserve synchronously before any lifecycle/authentication wait. The renderer waits for
    /// this ID before sending start, and targets cancellation at the same reservation.
    pub fn reserve(&self) -> Result<String> {
        let mut state = self.state.lock().unwrap();
        if state.stopped {
            return Err(TransferError::new("应用正在退出"));
        }
        if state.active.is_some() {
            return Err(TransferError::new("已有审计导出正在进行"));
        }
        let id = uuid::Uuid::new_v4().to_string();
        state.active = Some(Arc::new(ActiveExport {
            id: id.clone(),
            claimed: AtomicBool::new(false),
            cancel: CancellationToken::new(),
            finished: CancellationToken::new(),
        }));
        Ok(id)
    }
    fn claim(self: &Arc<Self>, export_id: &str) -> Result<Option<ExportRegistration>> {
        let state = self.state.lock().unwrap();
        let Some(control) = state
            .active
            .as_ref()
            .filter(|active| active.id == export_id)
        else {
            // A cancelled or stale reservation cannot open a picker even if start arrives late.
            return Ok(None);
        };
        if control.claimed.swap(true, Ordering::SeqCst) {
            return Err(TransferError::new("审计导出已启动"));
        }
        Ok(Some(ExportRegistration {
            manager: self.clone(),
            control: control.clone(),
        }))
    }
    pub async fn export_reserved(
        self: &Arc<Self>,
        export_id: &str,
        lifecycle: &tokio::sync::Mutex<()>,
        auth: Arc<AuthManager>,
        request: ExportRequest,
        picker: &dyn AuditSavePicker,
    ) -> Result<ExportOutcome> {
        let Some(registration) = self.claim(export_id)? else {
            return Ok(ExportOutcome::Cancelled);
        };
        let job = {
            // Logout holds lifecycle while cancelling. Startup must observe cancellation without
            // acquiring that lock, otherwise logout and this queued export would deadlock.
            let _guard = tokio::select! {
                biased;
                _ = registration.control.cancel.cancelled() => return Ok(ExportOutcome::Cancelled),
                guard = lifecycle.lock() => guard,
            };
            let Some(job) = registration.begin(auth, request).await? else {
                return Ok(ExportOutcome::Cancelled);
            };
            job
        };
        job.save(picker).await
    }
    /// Native callers can create a job directly. Registration still precedes the auth await.
    pub async fn begin(
        self: &Arc<Self>,
        auth: Arc<AuthManager>,
        request: ExportRequest,
    ) -> Result<ExportJob> {
        let id = self.reserve()?;
        let registration = self
            .claim(&id)?
            .ok_or_else(|| TransferError::new("审计导出已取消"))?;
        registration
            .begin(auth, request)
            .await?
            .ok_or_else(|| TransferError::new("审计导出已取消"))
    }
    fn cancel_matching(&self, export_id: Option<&str>) -> Option<Arc<ActiveExport>> {
        let mut state = self.state.lock().unwrap();
        let active = state
            .active
            .as_ref()
            .filter(|active| export_id.is_none_or(|id| active.id == id))?
            .clone();
        active.cancel.cancel();
        if !active.claimed.load(Ordering::SeqCst) {
            state.active = None;
            active.finished.cancel();
        }
        Some(active)
    }
    pub async fn cancel(&self, export_id: &str) {
        if let Some(active) = self.cancel_matching(Some(export_id)) {
            active.finished.cancelled().await;
        }
    }
    pub async fn cancel_and_wait(&self) {
        if let Some(active) = self.cancel_matching(None) {
            active.finished.cancelled().await;
        }
    }
    pub async fn shutdown(&self) {
        self.state.lock().unwrap().stopped = true;
        self.cancel_and_wait().await;
    }
}
struct ExportRegistration {
    manager: Arc<AuditExportManager>,
    control: Arc<ActiveExport>,
}
pub struct ExportJob {
    _registration: ExportRegistration,
    manager: Arc<AuditExportManager>,
    control: Arc<ActiveExport>,
    auth: Arc<AuthManager>,
    owner: String,
    device: String,
    request: ExportRequest,
}
impl Drop for ExportRegistration {
    fn drop(&mut self) {
        let mut state = self.manager.state.lock().unwrap();
        if state
            .active
            .as_ref()
            .is_some_and(|active| Arc::ptr_eq(active, &self.control))
        {
            state.active = None;
        }
        self.control.finished.cancel();
    }
}
impl ExportRegistration {
    async fn begin(
        self,
        auth: Arc<AuthManager>,
        request: ExportRequest,
    ) -> Result<Option<ExportJob>> {
        let current = tokio::select! {
            biased;
            _ = self.control.cancel.cancelled() => return Ok(None),
            current = auth.current() => current?,
        };
        if current.session.user.disabled
            || current.session.user.must_change_password
            || !current
                .session
                .user
                .roles
                .iter()
                .any(|role| role == "admin")
        {
            return Err(TransferError::new("需要已完成密码设置的管理员账户"));
        }
        Ok(Some(ExportJob {
            manager: self.manager.clone(),
            control: self.control.clone(),
            _registration: self,
            auth,
            owner: current.owner(),
            device: current.session.device_id,
            request,
        }))
    }
}
impl ExportJob {
    pub async fn save(self, picker: &dyn AuditSavePicker) -> Result<ExportOutcome> {
        self.save_inner(picker).await
    }
    async fn check_identity(&self) -> Result<()> {
        let current = self.auth.current().await?;
        if current.owner() != self.owner
            || current.session.device_id != self.device
            || current.session.user.disabled
            || current.session.user.must_change_password
            || !current
                .session
                .user
                .roles
                .iter()
                .any(|role| role == "admin")
        {
            return Err(TransferError::new("登录账户或管理员权限已更改，导出已停止"));
        }
        Ok(())
    }
    async fn identity_changed(&self) -> TransferError {
        loop {
            if let Err(error) = self.check_identity().await {
                return error;
            }
            tokio::time::sleep(Duration::from_millis(100)).await;
        }
    }
    async fn response(&self) -> Result<Response> {
        let mut current = self.auth.current().await?;
        for attempt in 0..2 {
            self.check_identity().await?;
            let response = self
                .manager
                .client
                .get(api_url(&current.origin, &self.request.path())?)
                .bearer_auth(&current.session.access_token)
                .send()
                .await?;
            if response.status().as_u16() == 401 && attempt == 0 {
                // This retry happens before opening/consuming an export body. Never retry midstream.
                current = self.auth.refresh_session(&current).await?;
                continue;
            }
            if response.status().as_u16() != 200 {
                return Err(TransferError::http(response.status().as_u16()));
            }
            let content_type = response
                .headers()
                .get(reqwest::header::CONTENT_TYPE)
                .and_then(|value| value.to_str().ok())
                .unwrap_or("")
                .split(';')
                .next()
                .unwrap_or("")
                .trim();
            if content_type != self.request.format.content_type() {
                return Err(TransferError::new("服务器返回了错误的审计导出格式"));
            }
            return Ok(response);
        }
        Err(TransferError::new("登录已失效"))
    }
    async fn save_inner(&self, picker: &dyn AuditSavePicker) -> Result<ExportOutcome> {
        let destination = tokio::select! {
            biased;
            _ = self.control.cancel.cancelled() => return Ok(ExportOutcome::Cancelled),
            error = self.identity_changed() => return Err(error),
            result = picker.pick(self.request.format) => result?,
        };
        let Some(destination) = destination else {
            return Ok(ExportOutcome::Cancelled);
        };
        self.check_identity().await?;
        // This is an early usability check; persist_noclobber below is the authoritative race-safe check.
        match std::fs::symlink_metadata(&destination) {
            Ok(_) => return Err(TransferError::new("目标文件已存在，请选择其他名称")),
            Err(error) if error.kind() == std::io::ErrorKind::NotFound => {}
            Err(error) => return Err(error.into()),
        }
        let directory = destination
            .parent()
            .filter(|path| !path.as_os_str().is_empty())
            .unwrap_or(Path::new("."));
        let response = tokio::select! {
            biased;
            _ = self.control.cancel.cancelled() => return Ok(ExportOutcome::Cancelled),
            error = self.identity_changed() => return Err(error),
            result = self.response() => result?,
        };
        let temporary = tempfile::Builder::new()
            .prefix(".linknestx-audit-")
            .suffix(".part")
            .tempfile_in(directory)?;
        let (file, temporary_path) = temporary.into_parts();
        let mut file = tokio::fs::File::from_std(file);
        let mut stream = response.bytes_stream();
        let transferred = tokio::select! {
            biased;
            _ = self.control.cancel.cancelled() => Ok(None),
            error = self.identity_changed() => Err(error),
            result = async {
                let mut bytes = 0u64;
                while let Some(next) = stream.next().await {
                    let chunk = next?; // A Go HTTP abort is an error, never a completed CSV.
                    self.check_identity().await?;
                    for block in chunk.chunks(64 * 1024) { file.write_all(block).await?; }
                    bytes = bytes.checked_add(chunk.len() as u64).ok_or_else(|| TransferError::new("导出文件过大"))?;
                }
                Ok::<_, TransferError>(Some(bytes))
            } => result,
        };
        // Tokio filesystem operations can outlive a dropped write future. Wait for the owned
        // handle before removing the temporary path, including on cancellation (Windows).
        let flushed = file.flush().await;
        let synced = if matches!(&transferred, Ok(Some(_))) && flushed.is_ok() {
            file.sync_all().await
        } else {
            Ok(())
        };
        drop(file.into_std().await);
        let Some(bytes) = transferred? else {
            return Ok(ExportOutcome::Cancelled);
        };
        flushed?;
        synced?;
        self.check_identity().await?;
        // Cancellation and publication share this lock: after cancellation wins, no new file can appear.
        let _publication = self.manager.state.lock().unwrap();
        if self.control.cancel.is_cancelled() {
            return Ok(ExportOutcome::Cancelled);
        }
        temporary_path
            .persist_noclobber(&destination)
            .map_err(|error| {
                if error.error.kind() == std::io::ErrorKind::AlreadyExists {
                    TransferError::new("目标文件已存在，请选择其他名称")
                } else {
                    TransferError::new("无法保存审计导出文件")
                }
            })?;
        Ok(ExportOutcome::Saved {
            path: destination.to_string_lossy().into_owned(),
            bytes,
        })
    }
}
