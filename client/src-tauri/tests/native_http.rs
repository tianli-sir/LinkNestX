use axum::{
    body::Bytes,
    extract::{Path, State},
    http::{HeaderMap, StatusCode},
    routing::{get, post, put},
    Json, Router,
};
use linknestx::{
    auth::session::AuthManager,
    events::NoopEvents,
    transfer::{models::*, service::TransferService},
};
use serde_json::{json, Value};
use std::sync::{
    atomic::{AtomicBool, AtomicUsize, Ordering},
    Arc,
};
use tempfile::TempDir;

const HASH: &str = "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad";
const SECOND_HASH: &str = "cb8379ac2098aa165029e3938a51da0bcecfc008fd6795f401178647f96c5b34";
#[derive(Clone, Default)]
struct Mock {
    mode: Arc<AtomicUsize>,
    reads: Arc<AtomicUsize>,
    refresh: Arc<AtomicUsize>,
    puts: Arc<AtomicUsize>,
    multi: Arc<AtomicUsize>,
    first_reads: Arc<AtomicUsize>,
    complete_failures: Arc<AtomicUsize>,
    published: Arc<AtomicBool>,
    blocked: Arc<AtomicUsize>,
    manifest_reads: Arc<AtomicUsize>,
}
fn session(user: &str, token: &str) -> Value {
    json!({"id":"session", "accessToken":token,"refreshToken":"refresh", "expiresAt":"2099-01-01T00:00:00Z", "deviceId":"device", "user":{"id":user,"username":user,"roles":["user"],"disabled":false,"mustChangePassword":false}})
}
fn manifest() -> Value {
    json!({"fileId":"file","versionId":"version","name":"abc.txt","size":3,"sha256":HASH,"chunkSize":4,"chunks":[{"index":0,"hash":HASH,"size":3}],"expiresAt":"2099-01-01T00:00:00Z","visibility":"group"})
}
async fn login(Json(body): Json<Value>) -> Json<Value> {
    Json(session(body["username"].as_str().unwrap(), "old"))
}
async fn refresh(State(s): State<Mock>, Json(body): Json<Value>) -> Json<Value> {
    assert_eq!(body["refreshToken"], "refresh");
    s.refresh.fetch_add(1, Ordering::SeqCst);
    tokio::time::sleep(std::time::Duration::from_millis(40)).await;
    Json(session("alice", "new"))
}
async fn me(headers: HeaderMap) -> Result<Json<Value>, StatusCode> {
    if headers.get("authorization").unwrap() == "Bearer new" {
        Ok(Json(json!({"ok":true})))
    } else {
        Err(StatusCode::UNAUTHORIZED)
    }
}
async fn get_manifest(State(s): State<Mock>) -> Result<Json<Value>, StatusCode> {
    let count = s.manifest_reads.fetch_add(1, Ordering::SeqCst);
    if s.mode.load(Ordering::SeqCst) == 11 && count > 0 {
        s.blocked.store(11, Ordering::SeqCst);
        std::future::pending::<()>().await;
    }
    if s.mode.load(Ordering::SeqCst) == 5 {
        return Err(StatusCode::FORBIDDEN);
    }
    let mut value = manifest();
    if s.multi.load(Ordering::SeqCst) > 0 {
        value["size"] = json!(6);
        value["chunkSize"] = json!(3);
        value["sha256"] = json!("bef57ec7f53a6d40beb640a780a639c83bc29ac8a9816f1fc6c5c6dcd93c4721");
        value["chunks"] =
            json!([{"index":0,"hash":HASH,"size":3},{"index":1,"hash":SECOND_HASH,"size":3}]);
    }
    if s.mode.load(Ordering::SeqCst) == 6 {
        value["expiresAt"] = json!("2000-01-01T00:00:00Z");
    }
    if s.mode.load(Ordering::SeqCst) == 7 {
        value["sha256"] = json!(SECOND_HASH);
    }
    Ok(Json(value))
}
async fn chunk(
    State(s): State<Mock>,
    Path((version, hash)): Path<(String, String)>,
    headers: HeaderMap,
) -> Result<&'static str, StatusCode> {
    assert_eq!(version, "version");
    assert!(hash == HASH || hash == SECOND_HASH);
    assert!(headers.contains_key("authorization"));
    s.reads.fetch_add(1, Ordering::SeqCst);
    if hash == HASH {
        s.first_reads.fetch_add(1, Ordering::SeqCst);
    }
    if hash == SECOND_HASH {
        if s.mode.load(Ordering::SeqCst) == 4 {
            tokio::time::sleep(std::time::Duration::from_secs(3)).await;
        }
        return Ok("def");
    }
    match s.mode.load(Ordering::SeqCst) {
        1 => Ok("bad"),
        2 => Err(StatusCode::FORBIDDEN),
        3 => {
            tokio::time::sleep(std::time::Duration::from_secs(2)).await;
            Ok("abc")
        }
        _ => Ok("abc"),
    }
}
async fn streamed_chunk(
    state: State<Mock>,
    path: Path<(String, String)>,
    headers: HeaderMap,
) -> axum::response::Response {
    use axum::response::IntoResponse;
    if state.0.mode.load(Ordering::SeqCst) == 13 {
        let stream = futures_util::stream::unfold(0usize, |index| async move {
            if index == 3 {
                return None;
            }
            tokio::time::sleep(std::time::Duration::from_millis(500)).await;
            Some((
                Ok::<_, std::io::Error>(Bytes::from_static(&b"abc"[index..index + 1])),
                index + 1,
            ))
        });
        axum::body::Body::from_stream(stream).into_response()
    } else {
        chunk(state, path, headers).await.into_response()
    }
}
async fn begin(State(s): State<Mock>, Json(body): Json<Value>) -> Json<Value> {
    if s.mode.load(Ordering::SeqCst) == 8 {
        s.blocked.store(8, Ordering::SeqCst);
        std::future::pending::<()>().await;
    }
    assert_eq!(body["sha256"], HASH);
    assert_eq!(body["size"], 3);
    assert_eq!(body["chunks"][0]["hash"], HASH);
    Json(json!({"uploadId":"upload","fileId":"file","versionId":"version","missingIndexes":[0]}))
}
async fn upload(State(s): State<Mock>, body: Bytes) -> StatusCode {
    assert_eq!(&body[..], b"abc");
    let attempt = s.puts.fetch_add(1, Ordering::SeqCst);
    if s.mode.load(Ordering::SeqCst) == 14 && attempt == 0 {
        return StatusCode::UNAUTHORIZED;
    }
    StatusCode::NO_CONTENT
}
async fn upload_status(State(s): State<Mock>) -> Json<Value> {
    if s.mode.load(Ordering::SeqCst) == 9 {
        s.blocked.store(9, Ordering::SeqCst);
        std::future::pending::<()>().await;
    }
    Json(
        json!({"uploadId":"upload","fileId":"file","versionId":"version","missingIndexes":if s.puts.load(Ordering::SeqCst)>0{vec![]}else{vec![0]},"completed":s.published.load(Ordering::SeqCst)}),
    )
}
async fn complete(State(s): State<Mock>) -> Result<Json<Value>, StatusCode> {
    if s.mode.load(Ordering::SeqCst) == 10 {
        s.blocked.store(10, Ordering::SeqCst);
        std::future::pending::<()>().await;
    }
    if s.mode.load(Ordering::SeqCst) == 12 {
        s.published.store(true, Ordering::SeqCst);
        return Err(StatusCode::SERVICE_UNAVAILABLE);
    }
    if s.complete_failures.swap(0, Ordering::SeqCst) > 0 {
        Err(StatusCode::SERVICE_UNAVAILABLE)
    } else {
        s.published.store(true, Ordering::SeqCst);
        Ok(Json(json!({"versionId":"version"})))
    }
}
struct Harness {
    _root: TempDir,
    origin: String,
    auth: Arc<AuthManager>,
    service: Arc<TransferService>,
    mock: Mock,
    server: tokio::task::JoinHandle<()>,
}
impl Drop for Harness {
    fn drop(&mut self) {
        self.server.abort();
    }
}
impl Harness {
    async fn new() -> Self {
        let mock = Mock::default();
        let app = Router::new()
            .route("/api/v1/auth/login", post(login))
            .route("/api/v1/auth/refresh", post(refresh))
            .route("/api/v1/me", get(me))
            .route("/api/v1/file-versions/version/manifest", get(get_manifest))
            .route("/api/v1/files/{version}/chunks/{hash}", get(streamed_chunk))
            .route("/api/v1/files/uploads", post(begin))
            .route("/api/v1/uploads/upload", get(upload_status))
            .route("/api/v1/uploads/upload/chunks/0", put(upload))
            .route("/api/v1/uploads/upload/complete", post(complete))
            .route(
                "/api/v1/redirect",
                get(|| async { axum::response::Redirect::temporary("http://127.0.0.1:9/stolen") }),
            )
            .with_state(mock.clone());
        let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
        let origin = format!("http://{}", listener.local_addr().unwrap());
        let server = tokio::spawn(async move { axum::serve(listener, app).await.unwrap() });
        let root = tempfile::tempdir().unwrap();
        let auth = Arc::new(AuthManager::new(root.path()).unwrap());
        auth.login(&origin, "alice", "password", None)
            .await
            .unwrap();
        let service =
            TransferService::open(root.path(), auth.clone(), Arc::new(NoopEvents)).unwrap();
        Self {
            _root: root,
            origin,
            auth,
            service,
            mock,
            server,
        }
    }
    fn destination(&self) -> String {
        self._root.path().join("saved.txt").to_str().unwrap().into()
    }
    async fn terminal(&self, id: &str) -> TaskSnapshot {
        tokio::time::timeout(std::time::Duration::from_secs(5), async {
            loop {
                let t = self
                    .service
                    .list()
                    .await
                    .unwrap()
                    .into_iter()
                    .find(|t| t.id == id)
                    .unwrap();
                if matches!(
                    t.state,
                    TaskState::Completed | TaskState::Failed | TaskState::Paused
                ) {
                    return t;
                }
                tokio::time::sleep(std::time::Duration::from_millis(10)).await;
            }
        })
        .await
        .unwrap()
    }
}

