use super::*;
use async_trait::async_trait;
pub(super) struct PeerSource {
    pub(super) manager: Weak<PeerManager>,
    pub(super) candidate: Candidate,
    pub(super) ice: Vec<RTCIceServer>,
}
#[async_trait]
impl ChunkSource for PeerSource {
    fn name(&self) -> String {
        format!("peer:{}", self.candidate.device_id)
    }
    fn has_chunk(&self, chunk: &Chunk) -> bool {
        self.candidate.indexes.contains(&chunk.index)
    }
    async fn fetch(&self, owner: &str, manifest: &Manifest, chunk: &Chunk) -> Result<Vec<u8>> {
        self.fetch_for_task(&uuid::Uuid::new_v4().to_string(), owner, manifest, chunk)
            .await
    }
    async fn fetch_for_task(
        &self,
        task_id: &str,
        owner: &str,
        manifest: &Manifest,
        chunk: &Chunk,
    ) -> Result<Vec<u8>> {
        if !self.has_chunk(chunk) {
            return Err(TransferError::new("对等节点没有此分块"));
        }
        let manager = self
            .manager
            .upgrade()
            .ok_or_else(|| TransferError::new("共享服务已停止"))?;
        let current = manager.auth.current().await?;
        if current.owner() != owner {
            return Err(TransferError::new("登录账户已改变"));
        }
        if !manager.policy.read().await.enabled {
            return Err(TransferError::new("共享已关闭"));
        }
        for attempt in 0..2 {
            let connection = manager
                .connect(&self.candidate, &manifest.version_id, self.ice.clone())
                .await?;
            let result = connection.fetch(task_id, owner, manifest, chunk).await;
            if result.is_ok() || attempt == 1 || !connection.expired.load(Ordering::SeqCst) {
                return result;
            }
            // Server expiry ends this pair, not the source's eligibility. Negotiate a
            // fresh UUID with the same authenticated device and obtain a fresh grant.
        }
        Err(TransferError::new("对等连接续接失败"))
    }
}
