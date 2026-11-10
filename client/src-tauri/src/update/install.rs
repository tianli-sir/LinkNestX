//! Trusted early helper. Network manifests never supply commands, target roots or launch arguments.
use super::{
    files,
    manifest::{self, bad, Manifest},
};
use crate::Result;
use serde::{Deserialize, Serialize};
use std::{
    fs::{self, File, OpenOptions},
    io::Read,
    path::{Path, PathBuf},
    process::{Child, Command, Stdio},
    time::{Duration, Instant},
};

#[derive(Serialize, Deserialize, Clone)]
#[serde(deny_unknown_fields)]
pub struct Transaction {
    target: PathBuf,
    staged: PathBuf,
    journal: PathBuf,
    id: String,
    old_digest: String,
}
impl Transaction {
    pub fn new(target: PathBuf, staged: PathBuf, journal: PathBuf, id: &str) -> Result<Self> {
        valid_id(id)?;
        files::no_links(&target)?;
        files::no_links(&staged)?;
        if target.parent() != staged.parent() {
            return Err(bad("更新必须与目标同卷"));
        }
        let tx = Self {
            old_digest: tree_digest(&target)?,
            target,
            staged,
            journal,
            id: id.into(),
        };
        if tx.backup_path().exists() {
            return Err(bad("备份已存在"));
        }
        tx.phase("prepared")?;
        Ok(tx)
    }
    fn backup_path(&self) -> PathBuf {
        self.target
            .parent()
            .unwrap()
            .join(format!(".linknestx-{}-backup", self.id))
    }
    fn failed_path(&self) -> PathBuf {
        self.target
            .parent()
            .unwrap()
            .join(format!(".linknestx-{}-failed", self.id))
    }
    fn phase(&self, phase: &str) -> Result<()> {
        files::atomic_write(&self.journal, phase.as_bytes())
    }
    #[cfg(any(test, not(any(target_os = "macos", target_os = "windows"))))]
    pub fn backup(&self) -> Result<()> {
        if tree_digest(&self.target)? != self.old_digest {
            return Err(bad("旧应用在安装前已改变"));
        }
        self.phase("swapping")?;
        fs::rename(&self.target, self.backup_path())?;
        files::sync_dir(self.target.parent().unwrap())?;
        self.phase("oldMoved")
    }
    #[cfg(any(test, not(any(target_os = "macos", target_os = "windows"))))]
    pub fn activate(&self) -> Result<()> {
        fs::rename(&self.staged, &self.target)?;
        files::sync_dir(self.target.parent().unwrap())?;
        self.phase("awaitingHealth")
    }
    fn swap(&self) -> Result<()> {
        if tree_digest(&self.target)? != self.old_digest {
            return Err(bad("旧应用在安装前已改变"));
        }
        self.phase("swapping")?;
        #[cfg(target_os = "macos")]
        {
            exchange_paths(&self.target, &self.staged)?;
            files::sync_dir(self.target.parent().unwrap())?;
            fs::rename(&self.staged, self.backup_path())?;
        }
        #[cfg(target_os = "windows")]
        replace_file(&self.target, &self.staged, &self.backup_path())?;
        #[cfg(not(any(target_os = "macos", target_os = "windows")))]
        {
            self.backup()?;
            self.activate()?;
        }
        files::sync_dir(self.target.parent().unwrap())?;
        self.phase("awaitingHealth")
    }
    pub fn rollback(&self) -> Result<()> {
        if read_small(&self.journal, 64).ok().as_deref() == Some(b"healthy") {
            return Ok(());
        }
        self.restore_original()?;
        // A crash after atomic restoration can leave the failed payload in the backup slot.
        // The verified old target is authoritative; never exchange it back to the failed app.
        let backup = self.backup_path();
        if backup.exists()
            && tree_digest(&backup)? != self.old_digest
            && !self.failed_path().exists()
        {
            fs::rename(&backup, self.failed_path())?;
            files::sync_dir(self.target.parent().unwrap())?;
        }
        self.phase("rolledBack")
    }
    fn restore_original(&self) -> Result<()> {
        if self.target.exists() && tree_digest(&self.target)? == self.old_digest {
            return Ok(());
        }
        let backup = self.backup_path();
        if backup.exists() {
            if tree_digest(&backup)? != self.old_digest {
                return Err(bad("备份完整性不匹配，需手动恢复"));
            }
            if self.target.exists() {
                #[cfg(target_os = "macos")]
                exchange_paths(&self.target, &backup)?;
                #[cfg(target_os = "windows")]
                {
                    if self.failed_path().exists() {
                        return Err(bad("恢复目录已存在，需手动恢复"));
                    }
                    replace_file(&self.target, &backup, &self.failed_path())?;
                }
                #[cfg(not(any(target_os = "macos", target_os = "windows")))]
                return Err(bad("当前平台不支持安全回滚"));
            } else {
                // Compatibility with an older interrupted transaction whose target is already absent.
                fs::rename(&backup, &self.target)?;
            }
            files::sync_dir(self.target.parent().unwrap())?;
        } else if self.staged.exists() && tree_digest(&self.staged)? == self.old_digest {
            if self.target.exists() {
                #[cfg(target_os = "macos")]
                exchange_paths(&self.target, &self.staged)?;
                #[cfg(not(target_os = "macos"))]
                return Err(bad("需要手动恢复原程序"));
            } else {
                fs::rename(&self.staged, &self.target)?;
            }
            files::sync_dir(self.target.parent().unwrap())?;
        } else {
            return Err(bad("原应用和备份均不可用"));
        }
        if tree_digest(&self.target)? != self.old_digest {
            return Err(bad("恢复后原应用校验失败"));
        }
        Ok(())
    }
    fn healthy(&self) -> Result<()> {
        self.phase("healthy")
    }
}
fn tree_digest(root: &Path) -> Result<String> {
    files::no_links(root)?;
    let metadata = fs::symlink_metadata(root)?;
    if metadata.is_file() {
        if metadata.len() > manifest::MAX_BYTES {
            return Err(bad("基线过大"));
        }
        return files::file_hash(root, metadata.len());
    }

    fn visit(root: &Path, path: &Path, rows: &mut Vec<String>, bytes: &mut u64) -> Result<()> {
        files::no_links(path)?;
        for e in fs::read_dir(path)? {
            let p = e?.path();
            files::no_links(&p)?;
            let meta = fs::symlink_metadata(&p)?;
            if meta.is_dir() {
                visit(root, &p, rows, bytes)?;
            } else if meta.is_file() {
                *bytes = bytes
                    .checked_add(meta.len())
                    .ok_or_else(|| bad("基线过大"))?;
                if *bytes > manifest::MAX_BYTES || rows.len() >= 4096 {
                    return Err(bad("基线过大，请手动安装"));
                }
                let rel = p
                    .strip_prefix(root)
                    .map_err(|_| bad("路径错误"))?
                    .to_str()
                    .ok_or_else(|| bad("文件名错误"))?;
                rows.push(format!(
                    "{}\0{}\0{}",
                    rel,
                    meta.len(),
                    files::file_hash(&p, meta.len())?
                ));
            } else {
                return Err(bad("当前应用含不支持的链接或特殊文件，请手动安装"));
            }
        }
        Ok(())
    }
    let mut rows = Vec::new();
    visit(root, root, &mut rows, &mut 0)?;
    rows.sort();
    Ok(manifest::hash(rows.join("\n").as_bytes()))
}
fn valid_id(id: &str) -> Result<()> {
    if uuid::Uuid::parse_str(id)
        .ok()
        .is_none_or(|v| v.to_string() != id)
    {
        return Err(bad("更新标识无效"));
    }
    Ok(())
}
fn read_small(path: &Path, max: u64) -> Result<Vec<u8>> {
    let mut f = files::regular(path)?;
    if f.metadata()?.len() > max {
        return Err(bad("状态文件过大"));
    }
    let mut out = Vec::new();
    f.by_ref().take(max + 1).read_to_end(&mut out)?;
    if out.len() as u64 > max {
        return Err(bad("状态文件超限"));
    }
    Ok(out)
}
fn user_home() -> Result<PathBuf> {
    std::env::var_os(if cfg!(windows) { "USERPROFILE" } else { "HOME" })
        .map(PathBuf::from)
        .ok_or_else(|| bad("无法确定当前用户目录"))
}
pub fn data_root() -> Result<PathBuf> {
    #[cfg(target_os = "macos")]
    {
        Ok(user_home()?.join("Library/Application Support/com.linknestx.client"))
    }
    #[cfg(target_os = "windows")]
    {
        Ok(
            PathBuf::from(std::env::var_os("APPDATA").ok_or_else(|| bad("无法确定用户应用目录"))?)
                .join("com.linknestx.client"),
        )
    }
    #[cfg(not(any(target_os = "windows", target_os = "macos")))]
    {
        Err(bad("当前平台需手动更新"))
    }
}
fn owned_target(target: &Path) -> Result<()> {
    files::no_links(target)?;
    let home = user_home()?.canonicalize()?;
    let actual = target.canonicalize()?;
    if !actual.starts_with(&home) || actual == home {
        return Err(bad("仅支持当前用户可写安装，请手动安装到用户目录"));
    }
    #[cfg(unix)]
    {
        use std::os::unix::fs::MetadataExt;
        for p in [target, target.parent().ok_or_else(|| bad("目标无父目录"))?] {
            let m = fs::metadata(p)?;
            if m.uid() != unsafe { libc::geteuid() } || m.mode() & 0o022 != 0 {
                return Err(bad("安装位置不属于当前用户或可被其他用户修改"));
            }
        }
    }
    #[cfg(target_os = "macos")]
    {
        if target.extension().and_then(|s| s.to_str()) != Some("app") {
            return Err(bad("请先安装完整 .app 后更新"));
        }
    }
    #[cfg(windows)]
    {
        let local =
            PathBuf::from(std::env::var_os("LOCALAPPDATA").ok_or_else(|| bad("缺少用户安装目录"))?)
                .canonicalize()?;
        if !actual.starts_with(local)
            || target.file_name().and_then(|s| s.to_str()) != Some("LinkNestX")
        {
            return Err(bad("请先安装到当前用户 LinkNestX 目录"));
        }
    }
    let probe = tempfile::NamedTempFile::new_in(target.parent().unwrap())
        .map_err(|_| bad("安装位置不可写，请手动更新"))?;
    drop(probe);
    Ok(())
}
pub fn installed_root() -> Result<PathBuf> {
    let exe = std::env::current_exe()?.canonicalize()?;
    #[cfg(target_os = "macos")]
    {
        let root = exe
            .parent()
            .and_then(Path::parent)
            .and_then(Path::parent)
            .ok_or_else(|| bad("请先安装完整 .app 后更新"))?;
        owned_target(root)?;
        if exe != root.join("Contents/MacOS/linknestx") {
            return Err(bad("应用入口不受支持"));
        }
        Ok(root.to_path_buf())
    }
    #[cfg(target_os = "windows")]
    {
        let root = exe.parent().ok_or_else(|| bad("缺少安装目录"))?;
        owned_target(root)?;
        if exe.file_name().and_then(|s| s.to_str()) != Some("LinkNestX.exe") {
            return Err(bad("应用入口不受支持"));
        }
        Ok(root.to_path_buf())
    }
    #[cfg(not(any(target_os = "windows", target_os = "macos")))]
    {
        let _ = exe;
        Err(bad("当前平台需手动更新"))
    }
}
pub fn validate_entry(m: &Manifest) -> Result<()> {
    let expected = if cfg!(windows) {
        "LinkNestX.exe"
    } else {
        "Contents/MacOS/linknestx"
    };
    if m.entry_point != expected
        || m.platform != std::env::consts::OS
        || m.arch != std::env::consts::ARCH
    {
        return Err(bad("更新主程序或平台不匹配"));
    }
    if cfg!(windows) && (m.files.len() != 1 || m.files[0].path != "LinkNestX.exe") {
        return Err(bad(
            "Windows 当前更新格式仅支持主程序文件，其他变更请手动安装",
        ));
    }
    Ok(())
}
fn staged_path(target: &Path, id: &str) -> PathBuf {
    target.parent().unwrap().join(if cfg!(windows) {
        format!(".LinkNestX-{id}.exe")
    } else {
        format!(".LinkNestX-{id}.app")
    })
}
fn application_entry(target: &Path) -> PathBuf {
    if cfg!(windows) {
        target.to_path_buf()
    } else {
        target.join("Contents/MacOS/linknestx")
    }
}
fn validate_slot(target: &Path) -> Result<()> {
    if cfg!(windows) {
        if target.file_name().and_then(|n| n.to_str()) != Some("LinkNestX.exe") {
            return Err(bad("Windows 更新目标无效"));
        }
        owned_target(target.parent().ok_or_else(|| bad("无安装目录"))?)
    } else {
        owned_target(target)
    }
}
fn verify_staged(m: &Manifest, path: &Path) -> Result<()> {
    if cfg!(windows) {
        if files::file_hash(path, m.files[0].size)? != m.files[0].sha256 {
            return Err(bad("主程序校验失败"));
        }
        Ok(())
    } else {
        files::verify_tree(path, m)
    }
}
fn platform_verify(root: &Path) -> Result<()> {
    #[cfg(target_os = "macos")]
    {
        if !Command::new("/usr/bin/codesign")
            .args(["--verify", "--deep", "--strict"])
            .arg(root)
            .stdout(Stdio::null())
            .stderr(Stdio::null())
            .status()?
            .success()
        {
            return Err(bad("macOS 应用签名验证失败，请手动安装"));
        }
    }
    #[cfg(not(target_os = "macos"))]
    {
        let _ = root;
    }
    Ok(())
}
#[derive(Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
struct Job {
    id: String,
    tx: Transaction,
    parent_pid: u32,
    nonce: String,
    helper_sha256: String,
    helper_size: u64,
    version: String,
    sequence: u64,
    prepared_at: i64,
}
fn helper_path(jobdir: &Path) -> PathBuf {
    jobdir.join(if cfg!(windows) {
        "helper.exe"
    } else {
        "helper"
    })
}
fn lock(root: &Path) -> Result<File> {
    files::directory(root)?;
    let path = root.join("install.lock");
    if path.exists() {
        files::no_links(&path)?;
    }
    let file = OpenOptions::new()
        .create(true)
        .truncate(false)
        .read(true)
        .write(true)
        .open(path)?;
    file.try_lock()
        .map_err(|_| bad("另一个安装或恢复任务正在运行"))?;
    Ok(file)
}
fn copy_tree(m: &Manifest, from: &Path, to: &Path) -> Result<()> {
    files::directory(to)?;
    for f in &m.files {
        let target = to.join(&f.path);
        files::directory(target.parent().unwrap())?;
        let mut input = files::regular(&from.join(&f.path))?;
        let mut output = OpenOptions::new()
            .write(true)
            .create_new(true)
            .open(&target)?;
        std::io::copy(&mut input, &mut output)?;
        output.sync_all()?;
        files::set_mode(&target, f.mode)?;
    }
    files::verify_tree(to, m)
}
pub struct Handoff {
    pub child: Child,
    _lock: File,
    pub id: String,
    pub jobdir: PathBuf,
}
pub fn prepare_handoff(root: &Path, envelope: &[u8], stage: &Path) -> Result<Handoff> {
    let updates = root.join("updates");
    let installation_lock = lock(&updates)?;
    let installed = installed_root()?;
    let target = if cfg!(windows) {
        installed.join("LinkNestX.exe")
    } else {
        installed
    };
    let m = manifest::verify(
        envelope,
        &manifest::pinned_key()?,
        chrono::Utc::now().timestamp(),
    )?;
    validate_entry(&m)?;
    manifest::eligible(
        &m,
        std::env::consts::OS,
        std::env::consts::ARCH,
        env!("CARGO_PKG_VERSION"),
        manifest::BUILD_SEQUENCE,
    )?;
    files::verify_tree(stage, &m)?;
    let id = uuid::Uuid::new_v4().to_string();
    let jobdir = updates.join("jobs").join(&id);
    files::directory(&jobdir)?;
    let staged = staged_path(&target, &id);
    if cfg!(windows) {
        let mut source = files::regular(&stage.join("LinkNestX.exe"))?;
        let mut out = OpenOptions::new()
            .create_new(true)
            .write(true)
            .open(&staged)?;
        std::io::copy(&mut source, &mut out)?;
        out.sync_all()?;
        verify_staged(&m, &staged)?;
    } else {
        copy_tree(&m, stage, &staged)?;
    }
    platform_verify(&staged)?;
    let tx = Transaction::new(target, staged, jobdir.join("phase"), &id)?;
    let current = std::env::current_exe()?;
    let helper = helper_path(&jobdir);
    let mut input = files::regular(&current)?;
    let size = input.metadata()?.len();
    let mut output = OpenOptions::new()
        .create_new(true)
        .write(true)
        .open(&helper)?;
    std::io::copy(&mut input, &mut output)?;
    output.sync_all()?;
    files::set_mode(&helper, 0o700)?;
    let job = Job {
        id: id.clone(),
        tx,
        parent_pid: std::process::id(),
        nonce: uuid::Uuid::new_v4().to_string(),
        helper_sha256: files::file_hash(&helper, size)?,
        helper_size: size,
        version: m.version,
        sequence: m.build_sequence,
        prepared_at: chrono::Utc::now().timestamp(),
    };
    files::atomic_write(&jobdir.join("manifest.json"), envelope)?;
    files::atomic_write(&jobdir.join("job.json"), &serde_json::to_vec(&job)?)?;
    files::atomic_write(&updates.join("pending"), id.as_bytes())?;
    let child = Command::new(&helper)
        .args(["--linknestx-update-helper", &id])
        .stdin(Stdio::null())
        .stdout(Stdio::null())
        .stderr(Stdio::null())
        .spawn()
        .map_err(|error| {
            let _ = files::atomic_write(&jobdir.join("phase"), b"cancelled");
            crate::TransferError::from(error)
        })?;
    Ok(Handoff {
        child,
        _lock: installation_lock,
        id,
        jobdir,
    })
}
impl Handoff {
    pub fn ready(&mut self) -> Result<bool> {
        if self.child.try_wait()?.is_some() {
            return Err(bad("更新助手未能启动"));
        }
        Ok(read_small(&self.jobdir.join("helper-ready"), 128)
            .ok()
            .as_deref()
            == Some(self.id.as_bytes()))
    }
    pub fn cancel(mut self) {
        let _ = self.child.kill();
        let _ = self.child.wait();
        let _ = files::atomic_write(&self.jobdir.join("phase"), b"cancelled");
    }
}
fn load_job(root: &Path, id: &str) -> Result<(Job, Manifest, PathBuf)> {
    valid_id(id)?;
    let dir = root.join("updates/jobs").join(id);
    let job: Job = serde_json::from_slice(&read_small(&dir.join("job.json"), 16384)?)?;
    if job.id != id
        || job.tx.id != id
        || job.tx.journal != dir.join("phase")
        || job.tx.target.parent().is_none()
        || job.tx.staged != staged_path(&job.tx.target, id)
    {
        return Err(bad("安装任务路径无效"));
    }
    // Validate parent even when a crash left the application itself in the backup slot.
    let check = if job.tx.target.exists() {
        job.tx.target.clone()
    } else {
        job.tx.backup_path()
    };
    files::no_links(&check)?;
    let parent = job.tx.target.parent().unwrap();
    files::no_links(parent)?;
    let home = user_home()?.canonicalize()?;
    if !parent.canonicalize()?.starts_with(home) {
        return Err(bad("恢复位置超出用户目录"));
    }
    if job.tx.target.exists() {
        validate_slot(&job.tx.target)?;
    }
    let m = manifest::verify(
        &read_small(&dir.join("manifest.json"), manifest::MAX_ENVELOPE as u64)?,
        &manifest::pinned_key()?,
        job.prepared_at,
    )?;
    validate_entry(&m)?;
    if job.version != m.version || job.sequence != m.build_sequence {
        return Err(bad("安装任务身份不匹配"));
    }
    Ok((job, m, dir))
}
fn process_alive(pid: u32) -> bool {
    #[cfg(unix)]
    {
        unsafe {
            libc::kill(pid as i32, 0) == 0
                || std::io::Error::last_os_error().raw_os_error() == Some(libc::EPERM)
        }
    }
    #[cfg(windows)]
    {
        use windows::Win32::{
            Foundation::{CloseHandle, WAIT_TIMEOUT},
            System::Threading::{OpenProcess, WaitForSingleObject, PROCESS_SYNCHRONIZE},
        };
        unsafe {
            match OpenProcess(PROCESS_SYNCHRONIZE, false, pid) {
                Ok(handle) => {
                    let alive = WaitForSingleObject(handle, 0) == WAIT_TIMEOUT;
                    let _ = CloseHandle(handle);
                    alive
                }
                Err(_) => false,
            }
        }
    }
    #[cfg(not(any(unix, windows)))]
    {
        let _ = pid;
        true
    }
}
fn wait_exit(pid: u32) -> Result<()> {
    let deadline = Instant::now() + Duration::from_secs(120);
    while process_alive(pid) {
        if Instant::now() > deadline {
            return Err(bad("旧程序未退出，保留原应用"));
        }
        std::thread::sleep(Duration::from_millis(100));
    }
    Ok(())
}
fn launch(job: &Job, health: bool) -> Result<Child> {
    let entry = application_entry(&job.tx.target);
    files::regular(&entry)?;
    let mut command = Command::new(entry);
    if health {
        command.args(["--linknestx-update-health", &job.id, &job.nonce]);
    }
    Ok(command
        .current_dir(job.tx.target.parent().unwrap())
        .stdin(Stdio::null())
        .stdout(Stdio::null())
        .stderr(Stdio::null())
        .spawn()?)
}
fn helper(root: &Path, id: &str, recovery: Option<u32>) -> Result<()> {
    let (job, m, dir) = load_job(root, id)?;
    // The per-job lock excludes duplicate helpers before the old application releases the installation lock.
    let _job_lock = lock(&dir)?;
    let own = std::env::current_exe()?;
    if files::file_hash(&own, job.helper_size)? != job.helper_sha256 {
        return Err(bad("助手副本已变化"));
    }
    if let Some(pid) = recovery {
        wait_exit(pid)?;
        let _install = lock(&root.join("updates"))?;
        job.tx.rollback()?;
        launch(&job, false)?;
        return Ok(());
    }
    verify_staged(&m, &job.tx.staged)?;
    platform_verify(&job.tx.staged)?;
    files::atomic_write(&dir.join("helper-ready"), id.as_bytes())?;
    wait_exit(job.parent_pid)?;
    let _install = lock(&root.join("updates"))?;
    // Revalidate immediately before the swap, after the old process has stopped.
    let result = (|| -> Result<()> {
        manifest::verify(
            &read_small(&dir.join("manifest.json"), manifest::MAX_ENVELOPE as u64)?,
            &manifest::pinned_key()?,
            chrono::Utc::now().timestamp(),
        )?;
        verify_staged(&m, &job.tx.staged)?;
        validate_slot(&job.tx.target)?;
        apply_and_monitor(
            &job.tx,
            || launch(&job, true),
            &dir.join("health"),
            &health_payload(&job)?,
            Duration::from_secs(60),
        )
    })();
    if let Err(error) = result {
        job.tx.rollback()?;
        files::atomic_write(&dir.join("error"), error.error.as_bytes())?;
        launch(&job, false)?;
    }
    Ok(())
}
fn health_payload(job: &Job) -> Result<Vec<u8>> {
    Ok(serde_json::to_vec(
        &serde_json::json!({"id":job.id,"nonce":job.nonce,"version":job.version,"sequence":job.sequence}),
    )?)
}
pub fn acknowledge_health(root: &Path) -> Result<()> {
    let args: Vec<String> = std::env::args().collect();
    if args.get(1).map(String::as_str) != Some("--linknestx-update-health") {
        return Ok(());
    }
    if args.len() != 4 {
        return Err(bad("健康确认参数错误"));
    }
    let (job, m, dir) = load_job(root, &args[2])?;
    if args[3] != job.nonce
        || m.version != env!("CARGO_PKG_VERSION")
        || m.build_sequence != manifest::BUILD_SEQUENCE
        || std::env::current_exe()?.canonicalize()?
            != application_entry(&job.tx.target).canonicalize()?
    {
        return Err(bad("健康确认身份错误"));
    }
    files::atomic_write(&dir.join("health"), &health_payload(&job)?)
}
pub fn early_mode() -> Option<i32> {
    let args: Vec<String> = std::env::args().collect();
    let mode = args.get(1).map(String::as_str);
    if matches!(
        mode,
        Some("--linknestx-update-helper" | "--linknestx-update-recover")
    ) {
        let result = (|| {
            let root = data_root()?;
            if mode == Some("--linknestx-update-helper") && args.len() == 3 {
                helper(&root, &args[2], None)
            } else if mode == Some("--linknestx-update-recover") && args.len() == 4 {
                helper(
                    &root,
                    &args[2],
                    Some(args[3].parse().map_err(|_| bad("恢复进程无效"))?),
                )
            } else {
                Err(bad("助手参数无效"))
            }
        })();
        return Some(if result.is_ok() { 0 } else { 1 });
    }
    if mode == Some("--linknestx-update-health") {
        return None;
    }
    // An interrupted swap is repaired by the already-copied trusted helper, after this process exits.
    let result = (|| -> Result<bool> {
        let root = data_root()?;
        let pending = root.join("updates/pending");
        if !pending.exists() {
            return Ok(false);
        }
        let id = String::from_utf8(read_small(&pending, 64)?).map_err(|_| bad("待恢复标识无效"))?;
        valid_id(&id)?;
        let dir = root.join("updates/jobs").join(&id);
        let phase = read_small(&dir.join("phase"), 64)?;
        if matches!(phase.as_slice(), b"healthy" | b"rolledBack" | b"cancelled") {
            return Ok(false);
        }
        let (job, _, dir) = load_job(&root, &id)?;
        let _lock = lock(&root.join("updates"))?;
        let helper = helper_path(&dir);
        if files::file_hash(&helper, job.helper_size)? != job.helper_sha256 {
            return Err(bad("恢复助手校验失败"));
        }
        Command::new(helper)
            .args([
                "--linknestx-update-recover",
                &id,
                &std::process::id().to_string(),
            ])
            .stdin(Stdio::null())
            .stdout(Stdio::null())
            .stderr(Stdio::null())
            .spawn()?;
        Ok(true)
    })();
    match result {
        Ok(true) => Some(0),
        Ok(false) => None,
        Err(error) => {
            eprintln!("更新恢复失败：{error}");
            Some(1)
        }
    }
}
#[derive(Clone)]
pub struct RollbackOutcome {
    pub error: String,
    pub version: Option<String>,
    pub sequence: Option<u64>,
}
pub fn last_outcome(root: &Path) -> Option<RollbackOutcome> {
    let id = String::from_utf8(read_small(&root.join("updates/pending"), 64).ok()?).ok()?;
    valid_id(&id).ok()?;
    let dir = root.join("updates/jobs").join(&id);
    if read_small(&dir.join("phase"), 64).ok().as_deref() != Some(b"rolledBack")
        || read_small(&dir.join("acknowledged"), 64).ok().as_deref() == Some(id.as_bytes())
    {
        return None;
    }
    // Job metadata identifies the local failed attempt only. It never authorizes an update:
    // signature, accepted-sequence and payload checks remain mandatory in the consumer.
    let job = read_small(&dir.join("job.json"), 16384)
        .ok()
        .and_then(|bytes| serde_json::from_slice::<Job>(&bytes).ok())
        .filter(|job| job.id == id);
    let error = String::from_utf8(read_small(&dir.join("error"), 4096).unwrap_or_default())
        .ok()
        .filter(|s| !s.is_empty())
        .unwrap_or_else(|| "更新未完成，已恢复原版本".into());
    Some(RollbackOutcome {
        error,
        version: job.as_ref().map(|j| j.version.clone()),
        sequence: job.map(|j| j.sequence),
    })
}
pub fn acknowledge_outcome(root: &Path) -> Result<()> {
    let path = root.join("updates/pending");
    if !path.exists() {
        return Ok(());
    }
    let id = String::from_utf8(read_small(&path, 64)?).map_err(|_| bad("回滚记录标识无效"))?;
    valid_id(&id)?;
    let dir = root.join("updates/jobs").join(&id);
    if read_small(&dir.join("phase"), 64)?.as_slice() == b"rolledBack" {
        files::atomic_write(&dir.join("acknowledged"), id.as_bytes())?;
    }
    Ok(())
}

