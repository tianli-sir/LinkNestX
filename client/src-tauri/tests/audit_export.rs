use async_trait::async_trait;
use axum::{
    body::{Body, Bytes},
    extract::{Query, State},
    http::{HeaderMap, Response, StatusCode},
    routing::{get, post},
    Json, Router,
};
use linknestx::{
    audit_export::{
        AuditExportManager, AuditFormat, AuditSavePicker, ExportOutcome, ExportRequest,
    },
    auth::session::AuthManager,
    Result,
};
use serde_json::{json, Value};
use std::{
    collections::HashMap,
    path::PathBuf,
    sync::{
        atomic::{AtomicUsize, Ordering},
        Arc,
    },
    time::Duration,
};
use tokio::sync::{Mutex, Notify};

#[derive(Clone, Default)]
struct ServerState {
    mode: Arc<AtomicUsize>,
    requests: Arc<Mutex<Vec<HashMap<String, String>>>>,
    refreshes: Arc<AtomicUsize>,
    body_started: Arc<Notify>,
    release: Arc<Notify>,
}
fn session(user: &str, token: &str, id: &str) -> Value {
    json!({"id":id,"accessToken":token,"refreshToken":"refresh","expiresAt":"2099-01-01T00:00:00Z","deviceId":"device","user":{"id":user,"username":user,"roles":["admin"],"disabled":false,"mustChangePassword":false}})
}
async fn export(
    State(state): State<ServerState>,
    Query(query): Query<HashMap<String, String>>,
    headers: HeaderMap,
) -> Response<Body> {
    assert!(headers.get("authorization").is_some());
    state.requests.lock().await.push(query.clone());
    let mode = state.mode.load(Ordering::SeqCst);
    if mode == 3 && headers["authorization"] != "Bearer new" {
        return Response::builder().status(401).body(Body::empty()).unwrap();
    }
    if mode == 4 {
        return Response::builder().status(403).body(Body::empty()).unwrap();
    }
    let csv = query["format"] == "csv";
    let body = if csv {
        let mut text = "id,action,actorId,targetId,createdAt\r\n".to_string();
        for index in 0..123 {
            text.push_str(&format!(
                "{index},file.upload,admin,file,2026-11-09T00:00:00Z\r\n"
            ));
        }
        text
    } else {
        json!({"events":(0..123).map(|id| json!({"id":id,"action":"file.upload"})).collect::<Vec<_>>(),"total":123}).to_string()
    };
    let chunks: Vec<Bytes> = body
        .as_bytes()
        .chunks(71)
        .map(Bytes::copy_from_slice)
        .collect();
    let stream = futures_util::stream::unfold(
        (0, chunks, state),
        move |(index, chunks, state)| async move {
            if index == 1 {
                state.body_started.notify_one();
                if mode == 1 || mode == 2 {
                    state.release.notified().await;
                }
                if mode == 2 {
                    return Some((
                        Err(std::io::Error::new(
                            std::io::ErrorKind::ConnectionReset,
                            "server aborted export",
                        )),
                        (usize::MAX, chunks, state),
                    ));
                }
            }
            if index >= chunks.len() {
                return None;
            }
            Some((
                Ok::<Bytes, std::io::Error>(chunks[index].clone()),
                (index + 1, chunks, state),
            ))
        },
    );
    Response::builder()
        .header(
            "content-type",
            if csv {
                "text/csv; charset=utf-8"
            } else {
                "application/json; charset=utf-8"
            },
        )
        .body(Body::from_stream(stream))
        .unwrap()
}
struct Harness {
    _root: tempfile::TempDir,
    output: tempfile::TempDir,
    auth: Arc<AuthManager>,
    manager: Arc<AuditExportManager>,
    origin: String,
    state: ServerState,
    server: tokio::task::JoinHandle<()>,
}
impl Drop for Harness {
    fn drop(&mut self) {
        self.server.abort();
    }
}
impl Harness {
    async fn new() -> Self {
        let state = ServerState::default();
        let app = Router::new()
            .route(
                "/api/v1/auth/login",
                post(|Json(body): Json<Value>| async move {
                    Json(session(
                        body["username"].as_str().unwrap(),
                        "old",
                        "initial",
                    ))
                }),
            )
            .route(
                "/api/v1/auth/logout",
                post(|| async { StatusCode::NO_CONTENT }),
            )
            .route(
                "/api/v1/auth/refresh",
                post(|State(state): State<ServerState>| async move {
                    state.refreshes.fetch_add(1, Ordering::SeqCst);
                    Json(session("admin", "new", "rotated"))
                }),
            )
            .route("/api/v1/admin/audit/export", get(export))
            .with_state(state.clone());
        let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
        let origin = format!("http://{}", listener.local_addr().unwrap());
        let server = tokio::spawn(async move {
            axum::serve(listener, app).await.unwrap();
        });
        let root = tempfile::tempdir().unwrap();
        let auth = Arc::new(AuthManager::new(root.path()).unwrap());
        auth.login(&origin, "admin", "password", None)
            .await
            .unwrap();
        Self {
            _root: root,
            output: tempfile::tempdir().unwrap(),
            auth,
            manager: Arc::new(AuditExportManager::new().unwrap()),
            origin,
            state,
            server,
        }
    }
    fn target(&self, format: &str) -> PathBuf {
        self.output.path().join(format!("audit.{format}"))
    }
    async fn run(&self, format: AuditFormat, target: Option<PathBuf>) -> Result<ExportOutcome> {
        self.manager
            .begin(
                self.auth.clone(),
                ExportRequest::new(format, "file.upload&actorId=someone".into())?,
            )
            .await?
            .save(&Picker(target))
            .await
    }
    async fn start_blocked(&self) -> tokio::task::JoinHandle<Result<ExportOutcome>> {
        self.state.mode.store(1, Ordering::SeqCst);
        let job = self
            .manager
            .begin(
                self.auth.clone(),
                ExportRequest::new(AuditFormat::Csv, "".into()).unwrap(),
            )
            .await
            .unwrap();
        let picker = Picker(Some(self.target("csv")));
        let running = tokio::spawn(async move { job.save(&picker).await });
        tokio::time::timeout(Duration::from_secs(5), self.state.body_started.notified())
            .await
            .unwrap();
        self.wait_for_partial().await;
        running
    }
    async fn wait_for_partial(&self) {
        tokio::time::timeout(Duration::from_secs(5), async {
            loop {
                let has_bytes = std::fs::read_dir(self.output.path()).unwrap().any(|entry| {
                    let entry = entry.unwrap();
                    entry.file_name().to_string_lossy().ends_with(".part")
                        && entry.metadata().unwrap().len() > 0
                });
                if has_bytes {
                    return;
                }
                tokio::time::sleep(Duration::from_millis(5)).await;
            }
        })
        .await
        .unwrap();
    }
    fn assert_no_partial(&self) {
        assert_eq!(std::fs::read_dir(self.output.path()).unwrap().count(), 0);
    }
}
struct Picker(Option<PathBuf>);
#[async_trait]
impl AuditSavePicker for Picker {
    async fn pick(&self, _: AuditFormat) -> Result<Option<PathBuf>> {
        Ok(self.0.clone())
    }
}

