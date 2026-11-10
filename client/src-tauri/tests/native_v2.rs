use linknestx::transfer::{db::TransferDb, models::SharingPolicy};
#[test]
fn existing_disabled_zero_cache_policy_migrates() {
    let root = tempfile::tempdir().unwrap();
    let db = TransferDb::open(root.path()).unwrap();
    let mut old = SharingPolicy::default();
    old.enabled = false;
    old.cache_quota_bytes = 0;
    db.set_policy(&old).unwrap();
    drop(db);
    let db = TransferDb::open(root.path()).unwrap();
    let policy = db.policy().unwrap();
    assert!(policy.enabled, "old local opt-out must migrate");
    assert!(policy.cache_quota_bytes >= 16_777_216);
}
#[test]
fn user_cannot_disable_participation_or_cache() {
    let mut policy = SharingPolicy::default();
    policy.enabled = false;
    assert!(policy.validate().is_err());
    policy.enabled = true;
    policy.cache_quota_bytes = 0;
    assert!(policy.validate().is_err());
}
#[test]
fn stale_worker_save_preserves_newer_metrics_checkpoint_and_legacy_is_unknown() {
    use linknestx::transfer::models::{Direction, Task};
    let root = tempfile::tempdir().unwrap();
    let db = TransferDb::open(root.path()).unwrap();
    let old = Task::new(
        "owner".into(),
        Direction::Download,
        "version".into(),
        "/unused".into(),
    );
    assert_eq!(old.snapshot.metrics.active_duration_ms, None);
    db.save(&old).unwrap();
    let mut newer = old.clone();
    newer.snapshot.metrics.revision = 10;
    newer.snapshot.metrics.network_bytes = 4000;
    newer.metrics_record.snapshot = newer.snapshot.metrics.clone();
    db.save(&newer).unwrap();
    db.save(&old).unwrap();
    let saved = db.get(&old.snapshot.id, "owner").unwrap();
    assert_eq!(saved.snapshot.metrics.network_bytes, 4000);
    assert_eq!(saved.snapshot.metrics.revision, 10);
}
#[test]
fn preferences_reject_hidden_opt_out_fields() {
    use linknestx::commands::TransferPreferences;
    assert!(serde_json::from_value::<TransferPreferences>(serde_json::json!({"maxConcurrent":3,"bandwidthLimitBps":0,"autostart":true,"enabled":false})).is_err());
    assert!(serde_json::from_value::<TransferPreferences>(serde_json::json!({"maxConcurrent":3,"bandwidthLimitBps":0,"autostart":true,"cacheQuotaBytes":0})).is_err());
}

#[test]
fn legacy_resume_retains_partial_history_while_new_tasks_keep_complete_measurement() {
    use linknestx::transfer::{
        metrics::Meter,
        models::{Direction, Task, TaskState},
    };
    let fresh = Task::new(
        "alice".into(),
        Direction::Download,
        "v".into(),
        "/unused".into(),
    );
    assert_eq!(
        serde_json::to_value(&fresh.snapshot.metrics).unwrap()["historyComplete"],
        true
    );
    let mut old_json = serde_json::to_value(&fresh).unwrap();
    old_json["snapshot"]["metrics"]
        .as_object_mut()
        .unwrap()
        .remove("historyComplete");
    old_json["metrics_record"]["snapshot"]
        .as_object_mut()
        .unwrap()
        .remove("historyComplete");
    let mut legacy: Task = serde_json::from_value(old_json).unwrap();
    legacy.snapshot.bytes_completed = 5000;
    let mut measured = Meter::new(legacy.metrics_record.clone(), 0, 100_000);
    measured.phase(true, 0, 100_000);
    measured.payload(1000, 1000, 101_000);
    measured.credit(5, 1000, false, 1000, 101_000);
    measured.stop(1000, 101_000);
    legacy.snapshot.metrics = measured.snapshot(1000, 101_000, 6000, 6000);
    legacy.metrics_record = measured.record;
    legacy.snapshot.state = TaskState::Completed;
    assert_eq!(legacy.snapshot.metrics.active_duration_ms, Some(1000));
    assert_eq!(legacy.snapshot.metrics.credited_network_bytes, 1000);
    assert_eq!(
        serde_json::to_value(&legacy.snapshot.metrics).unwrap()["historyComplete"],
        false
    );
    let root = tempfile::tempdir().unwrap();
    let db = TransferDb::open(root.path()).unwrap();
    db.save(&legacy).unwrap();
    drop(db);
    let saved = TransferDb::open(root.path())
        .unwrap()
        .get(&legacy.snapshot.id, "alice")
        .unwrap();
    let mut resumed = Meter::new(saved.metrics_record, 0, 200_000);
    assert_eq!(
        serde_json::to_value(resumed.snapshot(0, 200_000, 6000, 6000)).unwrap()["historyComplete"],
        false
    );
    let mut normal = Meter::new(fresh.metrics_record, 0, 1000);
    normal.phase(true, 0, 1000);
    normal.stop(1000, 2000);
    let restored = serde_json::from_value(serde_json::to_value(normal.record).unwrap()).unwrap();
    let mut normal = Meter::new(restored, 0, 3000);
    assert_eq!(
        serde_json::to_value(normal.snapshot(0, 3000, 0, 1000)).unwrap()["historyComplete"],
        true
    );
}