fn apply_and_monitor(
    tx: &Transaction,
    spawn: impl FnOnce() -> Result<Child>,
    health: &Path,
    expected: &[u8],
    timeout: Duration,
) -> Result<()> {
    let result = (|| -> Result<()> {
        tx.swap()?;
        let mut child = spawn()?;
        let deadline = Instant::now() + timeout;
        loop {
            match child.try_wait() {
                Ok(Some(_)) => return Err(bad("新应用在健康确认前退出")),
                Err(error) => {
                    let _ = child.kill();
                    let _ = child.wait();
                    return Err(error.into());
                }
                Ok(None) => {}
            }
            if read_small(health, 1024).ok().as_deref() == Some(expected) {
                tx.healthy()?;
                return Ok(());
            }
            if Instant::now() >= deadline {
                let _ = child.kill();
                let _ = child.wait();
                return Err(bad("新应用健康确认超时"));
            }
            std::thread::sleep(Duration::from_millis(50));
        }
    })();
    if result.is_err() {
        tx.rollback()?;
    }
    result
}

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn child_health_fixture() {
        let Some(path) = std::env::var_os("LINKNESTX_TEST_HEALTH") else {
            return;
        };
        let mode = std::env::var("LINKNESTX_TEST_HEALTH_MODE").unwrap();
        if mode == "crash" {
            std::process::exit(7);
        }
        if mode != "timeout" {
            fs::write(
                path,
                if mode == "healthy" {
                    b"correct-nonce".as_slice()
                } else {
                    b"wrong-nonce".as_slice()
                },
            )
            .unwrap();
        }
        std::thread::sleep(Duration::from_secs(2));
    }
    #[test]
    fn real_child_health_ack_commits_and_failure_rolls_back() {
        for mode in ["healthy", "wrong", "timeout", "crash"] {
            let root = tempfile::tempdir_in(std::env::temp_dir().canonicalize().unwrap()).unwrap();
            let target = root.path().join(if cfg!(windows) {
                "LinkNestX.exe"
            } else {
                "LinkNestX.app"
            });
            let stage = root.path().join("prepared");
            let (target_entry, stage_entry) = if cfg!(windows) {
                // Windows production transactions replace one executable with ReplaceFileW.
                (target.clone(), stage.clone())
            } else {
                fs::create_dir(&target).unwrap();
                fs::create_dir(&stage).unwrap();
                (target.join("entry"), stage.join("entry"))
            };
            fs::write(&target_entry, b"old").unwrap();
            fs::write(&stage_entry, b"new").unwrap();
            let tx = Transaction::new(
                target.clone(),
                stage,
                root.path().join("phase"),
                &uuid::Uuid::new_v4().to_string(),
            )
            .unwrap();
            let health = root.path().join("health");
            let result = apply_and_monitor(
                &tx,
                || {
                    Ok(Command::new(std::env::current_exe()?)
                        .args([
                            "--exact",
                            "update::install::tests::child_health_fixture",
                            "--nocapture",
                        ])
                        .env("LINKNESTX_TEST_HEALTH", &health)
                        .env("LINKNESTX_TEST_HEALTH_MODE", mode)
                        .stdout(Stdio::null())
                        .stderr(Stdio::null())
                        .spawn()?)
                },
                &health,
                b"correct-nonce",
                Duration::from_millis(400),
            );
            assert_eq!(result.is_ok(), mode == "healthy", "{mode}");
            assert_eq!(
                fs::read(&target_entry).unwrap(),
                if mode == "healthy" { b"new" } else { b"old" }
            );
        }
    }
    #[test]
    fn exclusive_helper_lock_releases_after_exit() {
        let root = tempfile::tempdir_in(std::env::temp_dir().canonicalize().unwrap()).unwrap();
        let a = lock(root.path()).unwrap();
        assert!(lock(root.path()).is_err());
        drop(a);
        assert!(lock(root.path()).is_ok());
    }
}
#[cfg(target_os = "macos")]
fn exchange_paths(a: &Path, b: &Path) -> Result<()> {
    use std::os::unix::ffi::OsStrExt;
    files::no_links(a)?;
    files::no_links(b)?;
    let a = std::ffi::CString::new(a.as_os_str().as_bytes()).map_err(|_| bad("路径编码无效"))?;
    let b = std::ffi::CString::new(b.as_os_str().as_bytes()).map_err(|_| bad("路径编码无效"))?;
    let code = unsafe {
        libc::renameatx_np(
            libc::AT_FDCWD,
            a.as_ptr(),
            libc::AT_FDCWD,
            b.as_ptr(),
            libc::RENAME_SWAP,
        )
    };
    if code != 0 {
        return Err(bad("当前文件系统不支持安全交换，请手动安装"));
    }
    Ok(())
}
#[cfg(all(test, target_os = "macos"))]
#[test]
fn interrupted_atomic_exchange_recovers_without_missing_target() {
    let root = tempfile::tempdir_in(std::env::temp_dir().canonicalize().unwrap()).unwrap();
    let target = root.path().join("LinkNestX.app");
    let stage = root.path().join("staged.app");
    fs::create_dir(&target).unwrap();
    fs::create_dir(&stage).unwrap();
    fs::write(target.join("entry"), b"old").unwrap();
    fs::write(stage.join("entry"), b"new").unwrap();
    let tx = Transaction::new(
        target.clone(),
        stage.clone(),
        root.path().join("phase"),
        &uuid::Uuid::new_v4().to_string(),
    )
    .unwrap();
    exchange_paths(&target, &stage).unwrap();
    assert_eq!(fs::read(target.join("entry")).unwrap(), b"new");
    tx.rollback().unwrap();
    assert_eq!(fs::read(target.join("entry")).unwrap(), b"old");
}

