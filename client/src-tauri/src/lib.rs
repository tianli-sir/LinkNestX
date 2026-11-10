pub mod audit_export;
pub mod auth;
pub mod autostart;
pub mod commands;
pub mod events;
pub mod peer;
pub mod transfer;
pub mod tray;

pub type Result<T> = std::result::Result<T, TransferError>;

#[derive(Debug, Clone, serde::Serialize)]
pub struct TransferError {
    pub error: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub status: Option<u16>,
}
impl TransferError {
    pub fn new(message: impl Into<String>) -> Self {
        Self {
            error: message.into(),
            status: None,
        }
    }
    pub fn http(status: u16) -> Self {
        Self {
            error: format!("服务器请求失败（{status}）"),
            status: Some(status),
        }
    }
}
impl std::fmt::Display for TransferError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        write!(f, "{}", self.error)
    }
}
impl std::error::Error for TransferError {}
impl From<std::io::Error> for TransferError {
    fn from(_: std::io::Error) -> Self {
        Self::new("无法读取或写入本地文件")
    }
}
impl From<rusqlite::Error> for TransferError {
    fn from(_: rusqlite::Error) -> Self {
        Self::new("无法保存本地传输状态")
    }
}
impl From<serde_json::Error> for TransferError {
    fn from(_: serde_json::Error) -> Self {
        Self::new("服务器或本地数据格式无效")
    }
}
impl From<reqwest::Error> for TransferError {
    fn from(_: reqwest::Error) -> Self {
        Self::new("无法连接服务器，请检查网络")
    }
}

pub mod background;
mod notifications;

pub mod activity;
pub mod attachments;
pub mod deployment;

pub mod update;

/// Host scheduling boundary shared with the standalone transfer service.
pub mod runtime {
    pub use tauri::async_runtime::{spawn, JoinHandle};
}

pub mod external;
