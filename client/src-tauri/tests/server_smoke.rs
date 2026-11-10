//! Actual Go server + production native authentication, transfer and WebRTC stack.
//! Run with scripts/smoke-test.sh; no endpoint, grant or ChunkSource is replaced.
use linknestx::{
    audit_export::{
        AuditExportManager, AuditFormat, AuditSavePicker, ExportOutcome, ExportRequest,
    },
    auth::session::{limited_body, AuthManager, User},
    events::EventSink,
    transfer::{
        models::{
            hash, Chunk, Manifest, TaskSnapshot, TaskState, DEFAULT_CHUNK_SIZE, MAX_FILE_SIZE,
        },
        scheduler::{ChunkSource, ServerSource},
        service::TransferService,
    },
};
use serde_json::{json, Value};
use sha2::{Digest, Sha256};
use std::{
    collections::{BTreeMap, BTreeSet},
    fs::File,
    io::{Read, Write},
    net::TcpListener,
    path::{Path, PathBuf},
    process::{Child, Command, Stdio},
    sync::{Arc, Mutex},
    time::{Duration, Instant},
};
use tempfile::TempDir;
use uuid::Uuid;

const CHUNKS: usize = 12;
const FILE_BYTES: u64 = CHUNKS as u64 * DEFAULT_CHUNK_SIZE;

struct OwnedAuditPicker(PathBuf);
#[async_trait::async_trait]
impl AuditSavePicker for OwnedAuditPicker {
    async fn pick(&self, _: AuditFormat) -> linknestx::Result<Option<PathBuf>> {
        Ok(Some(self.0.clone()))
    }
}

struct Server {
    child: Child,
    _root: TempDir,
    origin: String,
    password: String,
    pid_marker: Option<PathBuf>,
}
impl Drop for Server {
    fn drop(&mut self) {
        // Kill/wait only this test's direct child, including panic/error paths.
        let _ = self.child.kill();
        let _ = self.child.wait();
        if let Some(marker) = &self.pid_marker {
            let _ = std::fs::remove_file(marker);
        }
    }
}
impl Server {
    async fn start() -> Self {
        let binary = std::env::var_os("LINKNESTX_SMOKE_SERVER")
            .expect("run scripts/smoke-test.sh to build and supply the actual Go server");
        assert!(
            Path::new(&binary).is_file(),
            "smoke server binary is missing"
        );
        let root = tempfile::tempdir().unwrap();
        let reserved = TcpListener::bind("127.0.0.1:0").unwrap();
        let address = reserved.local_addr().unwrap();
        let password = Uuid::new_v4().to_string();
        let log = File::create(root.path().join("server.log")).unwrap();
        let mut command = Command::new(binary);
        for (name, _) in std::env::vars_os() {
            if name.to_string_lossy().starts_with("LINKNESTX_") {
                command.env_remove(name);
            }
        }
        command
            .env("LINKNESTX_BIND_ADDRESS", address.to_string())
            .env("LINKNESTX_SQLITE_PATH", root.path().join("server.sqlite3"))
            .env("LINKNESTX_STORAGE_ROOT", root.path().join("chunks"))
            .env("LINKNESTX_JWT_SIGNING_KEY", Uuid::new_v4().to_string())
            .env("LINKNESTX_JWT_REFRESH_KEY", Uuid::new_v4().to_string())
            .env("LINKNESTX_BOOTSTRAP_ADMIN_USERNAME", "smoke-admin")
            .env("LINKNESTX_BOOTSTRAP_ADMIN_PASSWORD", &password)
            .stdin(Stdio::null())
            .stdout(Stdio::from(log.try_clone().unwrap()))
            .stderr(Stdio::from(log));
        drop(reserved);
        let mut server = Self {
            child: command.spawn().expect("start owned Go server child"),
            _root: root,
            origin: format!("http://{address}"),
            password,
            pid_marker: std::env::var_os("LINKNESTX_SMOKE_PID_MARKER").map(PathBuf::from),
        };
        if let Some(marker) = &server.pid_marker {
            std::fs::write(
                marker,
                format!("{} {}\n", std::process::id(), server.child.id()),
            )
            .expect("record owned smoke processes for interrupt cleanup");
        }
        let client = reqwest::Client::builder()
            .redirect(reqwest::redirect::Policy::none())
            .timeout(Duration::from_secs(1))
            .build()
            .unwrap();
        let started = Instant::now();
        loop {
            assert!(
                server.child.try_wait().unwrap().is_none(),
                "owned Go server exited during startup"
            );
            if let Ok(response) = client
                .get(format!("{}/healthz", server.origin))
                .send()
                .await
            {
                if response.status().is_success() {
                    break;
                }
            }
            assert!(
                started.elapsed() < Duration::from_secs(15),
                "Go server startup timed out"
            );
            tokio::time::sleep(Duration::from_millis(50)).await;
        }
        server
    }
}

