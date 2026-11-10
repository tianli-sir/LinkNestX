use super::manifest::{bad, Manifest};
use crate::Result;
use sha2::{Digest, Sha256};
use std::{
    fs::{self, File, OpenOptions},
    io::{Read, Write},
    path::{Path, PathBuf},
};
pub fn no_links(path: &Path) -> Result<()> {
    #[cfg(windows)]
    {
        let mut components = path.components();
        let disk_prefix = matches!(components.next(), Some(std::path::Component::Prefix(prefix))
            if matches!(prefix.kind(), std::path::Prefix::Disk(_) | std::path::Prefix::VerbatimDisk(_)));
        // Rust treats a bare verbatim disk prefix (\\?\C:) as absolute even
        // without a root separator. Require RootDir before deferring its check.
        if !path.is_absolute()
            || (disk_prefix && components.next() != Some(std::path::Component::RootDir))
        {
            return Err(bad("更新路径必须包含完整绝对根"));
        }
    }
    let mut part = PathBuf::new();
    for c in path.components() {
        part.push(c);
        #[cfg(windows)]
        if matches!(c, std::path::Component::Prefix(prefix)
            if matches!(prefix.kind(), std::path::Prefix::Disk(_) | std::path::Prefix::VerbatimDisk(_)))
        {
            // A disk prefix (especially canonical \\?\C:) is not a complete
            // filesystem path. Inspect it after RootDir is appended, then
            // inspect every following ancestor without canonicalizing links away.
            continue;
        }
        let meta = fs::symlink_metadata(&part)?;
        if meta.file_type().is_symlink() {
            return Err(bad("路径包含符号链接，请手动安装"));
        }
        #[cfg(windows)]
        {
            use std::os::windows::fs::MetadataExt;
            if meta.file_attributes() & 0x400 != 0 {
                return Err(bad("路径包含重解析点，请手动安装"));
            }
        }
    }
    Ok(())
}
pub fn directory(path: &Path) -> Result<()> {
    if path.exists() {
        no_links(path)?;
        if !path.is_dir() {
            return Err(bad("目录无效"));
        }
        return Ok(());
    }
    let parent = path.parent().ok_or_else(|| bad("缺少父目录"))?;
    directory(parent)?;
    fs::create_dir(path)?;
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        fs::set_permissions(path, fs::Permissions::from_mode(0o700))?;
    }
    no_links(path)
}
pub fn regular(path: &Path) -> Result<File> {
    no_links(path)?;
    let mut options = OpenOptions::new();
    options.read(true);
    #[cfg(unix)]
    {
        use std::os::unix::fs::OpenOptionsExt;
        options.custom_flags(libc::O_NOFOLLOW);
    }
    #[cfg(windows)]
    {
        use std::os::windows::fs::OpenOptionsExt;
        options.custom_flags(0x00200000);
    }
    let file = options.open(path)?;
    if !file.metadata()?.is_file() {
        return Err(bad("仅支持普通文件"));
    }
    Ok(file)
}
pub fn file_hash(path: &Path, size: u64) -> Result<String> {
    let mut f = regular(path)?;
    if f.metadata()?.len() != size {
        return Err(bad("文件长度不匹配"));
    }
    let mut digest = Sha256::new();
    let mut buffer = [0u8; 65536];
    let mut read = 0u64;
    loop {
        let n = f.read(&mut buffer)?;
        if n == 0 {
            break;
        }
        read += n as u64;
        if read > size {
            return Err(bad("文件读取超限"));
        }
        digest.update(&buffer[..n]);
    }
    if read != size {
        return Err(bad("文件被截断"));
    }
    Ok(format!("{:x}", digest.finalize()))
}
pub fn verify_tree(root: &Path, m: &Manifest) -> Result<()> {
    no_links(root)?;
    let expected: std::collections::BTreeSet<_> = m.files.iter().map(|f| f.path.clone()).collect();
    let mut actual = std::collections::BTreeSet::new();
    walk(root, root, &mut actual)?;
    if expected != actual {
        return Err(bad("文件树包含缺失或多余文件"));
    }
    for f in &m.files {
        let p = root.join(&f.path);
        if file_hash(&p, f.size)? != f.sha256 {
            return Err(bad("完整文件哈希不匹配"));
        }
        #[cfg(unix)]
        {
            use std::os::unix::fs::PermissionsExt;
            if fs::metadata(p)?.permissions().mode() & 0o777 != f.mode {
                return Err(bad("文件权限不匹配"));
            }
        }
    }
    Ok(())
}
fn walk(root: &Path, path: &Path, out: &mut std::collections::BTreeSet<String>) -> Result<()> {
    for entry in fs::read_dir(path)? {
        let p = entry?.path();
        no_links(&p)?;
        let t = fs::symlink_metadata(&p)?.file_type();
        if t.is_dir() {
            walk(root, &p, out)?;
        } else if t.is_file() {
            let rel = p
                .strip_prefix(root)
                .map_err(|_| bad("路径逃逸"))?
                .to_str()
                .ok_or_else(|| bad("文件名编码无效"))?
                .replace('\\', "/");
            out.insert(rel);
            if out.len() > 4096 {
                return Err(bad("文件过多"));
            }
        } else {
            return Err(bad("不支持特殊文件"));
        }
    }
    Ok(())
}
pub fn atomic_write(path: &Path, data: &[u8]) -> Result<()> {
    let parent = path.parent().ok_or_else(|| bad("缺少目录"))?;
    directory(parent)?;
    let mut temp = tempfile::NamedTempFile::new_in(parent)?;
    temp.write_all(data)?;
    temp.as_file().sync_all()?;
    temp.persist(path).map_err(|_| bad("保存更新状态失败"))?;
    sync_dir(parent)?;
    Ok(())
}
pub fn sync_dir(path: &Path) -> Result<()> {
    #[cfg(unix)]
    {
        File::open(path)?.sync_all()?;
    }
    #[cfg(not(unix))]
    {
        let _ = path;
    }
    Ok(())
}
pub fn set_mode(path: &Path, mode: u32) -> Result<()> {
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        fs::set_permissions(path, fs::Permissions::from_mode(mode))?;
    }
    #[cfg(not(unix))]
    {
        let _ = (path, mode);
    }
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;

    fn exercise_update_files(root: &Path) {
        let state_dir = root.join("updates/jobs/pending");
        directory(&state_dir).unwrap();
        no_links(&state_dir).unwrap();
        let state = state_dir.join("phase");
        atomic_write(&state, b"prepared").unwrap();
        assert_eq!(
            file_hash(&state, 8).unwrap(),
            super::super::manifest::hash(b"prepared")
        );
        atomic_write(&state, b"healthy").unwrap();
        assert_eq!(
            file_hash(&state, 7).unwrap(),
            super::super::manifest::hash(b"healthy")
        );
        assert!(regular(&state_dir).is_err());
        assert!(no_links(&state_dir.join("missing")).is_err());
    }

    #[test]
    fn canonical_absolute_paths_support_directory_hash_and_atomic_replacement() {
        let root = tempfile::tempdir_in(std::env::temp_dir().canonicalize().unwrap()).unwrap();
        let canonical = root.path().canonicalize().unwrap();
        #[cfg(windows)]
        assert!(
            matches!(canonical.components().next(), Some(std::path::Component::Prefix(prefix))
            if matches!(prefix.kind(), std::path::Prefix::VerbatimDisk(_)))
        );
        exercise_update_files(&canonical);
    }

    #[cfg(windows)]
    #[test]
    fn ordinary_absolute_disk_paths_support_update_files() {
        let root = tempfile::tempdir().unwrap();
        let canonical = root.path().canonicalize().unwrap();
        let text = canonical.to_str().unwrap();
        let ordinary = PathBuf::from(text.strip_prefix(r"\\?\").unwrap());
        assert!(
            matches!(ordinary.components().next(), Some(std::path::Component::Prefix(prefix))
            if matches!(prefix.kind(), std::path::Prefix::Disk(_)))
        );
        exercise_update_files(&ordinary);
    }

    #[cfg(windows)]
    #[test]
    fn incomplete_and_drive_relative_paths_are_not_update_roots() {
        for path in [
            r"C:",
            r"C:relative",
            r"\\?\C:",
            r"\\?\C:relative",
            r"\relative",
        ] {
            assert!(no_links(Path::new(path)).is_err(), "{path}");
        }
    }

    #[cfg(windows)]
    #[test]
    fn prefix_only_namespaces_must_not_bypass_metadata_checks() {
        let missing = format!("linknestx-missing-{}", uuid::Uuid::new_v4());
        for path in [format!(r"\\?\{missing}"), format!(r"\\.\{missing}")] {
            let path = Path::new(&path);
            assert!(path.is_absolute());
            assert!(fs::symlink_metadata(path).is_err());
            assert!(no_links(path).is_err());
        }
    }

    #[cfg(windows)]
    #[test]
    fn junction_ancestors_still_block_read_write_and_directory_creation() {
        let root = tempfile::tempdir().unwrap();
        let outside = tempfile::tempdir().unwrap();
        fs::write(outside.path().join("state"), b"original").unwrap();
        let junction = root.path().join("redirected");
        let output = std::process::Command::new("cmd.exe")
            .args(["/d", "/c", "mklink", "/J"])
            .arg(&junction)
            .arg(outside.path())
            .output()
            .unwrap();
        assert!(
            output.status.success(),
            "junction fixture: {} {}",
            String::from_utf8_lossy(&output.stdout),
            String::from_utf8_lossy(&output.stderr)
        );
        for base in [
            root.path().to_path_buf(),
            root.path().canonicalize().unwrap(),
        ] {
            let redirected = base.join("redirected");
            assert!(no_links(&redirected.join("state")).is_err());
            assert!(regular(&redirected.join("state")).is_err());
            assert!(atomic_write(&redirected.join("state"), b"changed").is_err());
            assert!(directory(&redirected.join("new-directory")).is_err());
        }
        assert_eq!(fs::read(outside.path().join("state")).unwrap(), b"original");
        assert!(!outside.path().join("new-directory").exists());
    }

    #[cfg(unix)]
    #[test]
    fn symlink_ancestors_still_block_update_files() {
        let root = tempfile::tempdir_in(std::env::temp_dir().canonicalize().unwrap()).unwrap();
        let outside = tempfile::tempdir().unwrap();
        fs::write(outside.path().join("state"), b"original").unwrap();
        let link = root.path().join("redirected");
        std::os::unix::fs::symlink(outside.path(), &link).unwrap();
        assert!(regular(&link.join("state")).is_err());
        assert!(atomic_write(&link.join("state"), b"changed").is_err());
        assert!(directory(&link.join("new-directory")).is_err());
        assert_eq!(fs::read(outside.path().join("state")).unwrap(), b"original");
        assert!(!outside.path().join("new-directory").exists());
    }
}
