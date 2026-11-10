use axum::{extract::State, http::StatusCode, routing::post, Json, Router};
use linknestx::auth::session::AuthManager;
use linknestx::peer::audit::{AuditOutbox, AuditRecord};
use serde_json::{json, Value};
use std::sync::{Arc, Mutex};

#[derive(Default)]
struct Reports {
    attempts: Mutex<Vec<String>>,
    failures: Mutex<std::collections::HashMap<String, StatusCode>>,
}
async fn submit(State(state): State<Arc<Reports>>, Json(value): Json<Value>) -> StatusCode {
    let task = value["taskId"].as_str().unwrap().to_string();
    state.attempts.lock().unwrap().push(task.clone());
    state
        .failures
        .lock()
        .unwrap()
        .get(&task)
        .copied()
        .unwrap_or(StatusCode::NO_CONTENT)
}
async fn outbox_fixture() -> (
    tempfile::TempDir,
    AuthManager,
    AuditOutbox,
    Arc<Reports>,
    tokio::task::JoinHandle<()>,
) {
    let root = tempfile::tempdir().unwrap();
    let reports = Arc::new(Reports::default());
    let app = Router::new().route("/api/v1/auth/login", post(|| async { Json(json!({
        "id":"session", "accessToken":"access", "refreshToken":"refresh", "deviceId":"00000000-0000-0000-0000-000000000001", "expiresAt":"2099-01-01T00:00:00Z",
        "user":{"id":"00000000-0000-0000-0000-000000000002", "username":"user", "roles":[], "disabled":false, "mustChangePassword":false}
    })) })).route("/api/v1/p2p/transfers", post(submit)).with_state(reports.clone());
    let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
    let origin = format!("http://{}", listener.local_addr().unwrap());
    let server = tokio::spawn(async move { axum::serve(listener, app).await.unwrap() });
    let auth = AuthManager::new(root.path()).unwrap();
    auth.login(&origin, "user", "pass", None).await.unwrap();
    let outbox = AuditOutbox::open(root.path()).unwrap();
    (root, auth, outbox, reports, server)
}
fn entry() -> AuditRecord {
    AuditRecord {
        task_id: uuid::Uuid::new_v4().to_string(),
        version_id: uuid::Uuid::new_v4().to_string(),
        node_id: uuid::Uuid::new_v4().to_string(),
        action: "peer.received".into(),
        bytes: 0,
    }
}

#[tokio::test]
async fn rejected_historical_report_does_not_starve_later_valid_report() {
    for status in [
        StatusCode::BAD_REQUEST,
        StatusCode::FORBIDDEN,
        StatusCode::NOT_FOUND,
    ] {
        let (root, auth, outbox, reports, server) = outbox_fixture().await;
        let owner = auth.owner().await.unwrap();
        let bad = entry();
        let good = entry();
        outbox.record(&owner, &bad, 0, 3, 3).unwrap();
        outbox.record(&owner, &good, 0, 3, 3).unwrap();
        reports
            .failures
            .lock()
            .unwrap()
            .insert(bad.task_id.clone(), status);
        assert!(outbox.flush(&auth).await.is_err());
        assert!(
            reports.attempts.lock().unwrap().contains(&good.task_id),
            "invalid old record must not stop the batch"
        );
        assert!(outbox.pending(&owner).unwrap().is_empty());
        drop(outbox);
        let outbox = AuditOutbox::open(root.path()).unwrap();
        assert!(outbox.pending(&owner).unwrap().is_empty());
        assert_eq!(
            outbox.failures(&owner).unwrap()[0].status,
            Some(status.as_u16())
        );
        assert_eq!(
            outbox.failures(&owner).unwrap()[0].record.task_id,
            bad.task_id
        );
        // The rejected evidence remains durably inspectable instead of silently disappearing.
        let db = rusqlite::Connection::open(root.path().join("peer-audit.sqlite3")).unwrap();
        let retained: u64 = db
            .query_row(
                "SELECT bytes FROM pending WHERE task=?1",
                [&bad.task_id],
                |r| r.get(0),
            )
            .unwrap();
        assert_eq!(retained, 3);
        server.abort();
    }
}

