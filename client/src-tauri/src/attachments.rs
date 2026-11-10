#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn handles_are_owner_bound_single_use_expiring_and_never_paths() {
        let root = tempfile::tempdir().unwrap();
        let source = root.path().join("hello.txt");
        std::fs::write(&source, b"hello").unwrap();
        let mut registry = Registry::new(root.path().join("staging")).unwrap();
        let handles = registry
            .prepare_files("owner-session", vec![source.clone()], 0)
            .unwrap();
        assert!(registry.take("other", &handles[0].id, 1).is_err());
        assert!(registry
            .take("owner-session", source.to_str().unwrap(), 1)
            .is_err());
        let selected = registry.take("owner-session", &handles[0].id, 1).unwrap();
        assert!(registry.take("owner-session", &handles[0].id, 1).is_err());
        let handles = registry
            .prepare_files("owner-session", vec![source.clone()], 0)
            .unwrap();
        assert!(registry
            .take("owner-session", &handles[0].id, TTL_MS + 1)
            .is_err());
        assert!(registry.entries.is_empty());
        drop(selected);
        assert_eq!(std::fs::read(&source).unwrap(), b"hello");
    }
    #[test]
    fn retained_file_descriptor_prevents_path_swap_and_clear_cleans_images() {
        let root = tempfile::tempdir().unwrap();
        let source = root.path().join("file.txt");
        std::fs::write(&source, b"chosen").unwrap();
        let mut registry = Registry::new(root.path().join("staging")).unwrap();
        let handles = registry
            .prepare_files("s", vec![source.clone()], 0)
            .unwrap();
        std::fs::rename(&source, root.path().join("old.txt")).unwrap();
        std::fs::write(&source, b"not chosen").unwrap();
        let selection = registry.take("s", &handles[0].id, 1).unwrap();
        let staged = selection.materialize(&registry.root).unwrap();
        assert_eq!(std::fs::read(staged.path()).unwrap(), b"chosen");
        let image = encode_rgba(1, 1, &[255, 0, 0, 255]).unwrap();
        let handles = registry.prepare_image("s", &image, 1).unwrap();
        assert_eq!(handles.len(), 1);
        registry.clear();
        assert!(registry.entries.is_empty());
        assert_eq!(std::fs::read(&source).unwrap(), b"not chosen");
    }
    #[test]
    fn directories_symlinks_and_oversized_images_are_rejected() {
        let root = tempfile::tempdir().unwrap();
        let mut registry = Registry::new(root.path().join("staging")).unwrap();
        assert!(registry
            .prepare_files("s", vec![root.path().to_owned()], 0)
            .is_err());
        #[cfg(unix)]
        {
            let file = root.path().join("a");
            std::fs::write(&file, b"a").unwrap();
            let link = root.path().join("link");
            std::os::unix::fs::symlink(file, &link).unwrap();
            assert!(registry.prepare_files("s", vec![link], 0).is_err());
        }
        assert!(encode_rgba(100_000, 100_000, &[]).is_err());
        assert!(registry.prepare_image("s", b"not a png", 0).is_err());
    }
    #[test]
    fn startup_cleanup_cannot_race_a_prepared_unregistered_source() {
        let root = tempfile::tempdir().unwrap();
        let db = crate::transfer::db::TransferDb::open(root.path()).unwrap();
        let source = root.path().join("chosen.txt");
        std::fs::write(&source, b"chosen").unwrap();
        let mut registry = Registry::new(root.path().join("attachments")).unwrap();
        let handle = registry
            .prepare_files("s", vec![source.clone()], 0)
            .unwrap()
            .remove(0);
        let pending = registry
            .take("s", &handle.id, 1)
            .unwrap()
            .materialize(&registry.root)
            .unwrap();
        let path = pending.path().to_owned();
        cleanup_uploads_at_startup(root.path(), &db).unwrap();
        assert_eq!(std::fs::read(&path).unwrap(), b"chosen");
        drop(pending);
        assert!(!path.exists());
        assert_eq!(std::fs::read(source).unwrap(), b"chosen");
    }
    #[test]
    fn dib_orientation_and_bounds_are_checked() {
        let mut dib = vec![0u8; 48];
        dib[0..4].copy_from_slice(&40u32.to_le_bytes());
        dib[4..8].copy_from_slice(&1i32.to_le_bytes());
        dib[8..12].copy_from_slice(&2i32.to_le_bytes());
        dib[12..14].copy_from_slice(&1u16.to_le_bytes());
        dib[14..16].copy_from_slice(&24u16.to_le_bytes());
        dib[40..43].copy_from_slice(&[255, 0, 0]);
        dib[44..47].copy_from_slice(&[0, 0, 255]);
        let png = dib_to_png(&dib).unwrap();
        let (_, _, pixels) = decode_png(&png).unwrap();
        assert_eq!(pixels, &[255, 0, 0, 255, 0, 0, 255, 255]);
        assert!(dib_to_png(&dib[..45]).is_err());
    }
}
use crate::{transfer::models::MAX_FILE_SIZE, Result, TransferError};
use serde::Serialize;
use std::{
    collections::HashMap,
    fs::File,
    io::{Read, Seek, SeekFrom, Write},
    path::{Path, PathBuf},
};
const TTL_MS: u64 = 10 * 60 * 1000;
const MAX_PIXELS: u64 = 20_000_000;
const MAX_IMAGE_BYTES: usize = 80_000_000;
#[derive(Clone, Debug, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct Attachment {
    pub id: String,
    pub name: String,
    pub size: u64,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub mime_type: Option<String>,
}
struct Entry {
    scope: String,
    expires: u64,
    selection: Selection,
}
pub(crate) struct Selection {
    pub name: String,
    pub size: u64,
    file: File,
    _temp: Option<tempfile::NamedTempFile>,
}
pub(crate) struct Staged {
    directory: tempfile::TempDir,
    // Prevent another process startup from reclaiming this source before registration.
    _preparation_lock: File,
    path: PathBuf,
}
impl Staged {
    pub fn path(&self) -> &Path {
        &self.path
    }
    pub fn commit(self) -> PathBuf {
        let _ = self.directory.keep();
        self.path
    }
}
impl Selection {
    pub fn read_bounded(mut self, limit: usize) -> Result<Vec<u8>> {
        if self.size > limit as u64 {
            return Err(TransferError::new("图片大小超过限制"));
        }
        let mut data = Vec::new();
        self.file.seek(SeekFrom::Start(0))?;
        self.file.take(limit as u64 + 1).read_to_end(&mut data)?;
        if data.len() > limit {
            return Err(TransferError::new("图片大小超过限制"));
        }
        Ok(data)
    }
    pub fn materialize(mut self, root: &Path) -> Result<Staged> {
        let preparation_lock = preparation_lock(root)?;
        preparation_lock.lock_shared()?;
        let directory = tempfile::Builder::new()
            .prefix("upload-")
            .tempdir_in(root)?;
        let path = directory
            .path()
            .join(crate::transfer::models::suggested_name(&self.name));
        let mut destination = std::fs::OpenOptions::new()
            .write(true)
            .create_new(true)
            .open(&path)?;
        self.file.seek(SeekFrom::Start(0))?;
        let copied = std::io::copy(&mut self.file.take(MAX_FILE_SIZE + 1), &mut destination)?;
        if copied != self.size || copied > MAX_FILE_SIZE {
            return Err(TransferError::new("选中的文件已发生变化，请重新选择"));
        }
        destination.sync_all()?;
        Ok(Staged {
            directory,
            path,
            _preparation_lock: preparation_lock,
        })
    }
}
pub struct Registry {
    pub(crate) root: PathBuf,
    entries: HashMap<String, Entry>,
}
impl Registry {
    pub fn new(root: PathBuf) -> Result<Self> {
        std::fs::create_dir_all(&root)?;
        #[cfg(unix)]
        {
            use std::os::unix::fs::PermissionsExt;
            std::fs::set_permissions(&root, std::fs::Permissions::from_mode(0o700))?;
        }
        // Drafts have no persisted task references. Interrupted uploads have their own directories.
        for item in std::fs::read_dir(&root)? {
            let item = item?;
            if item.file_name().to_string_lossy().starts_with("draft-")
                && item.file_type()?.is_file()
            {
                let _ = std::fs::remove_file(item.path());
            }
        }
        Ok(Self {
            root,
            entries: HashMap::new(),
        })
    }
    pub(crate) fn expire(&mut self, now: u64) {
        self.entries.retain(|_, e| e.expires > now);
    }
    pub(crate) fn clear(&mut self) {
        self.entries.clear();
    }
    fn insert(
        &mut self,
        scope: &str,
        selection: Selection,
        now: u64,
        mime: Option<String>,
    ) -> Attachment {
        let handle = Attachment {
            id: uuid::Uuid::new_v4().to_string(),
            name: selection.name.clone(),
            size: selection.size,
            mime_type: mime,
        };
        self.entries.insert(
            handle.id.clone(),
            Entry {
                scope: scope.into(),
                expires: now + TTL_MS,
                selection,
            },
        );
        handle
    }
    pub(crate) fn prepare_files(
        &mut self,
        scope: &str,
        paths: Vec<PathBuf>,
        now: u64,
    ) -> Result<Vec<Attachment>> {
        self.expire(now);
        if paths.len() > 20 || self.entries.len() + paths.len() > 100 {
            return Err(TransferError::new("一次最多选择20个文件"));
        }
        let mut selections = Vec::new();
        for path in paths {
            if !path.is_absolute() {
                return Err(TransferError::new("请选择本机普通文件"));
            }
            #[cfg(windows)]
            {
                let text = path.as_os_str().to_string_lossy();
                if text.starts_with("\\\\") || text.starts_with("//") {
                    return Err(TransferError::new("暂不支持网络路径，请先保存到本机"));
                }
            }
            let meta = std::fs::symlink_metadata(&path)?;
            if !meta.is_file() || meta.file_type().is_symlink() {
                return Err(TransferError::new("不支持文件夹或链接，请选择普通文件"));
            }
            let mut options = std::fs::OpenOptions::new();
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
            let file = options.open(&path)?;
            let meta = file.metadata()?;
            #[cfg(windows)]
            {
                use std::os::windows::fs::MetadataExt;
                if meta.file_attributes() & 0x400 != 0 {
                    return Err(TransferError::new("不支持重解析链接"));
                }
            }
            if !meta.is_file() || meta.len() > MAX_FILE_SIZE {
                return Err(TransferError::new("每个文件最大20GB"));
            }
            let name = path
                .file_name()
                .and_then(|s| s.to_str())
                .filter(|s| crate::transfer::models::display_name(s))
                .ok_or_else(|| TransferError::new("文件名称无法识别，请重命名后重试"))?
                .to_owned();
            selections.push(Selection {
                name,
                size: meta.len(),
                file,
                _temp: None,
            });
        }
        Ok(selections
            .into_iter()
            .map(|s| self.insert(scope, s, now, None))
            .collect())
    }
    pub(crate) fn prepare_image(
        &mut self,
        scope: &str,
        png: &[u8],
        now: u64,
    ) -> Result<Vec<Attachment>> {
        self.expire(now);
        if self.entries.len() >= 100 {
            return Err(TransferError::new("待发送附件过多"));
        }
        let (width, height, pixels) = decode_png(png)?;
        let normalized = encode_rgba(width, height, &pixels)?;
        let mut temp = tempfile::Builder::new()
            .prefix("draft-")
            .suffix(".png")
            .tempfile_in(&self.root)?;
        temp.write_all(&normalized)?;
        temp.as_file().sync_all()?;
        let file = temp.reopen()?;
        let selection = Selection {
            name: format!("截图-{}.png", chrono::Local::now().format("%Y%m%d-%H%M%S")),
            size: normalized.len() as u64,
            file,
            _temp: Some(temp),
        };
        Ok(vec![self.insert(
            scope,
            selection,
            now,
            Some("image/png".into()),
        )])
    }
    pub(crate) fn take(&mut self, scope: &str, id: &str, now: u64) -> Result<Selection> {
        self.expire(now);
        if !self.entries.get(id).is_some_and(|e| e.scope == scope) {
            return Err(TransferError::new("附件已失效，请重新选择"));
        }
        Ok(self.entries.remove(id).unwrap().selection)
    }
    pub(crate) fn discard(&mut self, scope: &str, id: &str, now: u64) -> Result<()> {
        self.take(scope, id, now).map(drop)
    }
}
pub(crate) fn now() -> u64 {
    chrono::Utc::now().timestamp_millis().max(0) as u64
}
fn dimensions(width: u32, height: u32) -> Result<usize> {
    let pixels = width as u64 * height as u64;
    if width == 0 || height == 0 || pixels > MAX_PIXELS {
        return Err(TransferError::new("图片尺寸超过限制"));
    }
    Ok(pixels as usize * 4)
}
fn image_error() -> TransferError {
    TransferError::new("剪贴板图片格式无效或超出限制")
}
fn decode_png(data: &[u8]) -> Result<(u32, u32, Vec<u8>)> {
    if data.len() > MAX_IMAGE_BYTES {
        return Err(image_error());
    }
    let mut decoder = png::Decoder::new(std::io::Cursor::new(data));
    decoder.set_limits(png::Limits {
        bytes: MAX_IMAGE_BYTES,
    });
    decoder.set_transformations(png::Transformations::EXPAND | png::Transformations::STRIP_16);
    let mut reader = decoder.read_info().map_err(|_| image_error())?;
    let info = reader.info();
    dimensions(info.width, info.height)?;
    let size = reader
        .output_buffer_size()
        .filter(|s| *s <= MAX_IMAGE_BYTES)
        .ok_or_else(image_error)?;
    let mut bytes = vec![0; size];
    let output = reader.next_frame(&mut bytes).map_err(|_| image_error())?;
    bytes.truncate(output.buffer_size());
    let mut rgba = Vec::with_capacity(dimensions(output.width, output.height)?);
    match output.color_type {
        png::ColorType::Rgba => rgba = bytes,
        png::ColorType::Rgb => {
            for p in bytes.chunks_exact(3) {
                rgba.extend_from_slice(&[p[0], p[1], p[2], 255]);
            }
        }
        png::ColorType::Grayscale => {
            for p in bytes {
                rgba.extend_from_slice(&[p, p, p, 255]);
            }
        }
        png::ColorType::GrayscaleAlpha => {
            for p in bytes.chunks_exact(2) {
                rgba.extend_from_slice(&[p[0], p[0], p[0], p[1]]);
            }
        }
        _ => return Err(image_error()),
    }
    Ok((output.width, output.height, rgba))
}
fn encode_rgba(width: u32, height: u32, data: &[u8]) -> Result<Vec<u8>> {
    if dimensions(width, height)? != data.len() {
        return Err(image_error());
    }
    let mut output = Vec::new();
    {
        let mut encoder = png::Encoder::new(&mut output, width, height);
        encoder.set_color(png::ColorType::Rgba);
        encoder.set_depth(png::BitDepth::Eight);
        let mut writer = encoder.write_header().map_err(|_| image_error())?;
        writer.write_image_data(data).map_err(|_| image_error())?;
    }
    if output.len() > MAX_IMAGE_BYTES {
        return Err(image_error());
    }
    Ok(output)
}
#[cfg(any(windows, test))]
fn dib_to_png(data: &[u8]) -> Result<Vec<u8>> {
    if data.len() < 40 || data.len() > MAX_IMAGE_BYTES {
        return Err(image_error());
    }
    let u16at = |n| u16::from_le_bytes(data[n..n + 2].try_into().unwrap());
    let u32at = |n| u32::from_le_bytes(data[n..n + 4].try_into().unwrap());
    let header = u32at(0) as usize;
    let width = u32at(4) as i32;
    let height = u32at(8) as i32;
    let bits = u16at(14);
    let compression = u32at(16);
    if header < 40
        || header > data.len()
        || width <= 0
        || height == 0
        || height == i32::MIN
        || u16at(12) != 1
        || !matches!(bits, 24 | 32)
        || !matches!(compression, 0 | 3)
        || (compression == 3 && bits != 32)
    {
        return Err(image_error());
    }
    let width = width as u32;
    let abs_height = height.unsigned_abs();
    dimensions(width, abs_height)?;
    let mut offset = header;
    if compression == 3 {
        if header == 40 {
            offset = 52;
        }
        if data.len() < 52
            || u32at(40) != 0x00ff0000
            || u32at(44) != 0x0000ff00
            || u32at(48) != 0x000000ff
        {
            return Err(image_error());
        }
        if header >= 56 && !matches!(u32at(52), 0 | 0xff000000) {
            return Err(image_error());
        }
    }
    let stride = (width as usize * bits as usize).div_ceil(32) * 4;
    if offset
        .checked_add(stride * abs_height as usize)
        .is_none_or(|n| n > data.len())
    {
        return Err(image_error());
    }
    let mut rgba = vec![0; dimensions(width, abs_height)?];
    for y in 0..abs_height as usize {
        let row = if height > 0 {
            abs_height as usize - 1 - y
        } else {
            y
        };
        for x in 0..width as usize {
            let source = offset + row * stride + x * (bits as usize / 8);
            let target = (y * width as usize + x) * 4;
            rgba[target..target + 4].copy_from_slice(&[
                data[source + 2],
                data[source + 1],
                data[source],
                if bits == 32 { data[source + 3] } else { 255 },
            ]);
        }
    }
    if bits == 32 && rgba.chunks_exact(4).all(|p| p[3] == 0) {
        for p in rgba.chunks_exact_mut(4) {
            p[3] = 255;
        }
    }
    encode_rgba(width, abs_height, &rgba)
}
pub(crate) enum ClipboardContent {
    Files(Vec<PathBuf>),
    Image(Vec<u8>),
    Empty,
}
#[cfg(target_os = "macos")]
#[path = "attachments/macos.rs"]
mod platform;
#[cfg(target_os = "windows")]
#[path = "attachments/windows.rs"]
mod platform;
#[cfg(any(target_os = "macos", target_os = "windows"))]
pub(crate) use platform::read_clipboard;
#[cfg(not(any(target_os = "macos", target_os = "windows")))]
pub(crate) fn read_clipboard() -> Result<ClipboardContent> {
    Err(TransferError::new("当前平台不支持粘贴附件，请选择文件"))
}

