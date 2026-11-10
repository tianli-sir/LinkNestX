//! Explorer file lists use predefined CF_HDROP, which cannot be read reliably by
//! registering a string clipboard format in Electron. Read it through Win7 APIs.
use crate::Result;
use std::path::PathBuf;

#[cfg(not(windows))]
pub fn files() -> Result<Vec<PathBuf>> {
    Ok(Vec::new())
}

#[cfg(windows)]
pub fn files() -> Result<Vec<PathBuf>> {
    use crate::TransferError;
    use std::{
        ffi::{c_void, OsString},
        os::windows::ffi::OsStringExt,
    };
    #[link(name = "user32")]
    unsafe extern "system" {
        fn OpenClipboard(owner: *mut c_void) -> i32;
        fn CloseClipboard() -> i32;
        fn IsClipboardFormatAvailable(format: u32) -> i32;
        fn GetClipboardData(format: u32) -> *mut c_void;
    }
    #[link(name = "shell32")]
    unsafe extern "system" {
        fn DragQueryFileW(drop: *mut c_void, index: u32, name: *mut u16, length: u32) -> u32;
    }
    struct Guard;
    impl Drop for Guard {
        fn drop(&mut self) {
            unsafe {
                CloseClipboard();
            }
        }
    }
    unsafe {
        if IsClipboardFormatAvailable(15) == 0 {
            return Ok(Vec::new());
        }
        if OpenClipboard(std::ptr::null_mut()) == 0 {
            return Err(TransferError::new("剪贴板正在使用，请重试粘贴"));
        }
        let _guard = Guard;
        let drop = GetClipboardData(15);
        if drop.is_null() {
            return Err(TransferError::new("无法读取剪贴板文件"));
        }
        let count = DragQueryFileW(drop, u32::MAX, std::ptr::null_mut(), 0);
        if count > 20 {
            return Err(TransferError::new("每次最多粘贴 20 个文件"));
        }
        let mut paths = Vec::with_capacity(count as usize);
        for index in 0..count {
            let length = DragQueryFileW(drop, index, std::ptr::null_mut(), 0);
            if length == 0 || length > 32767 {
                return Err(TransferError::new("剪贴板文件路径无效"));
            }
            let mut buffer = vec![0u16; length as usize + 1];
            if DragQueryFileW(drop, index, buffer.as_mut_ptr(), buffer.len() as u32) != length {
                return Err(TransferError::new("剪贴板文件路径不完整"));
            }
            buffer.truncate(length as usize);
            let path = PathBuf::from(OsString::from_wide(&buffer));
            if !path.is_absolute() || path.as_os_str().to_string_lossy().starts_with("\\\\") {
                return Err(TransferError::new("请选择本机普通文件"));
            }
            paths.push(path);
        }
        Ok(paths)
    }
}
