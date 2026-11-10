use super::*;
impl PeerConnection {
    pub(super) async fn receive(self: &Arc<Self>, frame: Frame, bytes: Vec<u8>) {
        match &frame {
            Frame::Data { request_id, .. } => {
                let mut pending = self.pending.lock().unwrap();
                let Some(p) = pending.get_mut(request_id) else {
                    self.cancel.cancel();
                    return;
                };
                let outcome = p.assembly.push(&frame, &bytes);
                if outcome.is_ok() {
                    if let Some(observer) = &p.observer {
                        observer.payload(bytes.len());
                    }
                }
                match outcome {
                    Ok(None) => {}
                    Ok(Some(bytes)) => {
                        let p = pending.remove(request_id).unwrap();
                        let _ = p.result.send(Ok(bytes));
                    }
                    Err(e) => {
                        let p = pending.remove(request_id).unwrap();
                        let _ = p.result.send(Err(e));
                        self.cancel.cancel();
                    }
                }
            }
            Frame::Unavailable { request_id } => {
                if let Some(p) = self.pending.lock().unwrap().remove(request_id) {
                    let _ = p.result.send(Err(TransferError::new("对等分块不可用")));
                }
            }
            Frame::Cancel { request_id } => {
                if let Some(cancel) = self.serving.lock().unwrap().get(request_id) {
                    cancel.cancel();
                }
            }
            Frame::Request { request_id, .. } => {
                if uuid::Uuid::parse_str(request_id).is_err() {
                    self.cancel.cancel();
                    return;
                }
                {
                    let mut seen = self.seen.lock().unwrap();
                    if seen.len() >= 4096 || !seen.insert(request_id.clone()) {
                        self.cancel.cancel();
                        return;
                    }
                }
                let token = self.cancel.child_token();
                {
                    let mut serving = self.serving.lock().unwrap();
                    if serving.len() >= 4 {
                        let _ = self.control.try_send(Frame::Unavailable {
                            request_id: request_id.clone(),
                        });
                        return;
                    }
                    serving.insert(request_id.clone(), token.clone());
                }
                let c = self.clone();
                let request = request_id.clone();
                tokio::spawn(async move {
                    let result = tokio::select! {_=token.cancelled()=>Err(TransferError::new("对等请求已取消")),r=tokio::time::timeout(Duration::from_secs(30),c.serve(frame,&token))=>r.unwrap_or_else(|_|Err(TransferError::new("对等服务超时")))};
                    c.serving.lock().unwrap().remove(&request);
                    if result.is_err() && !token.is_cancelled() {
                        let _ = c.control.try_send(Frame::Unavailable {
                            request_id: request,
                        });
                    }
                });
            }
        }
    }
    async fn serve(&self, request: Frame, cancel: &CancellationToken) -> Result<()> {
        let Frame::Request {
            task_id,
            request_id,
            version_id,
            index,
            hash,
            grant,
        } = request
        else {
            return Err(TransferError::new("对等请求无效"));
        };
        uuid::Uuid::parse_str(&task_id).map_err(|_| TransferError::new("对等任务标识无效"))?;
        let manager = self
            .manager
            .upgrade()
            .ok_or_else(|| TransferError::new("共享服务已停止"))?;
        let _activity = manager.auth.activity.try_enter()?;
        let policy = manager.policy.read().await.clone();
        if !policy.enabled {
            return Err(TransferError::new("共享已关闭"));
        }

        if version_id != self.version {
            return Err(TransferError::new("对等版本不匹配"));
        }
        let owner = manager.auth.owner().await?;
        let manifest = manager.refresh_manifest(&owner, &version_id).await?;
        let effective = manifest.sharing_policy.effective(&policy);
        if !effective.enabled {
            return Err(TransferError::new("管理员已暂停此文件的对等共享"));
        }
        let _permit = manager.upload_permit(effective.max_concurrent)?;
        let version_limiter = manager
            .version_bandwidth
            .lock()
            .await
            .entry(version_id.clone())
            .or_default()
            .clone();
        let chunk = manifest
            .chunks
            .get(index)
            .ok_or_else(|| TransferError::new("分块不存在"))?;
        if chunk.hash != hash {
            return Err(TransferError::new("对等分块不匹配"));
        }
        grant.bind(&self.remote_user, &self.remote_device, &version_id, chunk)?;
        let owner = manager.auth.owner().await?;
        // Server authorization is mandatory for every chunk, before opening local bytes.
        manager
            .auth
            .json_for(
                &owner,
                "POST",
                "/api/v1/p2p/grants/validate",
                Some(json!({"grant":grant})),
            )
            .await?;
        let bytes = manager
            .cache
            .read(&owner, &version_id, chunk)
            .await?
            .ok_or_else(|| TransferError::new("本地分块已清理"))?;
        let mut audit = SentAudit {
            outbox: manager.audit.clone(),
            owner: owner.clone(),
            record: super::super::audit::AuditRecord {
                task_id,
                version_id: version_id.clone(),
                node_id: self.remote_device.clone(),
                action: "peer.sent".into(),
                bytes: 0,
            },
            index,
            version_size: manifest.size,
            sent: 0,
            events: manager.events.clone(),
        };
        manager
            .audit
            .record(&owner, &audit.record, index, 0, manifest.size)?;
        for (part, data) in bytes.chunks(protocol::PAYLOAD).enumerate() {
            let policy = manager.policy.read().await.clone();
            if !policy.enabled {
                return Err(TransferError::new("共享已关闭"));
            }
            version_limiter
                .wait(
                    data.len() as u64,
                    manifest.sharing_policy.bandwidth_limit_bps,
                    cancel,
                )
                .await?;
            manager
                .bandwidth
                .wait(data.len() as u64, policy.bandwidth_limit_bps, cancel)
                .await?;
            let offset = part * protocol::PAYLOAD;
            let end = offset + data.len() == bytes.len();
            self.send(
                &Frame::Data {
                    request_id: request_id.clone(),
                    version_id: version_id.clone(),
                    index,
                    hash: hash.clone(),
                    offset: offset as u64,
                    end,
                },
                data,
                cancel,
            )
            .await?;
            audit.sent += data.len() as u64;
        }
        manager.events.emit(
            "peer.sent",
            json!({"versionId":version_id,"nodeId":self.remote_device,"bytes":chunk.size}),
        );
        Ok(())
    }
}

struct SentAudit {
    outbox: Arc<super::super::audit::AuditOutbox>,
    owner: String,
    record: super::super::audit::AuditRecord,
    index: usize,
    version_size: u64,
    sent: u64,
    events: Arc<dyn crate::events::EventSink>,
}
impl Drop for SentAudit {
    fn drop(&mut self) {
        if self.sent > 0 {
            if let Err(error) = self.outbox.record(
                &self.owner,
                &self.record,
                self.index,
                self.sent,
                self.version_size,
            ) {
                self.events
                    .emit("service.error", json!({"error":error.error}));
            }
        }
    }
}
