use crate::{
    transfer::models::{Chunk, MAX_CHUNK_SIZE},
    Result, TransferError,
};
use serde::{Deserialize, Serialize};

pub const MAX_FRAME: usize = 16 * 1024;
pub const PAYLOAD: usize = 12 * 1024;

#[derive(Clone, Debug, Serialize, Deserialize)]
#[serde(rename_all = "camelCase", deny_unknown_fields)]
pub struct Grant {
    pub user_id: String,
    pub device_id: String,
    pub session_id: String,
    pub version_id: String,
    pub chunk_hash: String,
    pub size: u64,
    pub expires_at: String,
    pub signature: String,
}
impl Grant {
    pub fn bind(&self, user: &str, device: &str, version: &str, chunk: &Chunk) -> Result<()> {
        if self.user_id != user
            || self.device_id != device
            || self.version_id != version
            || self.chunk_hash != chunk.hash
            || self.size != chunk.size
            || self.size > MAX_CHUNK_SIZE
            || self.session_id.is_empty()
            || self.signature.is_empty()
            || chrono::DateTime::parse_from_rfc3339(&self.expires_at)
                .map_or(true, |v| v <= chrono::Utc::now())
        {
            return Err(TransferError::new("对等传输授权与请求不匹配"));
        }
        Ok(())
    }
}
#[derive(Clone, Debug, Serialize, Deserialize)]
#[serde(
    tag = "type",
    rename_all = "camelCase",
    rename_all_fields = "camelCase",
    deny_unknown_fields
)]
pub enum Frame {
    Request {
        task_id: String,
        request_id: String,
        version_id: String,
        index: usize,
        hash: String,
        grant: Grant,
    },
    Data {
        request_id: String,
        version_id: String,
        index: usize,
        hash: String,
        offset: u64,
        end: bool,
    },
    Cancel {
        request_id: String,
    },
    Unavailable {
        request_id: String,
    },
}
impl Frame {
    pub fn request_id(&self) -> &str {
        match self {
            Self::Request { request_id, .. }
            | Self::Data { request_id, .. }
            | Self::Cancel { request_id }
            | Self::Unavailable { request_id } => request_id,
        }
    }
}
pub fn encode(frame: &Frame, payload: &[u8]) -> Result<Vec<u8>> {
    let header = serde_json::to_vec(frame)?;
    if header.len() > 4096
        || header.len() + 4 + payload.len() > MAX_FRAME
        || (!matches!(frame, Frame::Data { .. }) && !payload.is_empty())
    {
        return Err(TransferError::new("对等传输帧超出允许范围"));
    }
    let mut bytes = Vec::with_capacity(4 + header.len() + payload.len());
    bytes.extend_from_slice(&(header.len() as u32).to_be_bytes());
    bytes.extend_from_slice(&header);
    bytes.extend_from_slice(payload);
    Ok(bytes)
}
pub fn decode(bytes: &[u8]) -> Result<(Frame, Vec<u8>)> {
    if bytes.len() < 4 || bytes.len() > MAX_FRAME {
        return Err(TransferError::new("对等传输帧无效"));
    }
    let length = u32::from_be_bytes(bytes[..4].try_into().unwrap()) as usize;
    if length > 4096 || length > bytes.len() - 4 {
        return Err(TransferError::new("对等传输帧头无效"));
    }
    let frame: Frame = serde_json::from_slice(&bytes[4..4 + length])?;
    let payload = &bytes[4 + length..];
    if !matches!(frame, Frame::Data { .. }) && !payload.is_empty() {
        return Err(TransferError::new("对等控制帧含数据"));
    }
    Ok((frame, payload.to_vec()))
}
pub struct Assembly {
    request: String,
    version: String,
    chunk: Chunk,
    bytes: Vec<u8>,
    complete: bool,
}
impl Assembly {
    pub fn new(request: String, version: String, chunk: Chunk) -> Result<Self> {
        if chunk.size == 0 || chunk.size > MAX_CHUNK_SIZE {
            return Err(TransferError::new("对等分块大小无效"));
        }
        Ok(Self {
            request,
            version,
            chunk,
            bytes: Vec::new(),
            complete: false,
        })
    }
    pub fn push(&mut self, frame: &Frame, payload: &[u8]) -> Result<Option<Vec<u8>>> {
        let Frame::Data {
            request_id,
            version_id,
            index,
            hash,
            offset,
            end,
        } = frame
        else {
            return Err(TransferError::new("预期对等数据帧"));
        };
        if self.complete
            || request_id != &self.request
            || version_id != &self.version
            || *index != self.chunk.index
            || hash != &self.chunk.hash
            || *offset != self.bytes.len() as u64
            || payload.is_empty()
            || payload.len() > PAYLOAD
            || self.bytes.len() as u64 + payload.len() as u64 > self.chunk.size
        {
            return Err(TransferError::new("对等分块顺序或身份无效"));
        }
        self.bytes.extend_from_slice(payload);
        if *end {
            if self.bytes.len() as u64 != self.chunk.size {
                return Err(TransferError::new("对等分块不完整"));
            }
            self.complete = true;
            return Ok(Some(std::mem::take(&mut self.bytes)));
        }
        if self.bytes.len() as u64 == self.chunk.size {
            return Err(TransferError::new("对等分块未终止"));
        }
        Ok(None)
    }
}
