use super::manifest::*;
use base64::{engine::general_purpose::URL_SAFE_NO_PAD as B64, Engine};
use ring::signature::{Ed25519KeyPair, KeyPair};
use sha2::{Digest, Sha256};
fn hash(b: &[u8]) -> String {
    format!("{:x}", Sha256::digest(b))
}
fn key() -> Ed25519KeyPair {
    Ed25519KeyPair::from_seed_unchecked(&[37; 32]).unwrap()
}
fn signed(raw: &[u8]) -> Vec<u8> {
    let k = key();
    let mut data = b"LinkNestX-update-manifest-v1\0".to_vec();
    data.extend(raw);
    serde_json::to_vec(&serde_json::json!({"keyId": &hash(k.public_key().as_ref())[..16],"manifest":B64.encode(raw),"signature":B64.encode(k.sign(&data).as_ref())})).unwrap()
}
fn sample() -> serde_json::Value {
    serde_json::json!({"schemaVersion":1,"appId":"com.linknestx.client","channel":"stable","platform":"macos","arch":"aarch64","version":"0.2.1","buildSequence":2,"issuedAt":"2026-11-10T00:00:00Z","expiresAt":"2027-01-01T00:00:00Z","entryPoint":"Contents/MacOS/linknestx","totalBytes":3,"files":[{"path":"Contents/MacOS/linknestx","mode":493,"size":3,"sha256":hash(b"new"),"blocks":[{"sha256":hash(b"new"),"size":3}]}]})
}
const NOW: i64 = 1794268800;

#[cfg(target_os = "macos")]
#[tokio::test]
#[ignore = "requires locally built, signed release assets and loopback sockets; never contacts the deployment server"]
async fn reconstructs_actual_packaged_macos_release_with_pinned_public_key() {
    use axum::{routing::get, Router};
    use std::{path::PathBuf, sync::Arc};
    let release =
        PathBuf::from(std::env::var_os("LINKNESTX_TEST_RELEASE_ROOT").expect("local release root"));
    let base = PathBuf::from(std::env::var_os("LINKNESTX_TEST_BASE_APP").expect("local prior app"));
    let envelope = std::fs::read(release.join("macos/aarch64/latest.json")).unwrap();
    let m = verify(
        &envelope,
        &pinned_key().unwrap(),
        chrono::Utc::now().timestamp(),
    )
    .unwrap();
    let objects = Arc::new(release.join("objects"));
    let app = Router::new().route(
        "/updates/objects/{id}",
        get(
            move |axum::extract::Path(id): axum::extract::Path<String>,
                  headers: axum::http::HeaderMap| {
                let objects = objects.clone();
                async move {
                    assert!(
                        !headers.contains_key("authorization") && !headers.contains_key("cookie")
                    );
                    if id.len() != 64 || !id.bytes().all(|c| c.is_ascii_hexdigit()) {
                        return (axum::http::StatusCode::NOT_FOUND, vec![]);
                    }
                    match tokio::fs::read(objects.join(id)).await {
                        Ok(bytes) => (axum::http::StatusCode::OK, bytes),
                        Err(_) => (axum::http::StatusCode::NOT_FOUND, vec![]),
                    }
                }
            },
        ),
    );
    let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
    let origin = format!("http://{}", listener.local_addr().unwrap());
    let server = tokio::spawn(async move { axum::serve(listener, app).await.unwrap() });
    let root = tempfile::tempdir_in(std::env::temp_dir().canonicalize().unwrap()).unwrap();
    let stage = root.path().join("LinkNestX.app");
    let progress = super::download::prepare(
        &m,
        &base,
        &stage,
        &root.path().join("cache"),
        &origin,
        |_| {},
    )
    .await
    .unwrap();
    super::files::verify_tree(&stage, &m).unwrap();
    assert!(progress.reused_bytes > 0 && progress.downloaded_bytes > 0);
    assert!(progress.downloaded_bytes < m.total_bytes);
    assert!(std::process::Command::new("/usr/bin/codesign")
        .args(["--verify", "--deep", "--strict"])
        .arg(&stage)
        .status()
        .unwrap()
        .success());
    println!(
        "{}",
        serde_json::json!({"kind":"actual-packaged-release","version":m.version,"files":m.files.len(),"targetBytes":m.total_bytes,"reusedBytes":progress.reused_bytes,"downloadedBytes":progress.downloaded_bytes,"pinnedSignatureVerified":true,"completeTreeVerified":true,"macosResourceSealVerified":true,"installedOrLaunched":false})
    );
    server.abort();
}