#[tokio::test]
async fn startup_reclaims_only_unreferenced_owned_upload_directories_once_per_root() {
    use linknestx::{
        auth::session::AuthManager,
        events::NoopEvents,
        transfer::{
            models::{Direction, Task, TaskState},
            service::TransferService,
        },
    };
    use std::sync::Arc;
    let root = tempfile::tempdir().unwrap();
    let staging = root.path().join("attachments");
    std::fs::create_dir(&staging).unwrap();
    let make_snapshot = || {
        let dir = tempfile::Builder::new()
            .prefix("upload-")
            .tempdir_in(&staging)
            .unwrap()
            .keep();
        std::fs::write(dir.join("chosen.txt"), b"chosen").unwrap();
        dir
    };
    let orphan = make_snapshot();
    let paused = make_snapshot();
    let failed = make_snapshot();
    let unknown_owned_flag = make_snapshot();
    let original = root.path().join("user-original.txt");
    std::fs::write(&original, b"original").unwrap();
    let unrelated = staging.join("upload-my-documents");
    std::fs::create_dir(&unrelated).unwrap();
    std::fs::write(unrelated.join("personal.txt"), b"personal").unwrap();
    let unusual = make_snapshot();
    std::fs::create_dir(unusual.join("nested")).unwrap();
    #[cfg(unix)]
    let symlink = {
        let p = staging.join("upload-link12");
        std::os::unix::fs::symlink(root.path(), &p).unwrap();
        p
    };
    let db = TransferDb::open(root.path()).unwrap();
    for (owner, dir, state, owned) in [
        ("alice", &paused, TaskState::Paused, true),
        ("bob", &failed, TaskState::Failed, true),
        ("carol", &unknown_owned_flag, TaskState::Paused, false),
    ] {
        let mut task = Task::new(
            owner.into(),
            Direction::Upload,
            "v".into(),
            dir.join("chosen.txt").to_str().unwrap().into(),
        );
        task.snapshot.state = state;
        task.owned_source = owned;
        db.save(&task).unwrap();
    }
    drop(db);
    let auth = Arc::new(AuthManager::new(root.path()).unwrap());
    let service = TransferService::open(root.path(), auth.clone(), Arc::new(NoopEvents)).unwrap();
    assert!(
        !orphan.exists(),
        "interrupted preparation has no task reference and must be reclaimed"
    );
    for dir in [&paused, &failed, &unknown_owned_flag] {
        assert_eq!(std::fs::read(dir.join("chosen.txt")).unwrap(), b"chosen");
    }
    assert_eq!(std::fs::read(&original).unwrap(), b"original");
    assert!(unrelated.exists());
    assert!(unusual.exists());
    #[cfg(unix)]
    assert!(std::fs::symlink_metadata(symlink)
        .unwrap()
        .file_type()
        .is_symlink());
    let preparing_now = make_snapshot();
    let reopened = TransferService::open(root.path(), auth, Arc::new(NoopEvents)).unwrap();
    assert!(
        preparing_now.exists(),
        "startup cleanup must not rerun against an active preparation"
    );
    reopened.shutdown().await.unwrap();
    service.shutdown().await.unwrap();
}