#[tokio::test]
async fn real_http_download_is_verified_and_never_overwrites_destination() {
    let h = Harness::new().await;
    let id = h
        .service
        .start_download("version".into(), h.destination())
        .await
        .unwrap();
    let t = h.terminal(&id).await;
    assert_eq!(t.state, TaskState::Completed);
    assert_eq!(t.bytes_completed, 3);
    assert_eq!(std::fs::read(h.destination()).unwrap(), b"abc");
    assert!(h
        .service
        .start_download("version".into(), h.destination())
        .await
        .is_err());
    h.service.shutdown().await.unwrap();
}

#[tokio::test]
async fn corrupt_or_forbidden_chunks_never_publish() {
    for mode in [1, 2] {
        let h = Harness::new().await;
        h.mock.mode.store(mode, Ordering::SeqCst);
        let id = h
            .service
            .start_download("version".into(), h.destination())
            .await
            .unwrap();
        let t = h.terminal(&id).await;
        assert_eq!(t.state, TaskState::Failed);
        assert_eq!(t.bytes_completed, 0);
        assert!(!std::path::Path::new(&h.destination()).exists());
        assert_eq!(h.mock.refresh.load(Ordering::SeqCst), 0);
        h.service.shutdown().await.unwrap();
    }
}

#[tokio::test]
async fn pause_restart_resume_and_account_isolation() {
    let h = Harness::new().await;
    h.mock.mode.store(3, Ordering::SeqCst);
    let id = h
        .service
        .start_download("version".into(), h.destination())
        .await
        .unwrap();
    tokio::time::sleep(std::time::Duration::from_millis(100)).await;
    h.service.pause(&id).await.unwrap();
    assert_eq!(h.terminal(&id).await.state, TaskState::Paused);
    h.service.shutdown().await.unwrap();
    let resumed =
        TransferService::open(h._root.path(), h.auth.clone(), Arc::new(NoopEvents)).unwrap();
    h.auth
        .login(&h.origin, "bob", "password", None)
        .await
        .unwrap();
    assert!(resumed.list().await.unwrap().is_empty());
    assert!(resumed.resume(&id).await.is_err());
    h.auth
        .login(&h.origin, "alice", "password", None)
        .await
        .unwrap();
    assert_eq!(resumed.list().await.unwrap()[0].state, TaskState::Paused);
    h.mock.mode.store(0, Ordering::SeqCst);
    resumed.resume(&id).await.unwrap();
    tokio::time::timeout(std::time::Duration::from_secs(5), async {
        loop {
            if resumed.list().await.unwrap()[0].state == TaskState::Completed {
                break;
            }
            tokio::time::sleep(std::time::Duration::from_millis(10)).await;
        }
    })
    .await
    .unwrap();
    assert_eq!(std::fs::read(h.destination()).unwrap(), b"abc");
    resumed.shutdown().await.unwrap();
}