#[test]
fn exact_domain_signed_manifest_is_accepted() {
    assert!(verify(
        &signed(&serde_json::to_vec(&sample()).unwrap()),
        key().public_key().as_ref(),
        NOW
    )
    .is_ok());
}
#[test]
fn rejects_corruption_unknown_duplicate_fields_and_path_collisions() {
    let mut valid = signed(&serde_json::to_vec(&sample()).unwrap());
    let mut value: serde_json::Value = serde_json::from_slice(&valid).unwrap();
    value["signature"] = serde_json::json!(B64.encode([0; 64]));
    valid = serde_json::to_vec(&value).unwrap();
    assert!(verify(&valid, key().public_key().as_ref(), NOW).is_err());
    for bad in [
        "../bad",
        "Contents/../bad",
        "C:/bad",
        "Contents\\bad",
        "Contents/NUL",
        "Contents/name.",
        "Contents/name ",
    ] {
        let mut m = sample();
        m["files"][0]["path"] = bad.into();
        assert!(verify(
            &signed(&serde_json::to_vec(&m).unwrap()),
            key().public_key().as_ref(),
            NOW
        )
        .is_err());
    }
    let raw = serde_json::to_string(&sample()).unwrap();
    let duplicate = raw.replacen("{", "{\"schemaVersion\":1,", 1);
    assert!(verify(
        &signed(duplicate.as_bytes()),
        key().public_key().as_ref(),
        NOW
    )
    .is_err());
    let mut m = sample();
    m["execute"] = "bad".into();
    assert!(verify(
        &signed(&serde_json::to_vec(&m).unwrap()),
        key().public_key().as_ref(),
        NOW
    )
    .is_err());
    let mut m = sample();
    let mut other = m["files"][0].clone();
    other["path"] = "contents/macos/LINKNESTX".into();
    m["files"].as_array_mut().unwrap().push(other);
    m["totalBytes"] = 6.into();
    assert!(verify(
        &signed(&serde_json::to_vec(&m).unwrap()),
        key().public_key().as_ref(),
        NOW
    )
    .is_err());
}

#[test]
fn rejects_expired_wrong_platform_rollback_and_malformed_block_ranges() {
    let valid = verify(
        &signed(&serde_json::to_vec(&sample()).unwrap()),
        key().public_key().as_ref(),
        NOW,
    )
    .unwrap();
    assert!(eligible(&valid, "windows", "x86_64", "0.2.0", 1).is_err());
    assert!(eligible(&valid, "macos", "aarch64", "0.2.1", 1).is_err());
    assert!(eligible(&valid, "macos", "aarch64", "0.2.0", 2).is_err());
    assert!(eligible(&valid, "macos", "aarch64", "0.2.0", 1).is_ok());
    for (field, value) in [
        ("expiresAt", serde_json::json!("2026-11-01T00:00:00Z")),
        ("issuedAt", serde_json::json!("2026-12-01T00:00:00Z")),
        ("appId", serde_json::json!("evil")),
        ("totalBytes", serde_json::json!(4)),
    ] {
        let mut m = sample();
        m[field] = value;
        assert!(verify(
            &signed(&serde_json::to_vec(&m).unwrap()),
            key().public_key().as_ref(),
            NOW
        )
        .is_err());
    }
    let mut m = sample();
    m["files"][0]["blocks"][0]["baseOffset"] = serde_json::json!(2147483648u64);
    assert!(verify(
        &signed(&serde_json::to_vec(&m).unwrap()),
        key().public_key().as_ref(),
        NOW
    )
    .is_err());
}

