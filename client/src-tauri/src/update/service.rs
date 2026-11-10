use super::{
    download, files, install,
    manifest::{self, bad},
};
use crate::{
    activity::{ActivityGate, InstallationPermit},
    events::EventSink,
    Result,
};
use serde::{Deserialize, Serialize};
use std::{
    path::PathBuf,
    sync::{Arc, Mutex},
    time::Duration,
};
#[derive(Clone, Serialize, Debug)]
#[serde(rename_all = "camelCase")]
pub struct UpdateStatus {
    pub state: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub version: Option<String>,
    pub downloaded_bytes: u64,
    pub reused_bytes: u64,
    pub total_bytes: u64,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub error: Option<String>,
}
impl Default for UpdateStatus {
    fn default() -> Self {
        Self {
            state: "idle".into(),
            version: None,
            downloaded_bytes: 0,
            reused_bytes: 0,
            total_bytes: 0,
            error: None,
        }
    }
}
#[derive(Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
struct Accepted {
    sequence: u64,
    envelope_hash: String,
}
struct Ready {
    envelope: Vec<u8>,
    stage: PathBuf,
}
pub struct UpdateService {
    root: PathBuf,
    events: Arc<dyn EventSink>,
    gate: Arc<ActivityGate>,
    status: Mutex<UpdateStatus>,
    ready: Mutex<Option<Ready>>,
    rollback: Mutex<Option<install::RollbackOutcome>>,
    operation: tokio::sync::Mutex<()>,
    handoff: Mutex<Option<(install::Handoff, InstallationPermit)>>,
}
impl UpdateService {
    pub fn new(root: PathBuf, events: Arc<dyn EventSink>, gate: Arc<ActivityGate>) -> Arc<Self> {
        let mut status = UpdateStatus::default();
        let rollback = install::last_outcome(&root);
        if let Some(outcome) = &rollback {
            status.state = "rolledBack".into();
            status.error = Some(outcome.error.clone());
            status.version = outcome.version.clone();
        }
        Arc::new(Self {
            root,
            events,
            gate,
            status: Mutex::new(status),
            ready: Mutex::new(None),
            rollback: Mutex::new(rollback),
            operation: tokio::sync::Mutex::new(()),
            handoff: Mutex::new(None),
        })
    }
    pub fn status(&self) -> UpdateStatus {
        self.status.lock().unwrap().clone()
    }
    fn change(&self, update: impl FnOnce(&mut UpdateStatus)) {
        let value = {
            let mut s = self.status.lock().unwrap();
            update(&mut s);
            serde_json::to_value(&*s).unwrap()
        };
        self.events.emit("update.status", value);
    }
    pub fn start(self: &Arc<Self>) {
        let service = self.clone();
        tauri::async_runtime::spawn(async move {
            {
                let _guard = service.operation.lock().await;
                let restore = service.clone();
                let _ = tokio::task::spawn_blocking(move || restore.restore_ready()).await;
            }
            tokio::time::sleep(Duration::from_secs(15)).await;
            loop {
                let _ = service.trusted_check(false).await;
                tokio::time::sleep(Duration::from_secs(6 * 60 * 60)).await;
            }
        });
    }
    pub async fn check(&self) -> UpdateStatus {
        self.trusted_check(true).await
    }
    async fn trusted_check(&self, explicit: bool) -> UpdateStatus {
        let key = match manifest::pinned_key() {
            Ok(key) => key,
            Err(error) => {
                if !self.has_rollback() {
                    self.change(|s| {
                        s.state = "failed".into();
                        s.error = Some(error.error);
                    });
                }
                return self.status();
            }
        };
        self.check_from(explicit, crate::deployment::SERVER_ORIGIN, &key)
            .await
    }
    fn has_rollback(&self) -> bool {
        self.rollback.lock().unwrap().is_some()
    }
    fn retain_rollback_for(&self, sequence: u64) -> bool {
        self.rollback
            .lock()
            .unwrap()
            .as_ref()
            .is_some_and(|outcome| sequence <= outcome.sequence.unwrap_or(u64::MAX))
    }
    fn acknowledge_rollback(&self) -> Result<()> {
        if self.has_rollback() {
            install::acknowledge_outcome(&self.root)?;
            *self.rollback.lock().unwrap() = None;
        }
        Ok(())
    }
    async fn check_from(&self, explicit: bool, origin: &str, key: &[u8]) -> UpdateStatus {
        let Ok(_guard) = self.operation.try_lock() else {
            return self.status();
        };
        if self.handoff.lock().unwrap().is_some() {
            return self.status();
        }
        if explicit && self.acknowledge_rollback().is_err() {
            return self.status();
        }
        if !self.has_rollback() {
            self.change(|s| {
                s.state = "checking".into();
                s.error = None;
            });
        }
        if let Err(e) = self.prepare_from(origin, key).await {
            if self.has_rollback() {
                return self.status();
            }
            self.change(|s| {
                s.state = if self.ready.lock().unwrap().is_some() {
                    "ready"
                } else {
                    "failed"
                }
                .into();
                s.error = Some(e.error);
            });
        }
        self.status()
    }
    fn restore_ready(&self) -> Result<()> {
        self.restore_ready_with_key(&manifest::pinned_key()?)
    }
    fn restore_ready_with_key(&self, key: &[u8]) -> Result<()> {
        let updates = self.root.join("updates");
        let path = updates.join("accepted.json");
        if !path.exists() {
            return Ok(());
        }
        let file = files::regular(&path)?;
        if file.metadata()?.len() > 1024 {
            return Err(bad("防回退记录无效"));
        }
        let accepted: Accepted = serde_json::from_reader(file)?;
        if accepted.sequence <= manifest::BUILD_SEQUENCE {
            return Ok(());
        }
        if accepted.envelope_hash.len() != 64
            || !accepted
                .envelope_hash
                .bytes()
                .all(|c| c.is_ascii_hexdigit() && !c.is_ascii_uppercase())
        {
            return Err(bad("本地更新标识错误"));
        }
        let release = updates.join("prepared").join(&accepted.envelope_hash);
        let file = files::regular(&release.join("manifest.json"))?;
        if file.metadata()?.len() > manifest::MAX_ENVELOPE as u64 {
            return Err(bad("本地清单过大"));
        }
        let mut envelope = Vec::new();
        use std::io::Read;
        file.take(manifest::MAX_ENVELOPE as u64 + 1)
            .read_to_end(&mut envelope)?;
        if manifest::identity(&envelope)? != accepted.envelope_hash {
            return Err(bad("本地清单已变化"));
        }
        let m = manifest::verify(&envelope, key, chrono::Utc::now().timestamp())?;
        manifest::eligible(
            &m,
            std::env::consts::OS,
            std::env::consts::ARCH,
            env!("CARGO_PKG_VERSION"),
            manifest::BUILD_SEQUENCE,
        )?;
        if m.build_sequence != accepted.sequence {
            return Err(bad("发布序号不匹配"));
        }
        let stage = release.join("payload");
        files::verify_tree(&stage, &m)?;
        let retain = self.retain_rollback_for(m.build_sequence);
        if !retain {
            self.acknowledge_rollback()?;
        }
        *self.ready.lock().unwrap() = Some(Ready { envelope, stage });
        self.change(|s| {
            if !retain {
                s.state = "ready".into();
                s.error = None;
            }
            s.version = Some(m.version);
            s.total_bytes = m.total_bytes;
        });
        Ok(())
    }
    async fn prepare_from(&self, origin: &str, key: &[u8]) -> Result<()> {
        let client = download::client()?;
        let url = format!(
            "{}/updates/{}/{}/latest.json",
            origin,
            std::env::consts::OS,
            std::env::consts::ARCH
        );
        let Some(envelope) = download::manifest_bytes(&client, &url).await? else {
            if self.has_rollback() {
                return Ok(());
            }
            self.change(|s| {
                s.state = if self.ready.lock().unwrap().is_some() {
                    "ready"
                } else {
                    "idle"
                }
                .into()
            });
            return Ok(());
        };
        let m = manifest::verify(&envelope, key, chrono::Utc::now().timestamp())?;
        install::validate_entry(&m)?;
        if m.platform != std::env::consts::OS || m.arch != std::env::consts::ARCH {
            return Err(bad("平台不匹配"));
        }
        if m.version == env!("CARGO_PKG_VERSION") && m.build_sequence == manifest::BUILD_SEQUENCE {
            if self.has_rollback() {
                return Ok(());
            }
            self.change(|s| s.state = "idle".into());
            return Ok(());
        }
        manifest::eligible(
            &m,
            std::env::consts::OS,
            std::env::consts::ARCH,
            env!("CARGO_PKG_VERSION"),
            manifest::BUILD_SEQUENCE,
        )?;
        let updates = self.root.join("updates");
        files::directory(&updates)?;
        let digest = manifest::identity(&envelope)?;
        let accepted_path = updates.join("accepted.json");
        if accepted_path.exists() {
            let f = files::regular(&accepted_path)?;
            if f.metadata()?.len() > 1024 {
                return Err(bad("本地防回退记录无效"));
            }
            let accepted: Accepted = serde_json::from_reader(f)?;
            if m.build_sequence < accepted.sequence
                || (m.build_sequence == accepted.sequence && digest != accepted.envelope_hash)
            {
                return Err(bad("拒绝已接受发布之前的序号或同序号替换"));
            }
        }
        if self.retain_rollback_for(m.build_sequence) {
            return Ok(());
        }
        // A manual retry may reuse the already verified prepared tree, but checks it again.
        let cached = {
            let ready = self.ready.lock().unwrap();
            ready
                .as_ref()
                .filter(|r| manifest::identity(&r.envelope).ok().as_deref() == Some(&digest))
                .map(|r| r.stage.clone())
        };
        if let Some(stage) = cached {
            files::verify_tree(&stage, &m)?;
            self.acknowledge_rollback()?;
            self.change(|s| {
                s.state = "ready".into();
                s.error = None;
            });
            return Ok(());
        }
        *self.ready.lock().unwrap() = None;
        files::atomic_write(
            &accepted_path,
            &serde_json::to_vec(&Accepted {
                sequence: m.build_sequence,
                envelope_hash: digest.clone(),
            })?,
        )?;
        self.acknowledge_rollback()?;
        let release = updates.join("prepared").join(&digest);
        files::directory(&release)?;
        files::atomic_write(&release.join("manifest.json"), &envelope)?;
        let stage = release.join("payload");
        let cache = release.join("objects");
        self.change(|s| {
            s.version = Some(m.version.clone());
            s.total_bytes = m.total_bytes;
            s.downloaded_bytes = 0;
            s.reused_bytes = 0;
            s.state = "downloading".into();
        });
        // Development binaries and system installs still report clear manual-install guidance.
        let base = install::installed_root()?;
        download::prepare(&m, &base, &stage, &cache, origin, |p| {
            self.change(|s| {
                s.downloaded_bytes = p.downloaded_bytes;
                s.reused_bytes = p.reused_bytes;
                s.state = if p.downloaded_bytes == 0 {
                    "reconstructing"
                } else {
                    "downloading"
                }
                .into();
            })
        })
        .await?;
        self.change(|s| s.state = "verifying".into());
        files::verify_tree(&stage, &m)?;
        *self.ready.lock().unwrap() = Some(Ready { envelope, stage });
        self.change(|s| {
            s.state = "ready".into();
            s.error = None;
        });
        Ok(())
    }
    pub fn defer(&self) -> UpdateStatus {
        if !self.has_rollback()
            && self.ready.lock().unwrap().is_some()
            && self.handoff.lock().unwrap().is_none()
        {
            self.change(|s| {
                s.state = "deferred".into();
                s.error = None;
            });
        }
        self.status()
    }
    pub async fn begin_install(&self) -> Result<bool> {
        let _operation = self
            .operation
            .try_lock()
            .map_err(|_| bad("正在检查或准备更新"))?;
        if self.handoff.lock().unwrap().is_some() {
            return Err(bad("安装已启动"));
        }
        let permit = match self.gate.try_quiesce() {
            Ok(p) => p,
            Err(_) => {
                self.change(|s| {
                    s.state = "deferred".into();
                    s.error = Some("活动任务完成后，请再次选择重启更新".into());
                });
                return Ok(false);
            }
        };
        let (envelope, stage) = {
            let r = self.ready.lock().unwrap();
            let ready = r.as_ref().ok_or_else(|| bad("尚无已验证的更新"))?;
            (ready.envelope.clone(), ready.stage.clone())
        };
        self.acknowledge_rollback()?;
        self.change(|s| {
            s.state = "installing".into();
            s.error = None;
        });
        let root = self.root.clone();
        let prepared =
            tokio::task::spawn_blocking(move || install::prepare_handoff(&root, &envelope, &stage))
                .await
                .map_err(|_| bad("安装准备任务失败"))?;
        let mut handoff = match prepared {
            Ok(h) => h,
            Err(e) => {
                self.change(|s| {
                    s.state = "ready".into();
                    s.error = Some(e.error.clone());
                });
                return Err(e);
            }
        };
        let deadline = tokio::time::Instant::now() + Duration::from_secs(30);
        loop {
            match handoff.ready() {
                Ok(true) => break,
                Ok(false) if tokio::time::Instant::now() < deadline => {
                    tokio::time::sleep(Duration::from_millis(100)).await
                }
                _ => {
                    handoff.cancel();
                    self.change(|s| {
                        s.state = "ready".into();
                        s.error = Some("助手启动失败，原应用保持运行".into());
                    });
                    return Err(bad("助手未就绪"));
                }
            }
        }
        *self.handoff.lock().unwrap() = Some((handoff, permit));
        self.change(|s| s.state = "awaitingHealth".into());
        Ok(true)
    }
}

