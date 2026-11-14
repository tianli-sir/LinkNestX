//! At-rest protection for the persisted login file. The modern Windows shell
//! seals it with DPAPI (user-scoped, no UI prompt); the legacy Win7 shell and
//! macOS fall back to plaintext behind owner-only file permissions, matching
//! how the device file is already treated.

#[cfg(all(windows, not(feature = "legacy-host")))]
mod platform {
    use crate::{Result, TransferError};
    use windows::core::PCWSTR;
    use windows::Win32::Foundation::{HLOCAL, LocalFree};
    use windows::Win32::Security::Cryptography::{
        CRYPTPROTECT_UI_FORBIDDEN, CRYPT_INTEGER_BLOB, CryptProtectData, CryptUnprotectData,
    };

    fn blob(bytes: &[u8]) -> CRYPT_INTEGER_BLOB {
        CRYPT_INTEGER_BLOB {
            cbData: bytes.len() as u32,
            pbData: bytes.as_ptr() as *mut u8,
        }
    }

    /// DPAPI allocates the output buffer with LocalAlloc; take copies ownership.
    fn take(output: &mut CRYPT_INTEGER_BLOB) -> Vec<u8> {
        if output.pbData.is_null() || output.cbData == 0 {
            return Vec::new();
        }
        unsafe {
            let bytes =
                std::slice::from_raw_parts(output.pbData, output.cbData as usize).to_vec();
            let _ = LocalFree(Some(HLOCAL(output.pbData as *mut _)));
            *output = CRYPT_INTEGER_BLOB::default();
            bytes
        }
    }

    pub fn protect(data: &[u8]) -> Result<Vec<u8>> {
        unsafe {
            let mut output = CRYPT_INTEGER_BLOB::default();
            CryptProtectData(
                &blob(data),
                PCWSTR::null(),
                None,
                None,
                None,
                CRYPTPROTECT_UI_FORBIDDEN,
                &mut output,
            )
            .map_err(|_| TransferError::new("无法加密本地登录状态"))?;
            Ok(take(&mut output))
        }
    }

    pub fn unprotect(data: &[u8]) -> Result<Vec<u8>> {
        unsafe {
            let mut output = CRYPT_INTEGER_BLOB::default();
            let mut description = windows::core::PWSTR::null();
            CryptUnprotectData(
                &blob(data),
                Some(&mut description),
                None,
                None,
                None,
                CRYPTPROTECT_UI_FORBIDDEN,
                &mut output,
            )
            .map_err(|_| TransferError::new("无法读取本地登录状态"))?;
            if !description.is_null() {
                let _ = LocalFree(Some(HLOCAL(description.as_ptr() as *mut _)));
            }
            Ok(take(&mut output))
        }
    }
}

#[cfg(any(not(windows), feature = "legacy-host"))]
mod platform {
    use crate::Result;
    pub fn protect(data: &[u8]) -> Result<Vec<u8>> {
        Ok(data.to_vec())
    }
    pub fn unprotect(data: &[u8]) -> Result<Vec<u8>> {
        Ok(data.to_vec())
    }
}

pub use platform::{protect, unprotect};
