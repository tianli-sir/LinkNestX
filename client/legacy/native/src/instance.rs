// Kept alive for the complete daemon lifetime so an installer cannot replace
// executables while background transfers are still running.
#[cfg(windows)]
pub struct InstallGuard(*mut std::ffi::c_void);
#[cfg(windows)]
#[link(name = "kernel32")]
unsafe extern "system" {
    fn CreateMutexW(
        attributes: *const std::ffi::c_void,
        owner: i32,
        name: *const u16,
    ) -> *mut std::ffi::c_void;
    fn CloseHandle(handle: *mut std::ffi::c_void) -> i32;
}
#[cfg(windows)]
impl InstallGuard {
    pub fn acquire() -> std::io::Result<Self> {
        let name: Vec<u16> = "Local\\LinkNestXLegacyRunning\0".encode_utf16().collect();
        let handle = unsafe { CreateMutexW(std::ptr::null(), 0, name.as_ptr()) };
        if handle.is_null() {
            Err(std::io::Error::last_os_error())
        } else {
            Ok(Self(handle))
        }
    }
}
#[cfg(windows)]
impl Drop for InstallGuard {
    fn drop(&mut self) {
        unsafe {
            CloseHandle(self.0);
        }
    }
}
#[cfg(not(windows))]
pub struct InstallGuard;
#[cfg(not(windows))]
impl InstallGuard {
    pub fn acquire() -> std::io::Result<Self> {
        Ok(Self)
    }
}
