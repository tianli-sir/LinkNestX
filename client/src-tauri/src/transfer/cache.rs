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
    changes: tokio::sync::watch::Sender<u64>,
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
        db.execute_batch("CREATE TABLE IF NOT EXISTS seed_chunks(key TEXT NOT NULL,chunk_index INTEGER NOT NULL,chunk_hash TEXT NOT NULL,PRIMARY KEY(key,chunk_index));")?;
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
            changes: tokio::sync::watch::channel(0).0,
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
        let db = self.lock()?;
        db.execute("DELETE FROM seed_chunks WHERE key=?1", [key])?;
        if db.execute("DELETE FROM chunks WHERE key=?1", [key])? > 0 {
            self.changes
                .send_modify(|revision| *revision = revision.wrapping_add(1));
        }
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
        // A content hash may occupy several manifest indexes. Preserve their aliases.
        let aliases: Vec<usize> = {
            let db = self.lock()?;
            let mut statement = db.prepare("SELECT chunk_index FROM seed_chunks WHERE key=?1")?;
            let rows = statement.query_map([&key], |r| r.get(0))?;
            rows.collect::<std::result::Result<_, _>>()?
        };
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
        self.lock()?.execute(
            "INSERT OR REPLACE INTO seed_chunks(key,chunk_index,chunk_hash) VALUES(?1,?2,?3)",
            params![key, chunk.index, chunk.hash],
        )?;
        for index in aliases {
            self.lock()?.execute(
                "INSERT OR IGNORE INTO seed_chunks(key,chunk_index,chunk_hash) VALUES(?1,?2,?3)",
                params![key, index, chunk.hash],
            )?;
        }
        self.changes
            .send_modify(|revision| *revision = revision.wrapping_add(1));
        Ok(true)
    }
    pub async fn refresh_expiry(
        &self,
        owner: &str,
        version: &str,
        expires: Option<&str>,
    ) -> Result<()> {
        let expiry = expires
            .map(|v| chrono::DateTime::parse_from_rfc3339(v).map(|d| d.timestamp()))
            .transpose()
            .map_err(|_| TransferError::new("缓存有效期无效"))?;
        let _guard = self.operation.lock().await;
        self.lock()?.execute(
            "UPDATE chunks SET expires=?3 WHERE owner=?1 AND version=?2",
            params![owner, version, expiry],
        )?;
        Ok(())
    }
    pub fn is_expired(&self, owner: &str, version: &str) -> Result<bool> {
        Ok(self.lock()?.query_row("SELECT EXISTS(SELECT 1 FROM chunks WHERE owner=?1 AND version=?2 AND expires IS NOT NULL AND expires<=unixepoch())",params![owner,version],|r|r.get(0))?)
    }
    pub(crate) fn expired_versions(&self) -> Result<Vec<(String, String)>> {
        let db = self.lock()?;
        let mut statement = db.prepare("SELECT DISTINCT owner,version FROM chunks WHERE expires IS NOT NULL AND expires<=unixepoch()")?;
        let rows = statement.query_map([], |r| Ok((r.get(0)?, r.get(1)?)))?;
        Ok(rows.collect::<std::result::Result<_, _>>()?)
    }
    /// Maintenance expiry never evicts still-valid bytes to satisfy the quota.
    pub(crate) async fn prune_expired(&self, owner: &str, version: &str) -> Result<()> {
        let _guard = self.operation.lock().await;
        let keys: Vec<String> = {
            let db = self.lock()?;
            let mut statement = db.prepare("SELECT key FROM chunks WHERE owner=?1 AND version=?2 AND expires IS NOT NULL AND expires<=unixepoch()")?;
            let rows = statement.query_map(params![owner, version], |r| r.get(0))?;
            rows.collect::<std::result::Result<_, _>>()?
        };
        for key in keys {
            self.delete_key(&key).await?;
        }
        Ok(())
    }
    pub fn changed(&self) {
        self.changes.send_modify(|v| *v = v.wrapping_add(1));
    }
    pub fn versions(&self, owner: &str) -> Result<Vec<String>> {
        let db = self.lock()?;
        let mut statement = db.prepare("SELECT DISTINCT version FROM chunks WHERE owner=?1")?;
        let rows = statement.query_map([owner], |r| r.get(0))?;
        Ok(rows.collect::<std::result::Result<_, _>>()?)
    }
    /// Upgrades pre-swarm cache metadata only after authoritative manifest and disk verification.
    pub async fn recover(&self, owner: &str, manifest: &super::models::Manifest) -> Result<()> {
        for chunk in &manifest.chunks {
            if self
                .read(owner, &manifest.version_id, chunk)
                .await?
                .is_some()
            {
                let key = Self::key(owner, &manifest.version_id, &chunk.hash);
                self.lock()?.execute("INSERT OR IGNORE INTO seed_chunks(key,chunk_index,chunk_hash) VALUES(?1,?2,?3)",params![key,chunk.index,chunk.hash])?;
            }
        }
        Ok(())
    }
    pub fn subscribe(&self) -> tokio::sync::watch::Receiver<u64> {
        self.changes.subscribe()
    }
    /// Revalidate persisted bytes before advertising them, including after a restart.
    pub fn inventory(&self, owner: &str) -> Result<Vec<(String, Chunk)>> {
        let entries: Vec<(String, Chunk)> = {
            let db = self.lock()?;
            let mut statement = db.prepare("SELECT c.version,s.chunk_index,s.chunk_hash,c.size FROM chunks c JOIN seed_chunks s ON s.key=c.key WHERE c.owner=?1 AND (c.expires IS NULL OR c.expires>unixepoch()) ORDER BY c.version,s.chunk_index")?;
            let rows = statement.query_map([owner], |r| {
                Ok((
                    r.get(0)?,
                    Chunk {
                        index: r.get(1)?,
                        hash: r.get(2)?,
                        size: r.get(3)?,
                    },
                ))
            })?;
            rows.collect::<std::result::Result<_, _>>()?
        };
        Ok(entries)
    }
    pub async fn catalog(&self, owner: &str) -> Result<Vec<(String, Chunk)>> {
        let entries = self.inventory(owner)?;
        let mut verified = Vec::new();
        for (version, chunk) in entries {
            if self.read(owner, &version, &chunk).await?.is_some() {
                verified.push((version, chunk));
            }
        }
        Ok(verified)
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