#[tokio::test]
async fn refresh_is_single_flight_and_paths_stay_on_origin() {
    let h = Harness::new().await;
    let mut requests = Vec::new();
    for _ in 0..8 {
        let a = h.auth.clone();
        requests.push(tokio::spawn(async move {
            a.json("GET", "/api/v1/me", None).await.unwrap()
        }));
    }
    for request in requests {
        assert_eq!(request.await.unwrap()["ok"], true)
    }
    assert_eq!(h.mock.refresh.load(Ordering::SeqCst), 1);
    for path in [
        "https://evil.test/api/v1/me",
        "//evil.test/api/v1/me",
        "/api/v1/../../elsewhere",
        "/api/v1/%2e%2e/secret",
        "/api/v1/me#token",
    ] {
        assert!(h.auth.json("GET", path, None).await.is_err(), "{path}");
    }
    h.service.shutdown().await.unwrap();
}

#[tokio::test]
async fn upload_streams_manifest_chunks_and_completes() {
    let h = Harness::new().await;
    std::fs::write(h.destination(), b"abc").unwrap();
    let id = h
        .service
        .start_upload("group".into(), h.destination(), None)
        .await
        .unwrap();
    let t = h.terminal(&id).await;
    assert_eq!(t.state, TaskState::Completed);
    assert_eq!(t.bytes_completed, 3);
    assert_eq!(h.mock.puts.load(Ordering::SeqCst), 1);
    let cache_db = rusqlite::Connection::open(h._root.path().join("cache/index.sqlite3")).unwrap();
    let expiry: Option<i64> = cache_db
        .query_row("SELECT expires FROM chunks LIMIT 1", [], |r| r.get(0))
        .unwrap();
    assert!(
        expiry.is_some(),
        "uploaded chunks must not remain cached indefinitely"
    );
    h.service.shutdown().await.unwrap();
}