#[derive(Default)]
struct Events(Mutex<Vec<(String, Value)>>);
impl EventSink for Events {
    fn emit(&self, name: &str, value: Value) {
        if name == "service.error" {
            eprintln!("smoke native service error: {value}");
        }
        self.0.lock().unwrap().push((name.into(), value));
    }
}
impl Events {
    fn sources(&self, task: &str) -> BTreeSet<String> {
        self.0
            .lock()
            .unwrap()
            .iter()
            .filter(|(name, event)| name == "transfer.updated" && event["id"] == task)
            .flat_map(|(_, event)| {
                event["activeSources"]
                    .as_array()
                    .cloned()
                    .unwrap_or_default()
            })
            .filter_map(|value| value.as_str().map(str::to_owned))
            .collect()
    }
}
struct Node {
    root: PathBuf,
    auth: Arc<AuthManager>,
    service: Arc<TransferService>,
    events: Arc<Events>,
    user: String,
    device: String,
}
impl Node {
    async fn initialized(root: PathBuf, origin: &str, name: &str, password: &str) -> Self {
        let auth = Arc::new(AuthManager::new(&root).unwrap());
        let session = auth.login(origin, name, password, None).await.unwrap();
        assert!(
            session.user.must_change_password,
            "new accounts must be gated"
        );
        let denied = auth.json("GET", "/api/v1/groups", None).await.unwrap_err();
        assert_eq!(denied.status, Some(403));
        let old_token = session.access_token;
        let value = auth
            .json(
                "POST",
                "/api/v1/auth/password",
                Some(json!({
                    "currentPassword":password,"newPassword":Uuid::new_v4().to_string()
                })),
            )
            .await
            .unwrap();
        let user: User = serde_json::from_value(value).unwrap();
        assert!(!user.must_change_password);
        auth.update_user(&auth.owner().await.unwrap(), user)
            .await
            .unwrap();
        assert!(
            auth.current().await.unwrap().session.access_token == old_token,
            "password change must preserve current native session"
        );
        let events = Arc::new(Events::default());
        let service = TransferService::open(&root, auth.clone(), events.clone()).unwrap();
        Self {
            root,
            auth,
            service,
            events,
            user: session.user.id,
            device: session.device_id,
        }
    }
    async fn limit(&self, bytes_per_second: u64) {
        let mut policy = self.service.sharing_policy().await;
        policy.bandwidth_limit_bps = bytes_per_second;
        self.service.set_sharing_policy(policy).await.unwrap();
    }
}

async fn snapshot(service: &TransferService, id: &str) -> TaskSnapshot {
    service
        .list()
        .await
        .unwrap()
        .into_iter()
        .find(|task| task.id == id)
        .unwrap()
}
async fn completed(service: &TransferService, id: &str, stage: &str) -> TaskSnapshot {
    tokio::time::timeout(Duration::from_secs(180), async {
        let started = Instant::now();
        let mut previous = String::new();
        let mut last_log = Instant::now();
        loop {
            let task = snapshot(service, id).await;
            let diagnostic = json!({"stage":stage,"state":task.state,"bytes":task.bytes_completed,
                "total":task.total_bytes,"sources":task.active_sources,"error":task.error})
            .to_string();
            if previous != diagnostic || last_log.elapsed() >= Duration::from_secs(10) {
                eprintln!(
                    "smoke progress after {}s: {diagnostic}",
                    started.elapsed().as_secs()
                );
                previous = diagnostic;
                last_log = Instant::now();
            }
            assert_ne!(
                task.state,
                TaskState::Failed,
                "native task failed: {:?}",
                task.error
            );
            if task.state == TaskState::Completed {
                return task;
            }
            tokio::time::sleep(Duration::from_millis(50)).await;
        }
    })
    .await
    .unwrap_or_else(|_| panic!("native {stage} task timed out"))
}
async fn until(mut predicate: impl AsyncFnMut() -> bool) {
    tokio::time::timeout(Duration::from_secs(45), async {
        while !predicate().await {
            tokio::time::sleep(Duration::from_millis(30)).await;
        }
    })
    .await
    .expect("real server/native condition timed out");
}
fn create_source(path: &Path) -> String {
    let mut file = File::create(path).unwrap();
    let mut digest = Sha256::new();
    // Only one 4 MiB buffer; each chunk has distinct deterministic contents.
    for index in 0..CHUNKS {
        let data: Vec<u8> = (0..DEFAULT_CHUNK_SIZE as usize)
            .map(|offset| ((offset * 31 + index * 17 + (offset >> 12)) % 251) as u8)
            .collect();
        file.write_all(&data).unwrap();
        digest.update(&data);
    }
    file.sync_all().unwrap();
    format!("{:x}", digest.finalize())
}
fn file_digest(path: &Path) -> String {
    let mut input = File::open(path).unwrap();
    let mut buffer = vec![0; 1024 * 1024];
    let mut digest = Sha256::new();
    loop {
        let n = input.read(&mut buffer).unwrap();
        if n == 0 {
            break;
        }
        digest.update(&buffer[..n]);
    }
    format!("{:x}", digest.finalize())
}
async fn fetch_manifest(auth: &AuthManager, version: &str) -> Manifest {
    let manifest: Manifest = serde_json::from_value(
        auth.json(
            "GET",
            &format!("/api/v1/file-versions/{version}/manifest"),
            None,
        )
        .await
        .unwrap(),
    )
    .unwrap();
    manifest.validate().unwrap();
    manifest
}
async fn sources(auth: &AuthManager, version: &str) -> Vec<Value> {
    auth.json(
        "GET",
        &format!("/api/v1/p2p/sources?versionId={version}"),
        None,
    )
    .await
    .unwrap()["sources"]
        .as_array()
        .unwrap()
        .clone()
}
async fn seed_from_server(node: &Node, manifest: &Manifest, remainder: usize) {
    let owner = node.auth.owner().await.unwrap();
    let source = ServerSource(node.auth.clone());
    for chunk in manifest
        .chunks
        .iter()
        .filter(|chunk| chunk.index % 3 == remainder)
    {
        let data = source.fetch(&owner, manifest, chunk).await.unwrap();
        assert_eq!(data.len() as u64, chunk.size);
        assert_eq!(hash(&data), chunk.hash);
        assert!(node
            .service
            .cache
            .store(
                &owner,
                &manifest.version_id,
                chunk,
                &data,
                manifest.expires_at.as_deref()
            )
            .await
            .unwrap());
    }
    node.service.restore_after_login().await.unwrap();
}