fn ordinary_metadata(metadata: &std::fs::Metadata) -> bool {
    if metadata.file_type().is_symlink() {
        return false;
    }
    #[cfg(windows)]
    {
        use std::os::windows::fs::MetadataExt;
        if metadata.file_attributes() & 0x400 != 0 {
            return false;
        }
    }
    true
}
fn preparation_lock(root: &Path) -> Result<File> {
    let mut options = std::fs::OpenOptions::new();
    options.read(true).write(true).create(true);
    #[cfg(unix)]
    {
        use std::os::unix::fs::OpenOptionsExt;
        options.custom_flags(libc::O_NOFOLLOW).mode(0o600);
    }
    #[cfg(windows)]
    {
        use std::os::windows::fs::OpenOptionsExt;
        options.custom_flags(0x00200000);
    }
    let file = options.open(root.join(".upload-prepare.lock"))?;
    let metadata = file.metadata()?;
    if !metadata.is_file() || !ordinary_metadata(&metadata) {
        return Err(TransferError::new("附件暂存目录无效"));
    }
    Ok(file)
}
/// Only service initialization calls this, before native input becomes available.
/// A process-wide once gate protects repeated opens; the OS lock also excludes
/// preparations owned by another process using this application data directory.
pub(crate) fn cleanup_uploads_at_startup(
    app_root: &Path,
    db: &crate::transfer::db::TransferDb,
) -> Result<()> {
    use std::collections::HashSet;
    use std::sync::{Mutex, OnceLock};
    static VISITED: OnceLock<Mutex<HashSet<PathBuf>>> = OnceLock::new();
    let app_root = std::fs::canonicalize(app_root)?;
    let mut visited = VISITED.get_or_init(Mutex::default).lock().unwrap();
    if visited.contains(&app_root) {
        return Ok(());
    }
    let staging = app_root.join("attachments");
    let metadata = match std::fs::symlink_metadata(&staging) {
        Ok(metadata) => metadata,
        Err(error) if error.kind() == std::io::ErrorKind::NotFound => {
            visited.insert(app_root);
            return Ok(());
        }
        Err(error) => return Err(error.into()),
    };
    if !metadata.is_dir() || !ordinary_metadata(&metadata) {
        visited.insert(app_root);
        return Ok(());
    }
    let lock = preparation_lock(&staging)?;
    if lock.try_lock().is_err() {
        visited.insert(app_root);
        return Ok(());
    }
    // Read every owner's persisted references only after holding the preparation
    // barrier. Task registration happens before its shared lock is released.
    let references: Vec<_> = db
        .all()?
        .into_iter()
        .map(|task| {
            let path = PathBuf::from(task.path);
            std::fs::canonicalize(&path).unwrap_or_else(|_| {
                path.parent()
                    .and_then(|parent| std::fs::canonicalize(parent).ok())
                    .and_then(|parent| path.file_name().map(|name| parent.join(name)))
                    .unwrap_or(path)
            })
        })
        .collect();
    for entry in std::fs::read_dir(&staging)? {
        let entry = entry?;
        let name = entry.file_name();
        let Some(suffix) = name.to_str().and_then(|s| s.strip_prefix("upload-")) else {
            continue;
        };
        // This exact shape is created by tempfile::Builder in materialize.
        if suffix.len() != 6 || !suffix.bytes().all(|b| b.is_ascii_alphanumeric()) {
            continue;
        }
        let path = entry.path();
        let metadata = std::fs::symlink_metadata(&path)?;
        if !metadata.is_dir() || !ordinary_metadata(&metadata) {
            continue;
        }
        let canonical = std::fs::canonicalize(&path)?;
        if references
            .iter()
            .any(|reference| reference.starts_with(&canonical))
        {
            continue;
        }
        let children = std::fs::read_dir(&path)?.collect::<std::io::Result<Vec<_>>>()?;
        // Native snapshots contain at most one ordinary payload. Do not recurse
        // into user-created content, follow links, or touch unexpected layouts.
        if children.len() > 1 {
            continue;
        }
        let mut safe = true;
        for child in &children {
            let metadata = std::fs::symlink_metadata(child.path())?;
            if !metadata.is_file() || !ordinary_metadata(&metadata) {
                safe = false;
                break;
            }
        }
        if !safe {
            continue;
        }
        for child in children {
            std::fs::remove_file(child.path())?;
        }
        std::fs::remove_dir(path)?;
    }
    visited.insert(app_root);
    Ok(())
}
