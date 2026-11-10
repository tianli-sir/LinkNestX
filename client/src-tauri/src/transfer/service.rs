use super::{
    cache::Cache,
    db::TransferDb,
    models::*,
    scheduler::{BandwidthLimiter, ChunkSource, ServerSource},
};
use crate::{auth::session::AuthManager, events::EventSink, Result, TransferError};
use serde_json::json;
use std::{
    collections::{HashMap, HashSet},
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
    pub(crate) db: Arc<TransferDb>,
    pub(crate) policy: Arc<RwLock<SharingPolicy>>,
    pub peers: Arc<crate::peer::PeerManager>,
    pub(crate) sources: RwLock<Vec<Arc<dyn ChunkSource>>>,
    pub(crate) bandwidth: BandwidthLimiter,
    workers: Mutex<HashMap<String, Worker>>,
    operations: Mutex<()>,
    running: Mutex<usize>,
    available: Notify,
    closing: AtomicBool,
    lifecycle_cursor: Mutex<usize>,
    maintenance_cancel: CancellationToken,
    maintenance: Mutex<Option<tauri::async_runtime::JoinHandle<()>>>,
}
impl TransferService {
    pub fn open(
        root: &Path,
        auth: Arc<AuthManager>,
        events: Arc<dyn EventSink>,
    ) -> Result<Arc<Self>> {
        let db = Arc::new(TransferDb::open(root)?);
        // Before exposing the service or accepting native attachment input.
        crate::attachments::cleanup_uploads_at_startup(root, &db)?;
        let policy = db.policy()?;
        let cache = Arc::new(Cache::open(root, policy.cache_quota_bytes)?);
        let source: Arc<dyn ChunkSource> = Arc::new(ServerSource(auth.clone()));
        let policy = Arc::new(RwLock::new(policy));
        let peers = crate::peer::PeerManager::new(
            root,
            auth.clone(),
            cache.clone(),
            policy.clone(),
            events.clone(),
        )?;
        let maintenance_cancel = CancellationToken::new();
        let service = Arc::new(Self {
            auth,
            cache,
            events,
            db,
            policy,
            peers,
            sources: RwLock::new(vec![source]),
            bandwidth: BandwidthLimiter::default(),
            workers: Mutex::new(HashMap::new()),
            operations: Mutex::new(()),
            running: Mutex::new(0),
            available: Notify::new(),
            closing: AtomicBool::new(false),
            maintenance_cancel,
            maintenance: Mutex::new(None),
            lifecycle_cursor: Mutex::new(0),
        });
        let weak = Arc::downgrade(&service);
        let cancel = service.maintenance_cancel.clone();
        let mut changes = service.events.subscribe();
        let maintenance = tauri::async_runtime::spawn(async move {
            let mut timer = tokio::time::interval(std::time::Duration::from_secs(30));
            loop {
                tokio::select! {_=cancel.cancelled()=>break,_=timer.tick()=>{},_=async {match &mut changes {Some(rx)=>{let _=rx.recv().await;},None=>std::future::pending::<()>().await}}=>{}}
                let Some(s) = weak.upgrade() else { break };
                if let Err(error) = tokio::select! {_=cancel.cancelled()=>break,result=s.reconcile_lifecycle()=>result}
                {
                    s.events.emit(
                        "service.error",
                        json!({"error":format!("缓存状态检查失败：{}",error.error)}),
                    );
                }
                if let Ok(owner) = s.auth.owner().await {
                    if let Err(error) = recover_completion_audits(&s.peers.audit, &s.db, &owner) {
                        s.events.emit(
                            "service.error",
                            json!({"error":format!("完成审计将在后台重试：{}",error.error)}),
                        );
                    }
                    tokio::select! {_=cancel.cancelled()=>break,result=tokio::time::timeout(std::time::Duration::from_secs(10),s.peers.audit.flush(&s.auth))=>{if let Ok(Err(error))=result{s.events.emit("service.error",json!({"error":format!("传输审计补传失败：{}",error.error)}));}}}
                }
            }
        });
        *service.maintenance.try_lock().unwrap() = Some(maintenance);
        Ok(service)
    }
    pub async fn list(&self) -> Result<Vec<TaskSnapshot>> {
        Ok(self
            .db
            .list(&self.auth.owner().await?)?
            .into_iter()
            .map(|mut t| {
                if let Some(live) = super::metrics::observer(&t.owner, &t.snapshot.id) {
                    t.snapshot.metrics = live
                        .record(t.snapshot.bytes_completed, t.snapshot.total_bytes)
                        .snapshot;
                }
                t.snapshot
            })
            .collect())
    }
    pub(crate) fn persist(&self, task: &Task) -> Result<()> {
        let mut task = task.clone();
        task.snapshot.group_id = task.group_id.clone();
        if let Some(live) = super::metrics::observer(&task.owner, &task.snapshot.id) {
            live.phase(&task.snapshot.state);
            task.metrics_record =
                live.record(task.snapshot.bytes_completed, task.snapshot.total_bytes);
            task.snapshot.metrics = task.metrics_record.snapshot.clone();
        } else {
            let previous = self
                .db
                .get(&task.snapshot.id, &task.owner)
                .ok()
                .map(|t| t.snapshot.metrics.revision)
                .unwrap_or(0);
            task.snapshot.metrics.revision = task
                .snapshot
                .metrics
                .revision
                .max(previous)
                .saturating_add(1);
            task.metrics_record.snapshot = task.snapshot.metrics.clone();
        }
        self.db.save(&task)?;
        self.events
            .emit("transfer.updated", serde_json::to_value(&task.snapshot)?);
        Ok(())
    }
    pub async fn start_download(
        self: &Arc<Self>,
        version_id: String,
        destination: String,
    ) -> Result<String> {
        self.start_download_for_message(version_id, destination, None, None)
            .await
    }
    pub async fn start_download_for_message(
        self: &Arc<Self>,
        version_id: String,
        destination: String,
        group_id: Option<String>,
        message_id: Option<String>,
    ) -> Result<String> {
        segment(&version_id)?;
        if let Some(id) = &group_id {
            segment(id)?;
        }
        if let Some(id) = &message_id {
            segment(id)?;
        }
        let destination = validate_destination(&destination)?;
        let owner = self.auth.owner().await?;
        let mut task = Task::new(owner, Direction::Download, version_id, destination);
        task.group_id = group_id;
        task.snapshot.message_id = message_id;
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
        self.start_upload_with_attachment(group_id, source, file_id, None, false, None)
            .await
    }
    pub async fn start_upload_with_attachment(
        self: &Arc<Self>,
        group_id: String,
        source: String,
        file_id: Option<String>,
        client_attachment_id: Option<String>,
        owned_source: bool,
        display_name_override: Option<String>,
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
        task.snapshot.client_attachment_id = client_attachment_id;
        task.owned_source = owned_source;
        if let Some(name) = display_name_override {
            task.snapshot.name = name;
        }
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
        let permit = self.auth.activity.try_enter()?;
        self.persist(&task)?;
        let task = self.db.get(&task.snapshot.id, &task.owner)?;
        self.spawn(task, permit).await;
        Ok(id)
    }
    async fn spawn(self: &Arc<Self>, task: Task, permit: crate::activity::ActivityPermit) {
        let id = task.snapshot.id.clone();
        let cancel = CancellationToken::new();
        let token = cancel.clone();
        let service = self.clone();
        let handle = tokio::spawn(async move {
            let _permit = permit;
            service.run(task, token).await
        });
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
        let mut metering = None;
        if self.acquire(&cancel).await {
            let live = super::metrics::Live::new(task.metrics_record.clone());
            super::metrics::register(&task.owner, &task.snapshot.id, &live);
            let stop = CancellationToken::new();
            let ticker_stop = stop.clone();
            let db = self.db.clone();
            let events = self.events.clone();
            let id = task.snapshot.id.clone();
            let owner = task.owner.clone();
            let timer_live = live.clone();
            let ticker = tokio::spawn(async move {
                let mut timer = tokio::time::interval(std::time::Duration::from_millis(500));
                loop {
                    tokio::select! { _=ticker_stop.cancelled()=>break, _=timer.tick()=>{} }
                    if let Ok(Some(snapshot)) = db.checkpoint_metrics(&id, &owner, &timer_live) {
                        if let Ok(value) = serde_json::to_value(snapshot) {
                            events.emit("transfer.updated", value);
                        }
                    }
                }
            });
            metering = Some((live, stop, ticker));
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
                    self.peers
                        .invalidate_version(&task.snapshot.version_id)
                        .await;
                    if let Err(cleanup) = remove_owned_partial(&task).await {
                        self.events
                            .emit("service.error", json!({"error":cleanup.error}));
                    }
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
        if let Some((live, stop, ticker)) = metering {
            stop.cancel();
            let _ = ticker.await;
            live.phase(&task.snapshot.state);
            let _ = self.persist(&task);
            super::metrics::remove(&task.owner, &task.snapshot.id);
        } else {
            let _ = self.persist(&task);
        }
        if task.owned_source && task.snapshot.state == TaskState::Completed {
            if let Some(parent) = Path::new(&task.path).parent() {
                if parent
                    .file_name()
                    .is_some_and(|n| n.to_string_lossy().starts_with("upload-"))
                {
                    let _ = tokio::fs::remove_file(&task.path).await;
                    let _ = tokio::fs::remove_dir(parent).await;
                }
            }
        }
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
        let permit = self.auth.activity.try_enter()?;
        self.persist(&task)?;
        let task = self.db.get(&task.snapshot.id, &task.owner)?;
        self.spawn(task, permit).await;
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
        self.peers.stop().await;
        self.maintenance_cancel.cancel();
        if let Some(handle) = self.maintenance.lock().await.take() {
            let _ = handle.await;
        }
        self.pause_all().await
    }
    pub async fn restore_after_login(self: &Arc<Self>) -> Result<()> {
        self.reconcile_lifecycle().await?;
        self.peers.start().await;
        self.events.emit(
            "sharing.changed",
            serde_json::to_value(self.sharing_policy().await)?,
        );
        let owner = self.auth.owner().await?;
        if let Err(error) = recover_completion_audits(&self.peers.audit, &self.db, &owner) {
            self.events.emit(
                "service.error",
                json!({"error":format!("完成审计将在后台重试：{}",error.error)}),
            );
        }
        for task in self.db.list(&owner)? {
            if task.resume_on_login {
                self.resume(&task.snapshot.id).await?;
            }
        }
        Ok(())
    }
    /// Bounded round-robin authority checks are independent of the window and local sharing.
    pub async fn reconcile_lifecycle(&self) -> Result<()> {
        self.reconcile_lifecycle_with(self.auth.owner().await.ok(), |owner, version| async move {
            self.peers.refresh_manifest(&owner, &version).await
        })
        .await
    }
    async fn reconcile_lifecycle_with<F, Fut>(
        &self,
        owner: Option<String>,
        mut refresh: F,
    ) -> Result<()>
    where
        F: FnMut(String, String) -> Fut,
        Fut: std::future::Future<Output = Result<Manifest>>,
    {
        let owner = match owner {
            Some(owner) => owner,
            None => return self.prune_expired_local(&HashSet::new()).await,
        };
        let tasks = self.db.list(&owner)?;
        let pending_uploads: std::collections::HashSet<_> = tasks
            .iter()
            .filter(|t| {
                t.snapshot.direction == Direction::Upload
                    && t.snapshot.state != TaskState::Completed
            })
            .map(|t| t.snapshot.version_id.clone())
            .collect();
        let mut versions: std::collections::BTreeSet<_> = self
            .cache
            .versions(&owner)?
            .into_iter()
            .filter(|v| !pending_uploads.contains(v))
            .collect();
        versions.extend(
            tasks
                .iter()
                .filter(|t| t.snapshot.direction == Direction::Download)
                .map(|t| t.snapshot.version_id.clone()),
        );
        versions.remove("");
        let versions: Vec<_> = versions.into_iter().collect();
        // Defer unvisited versions until their bounded authority check; a promotion
        // must be applied before stale local expiry can remove their bytes.
        let mut deferred: HashSet<_> = versions
            .iter()
            .map(|v| (owner.clone(), v.clone()))
            .collect();
        let start = *self.lifecycle_cursor.lock().await;
        for offset in 0..versions.len().min(16) {
            let version = &versions[(start + offset) % versions.len()];
            let outcome = tokio::time::timeout(
                std::time::Duration::from_secs(3),
                refresh(owner.clone(), version.clone()),
            )
            .await;
            match outcome {
                Ok(Ok(manifest)) => {
                    self.apply_authority_result(&owner, version, Ok(manifest))
                        .await?;
                }
                result => {
                    deferred.remove(&(owner.clone(), version.clone()));
                    if let Ok(result) = result {
                        self.apply_authority_result(&owner, version, result).await?;
                    }
                }
            }
            // Timeouts/network/5xx retain valid bytes, but do not extend known expiry.
        }
        *self.lifecycle_cursor.lock().await = if versions.is_empty() {
            0
        } else {
            (start + 16) % versions.len()
        };
        self.prune_expired_local(&deferred).await
    }
    async fn prune_expired_local(&self, deferred: &HashSet<(String, String)>) -> Result<()> {
        let _guard = self.operations.lock().await;
        let expired = |task: &Task| {
            task.snapshot.direction == Direction::Download
                && task
                    .manifest
                    .as_ref()
                    .and_then(|m| m.expires_at.as_deref())
                    .and_then(|value| chrono::DateTime::parse_from_rfc3339(value).ok())
                    .is_some_and(|expiry| expiry <= chrono::Utc::now())
                && !deferred.contains(&(task.owner.clone(), task.snapshot.version_id.clone()))
        };
        for mut task in self.db.all()?.into_iter().filter(&expired) {
            if let Some(worker) = self.workers.lock().await.remove(&task.snapshot.id) {
                worker.cancel.cancel();
                let _ = worker.handle.await;
                task = self.db.get(&task.snapshot.id, &task.owner)?;
                if !expired(&task) {
                    continue;
                }
            }
            if let Err(error) = remove_owned_partial(&task).await {
                self.events
                    .emit("service.error", json!({"error":error.error}));
                continue;
            }
            // Eviction is not authority denial. Keep the owned path so a retry can
            // fetch a fresh manifest and recreate staging after an offline promotion.
            task.manifest = None;
            task.verified.clear();
            if task.snapshot.state != TaskState::Completed {
                task.snapshot.state = TaskState::Paused;
                task.snapshot.bytes_completed = 0;
                task.snapshot.active_sources.clear();
                task.snapshot.error = Some("本地临时文件已过期，可重试下载".into());
            }
            self.persist(&task)?;
        }
        for (owner, version) in self.cache.expired_versions()? {
            if !deferred.contains(&(owner.clone(), version.clone())) {
                self.cache.prune_expired(&owner, &version).await?;
            }
        }
        Ok(())
    }
    async fn apply_authority_result(
        &self,
        owner: &str,
        version: &str,
        result: Result<Manifest>,
    ) -> Result<()> {
        match result {
            Ok(manifest) => {
                let _guard = self.operations.lock().await;
                self.cache
                    .refresh_expiry(owner, version, manifest.expires_at.as_deref())
                    .await?;
                for mut task in self
                    .db
                    .list(owner)?
                    .into_iter()
                    .filter(|t| t.snapshot.version_id == version)
                {
                    if let Some(stored) = task.manifest.as_mut() {
                        stored.expires_at.clone_from(&manifest.expires_at);
                        self.db.save(&task)?;
                    }
                }
            }
            Err(error) if matches!(error.status, Some(403 | 404 | 410)) => {
                self.revoke_version(owner, version).await?;
            }
            Err(_) => {}
        }
        Ok(())
    }
    async fn revoke_version(&self, owner: &str, version: &str) -> Result<()> {
        let _guard = self.operations.lock().await;
        for mut task in self
            .db
            .list(owner)?
            .into_iter()
            .filter(|t| t.snapshot.version_id == version)
        {
            if let Some(worker) = self.workers.lock().await.remove(&task.snapshot.id) {
                worker.cancel.cancel();
                let _ = worker.handle.await;
                task = self.db.get(&task.snapshot.id, owner)?;
            }
            if let Err(error) = remove_owned_partial(&task).await {
                self.events
                    .emit("service.error", json!({"error":error.error}));
            }
            task.partial = None;
            task.manifest = None;
            task.verified.clear();
            task.resume_on_login = false;
            if task.snapshot.state != TaskState::Completed {
                task.snapshot.state = TaskState::Failed;
                task.snapshot.error = Some("文件已删除、过期或访问权限已撤销".into());
            }
            self.persist(&task)?;
        }
        self.cache.revoke(owner, version).await?;
        self.peers.invalidate_version(version).await;
        self.events
            .emit("sharing.revoked", json!({"versionId":version}));
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
        self.bandwidth.set_limit(policy.bandwidth_limit_bps);
        self.peers.set_bandwidth_limit(policy.bandwidth_limit_bps);
        self.available.notify_waiters();
        self.events
            .emit("sharing.changed", serde_json::to_value(&policy)?);
        if policy.enabled {
            self.peers.start().await;
        } else {
            self.peers.stop().await;
        }
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
fn recover_completion_audits(
    audit: &crate::peer::audit::AuditOutbox,
    db: &TransferDb,
    owner: &str,
) -> Result<()> {
    for task in db.list(owner)?.into_iter().filter(|t| {
        t.snapshot.direction == Direction::Download && t.snapshot.state == TaskState::Completed
    }) {
        audit.logical_completion(
            owner,
            &task.snapshot.id,
            &task.snapshot.version_id,
            task.snapshot.total_bytes,
        )?;
    }
    for (id, version) in audit.staged_tasks(owner)? {
        if db.get(&id, owner).is_ok_and(|task| {
            task.snapshot.state == TaskState::Completed && task.snapshot.version_id == version
        }) {
            audit.publish_completion(owner, &id, &version)?;
        }
    }
    Ok(())
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

#[cfg(test)]
mod final_expiry_tests {
    use super::*;
    use crate::events::NoopEvents;

    async fn open_service(root: &Path) -> Arc<TransferService> {
        let auth = Arc::new(AuthManager::new(root).unwrap());
        let service = TransferService::open(root, auth, Arc::new(NoopEvents)).unwrap();
        service.maintenance_cancel.cancel();
        if let Some(handle) = service.maintenance.lock().await.take() {
            handle.await.unwrap();
        }
        service.policy.write().await.enabled = false;
        service
    }

    async fn cached(
        service: &TransferService,
        root: &Path,
        owner: &str,
        version: &str,
    ) -> std::path::PathBuf {
        let chunk = Chunk {
            index: 0,
            hash: hash(b"bytes"),
            size: 5,
        };
        service
            .cache
            .store(owner, version, &chunk, b"bytes", None)
            .await
            .unwrap();
        root.join("cache").join(hash(
            format!("{owner}\0{version}\0{}", chunk.hash).as_bytes(),
        ))
    }

    fn expire_cache(root: &Path, version: &str) {
        let db = rusqlite::Connection::open(root.join("cache/index.sqlite3")).unwrap();
        db.execute("UPDATE chunks SET expires=0 WHERE version=?1", [version])
            .unwrap();
    }

    fn manifest(version: &str, expiry: Option<&str>) -> Manifest {
        Manifest {
            file_id: "file".into(),
            version_id: version.into(),
            name: "saved.bin".into(),
            size: 5,
            sha256: hash(b"bytes"),
            chunk_size: 5,
            chunks: vec![Chunk {
                index: 0,
                hash: hash(b"bytes"),
                size: 5,
            }],
            expires_at: expiry.map(str::to_owned),
            visibility: "group".into(),
            sharing_policy: AdminSharingPolicy::default(),
        }
    }

    fn partial(
        service: &TransferService,
        root: &Path,
        owner: &str,
        version: &str,
        expiry: Option<&str>,
    ) -> Task {
        let root = root.canonicalize().unwrap();
        let mut task = Task::new(
            owner.into(),
            Direction::Download,
            version.into(),
            root.join(format!("{version}.bin")).to_string_lossy().into(),
        );
        task.partial = Some(
            root.join(format!(".linknestx-{}.part", task.snapshot.id))
                .to_string_lossy()
                .into(),
        );
        std::fs::write(task.partial.as_ref().unwrap(), b"bytes").unwrap();
        task.manifest = Some(manifest(version, expiry));
        task.verified = vec![true];
        task.snapshot.state = TaskState::Paused;
        task.snapshot.bytes_completed = 5;
        service.db.save(&task).unwrap();
        task
    }

    #[tokio::test]
    async fn logged_out_maintenance_removes_expired_cache_for_every_owner_only() {
        let root = tempfile::tempdir().unwrap();
        let service = open_service(root.path()).await;
        let expired_a = cached(&service, root.path(), "a", "expired-a").await;
        let expired_b = cached(&service, root.path(), "b", "expired-b").await;
        let valid = cached(&service, root.path(), "a", "valid").await;
        let permanent = cached(&service, root.path(), "b", "permanent").await;
        expire_cache(root.path(), "expired-a");
        expire_cache(root.path(), "expired-b");
        service
            .cache
            .refresh_expiry(
                "a",
                "valid",
                Some(&(chrono::Utc::now() + chrono::Duration::days(1)).to_rfc3339()),
            )
            .await
            .unwrap();
        let mut policy = service.db.policy().unwrap();
        policy.cache_quota_bytes = 0;
        service.db.set_policy(&policy).unwrap();
        drop(service);
        let service = open_service(root.path()).await;
        assert!(service.auth.owner().await.is_err());

        service.reconcile_lifecycle().await.unwrap();

        assert!(
            !expired_a.exists(),
            "logged-out maintenance must delete expired bytes"
        );
        assert!(
            !expired_b.exists(),
            "maintenance must include other persisted owners"
        );
        assert_eq!(std::fs::read(valid).unwrap(), b"bytes");
        assert_eq!(std::fs::read(permanent).unwrap(), b"bytes");
    }

    #[tokio::test(start_paused = true)]
    async fn transient_and_timeout_maintenance_only_remove_known_expired_content() {
        for timeout in [false, true] {
            let root = tempfile::tempdir().unwrap();
            let service = open_service(root.path()).await;
            let expired = cached(&service, root.path(), "o", "expired").await;
            let valid = cached(&service, root.path(), "o", "valid").await;
            let permanent = cached(&service, root.path(), "o", "permanent").await;
            expire_cache(root.path(), "expired");
            let future = (chrono::Utc::now() + chrono::Duration::days(1)).to_rfc3339();
            service
                .cache
                .refresh_expiry("o", "valid", Some(&future))
                .await
                .unwrap();
            let expired_task = partial(
                &service,
                root.path(),
                "o",
                "expired",
                Some("2000-01-01T00:00:00Z"),
            );
            let valid_task = partial(&service, root.path(), "o", "valid", Some(&future));
            let permanent_task = partial(&service, root.path(), "o", "permanent", None);

            service
                .reconcile_lifecycle_with(Some("o".into()), |_, _| async move {
                    if timeout {
                        std::future::pending::<()>().await;
                    }
                    Err(TransferError::http(503))
                })
                .await
                .unwrap();

            assert!(
                !expired.exists(),
                "expired cache remained after timeout={timeout}"
            );
            assert!(!Path::new(expired_task.partial.as_ref().unwrap()).exists());
            for path in [
                valid,
                permanent,
                valid_task.partial.unwrap().into(),
                permanent_task.partial.unwrap().into(),
            ] {
                assert_eq!(std::fs::read(path).unwrap(), b"bytes");
            }
        }
    }

    #[tokio::test]
    async fn offline_partial_expiry_preserves_saved_files_and_retry_path_for_all_owners() {
        let root = tempfile::tempdir().unwrap();
        let service = open_service(root.path()).await;
        let expired = Some("2000-01-01T00:00:00Z");
        let paused = partial(&service, root.path(), "a", "paused", expired);
        let mut completed = partial(&service, root.path(), "b", "completed", expired);
        completed.snapshot.state = TaskState::Completed;
        service.db.save(&completed).unwrap();
        std::fs::write(&completed.path, b"employee saved bytes").unwrap();
        let unrelated = root.path().join("unrelated.part");
        std::fs::write(&unrelated, b"unrelated bytes").unwrap();

        service.reconcile_lifecycle().await.unwrap();

        for task in [&paused, &completed] {
            assert!(
                !Path::new(task.partial.as_ref().unwrap()).exists(),
                "expired owned partial remained"
            );
            let stored = service.db.get(&task.snapshot.id, &task.owner).unwrap();
            assert_eq!(
                stored.partial, task.partial,
                "retry must retain the owned staging path"
            );
            assert!(stored.manifest.is_none());
            assert!(stored.verified.is_empty());
            assert_eq!(stored.snapshot.state, task.snapshot.state);
        }
        let retry = service.db.get(&paused.snapshot.id, "a").unwrap();
        assert_eq!(retry.snapshot.bytes_completed, 0);
        assert!(validate_destination(&retry.path).is_ok());
        assert_eq!(
            std::fs::read(&completed.path).unwrap(),
            b"employee saved bytes"
        );
        assert_eq!(std::fs::read(unrelated).unwrap(), b"unrelated bytes");
    }

    #[tokio::test]
    async fn maintenance_applies_promotion_before_expiry_and_persists_it_for_logout() {
        let root = tempfile::tempdir().unwrap();
        let service = open_service(root.path()).await;
        let cache_path = cached(&service, root.path(), "o", "promoted").await;
        expire_cache(root.path(), "promoted");
        let task = partial(
            &service,
            root.path(),
            "o",
            "promoted",
            Some("2000-01-01T00:00:00Z"),
        );

        service
            .reconcile_lifecycle_with(Some("o".into()), |_, version| async move {
                Ok(manifest(&version, None))
            })
            .await
            .unwrap();
        // A running worker still owns its pre-promotion task snapshot.
        service.persist(&task).unwrap();
        service.reconcile_lifecycle().await.unwrap();

        assert!(!service.cache.is_expired("o", "promoted").unwrap());
        let stored = service.db.get(&task.snapshot.id, "o").unwrap();
        assert!(stored.manifest.unwrap().expires_at.is_none());
        assert_eq!(stored.verified, vec![true]);
        assert_eq!(std::fs::read(cache_path).unwrap(), b"bytes");
        assert_eq!(std::fs::read(task.partial.unwrap()).unwrap(), b"bytes");
    }

    #[tokio::test]
    async fn bounded_maintenance_defers_expiry_until_that_versions_refresh_turn() {
        let root = tempfile::tempdir().unwrap();
        let service = open_service(root.path()).await;
        for index in 0..16 {
            partial(&service, root.path(), "o", &format!("a-{index:02}"), None);
        }
        let cache_path = cached(&service, root.path(), "o", "z-promoted").await;
        expire_cache(root.path(), "z-promoted");
        let task = partial(
            &service,
            root.path(),
            "o",
            "z-promoted",
            Some("2000-01-01T00:00:00Z"),
        );

        service
            .reconcile_lifecycle_with(Some("o".into()), |_, version| async move {
                assert_ne!(version, "z-promoted");
                Err(TransferError::http(503))
            })
            .await
            .unwrap();
        assert!(cache_path.exists());
        assert!(Path::new(task.partial.as_ref().unwrap()).exists());
        service
            .reconcile_lifecycle_with(Some("o".into()), |_, version| async move {
                if version == "z-promoted" {
                    Ok(manifest(&version, None))
                } else {
                    Err(TransferError::http(503))
                }
            })
            .await
            .unwrap();
        service.reconcile_lifecycle().await.unwrap();
        assert_eq!(std::fs::read(cache_path).unwrap(), b"bytes");
        assert_eq!(std::fs::read(task.partial.unwrap()).unwrap(), b"bytes");
    }
}

#[cfg(test)]
mod final_lifecycle_tests {
    use super::*;
    #[tokio::test]
    async fn owned_partial_cleanup_preserves_saved_and_unrelated_files() {
        let root = tempfile::tempdir().unwrap();
        let saved = root.path().canonicalize().unwrap().join("saved.bin");
        std::fs::write(&saved, b"saved").unwrap();
        let mut task = Task::new(
            "owner".into(),
            Direction::Download,
            uuid::Uuid::new_v4().to_string(),
            saved.to_str().unwrap().into(),
        );
        let part = root
            .path()
            .canonicalize()
            .unwrap()
            .join(format!(".linknestx-{}.part", task.snapshot.id));
        std::fs::write(&part, b"partial").unwrap();
        task.partial = Some(part.to_str().unwrap().into());
        let unrelated = root.path().join("other.part");
        std::fs::write(&unrelated, b"other").unwrap();
        remove_owned_partial(&task).await.unwrap();
        assert!(!part.exists());
        assert_eq!(std::fs::read(&saved).unwrap(), b"saved");
        task.partial = Some(unrelated.to_str().unwrap().into());
        assert!(remove_owned_partial(&task).await.is_err());
        assert!(unrelated.exists());
    }
    #[cfg(unix)]
    #[tokio::test]
    async fn partial_cleanup_rejects_symlink() {
        use std::os::unix::fs::symlink;
        let root = tempfile::tempdir().unwrap();
        let saved = root.path().join("saved");
        std::fs::write(&saved, b"saved").unwrap();
        let mut task = Task::new(
            "owner".into(),
            Direction::Download,
            uuid::Uuid::new_v4().to_string(),
            saved.to_str().unwrap().into(),
        );
        let part = root
            .path()
            .canonicalize()
            .unwrap()
            .join(format!(".linknestx-{}.part", task.snapshot.id));
        symlink(&saved, &part).unwrap();
        task.partial = Some(part.to_str().unwrap().into());
        assert!(remove_owned_partial(&task).await.is_err());
        assert_eq!(std::fs::read(&saved).unwrap(), b"saved");
    }
}

/// Paths come only from local task creation. Never remove task.path or follow symlinks.
pub(crate) async fn remove_owned_partial(task: &Task) -> Result<()> {
    if task.snapshot.direction != Direction::Download {
        return Ok(());
    }
    let Some(value) = &task.partial else {
        return Ok(());
    };
    uuid::Uuid::parse_str(&task.snapshot.id)
        .map_err(|_| TransferError::new("临时文件任务标识无效"))?;
    let path = Path::new(value);
    let saved = Path::new(&task.path);
    let parent = saved
        .parent()
        .ok_or_else(|| TransferError::new("临时文件位置无效"))?;
    if !path.is_absolute()
        || path == saved
        || path.parent() != Some(parent)
        || path.file_name().and_then(|n| n.to_str())
            != Some(&format!(".linknestx-{}.part", task.snapshot.id))
        || parent.canonicalize()? != parent
    {
        return Err(TransferError::new("临时文件所有权检查失败"));
    }
    match tokio::fs::symlink_metadata(path).await {
        Ok(m) if m.is_file() && !m.file_type().is_symlink() => tokio::fs::remove_file(path).await?,
        Ok(_) => return Err(TransferError::new("临时文件不是普通文件")),
        Err(e) if e.kind() == std::io::ErrorKind::NotFound => {}
        Err(e) => return Err(e.into()),
    };
    Ok(())
}

#[cfg(test)]
mod final_authority_tests {
    use super::*;
    use crate::events::NoopEvents;
    #[tokio::test]
    async fn authority_denial_cleans_completed_paused_and_failed_versions_even_with_sharing_disabled(
    ) {
        for (status, state) in [
            (403, TaskState::Completed),
            (404, TaskState::Paused),
            (410, TaskState::Failed),
        ] {
            let root = tempfile::tempdir().unwrap();
            let root_path = root.path().canonicalize().unwrap();
            let auth = Arc::new(AuthManager::new(&root_path).unwrap());
            let service = TransferService::open(&root_path, auth, Arc::new(NoopEvents)).unwrap();
            service.policy.write().await.enabled = false;
            let version = uuid::Uuid::new_v4().to_string();
            let chunk = Chunk {
                index: 0,
                hash: hash(b"content"),
                size: 7,
            };
            service
                .cache
                .store("o", &version, &chunk, b"content", None)
                .await
                .unwrap();
            let saved = root_path.join("employee.bin");
            std::fs::write(&saved, b"saved forever").unwrap();
            let mut task = Task::new(
                "o".into(),
                Direction::Download,
                version.clone(),
                saved.to_string_lossy().into(),
            );
            task.snapshot.state = state.clone();
            let part = root_path.join(format!(".linknestx-{}.part", task.snapshot.id));
            std::fs::write(&part, b"owned bytes").unwrap();
            task.partial = Some(part.to_string_lossy().into());
            service.db.save(&task).unwrap();
            service
                .apply_authority_result("o", &version, Err(TransferError::http(503)))
                .await
                .unwrap();
            assert!(part.exists());
            assert!(service
                .cache
                .read("o", &version, &chunk)
                .await
                .unwrap()
                .is_some());
            service
                .apply_authority_result("o", &version, Err(TransferError::http(status)))
                .await
                .unwrap();
            assert!(!part.exists());
            assert!(service.cache.versions("o").unwrap().is_empty());
            assert_eq!(std::fs::read(&saved).unwrap(), b"saved forever");
            let stored = service.db.get(&task.snapshot.id, "o").unwrap();
            assert!(stored.partial.is_none());
            assert_eq!(
                stored.snapshot.state,
                if state == TaskState::Completed {
                    TaskState::Completed
                } else {
                    TaskState::Failed
                }
            );
            service.shutdown().await.unwrap();
            let reopened = Cache::open(&root_path, 100).unwrap();
            assert!(reopened.versions("o").unwrap().is_empty());
        }
    }
    #[tokio::test]
    async fn promotion_refreshes_expiry_before_cache_read_or_prune() {
        let root = tempfile::tempdir().unwrap();
        let cache = Cache::open(root.path(), 100).unwrap();
        let c = Chunk {
            index: 0,
            hash: hash(b"abc"),
            size: 3,
        };
        cache.store("o", "v", &c, b"abc", None).await.unwrap();
        let db = rusqlite::Connection::open(root.path().join("cache/index.sqlite3")).unwrap();
        db.execute("UPDATE chunks SET expires=0", []).unwrap();
        assert!(cache.is_expired("o", "v").unwrap());
        cache.refresh_expiry("o", "v", None).await.unwrap();
        cache.prune().await.unwrap();
        assert_eq!(cache.read("o", "v", &c).await.unwrap().unwrap(), b"abc");
    }
    #[test]
    fn administrative_policy_caps_and_local_pause_combine() {
        let mut local = SharingPolicy::default();
        local.max_concurrent = 4;
        local.bandwidth_limit_bps = 2000;
        let mut admin = AdminSharingPolicy {
            enabled: true,
            max_concurrent: 2,
            bandwidth_limit_bps: 1000,
        };
        let p = admin.effective(&local);
        assert_eq!(p.max_concurrent, 2);
        assert_eq!(p.bandwidth_limit_bps, 1000);
        local.enabled = false;
        assert!(!admin.effective(&local).enabled);
        local.enabled = true;
        admin.enabled = false;
        assert!(!admin.effective(&local).enabled);
        admin.enabled = true;
        admin.max_concurrent = 0;
        admin.bandwidth_limit_bps = 0;
        assert_eq!(admin.effective(&local).max_concurrent, 4);
        assert_eq!(admin.effective(&local).bandwidth_limit_bps, 2000);
    }
}
