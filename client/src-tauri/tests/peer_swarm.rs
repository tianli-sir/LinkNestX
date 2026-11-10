// Real SCTP/DTLS/ICE DataChannels. The local HTTP/WS fixture implements the frozen
// server contract; no ChunkSource or peer transport is replaced by a fake.
use axum::{
    extract::{
        ws::{Message, WebSocketUpgrade},
        Path, State,
    },
    http::{HeaderMap, StatusCode},
    response::{IntoResponse, Response},
    routing::{get, post},
    Json, Router,
};
use futures_util::{SinkExt, StreamExt};
use linknestx::{
    auth::session::AuthManager,
    events::EventSink,
    transfer::{models::*, service::TransferService},
};
use serde_json::{json, Value};
use std::{
    collections::HashMap,
    sync::{
        atomic::{AtomicBool, Ordering},
        Arc,
    },
    time::Duration,
};
use tempfile::TempDir;
use tokio::sync::{mpsc, Mutex};

#[derive(Default)]
struct Events(std::sync::Mutex<Vec<(String, Value)>>);
impl EventSink for Events {
    fn emit(&self, n: &str, v: Value) {
        self.0.lock().unwrap().push((n.into(), v));
    }
}
struct Fixture {
    manifest: Manifest,
    chunks: Vec<Vec<u8>>,
    announcements: Mutex<HashMap<String, Vec<usize>>>,
    allocations: Mutex<HashMap<String, (usize, usize)>>,
    connections: Mutex<HashMap<String, Value>>,
    sockets: Mutex<HashMap<String, mpsc::UnboundedSender<Value>>>,
    socket_tokens: Mutex<Vec<String>>,
    announcements_by_token: Mutex<Vec<String>>,
    denied: AtomicBool,
    lifecycle_status: std::sync::atomic::AtomicU16,
    policy_enabled: AtomicBool,
    task_headers: Mutex<Vec<String>>,
    allocation_unavailable: AtomicBool,
    bad_final_hash: AtomicBool,
    deny_validation: AtomicBool,
    audit_failed: AtomicBool,
    audits: Mutex<HashMap<String, u64>>,
    validations: Mutex<Vec<String>>,
    served: Mutex<Vec<usize>>,
    served_by: Mutex<HashMap<String, Vec<usize>>>,
    only_third_chunk_until_peer_request: AtomicBool,
}
fn device(headers: &HeaderMap) -> String {
    headers
        .get("authorization")
        .unwrap()
        .to_str()
        .unwrap()
        .trim_start_matches("Bearer ")
        .split(':')
        .next()
        .unwrap()
        .into()
}
async fn login(Json(v): Json<Value>) -> Json<Value> {
    let d = v["deviceId"].as_str().unwrap();
    Json(
        json!({"id":d,"accessToken":d,"refreshToken":d,"deviceId":d,"expiresAt":"2099-01-01T00:00:00Z","user":{"id":"00000000-0000-0000-0000-000000000099","username":"user","roles":[],"disabled":false,"mustChangePassword":false}}),
    )
}
async fn sources(State(s): State<Arc<Fixture>>, h: HeaderMap) -> Json<Value> {
    let d = device(&h);
    let map = s.announcements.lock().await;
    let gated = s.only_third_chunk_until_peer_request.load(Ordering::SeqCst);
    let sources: Vec<_> = map.iter().filter(|(id, _)| **id != d).filter_map(|(id, indexes)| {
        let indexes: Vec<_> = indexes.iter().copied().filter(|index| !gated || *index == 2).collect();
        (!indexes.is_empty()).then(|| json!({"deviceId":id,"userId":"00000000-0000-0000-0000-000000000099","indexes":indexes,"lastSeen":"2099-01-01T00:00:00Z"}))
    }).collect();
    Json(json!({"sources":sources,"iceServers":[]}))
}
async fn announce(State(s): State<Arc<Fixture>>, h: HeaderMap, Json(v): Json<Value>) -> StatusCode {
    s.announcements_by_token
        .lock()
        .await
        .push(h["authorization"].to_str().unwrap().to_string());
    s.announcements.lock().await.insert(
        device(&h),
        serde_json::from_value(v["indexes"].clone()).unwrap(),
    );
    StatusCode::NO_CONTENT
}
async fn allocate(State(s): State<Arc<Fixture>>, h: HeaderMap, Json(v): Json<Value>) -> Response {
    if s.allocation_unavailable.load(Ordering::SeqCst) {
        return StatusCode::SERVICE_UNAVAILABLE.into_response();
    }
    assert!(uuid::Uuid::parse_str(v["taskId"].as_str().unwrap()).is_ok());
    let mut allocations = s.allocations.lock().await;
    let regions = s.chunks.len().min(3);
    let mut occupancy = vec![0; regions];
    for (start, _) in allocations.values() {
        if regions > 0 {
            occupancy[start * regions / s.chunks.len()] += 1;
        }
    }
    let selected = (0..regions)
        .min_by_key(|index| occupancy[*index])
        .unwrap_or(0);
    let range = allocations.entry(device(&h)).or_insert_with(|| {
        if regions == 0 {
            (0, 0)
        } else {
            (
                selected * s.chunks.len() / regions,
                (selected + 1) * s.chunks.len() / regions,
            )
        }
    });
    Json(
        json!({"startIndex":range.0,"endIndex":range.1,"expiresAt":(chrono::Utc::now()+chrono::Duration::seconds(60)).to_rfc3339()}),
    ).into_response()
}
async fn withdraw(State(s): State<Arc<Fixture>>, h: HeaderMap) -> StatusCode {
    s.announcements.lock().await.remove(&device(&h));
    StatusCode::NO_CONTENT
}
async fn grant(State(s): State<Arc<Fixture>>, h: HeaderMap, Json(v): Json<Value>) -> Response {
    if s.denied.load(Ordering::SeqCst) {
        return StatusCode::FORBIDDEN.into_response();
    }
    let d = device(&h);
    let Some(c) = s.manifest.chunks.iter().find(|c| c.hash == v["chunkHash"]) else {
        return StatusCode::NOT_FOUND.into_response();
    };
    Json(json!({"userId":"00000000-0000-0000-0000-000000000099","deviceId":d,"sessionId":d,"versionId":s.manifest.version_id,"chunkHash":c.hash,"size":c.size,"expiresAt":"2099-01-01T00:00:00Z","signature":"fixture-signed"})).into_response()
}
async fn validate(State(s): State<Arc<Fixture>>, Json(v): Json<Value>) -> StatusCode {
    if s.denied.load(Ordering::SeqCst)
        || s.deny_validation.load(Ordering::SeqCst)
        || v["grant"]["signature"] != "fixture-signed"
    {
        return StatusCode::FORBIDDEN;
    }
    s.validations
        .lock()
        .await
        .push(v["grant"]["deviceId"].as_str().unwrap().into());
    if s.manifest
        .chunks
        .get(2)
        .is_some_and(|chunk| v["grant"]["chunkHash"] == chunk.hash)
    {
        s.only_third_chunk_until_peer_request
            .store(false, Ordering::SeqCst);
    }
    StatusCode::NO_CONTENT
}
async fn audit(
    State(s): State<Arc<Fixture>>,
    headers: HeaderMap,
    Json(v): Json<Value>,
) -> StatusCode {
    if s.audit_failed.load(Ordering::SeqCst) {
        return StatusCode::SERVICE_UNAVAILABLE;
    }
    let key = format!(
        "{}:{}:{}:{}",
        device(&headers),
        v["taskId"],
        v["nodeId"],
        v["action"]
    );
    let mut audits = s.audits.lock().await;
    let current = audits.entry(key).or_default();
    *current = (*current).max(v["bytes"].as_u64().unwrap());
    StatusCode::NO_CONTENT
}
async fn chunk(
    State(s): State<Arc<Fixture>>,
    Path((_version, hash)): Path<(String, String)>,
    headers: HeaderMap,
) -> Response {
    if s.denied.load(Ordering::SeqCst) {
        return StatusCode::FORBIDDEN.into_response();
    }
    let i = s
        .manifest
        .chunks
        .iter()
        .position(|c| c.hash == hash)
        .unwrap();
    s.task_headers.lock().await.push(
        headers
            .get("X-Transfer-ID")
            .unwrap()
            .to_str()
            .unwrap()
            .to_string(),
    );
    s.served.lock().await.push(i);
    s.served_by
        .lock()
        .await
        .entry(device(&headers))
        .or_default()
        .push(i);
    tokio::time::sleep(Duration::from_millis(700)).await;
    s.chunks[i].clone().into_response()
}
async fn ws(State(s): State<Arc<Fixture>>, h: HeaderMap, upgrade: WebSocketUpgrade) -> Response {
    let id = device(&h);
    s.socket_tokens
        .lock()
        .await
        .push(h["authorization"].to_str().unwrap().to_string());
    upgrade.on_upgrade(move|socket|async move {
        let (mut tx,mut rx)=socket.split();let (out,mut incoming)=mpsc::unbounded_channel();s.sockets.lock().await.insert(id.clone(),out);
        tx.send(Message::Text(json!({"type":"session.ready","deviceId":id,"userId":"00000000-0000-0000-0000-000000000099"}).to_string().into())).await.unwrap();
        loop{tokio::select!{
            v=incoming.recv()=>{let Some(v)=v else{break};if tx.send(Message::Text(v.to_string().into())).await.is_err(){break}},
            msg=rx.next()=>{let Some(Ok(Message::Text(t)))=msg else{break};let mut v:Value=serde_json::from_str(&t).unwrap();v["fromDeviceId"]=json!(id);v["fromUserId"]=json!("00000000-0000-0000-0000-000000000099");if v["type"]=="peer.offer" {s.connections.lock().await.insert(v["connectionId"].as_str().unwrap().to_string(),v.clone());}let target=v["toDeviceId"].as_str().unwrap().to_string();if let Some(dest)=s.sockets.lock().await.get(&target){let _=dest.send(v);}}
        }}
        s.sockets.lock().await.remove(&id);
    })
}
async fn fixture() -> (Arc<Fixture>, String, tokio::task::JoinHandle<()>) {
    fixture_with_chunks(6).await
}
async fn fixture_with_chunks(count: usize) -> (Arc<Fixture>, String, tokio::task::JoinHandle<()>) {
    fixture_with_chunk_size(count, 32768).await
}
async fn fixture_with_chunk_size(
    count: usize,
    size: usize,
) -> (Arc<Fixture>, String, tokio::task::JoinHandle<()>) {
    let chunks: Vec<Vec<u8>> = (0..count).map(|i| vec![i as u8 + 1; size]).collect();
    let bytes = chunks.concat();
    let manifest = Manifest {
        file_id: "00000000-0000-0000-0000-000000000010".into(),
        version_id: "00000000-0000-0000-0000-000000000020".into(),
        name: "swarm.bin".into(),
        size: bytes.len() as u64,
        sha256: hash(&bytes),
        chunk_size: size as u64,
        chunks: chunks
            .iter()
            .enumerate()
            .map(|(index, b)| Chunk {
                index,
                hash: hash(b),
                size: b.len() as u64,
            })
            .collect(),
        expires_at: None,
        visibility: "pool".into(),
        sharing_policy: Default::default(),
    };
    let state = Arc::new(Fixture {
        manifest,
        chunks,
        announcements: Mutex::new(HashMap::new()),
        allocations: Mutex::new(HashMap::new()),
        connections: Mutex::new(HashMap::new()),
        sockets: Mutex::new(HashMap::new()),
        socket_tokens: Mutex::new(vec![]),
        announcements_by_token: Mutex::new(vec![]),
        denied: AtomicBool::new(false),
        lifecycle_status: std::sync::atomic::AtomicU16::new(200),
        policy_enabled: AtomicBool::new(true),
        task_headers: Mutex::new(vec![]),
        allocation_unavailable: AtomicBool::new(false),
        bad_final_hash: AtomicBool::new(false),
        deny_validation: AtomicBool::new(false),
        audit_failed: AtomicBool::new(true),
        audits: Mutex::new(HashMap::new()),
        validations: Mutex::new(vec![]),
        served: Mutex::new(vec![]),
        served_by: Mutex::new(HashMap::new()),
        only_third_chunk_until_peer_request: AtomicBool::new(false),
    });
    let app = Router::new()
        .route("/api/v1/auth/login", post(login))
        .route("/api/v1/auth/refresh", post(|Json(v): Json<Value>| async move {
            let d = v["refreshToken"].as_str().unwrap();
            Json(json!({"id":format!("{d}:rotated"),"accessToken":format!("{d}:rotated"),"refreshToken":d,"deviceId":d,"expiresAt":"2099-01-01T00:00:00Z","user":{"id":"00000000-0000-0000-0000-000000000099","username":"user","roles":[],"disabled":false,"mustChangePassword":false}}))
        }))
        .route(
            "/api/v1/auth/logout",
            post(|| async { StatusCode::NO_CONTENT }),
        )
        .route(
            "/api/v1/p2p/config",
            get(|| async { Json(json!({"iceServers":[]})) }),
        )
        .route("/api/v1/p2p/sources", get(sources))
        .route("/api/v1/p2p/announce", post(announce))
        .route("/api/v1/p2p/allocate", post(allocate))
        .route(
            "/api/v1/p2p/heartbeat",
            post(|| async { StatusCode::NO_CONTENT }),
        )
        .route("/api/v1/p2p/withdraw", post(withdraw))
        .route("/api/v1/p2p/grants", post(grant))
        .route("/api/v1/p2p/grants/validate", post(validate))
        .route("/api/v1/p2p/transfers", post(audit))
        .route("/api/v1/p2p/ws", get(ws))
        .route(
            "/api/v1/file-versions/{version}/manifest",
            get(|State(s): State<Arc<Fixture>>| async move {
                let status=s.lifecycle_status.load(Ordering::SeqCst);if status!=200{return StatusCode::from_u16(status).unwrap().into_response()}
                let mut manifest = s.manifest.clone();
                manifest.sharing_policy.enabled=s.policy_enabled.load(Ordering::SeqCst);
                if s.bad_final_hash.load(Ordering::SeqCst) { manifest.sha256 = hash(b"wrong whole-file hash"); }
                Json(manifest).into_response()
            }),
        )
        .route("/api/v1/files/{version}/chunks/{hash}", get(chunk))
        .with_state(state.clone());
    let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
    let origin = format!("http://{}", listener.local_addr().unwrap());
    let handle = tokio::spawn(async { axum::serve(listener, app).await.unwrap() });
    (state, origin, handle)
}
async fn node(origin: &str, n: u8) -> (TempDir, Arc<TransferService>, Arc<Events>) {
    let dir = tempfile::tempdir().unwrap();
    let auth = Arc::new(AuthManager::new(dir.path()).unwrap());
    auth.login(
        origin,
        "user",
        "pass",
        Some(format!("00000000-0000-0000-0000-00000000000{n}")),
    )
    .await
    .unwrap();
    let events = Arc::new(Events::default());
    let service = TransferService::open(dir.path(), auth, events.clone()).unwrap();
    (dir, service, events)
}
async fn until(mut predicate: impl AsyncFnMut() -> bool) {
    tokio::time::timeout(Duration::from_secs(15), async {
        while !predicate().await {
            tokio::time::sleep(Duration::from_millis(20)).await;
        }
    })
    .await
    .unwrap();
}

