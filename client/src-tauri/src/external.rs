use crate::{Result, TransferError};

pub fn checked_url(value: &str) -> Result<String> {
    if value.len() > 4096 || value.chars().any(char::is_control) || value.contains('\\') {
        return Err(TransferError::new("外部链接无效"));
    }
    let url = reqwest::Url::parse(value).map_err(|_| TransferError::new("外部链接无效"))?;
    if !matches!(url.scheme(), "http" | "https" | "mailto")
        || !url.username().is_empty()
        || url.password().is_some()
        || (url.scheme() != "mailto" && url.host_str().is_none())
    {
        return Err(TransferError::new("仅支持网页与邮件链接"));
    }
    if url.scheme() == "mailto"
        && (url.path().is_empty()
            || !url.path().split(',').all(|address| {
                address.contains('@')
                    && address
                        .bytes()
                        .all(|c| c.is_ascii_alphanumeric() || b"@._+-".contains(&c))
            })
            || url
                .query_pairs()
                .any(|(key, _)| !matches!(key.as_ref(), "subject" | "body")))
    {
        return Err(TransferError::new("邮件链接无效"));
    }
    Ok(url.into())
}

pub fn open(value: &str) -> Result<()> {
    let url = checked_url(value)?;
    #[cfg(target_os = "macos")]
    {
        let status = std::process::Command::new("/usr/bin/open")
            .arg(&url)
            .stdin(std::process::Stdio::null())
            .stdout(std::process::Stdio::null())
            .stderr(std::process::Stdio::null())
            .status()?;
        if status.success() {
            return Ok(());
        }
    }
    #[cfg(target_os = "windows")]
    {
        use windows::{
            core::PCWSTR,
            Win32::UI::{Shell::ShellExecuteW, WindowsAndMessaging::SW_SHOWNORMAL},
        };
        let verb: Vec<u16> = "open\0".encode_utf16().collect();
        let destination: Vec<u16> = url.encode_utf16().chain(std::iter::once(0)).collect();
        let result = unsafe {
            ShellExecuteW(
                None,
                PCWSTR(verb.as_ptr()),
                PCWSTR(destination.as_ptr()),
                None,
                None,
                SW_SHOWNORMAL,
            )
        };
        if result.0 as isize > 32 {
            return Ok(());
        }
    }
    Err(TransferError::new("无法打开外部链接"))
}

// Local-file launching is deliberately separate from checked_url, which must
// keep rejecting file paths. The only callers resolve absolute paths from the
// transfer task library, so the path itself is the whitelist.
pub fn open_local_path(path: &str) -> Result<()> {
    let target = std::path::Path::new(path);
    if path.is_empty() || !target.is_absolute() {
        return Err(TransferError::new("文件路径无效"));
    }
    #[cfg(target_os = "macos")]
    {
        let status = std::process::Command::new("/usr/bin/open")
            .arg(target)
            .stdin(std::process::Stdio::null())
            .stdout(std::process::Stdio::null())
            .stderr(std::process::Stdio::null())
            .status()?;
        if status.success() {
            return Ok(());
        }
    }
    #[cfg(target_os = "windows")]
    {
        use windows::{
            core::PCWSTR,
            Win32::UI::{Shell::ShellExecuteW, WindowsAndMessaging::SW_SHOWNORMAL},
        };
        let verb: Vec<u16> = "open\0".encode_utf16().collect();
        let destination: Vec<u16> = path.encode_utf16().chain(std::iter::once(0)).collect();
        let result = unsafe {
            ShellExecuteW(
                None,
                PCWSTR(verb.as_ptr()),
                PCWSTR(destination.as_ptr()),
                None,
                None,
                SW_SHOWNORMAL,
            )
        };
        if result.0 as isize > 32 {
            return Ok(());
        }
    }
    Err(TransferError::new("无法打开文件"))
}

pub fn reveal_local_path(path: &str) -> Result<()> {
    let target = std::path::Path::new(path);
    if path.is_empty() || !target.is_absolute() {
        return Err(TransferError::new("文件路径无效"));
    }
    #[cfg(target_os = "macos")]
    {
        let status = std::process::Command::new("/usr/bin/open")
            .arg("-R")
            .arg(target)
            .stdin(std::process::Stdio::null())
            .stdout(std::process::Stdio::null())
            .stderr(std::process::Stdio::null())
            .status()?;
        if status.success() {
            return Ok(());
        }
    }
    #[cfg(target_os = "windows")]
    {
        let status = std::process::Command::new("explorer.exe")
            .arg(format!("/select,{}", target.display()))
            .stdin(std::process::Stdio::null())
            .stdout(std::process::Stdio::null())
            .stderr(std::process::Stdio::null())
            .status()?;
        if status.success() {
            return Ok(());
        }
    }
    Err(TransferError::new("无法打开文件所在文件夹"))
}

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn local_launch_requires_absolute_paths() {
        assert!(open_local_path("readme.txt").is_err());
        assert!(reveal_local_path("readme.txt").is_err());
        assert!(open_local_path("").is_err());
        assert!(reveal_local_path("").is_err());
    }
    #[test]
    fn external_links_cannot_open_programs_files_or_credential_urls() {
        for value in [
            "javascript:alert(1)",
            "file:///private/a",
            "data:text/html,test",
            "ms-settings:display",
            "https://user:password@example.com",
            "https://example.com\nargument",
            "mailto:user@example.com?attach=/private/a",
            "mailto:user@example.com?attachment=file",
        ] {
            assert!(checked_url(value).is_err(), "{value}");
        }
        for value in [
            "https://example.com/path?q=notes",
            "http://192.168.1.10/guide",
            "mailto:team@example.com",
        ] {
            assert!(checked_url(value).is_ok());
        }
    }
}