#[tokio::test]
async fn full_csv_and_json_streams_include_all_123_rows_and_encode_the_applied_filter() {
    let h = Harness::new().await;
    for (format, extension) in [(AuditFormat::Csv, "csv"), (AuditFormat::Json, "json")] {
        let target = h.target(extension);
        let result = h.run(format, Some(target.clone())).await.unwrap();
        let bytes = std::fs::read(&target).unwrap();
        match result {
            ExportOutcome::Saved { path, bytes: count } => {
                assert_eq!(path, target.to_string_lossy());
                assert_eq!(count, bytes.len() as u64);
            }
            _ => panic!("export was cancelled"),
        }
        if extension == "csv" {
            assert_eq!(String::from_utf8(bytes).unwrap().lines().count(), 124);
        } else {
            let data: Value = serde_json::from_slice(&bytes).unwrap();
            assert_eq!(data["events"].as_array().unwrap().len(), 123);
            assert_eq!(data["total"], 123);
        }
    }
    let requests = h.state.requests.lock().await;
    assert_eq!(requests.len(), 2);
    assert_eq!(requests[0]["format"], "csv");
    assert_eq!(requests[1]["format"], "json");
    for query in requests.iter() {
        assert_eq!(query["action"], "file.upload&actorId=someone");
        assert_eq!(query.len(), 2);
    }
}