#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn three_real_nodes_swarm_reuse_partial_reject_opt_out_and_stop_on_logout() {
    let (fixture, origin, server) = fixture().await;
    let (_da, a, _ea) = node(&origin, 1).await;
    let (_db, b, _eb) = node(&origin, 2).await;
    let (dc, c, ec) = node(&origin, 3).await;
    for (node, index) in [(&a, 0), (&b, 1), (&c, 2)] {
        node.cache
            .store(
                &node.auth.owner().await.unwrap(),
                &fixture.manifest.version_id,
                &fixture.manifest.chunks[index],
                &fixture.chunks[index],
                None,
            )
            .await
            .unwrap();
        node.restore_after_login().await.unwrap();
    }
    until(async || {
        fixture.announcements.lock().await.len() == 3 && fixture.sockets.lock().await.len() == 3
    })
    .await;
    let id = c
        .start_download(
            fixture.manifest.version_id.clone(),
            dc.path().join("done.bin").to_string_lossy().into(),
        )
        .await
        .unwrap();
    until(async || {
        fixture
            .announcements
            .lock()
            .await
            .get("00000000-0000-0000-0000-000000000003")
            .is_some_and(|v| v.contains(&0))
    })
    .await;
    assert!(
        !dc.path().join("done.bin").exists(),
        "partial chunk must be advertised before complete"
    );
    let sources = a.peers.sources(&fixture.manifest).await;
    let from_c = sources.iter().find(|s| s.name().ends_with('3')).unwrap();
    assert_eq!(
        from_c
            .fetch(
                &a.auth.owner().await.unwrap(),
                &fixture.manifest,
                &fixture.manifest.chunks[0]
            )
            .await
            .unwrap(),
        fixture.chunks[0]
    );
    until(async || {
        c.list()
            .await
            .unwrap()
            .iter()
            .any(|t| t.id == id && t.state == TaskState::Completed)
    })
    .await;
    assert_eq!(
        tokio::fs::read(dc.path().join("done.bin")).await.unwrap(),
        fixture.chunks.concat()
    );
    let snapshot = c
        .list()
        .await
        .unwrap()
        .into_iter()
        .find(|t| t.id == id)
        .unwrap();
    assert_eq!(
        snapshot.metrics.credited_network_bytes,
        fixture.manifest.size - 32768
    );
    assert_eq!(snapshot.metrics.cache_bytes, 32768);
    assert_eq!(
        snapshot.metrics.network_bytes,
        fixture.manifest.size - 32768
    );
    let events = ec.0.lock().unwrap();
    let names: Vec<String> = events
        .iter()
        .filter(|(n, _)| n == "transfer.updated")
        .flat_map(|(_, v)| {
            v["activeSources"]
                .as_array()
                .unwrap()
                .iter()
                .filter_map(|v| v.as_str().map(String::from))
        })
        .collect();
    assert!(names.iter().any(|n| n == "server"));
    assert!(names.iter().any(|n| n.ends_with('1')));
    assert!(names.iter().any(|n| n.ends_with('2')));
    drop(events);
    assert!(!fixture.validations.lock().await.is_empty());
    assert_eq!(
        c.peers
            .audit
            .pending(&c.auth.owner().await.unwrap())
            .unwrap()
            .iter()
            .filter(|r| r.task_id == id && r.action == "peer.received")
            .map(|r| r.bytes)
            .sum::<u64>(),
        65536
    );
    let completed: HashMap<_, _> = c
        .peers
        .audit
        .pending(&c.auth.owner().await.unwrap())
        .unwrap()
        .into_iter()
        .filter(|r| r.task_id == id && r.action == "transfer.completed")
        .map(|r| (r.node_id, r.bytes))
        .collect();
    assert_eq!(
        completed.get("00000000-0000-0000-0000-000000000001"),
        Some(&32768)
    );
    assert_eq!(
        completed.get("00000000-0000-0000-0000-000000000002"),
        Some(&32768)
    );
    assert_eq!(completed.get("server"), Some(&98304));
    assert_eq!(
        completed.values().sum::<u64>(),
        fixture.manifest.size - 32768,
        "previous unrelated cached bytes have no invented source"
    );
    assert!(c.peers.audit.flush(&c.auth).await.is_err());
    assert!(!c
        .peers
        .audit
        .pending(&c.auth.owner().await.unwrap())
        .unwrap()
        .is_empty());
    fixture.audit_failed.store(false, Ordering::SeqCst);
    for peer in [&a, &b, &c] {
        peer.peers.audit.flush(&peer.auth).await.unwrap();
    }
    assert!(c
        .peers
        .audit
        .pending(&c.auth.owner().await.unwrap())
        .unwrap()
        .is_empty());
    assert!(fixture
        .audits
        .lock()
        .await
        .keys()
        .any(|k| k.contains("peer.sent")));
    assert!(fixture
        .audits
        .lock()
        .await
        .keys()
        .any(|k| k.contains("peer.received")));

    let mut policy = c.sharing_policy().await;
    policy.enabled = false;
    assert!(c.set_sharing_policy(policy).await.is_err());
    assert!(c.sharing_policy().await.enabled);
    c.peers.stop().await;
    c.auth.logout().await.unwrap();
    until(async || {
        !fixture
            .announcements
            .lock()
            .await
            .contains_key("00000000-0000-0000-0000-000000000003")
    })
    .await;
    assert!(from_c
        .fetch(
            &a.auth.owner().await.unwrap(),
            &fixture.manifest,
            &fixture.manifest.chunks[0]
        )
        .await
        .is_err());
    a.peers.stop().await;
    a.auth.logout().await.unwrap();
    assert!(a.peers.connection_count().await == 0);
    a.shutdown().await.unwrap();
    b.shutdown().await.unwrap();
    c.shutdown().await.unwrap();
    server.abort();
}

