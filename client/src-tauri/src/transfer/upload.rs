use super::{models::*, scheduler::cancellable, service::TransferService};
use crate::{Result, TransferError};
use serde::Deserialize;
use serde_json::json;
use sha2::{Digest, Sha256};
use std::{io::SeekFrom, path::Path};
use tokio::io::{AsyncReadExt, AsyncSeekExt};
use tokio_util::sync::CancellationToken;

#[derive(Deserialize)]
#[serde(rename_all = "camelCase")]
struct UploadSession {
    upload_id: String,
    file_id: String,
    version_id: String,
    missing_indexes: Vec<usize>,
    #[serde(default)]
    completed: bool,
}
impl TransferService {
    async fn hash_upload(&self, task: &mut Task, cancel: &CancellationToken) -> Result<Manifest> {
        task.snapshot.state = TaskState::Hashing;
        self.persist(task)?;
        let mut file = tokio::fs::File::open(&task.path).await?;
        let metadata = file.metadata().await?;
        let size = metadata.len();
        if !metadata.is_file() || size > MAX_FILE_SIZE {
            return Err(TransferError::new("文件超过 20GB 上限或不是普通文件"));
        }
        let mut chunks = Vec::new();
        let mut digest = Sha256::new();
        let mut total = 0;
        loop {
            if cancel.is_cancelled() {
                return Err(TransferError::new("传输已暂停"));
            }
            let mut bytes = vec![0; DEFAULT_CHUNK_SIZE as usize];
            let mut count = 0;
            while count < bytes.len() {
                let next = file.read(&mut bytes[count..]).await?;
                if next == 0 {
                    break;
                }
                count += next;
            }
            if count == 0 {
                break;
            }
            bytes.truncate(count);
            total += count as u64;
            if total > size {
                return Err(TransferError::new("文件在读取时发生变化"));
            }
            digest.update(&bytes);
            chunks.push(Chunk {
                index: chunks.len(),
                hash: hash(&bytes),
                size: count as u64,
            });
        }
        if total != size {
            return Err(TransferError::new("文件在读取时发生变化"));
        }
        Ok(Manifest {
            file_id: task.file_id.clone().unwrap_or_else(|| "pending".into()),
            version_id: "pending".into(),
            name: Path::new(&task.path)
                .file_name()
                .and_then(|v| v.to_str())
                .ok_or_else(|| TransferError::new("文件名无效"))?
                .into(),
            size,
            sha256: format!("{:x}", digest.finalize()),
            chunk_size: DEFAULT_CHUNK_SIZE,
            chunks,
            expires_at: None,
            visibility: "group".into(),
        })
    }
    pub(crate) async fn upload(&self, task: &mut Task, cancel: &CancellationToken) -> Result<()> {
        let mut manifest = if let Some(manifest) = &task.manifest {
            manifest.clone()
        } else {
            self.hash_upload(task, cancel).await?
        };
        manifest.validate()?;
        task.snapshot.total_bytes = manifest.size;
        task.snapshot.name = manifest.name.clone();
        let session: UploadSession = if let Some(id) = &task.upload_id {
            segment(id)?;
            serde_json::from_value(
                cancellable(
                    cancel,
                    self.auth
                        .json_for(&task.owner, "GET", &format!("/api/v1/uploads/{id}"), None),
                )
                .await?,
            )?
        } else {
            let body = json!({"groupId":task.group_id,"name":manifest.name,"size":manifest.size,"sha256":manifest.sha256,"chunkSize":manifest.chunk_size,"chunks":manifest.chunks,"fileId":task.file_id});
            serde_json::from_value(
                cancellable(
                    cancel,
                    self.auth
                        .json_for(&task.owner, "POST", "/api/v1/files/uploads", Some(body)),
                )
                .await?,
            )?
        };
        segment(&session.upload_id)?;
        segment(&session.file_id)?;
        segment(&session.version_id)?;
        if let Some(previous) = &task.upload_id {
            if previous != &session.upload_id
                || task.snapshot.version_id != session.version_id
                || task.file_id.as_ref() != Some(&session.file_id)
            {
                return Err(TransferError::new("上传会话发生变化"));
            }
        }
        if session.completed && !session.missing_indexes.is_empty() {
            return Err(TransferError::new("已发布上传仍包含缺失分块"));
        }
        let mut missing = vec![false; manifest.chunks.len()];
        for index in session.missing_indexes {
            if index >= missing.len() || missing[index] {
                return Err(TransferError::new("服务器缺失分块清单无效"));
            }
            missing[index] = true;
        }
        task.upload_id = Some(session.upload_id.clone());
        task.file_id = Some(session.file_id.clone());
        task.snapshot.version_id = session.version_id.clone();
        manifest.version_id = session.version_id;
        manifest.file_id = session.file_id;
        task.manifest = Some(manifest.clone());
        task.verified = missing.iter().map(|m| !m).collect();
        task.snapshot.bytes_completed = manifest
            .chunks
            .iter()
            .filter(|c| !missing[c.index])
            .map(|c| c.size)
            .sum();
        task.snapshot.state = TaskState::Transferring;
        self.persist(task)?;
        if session.completed {
            task.snapshot.state = TaskState::Completed;
            return Ok(());
        }
        let mut source = if missing.iter().any(|missing| *missing) {
            let file = tokio::fs::File::open(&task.path).await?;
            if file.metadata().await?.len() != manifest.size {
                return Err(TransferError::new("源文件已更改"));
            }
            Some(file)
        } else {
            None
        };
        for chunk in &manifest.chunks {
            if cancel.is_cancelled() {
                return Err(TransferError::new("传输已暂停"));
            }
            if !missing[chunk.index] {
                continue;
            }
            let file = source
                .as_mut()
                .ok_or_else(|| TransferError::new("源文件不可用"))?;
            file.seek(SeekFrom::Start(chunk.index as u64 * manifest.chunk_size))
                .await?;
            let mut bytes = vec![0; chunk.size as usize];
            file.read_exact(&mut bytes).await?;
            if hash(&bytes) != chunk.hash {
                return Err(TransferError::new("源文件已更改，请重新创建上传任务"));
            }
            self.bandwidth
                .wait(
                    chunk.size,
                    self.policy.read().await.bandwidth_limit_bps,
                    cancel,
                )
                .await?;
            let chunk_path = format!(
                "/api/v1/uploads/{}/chunks/{}",
                session.upload_id, chunk.index
            );
            let put =
                self.auth
                    .request_for(&task.owner, "PUT", &chunk_path, Some(bytes.clone()), false);
            tokio::select! {_=cancel.cancelled()=>return Err(TransferError::new("传输已暂停")),response=put=>{response?;}}
            self.verified(task, chunk, &bytes).await?;
            task.verified[chunk.index] = true;
            task.snapshot.bytes_completed += chunk.size;
            task.snapshot.active_sources = vec!["server".into()];
            self.persist(task)?;
        }
        if cancel.is_cancelled() {
            return Err(TransferError::new("传输已暂停"));
        }
        task.snapshot.state = TaskState::Verifying;
        self.persist(task)?;
        cancellable(
            cancel,
            self.auth.json_for(
                &task.owner,
                "POST",
                &format!("/api/v1/uploads/{}/complete", session.upload_id),
                Some(json!({})),
            ),
        )
        .await?;
        task.snapshot.state = TaskState::Completed;
        Ok(())
    }
}