#[tokio::test]
async fn shutdown_persists_interrupted_tasks_for_the_same_accounts_next_login() {
    let h = Harness::new().await;
    h.mock.mode.store(3, Ordering::SeqCst);
    let id = h
        .service
        .start_download("version".into(), h.destination())
        .await
        .unwrap();
    tokio::time::sleep(std::time::Duration::from_millis(30)).await;
    h.service.shutdown().await.unwrap();
    assert_eq!(h.service.list().await.unwrap()[0].state, TaskState::Paused);
    assert!(h.service.resume(&id).await.is_err());
    let restored =
        TransferService::open(h._root.path(), h.auth.clone(), Arc::new(NoopEvents)).unwrap();
    h.mock.mode.store(0, Ordering::SeqCst);
    restored.restore_after_login().await.unwrap();
    tokio::time::timeout(std::time::Duration::from_secs(5), async {
        loop {
            if restored.list().await.unwrap()[0].state == TaskState::Completed {
                break;
            }
            tokio::time::sleep(std::time::Duration::from_millis(10)).await;
        }
    })
    .await
    .unwrap();
    assert_eq!(std::fs::read(h.destination()).unwrap(), b"abc");
    restored.shutdown().await.unwrap();
}

#[tokio::test]
async fn destination_created_during_transfer_is_not_replaced() {
    let h = Harness::new().await;
    h.mock.mode.store(3, Ordering::SeqCst);
    let id = h
        .service
        .start_download("version".into(), h.destination())
        .await
        .unwrap();
    tokio::time::sleep(std::time::Duration::from_millis(30)).await;
    std::fs::write(h.destination(), b"user content").unwrap();
    assert_eq!(h.terminal(&id).await.state, TaskState::Failed);
    assert_eq!(std::fs::read(h.destination()).unwrap(), b"user content");
    h.service.shutdown().await.unwrap();
}