#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn denied_grant_cancellation_and_missing_signaling_return_to_server() {
    let (fixture, origin, server) = fixture().await;
    let (_da, a, ea) = node(&origin, 1).await;
    let (db, b, _eb) = node(&origin, 2).await;
    a.cache
        .store(
            &a.auth.owner().await.unwrap(),
            &fixture.manifest.version_id,
            &fixture.manifest.chunks[0],
            &fixture.chunks[0],
            None,
        )
        .await
        .unwrap();
    a.restore_after_login().await.unwrap();
    b.restore_after_login().await.unwrap();
    until(async || {
        fixture.announcements.lock().await.len() == 1 && fixture.sockets.lock().await.len() == 2
    })
    .await;
    let source = b.peers.sources(&fixture.manifest).await.remove(0);
    fixture.deny_validation.store(true, Ordering::SeqCst);
    assert!(source
        .fetch(
            &b.auth.owner().await.unwrap(),
            &fixture.manifest,
            &fixture.manifest.chunks[0]
        )
        .await
        .is_err());
    assert!(!ea.0.lock().unwrap().iter().any(|(n, _)| n == "peer.sent"));
    assert!(
        a.peers
            .audit
            .pending(&a.auth.owner().await.unwrap())
            .unwrap()
            .is_empty(),
        "denied grant must not open bytes or record any sent payload"
    );
    fixture.deny_validation.store(false, Ordering::SeqCst);
    let mut policy = a.sharing_policy().await;
    policy.bandwidth_limit_bps = 1024;
    policy.max_concurrent = 1;
    a.set_sharing_policy(policy).await.unwrap();
    until(async || fixture.sockets.lock().await.len() == 2).await;
    let source = b.peers.sources(&fixture.manifest).await.remove(0);
    let owner = b.auth.owner().await.unwrap();
    let manifest = fixture.manifest.clone();
    let download =
        tokio::spawn(async move { source.fetch(&owner, &manifest, &manifest.chunks[0]).await });
    until(async || a.peers.active_uploads() > 0).await;
    let second = b.peers.sources(&fixture.manifest).await.remove(0);
    assert!(tokio::time::timeout(
        Duration::from_secs(2),
        second.fetch(
            &b.auth.owner().await.unwrap(),
            &fixture.manifest,
            &fixture.manifest.chunks[0]
        )
    )
    .await
    .unwrap()
    .is_err());
    assert_eq!(
        a.peers.active_uploads(),
        1,
        "upload concurrency policy is enforced on real requests"
    );
    download.abort();
    let _ = download.await;
    until(async || a.peers.active_uploads() == 0).await;
    a.peers.stop().await;
    b.peers.stop().await;
    let id = b
        .start_download(
            fixture.manifest.version_id.clone(),
            db.path().join("fallback.bin").to_string_lossy().into(),
        )
        .await
        .unwrap();
    until(async || {
        b.list()
            .await
            .unwrap()
            .iter()
            .any(|t| t.id == id && t.state == TaskState::Completed)
    })
    .await;
    assert_eq!(
        tokio::fs::read(db.path().join("fallback.bin"))
            .await
            .unwrap(),
        fixture.chunks.concat()
    );
    a.shutdown().await.unwrap();
    b.shutdown().await.unwrap();
    server.abort();
}