#[cfg(test)]
mod review_tests {
    use super::*;
    use base64::{engine::general_purpose::URL_SAFE_NO_PAD as B64, Engine};
    use ring::signature::{Ed25519KeyPair, KeyPair};
    fn next_version(offset: u64) -> String {
        let mut version = semver::Version::parse(env!("CARGO_PKG_VERSION")).unwrap();
        version.patch += offset;
        version.to_string()
    }
    fn cached_failed_release() -> (tempfile::TempDir, Vec<u8>, Vec<u8>, String) {
        let root = tempfile::tempdir_in(std::env::temp_dir().canonicalize().unwrap()).unwrap();
        let key = Ed25519KeyPair::from_seed_unchecked(&[71; 32]).unwrap();
        let public = key.public_key().as_ref().to_vec();
        let now = chrono::Utc::now();
        let path = if cfg!(windows) {
            "LinkNestX.exe"
        } else {
            "Contents/MacOS/linknestx"
        };
        let manifest = serde_json::json!({"schemaVersion":1,"appId":"com.linknestx.client","channel":"stable","platform":std::env::consts::OS,"arch":std::env::consts::ARCH,"version":next_version(1),"buildSequence":manifest::BUILD_SEQUENCE+1,"issuedAt":now.to_rfc3339(),"expiresAt":(now+chrono::Duration::days(1)).to_rfc3339(),"entryPoint":path,"totalBytes":3,"files":[{"path":path,"mode":493,"size":3,"sha256":manifest::hash(b"new"),"blocks":[{"sha256":manifest::hash(b"new"),"size":3}]}]});
        let raw = serde_json::to_vec(&manifest).unwrap();
        let mut message = b"LinkNestX-update-manifest-v1\0".to_vec();
        message.extend(&raw);
        let envelope=serde_json::to_vec(&serde_json::json!({"keyId":&manifest::hash(&public)[..16],"manifest":B64.encode(raw),"signature":B64.encode(key.sign(&message).as_ref())})).unwrap();
        let identity = manifest::identity(&envelope).unwrap();
        let release = root.path().join("updates/prepared").join(&identity);
        files::directory(&release.join("payload").join(path).parent().unwrap()).unwrap();
        std::fs::write(release.join("payload").join(path), b"new").unwrap();
        files::set_mode(&release.join("payload").join(path), 493).unwrap();
        files::atomic_write(&release.join("manifest.json"), &envelope).unwrap();
        files::atomic_write(
            &root.path().join("updates/accepted.json"),
            &serde_json::to_vec(&Accepted {
                sequence: manifest::BUILD_SEQUENCE + 1,
                envelope_hash: identity,
            })
            .unwrap(),
        )
        .unwrap();
        let id = uuid::Uuid::new_v4().to_string();
        let jobdir = root.path().join("updates/jobs").join(&id);
        files::directory(&jobdir).unwrap();
        files::atomic_write(&root.path().join("updates/pending"), id.as_bytes()).unwrap();
        files::atomic_write(&jobdir.join("phase"), b"rolledBack").unwrap();
        files::atomic_write(&jobdir.join("error"), b"health confirmation timed out").unwrap();
        // The same persisted job metadata produced by the trusted handoff; no helper is launched here.
        let job = serde_json::json!({"id":id,"tx":{"target":root.path().join("LinkNestX.app"),"staged":root.path().join("staged"),"journal":jobdir.join("phase"),"id":id,"old_digest":"fixture"},"parent_pid":0,"nonce":"fixture","helper_sha256":"fixture","helper_size":0,"version":next_version(1),"sequence":manifest::BUILD_SEQUENCE+1,"prepared_at":now.timestamp()});
        files::atomic_write(&jobdir.join("job.json"), &serde_json::to_vec(&job).unwrap()).unwrap();
        (root, public, envelope, id)
    }
    fn service(root: &std::path::Path) -> Arc<UpdateService> {
        UpdateService::new(
            root.to_path_buf(),
            Arc::new(crate::events::NoopEvents),
            Arc::new(ActivityGate::default()),
        )
    }
    #[tokio::test]
    async fn startup_and_automatic_same_release_check_keep_rollback_reason() {
        use axum::{routing::get, Router};
        let (root, key, envelope, _) = cached_failed_release();
        let update = service(root.path());
        assert_eq!(update.status().state, "rolledBack");
        update.restore_ready_with_key(&key).unwrap();
        assert_eq!(update.status().state, "rolledBack");
        assert_eq!(
            update.status().error.as_deref(),
            Some("health confirmation timed out")
        );
        assert!(update.ready.lock().unwrap().is_some());
        let body = envelope.clone();
        let app = Router::new().route(
            "/updates/{platform}/{arch}/latest.json",
            get(move || {
                let body = body.clone();
                async move { body }
            }),
        );
        let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
        let origin = format!("http://{}", listener.local_addr().unwrap());
        let server = tokio::spawn(async move { axum::serve(listener, app).await.unwrap() });
        update.check_from(false, &origin, &key).await;
        assert_eq!(update.status().state, "rolledBack");
        assert_eq!(
            update.status().error.as_deref(),
            Some("health confirmation timed out")
        );
        assert_eq!(update.defer().state, "rolledBack");
        update.check_from(true, &origin, &key).await;
        assert_eq!(update.status().state, "ready");
        assert!(update.status().error.is_none());
        assert_eq!(
            std::fs::read(
                root.path()
                    .join("updates/jobs")
                    .join(std::fs::read_to_string(root.path().join("updates/pending")).unwrap())
                    .join("phase")
            )
            .unwrap(),
            b"rolledBack"
        );
        let reopened = service(root.path());
        reopened.restore_ready_with_key(&key).unwrap();
        assert_eq!(reopened.status().state, "ready");
        server.abort();
    }
    #[tokio::test]
    async fn automatic_missing_bad_and_corrupt_responses_preserve_rollback_history() {
        use axum::{http::StatusCode, routing::get, Router};
        let (root, key, envelope, id) = cached_failed_release();
        let update = service(root.path());
        update.restore_ready_with_key(&key).unwrap();
        let response = Arc::new(Mutex::new((StatusCode::OK, envelope.clone())));
        let served = response.clone();
        let app = Router::new().route(
            "/updates/{platform}/{arch}/latest.json",
            get(move || {
                let served = served.clone();
                async move { served.lock().unwrap().clone() }
            }),
        );
        let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
        let origin = format!("http://{}", listener.local_addr().unwrap());
        let server = tokio::spawn(async move { axum::serve(listener, app).await.unwrap() });
        let mut corrupt: serde_json::Value = serde_json::from_slice(&envelope).unwrap();
        corrupt["signature"] = B64.encode([0u8; 64]).into();
        for (status, body) in [
            (StatusCode::NOT_FOUND, Vec::new()),
            (StatusCode::INTERNAL_SERVER_ERROR, Vec::new()),
            (StatusCode::OK, serde_json::to_vec(&corrupt).unwrap()),
        ] {
            *response.lock().unwrap() = (status, body);
            update.check_from(false, &origin, &key).await;
            assert_eq!(update.status().state, "rolledBack");
            assert_eq!(
                update.status().error.as_deref(),
                Some("health confirmation timed out")
            );
            assert!(!root
                .path()
                .join("updates/jobs")
                .join(&id)
                .join("acknowledged")
                .exists());
        }
        let accepted: Accepted = serde_json::from_slice(
            &std::fs::read(root.path().join("updates/accepted.json")).unwrap(),
        )
        .unwrap();
        assert_eq!(accepted.sequence, manifest::BUILD_SEQUENCE + 1);
        server.abort();
    }
    #[test]
    fn only_verified_higher_cached_release_replaces_rollback_status() {
        let (root, public, envelope, id) = cached_failed_release();
        let update = service(root.path());
        let mut signed: serde_json::Value = serde_json::from_slice(&envelope).unwrap();
        let mut m: serde_json::Value =
            serde_json::from_slice(&B64.decode(signed["manifest"].as_str().unwrap()).unwrap())
                .unwrap();
        m["version"] = next_version(2).into();
        m["buildSequence"] = (manifest::BUILD_SEQUENCE + 2).into();
        let raw = serde_json::to_vec(&m).unwrap();
        signed["manifest"] = B64.encode(&raw).into();
        let key = Ed25519KeyPair::from_seed_unchecked(&[71; 32]).unwrap();
        let mut message = b"LinkNestX-update-manifest-v1\0".to_vec();
        message.extend(raw);
        signed["signature"] = B64.encode(key.sign(&message).as_ref()).into();
        let higher = serde_json::to_vec(&signed).unwrap();
        let identity = manifest::identity(&higher).unwrap();
        let release = root.path().join("updates/prepared").join(&identity);
        let path = m["entryPoint"].as_str().unwrap();
        files::directory(release.join("payload").join(path).parent().unwrap()).unwrap();
        std::fs::write(release.join("payload").join(path), b"new").unwrap();
        files::set_mode(&release.join("payload").join(path), 493).unwrap();
        files::atomic_write(
            &root.path().join("updates/accepted.json"),
            &serde_json::to_vec(&Accepted {
                sequence: manifest::BUILD_SEQUENCE + 2,
                envelope_hash: identity,
            })
            .unwrap(),
        )
        .unwrap();
        let mut corrupted = signed;
        corrupted["signature"] = B64.encode([0u8; 64]).into();
        files::atomic_write(
            &release.join("manifest.json"),
            &serde_json::to_vec(&corrupted).unwrap(),
        )
        .unwrap();
        assert!(update.restore_ready_with_key(&public).is_err());
        assert_eq!(update.status().state, "rolledBack");
        files::atomic_write(&release.join("manifest.json"), &higher).unwrap();
        update.restore_ready_with_key(&public).unwrap();
        assert_eq!(update.status().state, "ready");
        assert_eq!(
            update.status().version.as_deref(),
            Some(next_version(2).as_str())
        );
        assert!(update.status().error.is_none());
        let jobdir = root.path().join("updates/jobs").join(id);
        assert_eq!(std::fs::read(jobdir.join("phase")).unwrap(), b"rolledBack");
        assert_eq!(
            std::fs::read(jobdir.join("error")).unwrap(),
            b"health confirmation timed out"
        );
        assert!(jobdir.join("acknowledged").is_file());
    }
}
