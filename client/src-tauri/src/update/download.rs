use super::{
    files,
    manifest::{bad, hash, Block, Manifest, PayloadFile},
};
use crate::Result;
use futures_util::StreamExt;
use std::{
    io::{Read, Seek, SeekFrom, Write},
    path::Path,
    time::Duration,
};
#[derive(Default, Clone, Debug)]
pub struct Progress {
    pub downloaded_bytes: u64,
    pub reused_bytes: u64,
}
pub fn client() -> Result<reqwest::Client> {
    Ok(reqwest::Client::builder()
        .redirect(reqwest::redirect::Policy::none())
        .connect_timeout(Duration::from_secs(10))
        .timeout(Duration::from_secs(600))
        .no_proxy()
        .build()?)
}
pub async fn manifest_bytes(client: &reqwest::Client, url: &str) -> Result<Option<Vec<u8>>> {
    let response = client.get(url).send().await?;
    if response.status() == reqwest::StatusCode::NOT_FOUND {
        return Ok(None);
    }
    if response.status() != reqwest::StatusCode::OK {
        return Err(bad("更新清单响应状态无效"));
    }
    let mut bytes = Vec::new();
    let mut stream = response.bytes_stream();
    while let Some(chunk) = stream.next().await {
        let chunk = chunk?;
        if bytes.len() + chunk.len() > super::manifest::MAX_ENVELOPE {
            return Err(bad("清单超限"));
        }
        bytes.extend(chunk);
    }
    Ok(Some(bytes))
}
async fn fetch(
    client: &reqwest::Client,
    origin: &str,
    id: &str,
    size: u64,
    path: &Path,
    p: &mut Progress,
    notify: &impl Fn(Progress),
) -> Result<()> {
    let response = client
        .get(format!("{origin}/updates/objects/{id}"))
        .header("Accept-Encoding", "identity")
        .send()
        .await?;
    // Object requests are whole objects. Never interpret 206/range data or redirects as a block.
    if response.status() != reqwest::StatusCode::OK
        || response.headers().contains_key("content-range")
        || response
            .headers()
            .get("content-encoding")
            .is_some_and(|v| v != "identity")
        || response.content_length().is_some_and(|n| n != size)
    {
        return Err(bad("对象响应状态或长度无效"));
    }
    let parent = path.parent().ok_or_else(|| bad("缺少目录"))?;
    files::directory(parent)?;
    let mut file = tempfile::NamedTempFile::new_in(parent)?;
    let mut stream = response.bytes_stream();
    let mut received = 0u64;
    let started = tokio::time::Instant::now();
    while let Some(chunk) = stream.next().await {
        let chunk = chunk?;
        p.downloaded_bytes += chunk.len() as u64;
        notify(p.clone());
        received += chunk.len() as u64;
        if received > size {
            return Err(bad("对象超出签名长度"));
        }
        file.write_all(&chunk)?;
        // One request at a time, capped at 4 MiB/s; chat and user transfers retain capacity.
        tokio::time::sleep_until(
            started + Duration::from_secs_f64(received as f64 / (4. * 1024. * 1024.)),
        )
        .await;
    }
    file.as_file().sync_all()?;
    if received != size || files::file_hash(file.path(), size)? != id {
        return Err(bad("对象完整性校验失败"));
    }
    file.persist(path).map_err(|_| bad("无法保存更新对象"))?;
    Ok(())
}
fn reuse(base: &Path, b: &Block) -> Result<Vec<u8>> {
    let path = b.base_path.as_ref().ok_or_else(|| bad("无可复用块"))?;
    let mut file = files::regular(&base.join(path))?;
    if b.base_offset
        .checked_add(b.size)
        .is_none_or(|end| end > file.metadata().map(|m| m.len()).unwrap_or(0))
    {
        return Err(bad("基线块越界"));
    }
    file.seek(SeekFrom::Start(b.base_offset))?;
    let mut bytes = vec![0; b.size as usize];
    file.read_exact(&mut bytes)?;
    if hash(&bytes) != b.sha256 {
        return Err(bad("基线块已变化"));
    }
    Ok(bytes)
}
async fn delta(
    m: &PayloadFile,
    base: &Path,
    stage: &Path,
    cache: &Path,
    client: &reqwest::Client,
    origin: &str,
    p: &mut Progress,
    notify: &impl Fn(Progress),
) -> Result<()> {
    let path = stage.join(&m.path);
    let parent = path.parent().ok_or_else(|| bad("缺少目录"))?;
    files::directory(parent)?;
    let mut output = tempfile::NamedTempFile::new_in(parent)?;
    for block in &m.blocks {
        if let Ok(bytes) = reuse(base, block) {
            output.write_all(&bytes)?;
            p.reused_bytes += bytes.len() as u64;
            notify(p.clone());
        } else {
            let object = cache.join(&block.sha256);
            if files::file_hash(&object, block.size).ok().as_deref() != Some(&block.sha256) {
                fetch(
                    client,
                    origin,
                    &block.sha256,
                    block.size,
                    &object,
                    p,
                    notify,
                )
                .await?;
            }
            let mut source = files::regular(&object)?;
            std::io::copy(&mut source, &mut output)?;
        }
    }
    output.as_file().sync_all()?;
    if files::file_hash(output.path(), m.size)? != m.sha256 {
        return Err(bad("重构文件完整性校验失败"));
    }
    files::set_mode(output.path(), m.mode)?;
    output.persist(&path).map_err(|_| bad("无法保存重构文件"))?;
    Ok(())
}
pub async fn prepare(
    m: &Manifest,
    base: &Path,
    stage: &Path,
    cache: &Path,
    origin: &str,
    notify: impl Fn(Progress),
) -> Result<Progress> {
    files::directory(stage)?;
    files::directory(cache)?;
    let client = client()?;
    let mut p = Progress::default();
    for file in &m.files {
        let reused_before = p.reused_bytes;
        if delta(file, base, stage, cache, &client, origin, &mut p, &notify)
            .await
            .is_err()
        {
            // Signed full target fallback; failed reconstruction is never credited as reuse.
            p.reused_bytes = reused_before;
            notify(p.clone());
            let target = stage.join(&file.path);
            fetch(
                &client,
                origin,
                &file.sha256,
                file.size,
                &target,
                &mut p,
                &notify,
            )
            .await?;
            files::set_mode(&target, file.mode)?;
        }
    }
    files::verify_tree(stage, m)?;
    Ok(p)
}