#[tokio::test]
async fn real_http_reuses_verified_block_and_fetches_only_missing_then_resumes() {
    use axum::{routing::get, Router};
    use std::sync::{Arc, Mutex};
    let old = vec![b'a'; 262144];
    let mut target = old.clone();
    target.extend(b"changed");
    let requested = Arc::new(Mutex::new(Vec::new()));
    let observed = requested.clone();
    let missing = hash(b"changed");
    let missing_route = missing.clone();
    let app = Router::new().route(
        "/updates/objects/{hash}",
        get(
            move |axum::extract::Path(id): axum::extract::Path<String>| {
                let observed = observed.clone();
                let missing = missing_route.clone();
                async move {
                    observed.lock().unwrap().push(id.clone());
                    if id == missing {
                        (axum::http::StatusCode::OK, b"changed".to_vec())
                    } else {
                        (axum::http::StatusCode::NOT_FOUND, Vec::new())
                    }
                }
            },
        ),
    );
    let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
    let origin = format!("http://{}", listener.local_addr().unwrap());
    let server = tokio::spawn(async move { axum::serve(listener, app).await.unwrap() });
    let root = tempfile::tempdir_in(std::env::temp_dir().canonicalize().unwrap()).unwrap();
    let base = root.path().join("base");
    std::fs::create_dir(&base).unwrap();
    std::fs::create_dir_all(base.join("Contents/MacOS")).unwrap();
    std::fs::write(base.join("Contents/MacOS/linknestx"), &old).unwrap();
    let mut value = sample();
    value["totalBytes"] = target.len().into();
    value["files"][0]["size"] = target.len().into();
    value["files"][0]["sha256"] = hash(&target).into();
    value["files"][0]["blocks"] = serde_json::json!([{"sha256":hash(&old),"size":262144,"basePath":"Contents/MacOS/linknestx","baseOffset":0},{"sha256":missing,"size":7}]);
    let m = verify(
        &signed(&serde_json::to_vec(&value).unwrap()),
        key().public_key().as_ref(),
        NOW,
    )
    .unwrap();
    let result = super::download::prepare(
        &m,
        &base,
        &root.path().join("stage"),
        &root.path().join("cache"),
        &origin,
        |_| {},
    )
    .await
    .unwrap();
    assert_eq!(result.reused_bytes, 262144);
    assert_eq!(result.downloaded_bytes, 7);
    assert_eq!(
        std::fs::read(root.path().join("stage/Contents/MacOS/linknestx")).unwrap(),
        target
    );
    assert_eq!(*requested.lock().unwrap(), vec![missing]);
    let resumed = super::download::prepare(
        &m,
        &base,
        &root.path().join("stage2"),
        &root.path().join("cache"),
        &origin,
        |_| {},
    )
    .await
    .unwrap();
    assert_eq!(resumed.downloaded_bytes, 0);
    assert_eq!(requested.lock().unwrap().len(), 1);
    server.abort();
}

#[test]
fn interrupted_swap_and_unhealthy_new_app_restore_old_tree() {
    for activate in [false, true] {
        let root = tempfile::tempdir_in(std::env::temp_dir().canonicalize().unwrap()).unwrap();
        let target = root.path().join("LinkNestX.app");
        let staged = root.path().join("prepared");
        std::fs::create_dir(&target).unwrap();
        std::fs::create_dir(&staged).unwrap();
        std::fs::write(target.join("entry"), b"original").unwrap();
        std::fs::write(staged.join("entry"), b"new").unwrap();
        let tx = super::install::Transaction::new(
            target.clone(),
            staged,
            root.path().join("journal"),
            "12345678-1234-4234-8234-123456789abc",
        )
        .unwrap();
        tx.backup().unwrap();
        if activate {
            tx.activate().unwrap();
        }
        tx.rollback().unwrap();
        assert_eq!(std::fs::read(target.join("entry")).unwrap(), b"original");
        tx.rollback().unwrap();
        assert_eq!(std::fs::read(target.join("entry")).unwrap(), b"original");
    }
}

