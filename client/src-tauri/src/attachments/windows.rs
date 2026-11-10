use super::*;
use std::os::windows::ffi::OsStringExt;
use windows::{
    core::w,
    Win32::{
        Foundation::HGLOBAL,
        System::{DataExchange::*, Memory::*},
        UI::Shell::{DragQueryFileW, HDROP},
    },
};
struct ClipboardGuard;
impl Drop for ClipboardGuard {
    fn drop(&mut self) {
        unsafe {
            let _ = CloseClipboard();
        }
    }
}
unsafe fn copy_format(format: u32) -> Result<Vec<u8>> {
    let handle = GetClipboardData(format).map_err(|_| image_error())?;
    let global = HGLOBAL(handle.0);
    let size = GlobalSize(global);
    if size == 0 || size > MAX_IMAGE_BYTES {
        return Err(image_error());
    }
    let pointer = GlobalLock(global);
    if pointer.is_null() {
        return Err(image_error());
    }
    let bytes = std::slice::from_raw_parts(pointer as *const u8, size).to_vec();
    let _ = GlobalUnlock(global);
    Ok(bytes)
}
pub(crate) fn read_clipboard() -> Result<ClipboardContent> {
    unsafe {
        let mut opened = false;
        for _ in 0..4 {
            if OpenClipboard(None).is_ok() {
                opened = true;
                break;
            }
            std::thread::sleep(std::time::Duration::from_millis(15));
        }
        if !opened {
            return Err(TransferError::new("剪贴板正被使用，请稍后再试或选择文件"));
        }
        let guard = ClipboardGuard;
        if IsClipboardFormatAvailable(15).is_ok() {
            let handle =
                GetClipboardData(15).map_err(|_| TransferError::new("无法读取复制的文件"))?;
            let drop = HDROP(handle.0);
            let count = DragQueryFileW(drop, u32::MAX, None);
            if count > 20 {
                return Err(TransferError::new("一次最多粘贴20个文件"));
            }
            let mut paths = Vec::new();
            for i in 0..count {
                let length = DragQueryFileW(drop, i, None) as usize;
                if length == 0 || length > 32767 {
                    return Err(TransferError::new("剪贴板文件路径无效"));
                }
                let mut path = vec![0u16; length + 1];
                if DragQueryFileW(drop, i, Some(&mut path)) as usize != length {
                    return Err(TransferError::new("剪贴板文件路径不完整"));
                }
                path.truncate(length);
                paths.push(PathBuf::from(std::ffi::OsString::from_wide(&path)));
            }
            return Ok(ClipboardContent::Files(paths));
        }
        let png = RegisterClipboardFormatW(w!("PNG"));
        if png != 0 && IsClipboardFormatAvailable(png).is_ok() {
            let data = copy_format(png)?;
            drop(guard);
            return Ok(ClipboardContent::Image(data));
        }
        for format in [17, 8] {
            if IsClipboardFormatAvailable(format).is_ok() {
                let data = copy_format(format)?;
                drop(guard);
                return Ok(ClipboardContent::Image(dib_to_png(&data)?));
            }
        }
        Ok(ClipboardContent::Empty)
    }
}