#[test]
fn twenty_gigabyte_manifest_geometry_and_boundary() {
    let mut manifest = Manifest {
        file_id: Uuid::new_v4().to_string(),
        version_id: Uuid::new_v4().to_string(),
        name: "geometry.bin".into(),
        size: MAX_FILE_SIZE,
        sha256: hash(b"geometry-only"),
        chunk_size: DEFAULT_CHUNK_SIZE,
        expires_at: None,
        visibility: "pool".into(),
        sharing_policy: Default::default(),
        chunks: (0..4769)
            .map(|index| Chunk {
                index,
                hash: hash(b"geometry-only"),
                size: if index == 4768 { 1_558_528 } else { 4_194_304 },
            })
            .collect(),
    };
    assert_eq!(
        manifest.chunks.iter().map(|chunk| chunk.size).sum::<u64>(),
        20_000_000_000
    );
    manifest.validate().unwrap();
    manifest.size += 1;
    manifest.chunks.last_mut().unwrap().size += 1;
    assert!(manifest.validate().is_err(), "20GB+1 must be rejected");
    manifest.size -= 1;
    manifest.chunks.last_mut().unwrap().size -= 2;
    assert!(
        manifest.validate().is_err(),
        "incorrect final chunk geometry must be rejected"
    );
}

#[tokio::test(flavor = "multi_thread", worker_threads = 8)]
#[ignore = "requires actual Go binary and local TCP/UDP; run scripts/smoke-test.sh (required make smoke/check target)"]
async fn actual_go_native_smoke() {
    tokio::time::timeout(Duration::from_secs(420), run_smoke())
        .await
        .expect("cross-language smoke timed out");
}

fn audit_csv_field(event: &Value, key: &str) -> String {
    if key == "bytes" {
        event[key].as_u64().unwrap().to_string()
    } else if key == "createdAt" {
        // Go JSON trims trailing fractional zeros; CSV uses audit.timestampLayout
        // with exactly nine fractional digits. Preserve the same UTC instant.
        chrono::DateTime::parse_from_rfc3339(event[key].as_str().unwrap())
            .expect("audit JSON timestamp must be RFC3339")
            .with_timezone(&chrono::Utc)
            .to_rfc3339_opts(chrono::SecondsFormat::Nanos, true)
    } else {
        event[key].as_str().unwrap().to_string()
    }
}

