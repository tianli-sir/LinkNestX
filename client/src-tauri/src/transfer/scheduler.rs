use super::models::{hash, Chunk, Manifest};
use crate::{
    auth::session::{limited_body_observed, AuthManager},
    Result, TransferError,
};
use async_trait::async_trait;
use std::{sync::Arc, time::Duration};
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
    fn has_chunk(&self, _chunk: &Chunk) -> bool {
        true
    }
    fn is_authoritative(&self) -> bool {
        false
    }
    async fn fetch(&self, owner: &str, manifest: &Manifest, chunk: &Chunk) -> Result<Vec<u8>>;
    async fn fetch_for_task(
        &self,
        _task_id: &str,
        owner: &str,
        manifest: &Manifest,
        chunk: &Chunk,
    ) -> Result<Vec<u8>> {
        self.fetch(owner, manifest, chunk).await
    }
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
        let response = self
            .0
            .request_for_transfer(
                owner,
                "GET",
                &format!(
                    "/api/v1/files/{}/chunks/{}",
                    manifest.version_id, chunk.hash
                ),
                None,
                false,
                Some(task_id),
            )
            .await?;
        limited_body_observed(
            response,
            chunk.size as usize,
            super::metrics::observer(owner, task_id),
        )
        .await
    }
}
#[derive(Default)]
pub struct SourceHealth {
    failures: std::sync::Mutex<std::collections::HashMap<String, Option<tokio::time::Instant>>>,
    initial_server_batch_completed: std::sync::atomic::AtomicBool,
}
impl SourceHealth {
    pub fn initial_server_batch_completed(&self) {
        self.initial_server_batch_completed
            .store(true, std::sync::atomic::Ordering::SeqCst);
    }
    fn eligible(&self, name: &str) -> bool {
        match self.failures.lock().unwrap().get(name) {
            None => true,
            Some(Some(until)) => tokio::time::Instant::now() >= *until,
            Some(None) => false,
        }
    }
    fn failed(&self, name: String, corrupt: bool) {
        let mut entries = self.failures.lock().unwrap();
        if corrupt {
            entries.insert(name, None);
        } else if !matches!(entries.get(&name), Some(None)) {
            entries.insert(
                name,
                Some(tokio::time::Instant::now() + Duration::from_secs(2)),
            );
        }
    }
}
pub async fn fetch(
    sources: &[Arc<dyn ChunkSource>],
    owner: &str,
    manifest: &Manifest,
    chunk: &Chunk,
    cancel: &CancellationToken,
) -> Result<(String, Vec<u8>)> {
    fetch_with_health(
        sources,
        owner,
        manifest,
        chunk,
        cancel,
        &SourceHealth::default(),
        &uuid::Uuid::new_v4().to_string(),
    )
    .await
}
pub async fn fetch_with_health(
    sources: &[Arc<dyn ChunkSource>],
    owner: &str,
    manifest: &Manifest,
    chunk: &Chunk,
    cancel: &CancellationToken,
    health: &SourceHealth,
    task_id: &str,
) -> Result<(String, Vec<u8>)> {
    let mut last = TransferError::new("没有可用的下载来源");
    for attempt in 0..3 {
        let mut eligible: Vec<_> = sources
            .iter()
            .filter(|s| !s.is_authoritative() && s.has_chunk(chunk) && health.eligible(&s.name()))
            .collect();
        if !eligible.is_empty() {
            let shift = chunk.index % eligible.len();
            eligible.rotate_left(shift);
        }
        for server in sources.iter().filter(|s| s.is_authoritative()) {
            // Cold starts already contributed distinct server chunks. Prefer any
            // newly advertised peer bytes, including a lone third-index chunk.
            if chunk.index % 3 == 2
                && !health
                    .initial_server_batch_completed
                    .load(std::sync::atomic::Ordering::SeqCst)
            {
                eligible.insert(0, server);
            } else {
                eligible.push(server);
            }
        }
        for source in eligible {
            let response = tokio::select! {_=cancel.cancelled()=>return Err(TransferError::new("传输已暂停")),r=tokio::time::timeout(Duration::from_secs(if source.is_authoritative(){90}else{12}),source.fetch_for_task(task_id,owner,manifest,chunk))=>r.unwrap_or_else(|_|Err(TransferError::new("下载来源响应超时")))};
            let corrupt = matches!(&response,Ok(bytes) if bytes.len() as u64 != chunk.size || hash(bytes) != chunk.hash);
            match response {
                Ok(bytes) if bytes.len() as u64 == chunk.size && hash(&bytes) == chunk.hash => {
                    return Ok((source.name(), bytes))
                }
                Ok(_) => last = TransferError::new("下载分块校验失败"),
                Err(e)
                    if source.is_authoritative()
                        && matches!(e.status, Some(401 | 403 | 404 | 410)) =>
                {
                    return Err(e)
                }
                Err(e) => last = e,
            }
            if !source.is_authoritative() {
                health.failed(source.name(), corrupt);
            }
        }
        if attempt < 2 {
            tokio::select! {_=cancel.cancelled()=>return Err(TransferError::new("传输已暂停")),_=tokio::time::sleep(Duration::from_millis(100*(attempt+1)))=>{}}
        }
    }
    Err(last)
}
pub struct BandwidthLimiter {
    turn: Mutex<()>,
    rate: tokio::sync::watch::Sender<Option<u64>>,
}
impl Default for BandwidthLimiter {
    fn default() -> Self {
        Self {
            turn: Mutex::new(()),
            rate: tokio::sync::watch::channel(None).0,
        }
    }
}
impl BandwidthLimiter {
    pub fn set_limit(&self, limit: u64) {
        self.rate.send_replace(Some(limit));
    }
    pub async fn wait(&self, bytes: u64, limit: u64, cancel: &CancellationToken) -> Result<()> {
        // Only the current sender owns time. Queued/cancelled requests never reserve
        // future time, and dropping this future releases its FIFO mutex permit.
        let _turn = tokio::select! { biased;
            _=cancel.cancelled()=>return Err(TransferError::new("传输已暂停")),
            turn=self.turn.lock()=>turn,
        };
        let mut changes = self.rate.subscribe();
        loop {
            let rate = (*changes.borrow_and_update()).unwrap_or(limit);
            if cancel.is_cancelled() {
                return Err(TransferError::new("传输已暂停"));
            }
            if rate == 0 {
                return Ok(());
            }
            let delay = Duration::from_secs_f64(bytes as f64 / rate as f64);
            tokio::select! { biased;
                _=cancel.cancelled()=>return Err(TransferError::new("传输已暂停")),
                _=changes.changed()=>{},
                _=tokio::time::sleep(delay)=>return Ok(()),
            }
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[tokio::test(start_paused = true)]
    async fn transient_peer_errors_cool_down_but_corruption_stays_quarantined() {
        let health = SourceHealth::default();
        health.failed("expired".into(), false);
        health.failed("corrupt".into(), true);
        assert!(!health.eligible("expired") && !health.eligible("corrupt"));
        tokio::time::advance(Duration::from_secs(2)).await;
        assert!(health.eligible("expired"));
        assert!(!health.eligible("corrupt"));
        health.failed("corrupt".into(), false);
        tokio::time::advance(Duration::from_secs(3)).await;
        assert!(!health.eligible("corrupt"));
    }

    #[tokio::test(start_paused = true)]
    async fn live_rate_change_restarts_only_current_wait_without_retaining_old_debt() {
        let limiter = Arc::new(BandwidthLimiter::default());
        limiter.set_limit(1000);
        let waiting = {
            let limiter = limiter.clone();
            tokio::spawn(async move { limiter.wait(12000, 1000, &CancellationToken::new()).await })
        };
        tokio::task::yield_now().await;
        tokio::time::advance(Duration::from_secs(1)).await;
        limiter.set_limit(12000);
        tokio::task::yield_now().await;
        tokio::time::advance(Duration::from_millis(999)).await;
        assert!(!waiting.is_finished());
        tokio::time::advance(Duration::from_millis(1)).await;
        tokio::task::yield_now().await;
        assert!(waiting.is_finished());
        waiting.await.unwrap().unwrap();
    }

    #[tokio::test(start_paused = true)]
    async fn cancelled_bandwidth_wait_does_not_charge_unsent_bytes_to_next_sender() {
        let limiter = Arc::new(BandwidthLimiter::default());
        let cancel = CancellationToken::new();
        let first = {
            let limiter = limiter.clone();
            let cancel = cancel.clone();
            tokio::spawn(async move { limiter.wait(12000, 1000, &cancel).await })
        };
        tokio::task::yield_now().await;
        tokio::time::advance(Duration::from_secs(1)).await;
        cancel.cancel();
        assert!(first.await.unwrap().is_err());
        let next = {
            let limiter = limiter.clone();
            tokio::spawn(async move { limiter.wait(1000, 1000, &CancellationToken::new()).await })
        };
        tokio::task::yield_now().await;
        tokio::time::advance(Duration::from_secs(1)).await;
        tokio::task::yield_now().await;
        assert!(
            next.is_finished(),
            "cancelled unsent payload must not retain future bandwidth debt"
        );
        next.await.unwrap().unwrap();
    }

    #[tokio::test(start_paused = true)]
    async fn cancelled_concurrent_waiters_cannot_bypass_aggregate_rate() {
        let limiter = Arc::new(BandwidthLimiter::default());
        let cancelled = CancellationToken::new();
        let first = {
            let limiter = limiter.clone();
            tokio::spawn(async move { limiter.wait(1000, 1000, &CancellationToken::new()).await })
        };
        tokio::task::yield_now().await;
        let second = {
            let limiter = limiter.clone();
            let cancelled = cancelled.clone();
            tokio::spawn(async move { limiter.wait(1000, 1000, &cancelled).await })
        };
        let third = {
            let limiter = limiter.clone();
            tokio::spawn(async move { limiter.wait(1000, 1000, &CancellationToken::new()).await })
        };
        tokio::task::yield_now().await;
        cancelled.cancel();
        assert!(second.await.unwrap().is_err());
        tokio::time::advance(Duration::from_millis(999)).await;
        assert!(!first.is_finished() && !third.is_finished());
        tokio::time::advance(Duration::from_millis(1)).await;
        first.await.unwrap().unwrap();
        tokio::task::yield_now().await;
        tokio::time::advance(Duration::from_millis(999)).await;
        assert!(
            !third.is_finished(),
            "two successful payloads need two aggregate intervals"
        );
        tokio::time::advance(Duration::from_millis(1)).await;
        tokio::task::yield_now().await;
        assert!(
            third.is_finished(),
            "cancelled queued waiter must leave no third interval"
        );
        third.await.unwrap().unwrap();
    }
}