#[tokio::test]
async fn cancelling_the_picker_does_not_start_a_request_or_create_a_file() {
    let h = Harness::new().await;
    assert!(matches!(
        h.run(AuditFormat::Json, None).await.unwrap(),
        ExportOutcome::Cancelled
    ));
    assert!(h.state.requests.lock().await.is_empty());
    h.assert_no_partial();
}

#[tokio::test]
async fn existing_destination_is_never_overwritten() {
    let h = Harness::new().await;
    let target = h.target("csv");
    std::fs::write(&target, b"existing user file").unwrap();
    assert!(h.run(AuditFormat::Csv, Some(target.clone())).await.is_err());
    assert_eq!(std::fs::read(target).unwrap(), b"existing user file");
    assert_eq!(std::fs::read_dir(h.output.path()).unwrap().count(), 1);
    assert!(h.state.requests.lock().await.is_empty());
}

#[tokio::test]
async fn a_file_created_while_streaming_wins_publication_without_clobber() {
    let h = Harness::new().await;
    let running = h.start_blocked().await;
    assert!(!h.target("csv").exists());
    std::fs::write(h.target("csv"), b"new user file").unwrap();
    h.state.release.notify_one();
    assert!(running.await.unwrap().is_err());
    assert_eq!(std::fs::read(h.target("csv")).unwrap(), b"new user file");
    assert_eq!(std::fs::read_dir(h.output.path()).unwrap().count(), 1);
}

#[tokio::test]
async fn aborted_http_body_never_publishes_partial_csv_or_json_and_never_restarts() {
    let h = Harness::new().await;
    h.state.mode.store(2, Ordering::SeqCst);
    for (format, extension) in [(AuditFormat::Csv, "csv"), (AuditFormat::Json, "json")] {
        let job = h
            .manager
            .begin(
                h.auth.clone(),
                ExportRequest::new(format, "".into()).unwrap(),
            )
            .await
            .unwrap();
        let picker = Picker(Some(h.target(extension)));
        let running = tokio::spawn(async move { job.save(&picker).await });
        tokio::time::timeout(Duration::from_secs(5), h.state.body_started.notified())
            .await
            .unwrap();
        h.wait_for_partial().await;
        assert!(!h.target(extension).exists());
        h.state.release.notify_one();
        assert!(running.await.unwrap().is_err());
        h.assert_no_partial();
    }
    assert_eq!(h.state.requests.lock().await.len(), 2);
    assert_eq!(h.state.refreshes.load(Ordering::SeqCst), 0);
}

struct PendingPicker(Arc<Notify>);
#[async_trait]
impl AuditSavePicker for PendingPicker {
    async fn pick(&self, _: AuditFormat) -> Result<Option<PathBuf>> {
        self.0.notify_one();
        std::future::pending().await
    }
}

struct CountingPicker {
    calls: Arc<AtomicUsize>,
    destination: PathBuf,
}
#[async_trait]
impl AuditSavePicker for CountingPicker {
    async fn pick(&self, _: AuditFormat) -> Result<Option<PathBuf>> {
        self.calls.fetch_add(1, Ordering::SeqCst);
        Ok(Some(self.destination.clone()))
    }
}

#[tokio::test]
async fn startup_cancellation_while_registration_waits_on_lifecycle_never_starts_the_picker() {
    let root = tempfile::tempdir().unwrap();
    let output = tempfile::tempdir().unwrap();
    let auth = Arc::new(AuthManager::new(root.path()).unwrap());
    let manager = Arc::new(AuditExportManager::new().unwrap());
    let lifecycle = Mutex::new(());
    let blocked = lifecycle.lock().await;
    let export_id = manager.reserve().unwrap();
    let calls = Arc::new(AtomicUsize::new(0));
    let picker = CountingPicker {
        calls: calls.clone(),
        destination: output.path().join("audit.csv"),
    };
    let mut export = Box::pin(manager.export_reserved(
        &export_id,
        &lifecycle,
        auth.clone(),
        ExportRequest::new(AuditFormat::Csv, "".into()).unwrap(),
        &picker,
    ));
    assert!(futures_util::poll!(export.as_mut()).is_pending());
    let mut cancel = Box::pin(manager.cancel(&export_id));
    let cancellation_finished = futures_util::poll!(cancel.as_mut()).is_ready();
    drop(blocked);
    let result = tokio::time::timeout(Duration::from_secs(2), async {
        if cancellation_finished {
            export.await
        } else {
            let (result, ()) = tokio::join!(export, cancel);
            result
        }
    })
    .await
    .unwrap();
    assert!(matches!(result.unwrap(), ExportOutcome::Cancelled));
    assert_eq!(
        calls.load(Ordering::SeqCst),
        0,
        "cancelled startup must never open the picker"
    );
    assert_eq!(std::fs::read_dir(output.path()).unwrap().count(), 0);
    assert!(auth.current().await.is_err());
}