#[test]
fn audit_csv_expected_timestamps_preserve_nanoseconds_in_fixed_utc_format() {
    for (json_timestamp, csv_timestamp) in [
        ("2026-11-10T01:02:03Z", "2026-11-10T01:02:03.000000000Z"),
        ("2026-11-10T01:02:03.12Z", "2026-11-10T01:02:03.120000000Z"),
        (
            "2026-11-10T01:02:03.123456Z",
            "2026-11-10T01:02:03.123456000Z",
        ),
        (
            "2026-11-10T01:02:03.123456789Z",
            "2026-11-10T01:02:03.123456789Z",
        ),
        (
            "2026-11-10T09:02:03.000000001+08:00",
            "2026-11-10T01:02:03.000000001Z",
        ),
    ] {
        let event = json!({
            "createdAt":json_timestamp,"bytes":123,"id":"record-id","action":"group.created",
            "actorId":"actor","targetId":"target","deviceId":"device","fileId":"file",
            "versionId":"version","taskId":"task","nodeId":"node","source":"server-observed",
        });
        assert_eq!(audit_csv_field(&event, "createdAt"), csv_timestamp);
        assert_eq!(
            chrono::DateTime::parse_from_rfc3339(csv_timestamp).unwrap(),
            chrono::DateTime::parse_from_rfc3339(json_timestamp).unwrap(),
            "normalizing text must preserve the precise instant",
        );
        for key in [
            "id",
            "action",
            "actorId",
            "targetId",
            "deviceId",
            "fileId",
            "versionId",
            "taskId",
            "nodeId",
            "source",
        ] {
            assert_eq!(audit_csv_field(&event, key), event[key].as_str().unwrap());
        }
        assert_eq!(audit_csv_field(&event, "bytes"), "123");
    }
}

#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
#[ignore = "requires actual Go binary and local TCP; run scripts/smoke-test.sh (required make smoke/check target)"]
async fn actual_go_native_audit_export() {
    tokio::time::timeout(Duration::from_secs(120), async {
        let server = Server::start().await;
        let output = tempfile::tempdir().unwrap();
        let admin = Node::initialized(
            output.path().join("admin"),
            &server.origin,
            "smoke-admin",
            &server.password,
        )
        .await;
        // More than the server's 200-record export batch proves full-stream export.
        for index in 0..205 {
            admin.auth.json("POST", "/api/v1/groups", Some(json!({"name":format!("export {index}")})))
                .await.unwrap();
        }
        let mut expected = Vec::new();
        for offset in [0, 200] {
            let page = admin.auth.json("GET", &format!("/api/v1/admin/audit?action=group.created&limit=200&offset={offset}"), None)
                .await.unwrap();
            assert_eq!(page["total"], 205);
            expected.extend(page["events"].as_array().unwrap().iter().cloned());
        }
        assert_eq!(expected.len(), 205);
        let manager = Arc::new(AuditExportManager::new().unwrap());
        let mut output_bytes = BTreeMap::new();
        for (format, extension) in [(AuditFormat::Csv, "csv"), (AuditFormat::Json, "json")] {
            let destination = output.path().join(format!("audit.{extension}"));
            let job = manager.begin(admin.auth.clone(), ExportRequest::new(format, "group.created".into()).unwrap())
                .await.unwrap();
            let outcome = job.save(&OwnedAuditPicker(destination.clone())).await.unwrap();
            let ExportOutcome::Saved { path, bytes } = outcome else { panic!("native export cancelled unexpectedly") };
            assert_eq!(Path::new(&path), destination);
            let contents = std::fs::read(&destination).unwrap();
            assert_eq!(contents.len() as u64, bytes);
            output_bytes.insert(extension, bytes);
            if extension == "json" {
                let actual: Value = serde_json::from_slice(&contents).unwrap();
                assert_eq!(actual["total"], 205);
                assert_eq!(actual["events"], json!(expected));
            } else {
                let columns = ["id","action","actorId","targetId","deviceId","fileId","versionId","taskId","nodeId","bytes","source","createdAt"];
                let mut expected_csv = format!("{}\n", columns.join(","));
                for event in &expected {
                    let row: Vec<String> = columns.iter().map(|key| {
                        let value = audit_csv_field(event, key);
                        // Controlled UUID/action/time records need no quoting; compare exact bytes.
                        assert!(!value.contains([',', '"', '\r', '\n']));
                        value
                    }).collect();
                    expected_csv.push_str(&row.join(","));
                    expected_csv.push('\n');
                }
                let actual_csv = std::str::from_utf8(&contents).expect("export CSV must be UTF-8");
                let actual_rows: Vec<_> = actual_csv.lines().collect();
                let expected_rows: Vec<_> = expected_csv.lines().collect();
                assert_eq!(actual_rows.len(), expected_rows.len(), "CSV row count");
                for (row, (actual, expected)) in actual_rows.iter().zip(&expected_rows).enumerate() {
                    let actual_fields: Vec<_> = actual.split(',').collect();
                    let expected_fields: Vec<_> = expected.split(',').collect();
                    assert_eq!(actual_fields.len(), columns.len(), "CSV row {row} field count");
                    for (column, (actual, expected)) in actual_fields.iter().zip(&expected_fields).enumerate() {
                        assert_eq!(actual, expected, "CSV row {row}, field {}", columns[column]);
                    }
                }
                assert!(contents == expected_csv.as_bytes(), "CSV framing differs after field comparison");
            }
        }
        assert!(std::fs::read_dir(output.path()).unwrap().all(|entry| !entry.unwrap().file_name().to_string_lossy().ends_with(".part")));
        manager.shutdown().await;
        admin.service.shutdown().await.unwrap();
        println!("{}", json!({"kind":"go-native-audit-export","records":expected.len(),"action":"group.created","savedBytes":output_bytes,"nativeStreamingExport":true}));
    }).await.expect("actual Go/native audit export timed out");
}