#[tokio::test]
async fn corrupt_delta_and_invalid_range_fall_back_to_verified_full_only() {
    use axum::{routing::get, Router};
    use std::sync::{Arc, Mutex};
    for mode in ["bad-chunk", "range", "redirect", "bad-full"] {
        let mut target = vec![b'x'; 262144];
        target.extend(b"tail");
        let fullhash = hash(&target);
        let fullcopy = target.clone();
        let seen = Arc::new(Mutex::new(Vec::new()));
        let observed = seen.clone();
        let matchhash = fullhash.clone();
        let app = Router::new().route(
            "/updates/objects/{hash}",
            get(
                move |axum::extract::Path(id): axum::extract::Path<String>,
                      headers: axum::http::HeaderMap| {
                    let observed = observed.clone();
                    let full = fullcopy.clone();
                    let hash = matchhash.clone();
                    async move {
                        assert!(
                            !headers.contains_key("authorization")
                                && !headers.contains_key("cookie")
                                && !headers.contains_key("range")
                        );
                        observed.lock().unwrap().push(id.clone());
                        if id == hash {
                            return (
                                axum::http::StatusCode::OK,
                                [("x-fixture", "full")],
                                if mode == "bad-full" {
                                    vec![0; full.len()]
                                } else {
                                    full
                                },
                            );
                        }
                        if mode == "range" {
                            (
                                axum::http::StatusCode::PARTIAL_CONTENT,
                                [("content-range", "bytes 0-3/262144")],
                                b"xxxx".to_vec(),
                            )
                        } else if mode == "redirect" {
                            (
                                axum::http::StatusCode::FOUND,
                                [("location", "http://127.0.0.1:1/must-not-follow")],
                                Vec::new(),
                            )
                        } else {
                            (
                                axum::http::StatusCode::OK,
                                [("x-fixture", "corrupt")],
                                vec![b'z'; 262144],
                            )
                        }
                    }
                },
            ),
        );
        let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
        let origin = format!("http://{}", listener.local_addr().unwrap());
        let server = tokio::spawn(async move { axum::serve(listener, app).await.unwrap() });
        let root = tempfile::tempdir_in(std::env::temp_dir().canonicalize().unwrap()).unwrap();
        let base = root.path().join("base");
        std::fs::create_dir_all(base.join("Contents/MacOS")).unwrap();
        std::fs::write(base.join("Contents/MacOS/linknestx"), vec![b'z'; 262144]).unwrap();
        let mut value = sample();
        value["totalBytes"] = target.len().into();
        value["files"][0]["size"] = target.len().into();
        value["files"][0]["sha256"] = fullhash.clone().into();
        value["files"][0]["blocks"] = serde_json::json!([{"sha256":hash(&target[..262144]),"size":262144,"basePath":"Contents/MacOS/linknestx","baseOffset":0},{"sha256":hash(b"tail"),"size":4}]);
        let m = verify(
            &signed(&serde_json::to_vec(&value).unwrap()),
            key().public_key().as_ref(),
            NOW,
        )
        .unwrap();
        let stage = root.path().join("stage");
        let result = super::download::prepare(
            &m,
            &base,
            &stage,
            &root.path().join("cache"),
            &origin,
            |_| {},
        )
        .await;
        if mode == "bad-full" {
            assert!(result.is_err());
            assert!(!stage.join("Contents/MacOS/linknestx").exists());
        } else {
            let progress = result.unwrap();
            assert_eq!(progress.reused_bytes, 0);
            assert_eq!(
                std::fs::read(stage.join("Contents/MacOS/linknestx")).unwrap(),
                target
            );
        }
        assert_eq!(seen.lock().unwrap().last(), Some(&fullhash));
        assert_eq!(
            std::fs::read(base.join("Contents/MacOS/linknestx")).unwrap(),
            vec![b'z'; 262144]
        );
        server.abort();
    }
}

#[cfg(unix)]
#[test]
fn rejects_symlink_staging_and_keeps_external_files_intact() {
    let root = tempfile::tempdir_in(std::env::temp_dir().canonicalize().unwrap()).unwrap();
    let outside = root.path().join("outside");
    std::fs::create_dir(&outside).unwrap();
    std::fs::write(outside.join("private"), b"original").unwrap();
    std::os::unix::fs::symlink(&outside, root.path().join("stage")).unwrap();
    assert!(super::files::directory(&root.path().join("stage")).is_err());
    assert_eq!(std::fs::read(outside.join("private")).unwrap(), b"original");
}

#[tokio::test]
async fn active_transfer_defers_install_and_never_closes_activity_gate() {
    let root = tempfile::tempdir_in(std::env::temp_dir().canonicalize().unwrap()).unwrap();
    let gate = std::sync::Arc::new(crate::activity::ActivityGate::default());
    let work = gate.try_enter().unwrap();
    let updater = super::UpdateService::new(
        root.path().to_path_buf(),
        std::sync::Arc::new(crate::events::NoopEvents),
        gate.clone(),
    );
    assert!(!updater.begin_install().await.unwrap());
    assert_eq!(updater.status().state, "deferred");
    assert!(gate.try_enter().is_ok());
    drop(work);
    assert!(gate.try_quiesce().is_ok());
}

