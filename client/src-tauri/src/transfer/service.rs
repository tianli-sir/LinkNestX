use super::{
    cache::Cache,
    db::TransferDb,
    models::*,
    scheduler::{BandwidthLimiter, ChunkSource, ServerSource},
};
use crate::{auth::session::AuthManager, events::EventSink, Result, TransferError};
use serde_json::json;
use std::{
    collections::HashMap,
    path::{Component, Path},
    sync::{
        atomic::{AtomicBool, Ordering},
        Arc,
    },
};
use tokio::sync::{Mutex, Notify, RwLock};
use tokio_util::sync::CancellationToken;

struct Worker {
    cancel: CancellationToken,
    handle: tokio::task::JoinHandle<()>,
}
pub struct TransferService {
    pub auth: Arc<AuthManager>,
    pub cache: Arc<Cache>,
    pub events: Arc<dyn EventSink>,
    pub(crate) db: TransferDb,
    pub(crate) policy: RwLock<SharingPolicy>,
    pub(crate) sources: RwLock<Vec<Arc<dyn ChunkSource>>>,
    pub(crate) bandwidth: BandwidthLimiter,
    workers: Mutex<HashMap<String, Worker>>,
    operations: Mutex<()>,
    running: Mutex<usize>,
    available: Notify,
    closing: AtomicBool,
    maintenance_cancel: CancellationToken,
    maintenance: Mutex<Option<tauri::async_runtime::JoinHandle<()>>>,
}
impl TransferService {
    pub fn open(
        root: &Path,
        auth: Arc<AuthManager>,
        events: Arc<dyn EventSink>,
    ) -> Result<Arc<Self>> {
        let db = TransferDb::open(root)?;
        let policy = db.policy()?;
        let cache = Arc::new(Cache::open(root, policy.cache_quota_bytes)?);
        let source: Arc<dyn ChunkSource> = Arc::new(ServerSource(auth.clone()));
        let maintenance_cancel = CancellationToken::new();
        let cancel = maintenance_cancel.clone();
        let background_cache = cache.clone();
        let background_events = events.clone();
        let maintenance = tauri::async_runtime::spawn(async move {
            let mut timer = tokio::time::interval(std::time::Duration::from_secs(30));
            loop {
                tokio::select! {_=cancel.cancelled()=>break,_=timer.tick()=>{
                    if let Err(error)=background_cache.prune().await {background_events.emit("service.error",json!({"error":error.error}));}
                }}
            }
        });
        Ok(Arc::new(Self {
            auth,
            cache,
            events,
            db,
            policy: RwLock::new(policy),
            sources: RwLock::new(vec![source]),
            bandwidth: BandwidthLimiter::default(),
            workers: Mutex::new(HashMap::new()),
            operations: Mutex::new(()),
            running: Mutex::new(0),
            available: Notify::new(),
            closing: AtomicBool::new(false),
            maintenance_cancel,
            maintenance: Mutex::new(Some(maintenance)),
        }))
    }
    pub async fn list(&self) -> Result<Vec<TaskSnapshot>> {
        Ok(self
            .db
            .list(&self.auth.owner().await?)?
            .into_iter()
            .map(|t| t.snapshot)
            .collect())
    }
    pub(crate) fn persist(&self, task: &Task) -> Result<()> {
        self.db.save(task)?;
        self.events
            .emit("transfer.updated", serde_json::to_value(&task.snapshot)?);
        Ok(())
    }
    pub async fn start_download(
        self: &Arc<Self>,
        version_id: String,
        destination: String,
    ) -> Result<String> {
        segment(&version_id)?;
        let destination = validate_destination(&destination)?;
        let owner = self.auth.owner().await?;
        let mut task = Task::new(owner, Direction::Download, version_id, destination);
        let parent = Path::new(&task.path)
            .parent()
            .ok_or_else(|| TransferError::new("保存位置无效"))?;
        task.partial = Some(
            parent
                .join(format!(".linknestx-{}.part", task.snapshot.id))
                .to_string_lossy()
                .into_owned(),
        );
        self.schedule(task).await
    }
    pub async fn start_upload(
        self: &Arc<Self>,
        group_id: String,
        source: String,
        file_id: Option<String>,
    ) -> Result<String> {
        segment(&group_id)?;
        if let Some(ref id) = file_id {
            segment(id)?;
        }
        let path = Path::new(&source);
        if !path.is_absolute() || !tokio::fs::metadata(path).await?.is_file() {
            return Err(TransferError::new("请选择有效的本地文件"));
        }
        let path = tokio::fs::canonicalize(path).await?;
        let name = path
            .file_name()
            .and_then(|v| v.to_str())
            .ok_or_else(|| TransferError::new("文件名无效"))?;
        if !display_name(name) {
            return Err(TransferError::new("文件名无效"));
        }
        let mut task = Task::new(
            self.auth.owner().await?,
            Direction::Upload,
            String::new(),
            path.to_string_lossy().into_owned(),
        );
        task.group_id = Some(group_id);
        task.file_id = file_id;
        self.schedule(task).await
    }
    async fn schedule(self: &Arc<Self>, task: Task) -> Result<String> {
        let _guard = self.operations.lock().await;
        if self.closing.load(Ordering::SeqCst) {
            return Err(TransferError::new("传输服务正在退出"));
        }
        let id = task.snapshot.id.clone();
        self.persist(&task)?;
        self.spawn(task).await;
        Ok(id)
    }
    async fn spawn(self: &Arc<Self>, task: Task) {
        let id = task.snapshot.id.clone();
        let cancel = CancellationToken::new();
        let token = cancel.clone();
        let service = self.clone();
        let handle = tokio::spawn(async move { service.run(task, token).await });
        self.workers
            .lock()
            .await
            .insert(id, Worker { cancel, handle });
    }
    async fn acquire(&self, cancel: &CancellationToken) -> bool {
        loop {
            let notification = self.available.notified();
            {
                let mut running = self.running.lock().await;
                if *running < self.policy.read().await.max_concurrent {
                    *running += 1;
                    return true;
                }
            }
            tokio::select! {_=cancel.cancelled()=>return false,_=notification=>{}}
        }
    }
    async fn run(self: Arc<Self>, mut task: Task, cancel: CancellationToken) {
        if self.acquire(&cancel).await {
            let result = match task.snapshot.direction {
                Direction::Download => self.download(&mut task, &cancel).await,
                Direction::Upload => self.upload(&mut task, &cancel).await,
            };
            *self.running.lock().await -= 1;
            self.available.notify_waiters();
            if let Err(error) = result {
                task.snapshot.error = if cancel.is_cancelled() {
                    None
                } else {
                    Some(error.error)
                };
                task.snapshot.state = if cancel.is_cancelled() {
                    TaskState::Paused
                } else {
                    TaskState::Failed
                };
                if matches!(error.status, Some(403 | 404 | 410)) {
                    let _ = self
                        .cache
                        .revoke(&task.owner, &task.snapshot.version_id)
                        .await;
                    self.events.emit(
                        "sharing.revoked",
                        json!({"versionId":task.snapshot.version_id}),
                    );
                }
            }
        } else {
            task.snapshot.state = TaskState::Paused;
        }
        task.snapshot.active_sources.clear();
        task.resume_on_login =
            self.closing.load(Ordering::SeqCst) && task.snapshot.state == TaskState::Paused;
        let _ = self.persist(&task);
    }
    pub async fn pause(&self, id: &str) -> Result<()> {
        let _guard = self.operations.lock().await;
        let owner = self.auth.owner().await?;
        let _ = self.db.get(id, &owner)?;
        if let Some(worker) = self.workers.lock().await.remove(id) {
            worker.cancel.cancel();
            let _ = worker.handle.await;
        }
        let mut task = self.db.get(id, &owner)?;
        if task.snapshot.state != TaskState::Completed {
            task.snapshot.state = TaskState::Paused;
            task.resume_on_login = false;
            task.snapshot.active_sources.clear();
            self.persist(&task)?;
        }
        Ok(())
    }
    pub async fn resume(self: &Arc<Self>, id: &str) -> Result<()> {
        let _guard = self.operations.lock().await;
        if self.closing.load(Ordering::SeqCst) {
            return Err(TransferError::new("传输服务正在退出"));
        }
        let owner = self.auth.owner().await?;
        let mut task = self.db.get(id, &owner)?;
        let mut workers = self.workers.lock().await;
        if workers.get(id).is_some_and(|w| !w.handle.is_finished()) {
            return Ok(());
        }
        if let Some(worker) = workers.remove(id) {
            let _ = worker.handle.await;
        }
        drop(workers);
        if task.snapshot.state == TaskState::Completed {
            return Ok(());
        }
        task.snapshot.state = TaskState::Queued;
        task.resume_on_login = false;
        task.snapshot.error = None;
        self.persist(&task)?;
        self.spawn(task).await;
        Ok(())
    }
    pub async fn pause_all(&self) -> Result<()> {
        let _guard = self.operations.lock().await;
        let workers = std::mem::take(&mut *self.workers.lock().await);
        for worker in workers.values() {
            worker.cancel.cancel();
        }
        for (_, worker) in workers {
            let _ = worker.handle.await;
        }
        self.db.checkpoint()
    }
    pub async fn shutdown(&self) -> Result<()> {
        self.closing.store(true, Ordering::SeqCst);
        self.maintenance_cancel.cancel();
        if let Some(handle) = self.maintenance.lock().await.take() {
            let _ = handle.await;
        }
        self.pause_all().await
    }
    pub async fn restore_after_login(self: &Arc<Self>) -> Result<()> {
        for task in self.db.list(&self.auth.owner().await?)? {
            if task.resume_on_login {
                self.resume(&task.snapshot.id).await?;
            }
        }
        Ok(())
    }
    pub async fn sharing_policy(&self) -> SharingPolicy {
        self.policy.read().await.clone()
    }
    pub async fn set_sharing_policy(&self, policy: SharingPolicy) -> Result<()> {
        policy.validate()?;
        self.cache.set_quota(policy.cache_quota_bytes).await?;
        self.db.set_policy(&policy)?;
        *self.policy.write().await = policy.clone();
        self.available.notify_waiters();
        self.events
            .emit("sharing.changed", serde_json::to_value(policy)?);
        Ok(())
    }
    /// Task8 supplies peer sources; keep the authenticated server fallback last.
    pub async fn set_peer_sources(&self, mut peers: Vec<Arc<dyn ChunkSource>>) {
        peers.push(Arc::new(ServerSource(self.auth.clone())));
        *self.sources.write().await = peers;
    }
    pub(crate) async fn verified(&self, task: &Task, chunk: &Chunk, bytes: &[u8]) -> Result<()> {
        // Publication starts the server's 30-day lifecycle. Pending upload chunks
        // use an earlier local deadline so an interrupted upload cannot retain cache forever.
        let pending_expiry = (chrono::Utc::now() + chrono::Duration::days(30)).to_rfc3339();
        let expiry = task.manifest.as_ref().and_then(|m| m.expires_at.as_deref());
        let expiry = expiry.or_else(|| {
            (task.snapshot.direction == Direction::Upload).then_some(pending_expiry.as_str())
        });
        if self
            .cache
            .store(&task.owner, &task.snapshot.version_id, chunk, bytes, expiry)
            .await?
            && self.policy.read().await.enabled
        {
            self.events.emit("chunk.verified",json!({"versionId":task.snapshot.version_id,"index":chunk.index,"hash":chunk.hash,"size":chunk.size,"owner":task.owner}));
        }
        Ok(())
    }
}
impl Drop for TransferService {
    fn drop(&mut self) {
        self.maintenance_cancel.cancel();
    }
}
pub fn validate_destination(value: &str) -> Result<String> {
    let path = Path::new(value);
    if !path.is_absolute()
        || path.components().any(|c| matches!(c, Component::ParentDir))
        || std::fs::symlink_metadata(path).is_ok()
    {
        return Err(TransferError::new("请选择尚不存在的绝对保存路径"));
    }
    let name = path
        .file_name()
        .and_then(|v| v.to_str())
        .ok_or_else(|| TransferError::new("保存文件名无效"))?;
    if !display_name(name) || (cfg!(windows) && !safe_name(name)) {
        return Err(TransferError::new("保存文件名无效"));
    }
    let parent = path
        .parent()
        .ok_or_else(|| TransferError::new("保存位置无效"))?
        .canonicalize()?;
    if !parent.is_dir() {
        return Err(TransferError::new("保存文件夹不存在"));
    }
    Ok(parent.join(name).to_string_lossy().into_owned())
}
