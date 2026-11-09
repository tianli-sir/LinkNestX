use crate::{Result, TransferError};
use tauri_plugin_autostart::ManagerExt;
pub fn set(app: &tauri::AppHandle, enabled: bool) -> Result<()> {
    let manager = app.autolaunch();
    let result = if enabled {
        manager.enable()
    } else {
        manager.disable()
    };
    result.map_err(|_| TransferError::new("无法更改系统登录启动设置，请检查系统权限"))
}
