use crate::{Result, TransferError};
use serde::Deserialize;
use serde_json::{json, Value};
use tokio::io::{AsyncRead, AsyncReadExt};

pub const MAX_REQUEST_BYTES: usize = 3 * 1024 * 1024;
pub const MAX_RESPONSE_BYTES: usize = 72 * 1024 * 1024;
#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
pub struct Request {
    pub id: u64,
    pub command: String,
    #[serde(default)]
    pub args: Value,
}
impl Request {
    pub fn parse(bytes: &[u8]) -> Result<Self> {
        if bytes.len() > MAX_REQUEST_BYTES {
            return Err(TransferError::new("请求超过限制"));
        }
        let request: Self = serde_json::from_slice(bytes)?;
        if request.id == 0
            || request.id > 9_007_199_254_740_991
            || request.command.len() > 80
            || !request
                .command
                .bytes()
                .all(|c| c.is_ascii_lowercase() || c == b'_')
            || !(request.args.is_null() || request.args.is_object())
        {
            return Err(TransferError::new("请求格式无效"));
        }
        Ok(request)
    }
}
pub async fn read_frame<R: AsyncRead + Unpin>(reader: &mut R) -> Result<Option<Vec<u8>>> {
    let mut header = [0; 4];
    match reader.read(&mut header[..1]).await? {
        0 => return Ok(None),
        1 => {}
        _ => unreachable!(),
    }
    reader.read_exact(&mut header[1..]).await?;
    let size = u32::from_le_bytes(header) as usize;
    if size == 0 || size > MAX_REQUEST_BYTES {
        return Err(TransferError::new("请求超过限制"));
    }
    let mut payload = vec![0; size];
    reader.read_exact(&mut payload).await?;
    Ok(Some(payload))
}
pub fn response(id: u64, result: Result<Value>) -> Value {
    match result {
        Ok(result) => json!({"id":id,"result":result}),
        Err(error) => json!({"id":id,"error":error}),
    }
}
#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn rejects_ambiguous_and_invalid_requests() {
        for input in [
            r#"{"id":1,"command":"api_request","path":"/etc/passwd"}"#,
            r#"{"id":0,"command":"login"}"#,
            r#"{"id":1,"command":"login","args":[]}"#,
            r#"{"id":1,"command":"../read"}"#,
        ] {
            assert!(Request::parse(input.as_bytes()).is_err());
        }
    }
    #[tokio::test]
    async fn refuses_declared_oversize_without_reading_payload() {
        let bytes = ((MAX_REQUEST_BYTES + 1) as u32).to_le_bytes();
        assert!(read_frame(&mut bytes.as_slice()).await.is_err());
        assert!(read_frame(&mut [1u8, 0].as_slice()).await.is_err());
        assert!(read_frame(&mut [].as_slice()).await.unwrap().is_none());
    }
}