async fn adversary(
    fixture: Arc<Fixture>,
    origin: String,
    mode: u8,
) -> (
    tokio::task::JoinHandle<()>,
    Arc<Mutex<Vec<Arc<webrtc::peer_connection::RTCPeerConnection>>>>,
) {
    use tokio_tungstenite::tungstenite::client::IntoClientRequest;
    use webrtc::{
        api::{setting_engine::SettingEngine, APIBuilder},
        peer_connection::{
            configuration::RTCConfiguration, sdp::session_description::RTCSessionDescription,
        },
    };
    let id = "00000000-0000-0000-0000-000000000004";
    fixture
        .announcements
        .lock()
        .await
        .insert(id.into(), vec![0, 1]);
    let connections = Arc::new(Mutex::new(Vec::new()));
    let keep = connections.clone();
    let handle = tokio::spawn(async move {
        let mut request = format!("{}/api/v1/p2p/ws", origin.replacen("http:", "ws:", 1))
            .into_client_request()
            .unwrap();
        request
            .headers_mut()
            .insert("Authorization", format!("Bearer {id}").parse().unwrap());
        let (socket, _) = tokio_tungstenite::connect_async(request).await.unwrap();
        let (mut tx, mut rx) = socket.split();
        while let Some(Ok(tokio_tungstenite::tungstenite::Message::Text(text))) = rx.next().await {
            let v: Value = serde_json::from_str(&text).unwrap();
            if v["type"] != "peer.offer" {
                continue;
            }
            let mut settings = SettingEngine::default();
            settings.set_include_loopback_candidate(true);
            let pc = Arc::new(
                APIBuilder::new()
                    .with_setting_engine(settings)
                    .build()
                    .new_peer_connection(RTCConfiguration::default())
                    .await
                    .unwrap(),
            );
            let weak = Arc::downgrade(&pc);
            pc.on_data_channel(Box::new(move |dc| {
                let weak = weak.clone();
                Box::pin(async move {
                    let channel = Arc::downgrade(&dc);
                    dc.on_message(Box::new(move |message| {
                        let channel = channel.clone();
                        let weak = weak.clone();
                        Box::pin(async move {
                            use linknestx::peer::protocol::{decode, encode, Frame, PAYLOAD};
                            let Ok((
                                Frame::Request {
                                    task_id: _,
                                    request_id,
                                    version_id,
                                    index,
                                    hash,
                                    grant,
                                },
                                _,
                            )) = decode(&message.data)
                            else {
                                return;
                            };
                            if mode == 1 {
                                if let Some(pc) = weak.upgrade() {
                                    tokio::spawn(async move {
                                        let _ = pc.close().await;
                                    });
                                }
                                return;
                            }
                            let Some(dc) = channel.upgrade() else { return };
                            if mode == 2 {
                                // Exceeds the application frame bound while remaining inside
                                // the library's bounded 64 KiB receive buffer.
                                dc.send(&bytes::Bytes::from(vec![0; 20 * 1024]))
                                    .await
                                    .unwrap();
                                return;
                            }
                            if mode == 3 {
                                // Exceeds the library's fixed receive buffer as well.
                                let _ = dc.send(&bytes::Bytes::from(vec![0; 65536])).await;
                                return;
                            }
                            // A real hostile DataChannel sends bytes with the expected framing and wrong digest.
                            let corrupt = vec![255u8; grant.size as usize];
                            for (part, payload) in corrupt.chunks(PAYLOAD).enumerate() {
                                let offset = part * PAYLOAD;
                                let frame = Frame::Data {
                                    request_id: request_id.clone(),
                                    version_id: version_id.clone(),
                                    index,
                                    hash: hash.clone(),
                                    offset: offset as u64,
                                    end: offset + payload.len() == corrupt.len(),
                                };
                                let _ = dc
                                    .send(&bytes::Bytes::from(encode(&frame, payload).unwrap()))
                                    .await;
                            }
                        })
                    }));
                })
            }));
            pc.set_remote_description(
                RTCSessionDescription::offer(v["payload"]["sdp"].as_str().unwrap().into()).unwrap(),
            )
            .await
            .unwrap();
            let mut gathered = pc.gathering_complete_promise().await;
            let answer = pc.create_answer(None).await.unwrap();
            pc.set_local_description(answer).await.unwrap();
            gathered.recv().await;
            let sdp = pc.local_description().await.unwrap().sdp;
            tx.send(tokio_tungstenite::tungstenite::Message::Text(json!({"type":"peer.answer","toDeviceId":v["fromDeviceId"],"connectionId":v["connectionId"],"versionId":v["versionId"],"payload":{"sdp":sdp}}).to_string().into())).await.unwrap();
            keep.lock().await.push(pc);
        }
    });
    (handle, connections)
}

