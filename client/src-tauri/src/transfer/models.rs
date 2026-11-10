use crate::{Result, TransferError};
use serde::{Deserialize, Serialize};
use sha2::{Digest, Sha256};
use std::path::Path;

pub const MAX_FILE_SIZE: u64 = 20_000_000_000;
pub const DEFAULT_CHUNK_SIZE: u64 = 4_194_304;
pub const MAX_CHUNK_SIZE: u64 = 16_777_216;
pub fn hash(bytes: &[u8]) -> String {
    format!("{:x}", Sha256::digest(bytes))
}
pub fn valid_hash(value: &str) -> bool {
    value.len() == 64
        && value
            .bytes()
            .all(|b| b.is_ascii_digit() || (b'a'..=b'f').contains(&b))
}
pub fn safe_name(value: &str) -> bool {
    !value.is_empty()
        && value.len() <= 255
        && value != "."
        && value != ".."
        && !value.ends_with([' ', '.'])
        && !value
            .chars()
            .any(|c| c.is_control() || "/\\:*?\"<>|".contains(c))
        && !matches!(
            value
                .split('.')
                .next()
                .unwrap_or("")
                .to_ascii_uppercase()
                .as_str(),
            "CON"
                | "PRN"
                | "AUX"
                | "NUL"
                | "COM1"
                | "COM2"
                | "COM3"
                | "COM4"
                | "COM5"
                | "COM6"
                | "COM7"
                | "COM8"
                | "COM9"
                | "LPT1"
                | "LPT2"
                | "LPT3"
                | "LPT4"
                | "LPT5"
                | "LPT6"
                | "LPT7"
                | "LPT8"
                | "LPT9"
        )
}
pub fn display_name(value: &str) -> bool {
    !value.is_empty() && value.len() <= 1024 && !value.chars().any(|c| c == '\0')
}
pub fn suggested_name(value: &str) -> String {
    let mut name: String = value
        .chars()
        .map(|c| {
            if c.is_control() || "/\\:*?\"<>|".contains(c) {
                '_'
            } else {
                c
            }
        })
        .collect();
    while name.len() > 200 {
        name.pop();
    }
    name = name.trim_end_matches([' ', '.']).to_string();
    if !safe_name(&name) {
        "下载文件".into()
    } else {
        name
    }
}
pub fn segment(value: &str) -> Result<&str> {
    if value.is_empty()
        || value.len() > 128
        || !value
            .bytes()
            .all(|b| b.is_ascii_alphanumeric() || b == b'-' || b == b'_')
    {
        Err(TransferError::new("标识符无效"))
    } else {
        Ok(value)
    }
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq, Eq)]
#[serde(rename_all = "camelCase")]
pub struct Chunk {
    pub index: usize,
    pub hash: String,
    pub size: u64,
}
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct Manifest {
    pub file_id: String,
    pub version_id: String,
    pub name: String,
    pub size: u64,
    pub sha256: String,
    pub chunk_size: u64,
    pub chunks: Vec<Chunk>,
    #[serde(default)]
    pub expires_at: Option<String>,
    #[serde(default)]
    pub visibility: String,
    #[serde(default)]
    pub sharing_policy: AdminSharingPolicy,
}
impl Manifest {
    pub fn validate(&self) -> Result<()> {
        segment(&self.version_id)?;
        segment(&self.file_id)?;
        if !display_name(&self.name)
            || self.size > MAX_FILE_SIZE
            || !valid_hash(&self.sha256)
            || self.chunk_size == 0
            || self.chunk_size > MAX_CHUNK_SIZE
        {
            return Err(TransferError::new("文件清单无效"));
        }
        let count = self.size.div_ceil(self.chunk_size);
        if count as usize != self.chunks.len() || count > 1_000_000 {
            return Err(TransferError::new("分块数量无效"));
        }
        for (i, c) in self.chunks.iter().enumerate() {
            if c.index != i
                || !valid_hash(&c.hash)
                || c.size != self.chunk_size.min(self.size - i as u64 * self.chunk_size)
            {
                return Err(TransferError::new("分块清单无效"));
            }
        }
        if self.size == 0 && self.sha256 != hash(b"") {
            return Err(TransferError::new("空文件校验无效"));
        }
        if let Some(expiry) = &self.expires_at {
            let expiry = chrono::DateTime::parse_from_rfc3339(expiry)
                .map_err(|_| TransferError::new("文件有效期无效"))?;
            if expiry <= chrono::Utc::now() {
                return Err(TransferError::new("文件已过期"));
            }
        }
        Ok(())
    }
    pub fn immutable_eq(&self, other: &Self) -> bool {
        self.version_id == other.version_id
            && self.file_id == other.file_id
            && self.size == other.size
            && self.sha256 == other.sha256
            && self.chunk_size == other.chunk_size
            && self.chunks == other.chunks
    }
}
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq, Eq)]
#[serde(rename_all = "lowercase")]
pub enum TaskState {
    Queued,
    Hashing,
    Transferring,
    Paused,
    Verifying,
    Completed,
    Failed,
}
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq, Eq)]
#[serde(rename_all = "lowercase")]
pub enum Direction {
    Upload,
    Download,
}
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct TaskSnapshot {
    pub id: String,
    pub direction: Direction,
    pub version_id: String,
    pub name: String,
    pub state: TaskState,
    pub bytes_completed: u64,
    pub total_bytes: u64,
    pub active_sources: Vec<String>,
    #[serde(default)]
    pub group_id: Option<String>,
    #[serde(default)]
    pub client_attachment_id: Option<String>,
    #[serde(default)]
    pub message_id: Option<String>,
    #[serde(default)]
    pub metrics: super::metrics::Snapshot,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub error: Option<String>,
}
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct Task {
    pub snapshot: TaskSnapshot,
    pub owner: String,
    pub path: String,
    pub partial: Option<String>,
    pub manifest: Option<Manifest>,
    pub verified: Vec<bool>,
    pub group_id: Option<String>,
    pub file_id: Option<String>,
    pub upload_id: Option<String>,
    #[serde(default)]
    pub resume_on_login: bool,
    #[serde(default)]
    pub metrics_record: super::metrics::Record,
    #[serde(default)]
    pub owned_source: bool,
}
impl Task {
    pub fn new(owner: String, direction: Direction, version_id: String, path: String) -> Self {
        let name = Path::new(&path)
            .file_name()
            .and_then(|v| v.to_str())
            .unwrap_or("文件")
            .to_string();
        let metrics = super::metrics::Snapshot {
            history_complete: true,
            ..Default::default()
        };
        Self {
            snapshot: TaskSnapshot {
                id: uuid::Uuid::new_v4().to_string(),
                direction,
                version_id,
                name,
                state: TaskState::Queued,
                bytes_completed: 0,
                total_bytes: 0,
                active_sources: Vec::new(),
                group_id: None,
                client_attachment_id: None,
                message_id: None,
                metrics: metrics.clone(),
                error: None,
            },
            owner,
            path,
            partial: None,
            manifest: None,
            verified: Vec::new(),
            group_id: None,
            file_id: None,
            upload_id: None,
            resume_on_login: false,
            metrics_record: super::metrics::Record::fresh(metrics),
            owned_source: false,
        }
    }
}
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(rename_all = "camelCase", default)]
pub struct SharingPolicy {
    pub enabled: bool,
    pub cache_quota_bytes: u64,
    pub max_concurrent: usize,
    pub bandwidth_limit_bps: u64,
    pub autostart: bool,
}
impl Default for SharingPolicy {
    fn default() -> Self {
        Self {
            enabled: true,
            cache_quota_bytes: 10_000_000_000,
            max_concurrent: 3,
            bandwidth_limit_bps: 0,
            autostart: true,
        }
    }
}
impl SharingPolicy {
    pub fn validate(&self) -> Result<()> {
        if !self.enabled
            || self.cache_quota_bytes < MAX_CHUNK_SIZE
            || self.max_concurrent == 0
            || self.max_concurrent > 16
            || self.cache_quota_bytes > 100_000_000_000
        {
            Err(TransferError::new("传输设置超出允许范围"))
        } else {
            Ok(())
        }
    }
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq, Eq)]
#[serde(rename_all = "camelCase")]
pub struct AdminSharingPolicy {
    pub enabled: bool,
    pub max_concurrent: usize,
    pub bandwidth_limit_bps: u64,
}
impl Default for AdminSharingPolicy {
    fn default() -> Self {
        Self {
            enabled: true,
            max_concurrent: 0,
            bandwidth_limit_bps: 0,
        }
    }
}
impl AdminSharingPolicy {
    pub fn effective(&self, local: &SharingPolicy) -> SharingPolicy {
        let mut p = local.clone();
        p.enabled &= self.enabled;
        if self.max_concurrent > 0 {
            p.max_concurrent = p.max_concurrent.min(self.max_concurrent)
        };
        if self.bandwidth_limit_bps > 0 {
            p.bandwidth_limit_bps = if p.bandwidth_limit_bps == 0 {
                self.bandwidth_limit_bps
            } else {
                p.bandwidth_limit_bps.min(self.bandwidth_limit_bps)
            }
        };
        p
    }
}