async fn run_smoke() {
    eprintln!("smoke stage: bootstrap and forced password changes");
    let server = Server::start().await;
    let clients = tempfile::tempdir().unwrap();
    let admin = Node::initialized(
        clients.path().join("admin"),
        &server.origin,
        "smoke-admin",
        &server.password,
    )
    .await;
    let password = Uuid::new_v4().to_string();
    for name in ["seed-a", "seed-b", "download-c"] {
        admin
            .auth
            .json(
                "POST",
                "/api/v1/users",
                Some(json!({"username":name,"password":password,"roles":["user"]})),
            )
            .await
            .unwrap();
    }
    let a = Node::initialized(
        clients.path().join("a"),
        &server.origin,
        "seed-a",
        &password,
    )
    .await;
    let b = Node::initialized(
        clients.path().join("b"),
        &server.origin,
        "seed-b",
        &password,
    )
    .await;
    let c = Node::initialized(
        clients.path().join("c"),
        &server.origin,
        "download-c",
        &password,
    )
    .await;
    eprintln!("smoke stage: group membership and chat");
    let group = admin
        .auth
        .json(
            "POST",
            "/api/v1/groups",
            Some(json!({"name":"native smoke"})),
        )
        .await
        .unwrap();
    let group_id = group["id"].as_str().unwrap();
    // A is deliberately outside this group; promotion must grant pool access.
    for id in [&b.user, &c.user] {
        admin
            .auth
            .json(
                "POST",
                &format!("/api/v1/groups/{group_id}/members"),
                Some(json!({"userId":id,"role":"member"})),
            )
            .await
            .unwrap();
    }
    let chat_path = format!("/api/v1/groups/{group_id}/messages");
    b.auth
        .json(
            "POST",
            &chat_path,
            Some(
                json!({"clientMessageId":Uuid::new_v4().to_string(),"body":"native/Go acceptance"}),
            ),
        )
        .await
        .unwrap();
    let history = c.auth.json("GET", &chat_path, None).await.unwrap();
    assert_eq!(history["messages"][0]["body"], "native/Go acceptance");
    assert_eq!(
        a.auth
            .json("GET", &chat_path, None)
            .await
            .unwrap_err()
            .status,
        Some(403)
    );

    eprintln!("smoke stage: native 48 MiB upload");
    let source = clients.path().join("source-48m.bin");
    let expected_hash = create_source(&source);
    let upload = admin
        .service
        .start_upload(group_id.into(), source.to_string_lossy().into(), None)
        .await
        .unwrap();
    let uploaded = completed(&admin.service, &upload, "upload").await;
    let version = uploaded.version_id;
    let initial_manifest = fetch_manifest(&admin.auth, &version).await;
    assert_eq!(initial_manifest.size, FILE_BYTES);
    assert_eq!(initial_manifest.chunks.len(), CHUNKS);
    assert_eq!(initial_manifest.sha256, expected_hash);
    assert!(initial_manifest.expires_at.is_some());
    assert_eq!(
        a.auth
            .json(
                "GET",
                &format!("/api/v1/file-versions/{version}/manifest"),
                None
            )
            .await
            .unwrap_err()
            .status,
        Some(403)
    );
    admin
        .auth
        .json(
            "POST",
            &format!("/api/v1/files/{}/promote", initial_manifest.file_id),
            Some(json!({})),
        )
        .await
        .unwrap();
    let manifest = fetch_manifest(&a.auth, &version).await;
    assert_eq!(manifest.visibility, "pool");
    assert!(manifest.expires_at.is_none());
    // The uploader leaves the swarm by stopping its process service. Shipping
    // clients no longer have a local sharing opt-out; its auth remains usable
    // for the administrative assertions below.
    admin.service.shutdown().await.unwrap();

    eprintln!("smoke stage: seed disjoint authorized server chunks and announce");
    seed_from_server(&a, &manifest, 0).await;
    seed_from_server(&b, &manifest, 1).await;
    c.limit(2 * 1024 * 1024).await;
    until(async || {
        let available = sources(&c.auth, &version).await;
        [&a.device, &b.device]
            .iter()
            .all(|id| available.iter().any(|s| s["deviceId"] == **id))
            && available.iter().all(|s| s["deviceId"] != admin.device)
    })
    .await;
    let initial = sources(&c.auth, &version).await;
    for (node, remainder) in [(&a, 0), (&b, 1)] {
        let peer = initial
            .iter()
            .find(|s| s["deviceId"] == node.device)
            .unwrap();
        let indexes: Vec<usize> = peer["indexes"]
            .as_array()
            .unwrap()
            .iter()
            .map(|v| v.as_u64().unwrap() as usize)
            .collect();
        assert_eq!(
            indexes,
            (0..CHUNKS)
                .filter(|i| i % 3 == remainder)
                .collect::<Vec<_>>()
        );
    }
    eprintln!("smoke stage: C download, early partial announcement and re-serving");
    let destination = clients.path().join("saved-c.bin");
    let task = c
        .service
        .start_download(version.clone(), destination.to_string_lossy().into())
        .await
        .unwrap();
    let mut announced_index = None;
    until(async || {
        assert!(
            !destination.exists(),
            "final destination published before partial announcement evidence"
        );
        let available = sources(&a.auth, &version).await;
        if let Some(peer) = available.iter().find(|s| s["deviceId"] == c.device) {
            announced_index = peer["indexes"]
                .as_array()
                .unwrap()
                .first()
                .and_then(Value::as_u64)
                .map(|n| n as usize);
        }
        announced_index.is_some()
    })
    .await;
    let announced_index = announced_index.unwrap();
    let partial_snapshot = snapshot(&c.service, &task).await;
    assert!(partial_snapshot.bytes_completed > 0 && partial_snapshot.bytes_completed < FILE_BYTES);
    assert_ne!(partial_snapshot.state, TaskState::Completed);
    // Fetch through the actual peer transport to prove newly verified C data is reusable.
    let peer_sources = a.service.peers.sources(&manifest).await;
    let from_c = peer_sources
        .iter()
        .find(|source| source.name() == format!("peer:{}", c.device))
        .unwrap();
    let shared = from_c
        .fetch(
            &a.auth.owner().await.unwrap(),
            &manifest,
            &manifest.chunks[announced_index],
        )
        .await
        .unwrap();
    assert_eq!(hash(&shared), manifest.chunks[announced_index].hash);
    assert!(
        !destination.exists(),
        "partial chunk must finish re-serving before final destination publication"
    );
    assert_ne!(
        snapshot(&c.service, &task).await.state,
        TaskState::Completed,
        "partial chunk must be reusable before whole-file completion"
    );
    let a_destination = clients.path().join("saved-outsider-a.bin");
    let a_task = a
        .service
        .start_download(version.clone(), a_destination.to_string_lossy().into())
        .await
        .unwrap();
    let (done_c, done_a) = tokio::join!(
        completed(&c.service, &task, "C download"),
        completed(&a.service, &a_task, "A concurrent outsider download")
    );
    assert_eq!(done_c.bytes_completed, FILE_BYTES);
    assert_eq!(done_a.bytes_completed, FILE_BYTES);
    assert_eq!(file_digest(&destination), expected_hash);
    assert_eq!(file_digest(&a_destination), expected_hash);
    let source_names = c.events.sources(&task);
    for required in [
        format!("peer:{}", a.device),
        format!("peer:{}", b.device),
        "server".into(),
    ] {
        assert!(
            source_names.contains(&required),
            "48MiB native download must use two real peers and server; got {source_names:?}"
        );
    }
    for node in [&a, &b, &c] {
        node.service.peers.audit.flush(&node.auth).await.unwrap();
    }
    let peer_records = admin
        .auth
        .json(
            "GET",
            &format!(
                "/api/v1/admin/audit?action=peer.received&actorId={}&versionId={version}",
                c.user
            ),
            None,
        )
        .await
        .unwrap();
    let mut peer_bytes = BTreeMap::new();
    for event in peer_records["events"]
        .as_array()
        .unwrap()
        .iter()
        .filter(|e| e["taskId"] == task)
    {
        assert_eq!(event["source"], "client-reported");
        peer_bytes.insert(
            event["nodeId"].as_str().unwrap().to_string(),
            event["bytes"].as_u64().unwrap(),
        );
    }
    assert!(peer_bytes.get(&a.device).is_some_and(|n| *n > 0));
    assert!(peer_bytes.get(&b.device).is_some_and(|n| *n > 0));
    let server_records = admin
        .auth
        .json(
            "GET",
            &format!(
                "/api/v1/admin/audit?action=server.sent&actorId={}&versionId={version}",
                c.user
            ),
            None,
        )
        .await
        .unwrap();
    let server_bytes: u64 = server_records["events"]
        .as_array()
        .unwrap()
        .iter()
        .map(|event| {
            assert_eq!(event["source"], "server-observed");
            event["bytes"].as_u64().unwrap()
        })
        .sum();
    assert!(server_bytes > 0);
    assert_eq!(peer_bytes.values().sum::<u64>() + server_bytes, FILE_BYTES);

    eprintln!("smoke stage: recreate native service and resume interrupted download");
    // Resume from the durable partial file after a real native service recreation.
    c.service
        .cache
        .revoke(&c.auth.owner().await.unwrap(), &version)
        .await
        .unwrap();
    assert!(c.service.sharing_policy().await.enabled);
    let resumed_destination = clients.path().join("saved-resumed.bin");
    let resume_task = c
        .service
        .start_download(
            version.clone(),
            resumed_destination.to_string_lossy().into(),
        )
        .await
        .unwrap();
    until(async || {
        let s = snapshot(&c.service, &resume_task).await;
        s.bytes_completed >= DEFAULT_CHUNK_SIZE && s.bytes_completed < FILE_BYTES
    })
    .await;
    c.service.pause(&resume_task).await.unwrap();
    let paused = snapshot(&c.service, &resume_task).await;
    assert_eq!(paused.state, TaskState::Paused);
    assert!(paused.bytes_completed < FILE_BYTES);
    assert!(!resumed_destination.exists());
    c.service.shutdown().await.unwrap();
    let restored =
        TransferService::open(&c.root, c.auth.clone(), Arc::new(Events::default())).unwrap();
    let persisted = snapshot(&restored, &resume_task).await;
    assert_eq!(persisted.bytes_completed, paused.bytes_completed);
    restored.resume(&resume_task).await.unwrap();
    completed(&restored, &resume_task, "recreated service resume").await;
    assert_eq!(file_digest(&resumed_destination), expected_hash);

    eprintln!("smoke stage: device revoke, saved file preservation and audit export");
    let devices = admin
        .auth
        .json("GET", "/api/v1/admin/devices", None)
        .await
        .unwrap();
    assert!(devices
        .as_array()
        .unwrap()
        .iter()
        .any(|d| d["id"] == c.device && d["active"] == true));
    let owner = c.auth.owner().await.unwrap();
    admin
        .auth
        .json(
            "POST",
            &format!("/api/v1/admin/devices/{}/revoke", c.device),
            Some(json!({})),
        )
        .await
        .unwrap();
    let grant_error = c
        .auth
        .json(
            "POST",
            "/api/v1/p2p/grants",
            Some(json!({"versionId":version,"chunkHash":manifest.chunks[0].hash})),
        )
        .await
        .unwrap_err();
    assert!(matches!(grant_error.status, Some(401 | 403)));
    assert!(ServerSource(c.auth.clone())
        .fetch(&owner, &manifest, &manifest.chunks[0])
        .await
        .is_err());
    restored.cache.revoke(&owner, &version).await.unwrap();
    restored.cache.set_quota(0).await.unwrap();
    restored.cache.prune().await.unwrap();
    assert_eq!(file_digest(&destination), expected_hash);
    assert_eq!(file_digest(&resumed_destination), expected_hash);
    assert_eq!(file_digest(&a_destination), expected_hash);
    let export = admin
        .auth
        .request_for(
            &admin.auth.owner().await.unwrap(),
            "GET",
            "/api/v1/admin/audit/export?format=csv",
            None,
            false,
        )
        .await
        .unwrap();
    assert!(export.headers()["content-disposition"]
        .to_str()
        .unwrap()
        .contains("attachment"));
    let csv = limited_body(export, 2 * 1024 * 1024).await.unwrap();
    assert!(std::str::from_utf8(&csv)
        .unwrap()
        .starts_with("id,action,actorId,targetId,deviceId,"));
    assert!(String::from_utf8(csv).unwrap().contains("client-reported"));
    assert_eq!(fetch_manifest(&a.auth, &version).await.visibility, "pool");
    println!(
        "{}",
        json!({"kind":"go-native-smoke","fileBytes":FILE_BYTES,"chunks":CHUNKS,"sha256":expected_hash,
        "peerReceivedBytes":peer_bytes,"serverObservedBytes":server_bytes,"partialAnnouncedIndex":announced_index,
        "partialBytesAtAnnouncement":partial_snapshot.bytes_completed,"resumedVerifiedBytes":paused.bytes_completed,
        "twoSavedDownloadsVerified":true,"savedFilesSurvivedCachePrune":true,"deviceRevokeDeniedNewGrant":true,
        "maxFileGeometryBytes":MAX_FILE_SIZE,"maxFileGeometryChunks":4769,"full20GBTransfer":false})
    );
    restored.shutdown().await.unwrap();
    for node in [&admin, &a, &b] {
        node.service.shutdown().await.unwrap();
    }
}