#[cfg(all(test, target_os = "macos"))]
#[test]
fn recovery_after_atomic_reverse_swap_is_idempotent_before_journal_write() {
    for bundle in [true, false] {
        let root = tempfile::tempdir_in(std::env::temp_dir().canonicalize().unwrap()).unwrap();
        let target = root.path().join(if bundle {
            "LinkNestX.app"
        } else {
            "LinkNestX.exe"
        });
        let stage = root.path().join("staged");
        if bundle {
            fs::create_dir(&target).unwrap();
            fs::create_dir(&stage).unwrap();
            fs::write(target.join("entry"), b"old").unwrap();
            fs::write(stage.join("entry"), b"new").unwrap();
        } else {
            fs::write(&target, b"old").unwrap();
            fs::write(&stage, b"new").unwrap();
        }
        let tx = Transaction::new(
            target.clone(),
            stage,
            root.path().join("phase"),
            &uuid::Uuid::new_v4().to_string(),
        )
        .unwrap();
        tx.swap().unwrap();
        // This is the OS fault boundary: reverse exchange committed, journal still awaitingHealth.
        exchange_paths(&target, &tx.backup_path()).unwrap();
        assert!(target.exists());
        tx.rollback().unwrap();
        tx.rollback().unwrap();
        assert_eq!(tree_digest(&target).unwrap(), tx.old_digest);
        assert_eq!(read_small(&tx.journal, 64).unwrap(), b"rolledBack");
    }
}

