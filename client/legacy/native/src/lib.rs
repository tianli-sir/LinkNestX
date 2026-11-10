//! Win7-compatible, window-system-independent host for the shared transfer engine.
//! Only the desktop main process owns the inherited pipe; no network IPC listener.
pub mod runtime {
    pub use tokio::{spawn, task::JoinHandle};
}
#[path = "../../../src-tauri/src/activity.rs"]
pub mod activity;
#[path = "../../../src-tauri/src/attachments.rs"]
pub mod attachments;
#[path = "../../../src-tauri/src/audit_export.rs"]
pub mod audit_export;
#[path = "../../../src-tauri/src/auth/mod.rs"]
pub mod auth;
mod clipboard;
#[path = "../../../src-tauri/src/deployment.rs"]
pub mod deployment;
pub mod events;
pub mod host;
#[path = "../../../src-tauri/src/peer/mod.rs"]
pub mod peer;
pub mod protocol;
#[path = "../../../src-tauri/src/transfer/mod.rs"]
pub mod transfer;

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
        self.error.fmt(f)
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
        Self::new("请求或响应格式无效")
    }
}
impl From<reqwest::Error> for TransferError {
    fn from(_: reqwest::Error) -> Self {
        Self::new("无法连接服务器，请检查网络")
    }
}
