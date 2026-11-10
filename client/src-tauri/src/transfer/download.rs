use super::{
    models::*,
    scheduler,
    service::{validate_destination, TransferService},
};
use crate::{Result, TransferError};
use sha2::{Digest, Sha256};
use std::{io::SeekFrom, path::Path, sync::Arc};
use tokio::io::{AsyncReadExt, AsyncSeekExt, AsyncWriteExt};
use tokio_util::sync::CancellationToken;

impl TransferService {
    async fn download_chunk(
        &self,
        owner: &str,
        task_id: &str,
        manifest: &Manifest,
        chunk: Chunk,
        health: &scheduler::SourceHealth,
        cancel: &CancellationToken,
    ) -> Result<(Chunk, String, Vec<u8>)> {
        let found =
            scheduler::cancellable(cancel, self.cache.read(owner, &manifest.version_id, &chunk))
                .await?;
        let (source, bytes) = match found {
            Some(bytes) => ("cache".into(), bytes),
            None => {
                let mut sources = scheduler::cancellable(cancel, async {
                    Ok(tokio::time::timeout(
                        std::time::Duration::from_secs(3),
                        self.peers.sources(manifest),
                    )
                    .await
                    .unwrap_or_default())
                })
                .await?;
                sources.extend(self.sources.read().await.clone());
                scheduler::fetch_with_health(
                    &sources, owner, manifest, &chunk, cancel, health, task_id,
                )
                .await?
            }
        };
        if source.starts_with("peer:") {
            if let Err(error) = self.peers.audit.record(
                owner,
                &crate::peer::audit::AuditRecord {
                    task_id: task_id.into(),
                    version_id: manifest.version_id.clone(),
                    node_id: source.trim_start_matches("peer:").into(),
                    action: "peer.received".into(),
                    bytes: 0,
                },
                chunk.index,
                chunk.size,
                manifest.size,
            ) {
                self.events.emit(
                    "service.error",
                    serde_json::json!({"error":format!("传输审计记录失败：{}",error.error)}),
                );
            }
        }
        let bandwidth_limit = self.policy.read().await.bandwidth_limit_bps;
        self.bandwidth
            .wait(chunk.size, bandwidth_limit, cancel)
            .await?;
        Ok((chunk, source, bytes))
    }
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
    pub(crate) async fn download(
        self: &Arc<Self>,
        task: &mut Task,
        cancel: &CancellationToken,
    ) -> Result<()> {
        task.snapshot.state = TaskState::Verifying;
        self.persist(task)?;
        validate_destination(&task.path)?;
        let manifest = tokio::select! {_=cancel.cancelled()=>return Err(TransferError::new("传输已暂停")),m=self.manifest_for(task)=>m?};
        self.cache
            .refresh_expiry(
                &task.owner,
                &manifest.version_id,
                manifest.expires_at.as_deref(),
            )
            .await?;
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
        if let Some(m) = super::metrics::observer(&task.owner, &task.snapshot.id) {
            m.resumed(task.snapshot.bytes_completed);
        }
        task.snapshot.state = TaskState::Transferring;
        self.persist(task)?;
        // Coordination is optional and has one overall startup deadline. Failure
        // leaves authenticated HTTP available without trusting an allocation as ACL.
        let (initial_sources, allocation) = scheduler::cancellable(cancel, async {
            Ok(
                tokio::time::timeout(std::time::Duration::from_secs(3), async {
                    let sources = self.peers.sources(&manifest).await;
                    let allocation = if sources
                        .iter()
                        .any(|source| manifest.chunks.iter().any(|chunk| source.has_chunk(chunk)))
                    {
                        None
                    } else {
                        self.peers.allocate(&manifest, &task.snapshot.id).await
                    };
                    (sources, allocation)
                })
                .await
                .unwrap_or_default(),
            )
        })
        .await?;
        let concurrency = self.policy.read().await.max_concurrent.min(4);
        let mut pending: Vec<Chunk> = manifest
            .chunks
            .iter()
            .filter(|c| !task.verified[c.index])
            .cloned()
            .collect();
        let initial_count = if let Some(allocation) = &allocation {
            Some(allocation.prioritize(&mut pending, concurrency))
        } else {
            // Current verified availability wins when the swarm is already warm.
            pending.sort_by_key(|chunk| {
                let count = initial_sources
                    .iter()
                    .filter(|source| source.has_chunk(chunk))
                    .count();
                if count == 0 {
                    usize::MAX
                } else {
                    count
                }
            });
            None
        };
        let health = Arc::new(scheduler::SourceHealth::default());
        let mut pending = pending.into_iter();
        let mut seed_remaining = initial_count;
        let mut requests = tokio::task::JoinSet::new();
        // Independently poll each bounded request. A cached read may hold the cache
        // operation lock across disk I/O while the consumer stores a completed sibling.
        // Keeping those reads inside buffer_unordered would deadlock that store.
        let transfer_result: Result<()> = async {
            loop {
                if seed_remaining == Some(0) && requests.is_empty() {
                    scheduler::cancellable(cancel, async {
                        self.peers.wait_for_partial_sources(&manifest).await;
                        Ok(())
                    })
                    .await?;
                    health.initial_server_batch_completed();
                    seed_remaining = None;
                }
                while requests.len() < concurrency && seed_remaining != Some(0) {
                    let Some(chunk) = pending.next() else { break };
                    if let Some(remaining) = &mut seed_remaining {
                        *remaining -= 1;
                    }
                    let service = self.clone();
                    let owner = task.owner.clone();
                    let manifest = manifest.clone();
                    let health = health.clone();
                    let task_id = task.snapshot.id.clone();
                    let cancel = cancel.clone();
                    requests.spawn(async move {
                        service
                            .download_chunk(&owner, &task_id, &manifest, chunk, &health, &cancel)
                            .await
                    });
                }
                let result = tokio::select! {
                    biased;
                    _ = cancel.cancelled() => return Err(TransferError::new("传输已暂停")),
                    next = requests.join_next() => next,
                };
                let Some(result) = result else { break };
                let (chunk, source, bytes) =
                    result.map_err(|_| TransferError::new("下载分块任务终止"))??;
                file.seek(SeekFrom::Start(chunk.index as u64 * manifest.chunk_size))
                    .await?;
                file.write_all(&bytes).await?;
                file.sync_data().await?;
                self.verified(task, &chunk, &bytes).await?;
                if source == "server" || source.starts_with("peer:") {
                    if let Err(error) = self.peers.audit.stage_completion(
                        &task.owner,
                        &crate::peer::audit::AuditRecord {
                            task_id: task.snapshot.id.clone(),
                            version_id: manifest.version_id.clone(),
                            node_id: source.trim_start_matches("peer:").into(),
                            action: "transfer.completed".into(),
                            bytes: 0,
                        },
                        chunk.index,
                        chunk.size,
                        manifest.size,
                    ) {
                        self.events.emit(
                        "service.error",
                        serde_json::json!({"error":format!("传输审计记录失败：{}", error.error)}),
                    );
                    }
                }
                if let Some(m) = super::metrics::observer(&task.owner, &task.snapshot.id) {
                    m.credit(chunk.index, chunk.size, source == "cache");
                }
                task.verified[chunk.index] = true;
                task.snapshot.bytes_completed += chunk.size;
                if !task.snapshot.active_sources.contains(&source) {
                    task.snapshot.active_sources.push(source);
                }
                self.persist(task)?;
            }
            Ok(())
        }
        .await;
        // Drain cancelled requests before pause, logout, or retry can reuse the task.
        requests.shutdown().await;
        transfer_result?;
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
        if let Err(error) = self.peers.audit.publish_completion(
            &task.owner,
            &task.snapshot.id,
            &manifest.version_id,
        ) {
            self.events.emit("service.error", serde_json::json!({"error":format!("文件已保存，完成审计将在后台重试：{}", error.error)}));
        }
        task.snapshot.state = TaskState::Completed;
        if let Err(error) = self.peers.audit.logical_completion(
            &task.owner,
            &task.snapshot.id,
            &manifest.version_id,
            manifest.size,
        ) {
            self.events.emit("service.error",serde_json::json!({"error":format!("文件已保存，完成审计将在后台重试：{}",error.error)}));
        }
        Ok(())
    }
}