#[tokio::test]
async fn startup_cancellation_before_export_command_dispatch_consumes_the_reservation() {
    let root = tempfile::tempdir().unwrap();
    let output = tempfile::tempdir().unwrap();
    let auth = Arc::new(AuthManager::new(root.path()).unwrap());
    let manager = Arc::new(AuditExportManager::new().unwrap());
    let export_id = manager.reserve().unwrap();
    manager.cancel(&export_id).await;
    let calls = Arc::new(AtomicUsize::new(0));
    let picker = CountingPicker {
        calls: calls.clone(),
        destination: output.path().join("audit.csv"),
    };
    let result = manager
        .export_reserved(
            &export_id,
            &Mutex::new(()),
            auth.clone(),
            ExportRequest::new(AuditFormat::Csv, "".into()).unwrap(),
            &picker,
        )
        .await
        .unwrap();
    assert!(matches!(result, ExportOutcome::Cancelled));
    assert_eq!(calls.load(Ordering::SeqCst), 0);
    assert_eq!(std::fs::read_dir(output.path()).unwrap().count(), 0);
    assert!(auth.current().await.is_err());
}

#[tokio::test]
async fn startup_logout_cancellation_finishes_while_lifecycle_remains_locked() {
    let root = tempfile::tempdir().unwrap();
    let output = tempfile::tempdir().unwrap();
    let auth = Arc::new(AuthManager::new(root.path()).unwrap());
    let manager = Arc::new(AuditExportManager::new().unwrap());
    let lifecycle = Mutex::new(());
    let blocked = lifecycle.lock().await;
    let export_id = manager.reserve().unwrap();
    let calls = Arc::new(AtomicUsize::new(0));
    let picker = CountingPicker {
        calls: calls.clone(),
        destination: output.path().join("audit.csv"),
    };
    let mut export = Box::pin(manager.export_reserved(
        &export_id,
        &lifecycle,
        auth,
        ExportRequest::new(AuditFormat::Csv, "".into()).unwrap(),
        &picker,
    ));
    assert!(futures_util::poll!(export.as_mut()).is_pending());
    let (result, ()) = tokio::time::timeout(Duration::from_secs(2), async {
        tokio::join!(export, manager.cancel_and_wait())
    })
    .await
    .unwrap();
    assert!(matches!(result.unwrap(), ExportOutcome::Cancelled));
    assert_eq!(calls.load(Ordering::SeqCst), 0);
    assert_eq!(std::fs::read_dir(output.path()).unwrap().count(), 0);
    drop(blocked);
    let next = manager.reserve().unwrap();
    manager.cancel(&next).await;
}

#[tokio::test]
async fn startup_stale_cancellation_cannot_cancel_a_newer_export_reservation() {
    let manager = Arc::new(AuditExportManager::new().unwrap());
    let old = manager.reserve().unwrap();
    manager.cancel(&old).await;
    let current = manager.reserve().unwrap();
    assert_ne!(current, old);
    manager.cancel(&old).await;
    assert!(
        manager.reserve().is_err(),
        "the current reservation must still own its slot"
    );
    manager.cancel(&current).await;
    let next = manager.reserve().unwrap();
    manager.cancel(&next).await;
}

#[tokio::test]
async fn startup_authentication_error_releases_its_reserved_slot() {
    let root = tempfile::tempdir().unwrap();
    let auth = Arc::new(AuthManager::new(root.path()).unwrap());
    let manager = Arc::new(AuditExportManager::new().unwrap());
    assert!(manager
        .begin(
            auth,
            ExportRequest::new(AuditFormat::Csv, "".into()).unwrap()
        )
        .await
        .is_err());
    let next = manager.reserve().unwrap();
    manager.cancel(&next).await;
}