#[tokio::test]
async fn durable_bitmap_skips_good_chunks_and_repairs_tampered_partial_and_cache() {
    for corrupt in [false, true] {
        let h = Harness::new().await;
        h.mock.multi.store(1, Ordering::SeqCst);
        h.mock.mode.store(4, Ordering::SeqCst);
        let mut policy = h.service.sharing_policy().await;
        policy.max_concurrent = 1;
        h.service.set_sharing_policy(policy).await.unwrap();
        let id = h
            .service
            .start_download("version".into(), h.destination())
            .await
            .unwrap();
        tokio::time::timeout(std::time::Duration::from_secs(3), async {
            loop {
                if h.service.list().await.unwrap()[0].bytes_completed == 3 {
                    break;
                }
                tokio::time::sleep(std::time::Duration::from_millis(5)).await;
            }
        })
        .await
        .unwrap();
        h.service.pause(&id).await.unwrap();
        h.service.shutdown().await.unwrap();
        if corrupt {
            use std::io::Write;
            let partial = h._root.path().join(format!(".linknestx-{id}.part"));
            std::fs::OpenOptions::new()
                .write(true)
                .open(partial)
                .unwrap()
                .write_all(b"bad")
                .unwrap();
            for entry in std::fs::read_dir(h._root.path().join("cache")).unwrap() {
                let path = entry.unwrap().path();
                if path.file_name().unwrap().to_string_lossy().len() == 64 {
                    std::fs::write(path, b"bad").unwrap();
                }
            }
        }
        let resumed =
            TransferService::open(h._root.path(), h.auth.clone(), Arc::new(NoopEvents)).unwrap();
        h.mock.mode.store(0, Ordering::SeqCst);
        resumed.resume(&id).await.unwrap();
        tokio::time::timeout(std::time::Duration::from_secs(3), async {
            loop {
                if resumed.list().await.unwrap()[0].state == TaskState::Completed {
                    break;
                }
                tokio::time::sleep(std::time::Duration::from_millis(5)).await;
            }
        })
        .await
        .unwrap();
        assert_eq!(std::fs::read(h.destination()).unwrap(), b"abcdef");
        assert_eq!(
            h.mock.first_reads.load(Ordering::SeqCst),
            if corrupt { 2 } else { 1 }
        );
        resumed.shutdown().await.unwrap();
    }
}

#[tokio::test]
async fn upload_retry_uses_server_receipts_without_reuploading_verified_bytes() {
    let h = Harness::new().await;
    std::fs::write(h.destination(), b"abc").unwrap();
    h.mock.complete_failures.store(1, Ordering::SeqCst);
    let id = h
        .service
        .start_upload("group".into(), h.destination(), None)
        .await
        .unwrap();
    assert_eq!(h.terminal(&id).await.state, TaskState::Failed);
    h.service.resume(&id).await.unwrap();
    assert_eq!(h.terminal(&id).await.state, TaskState::Completed);
    assert_eq!(h.mock.puts.load(Ordering::SeqCst), 1);
    h.service.shutdown().await.unwrap();
}