#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn real_corrupt_disconnected_and_oversized_datachannels_requeue_to_verified_http() {
    for mode in [0, 1, 2, 3] {
        let (fixture, origin, server) = fixture().await;
        let (adversary, connections) = adversary(fixture.clone(), origin.clone(), mode).await;
        let (root, node, events) = node(&origin, 1).await;
        node.restore_after_login().await.unwrap();
        until(async || fixture.sockets.lock().await.len() == 2).await;
        let id = node
            .start_download(
                fixture.manifest.version_id.clone(),
                root.path().join("safe.bin").to_string_lossy().into(),
            )
            .await
            .unwrap();
        until(async || {
            node.list()
                .await
                .unwrap()
                .iter()
                .any(|t| t.id == id && t.state == TaskState::Completed)
        })
        .await;
        assert_eq!(
            tokio::fs::read(root.path().join("safe.bin")).await.unwrap(),
            fixture.chunks.concat()
        );
        assert!(
            !connections.lock().await.is_empty(),
            "must actually negotiate an adversarial WebRTC channel"
        );
        assert!(fixture.served.lock().await.contains(&0));
        assert!(!events
            .0
            .lock()
            .unwrap()
            .iter()
            .any(|(n, v)| n == "transfer.updated"
                && v["activeSources"]
                    .as_array()
                    .unwrap()
                    .iter()
                    .any(|s| s.as_str().is_some_and(|s| s.starts_with("peer:")))));
        node.shutdown().await.unwrap();
        adversary.abort();
        for c in connections.lock().await.iter() {
            c.close().await.unwrap();
        }
        server.abort();
    }
}

