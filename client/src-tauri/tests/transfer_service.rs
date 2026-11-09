use linknestx::{
    auth::session::normalize_origin,
    transfer::{cache::Cache, models::*},
};
use tempfile::tempdir;

#[test]
fn go_session_omitted_optional_email_is_normalized_for_desktop() {
    let session:linknestx::auth::session::Session=serde_json::from_value(serde_json::json!({
        "id":"session","accessToken":"access","refreshToken":"refresh","expiresAt":"2099-01-01T00:00:00Z","deviceId":"device",
        "user":{"id":"employee","username":"员工","roles":["user"],"disabled":false,"mustChangePassword":true,"createdAt":"2026-11-09T00:00:00Z"}
    })).unwrap();
    assert_eq!(session.user.email, "");
    assert_eq!(serde_json::to_value(session).unwrap()["user"]["email"], "");
}

#[tokio::test]
async fn cancellation_drops_an_inflight_operation() {
    use linknestx::transfer::scheduler::cancellable;
    let cancel = tokio_util::sync::CancellationToken::new();
    let trigger = cancel.clone();
    let (started_tx, started_rx) = tokio::sync::oneshot::channel();
    let (dropped_tx, dropped_rx) = tokio::sync::oneshot::channel();
    struct Dropped(Option<tokio::sync::oneshot::Sender<()>>);
    impl Drop for Dropped {
        fn drop(&mut self) {
            if let Some(tx) = self.0.take() {
                let _ = tx.send(());
            }
        }
    }
    let worker = tokio::spawn(async move {
        cancellable(&cancel, async move {
            let _guard = Dropped(Some(dropped_tx));
            let _ = started_tx.send(());
            std::future::pending::<linknestx::Result<()>>().await
        })
        .await
    });
    started_rx.await.unwrap();
    trigger.cancel();
    assert!(
        tokio::time::timeout(std::time::Duration::from_millis(100), worker)
            .await
            .unwrap()
            .unwrap()
            .is_err()
    );
    dropped_rx.await.unwrap();
}

#[test]
fn origin_and_paths_cannot_leak_credentials() {
    for origin in [
        "http://example.com",
        "https://user:pass@host",
        "https://host/path",
        "https://host?x=1",
        "file:///tmp",
    ] {
        assert!(normalize_origin(origin).is_err(), "{origin}");
    }
    assert_eq!(
        normalize_origin("localhost:8080").unwrap(),
        "https://localhost:8080"
    );
    assert_eq!(
        normalize_origin("http://127.0.0.1:9000/").unwrap(),
        "http://127.0.0.1:9000"
    );
}

#[tokio::test]
async fn cache_rejects_corrupt_chunks_and_enforces_quota() {
    let root = tempdir().unwrap();
    let cache = Cache::open(root.path(), 6).unwrap();
    let a = Chunk {
        index: 0,
        hash: "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad".into(),
        size: 3,
    };
    assert!(cache
        .store("owner", "version", &a, b"bad", None)
        .await
        .is_err());
    assert!(cache.read("owner", "version", &a).await.unwrap().is_none());
    cache
        .store("owner", "version", &a, b"abc", None)
        .await
        .unwrap();
    assert_eq!(
        cache.read("owner", "version", &a).await.unwrap().unwrap(),
        b"abc"
    );
    assert!(cache.read("other", "version", &a).await.unwrap().is_none());
    cache.set_quota(2).await.unwrap();
    assert!(cache.read("owner", "version", &a).await.unwrap().is_none());
}

#[test]
fn manifests_reject_inconsistent_geometry_and_picker_sanitizes_metadata() {
    let mut manifest: Manifest = serde_json::from_value(serde_json::json!({
        "fileId":"file", "versionId":"version", "name":"../saved.txt", "size":3,
        "sha256":"ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad", "chunkSize":4,
        "chunks":[{"index":0,"hash":"ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad","size":3}], "expiresAt":null,"visibility":"group"
    })).unwrap();
    assert!(manifest.validate().is_ok());
    assert_eq!(suggested_name(&manifest.name), ".._saved.txt");
    manifest.name = "测量:数据.txt".into();
    assert!(manifest.validate().is_ok());
    assert_eq!(suggested_name(&manifest.name), "测量_数据.txt");
    manifest.chunks[0].size = 4;
    assert!(manifest.validate().is_err());
}

#[test]
fn destination_rejects_traversal_and_existing_files() {
    use linknestx::transfer::service::validate_destination;
    let root = tempdir().unwrap();
    assert!(validate_destination("relative.txt").is_err());
    assert!(validate_destination(root.path().join("../escape.txt").to_str().unwrap()).is_err());
    let saved = root.path().join("saved.txt");
    std::fs::write(&saved, b"existing").unwrap();
    assert!(validate_destination(saved.to_str().unwrap()).is_err());
    assert_eq!(std::fs::read(saved).unwrap(), b"existing");
}

#[tokio::test]
async fn expired_and_revoked_cache_is_removed_without_touching_saved_files() {
    let root = tempdir().unwrap();
    let cache = Cache::open(root.path(), 100).unwrap();
    let chunk = Chunk {
        index: 0,
        hash: "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad".into(),
        size: 3,
    };
    std::fs::write(root.path().join("saved.txt"), b"abc").unwrap();
    assert!(cache
        .store(
            "owner",
            "version",
            &chunk,
            b"abc",
            Some("2000-01-01T00:00:00Z")
        )
        .await
        .is_err());
    cache
        .store("owner", "version", &chunk, b"abc", None)
        .await
        .unwrap();
    cache.revoke("owner", "version").await.unwrap();
    assert!(cache
        .read("owner", "version", &chunk)
        .await
        .unwrap()
        .is_none());
    assert_eq!(
        std::fs::read(root.path().join("saved.txt")).unwrap(),
        b"abc"
    );
}
