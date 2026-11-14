use crate::{Result, TransferError};
use base64::{engine::general_purpose::URL_SAFE_NO_PAD as B64, Engine};
use serde::{Deserialize, Serialize};
use sha2::{Digest, Sha256};
use std::collections::BTreeSet;
pub const MAX_ENVELOPE: usize = 8 * 1024 * 1024 + 1024;
pub const MAX_BYTES: u64 = 2 * 1024 * 1024 * 1024;
pub const BLOCK_BYTES: u64 = 262144;
pub const BUILD_SEQUENCE: u64 = 6;
pub fn bad(message: &str) -> TransferError {
    TransferError::new(format!("更新验证失败：{message}"))
}
pub fn hash(bytes: &[u8]) -> String {
    format!("{:x}", Sha256::digest(bytes))
}
#[derive(Deserialize)]
#[serde(rename_all = "camelCase", deny_unknown_fields)]
struct Envelope {
    key_id: String,
    manifest: String,
    signature: String,
}
#[derive(Deserialize)]
#[serde(rename_all = "camelCase", deny_unknown_fields)]
struct PublicKey {
    key_id: String,
    public_key: String,
}
#[derive(Debug, Clone, Deserialize, Serialize)]
#[serde(rename_all = "camelCase", deny_unknown_fields)]
pub struct Manifest {
    pub schema_version: u32,
    pub app_id: String,
    pub channel: String,
    pub platform: String,
    pub arch: String,
    pub version: String,
    pub build_sequence: u64,
    pub issued_at: String,
    pub expires_at: String,
    pub entry_point: String,
    pub total_bytes: u64,
    pub files: Vec<PayloadFile>,
}
#[derive(Debug, Clone, Deserialize, Serialize)]
#[serde(rename_all = "camelCase", deny_unknown_fields)]
pub struct PayloadFile {
    pub path: String,
    pub mode: u32,
    pub size: u64,
    pub sha256: String,
    pub blocks: Vec<Block>,
}
#[derive(Debug, Clone, Deserialize, Serialize)]
#[serde(rename_all = "camelCase", deny_unknown_fields)]
pub struct Block {
    pub sha256: String,
    pub size: u64,
    #[serde(default, deserialize_with = "present_string")]
    pub base_path: Option<String>,
    #[serde(default)]
    pub base_offset: u64,
}
fn present_string<'de, D: serde::Deserializer<'de>>(
    deserializer: D,
) -> std::result::Result<Option<String>, D::Error> {
    String::deserialize(deserializer).map(Some)
}
pub fn pinned_key() -> Result<Vec<u8>> {
    let k: PublicKey = serde_json::from_str(include_str!("../../../update-public-key.json"))?;
    let raw = B64.decode(k.public_key).map_err(|_| bad("内置公钥无效"))?;
    if raw.len() != 32 || hash(&raw)[..16] != k.key_id {
        return Err(bad("内置公钥标识无效"));
    }
    Ok(raw)
}
pub fn safe_path(path: &str) -> bool {
    if path.is_empty()
        || path.len() > 1024
        || path
            .chars()
            .any(|c| c.is_control() || "\\:\0<>\"|?*".contains(c))
    {
        return false;
    }
    path.split('/').all(|p| {
        if p.is_empty() || p == "." || p == ".." || p.ends_with(['.', ' ']) {
            return false;
        }
        let base = p.split('.').next().unwrap().to_ascii_uppercase();
        !matches!(base.as_str(), "CON" | "PRN" | "AUX" | "NUL")
            && !(base.len() == 4
                && (base.starts_with("COM") || base.starts_with("LPT"))
                && matches!(base.as_bytes()[3], b'1'..=b'9'))
    })
}
fn valid_hash(h: &str) -> bool {
    h.len() == 64
        && h.bytes()
            .all(|c| c.is_ascii_digit() || (b'a'..=b'f').contains(&c))
}
pub fn verify(raw: &[u8], key: &[u8], now: i64) -> Result<Manifest> {
    if raw.len() > MAX_ENVELOPE {
        return Err(bad("清单过大"));
    }
    let envelope: Envelope = serde_json::from_slice(raw)?;
    if key.len() != 32
        || envelope.key_id != hash(key)[..16]
        || envelope.manifest.len() > 8 * 1024 * 1024
    {
        return Err(bad("未知签名密钥"));
    }
    let bytes = B64
        .decode(envelope.manifest)
        .map_err(|_| bad("清单编码无效"))?;
    let signature = B64
        .decode(envelope.signature)
        .map_err(|_| bad("签名编码无效"))?;
    let mut signed = b"LinkNestX-update-manifest-v1\0".to_vec();
    signed.extend(&bytes);
    ring::signature::UnparsedPublicKey::new(&ring::signature::ED25519, key)
        .verify(&signed, &signature)
        .map_err(|_| bad("签名不匹配"))?;
    // Deserialize only after authentication. Derived struct visitors reject duplicate and unknown fields.
    let m: Manifest = serde_json::from_slice(&bytes)?;
    let valid_platform = (m.platform == "windows" && m.arch == "x86_64")
        || (m.platform == "macos" && matches!(m.arch.as_str(), "aarch64" | "x86_64"));
    if m.schema_version != 1
        || m.app_id != "com.linknestx.client"
        || m.channel != "stable"
        || !valid_platform
        || m.build_sequence == 0
        || semver::Version::parse(&m.version).is_err()
    {
        return Err(bad("发布身份无效"));
    }
    let issued = chrono::DateTime::parse_from_rfc3339(&m.issued_at)
        .map_err(|_| bad("发布时间无效"))?
        .timestamp();
    let expires = chrono::DateTime::parse_from_rfc3339(&m.expires_at)
        .map_err(|_| bad("到期时间无效"))?
        .timestamp();
    if issued > now + 300 || expires <= now || expires <= issued || expires - issued > 366 * 86400 {
        return Err(bad("清单已过期或时间无效"));
    }
    if !safe_path(&m.entry_point)
        || m.files.is_empty()
        || m.files.len() > 4096
        || m.total_bytes > MAX_BYTES
    {
        return Err(bad("文件树无效"));
    }
    let mut seen = BTreeSet::<String>::new();
    let (mut total, mut blocks) = (0u64, 0usize);
    let mut entry = false;
    for f in &m.files {
        let path = f.path.to_lowercase();
        if !safe_path(&f.path)
            || !valid_hash(&f.sha256)
            || f.size > MAX_BYTES
            || !matches!(f.mode, 420 | 493)
            || !seen.insert(path.clone())
        {
            return Err(bad("文件属性或重复路径无效"));
        }
        total = total
            .checked_add(f.size)
            .ok_or_else(|| bad("文件总长溢出"))?;
        if f.path == m.entry_point {
            entry = true;
            if m.platform == "macos" && f.mode != 493 {
                return Err(bad("主程序不可执行"));
            }
        }
        let mut size = 0u64;
        for (i, b) in f.blocks.iter().enumerate() {
            blocks += 1;
            if blocks > 16384
                || !valid_hash(&b.sha256)
                || b.size == 0
                || b.size > BLOCK_BYTES
                || (i + 1 < f.blocks.len() && b.size != BLOCK_BYTES)
                || b.base_offset > MAX_BYTES - b.size
                || b.base_path.as_ref().is_some_and(|p| !safe_path(p))
                || (b.base_path.is_none() && b.base_offset != 0)
            {
                return Err(bad("分块范围无效"));
            }
            size = size.checked_add(b.size).ok_or_else(|| bad("块长度溢出"))?;
        }
        if size != f.size {
            return Err(bad("块未完整覆盖文件"));
        }
    }
    for path in &seen {
        for (i, _) in path.match_indices('/') {
            if seen.contains(&path[..i]) {
                return Err(bad("文件与目录冲突"));
            }
        }
    }
    if !entry || total != m.total_bytes || total > MAX_BYTES {
        return Err(bad("总长度或入口无效"));
    }
    Ok(m)
}
pub fn eligible(
    m: &Manifest,
    platform: &str,
    arch: &str,
    current: &str,
    sequence: u64,
) -> Result<()> {
    if m.platform != platform || m.arch != arch {
        return Err(bad("平台不匹配"));
    }
    let target = semver::Version::parse(&m.version).map_err(|_| bad("版本无效"))?;
    let current = semver::Version::parse(current).map_err(|_| bad("当前版本无效"))?;
    if target.cmp_precedence(&current) != std::cmp::Ordering::Greater
        || m.build_sequence <= sequence
    {
        return Err(bad("拒绝旧版本或旧发布序号"));
    }
    Ok(())
}
/// Call after signature verification; unsigned JSON whitespace does not identify a release.
pub fn identity(envelope: &[u8]) -> Result<String> {
    let envelope: Envelope = serde_json::from_slice(envelope)?;
    let bytes = B64
        .decode(envelope.manifest)
        .map_err(|_| bad("清单编码无效"))?;
    Ok(hash(&bytes))
}
