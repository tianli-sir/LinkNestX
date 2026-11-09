use axum::{
    extract::State,
    http::{HeaderMap, StatusCode},
    routing::{get, post},
    Json, Router,
};
use linknestx::{
    auth::{session::AuthManager, socket::SocketBridge},
    commands::NativeState,
    events::NoopEvents,
    transfer::service::TransferService,
};
use serde_json::{json, Value};
use std::{
    collections::HashSet,
    sync::{
        atomic::{AtomicBool, AtomicUsize, Ordering},
        Arc,
    },
};
use tokio::sync::Mutex;

#[derive(Clone, Default)]
struct Server {
    rotated: Arc<AtomicBool>,
    revoked: Arc<AtomicBool>,
    changed: Arc<AtomicBool>,
    refreshes: Arc<AtomicUsize>,
    logout_error: Arc<AtomicBool>,
}
fn user(changed: bool) -> Value {
    json!({"id":"alice-id","username":"alice","roles":["user"],"disabled":false,"mustChangePassword":!changed})
}
fn session(rotated: bool, changed: bool) -> Value {
    json!({"id":if rotated{"rotated-session"}else{"session"},"accessToken":if rotated{"fresh-access"}else{"expired-access"},"refreshToken":if rotated{"rotated-refresh"}else{"original-refresh"},"expiresAt":"2099-01-01T00:00:00Z","deviceId":"device","user":user(changed)})
}
async fn refresh(
    State(s): State<Server>,
    Json(body): Json<Value>,
) -> Result<Json<Value>, StatusCode> {
    let token = body["refreshToken"].as_str().unwrap_or("");
    if s.revoked.load(Ordering::SeqCst)
        || token
            != if s.rotated.load(Ordering::SeqCst) {
                "rotated-refresh"
            } else {
                "original-refresh"
            }
    {
        return Err(StatusCode::UNAUTHORIZED);
    }
    s.rotated.store(true, Ordering::SeqCst);
    s.refreshes.fetch_add(1, Ordering::SeqCst);
    Ok(Json(session(true, s.changed.load(Ordering::SeqCst))))
}
async fn logout(State(s): State<Server>, headers: HeaderMap) -> StatusCode {
    if s.logout_error.load(Ordering::SeqCst) {
        return StatusCode::SERVICE_UNAVAILABLE;
    }
    if headers.get("authorization").and_then(|v| v.to_str().ok()) != Some("Bearer fresh-access") {
        return StatusCode::UNAUTHORIZED;
    }
    s.revoked.store(true, Ordering::SeqCst);
    StatusCode::NO_CONTENT
}
async fn password(
    State(s): State<Server>,
    headers: HeaderMap,
    Json(body): Json<Value>,
) -> Result<Json<Value>, StatusCode> {
    if headers.get("authorization").and_then(|v| v.to_str().ok()) != Some("Bearer fresh-access") {
        return Err(StatusCode::UNAUTHORIZED);
    }
    if body != json!({"currentPassword":"temporary","newPassword":"new-password"}) {
        return Err(StatusCode::BAD_REQUEST);
    }
    s.changed.store(true, Ordering::SeqCst);
    Ok(Json(user(true)))
}
struct Harness {
    root: tempfile::TempDir,
    origin: String,
    server: Server,
    native: NativeState,
    task: tokio::task::JoinHandle<()>,
}
impl Drop for Harness {
    fn drop(&mut self) {
        self.task.abort();
    }
}
impl Harness {
    async fn new() -> Self {
        let server = Server::default();
        let app = Router::new()
            .route(
                "/api/v1/auth/login",
                post(|| async { Json(session(false, false)) }),
            )
            .route("/api/v1/auth/refresh", post(refresh))
            .route("/api/v1/auth/logout", post(logout))
            .route("/api/v1/auth/password", post(password))
            .route(
                "/api/v1/me",
                get(|headers: HeaderMap| async move {
                    if headers.get("authorization").unwrap() == "Bearer fresh-access" {
                        Ok(Json(user(false)))
                    } else {
                        Err(StatusCode::UNAUTHORIZED)
                    }
                }),
            )
            .with_state(server.clone());
        let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
        let origin = format!("http://{}", listener.local_addr().unwrap());
        let task = tokio::spawn(async move { axum::serve(listener, app).await.unwrap() });
        let root = tempfile::tempdir().unwrap();
        let auth = Arc::new(AuthManager::new(root.path()).unwrap());
        let events = Arc::new(NoopEvents);
        let transfers = TransferService::open(root.path(), auth.clone(), events.clone()).unwrap();
        let native = NativeState {
            auth,
            transfers,
            events,
            socket: SocketBridge::default(),
            lifecycle: Mutex::new(()),
            exiting: AtomicBool::new(false),
            exit_ready: AtomicBool::new(false),
            uploads: Mutex::new(HashSet::new()),
            destinations: Mutex::new(HashSet::new()),
        };
        Self {
            root,
            origin,
            server,
            native,
            task,
        }
    }
    async fn login(&self) {
        let session = self
            .native
            .auth
            .login(&self.origin, "alice", "temporary", None)
            .await
            .unwrap();
        assert_eq!(session.user.email, "");
    }
}