#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn three_empty_nodes_start_in_distinct_server_regions_then_exchange_verified_chunks() {
    for count in [3, 12] {
        let (fixture, origin, server) = fixture_with_chunks(count).await;
        // Reproduce staggered discovery without a timing-dependent sleep: only
        // chunk 2 is visible until a real DataChannel request validates its grant.
        fixture
            .only_third_chunk_until_peer_request
            .store(true, Ordering::SeqCst);
        let (da, a, ea) = node(&origin, 1).await;
        let (db, b, eb) = node(&origin, 4).await;
        let (dc, c, ec) = node(&origin, 7).await;
        for service in [&a, &b, &c] {
            service.restore_after_login().await.unwrap();
        }
        until(async || fixture.sockets.lock().await.len() == 3).await;
        for (dir, service) in [(&da, &a), (&db, &b), (&dc, &c)] {
            assert!(service
                .cache
                .catalog(&service.auth.owner().await.unwrap())
                .await
                .unwrap()
                .is_empty());
            service
                .start_download(
                    fixture.manifest.version_id.clone(),
                    dir.path().join("cold.bin").to_string_lossy().into(),
                )
                .await
                .unwrap();
        }
        until(async || {
            fixture
                .served_by
                .lock()
                .await
                .values()
                .filter(|v| v.len() >= (count / 3).min(3))
                .count()
                == 3
        })
        .await;
        let allocations = fixture.allocations.lock().await.clone();
        assert_eq!(allocations.len(), 3);
        let mut first_indexes = std::collections::HashSet::new();
        for n in [1, 4, 7] {
            let device = format!("00000000-0000-0000-0000-00000000000{n}");
            let (start, end) = allocations[&device];
            let batch = (end - start).min(3);
            let mut first = fixture.served_by.lock().await[&device][..batch].to_vec();
            first.sort_unstable();
            assert_eq!(
                first,
                (start..start + batch).collect::<Vec<_>>(),
                "first batch must stay in assigned region"
            );
            for index in first {
                assert!(
                    first_indexes.insert(index),
                    "same-modulo devices must not overlap initial requests"
                );
            }
        }
        for (dir, service, events) in [(&da, &a, &ea), (&db, &b, &eb), (&dc, &c, &ec)] {
            until(async || {
                service
                    .list()
                    .await
                    .unwrap()
                    .iter()
                    .any(|t| t.state == TaskState::Completed)
            })
            .await;
            assert_eq!(
                tokio::fs::read(dir.path().join("cold.bin")).await.unwrap(),
                fixture.chunks.concat()
            );
            assert!(
                events
                    .0
                    .lock()
                    .unwrap()
                    .iter()
                    .any(|(n, v)| n == "transfer.updated"
                        && v["activeSources"]
                            .as_array()
                            .unwrap()
                            .iter()
                            .any(|s| s.as_str().is_some_and(|s| s.starts_with("peer:")))),
                "each native downloader must reuse another node's verified partial data; chunks={count}, destination={}", dir.path().display()
            );
        }
        for service in [&a, &b, &c] {
            service.shutdown().await.unwrap();
        }
        assert!(!fixture
            .only_third_chunk_until_peer_request
            .load(Ordering::SeqCst));
        server.abort();
    }
}

#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn persisted_cache_revalidates_reannounces_after_refresh_and_retracts_eviction() {
    let (fixture, origin, server) = fixture().await;
    let (root, original, _events) = node(&origin, 1).await;
    let owner = original.auth.owner().await.unwrap();
    for index in [0, 1] {
        original
            .cache
            .store(
                &owner,
                &fixture.manifest.version_id,
                &fixture.manifest.chunks[index],
                &fixture.chunks[index],
                None,
            )
            .await
            .unwrap();
    }
    original.shutdown().await.unwrap();
    drop(original);
    let bad_key = hash(
        format!(
            "{owner}\0{}\0{}",
            fixture.manifest.version_id, fixture.manifest.chunks[1].hash
        )
        .as_bytes(),
    );
    tokio::fs::write(root.path().join("cache").join(bad_key), vec![255; 32768])
        .await
        .unwrap();
    let auth = Arc::new(AuthManager::new(root.path()).unwrap());
    auth.login(&origin, "user", "pass", None).await.unwrap();
    let restored =
        TransferService::open(root.path(), auth.clone(), Arc::new(Events::default())).unwrap();
    restored.restore_after_login().await.unwrap();
    let id = "00000000-0000-0000-0000-000000000001";
    until(async || fixture.announcements.lock().await.get(id) == Some(&vec![0])).await;
    let (_other_root, other, _other_events) = node(&origin, 2).await;
    other.restore_after_login().await.unwrap();
    until(async || fixture.sockets.lock().await.len() == 2).await;
    let source = other.peers.sources(&fixture.manifest).await.remove(0);
    assert_eq!(
        source
            .fetch(
                &other.auth.owner().await.unwrap(),
                &fixture.manifest,
                &fixture.manifest.chunks[0]
            )
            .await
            .unwrap(),
        fixture.chunks[0]
    );
    auth.refresh_session(&auth.current().await.unwrap())
        .await
        .unwrap();
    let rotated = format!("Bearer {id}:rotated");
    until(async || {
        fixture.socket_tokens.lock().await.contains(&rotated)
            && fixture
                .announcements_by_token
                .lock()
                .await
                .contains(&rotated)
    })
    .await;
    assert_eq!(
        source
            .fetch(
                &other.auth.owner().await.unwrap(),
                &fixture.manifest,
                &fixture.manifest.chunks[0]
            )
            .await
            .unwrap(),
        fixture.chunks[0]
    );
    restored.cache.set_quota(0).await.unwrap();
    until(async || fixture.announcements.lock().await.get(id) == Some(&vec![])).await;
    assert!(source
        .fetch(
            &other.auth.owner().await.unwrap(),
            &fixture.manifest,
            &fixture.manifest.chunks[0]
        )
        .await
        .is_err());
    restored.peers.stop().await;
    auth.logout().await.unwrap();
    until(async || !fixture.sockets.lock().await.contains_key(id)).await;
    assert!(!fixture.announcements.lock().await.contains_key(id));
    assert_eq!(restored.peers.active_uploads(), 0);
    restored.shutdown().await.unwrap();
    other.shutdown().await.unwrap();
    server.abort();
}

#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn failed_final_hash_never_publishes_file_or_completion_audit() {
    let (fixture, origin, server) = fixture().await;
    fixture.bad_final_hash.store(true, Ordering::SeqCst);
    let (root, node, _events) = node(&origin, 1).await;
    let id = node
        .start_download(
            fixture.manifest.version_id.clone(),
            root.path().join("never.bin").to_string_lossy().into(),
        )
        .await
        .unwrap();
    until(async || {
        node.list()
            .await
            .unwrap()
            .iter()
            .any(|t| t.id == id && t.state == TaskState::Failed)
    })
    .await;
    assert!(!root.path().join("never.bin").exists());
    assert!(node
        .peers
        .audit
        .pending(&node.auth.owner().await.unwrap())
        .unwrap()
        .iter()
        .all(|r| r.action != "transfer.completed"));
    node.shutdown().await.unwrap();
    server.abort();
}