#[cfg(target_os = "windows")]
fn replace_file(target: &Path, replacement: &Path, backup: &Path) -> Result<()> {
    use std::os::windows::ffi::OsStrExt;
    use windows::{
        core::PCWSTR,
        Win32::Storage::FileSystem::{ReplaceFileW, REPLACE_FILE_FLAGS},
    };
    files::no_links(target)?;
    files::no_links(replacement)?;
    if backup.exists() {
        return Err(bad("备份位置已存在"));
    }
    let wide = |p: &Path| {
        p.as_os_str()
            .encode_wide()
            .chain(Some(0))
            .collect::<Vec<u16>>()
    };
    let target = wide(target);
    let replacement = wide(replacement);
    let backup = wide(backup);
    unsafe {
        ReplaceFileW(
            PCWSTR(target.as_ptr()),
            PCWSTR(replacement.as_ptr()),
            PCWSTR(backup.as_ptr()),
            REPLACE_FILE_FLAGS(0),
            None,
            None,
        )
        .map_err(|_| bad("Windows 文件替换失败，保留原程序"))?;
    }
    Ok(())
}

#[cfg(all(test, target_os = "macos"))]
#[test]
fn rollback_production_restore_checkpoint_keeps_entry_before_journal_finalization() {
    let root = tempfile::tempdir_in(std::env::temp_dir().canonicalize().unwrap()).unwrap();
    let target = root.path().join("LinkNestX.app");
    let stage = root.path().join("staged");
    fs::create_dir(&target).unwrap();
    fs::create_dir(&stage).unwrap();
    fs::write(target.join("entry"), b"old").unwrap();
    fs::write(stage.join("entry"), b"new").unwrap();
    let tx = Transaction::new(
        target.clone(),
        stage,
        root.path().join("phase"),
        &uuid::Uuid::new_v4().to_string(),
    )
    .unwrap();
    tx.swap().unwrap();
    tx.restore_original().unwrap();
    assert!(target.join("entry").is_file());
    assert_eq!(fs::read(target.join("entry")).unwrap(), b"old");
    assert_eq!(read_small(&tx.journal, 64).unwrap(), b"awaitingHealth");
    tx.rollback().unwrap();
    tx.rollback().unwrap();
    assert_eq!(fs::read(target.join("entry")).unwrap(), b"old");
    assert_eq!(fs::read(tx.failed_path().join("entry")).unwrap(), b"new");
}
