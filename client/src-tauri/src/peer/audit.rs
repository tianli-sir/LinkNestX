use crate::{auth::session::AuthManager, Result, TransferError};
use rusqlite::{params, Connection};
use serde::Serialize;
use std::{path::Path, sync::Mutex};

#[derive(Clone, Debug, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct AuditRecord {
    pub task_id: String,
    pub version_id: String,
    pub node_id: String,
    pub action: String,
    pub bytes: u64,
}
#[derive(Clone, Debug, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct AuditFailure {
    pub record: AuditRecord,
    pub status: Option<u16>,
    pub error: String,
}
/// Separate from the cache: eviction must never erase a pending transfer audit.
pub struct AuditOutbox(Mutex<Connection>);
impl AuditOutbox {
    pub fn open(root: &Path) -> Result<Self> {
        let db = Connection::open(root.join("peer-audit.sqlite3"))?;
        db.execute_batch("PRAGMA journal_mode=WAL;PRAGMA synchronous=FULL;
            CREATE TABLE IF NOT EXISTS chunks(owner TEXT,task TEXT,version TEXT,node TEXT,action TEXT,idx INTEGER,bytes INTEGER NOT NULL,updated INTEGER NOT NULL,PRIMARY KEY(owner,task,version,node,action,idx));
            CREATE TABLE IF NOT EXISTS completion_chunks(owner TEXT,task TEXT,version TEXT,idx INTEGER,node TEXT NOT NULL,bytes INTEGER NOT NULL,PRIMARY KEY(owner,task,version,idx));
            CREATE TABLE IF NOT EXISTS pending(owner TEXT,task TEXT,version TEXT,node TEXT,action TEXT,bytes INTEGER NOT NULL,dirty INTEGER NOT NULL DEFAULT 1,PRIMARY KEY(owner,task,version,node,action));")?;
        let columns: Vec<String> = db
            .prepare("PRAGMA table_info(pending)")?
            .query_map([], |r| r.get(1))?
            .collect::<std::result::Result<_, _>>()?;
        for (name, definition) in [
            ("attempts", "INTEGER NOT NULL DEFAULT 0"),
            ("last_status", "INTEGER"),
            ("last_error", "TEXT"),
        ] {
            if !columns.iter().any(|column| column == name) {
                db.execute(
                    &format!("ALTER TABLE pending ADD COLUMN {name} {definition}"),
                    [],
                )?;
            }
        }
        Ok(Self(Mutex::new(db)))
    }
    pub fn record(
        &self,
        owner: &str,
        record: &AuditRecord,
        index: usize,
        bytes: u64,
        version_size: u64,
    ) -> Result<()> {
        if uuid::Uuid::parse_str(&record.task_id).is_err()
            || uuid::Uuid::parse_str(&record.version_id).is_err()
            || (record.node_id != "server" && uuid::Uuid::parse_str(&record.node_id).is_err())
            || !matches!(
                record.action.as_str(),
                "peer.sent" | "peer.received" | "transfer.completed"
            )
            || bytes > crate::transfer::models::MAX_CHUNK_SIZE
            || bytes > version_size
        {
            return Err(TransferError::new("传输审计参数无效"));
        }
        let mut db = self
            .0
            .lock()
            .map_err(|_| TransferError::new("传输审计存储不可用"))?;
        let tx = db.transaction()?;
        tx.execute("INSERT INTO chunks(owner,task,version,node,action,idx,bytes,updated) VALUES(?1,?2,?3,?4,?5,?6,?7,unixepoch()) ON CONFLICT(owner,task,version,node,action,idx) DO UPDATE SET bytes=max(bytes,excluded.bytes),updated=unixepoch()",params![owner,record.task_id,record.version_id,record.node_id,record.action,index,bytes])?;
        let total:u64=tx.query_row("SELECT coalesce(sum(bytes),0) FROM chunks WHERE owner=?1 AND task=?2 AND version=?3 AND node=?4 AND action=?5",params![owner,record.task_id,record.version_id,record.node_id,record.action],|r|r.get(0))?;
        if total > version_size {
            return Err(TransferError::new("传输审计超出文件大小"));
        }
        tx.execute("INSERT INTO pending(owner,task,version,node,action,bytes,dirty) VALUES(?1,?2,?3,?4,?5,?6,1) ON CONFLICT(owner,task,version,node,action) DO UPDATE SET dirty=CASE WHEN dirty=2 THEN 2 WHEN excluded.bytes>bytes THEN 1 ELSE dirty END,bytes=max(bytes,excluded.bytes)",params![owner,record.task_id,record.version_id,record.node_id,record.action,total])?;
        tx.commit()?;
        Ok(())
    }
    /// Save attribution at verified progress, but do not claim completion yet.
    pub fn stage_completion(
        &self,
        owner: &str,
        record: &AuditRecord,
        index: usize,
        bytes: u64,
        version_size: u64,
    ) -> Result<()> {
        if record.action != "transfer.completed"
            || uuid::Uuid::parse_str(&record.task_id).is_err()
            || uuid::Uuid::parse_str(&record.version_id).is_err()
            || (record.node_id != "server" && uuid::Uuid::parse_str(&record.node_id).is_err())
            || bytes > crate::transfer::models::MAX_CHUNK_SIZE
            || bytes > version_size
        {
            return Err(TransferError::new("传输完成审计参数无效"));
        }
        let mut db = self
            .0
            .lock()
            .map_err(|_| TransferError::new("传输审计存储不可用"))?;
        let tx = db.transaction()?;
        tx.execute("INSERT INTO completion_chunks(owner,task,version,idx,node,bytes) VALUES(?1,?2,?3,?4,?5,?6) ON CONFLICT(owner,task,version,idx) DO NOTHING", params![owner,record.task_id,record.version_id,index,record.node_id,bytes])?;
        let total: u64 = tx.query_row("SELECT coalesce(sum(bytes),0) FROM completion_chunks WHERE owner=?1 AND task=?2 AND version=?3", params![owner,record.task_id,record.version_id], |r| r.get(0))?;
        if total > version_size {
            return Err(TransferError::new("传输完成审计超出文件大小"));
        }
        tx.commit()?;
        Ok(())
    }
    /// Called only after full-file verification and successful destination publication.
    pub fn publish_completion(&self, owner: &str, task: &str, version: &str) -> Result<()> {
        let mut db = self
            .0
            .lock()
            .map_err(|_| TransferError::new("传输审计存储不可用"))?;
        let tx = db.transaction()?;
        tx.execute(
            "INSERT INTO pending(owner,task,version,node,action,bytes,dirty)
             SELECT owner,task,version,node,'transfer.completed',sum(bytes),1 FROM completion_chunks
             WHERE owner=?1 AND task=?2 AND version=?3 GROUP BY node
             ON CONFLICT(owner,task,version,node,action) DO UPDATE SET dirty=CASE WHEN dirty=2 THEN 2 WHEN excluded.bytes>bytes THEN 1 ELSE dirty END,bytes=max(bytes,excluded.bytes)",
            params![owner,task,version])?;
        tx.execute(
            "DELETE FROM completion_chunks WHERE owner=?1 AND task=?2 AND version=?3",
            params![owner, task, version],
        )?;
        tx.commit()?;
        Ok(())
    }
    /// One logical successful save; never attributes cached bytes to a remote source.
    pub fn logical_completion(
        &self,
        owner: &str,
        task: &str,
        version: &str,
        bytes: u64,
    ) -> Result<()> {
        if uuid::Uuid::parse_str(task).is_err()
            || uuid::Uuid::parse_str(version).is_err()
            || bytes > crate::transfer::models::MAX_FILE_SIZE
        {
            return Err(TransferError::new("完成审计参数无效"));
        }
        self.0.lock().map_err(|_|TransferError::new("传输审计存储不可用"))?.execute("INSERT INTO pending(owner,task,version,node,action,bytes,dirty) VALUES(?1,?2,?3,'local','download.completed',?4,1) ON CONFLICT(owner,task,version,node,action) DO NOTHING",params![owner,task,version,bytes])?;
        Ok(())
    }
    pub fn staged_tasks(&self, owner: &str) -> Result<Vec<(String, String)>> {
        let db = self
            .0
            .lock()
            .map_err(|_| TransferError::new("传输审计存储不可用"))?;
        let mut query =
            db.prepare("SELECT DISTINCT task,version FROM completion_chunks WHERE owner=?1")?;
        let rows = query.query_map([owner], |r| Ok((r.get(0)?, r.get(1)?)))?;
        Ok(rows.collect::<std::result::Result<_, _>>()?)
    }
    pub fn pending(&self, owner: &str) -> Result<Vec<AuditRecord>> {
        let db = self
            .0
            .lock()
            .map_err(|_| TransferError::new("传输审计存储不可用"))?;
        let mut query=db.prepare("SELECT task,version,node,action,bytes FROM pending WHERE owner=?1 AND dirty=1 ORDER BY attempts,rowid LIMIT 64")?;
        let rows = query.query_map([owner], |r| {
            Ok(AuditRecord {
                task_id: r.get(0)?,
                version_id: r.get(1)?,
                node_id: r.get(2)?,
                action: r.get(3)?,
                bytes: r.get(4)?,
            })
        })?;
        Ok(rows.collect::<std::result::Result<_, _>>()?)
    }
    pub fn failures(&self, owner: &str) -> Result<Vec<AuditFailure>> {
        let db = self
            .0
            .lock()
            .map_err(|_| TransferError::new("传输审计存储不可用"))?;
        let mut query = db.prepare("SELECT task,version,node,action,bytes,last_status,last_error FROM pending WHERE owner=?1 AND dirty=2 ORDER BY rowid LIMIT 64")?;
        let rows = query.query_map([owner], |r| {
            Ok(AuditFailure {
                record: AuditRecord {
                    task_id: r.get(0)?,
                    version_id: r.get(1)?,
                    node_id: r.get(2)?,
                    action: r.get(3)?,
                    bytes: r.get(4)?,
                },
                status: r.get(5)?,
                error: r.get(6)?,
            })
        })?;
        Ok(rows.collect::<std::result::Result<_, _>>()?)
    }
    pub async fn flush(&self, auth: &AuthManager) -> Result<()> {
        let owner = auth.owner().await?;
        let mut failure = None;
        for record in self.pending(&owner)? {
            // Count before awaiting: cancellation or a hung endpoint must not keep one row
            // permanently at the front of every bounded maintenance batch.
            self.0.lock().map_err(|_|TransferError::new("传输审计存储不可用"))?.execute("UPDATE pending SET attempts=attempts+1 WHERE owner=?1 AND task=?2 AND version=?3 AND node=?4 AND action=?5",params![owner,record.task_id,record.version_id,record.node_id,record.action])?;
            let result = tokio::time::timeout(
                std::time::Duration::from_secs(2),
                auth.json_for(
                    &owner,
                    "POST",
                    "/api/v1/p2p/transfers",
                    Some(serde_json::to_value(&record)?),
                ),
            )
            .await
            .unwrap_or_else(|_| Err(TransferError::new("传输审计补传超时，稍后重试")));
            if let Err(error) = result {
                let terminal = matches!(error.status, Some(400 | 403 | 404 | 410 | 422));
                self.0.lock().map_err(|_|TransferError::new("传输审计存储不可用"))?.execute("UPDATE pending SET dirty=?6,last_status=?7,last_error=?8 WHERE owner=?1 AND task=?2 AND version=?3 AND node=?4 AND action=?5",params![owner,record.task_id,record.version_id,record.node_id,record.action,if terminal {2} else {1},error.status,error.error])?;
                if error.status == Some(401)
                    || !auth.owner().await.is_ok_and(|current| current == owner)
                {
                    return Err(error);
                }
                if terminal || failure.is_none() {
                    failure = Some(error);
                }
                continue;
            }
            self.0.lock().map_err(|_|TransferError::new("传输审计存储不可用"))?.execute("UPDATE pending SET dirty=0 WHERE owner=?1 AND task=?2 AND version=?3 AND node=?4 AND action=?5 AND bytes=?6",params![owner,record.task_id,record.version_id,record.node_id,record.action,record.bytes])?;
        }
        failure.map_or(Ok(()), Err)
    }
}

#[cfg(test)]
mod final_completion_tests {
    use super::*;
    #[test]
    fn cache_only_and_empty_completion_are_idempotent_without_source_bytes() {
        let root = tempfile::tempdir().unwrap();
        let db = AuditOutbox::open(root.path()).unwrap();
        let v = uuid::Uuid::new_v4().to_string();
        for bytes in [0, 4096] {
            let t = uuid::Uuid::new_v4().to_string();
            db.logical_completion("o", &t, &v, bytes).unwrap();
            db.logical_completion("o", &t, &v, bytes).unwrap();
        }
        let rows = db.pending("o").unwrap();
        assert_eq!(rows.len(), 2);
        assert!(rows
            .iter()
            .all(|r| r.action == "download.completed" && r.node_id == "local"));
        assert_eq!(rows.iter().map(|r| r.bytes).sum::<u64>(), 4096);
    }
}
