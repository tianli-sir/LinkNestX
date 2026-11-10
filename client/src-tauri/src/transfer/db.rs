use super::models::*;
use crate::{Result, TransferError};
use rusqlite::{params, Connection, OptionalExtension};
use std::{path::Path, sync::Mutex};

pub struct TransferDb(Mutex<Connection>);
impl TransferDb {
    pub fn open(root: &Path) -> Result<Self> {
        std::fs::create_dir_all(root)?;
        let db = Connection::open(root.join("transfers.sqlite3"))?;
        db.execute_batch("PRAGMA journal_mode=WAL; PRAGMA synchronous=FULL; CREATE TABLE IF NOT EXISTS tasks(id TEXT PRIMARY KEY,owner TEXT NOT NULL,data TEXT NOT NULL); CREATE INDEX IF NOT EXISTS tasks_owner ON tasks(owner); CREATE TABLE IF NOT EXISTS settings(key TEXT PRIMARY KEY,value TEXT NOT NULL);")?;
        let this = Self(Mutex::new(db));
        for mut task in this.all()? {
            if !matches!(
                task.snapshot.state,
                TaskState::Completed | TaskState::Failed | TaskState::Paused
            ) {
                task.snapshot.state = TaskState::Paused;
                task.resume_on_login = true;
                task.snapshot.active_sources.clear();
                task.snapshot.metrics.current_bytes_per_second = 0.0;
                task.snapshot.metrics.eta_seconds = None;
                task.snapshot.metrics.revision = task.snapshot.metrics.revision.saturating_add(1);
                task.metrics_record.snapshot = task.snapshot.metrics.clone();
                this.save(&task)?;
            }
        }
        Ok(this)
    }
    fn lock(&self) -> Result<std::sync::MutexGuard<'_, Connection>> {
        self.0
            .lock()
            .map_err(|_| TransferError::new("传输数据库不可用"))
    }
    pub fn save(&self, task: &Task) -> Result<()> {
        let db = self.lock()?;
        let mut task = task.clone();
        let previous: Option<String> = db
            .query_row(
                "SELECT data FROM tasks WHERE id=?1 AND owner=?2",
                params![task.snapshot.id, task.owner],
                |r| r.get(0),
            )
            .optional()?;
        if let Some(previous) = previous {
            let previous: Task = serde_json::from_str(&previous)?;
            if previous.snapshot.metrics.revision > task.snapshot.metrics.revision {
                task.snapshot.metrics = previous.snapshot.metrics;
                task.metrics_record = previous.metrics_record;
            }
            if let Some(manifest) = task.manifest.as_mut().filter(|m| m.expires_at.is_some()) {
                if previous
                    .manifest
                    .as_ref()
                    .is_some_and(|old| old.expires_at.is_none() && old.immutable_eq(manifest))
                {
                    manifest.expires_at = None;
                }
            }
        }
        db.execute("INSERT INTO tasks(id,owner,data) VALUES(?1,?2,?3) ON CONFLICT(id) DO UPDATE SET data=excluded.data",params![task.snapshot.id,task.owner,serde_json::to_string(&task)?])?;
        Ok(())
    }
    pub fn checkpoint_metrics(
        &self,
        id: &str,
        owner: &str,
        live: &super::metrics::Live,
    ) -> Result<Option<TaskSnapshot>> {
        let db = self.lock()?;
        let data: Option<String> = db
            .query_row(
                "SELECT data FROM tasks WHERE id=?1 AND owner=?2",
                params![id, owner],
                |r| r.get(0),
            )
            .optional()?;
        let Some(data) = data else { return Ok(None) };
        let mut task: Task = serde_json::from_str(&data)?;
        if matches!(
            task.snapshot.state,
            TaskState::Completed | TaskState::Paused | TaskState::Failed
        ) {
            return Ok(None);
        }
        task.metrics_record = live.record(task.snapshot.bytes_completed, task.snapshot.total_bytes);
        task.snapshot.metrics = task.metrics_record.snapshot.clone();
        db.execute(
            "UPDATE tasks SET data=?1 WHERE id=?2 AND owner=?3",
            params![serde_json::to_string(&task)?, id, owner],
        )?;
        Ok(Some(task.snapshot))
    }
    pub fn get(&self, id: &str, owner: &str) -> Result<Task> {
        let data: Option<String> = self
            .lock()?
            .query_row(
                "SELECT data FROM tasks WHERE id=?1 AND owner=?2",
                params![id, owner],
                |r| r.get(0),
            )
            .optional()?;
        serde_json::from_str(&data.ok_or_else(|| TransferError::new("找不到当前账户的传输任务"))?)
            .map_err(Into::into)
    }
    pub fn list(&self, owner: &str) -> Result<Vec<Task>> {
        let db = self.lock()?;
        let mut statement = db.prepare("SELECT data FROM tasks WHERE owner=?1 ORDER BY rowid")?;
        let rows = statement.query_map([owner], |r| r.get::<_, String>(0))?;
        rows.map(|r| Ok(serde_json::from_str(&r?)?)).collect()
    }
    pub(crate) fn all(&self) -> Result<Vec<Task>> {
        let db = self.lock()?;
        let mut statement = db.prepare("SELECT data FROM tasks")?;
        let rows = statement.query_map([], |r| r.get::<_, String>(0))?;
        rows.map(|r| Ok(serde_json::from_str(&r?)?)).collect()
    }
    pub fn policy(&self) -> Result<SharingPolicy> {
        let value: Option<String> = self
            .lock()?
            .query_row("SELECT value FROM settings WHERE key='policy'", [], |r| {
                r.get(0)
            })
            .optional()?;
        match value {
            Some(v) => {
                let mut p: SharingPolicy = serde_json::from_str(&v)?;
                p.enabled = true;
                if p.cache_quota_bytes < MAX_CHUNK_SIZE {
                    p.cache_quota_bytes = SharingPolicy::default().cache_quota_bytes;
                }
                p.validate()?;
                self.set_policy(&p)?;
                Ok(p)
            }
            None => Ok(SharingPolicy::default()),
        }
    }
    pub fn set_policy(&self, policy: &SharingPolicy) -> Result<()> {
        self.lock()?.execute("INSERT INTO settings(key,value) VALUES('policy',?1) ON CONFLICT(key) DO UPDATE SET value=excluded.value",[serde_json::to_string(policy)?])?;
        Ok(())
    }
    pub fn checkpoint(&self) -> Result<()> {
        self.lock()?
            .execute_batch("PRAGMA wal_checkpoint(TRUNCATE)")?;
        Ok(())
    }
}