#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn completion_audit_storage_failure_preserves_saved_success_and_recovers_on_login() {
    let (fixture, origin, server) = fixture().await;
    let (root, node, events) = node(&origin, 1).await;
    let audit_db = rusqlite::Connection::open(root.path().join("peer-audit.sqlite3")).unwrap();
    audit_db.execute_batch("CREATE TRIGGER fail_completion BEFORE INSERT ON pending WHEN NEW.action='transfer.completed' BEGIN SELECT RAISE(FAIL,'audit storage unavailable'); END;").unwrap();
    let id = node
        .start_download(
            fixture.manifest.version_id.clone(),
            root.path().join("saved.bin").to_string_lossy().into(),
        )
        .await
        .unwrap();
    until(async || {
        node.list()
            .await
            .unwrap()
            .iter()
            .any(|t| t.id == id && matches!(t.state, TaskState::Completed | TaskState::Failed))
    })
    .await;
    assert_eq!(
        tokio::fs::read(root.path().join("saved.bin"))
            .await
            .unwrap(),
        fixture.chunks.concat()
    );
    assert_eq!(
        node.list()
            .await
            .unwrap()
            .into_iter()
            .find(|t| t.id == id)
            .unwrap()
            .state,
        TaskState::Completed,
        "audit failure after file publication cannot turn successful delivery into failure"
    );
    assert!(events
        .0
        .lock()
        .unwrap()
        .iter()
        .any(|(name, value)| name == "service.error"
            && value["error"].as_str().unwrap().contains("审计")));
    audit_db
        .execute_batch("DROP TRIGGER fail_completion;")
        .unwrap();
    node.restore_after_login().await.unwrap();
    let owner = node.auth.owner().await.unwrap();
    assert_eq!(
        node.peers
            .audit
            .pending(&owner)
            .unwrap()
            .iter()
            .filter(|r| r.task_id == id && r.action == "transfer.completed")
            .map(|r| r.bytes)
            .sum::<u64>(),
        fixture.manifest.size
    );
    node.shutdown().await.unwrap();
    server.abort();
}

#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn parallel_download_from_partial_disk_cache_does_not_deadlock_publication() {
    let (fixture, origin, server) = fixture_with_chunk_size(6, 4 * 1024 * 1024).await;
    let (root, node, _events) = node(&origin, 3).await;
    for index in [0, 1, 2] {
        node.cache
            .store(
                &node.auth.owner().await.unwrap(),
                &fixture.manifest.version_id,
                &fixture.manifest.chunks[index],
                &fixture.chunks[index],
                None,
            )
            .await
            .unwrap();
    }
    let id = node
        .start_download(
            fixture.manifest.version_id.clone(),
            root.path().join("cached.bin").to_string_lossy().into(),
        )
        .await
        .unwrap();
    until(async || {
        node.list()
            .await
            .unwrap()
            .iter()
            .any(|t| t.id == id && t.state == TaskState::Completed)
    })
    .await;
    assert_eq!(
        tokio::fs::read(root.path().join("cached.bin"))
            .await
            .unwrap(),
        fixture.chunks.concat()
    );
    node.shutdown().await.unwrap();
    server.abort();
}

#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn expired_active_pair_reconnects_and_same_device_serves_later_chunks() {
    let (fixture, origin, server) = fixture_with_chunks(12).await;
    let (_root_a, a, _events_a) = node(&origin, 1).await;
    let (root_b, b, events_b) = node(&origin, 2).await;
    for chunk in &fixture.manifest.chunks {
        a.cache
            .store(
                &a.auth.owner().await.unwrap(),
                &fixture.manifest.version_id,
                chunk,
                &fixture.chunks[chunk.index],
                None,
            )
            .await
            .unwrap();
    }
    a.restore_after_login().await.unwrap();
    b.restore_after_login().await.unwrap();
    let mut policy = a.sharing_policy().await;
    policy.bandwidth_limit_bps = 128 * 1024;
    a.set_sharing_policy(policy).await.unwrap();
    until(async || {
        fixture.announcements.lock().await.len() == 1 && fixture.sockets.lock().await.len() == 2
    })
    .await;
    let task = b
        .start_download(
            fixture.manifest.version_id.clone(),
            root_b.path().join("renewed.bin").to_string_lossy().into(),
        )
        .await
        .unwrap();
    until(async || a.peers.active_uploads() > 0 && !fixture.connections.lock().await.is_empty())
        .await;
    let connection = fixture
        .connections
        .lock()
        .await
        .values()
        .next()
        .unwrap()
        .clone();
    for (target, remote) in [
        (
            connection["fromDeviceId"].as_str().unwrap(),
            connection["toDeviceId"].as_str().unwrap(),
        ),
        (
            connection["toDeviceId"].as_str().unwrap(),
            connection["fromDeviceId"].as_str().unwrap(),
        ),
    ] {
        fixture.sockets.lock().await[target].send(json!({"type":"peer.close","code":"expired","connectionId":connection["connectionId"],"versionId":fixture.manifest.version_id,"toDeviceId":target,"fromDeviceId":remote,"fromUserId":"00000000-0000-0000-0000-000000000099","payload":{}})).unwrap();
    }
    until(async || {
        b.list()
            .await
            .unwrap()
            .iter()
            .any(|entry| entry.id == task && entry.state == TaskState::Completed)
    })
    .await;
    assert_eq!(
        tokio::fs::read(root_b.path().join("renewed.bin"))
            .await
            .unwrap(),
        fixture.chunks.concat()
    );
    assert!(
        fixture.connections.lock().await.len() >= 2,
        "expired pair must negotiate a fresh connection UUID"
    );
    let received = b
        .peers
        .audit
        .pending(&b.auth.owner().await.unwrap())
        .unwrap()
        .into_iter()
        .find(|r| r.task_id == task && r.action == "peer.received")
        .unwrap();
    assert!(
        received.bytes >= 3 * 32768,
        "same healthy peer must serve later chunks after expiry"
    );
    assert!(events_b
        .0
        .lock()
        .unwrap()
        .iter()
        .any(|(name, value)| name == "transfer.updated"
            && value["activeSources"]
                .as_array()
                .unwrap()
                .iter()
                .any(|source| source
                    .as_str()
                    .is_some_and(|source| source.starts_with("peer:")))));
    a.shutdown().await.unwrap();
    b.shutdown().await.unwrap();
    server.abort();
}

