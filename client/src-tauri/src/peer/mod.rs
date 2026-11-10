pub mod allocation;
pub mod audit;
mod connection;
pub mod protocol;
mod signal;
mod source;
use source::PeerSource;

use crate::{
    auth::session::AuthManager,
    events::EventSink,
    transfer::{
        cache::Cache,
        models::{Chunk, Manifest, SharingPolicy},
        scheduler::{BandwidthLimiter, ChunkSource},
    },
    Result, TransferError,
};
use connection::PeerConnection;
use serde::Deserialize;
use serde_json::{json, Value};
use std::{
    collections::HashMap,
    sync::{
        atomic::{AtomicUsize, Ordering},
        Arc, Weak,
    },
    time::{Duration, Instant},
};
use tokio::sync::{mpsc, Mutex, RwLock};
use tokio_util::sync::CancellationToken;
use webrtc::ice_transport::ice_server::RTCIceServer;

#[derive(Clone, Deserialize)]
#[serde(rename_all = "camelCase")]
struct Candidate {
    device_id: String,
    user_id: String,
    indexes: Vec<usize>,
}
#[derive(Clone, Deserialize)]
struct IceServer {
    urls: Vec<String>,
    #[serde(default)]
    username: String,
    #[serde(default)]
    credential: String,
}
impl From<IceServer> for RTCIceServer {
    fn from(value: IceServer) -> Self {
        Self {
            urls: value.urls,
            username: value.username,
            credential: value.credential,
            ..Default::default()
        }
    }
}
#[derive(Deserialize)]
#[serde(rename_all = "camelCase")]
struct Discovery {
    sources: Vec<Candidate>,
    ice_servers: Vec<IceServer>,
}
struct Discovered {
    at: Instant,
    sources: Vec<Candidate>,
    ice: Vec<RTCIceServer>,
}
struct Run {
    cancel: CancellationToken,
    handle: tokio::task::JoinHandle<()>,
    sender: mpsc::Sender<Value>,
}
pub struct PeerManager {
    pub audit: Arc<audit::AuditOutbox>,
    auth: Arc<AuthManager>,
    cache: Arc<Cache>,
    policy: Arc<RwLock<SharingPolicy>>,
    events: Arc<dyn EventSink>,
    run: Mutex<Option<Run>>,
    lifecycle: Mutex<()>,
    connections: Mutex<HashMap<String, Arc<PeerConnection>>>,
    discoveries: Mutex<HashMap<String, Discovered>>,
    manifests: RwLock<HashMap<String, Manifest>>,
    negotiating: Mutex<()>,
    uploading: AtomicUsize,
    bandwidth: BandwidthLimiter,
    version_bandwidth: Mutex<HashMap<String, Arc<BandwidthLimiter>>>,
}
impl PeerManager {
    pub fn new(
        root: &std::path::Path,
        auth: Arc<AuthManager>,
        cache: Arc<Cache>,
        policy: Arc<RwLock<SharingPolicy>>,
        events: Arc<dyn EventSink>,
    ) -> Result<Arc<Self>> {
        Ok(Arc::new(Self {
            audit: Arc::new(audit::AuditOutbox::open(root)?),
            auth,
            cache,
            policy,
            events,
            run: Mutex::new(None),
            lifecycle: Mutex::new(()),
            connections: Mutex::new(HashMap::new()),
            discoveries: Mutex::new(HashMap::new()),
            manifests: RwLock::new(HashMap::new()),
            negotiating: Mutex::new(()),
            uploading: AtomicUsize::new(0),
            bandwidth: BandwidthLimiter::default(),
            version_bandwidth: Mutex::new(HashMap::new()),
        }))
    }
    pub async fn start(self: &Arc<Self>) {
        let _guard = self.lifecycle.lock().await;
        self.stop_inner().await;
        if !self.policy.read().await.enabled || self.auth.current().await.is_err() {
            return;
        }
        let cancel = CancellationToken::new();
        let token = cancel.clone();
        let (sender, rx) = mpsc::channel(64);
        let manager = self.clone();
        let handle = tokio::spawn(async move {
            manager.signal_loop(token, rx).await;
        });
        *self.run.lock().await = Some(Run {
            cancel,
            handle,
            sender,
        });
    }
    pub async fn stop(&self) {
        let _guard = self.lifecycle.lock().await;
        self.stop_inner().await;
    }
    async fn stop_inner(&self) {
        if let Some(run) = self.run.lock().await.take() {
            run.cancel.cancel();
            let mut h = run.handle;
            if tokio::time::timeout(Duration::from_millis(500), &mut h)
                .await
                .is_err()
            {
                h.abort();
                let _ = h.await;
            }
        }
        self.close_connections().await;
        self.discoveries.lock().await.clear();
        self.manifests.write().await.clear();
        if let Ok(owner) = self.auth.owner().await {
            let _ = tokio::time::timeout(
                Duration::from_secs(2),
                self.auth
                    .json_for(&owner, "POST", "/api/v1/p2p/withdraw", Some(json!({}))),
            )
            .await;
        }
    }
    async fn close_connections(&self) {
        let connections = std::mem::take(&mut *self.connections.lock().await);
        futures_util::future::join_all(
            connections
                .into_values()
                .map(|connection| async move { connection.shutdown().await }),
        )
        .await;
    }
    pub fn active_uploads(&self) -> usize {
        self.uploading.load(Ordering::SeqCst)
    }
    pub fn set_bandwidth_limit(&self, limit: u64) {
        self.bandwidth.set_limit(limit);
    }
    pub async fn connection_count(&self) -> usize {
        self.connections.lock().await.len()
    }
    async fn signal(&self, value: Value) -> Result<()> {
        if serde_json::to_vec(&value)?.len() > 65536 {
            return Err(TransferError::new("对等信令过大"));
        }
        self.run
            .lock()
            .await
            .as_ref()
            .ok_or_else(|| TransferError::new("对等信令离线"))?
            .sender
            .try_send(value)
            .map_err(|_| TransferError::new("对等信令繁忙"))
    }
    pub async fn invalidate_version(&self, version: &str) {
        self.manifests.write().await.remove(version);
        self.discoveries.lock().await.remove(version);
        self.version_bandwidth.lock().await.remove(version);
        self.close_version_connections(version).await;
        if let Ok(owner) = self.auth.owner().await {
            let _ = tokio::time::timeout(
                Duration::from_secs(2),
                self.auth.json_for(
                    &owner,
                    "POST",
                    "/api/v1/p2p/withdraw",
                    Some(json!({"versionId":version})),
                ),
            )
            .await;
        }
    }
    async fn close_version_connections(&self, version: &str) {
        let connections: Vec<_> = {
            let mut map = self.connections.lock().await;
            let ids: Vec<_> = map
                .iter()
                .filter(|(_, c)| c.version == version)
                .map(|(id, _)| id.clone())
                .collect();
            ids.into_iter().filter_map(|id| map.remove(&id)).collect()
        };
        for c in connections {
            c.shutdown().await;
        }
    }
    pub async fn refresh_manifest(&self, owner: &str, version: &str) -> Result<Manifest> {
        crate::transfer::models::segment(version)?;
        let value = self
            .auth
            .json_for(
                owner,
                "GET",
                &format!("/api/v1/file-versions/{version}/manifest"),
                None,
            )
            .await?;
        let m: Manifest = serde_json::from_value(value)?;
        m.validate()?;
        if m.version_id != version {
            return Err(TransferError::new("文件版本不匹配"));
        }
        let changed = {
            let old = self.manifests.read().await;
            old.get(version)
                .is_some_and(|v| v.sharing_policy != m.sharing_policy)
        };
        if changed || !m.sharing_policy.enabled {
            self.close_version_connections(version).await;
            self.discoveries.lock().await.remove(version);
        }
        self.cache
            .refresh_expiry(owner, version, m.expires_at.as_deref())
            .await?;
        self.manifests
            .write()
            .await
            .insert(version.into(), m.clone());
        if !m.sharing_policy.enabled {
            let _ = self
                .auth
                .json_for(
                    owner,
                    "POST",
                    "/api/v1/p2p/withdraw",
                    Some(json!({"versionId":version})),
                )
                .await;
        }
        Ok(m)
    }
    async fn manifest(&self, version: &str) -> Result<Manifest> {
        if let Some(m) = self.manifests.read().await.get(version) {
            return Ok(m.clone());
        }
        let owner = self.auth.owner().await?;
        self.refresh_manifest(&owner, version).await
    }
    pub async fn sources(self: &Arc<Self>, manifest: &Manifest) -> Vec<Arc<dyn ChunkSource>> {
        if !manifest.sharing_policy.enabled || self.run.lock().await.is_none() {
            return vec![];
        }
        {
            let mut known = self.manifests.write().await;
            let latest = known
                .entry(manifest.version_id.clone())
                .or_insert_with(|| manifest.clone());
            if !latest.sharing_policy.enabled {
                return vec![];
            }
        }
        let refresh = self
            .discoveries
            .lock()
            .await
            .get(&manifest.version_id)
            .is_none_or(|v| v.at.elapsed() > Duration::from_secs(2));
        if refresh {
            if let Ok(Ok(value)) = tokio::time::timeout(
                Duration::from_secs(2),
                self.auth.json(
                    "GET",
                    &format!("/api/v1/p2p/sources?versionId={}", manifest.version_id),
                    None,
                ),
            )
            .await
            {
                if let Ok(mut d) = serde_json::from_value::<Discovery>(value) {
                    d.sources.retain(|s| {
                        uuid::Uuid::parse_str(&s.device_id).is_ok()
                            && uuid::Uuid::parse_str(&s.user_id).is_ok()
                            && s.indexes.len() <= 5000
                            && s.indexes.iter().all(|i| *i < manifest.chunks.len())
                    });
                    d.sources.sort_by(|a, b| a.device_id.cmp(&b.device_id));
                    d.sources.truncate(12);
                    self.discoveries.lock().await.insert(
                        manifest.version_id.clone(),
                        Discovered {
                            at: Instant::now(),
                            sources: d.sources,
                            ice: d.ice_servers.into_iter().map(Into::into).collect(),
                        },
                    );
                }
            }
        }
        self.discoveries
            .lock()
            .await
            .get(&manifest.version_id)
            .map(|d| {
                d.sources
                    .iter()
                    .map(|s| {
                        Arc::new(PeerSource {
                            manager: Arc::downgrade(self),
                            candidate: s.clone(),
                            ice: d.ice.clone(),
                        }) as Arc<dyn ChunkSource>
                    })
                    .collect()
            })
            .unwrap_or_default()
    }
    async fn connect(
        self: &Arc<Self>,
        candidate: &Candidate,
        version: &str,
        ice: Vec<RTCIceServer>,
    ) -> Result<Arc<PeerConnection>> {
        let _guard = self.negotiating.lock().await;
        {
            let map = self.connections.lock().await;
            if let Some(c) = map.values().find(|c| {
                c.remote_device == candidate.device_id
                    && c.version == version
                    && !c.cancel.is_cancelled()
            }) {
                return Ok(c.clone());
            }
        }
        self.connections
            .lock()
            .await
            .retain(|_, c| !c.cancel.is_cancelled());
        if self.connections.lock().await.len() >= 12 {
            return Err(TransferError::new("对等连接已达上限"));
        }
        let id = uuid::Uuid::new_v4().to_string();
        let conn = PeerConnection::new(
            self,
            id.clone(),
            version.into(),
            candidate.device_id.clone(),
            candidate.user_id.clone(),
            ice,
            true,
        )
        .await?;
        self.connections.lock().await.insert(id, conn.clone());
        if let Err(e) = conn.offer().await {
            conn.shutdown().await;
            return Err(e);
        }
        Ok(conn)
    }
    async fn recover_cache(&self, owner: &str) -> Result<()> {
        self.events.emit("lifecycle.refresh", json!({}));
        for version in self.cache.versions(owner)? {
            if let Ok(manifest) = self.refresh_manifest(owner, &version).await {
                self.cache
                    .refresh_expiry(owner, &version, manifest.expires_at.as_deref())
                    .await?;
                self.cache.recover(owner, &manifest).await?;
            }
        }
        Ok(())
    }
    async fn advertise(
        &self,
        owner: &str,
        announced: &mut HashMap<String, Vec<usize>>,
    ) -> Result<()> {
        let catalog = self.cache.inventory(owner)?;
        let mut current: HashMap<String, Vec<usize>> = HashMap::new();
        for (version, chunk) in catalog {
            if let Ok(manifest) = self.manifest(&version).await {
                if manifest.sharing_policy.enabled
                    && manifest.chunks.get(chunk.index) == Some(&chunk)
                {
                    current.entry(version).or_default().push(chunk.index);
                }
            }
        }
        for indexes in current.values_mut() {
            indexes.sort_unstable();
            indexes.dedup();
            indexes.truncate(5000);
        }
        for version in announced.keys() {
            current.entry(version.clone()).or_default();
        }
        let mut rejected = Vec::new();
        for (version, indexes) in &current {
            if announced.get(version) != Some(indexes) {
                if let Err(error) = self
                    .auth
                    .json_for(
                        owner,
                        "POST",
                        "/api/v1/p2p/announce",
                        Some(json!({"versionId":version,"indexes":indexes})),
                    )
                    .await
                {
                    if error.status == Some(409) {
                        self.discoveries.lock().await.remove(version);
                        self.manifests.write().await.remove(version);
                        rejected.push(version.clone());
                    } else if matches!(error.status, Some(403 | 404 | 410)) {
                        self.cache.revoke(owner, version).await?;
                        self.manifests.write().await.remove(version);
                        rejected.push(version.clone());
                    } else {
                        return Err(error);
                    }
                }
            }
        }
        for version in rejected {
            current.remove(&version);
        }
        current.retain(|_, v| !v.is_empty());
        *announced = current;
        Ok(())
    }
    fn upload_permit(&self, maximum: usize) -> Result<UploadPermit<'_>> {
        let mut count = self.uploading.load(Ordering::SeqCst);
        loop {
            if count >= maximum {
                return Err(TransferError::new("共享上传繁忙"));
            }
            match self.uploading.compare_exchange_weak(
                count,
                count + 1,
                Ordering::SeqCst,
                Ordering::SeqCst,
            ) {
                Ok(_) => break,
                Err(current) => count = current,
            }
        }
        self.events.emit(
            "sharing.activity",
            json!({"activeUploads":self.uploading.load(Ordering::SeqCst)}),
        );
        Ok(UploadPermit(self))
    }
}
struct UploadPermit<'a>(&'a PeerManager);
impl Drop for UploadPermit<'_> {
    fn drop(&mut self) {
        self.0.uploading.fetch_sub(1, Ordering::SeqCst);
        self.0.events.emit(
            "sharing.activity",
            json!({"activeUploads":self.0.uploading.load(Ordering::SeqCst)}),
        );
    }
}
