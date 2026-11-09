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
        self.lock()?.execute("INSERT INTO tasks(id,owner,data) VALUES(?1,?2,?3) ON CONFLICT(id) DO UPDATE SET data=excluded.data",params![task.snapshot.id,task.owner,serde_json::to_string(task)?])?;
        Ok(())
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
    fn all(&self) -> Result<Vec<Task>> {
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
            Some(v) => Ok(serde_json::from_str(&v)?),
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
