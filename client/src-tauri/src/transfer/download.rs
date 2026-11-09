use super::{
    models::*,
    scheduler,
    service::{validate_destination, TransferService},
};
use crate::{Result, TransferError};
use futures_util::{stream, StreamExt};
use sha2::{Digest, Sha256};
use std::{io::SeekFrom, path::Path};
use tokio::io::{AsyncReadExt, AsyncSeekExt, AsyncWriteExt};
use tokio_util::sync::CancellationToken;

impl TransferService {
    async fn manifest_for(&self, task: &Task) -> Result<Manifest> {
        let value = self
            .auth
            .json_for(
                &task.owner,
                "GET",
                &format!(
                    "/api/v1/file-versions/{}/manifest",
                    task.snapshot.version_id
                ),
                None,
            )
            .await?;
        let manifest: Manifest = serde_json::from_value(value)?;
        manifest.validate()?;
        if manifest.version_id != task.snapshot.version_id {
            return Err(TransferError::new("文件版本与清单不匹配"));
        }
        if task
            .manifest
            .as_ref()
            .is_some_and(|old| !old.immutable_eq(&manifest))
        {
            return Err(TransferError::new("不可变文件清单发生变化"));
        }
        Ok(manifest)
    }
    pub(crate) async fn download(&self, task: &mut Task, cancel: &CancellationToken) -> Result<()> {
        validate_destination(&task.path)?;
        let manifest = tokio::select! {_=cancel.cancelled()=>return Err(TransferError::new("传输已暂停")),m=self.manifest_for(task)=>m?};
        task.snapshot.name = manifest.name.clone();
        task.snapshot.total_bytes = manifest.size;
        task.verified.resize(manifest.chunks.len(), false);
        task.manifest = Some(manifest.clone());
        let partial = task
            .partial
            .clone()
            .ok_or_else(|| TransferError::new("临时文件路径缺失"))?;
        if Path::new(&partial).parent() != Path::new(&task.path).parent()
            || Path::new(&partial).file_name().and_then(|v| v.to_str())
                != Some(&format!(".linknestx-{}.part", task.snapshot.id))
        {
            return Err(TransferError::new("临时文件路径无效"));
        }
        let exists = match tokio::fs::symlink_metadata(&partial).await {
            Ok(meta) => {
                if !meta.is_file() || meta.file_type().is_symlink() {
                    return Err(TransferError::new("临时文件不是普通文件"));
                }
                true
            }
            Err(error) if error.kind() == std::io::ErrorKind::NotFound => false,
            Err(error) => return Err(error.into()),
        };
        let mut options = tokio::fs::OpenOptions::new();
        options.read(true).write(true);
        if !exists {
            options.create_new(true);
        }
        let mut file = options.open(&partial).await?;
        file.set_len(manifest.size).await?;
        // Persisted bits are only hints after a crash: verify bytes before counting them again.
        task.snapshot.bytes_completed = 0;
        for chunk in &manifest.chunks {
            if task.verified[chunk.index] {
                let mut bytes = vec![0; chunk.size as usize];
                file.seek(SeekFrom::Start(chunk.index as u64 * manifest.chunk_size))
                    .await?;
                let valid = file.read_exact(&mut bytes).await.is_ok() && hash(&bytes) == chunk.hash;
                task.verified[chunk.index] = valid;
                if valid {
                    task.snapshot.bytes_completed += chunk.size;
                }
            }
        }
        task.snapshot.state = TaskState::Transferring;
        self.persist(task)?;
        let sources = self.sources.read().await.clone();
        let concurrency = self.policy.read().await.max_concurrent.min(4);
        let pending: Vec<Chunk> = manifest
            .chunks
            .iter()
            .filter(|c| !task.verified[c.index])
            .cloned()
            .collect();
        let owner = task.owner.clone();
        let requests = stream::iter(pending)
            .map(|chunk| {
                let owner = &owner;
                let manifest = &manifest;
                let sources = &sources;
                async move {
                    let found = self.cache.read(owner, &manifest.version_id, &chunk).await?;
                    let (source, bytes) = match found {
                        Some(bytes) => ("cache".into(), bytes),
                        None => scheduler::fetch(sources, owner, manifest, &chunk, cancel).await?,
                    };
                    self.bandwidth
                        .wait(
                            chunk.size,
                            self.policy.read().await.bandwidth_limit_bps,
                            cancel,
                        )
                        .await?;
                    Ok::<_, TransferError>((chunk, source, bytes))
                }
            })
            .buffer_unordered(concurrency);
        tokio::pin!(requests);
        while let Some(result) = requests.next().await {
            if cancel.is_cancelled() {
                return Err(TransferError::new("传输已暂停"));
            }
            let (chunk, source, bytes) = result?;
            file.seek(SeekFrom::Start(chunk.index as u64 * manifest.chunk_size))
                .await?;
            file.write_all(&bytes).await?;
            file.sync_data().await?;
            self.verified(task, &chunk, &bytes).await?;
            task.verified[chunk.index] = true;
            task.snapshot.bytes_completed += chunk.size;
            task.snapshot.active_sources = vec![source];
            self.persist(task)?;
        }
        task.snapshot.state = TaskState::Verifying;
        task.snapshot.active_sources.clear();
        self.persist(task)?;
        file.seek(SeekFrom::Start(0)).await?;
        let mut digest = Sha256::new();
        let mut buffer = vec![0; 1024 * 1024];
        loop {
            if cancel.is_cancelled() {
                return Err(TransferError::new("传输已暂停"));
            }
            let size = file.read(&mut buffer).await?;
            if size == 0 {
                break;
            }
            digest.update(&buffer[..size]);
        }
        if format!("{:x}", digest.finalize()) != manifest.sha256 {
            return Err(TransferError::new("完整文件校验失败"));
        }
        // Recheck authorization immediately before making even a fully cached file visible.
        scheduler::cancellable(cancel, self.manifest_for(task)).await?;
        if cancel.is_cancelled() {
            return Err(TransferError::new("传输已暂停"));
        }
        file.sync_all().await?;
        drop(file);
        // Same-directory hard linking atomically creates a new destination and cannot replace one.
        tokio::fs::hard_link(&partial, &task.path)
            .await
            .map_err(|_| TransferError::new("无法完成保存，目标文件可能已存在"))?;
        let _ = tokio::fs::remove_file(&partial).await;
        task.snapshot.state = TaskState::Completed;
        Ok(())
    }
}