#[tokio::test]
async fn transient_report_failure_remains_retryable_without_blocking_valid_reports() {
    let (_root, auth, outbox, reports, server) = outbox_fixture().await;
    let owner = auth.owner().await.unwrap();
    let unavailable = entry();
    let good = entry();
    outbox.record(&owner, &unavailable, 0, 3, 3).unwrap();
    outbox.record(&owner, &good, 0, 3, 3).unwrap();
    reports
        .failures
        .lock()
        .unwrap()
        .insert(unavailable.task_id.clone(), StatusCode::SERVICE_UNAVAILABLE);
    assert!(outbox.flush(&auth).await.is_err());
    assert!(reports.attempts.lock().unwrap().contains(&good.task_id));
    assert_eq!(outbox.pending(&owner).unwrap().len(), 1);
    reports.failures.lock().unwrap().clear();
    outbox.flush(&auth).await.unwrap();
    assert!(outbox.pending(&owner).unwrap().is_empty());
    server.abort();
}

#[tokio::test]
async fn more_than_one_batch_of_transient_failures_cannot_starve_new_evidence() {
    let (_root, auth, outbox, reports, server) = outbox_fixture().await;
    let owner = auth.owner().await.unwrap();
    for _ in 0..64 {
        let bad = entry();
        outbox.record(&owner, &bad, 0, 3, 3).unwrap();
        reports
            .failures
            .lock()
            .unwrap()
            .insert(bad.task_id, StatusCode::SERVICE_UNAVAILABLE);
    }
    let good = entry();
    outbox.record(&owner, &good, 0, 3, 3).unwrap();
    assert!(outbox.flush(&auth).await.is_err());
    assert!(!reports.attempts.lock().unwrap().contains(&good.task_id));
    assert!(outbox.flush(&auth).await.is_err());
    assert!(reports.attempts.lock().unwrap().contains(&good.task_id));
    server.abort();
}
#[test]
fn peer_audit_survives_restart_deduplicates_chunks_and_isolates_accounts() {
    let root = tempfile::tempdir().unwrap();
    let record = AuditRecord {
        task_id: uuid::Uuid::new_v4().to_string(),
        version_id: uuid::Uuid::new_v4().to_string(),
        node_id: uuid::Uuid::new_v4().to_string(),
        action: "peer.received".into(),
        bytes: 0,
    };
    let outbox = AuditOutbox::open(root.path()).unwrap();
    outbox.record("owner", &record, 0, 3, 6).unwrap();
    outbox.record("owner", &record, 0, 3, 6).unwrap();
    outbox.record("owner", &record, 1, 2, 6).unwrap();
    assert_eq!(outbox.pending("owner").unwrap()[0].bytes, 5);
    assert!(outbox.pending("other").unwrap().is_empty());
    drop(outbox);
    let outbox = AuditOutbox::open(root.path()).unwrap();
    assert_eq!(outbox.pending("owner").unwrap()[0].bytes, 5);
    outbox.record("owner", &record, 1, 3, 6).unwrap();
    assert_eq!(outbox.pending("owner").unwrap()[0].bytes, 6);
    assert!(outbox.record("owner", &record, 2, 1, 6).is_err());
    assert_eq!(outbox.pending("owner").unwrap()[0].bytes, 6);
}

#[test]
fn completion_sources_survive_resume_and_only_publish_after_success() {
    let root = tempfile::tempdir().unwrap();
    let outbox = AuditOutbox::open(root.path()).unwrap();
    let mut peer = entry();
    peer.action = "transfer.completed".into();
    let mut server = peer.clone();
    server.node_id = "server".into();
    outbox.stage_completion("owner", &peer, 0, 3, 6).unwrap();
    assert!(outbox.pending("owner").unwrap().is_empty());
    drop(outbox);
    let outbox = AuditOutbox::open(root.path()).unwrap();
    // A retry of the already accepted chunk cannot invent a second source credit.
    outbox.stage_completion("owner", &server, 0, 3, 6).unwrap();
    outbox.stage_completion("owner", &server, 1, 3, 6).unwrap();
    assert!(outbox.pending("owner").unwrap().is_empty());
    outbox
        .publish_completion("owner", &peer.task_id, &peer.version_id)
        .unwrap();
    outbox
        .publish_completion("owner", &peer.task_id, &peer.version_id)
        .unwrap();
    let records = outbox.pending("owner").unwrap();
    assert_eq!(records.len(), 2);
    assert_eq!(records.iter().map(|r| r.bytes).sum::<u64>(), 6);
    assert!(records
        .iter()
        .all(|r| r.action == "transfer.completed" && r.bytes == 3));
    assert!(outbox.pending("other").unwrap().is_empty());
}
