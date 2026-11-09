use super::models::{hash, Chunk, Manifest};
use crate::{
    auth::session::{limited_body, AuthManager},
    Result, TransferError,
};
use async_trait::async_trait;
use std::{
    sync::Arc,
    time::{Duration, Instant},
};
use tokio::sync::Mutex;
use tokio_util::sync::CancellationToken;

pub async fn cancellable<T>(
    cancel: &CancellationToken,
    operation: impl std::future::Future<Output = Result<T>>,
) -> Result<T> {
    tokio::select! {biased;_=cancel.cancelled()=>Err(TransferError::new("传输已暂停")),result=operation=>result}
}

/// Sources return bounded bytes; only this scheduler can accept them as verified progress.
#[async_trait]
pub trait ChunkSource: Send + Sync {
    fn name(&self) -> String;
    fn is_authoritative(&self) -> bool {
        false
    }
    async fn fetch(&self, owner: &str, manifest: &Manifest, chunk: &Chunk) -> Result<Vec<u8>>;
}
pub struct ServerSource(pub Arc<AuthManager>);
#[async_trait]
impl ChunkSource for ServerSource {
    fn is_authoritative(&self) -> bool {
        true
    }
    fn name(&self) -> String {
        "server".into()
    }
    async fn fetch(&self, owner: &str, manifest: &Manifest, chunk: &Chunk) -> Result<Vec<u8>> {
        let response = self
            .0
            .request_for(
                owner,
                "GET",
                &format!(
                    "/api/v1/files/{}/chunks/{}",
                    manifest.version_id, chunk.hash
                ),
                None,
                false,
            )
            .await?;
        limited_body(response, chunk.size as usize).await
    }
}
pub async fn fetch(
    sources: &[Arc<dyn ChunkSource>],
    owner: &str,
    manifest: &Manifest,
    chunk: &Chunk,
    cancel: &CancellationToken,
) -> Result<(String, Vec<u8>)> {
    let mut last = TransferError::new("没有可用的下载来源");
    // Distribute chunk preference across sources; a failed peer always falls back to the server.
    for shift in 0..sources.len() {
        let source = &sources[(chunk.index + shift) % sources.len()];
        let response = tokio::select! {_ = cancel.cancelled()=>return Err(TransferError::new("传输已暂停")),response=source.fetch(owner,manifest,chunk)=>response};
        match response {
            Ok(bytes) if bytes.len() as u64 == chunk.size && hash(&bytes) == chunk.hash => {
                return Ok((source.name(), bytes))
            }
            Ok(_) => last = TransferError::new("下载分块校验失败"),
            Err(error)
                if source.is_authoritative()
                    && matches!(error.status, Some(401 | 403 | 404 | 410)) =>
            {
                return Err(error)
            }
            Err(error) => last = error,
        }
    }
    Err(last)
}
pub struct BandwidthLimiter(Mutex<Instant>);
impl Default for BandwidthLimiter {
    fn default() -> Self {
        Self(Mutex::new(Instant::now()))
    }
}
impl BandwidthLimiter {
    pub async fn wait(&self, bytes: u64, limit: u64, cancel: &CancellationToken) -> Result<()> {
        if limit == 0 {
            return Ok(());
        }
        let deadline = {
            let mut next = self.0.lock().await;
            let now = Instant::now();
            *next = (*next).max(now) + Duration::from_secs_f64(bytes as f64 / limit as f64);
            *next
        };
        tokio::select! {_ = cancel.cancelled()=>Err(TransferError::new("传输已暂停")),_=tokio::time::sleep_until(deadline.into())=>Ok(())}
    }
}
