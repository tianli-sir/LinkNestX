use super::manifest::{bad, Manifest};
use crate::Result;
use sha2::{Digest, Sha256};
use std::{
    fs::{self, File, OpenOptions},
    io::{Read, Write},
    path::{Path, PathBuf},
};
pub fn no_links(path: &Path) -> Result<()> {
    let mut part = PathBuf::new();
    for c in path.components() {
        part.push(c);
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
