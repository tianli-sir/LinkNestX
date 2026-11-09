use super::models::{hash, Chunk};
use crate::{Result, TransferError};
use rusqlite::{params, Connection, OptionalExtension};
use std::{
    path::{Path, PathBuf},
    sync::{
        atomic::{AtomicU64, Ordering},
        Mutex,
    },
};

pub struct Cache {
    root: PathBuf,
    db: Mutex<Connection>,
    quota: AtomicU64,
    operation: tokio::sync::Mutex<()>,
}
impl Cache {
    pub fn open(root: &Path, quota: u64) -> Result<Self> {
        let root = root.join("cache");
        std::fs::create_dir_all(&root)?;
        #[cfg(unix)]
        {
            use std::os::unix::fs::PermissionsExt;
            std::fs::set_permissions(&root, std::fs::Permissions::from_mode(0o700))?;
        }
        let db = Connection::open(root.join("index.sqlite3"))?;
        db.execute_batch("PRAGMA journal_mode=WAL;CREATE TABLE IF NOT EXISTS chunks(key TEXT PRIMARY KEY,owner TEXT NOT NULL,version TEXT NOT NULL,size INTEGER NOT NULL,last_used INTEGER NOT NULL,expires INTEGER);")?;
        // Files left by a crash before metadata commit are never trusted or counted as cache.
        for entry in std::fs::read_dir(&root)? {
            let entry = entry?;
            let name = entry.file_name().to_string_lossy().into_owned();
            if name.ends_with(".part")
                || (super::models::valid_hash(&name)
                    && !db.query_row(
                        "SELECT EXISTS(SELECT 1 FROM chunks WHERE key=?1)",
                        [&name],
                        |r| r.get::<_, bool>(0),
                    )?)
            {
                std::fs::remove_file(entry.path())?;
            }
        }
        Ok(Self {
            root,
            db: Mutex::new(db),
            quota: AtomicU64::new(quota),
            operation: tokio::sync::Mutex::new(()),
        })
    }
    fn lock(&self) -> Result<std::sync::MutexGuard<'_, Connection>> {
        self.db
            .lock()
            .map_err(|_| TransferError::new("缓存数据库不可用"))
    }
    fn key(owner: &str, version: &str, hash_value: &str) -> String {
        hash(format!("{owner}\0{version}\0{hash_value}").as_bytes())
    }
    pub async fn set_quota(&self, quota: u64) -> Result<()> {
        let _guard = self.operation.lock().await;
        self.quota.store(quota, Ordering::SeqCst);
        self.evict(0).await
    }
    pub async fn prune(&self) -> Result<()> {
        let _guard = self.operation.lock().await;
        self.evict(0).await
    }
    async fn delete_key(&self, key: &str) -> Result<()> {
        match tokio::fs::remove_file(self.root.join(key)).await {
            Ok(()) => {}
            Err(e) if e.kind() == std::io::ErrorKind::NotFound => {}
            Err(e) => return Err(e.into()),
        }
        self.lock()?
            .execute("DELETE FROM chunks WHERE key=?1", [key])?;
        Ok(())
    }
    async fn evict(&self, reserve: u64) -> Result<()> {
        let now = chrono::Utc::now().timestamp();
        let entries: Vec<(String, u64, Option<i64>)> = {
            let db = self.lock()?;
            let mut statement =
                db.prepare("SELECT key,size,expires FROM chunks ORDER BY last_used,key")?;
            let rows = statement.query_map([], |r| Ok((r.get(0)?, r.get(1)?, r.get(2)?)))?;
            rows.collect::<std::result::Result<_, _>>()?
        };
        let mut total = entries.iter().map(|v| v.1).sum::<u64>();
        for (key, size, expiry) in entries {
            if expiry.is_some_and(|v| v <= now)
                || total.saturating_add(reserve) > self.quota.load(Ordering::SeqCst)
            {
                self.delete_key(&key).await?;
                total = total.saturating_sub(size);
            }
        }
        Ok(())
    }
    pub async fn store(
        &self,
        owner: &str,
        version: &str,
        chunk: &Chunk,
        bytes: &[u8],
        expires: Option<&str>,
    ) -> Result<bool> {
        if bytes.len() as u64 != chunk.size || hash(bytes) != chunk.hash {
            return Err(TransferError::new("分块校验失败"));
        }
        let _guard = self.operation.lock().await;
        if chunk.size > self.quota.load(Ordering::SeqCst) {
            return Ok(false);
        }
        let expiry = expires
            .map(|v| chrono::DateTime::parse_from_rfc3339(v).map(|d| d.timestamp()))
            .transpose()
            .map_err(|_| TransferError::new("缓存有效期无效"))?;
        if expiry.is_some_and(|v| v <= chrono::Utc::now().timestamp()) {
            return Err(TransferError::new("文件已过期"));
        }
        let key = Self::key(owner, version, &chunk.hash);
        self.delete_key(&key).await?;
        self.evict(chunk.size).await?;
        let temporary = self.root.join(format!("{}.part", uuid::Uuid::new_v4()));
        let mut file = tokio::fs::OpenOptions::new()
            .write(true)
            .create_new(true)
            .open(&temporary)
            .await?;
        use tokio::io::AsyncWriteExt;
        file.write_all(bytes).await?;
        file.sync_all().await?;
        drop(file);
        tokio::fs::rename(temporary, self.root.join(&key)).await?;
        self.lock()?.execute("INSERT INTO chunks(key,owner,version,size,last_used,expires) VALUES(?1,?2,?3,?4,?5,?6)",params![key,owner,version,chunk.size,chrono::Utc::now().timestamp_millis(),expiry])?;
        Ok(true)
    }
    pub async fn read(&self, owner: &str, version: &str, chunk: &Chunk) -> Result<Option<Vec<u8>>> {
        let _guard = self.operation.lock().await;
        let key = Self::key(owner, version, &chunk.hash);
        let entry: Option<Option<i64>> = self
            .lock()?
            .query_row("SELECT expires FROM chunks WHERE key=?1", [&key], |r| {
                r.get(0)
            })
            .optional()?;
        let Some(expiry) = entry else { return Ok(None) };
        if expiry.is_some_and(|v| v <= chrono::Utc::now().timestamp()) {
            self.delete_key(&key).await?;
            return Ok(None);
        }
        let path = self.root.join(&key);
        if tokio::fs::metadata(&path)
            .await
            .map(|m| m.len())
            .unwrap_or(u64::MAX)
            != chunk.size
        {
            self.delete_key(&key).await?;
            return Ok(None);
        }
        let bytes = tokio::fs::read(&path).await?;
        if hash(&bytes) != chunk.hash {
            self.delete_key(&key).await?;
            return Ok(None);
        }
        self.lock()?.execute(
            "UPDATE chunks SET last_used=?2 WHERE key=?1",
            params![key, chrono::Utc::now().timestamp_millis()],
        )?;
        Ok(Some(bytes))
    }
    /// Opens an independently verified stream. The caller must validate a current recipient grant first.
    pub async fn open_verified(
        &self,
        owner: &str,
        version: &str,
        chunk: &Chunk,
    ) -> Result<Option<std::io::Cursor<Vec<u8>>>> {
        Ok(self
            .read(owner, version, chunk)
            .await?
            .map(std::io::Cursor::new))
    }
    pub async fn revoke(&self, owner: &str, version: &str) -> Result<()> {
        let _guard = self.operation.lock().await;
        let keys: Vec<String> = {
            let db = self.lock()?;
            let mut statement =
                db.prepare("SELECT key FROM chunks WHERE owner=?1 AND version=?2")?;
            let rows = statement.query_map(params![owner, version], |r| r.get(0))?;
            rows.collect::<std::result::Result<_, _>>()?
        };
        for key in keys {
            self.delete_key(&key).await?;
        }
        Ok(())
    }
}