#[tokio::test]
async fn consumes_actual_go_signed_fixture_with_exact_delta_savings() {
    use axum::{routing::get, Router};
    use std::sync::{Arc, Mutex};
    let envelope = include_bytes!("../../tests/fixtures/update-go/envelope.json");
    let public: serde_json::Value =
        serde_json::from_slice(include_bytes!("../../tests/fixtures/update-go/public.json"))
            .unwrap();
    let pubkey = B64.decode(public["publicKey"].as_str().unwrap()).unwrap();
    let now = chrono::DateTime::parse_from_rfc3339("2026-11-10T14:00:00Z")
        .unwrap()
        .timestamp();
    let m = verify(envelope, &pubkey, now).unwrap();
    let a = vec![b'A'; 262144];
    let b = vec![b'B'; 262144];
    let c = vec![b'C'; 262144];
    let mut base = a.clone();
    base.extend(&b);
    let mut target = a;
    target.extend(&c);
    target.extend(&b);
    let info=b"<?xml version=\"1.0\"?><plist version=\"1.0\"><dict><key>CFBundleVersion</key><string>0.2.1</string></dict></plist>".to_vec();
    let mut objects = std::collections::HashMap::new();
    objects.insert(hash(&c), c);
    objects.insert(hash(&info), info.clone());
    let observed = Arc::new(Mutex::new(Vec::new()));
    let seen = observed.clone();
    let app = Router::new().route(
        "/updates/objects/{hash}",
        get(
            move |axum::extract::Path(id): axum::extract::Path<String>| {
                let objects = objects.clone();
                let seen = seen.clone();
                async move {
                    seen.lock().unwrap().push(id.clone());
                    match objects.get(&id) {
                        Some(b) => (axum::http::StatusCode::OK, b.clone()),
                        None => (axum::http::StatusCode::NOT_FOUND, vec![]),
                    }
                }
            },
        ),
    );
    let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
    let origin = format!("http://{}", listener.local_addr().unwrap());
    let server = tokio::spawn(async move { axum::serve(listener, app).await.unwrap() });
    let root = tempfile::tempdir_in(std::env::temp_dir().canonicalize().unwrap()).unwrap();
    let old = root.path().join("old");
    std::fs::create_dir_all(old.join("Contents/MacOS")).unwrap();
    std::fs::write(old.join("Contents/MacOS/linknestx"), base).unwrap();
    let stage = root.path().join("new");
    let p = super::download::prepare(
        &m,
        &old,
        &stage,
        &root.path().join("cache"),
        &origin,
        |_| {},
    )
    .await
    .unwrap();
    assert_eq!(p.reused_bytes, 524288);
    assert_eq!(p.downloaded_bytes, 262255);
    assert!(p.downloaded_bytes < m.total_bytes);
    assert_eq!(observed.lock().unwrap().len(), 2);
    assert_eq!(
        std::fs::read(stage.join("Contents/MacOS/linknestx")).unwrap(),
        target
    );
    assert_eq!(
        std::fs::read(stage.join("Contents/Info.plist")).unwrap(),
        info
    );
    server.abort();
}

#[test]
fn build_metadata_alone_is_not_a_newer_version_and_null_hint_is_rejected() {
    let mut value = sample();
    value["version"] = "0.2.0+repacked".into();
    let m = verify(
        &signed(&serde_json::to_vec(&value).unwrap()),
        key().public_key().as_ref(),
        NOW,
    )
    .unwrap();
    assert!(eligible(&m, "macos", "aarch64", "0.2.0", 1).is_err());
    let mut value = sample();
    value["files"][0]["blocks"][0]["basePath"] = serde_json::Value::Null;
    assert!(verify(
        &signed(&serde_json::to_vec(&value).unwrap()),
        key().public_key().as_ref(),
        NOW
    )
    .is_err());
}

#[test]
fn executable_swap_preserves_windows_uninstaller_and_rollback() {
    let root = tempfile::tempdir_in(std::env::temp_dir().canonicalize().unwrap()).unwrap();
    let exe = root.path().join("LinkNestX.exe");
    let stage = root.path().join("new.exe");
    let uninstaller = root.path().join("uninstall.exe");
    std::fs::write(&exe, b"old").unwrap();
    std::fs::write(&stage, b"new").unwrap();
    std::fs::write(&uninstaller, b"keep-uninstaller").unwrap();
    let tx = super::install::Transaction::new(
        exe.clone(),
        stage,
        root.path().join("journal"),
        &uuid::Uuid::new_v4().to_string(),
    )
    .unwrap();
    tx.backup().unwrap();
    tx.activate().unwrap();
    assert_eq!(std::fs::read(&exe).unwrap(), b"new");
    assert_eq!(std::fs::read(&uninstaller).unwrap(), b"keep-uninstaller");
    tx.rollback().unwrap();
    assert_eq!(std::fs::read(&exe).unwrap(), b"old");
    assert_eq!(std::fs::read(&uninstaller).unwrap(), b"keep-uninstaller");
}

#[test]
fn accepted_identity_uses_signed_bytes_not_unsigned_envelope_spacing() {
    let envelope = signed(&serde_json::to_vec(&sample()).unwrap());
    let value: serde_json::Value = serde_json::from_slice(&envelope).unwrap();
    let reformatted = serde_json::to_vec_pretty(&value).unwrap();
    assert_eq!(
        super::manifest::identity(&envelope).unwrap(),
        super::manifest::identity(&reformatted).unwrap()
    );
}
