use linknestx::{
    transfer::{
        models::{hash, Chunk, Manifest},
        scheduler::{fetch_with_health, ChunkSource, SourceHealth},
    },
    Result, TransferError,
};
use std::sync::{
    atomic::{AtomicUsize, Ordering},
    Arc,
};
use tokio_util::sync::CancellationToken;

struct Source {
    name: String,
    authoritative: bool,
    calls: AtomicUsize,
    behavior: u8,
}
#[async_trait::async_trait]
impl ChunkSource for Source {
    fn name(&self) -> String {
        self.name.clone()
    }
    fn is_authoritative(&self) -> bool {
        self.authoritative
    }
    async fn fetch(&self, _: &str, _: &Manifest, _: &Chunk) -> Result<Vec<u8>> {
        let call = self.calls.fetch_add(1, Ordering::SeqCst);
        match (self.behavior, call) {
            (1, 0) => Err(TransferError::new("temporary connection expired")),
            (2, _) => Ok(b"bad".to_vec()),
            _ => Ok(b"abc".to_vec()),
        }
    }
}
fn manifest() -> Manifest {
    Manifest {
        file_id: "file".into(),
        version_id: "version".into(),
        name: "data".into(),
        size: 3,
        sha256: hash(b"abc"),
        chunk_size: 3,
        chunks: vec![Chunk {
            index: 0,
            hash: hash(b"abc"),
            size: 3,
        }],
        expires_at: None,
        visibility: "pool".into(),
        sharing_policy: Default::default(),
    }
}

#[tokio::test]
async fn cold_seed_reuses_the_only_available_peer_chunk_even_at_server_preferred_index() {
    let peer = Arc::new(Source {
        name: "peer:partial".into(),
        authoritative: false,
        calls: AtomicUsize::new(0),
        behavior: 0,
    });
    let server = Arc::new(Source {
        name: "server".into(),
        authoritative: true,
        calls: AtomicUsize::new(0),
        behavior: 0,
    });
    let sources: Vec<Arc<dyn ChunkSource>> = vec![peer.clone(), server.clone()];
    let health = SourceHealth::default();
    let mut m = manifest();
    m.size = 9;
    m.sha256 = hash(b"abcabcabc");
    m.chunks = (0..3)
        .map(|index| Chunk {
            index,
            hash: hash(b"abc"),
            size: 3,
        })
        .collect();
    m.validate().unwrap();
    let cancel = CancellationToken::new();
    let warm = fetch_with_health(
        &sources,
        "owner",
        &m,
        &m.chunks[2],
        &cancel,
        &health,
        "task",
    )
    .await
    .unwrap();
    assert_eq!(
        warm.0, "server",
        "warm scheduling retains mixed-source preference"
    );
    health.initial_server_batch_completed();
    let cold = fetch_with_health(
        &sources,
        "owner",
        &m,
        &m.chunks[2],
        &cancel,
        &health,
        "task",
    )
    .await
    .unwrap();
    assert_eq!(
        cold.0, "peer:partial",
        "a completed initial server batch must not suppress the only available peer chunk"
    );
    assert_eq!(peer.calls.load(Ordering::SeqCst), 1);
    assert_eq!(server.calls.load(Ordering::SeqCst), 1);
}

#[tokio::test(start_paused = true)]
async fn transient_peer_supplies_later_chunk_in_same_task_but_corruption_never_retries() {
    for behavior in [1, 2] {
        let peer = Arc::new(Source {
            name: "peer:source".into(),
            authoritative: false,
            calls: AtomicUsize::new(0),
            behavior,
        });
        let server = Arc::new(Source {
            name: "server".into(),
            authoritative: true,
            calls: AtomicUsize::new(0),
            behavior: 0,
        });
        let sources: Vec<Arc<dyn ChunkSource>> = vec![peer.clone(), server];
        let health = SourceHealth::default();
        let m = manifest();
        let cancel = CancellationToken::new();
        assert_eq!(
            fetch_with_health(
                &sources,
                "owner",
                &m,
                &m.chunks[0],
                &cancel,
                &health,
                "task"
            )
            .await
            .unwrap()
            .0,
            "server"
        );
        tokio::time::advance(std::time::Duration::from_secs(3)).await;
        let recovered = fetch_with_health(
            &sources,
            "owner",
            &m,
            &m.chunks[0],
            &cancel,
            &health,
            "task",
        )
        .await
        .unwrap();
        assert_eq!(
            recovered.0,
            if behavior == 1 {
                "peer:source"
            } else {
                "server"
            }
        );
        assert_eq!(
            peer.calls.load(Ordering::SeqCst),
            if behavior == 1 { 2 } else { 1 }
        );
    }
}