#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn cancelled_rate_limited_peer_request_does_not_delay_next_request_at_new_rate() {
    let (fixture, origin, server) = fixture().await;
    let (_root_a, a, events_a) = node(&origin, 1).await;
    let (_root_b, b, _events_b) = node(&origin, 2).await;
    a.cache
        .store(
            &a.auth.owner().await.unwrap(),
            &fixture.manifest.version_id,
            &fixture.manifest.chunks[0],
            &fixture.chunks[0],
            None,
        )
        .await
        .unwrap();
    let mut policy = a.sharing_policy().await;
    policy.bandwidth_limit_bps = 1024;
    a.set_sharing_policy(policy).await.unwrap();
    b.restore_after_login().await.unwrap();
    until(async || {
        fixture.announcements.lock().await.len() == 1 && fixture.sockets.lock().await.len() == 2
    })
    .await;
    let source = b.peers.sources(&fixture.manifest).await.remove(0);
    let owner = b.auth.owner().await.unwrap();
    let manifest = fixture.manifest.clone();
    let request =
        tokio::spawn(async move { source.fetch(&owner, &manifest, &manifest.chunks[0]).await });
    until(async || a.peers.active_uploads() > 0).await;
    request.abort();
    let _ = request.await;
    until(async || a.peers.active_uploads() == 0).await;
    assert!(!events_a
        .0
        .lock()
        .unwrap()
        .iter()
        .any(|(event, _)| event == "peer.sent"));
    let mut policy = a.sharing_policy().await;
    policy.bandwidth_limit_bps = 1024 * 1024;
    a.set_sharing_policy(policy).await.unwrap();
    until(async || fixture.sockets.lock().await.len() == 2).await;
    let source = b.peers.sources(&fixture.manifest).await.remove(0);
    let bytes = tokio::time::timeout(
        Duration::from_secs(3),
        source.fetch(
            &b.auth.owner().await.unwrap(),
            &fixture.manifest,
            &fixture.manifest.chunks[0],
        ),
    )
    .await
    .unwrap()
    .unwrap();
    assert_eq!(bytes, fixture.chunks[0]);
    a.shutdown().await.unwrap();
    b.shutdown().await.unwrap();
    server.abort();
}

#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn unavailable_initial_coordination_keeps_verified_http_download_available() {
    let (fixture, origin, server) = fixture().await;
    fixture.allocation_unavailable.store(true, Ordering::SeqCst);
    let (root, node, _events) = node(&origin, 1).await;
    node.restore_after_login().await.unwrap();
    let task = node
        .start_download(
            fixture.manifest.version_id.clone(),
            root.path()
                .join("uncoordinated.bin")
                .to_string_lossy()
                .into(),
        )
        .await
        .unwrap();
    until(async || {
        node.list()
            .await
            .unwrap()
            .iter()
            .any(|entry| entry.id == task && entry.state == TaskState::Completed)
    })
    .await;
    assert_eq!(
        tokio::fs::read(root.path().join("uncoordinated.bin"))
            .await
            .unwrap(),
        fixture.chunks.concat()
    );
    assert!(fixture.allocations.lock().await.is_empty());
    assert_eq!(fixture.served.lock().await.len(), fixture.chunks.len());
    node.shutdown().await.unwrap();
    server.abort();
}

// Compiled offline; actual listeners intentionally await the final authorized runtime gate.
#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn final_completed_cache_revokes_after_rejected_opt_out_and_saved_copy_survives() {
    let (fixture, origin, server) = fixture_with_chunks(1).await;
    let (root, node, _) = node(&origin, 1).await;
    let mut policy = node.sharing_policy().await;
    policy.enabled = false;
    assert!(node.set_sharing_policy(policy).await.is_err());
    let destination = root.path().join("saved.bin");
    let id = node
        .start_download(
            fixture.manifest.version_id.clone(),
            destination.to_string_lossy().into(),
        )
        .await
        .unwrap();
    until(async || {
        node.list()
            .await
            .unwrap()
            .iter()
            .any(|t| t.id == id && t.state == TaskState::Completed)
    })
    .await;
    let owner = node.auth.owner().await.unwrap();
    assert!(!node.cache.versions(&owner).unwrap().is_empty());
    fixture.lifecycle_status.store(503, Ordering::SeqCst);
    node.reconcile_lifecycle().await.unwrap();
    assert!(!node.cache.versions(&owner).unwrap().is_empty());
    fixture.lifecycle_status.store(403, Ordering::SeqCst);
    node.reconcile_lifecycle().await.unwrap();
    assert!(node.cache.versions(&owner).unwrap().is_empty());
    assert_eq!(
        tokio::fs::read(destination).await.unwrap(),
        fixture.chunks.concat()
    );
    node.shutdown().await.unwrap();
    server.abort();
}
#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn final_real_http_task_identity_and_cache_only_empty_completion() {
    for count in [0, 1] {
        let (fixture, origin, server) = fixture_with_chunks(count).await;
        let (root, node, _) = node(&origin, 1).await;
        let mut ids = Vec::new();
        for i in 0..3 {
            if i == 1 {
                node.cache
                    .revoke(
                        &node.auth.owner().await.unwrap(),
                        &fixture.manifest.version_id,
                    )
                    .await
                    .unwrap();
            }
            let id = node
                .start_download(
                    fixture.manifest.version_id.clone(),
                    root.path()
                        .join(format!("saved-{i}.bin"))
                        .to_string_lossy()
                        .into(),
                )
                .await
                .unwrap();
            until(async || {
                node.list()
                    .await
                    .unwrap()
                    .iter()
                    .any(|t| t.id == id && t.state == TaskState::Completed)
            })
            .await;
            ids.push(id);
        }
        let rows = node
            .peers
            .audit
            .pending(&node.auth.owner().await.unwrap())
            .unwrap();
        for id in &ids {
            assert_eq!(
                rows.iter()
                    .filter(|r| r.task_id == *id
                        && r.action == "download.completed"
                        && r.node_id == "local"
                        && r.bytes == fixture.manifest.size)
                    .count(),
                1
            )
        }
        let headers = fixture.task_headers.lock().await.clone();
        if count == 1 {
            assert!(headers.contains(&ids[0]) && headers.contains(&ids[1]));
            assert!(!headers.contains(&ids[2]));
        } else {
            assert!(headers.is_empty());
        }
        assert!(!rows
            .iter()
            .any(|r| r.task_id == ids[2] && r.action == "transfer.completed"));
        node.shutdown().await.unwrap();
        server.abort();
    }
}