#[tokio::test]
async fn expired_final_hash_mismatch_and_redirect_are_rejected() {
    for mode in [6, 7] {
        let h = Harness::new().await;
        h.mock.mode.store(mode, Ordering::SeqCst);
        let id = h
            .service
            .start_download("version".into(), h.destination())
            .await
            .unwrap();
        assert_eq!(h.terminal(&id).await.state, TaskState::Failed);
        assert!(!std::path::Path::new(&h.destination()).exists());
        let error = h
            .auth
            .json("GET", "/api/v1/redirect", None)
            .await
            .unwrap_err();
        assert_eq!(error.status, Some(307));
        h.service.shutdown().await.unwrap();
    }
}

#[tokio::test]
async fn cached_content_is_not_reused_after_server_revokes_access() {
    let h = Harness::new().await;
    let id = h
        .service
        .start_download("version".into(), h.destination())
        .await
        .unwrap();
    assert_eq!(h.terminal(&id).await.state, TaskState::Completed);
    h.mock.mode.store(5, Ordering::SeqCst);
    let second = h
        ._root
        .path()
        .join("denied.txt")
        .to_string_lossy()
        .into_owned();
    let id = h
        .service
        .start_download("version".into(), second.clone())
        .await
        .unwrap();
    assert_eq!(h.terminal(&id).await.state, TaskState::Failed);
    assert!(!std::path::Path::new(&second).exists());
    assert_eq!(std::fs::read(h.destination()).unwrap(), b"abc");
    let chunk = Chunk {
        index: 0,
        hash: HASH.into(),
        size: 3,
    };
    assert!(h
        .service
        .cache
        .read(&h.auth.owner().await.unwrap(), "version", &chunk)
        .await
        .unwrap()
        .is_none());
    h.service.shutdown().await.unwrap();
}

#[tokio::test]
async fn completed_or_receipted_upload_resumes_without_local_source() {
    for completed in [false, true] {
        let h = Harness::new().await;
        std::fs::write(h.destination(), b"abc").unwrap();
        if completed {
            h.mock.mode.store(12, Ordering::SeqCst);
        } else {
            h.mock.complete_failures.store(1, Ordering::SeqCst);
        }
        let id = h
            .service
            .start_upload("group".into(), h.destination(), None)
            .await
            .unwrap();
        assert_eq!(h.terminal(&id).await.state, TaskState::Failed);
        std::fs::remove_file(h.destination()).unwrap();
        h.mock.mode.store(0, Ordering::SeqCst);
        h.service.resume(&id).await.unwrap();
        let result = h.terminal(&id).await;
        assert_eq!(result.state, TaskState::Completed, "{:?}", result.error);
        assert_eq!(result.bytes_completed, 3);
        assert_eq!(h.mock.puts.load(Ordering::SeqCst), 1);
        h.service.shutdown().await.unwrap();
    }
}

#[tokio::test]
async fn pause_interrupts_upload_session_status_and_completion_requests() {
    for mode in [8, 9, 10] {
        let h = Harness::new().await;
        std::fs::write(h.destination(), b"abc").unwrap();
        if mode == 9 {
            h.mock.complete_failures.store(1, Ordering::SeqCst);
        } else {
            h.mock.mode.store(mode, Ordering::SeqCst);
        }
        let id = h
            .service
            .start_upload("group".into(), h.destination(), None)
            .await
            .unwrap();
        if mode == 9 {
            assert_eq!(h.terminal(&id).await.state, TaskState::Failed);
            h.mock.mode.store(9, Ordering::SeqCst);
            h.service.resume(&id).await.unwrap();
        }
        tokio::time::timeout(std::time::Duration::from_secs(3), async {
            while h.mock.blocked.load(Ordering::SeqCst) != mode {
                tokio::time::sleep(std::time::Duration::from_millis(5)).await;
            }
        })
        .await
        .unwrap();
        tokio::time::timeout(std::time::Duration::from_millis(500), h.service.pause(&id))
            .await
            .expect("pause must interrupt the pending request")
            .unwrap();
        assert_eq!(h.terminal(&id).await.state, TaskState::Paused);
        h.service.shutdown().await.unwrap();
    }
}