#[tokio::test]
async fn go_shaped_session_without_optional_email_supports_login_and_refresh() {
    let h = Harness::new().await;
    h.login().await;
    h.native.auth.json("GET", "/api/v1/me", None).await.unwrap();
    assert_eq!(
        h.native.auth.current().await.unwrap().session.user.email,
        ""
    );
    assert_eq!(h.server.refreshes.load(Ordering::SeqCst), 1);
    for entry in std::fs::read_dir(h.root.path()).unwrap() {
        let path = entry.unwrap().path();
        if path.is_file() {
            let data = std::fs::read(path).unwrap();
            assert!(!data
                .windows("original-refresh".len())
                .any(|s| s == b"original-refresh"));
        }
    }
    h.native.stop().await.unwrap();
}

#[tokio::test]
async fn password_change_works_through_bridge_and_updates_native_user() {
    let h = Harness::new().await;
    h.login().await;
    let result = h
        .native
        .api_request(
            "POST",
            "/api/v1/auth/password",
            Some(json!({"currentPassword":"temporary","newPassword":"new-password"})),
        )
        .await
        .unwrap();
    assert_eq!(result["mustChangePassword"], false);
    assert_eq!(result["email"], "");
    assert!(
        !h.native
            .auth
            .current()
            .await
            .unwrap()
            .session
            .user
            .must_change_password
    );
    for path in [
        "/api/v1/auth/login",
        "/api/v1/auth/refresh",
        "/api/v1/auth/logout",
    ] {
        assert!(h.native.api_request("POST", path, None).await.is_err());
    }
    assert!(h.native.auth.current().await.is_ok());
    h.native.stop().await.unwrap();
}

#[tokio::test]
async fn logout_with_expired_access_rotates_then_revokes_refresh_credentials() {
    let h = Harness::new().await;
    h.login().await;
    h.native.auth.logout().await.unwrap();
    assert!(h.native.auth.current().await.is_err());
    assert!(h.server.revoked.load(Ordering::SeqCst));
    for token in ["original-refresh", "rotated-refresh"] {
        let response = reqwest::Client::new()
            .post(format!("{}/api/v1/auth/refresh", h.origin))
            .json(&json!({"refreshToken":token}))
            .send()
            .await
            .unwrap();
        assert_eq!(response.status(), StatusCode::UNAUTHORIZED);
    }
    h.native.stop().await.unwrap();
}

#[tokio::test]
async fn logout_failure_still_removes_local_credentials() {
    let h = Harness::new().await;
    h.login().await;
    h.server.logout_error.store(true, Ordering::SeqCst);
    assert!(h.native.auth.logout().await.is_err());
    assert!(h.native.auth.current().await.is_err());
    h.native.stop().await.unwrap();
}