#[tokio::test]
async fn cancellation_interrupts_an_open_picker_without_starting_http() {
    let h = Harness::new().await;
    let opened = Arc::new(Notify::new());
    let picker = PendingPicker(opened.clone());
    let job = h
        .manager
        .begin(
            h.auth.clone(),
            ExportRequest::new(AuditFormat::Csv, "".into()).unwrap(),
        )
        .await
        .unwrap();
    let running = tokio::spawn(async move { job.save(&picker).await });
    tokio::time::timeout(Duration::from_secs(2), opened.notified())
        .await
        .unwrap();
    tokio::time::timeout(Duration::from_secs(2), h.manager.cancel_and_wait())
        .await
        .unwrap();
    assert!(matches!(
        running.await.unwrap().unwrap(),
        ExportOutcome::Cancelled
    ));
    assert!(h.state.requests.lock().await.is_empty());
    h.assert_no_partial();
}

#[tokio::test]
async fn shutdown_cancels_partial_export_and_refuses_new_jobs() {
    let h = Harness::new().await;
    let running = h.start_blocked().await;
    tokio::time::timeout(Duration::from_secs(2), h.manager.shutdown())
        .await
        .unwrap();
    assert!(matches!(
        running.await.unwrap().unwrap(),
        ExportOutcome::Cancelled
    ));
    h.assert_no_partial();
    assert!(h
        .manager
        .begin(
            h.auth.clone(),
            ExportRequest::new(AuditFormat::Csv, "".into()).unwrap()
        )
        .await
        .is_err());
}

#[tokio::test]
async fn cancellation_waits_for_partial_cleanup_and_allows_the_next_export() {
    let h = Harness::new().await;
    let running = h.start_blocked().await;
    assert!(h
        .manager
        .begin(
            h.auth.clone(),
            ExportRequest::new(AuditFormat::Json, "".into()).unwrap()
        )
        .await
        .is_err());
    tokio::time::timeout(Duration::from_secs(2), h.manager.cancel_and_wait())
        .await
        .unwrap();
    assert!(matches!(
        running.await.unwrap().unwrap(),
        ExportOutcome::Cancelled
    ));
    h.assert_no_partial();
    h.state.mode.store(0, Ordering::SeqCst);
    assert!(matches!(
        h.run(AuditFormat::Csv, Some(h.target("csv")))
            .await
            .unwrap(),
        ExportOutcome::Saved { .. }
    ));
}

#[tokio::test]
async fn logout_and_account_switch_interrupt_a_stalled_response_without_publishing() {
    for logout in [true, false] {
        let h = Harness::new().await;
        let running = h.start_blocked().await;
        if logout {
            h.auth.logout().await.unwrap();
        } else {
            h.auth
                .login(&h.origin, "another-admin", "password", None)
                .await
                .unwrap();
        }
        assert!(tokio::time::timeout(Duration::from_secs(2), running)
            .await
            .unwrap()
            .unwrap()
            .is_err());
        h.assert_no_partial();
    }
}

#[tokio::test]
async fn a_401_refreshes_before_streaming_without_concatenating_responses() {
    let h = Harness::new().await;
    h.state.mode.store(3, Ordering::SeqCst);
    h.run(AuditFormat::Json, Some(h.target("json")))
        .await
        .unwrap();
    let data: Value = serde_json::from_slice(&std::fs::read(h.target("json")).unwrap()).unwrap();
    assert_eq!(data["total"], 123);
    assert_eq!(h.state.requests.lock().await.len(), 2);
    assert_eq!(h.state.refreshes.load(Ordering::SeqCst), 1);
}

#[tokio::test]
async fn permission_and_network_errors_leave_no_destination_or_partial_file() {
    let h = Harness::new().await;
    h.state.mode.store(4, Ordering::SeqCst);
    assert_eq!(
        h.run(AuditFormat::Csv, Some(h.target("csv")))
            .await
            .unwrap_err()
            .status,
        Some(403)
    );
    h.assert_no_partial();
    h.server.abort();
    tokio::task::yield_now().await;
    assert!(h
        .run(AuditFormat::Json, Some(h.target("json")))
        .await
        .is_err());
    h.assert_no_partial();
}

#[test]
fn renderer_format_and_query_are_bounded_and_restricted() {
    assert!(serde_json::from_str::<AuditFormat>("\"../../secrets\"").is_err());
    assert!(ExportRequest::new(AuditFormat::Csv, "x".repeat(257)).is_err());
    assert!(ExportRequest::new(AuditFormat::Json, "file.upload\n".into()).is_err());
}