#[tokio::test]
async fn shutdown_interrupts_final_download_permission_check() {
    let h = Harness::new().await;
    h.mock.mode.store(11, Ordering::SeqCst);
    let id = h
        .service
        .start_download("version".into(), h.destination())
        .await
        .unwrap();
    tokio::time::timeout(std::time::Duration::from_secs(3), async {
        while h.mock.blocked.load(Ordering::SeqCst) != 11 {
            tokio::time::sleep(std::time::Duration::from_millis(5)).await;
        }
    })
    .await
    .unwrap();
    tokio::time::timeout(std::time::Duration::from_millis(500), h.service.shutdown())
        .await
        .expect("shutdown must interrupt final authorization")
        .unwrap();
    assert_eq!(h.terminal(&id).await.state, TaskState::Paused);
    assert!(!std::path::Path::new(&h.destination()).exists());
}

#[tokio::test]
async fn streamed_upload_replays_after_401_and_counts_attempts_but_credits_once() {
    let h = Harness::new().await;
    h.mock.mode.store(14, Ordering::SeqCst);
    let source = h._root.path().join("send.txt");
    std::fs::write(&source, b"abc").unwrap();
    let id = h
        .service
        .start_upload("group".into(), source.to_string_lossy().into(), None)
        .await
        .unwrap();
    let snapshot = h.terminal(&id).await;
    assert_eq!(snapshot.state, TaskState::Completed);
    assert_eq!(h.mock.puts.load(Ordering::SeqCst), 2);
    assert_eq!(snapshot.metrics.network_bytes, 6);
    assert_eq!(snapshot.metrics.credited_network_bytes, 3);
    assert_eq!(snapshot.metrics.current_bytes_per_second, 0.0);
    h.service.shutdown().await.unwrap();
}
#[tokio::test]
async fn partial_http_payload_is_visible_before_chunk_ack_and_cache_is_not_network() {
    let h = Harness::new().await;
    h.mock.mode.store(13, Ordering::SeqCst);
    let id = h
        .service
        .start_download(
            "version".into(),
            h._root.path().join("streamed.txt").to_string_lossy().into(),
        )
        .await
        .unwrap();
    tokio::time::timeout(std::time::Duration::from_secs(5), async {
        loop {
            let t = h
                .service
                .list()
                .await
                .unwrap()
                .into_iter()
                .find(|t| t.id == id)
                .unwrap();
            if t.metrics.network_bytes > 0 {
                assert!(t.metrics.network_bytes < 3);
                assert_eq!(t.bytes_completed, 0);
                assert!(t.metrics.current_bytes_per_second > 0.0);
                break;
            }
            tokio::time::sleep(std::time::Duration::from_millis(25)).await;
        }
    })
    .await
    .unwrap();
    let first = h.terminal(&id).await;
    assert_eq!(first.metrics.network_bytes, 3);
    assert_eq!(first.metrics.credited_network_bytes, 3);
    let cached = h
        .service
        .start_download(
            "version".into(),
            h._root.path().join("cached.txt").to_string_lossy().into(),
        )
        .await
        .unwrap();
    let second = h.terminal(&cached).await;
    assert_eq!(second.state, TaskState::Completed);
    assert_eq!(second.metrics.network_bytes, 0);
    assert_eq!(second.metrics.credited_network_bytes, 0);
    assert_eq!(second.metrics.cache_bytes, 3);
    assert_eq!(second.metrics.average_bytes_per_second, None);
    h.service.shutdown().await.unwrap();
}