#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
#[ignore = "requires actual Go binary and local TCP/UDP; run scripts/smoke-test.sh"]
async fn actual_go_native_document_attachments() {
    use linknestx::transfer::models::UploadTarget;
    let server = Server::start().await;
    let clients = tempfile::tempdir().unwrap();
    let admin = Node::initialized(
        clients.path().join("admin"),
        &server.origin,
        "smoke-admin",
        &server.password,
    )
    .await;
    let password = Uuid::new_v4().to_string();
    for name in ["writer", "reader"] {
        admin
            .auth
            .json(
                "POST",
                "/api/v1/users",
                Some(json!({"username":name,"password":password,"roles":["user"]})),
            )
            .await
            .unwrap();
    }
    let writer = Node::initialized(
        clients.path().join("writer"),
        &server.origin,
        "writer",
        &password,
    )
    .await;
    let reader = Node::initialized(
        clients.path().join("reader"),
        &server.origin,
        "reader",
        &password,
    )
    .await;
    let doc = writer.auth.json("POST", "/api/v1/documents", Some(json!({"title":"Native attachment guide","body":"# Draft","visibility":"team","status":"draft"}))).await.unwrap();
    let id = doc["id"].as_str().unwrap();
    let path = format!("/api/v1/documents/{id}");
    assert_eq!(
        reader
            .auth
            .json("GET", &path, None)
            .await
            .unwrap_err()
            .status,
        Some(404)
    );
    let bytes: Vec<u8> = (0..1_048_777)
        .map(|i| ((i * 31 + i / 97) % 251) as u8)
        .collect();
    let source = clients.path().join("guide.zip");
    std::fs::write(&source, &bytes).unwrap();
    let upload = writer
        .service
        .start_upload_to(
            UploadTarget::Document(id.into()),
            source.to_string_lossy().into(),
            None,
            Some("document-attachment".into()),
            false,
            None,
        )
        .await
        .unwrap();
    let task = completed(&writer.service, &upload, "document-upload").await;
    assert_eq!(task.document_id.as_deref(), Some(id));
    assert!(task.group_id.is_none());
    let manifest = fetch_manifest(&writer.auth, &task.version_id).await;
    assert_eq!(manifest.visibility, "document");
    assert!(manifest.expires_at.is_none());
    assert_eq!(manifest.sha256, hash(&bytes));
    let private_path = format!("/api/v1/file-versions/{}/manifest", task.version_id);
    assert_eq!(
        reader
            .auth
            .json("GET", &private_path, None)
            .await
            .unwrap_err()
            .status,
        Some(403)
    );
    let current = writer.auth.json("GET", &path, None).await.unwrap();
    let published = writer.auth.json("PATCH", &path, Some(json!({"revision":current["revision"],"title":"Native attachment guide","body":format!("[Download](linknestx-file:{})",task.version_id),"visibility":"team","status":"published"}))).await.unwrap();
    let visible = reader.auth.json("GET", &path, None).await.unwrap();
    assert_eq!(visible["attachments"][0]["versionId"], task.version_id);
    let destination = clients.path().join("reader-download.zip");
    let download = reader
        .service
        .start_download(
            task.version_id.clone(),
            destination.to_string_lossy().into(),
        )
        .await
        .unwrap();
    completed(&reader.service, &download, "document-download").await;
    assert_eq!(std::fs::read(&destination).unwrap(), bytes);
    let curated = admin
        .auth
        .json(
            "POST",
            &format!("{path}/curation"),
            Some(json!({"revision":published["revision"],"curated":true})),
        )
        .await
        .unwrap();
    assert_eq!(
        writer
            .auth
            .json(
                "DELETE",
                &format!("{path}?revision={}", curated["revision"]),
                None
            )
            .await
            .unwrap_err()
            .status,
        Some(403)
    );
    assert_eq!(
        writer
            .auth
            .json(
                "DELETE",
                &format!("/api/v1/files/{}", manifest.file_id),
                None
            )
            .await
            .unwrap_err()
            .status,
        Some(403)
    );
    admin
        .auth
        .json(
            "DELETE",
            &format!("{path}?revision={}", curated["revision"]),
            None,
        )
        .await
        .unwrap();
    assert!(reader.auth.json("GET", &private_path, None).await.is_err());
    reader.service.reconcile_lifecycle().await.unwrap();
    assert_eq!(std::fs::read(&destination).unwrap(), bytes);
    for node in [&writer, &reader, &admin] {
        node.service.shutdown().await.unwrap();
    }
}
