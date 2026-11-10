//! Opt-in deployment check. Supply LINKNESTX_TLS_SMOKE_ORIGIN and credential
//! FILE paths in LINKNESTX_TLS_SMOKE_USERNAME_FILE / PASSWORD_FILE. Trust comes
//! from the OS certificate store or SSL_CERT_FILE; verification stays enabled.
use linknestx::{
    auth::{
        session::{normalize_origin, AuthManager},
        socket::SocketBridge,
    },
    events::EventSink,
};
use serde_json::{json, Value};
use std::{sync::Arc, time::Duration};
use tokio::sync::mpsc;

struct Events(mpsc::Sender<Value>);
impl EventSink for Events {
    fn emit(&self, name: &str, value: Value) {
        if name == "server.event"
            && matches!(value["type"].as_str(), Some("session.ready" | "pong"))
        {
            let _ = self.0.try_send(value);
        }
    }
}

fn credential(variable: &str) -> String {
    let path =
        std::env::var_os(variable).unwrap_or_else(|| panic!("set {variable} to a credential file"));
    let value = std::fs::read_to_string(path).expect("cannot read deployment credential file");
    let value = value.trim_end_matches(['\r', '\n']).to_owned();
    assert!(!value.is_empty(), "deployment credential file is empty");
    value
}

fn unknown_issuer(mut error: &(dyn std::error::Error + 'static)) -> bool {
    loop {
        if error.to_string().contains("UnknownIssuer") {
            return true;
        }
        match error.source() {
            Some(source) => error = source,
            None => return false,
        }
    }
}

#[tokio::test]
#[ignore = "requires the authorized deployment BEFORE trusting its private CA; no credentials are read or sent"]
async fn untrusted_deployment_rejects_https_and_wss() {
    let origin = normalize_origin(
        &std::env::var("LINKNESTX_TLS_SMOKE_ORIGIN").expect("set LINKNESTX_TLS_SMOKE_ORIGIN"),
    )
    .expect("deployment origin is invalid");
    assert!(
        origin.starts_with("https://"),
        "deployment trust check requires HTTPS"
    );
    let http = reqwest::Client::builder()
        .redirect(reqwest::redirect::Policy::none())
        .timeout(Duration::from_secs(15))
        .build()
        .unwrap()
        .get(format!("{origin}/healthz"))
        .send()
        .await;
    match http {
        Ok(_) => panic!("HTTPS unexpectedly trusted the private CA"),
        Err(error) => assert!(
            unknown_issuer(&error),
            "HTTPS failure was not UnknownIssuer: {error:?}"
        ),
    }
    let websocket = tokio::time::timeout(
        Duration::from_secs(15),
        tokio_tungstenite::connect_async(format!(
            "{}/api/v1/ws",
            origin.replacen("https://", "wss://", 1)
        )),
    )
    .await
    .expect("WSS certificate rejection timed out");
    match websocket {
        Ok(_) => panic!("WSS unexpectedly trusted the private CA"),
        Err(error) => assert!(
            unknown_issuer(&error),
            "WSS failure was not UnknownIssuer: {error:?}"
        ),
    }
    println!("untrusted deployment HTTPS and WSS both rejected UnknownIssuer");
}

#[tokio::test]
#[ignore = "requires an authorized HTTPS deployment and credential files; SSL_CERT_FILE may select its trusted CA"]
async fn trusted_deployment_native_https_and_authenticated_wss() {
    let origin = normalize_origin(
        &std::env::var("LINKNESTX_TLS_SMOKE_ORIGIN").expect("set LINKNESTX_TLS_SMOKE_ORIGIN"),
    )
    .expect("deployment origin is invalid");
    assert!(
        origin.starts_with("https://"),
        "deployment trust check requires HTTPS"
    );
    authenticated_deployment_check(&origin).await;
}

#[tokio::test]
#[ignore = "requires an authorized private IPv4 HTTP deployment and credential files; no CA is needed"]
async fn private_lan_deployment_native_http_and_authenticated_ws() {
    let origin = normalize_origin(
        &std::env::var("LINKNESTX_HTTP_SMOKE_ORIGIN").expect("set LINKNESTX_HTTP_SMOKE_ORIGIN"),
    )
    .expect("private LAN deployment origin is invalid");
    assert!(
        origin.starts_with("http://"),
        "LAN HTTP check requires explicit HTTP"
    );
    authenticated_deployment_check(&origin).await;
}

async fn authenticated_deployment_check(origin: &str) {
    let username = credential("LINKNESTX_TLS_SMOKE_USERNAME_FILE");
    let password = credential("LINKNESTX_TLS_SMOKE_PASSWORD_FILE");
    let health = reqwest::Client::builder()
        .redirect(reqwest::redirect::Policy::none())
        .timeout(Duration::from_secs(15))
        .build()
        .expect("cannot initialize native HTTP client")
        .get(format!("{origin}/healthz"))
        .send()
        .await
        .expect("HTTP health request failed certificate or transport validation");
    assert!(
        health.status().is_success(),
        "HTTP health returned a failure status"
    );

    let root = tempfile::tempdir().unwrap();
    let auth =
        Arc::new(AuthManager::new(root.path()).expect("cannot initialize native authentication"));
    let session = tokio::time::timeout(
        Duration::from_secs(20),
        auth.login(&origin, &username, &password, None),
    )
    .await
    .expect("native login timed out")
    .expect("native login failed");
    drop(password);
    let bridge = SocketBridge::default();
    let checks = tokio::time::timeout(Duration::from_secs(35), async {
        if session.user.must_change_password {
            return Err("deployment account must complete its initial password change first");
        }
        let me = auth
            .json("GET", "/api/v1/me", None)
            .await
            .map_err(|_| "authenticated HTTP identity request failed")?;
        if me["id"].as_str() != Some(session.user.id.as_str()) {
            return Err("authenticated HTTP identity did not match the session");
        }
        let files = auth
            .json("GET", "/api/v1/files?scope=pool", None)
            .await
            .map_err(|_| "authenticated HTTP file list failed")?;
        if !files.is_array() {
            return Err("authenticated HTTP file list was not an array");
        }
        let (tx, mut rx) = mpsc::channel(8);
        bridge.start(auth.clone(), Arc::new(Events(tx))).await;
        let ready = rx
            .recv()
            .await
            .ok_or("authenticated WebSocket stream ended before session.ready")?;
        if ready["type"] != "session.ready" {
            return Err("authenticated WebSocket session.ready missing");
        }
        let request_id = uuid::Uuid::new_v4().to_string();
        bridge
            .send(json!({"type":"ping","requestId":request_id}))
            .await
            .map_err(|_| "native WebSocket ping could not be queued")?;
        loop {
            let event = rx
                .recv()
                .await
                .ok_or("authenticated WebSocket stream ended before pong")?;
            if event["type"] == "pong" && event["requestId"] == request_id {
                break;
            }
        }
        Ok::<(), &str>(())
    })
    .await;
    bridge.stop().await;
    let logout = auth.logout().await;
    checks
        .expect("deployment HTTP/WebSocket checks timed out")
        .expect("deployment native transport check failed");
    logout.expect("deployment test session logout failed");
    println!("native deployment health/login/identity/files and authenticated WebSocket ping passed; test session logged out");
}
