mod serving;
use super::{
    protocol::{self, Assembly, Frame, Grant},
    PeerManager,
};
use crate::{
    transfer::models::{Chunk, Manifest},
    Result, TransferError,
};
use bytes::Bytes;
use serde_json::json;
use std::{
    collections::HashMap,
    sync::{Arc, Mutex as StdMutex, Weak},
    time::Duration,
};
use tokio::sync::{mpsc, oneshot, Mutex, Notify, OnceCell};
use tokio_util::sync::CancellationToken;
use webrtc::{
    api::{setting_engine::SettingEngine, APIBuilder},
    data_channel::{
        data_channel_message::DataChannelMessage, data_channel_state::RTCDataChannelState,
        RTCDataChannel,
    },
    ice::network_type::NetworkType,
    ice_transport::{
        ice_candidate::RTCIceCandidateInit, ice_connection_state::RTCIceConnectionState,
        ice_server::RTCIceServer,
    },
    peer_connection::{configuration::RTCConfiguration, RTCPeerConnection},
};

pub(super) fn rtc_error(_: webrtc::Error) -> TransferError {
    TransferError::new("无法建立对等加密连接")
}
struct Pending {
    observer: Option<Arc<crate::transfer::metrics::Live>>,
    assembly: Assembly,
    result: oneshot::Sender<Result<Vec<u8>>>,
}
pub(super) struct PeerConnection {
    pub pc: Arc<RTCPeerConnection>,
    pub remote_device: String,
    pub remote_user: String,
    pub version: String,
    pub cancel: CancellationToken,
    pub expired: std::sync::atomic::AtomicBool,
    id: String,
    manager: Weak<PeerManager>,
    channel: OnceCell<Arc<RTCDataChannel>>,
    opened: Notify,
    pending: StdMutex<HashMap<String, Pending>>,
    serving: StdMutex<HashMap<String, CancellationToken>>,
    seen: StdMutex<std::collections::HashSet<String>>,
    ice: Mutex<Vec<RTCIceCandidateInit>>,
    control: mpsc::Sender<Frame>,
}
impl PeerConnection {
    pub async fn new(
        manager: &Arc<PeerManager>,
        id: String,
        version: String,
        device: String,
        user: String,
        ice_servers: Vec<RTCIceServer>,
        initiator: bool,
    ) -> Result<Arc<Self>> {
        let mut settings = SettingEngine::default();
        settings.set_network_types(vec![NetworkType::Udp4, NetworkType::Udp6]);
        settings.set_include_loopback_candidate(true);
        let api = APIBuilder::new().with_setting_engine(settings).build();
        let pc = Arc::new(
            api.new_peer_connection(RTCConfiguration {
                ice_servers,
                ..Default::default()
            })
            .await
            .map_err(rtc_error)?,
        );
        let (control, mut controls) = mpsc::channel(16);
        let conn = Arc::new(Self {
            pc: pc.clone(),
            remote_device: device,
            remote_user: user,
            version,
            id,
            manager: Arc::downgrade(manager),
            channel: OnceCell::new(),
            opened: Notify::new(),
            cancel: CancellationToken::new(),
            expired: std::sync::atomic::AtomicBool::new(false),
            pending: StdMutex::new(HashMap::new()),
            serving: StdMutex::new(HashMap::new()),
            seen: StdMutex::new(std::collections::HashSet::new()),
            ice: Mutex::new(Vec::new()),
            control,
        });
        let weak = Arc::downgrade(&conn);
        pc.on_ice_connection_state_change(Box::new(move |state| {
            let weak = weak.clone();
            Box::pin(async move {
                if matches!(
                    state,
                    RTCIceConnectionState::Disconnected
                        | RTCIceConnectionState::Failed
                        | RTCIceConnectionState::Closed
                ) {
                    if let Some(c) = weak.upgrade() {
                        c.cancel.cancel();
                        c.fail_pending();
                    }
                }
            })
        }));
        let weak = Arc::downgrade(&conn);
        pc.on_data_channel(Box::new(move |channel| {
            let weak = weak.clone();
            Box::pin(async move {
                if let Some(c) = weak.upgrade() {
                    c.attach(channel).await;
                }
            })
        }));
        if initiator {
            let channel = pc
                .create_data_channel("linknestx-chunks-v1", None)
                .await
                .map_err(rtc_error)?;
            conn.attach(channel).await;
        }
        let weak = Arc::downgrade(&conn);
        let cancel = conn.cancel.clone();
        tokio::spawn(async move {
            loop {
                tokio::select! {_=cancel.cancelled()=>break,frame=controls.recv()=>{let Some(frame)=frame else{break};let Some(c)=weak.upgrade() else{break};let _=c.send(&frame,&[],&cancel).await;}}
            }
        });
        let weak = Arc::downgrade(&conn);
        let cancel = conn.cancel.clone();
        tokio::spawn(async move {
            cancel.cancelled().await;
            if let Some(c) = weak.upgrade() {
                c.fail_pending();
                let _ = tokio::time::timeout(Duration::from_millis(500), c.pc.close()).await;
            }
        });
        Ok(conn)
    }
    async fn attach(self: &Arc<Self>, channel: Arc<RTCDataChannel>) {
        if channel.label() != "linknestx-chunks-v1" || self.channel.set(channel.clone()).is_err() {
            let _ = channel.close().await;
            return;
        }
        let weak = Arc::downgrade(self);
        channel.on_open(Box::new(move || {
            let weak = weak.clone();
            Box::pin(async move {
                if let Some(c) = weak.upgrade() {
                    c.opened.notify_one();
                }
            })
        }));
        let weak = Arc::downgrade(self);
        channel.on_close(Box::new(move || {
            let weak = weak.clone();
            Box::pin(async move {
                if let Some(c) = weak.upgrade() {
                    c.cancel.cancel();
                    c.fail_pending();
                }
            })
        }));
        let (tx, mut rx) = mpsc::channel::<(Frame, Vec<u8>)>(32);
        let cancel = self.cancel.clone();
        channel.on_message(Box::new(move |message: DataChannelMessage| {
            let tx = tx.clone();
            let cancel = cancel.clone();
            Box::pin(async move {
                if message.is_string {
                    cancel.cancel();
                    return;
                }
                match protocol::decode(&message.data) {
                    Ok(frame) => {
                        tokio::select! { _=cancel.cancelled()=>{}, result=tx.send(frame)=>{if result.is_err(){cancel.cancel();}} }
                    }
                    Err(_) => cancel.cancel(),
                }
            })
        }));
        let weak = Arc::downgrade(self);
        let cancel = self.cancel.clone();
        tokio::spawn(async move {
            loop {
                tokio::select! {_=cancel.cancelled()=>break,next=rx.recv()=>{let Some((frame,bytes))=next else{break};let Some(c)=weak.upgrade() else{break};c.receive(frame,bytes).await;}}
            }
            if let Some(c) = weak.upgrade() {
                c.fail_pending();
            }
        });
    }
    async fn signal(&self, kind: &str, payload: serde_json::Value) -> Result<()> {
        self.manager.upgrade().ok_or_else(||TransferError::new("共享服务已停止"))?.signal(json!({"type":kind,"connectionId":self.id,"versionId":self.version,"toDeviceId":self.remote_device,"payload":payload})).await
    }
    pub async fn offer(&self) -> Result<()> {
        let mut gathered = self.pc.gathering_complete_promise().await;
        let offer = self.pc.create_offer(None).await.map_err(rtc_error)?;
        self.pc
            .set_local_description(offer)
            .await
            .map_err(rtc_error)?;
        tokio::time::timeout(Duration::from_secs(4), gathered.recv())
            .await
            .map_err(|_| TransferError::new("ICE收集超时"))?;
        let local = self
            .pc
            .local_description()
            .await
            .ok_or_else(|| TransferError::new("本地信令缺失"))?;
        self.signal("peer.offer", json!({"sdp":local.sdp})).await
    }
    pub async fn answer(&self) -> Result<()> {
        let mut gathered = self.pc.gathering_complete_promise().await;
        let answer = self.pc.create_answer(None).await.map_err(rtc_error)?;
        self.pc
            .set_local_description(answer)
            .await
            .map_err(rtc_error)?;
        tokio::time::timeout(Duration::from_secs(4), gathered.recv())
            .await
            .map_err(|_| TransferError::new("ICE收集超时"))?;
        let local = self
            .pc
            .local_description()
            .await
            .ok_or_else(|| TransferError::new("本地信令缺失"))?;
        self.signal("peer.answer", json!({"sdp":local.sdp})).await
    }
    pub async fn add_ice(&self, candidate: RTCIceCandidateInit) -> Result<()> {
        if self.pc.remote_description().await.is_some() {
            self.pc
                .add_ice_candidate(candidate)
                .await
                .map_err(rtc_error)
        } else {
            let mut pending = self.ice.lock().await;
            if pending.len() >= 64 {
                return Err(TransferError::new("ICE候选过多"));
            }
            pending.push(candidate);
            Ok(())
        }
    }
    pub async fn flush_ice(&self) -> Result<()> {
        for candidate in std::mem::take(&mut *self.ice.lock().await) {
            self.pc
                .add_ice_candidate(candidate)
                .await
                .map_err(rtc_error)?;
        }
        Ok(())
    }
    async fn ready(&self) -> Result<Arc<RTCDataChannel>> {
        tokio::time::timeout(Duration::from_secs(6),async{loop{let notification=self.opened.notified();if let Some(c)=self.channel.get(){if c.ready_state()==RTCDataChannelState::Open{return Ok(c.clone())}}tokio::select!{_=self.cancel.cancelled()=>return Err(TransferError::new("对等节点已断开")),_=notification=>{},_=tokio::time::sleep(Duration::from_millis(20))=>{}}}}).await.map_err(|_|{self.cancel.cancel();TransferError::new("对等连接超时")})?
    }
    async fn send(&self, frame: &Frame, bytes: &[u8], cancel: &CancellationToken) -> Result<()> {
        let channel = self.ready().await?;
        let bytes = Bytes::from(protocol::encode(frame, bytes)?);
        tokio::select! {biased;_=self.cancel.cancelled()=>Err(TransferError::new("对等节点已断开")),_=cancel.cancelled()=>Err(TransferError::new("对等请求已取消")),result=tokio::time::timeout(Duration::from_secs(5),async{
            while channel.buffered_amount().await>64*1024 {tokio::time::sleep(Duration::from_millis(5)).await;}
            channel.send(&bytes).await.map_err(rtc_error)?;Ok(())
        })=>result.map_err(|_|TransferError::new("对等发送超时"))?}
    }
    pub async fn fetch(
        self: &Arc<Self>,
        task_id: &str,
        owner: &str,
        manifest: &Manifest,
        chunk: &Chunk,
    ) -> Result<Vec<u8>> {
        let manager = self
            .manager
            .upgrade()
            .ok_or_else(|| TransferError::new("共享服务已停止"))?;
        self.ready().await?;
        let grant: Grant = serde_json::from_value(
            manager
                .auth
                .json_for(
                    owner,
                    "POST",
                    "/api/v1/p2p/grants",
                    Some(json!({"versionId":manifest.version_id,"chunkHash":chunk.hash})),
                )
                .await?,
        )?;
        let current = manager.auth.current().await?;
        grant.bind(
            &current.session.user.id,
            &current.session.device_id,
            &manifest.version_id,
            chunk,
        )?;
        let request = uuid::Uuid::new_v4().to_string();
        let (tx, rx) = oneshot::channel();
        {
            let mut pending = self.pending.lock().unwrap();
            if pending.len() >= 4 {
                return Err(TransferError::new("对等请求过多"));
            }
            pending.insert(
                request.clone(),
                Pending {
                    observer: crate::transfer::metrics::observer(owner, task_id),
                    assembly: Assembly::new(
                        request.clone(),
                        manifest.version_id.clone(),
                        chunk.clone(),
                    )?,
                    result: tx,
                },
            );
        }
        let _guard = RequestGuard {
            connection: Arc::downgrade(self),
            id: request.clone(),
        };
        self.send(
            &Frame::Request {
                task_id: task_id.into(),
                request_id: request,
                version_id: manifest.version_id.clone(),
                index: chunk.index,
                hash: chunk.hash.clone(),
                grant,
            },
            &[],
            &self.cancel,
        )
        .await?;
        tokio::select! {_=self.cancel.cancelled()=>Err(TransferError::new("对等节点已断开")),result=tokio::time::timeout(Duration::from_secs(15),rx)=>result.map_err(|_|TransferError::new("对等分块超时"))?.map_err(|_|TransferError::new("对等请求终止"))?}
    }
    fn fail_pending(&self) {
        self.pending.lock().unwrap().clear();
        for token in self.serving.lock().unwrap().values() {
            token.cancel();
        }
    }
    pub async fn shutdown(&self) {
        self.cancel.cancel();
        self.fail_pending();
        let _ = tokio::time::timeout(Duration::from_millis(500), self.pc.close()).await;
    }
}
struct RequestGuard {
    connection: Weak<PeerConnection>,
    id: String,
}
impl Drop for RequestGuard {
    fn drop(&mut self) {
        if let Some(c) = self.connection.upgrade() {
            if c.pending.lock().unwrap().remove(&self.id).is_some() {
                let _ = c.control.try_send(Frame::Cancel {
                    request_id: self.id.clone(),
                });
            }
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[tokio::test(flavor = "multi_thread", worker_threads = 4)]
    async fn real_stalled_datachannel_bounds_sender_queue_and_cancellation_cleans_up() {
        struct Events;
        impl crate::events::EventSink for Events {
            fn emit(&self, _: &str, _: serde_json::Value) {}
        }
        let root = tempfile::tempdir().unwrap();
        let manager = PeerManager::new(
            root.path(),
            Arc::new(crate::auth::session::AuthManager::new(root.path()).unwrap()),
            Arc::new(crate::transfer::cache::Cache::open(root.path(), 1024 * 1024).unwrap()),
            Arc::new(tokio::sync::RwLock::new(
                crate::transfer::models::SharingPolicy::default(),
            )),
            Arc::new(Events),
        )
        .unwrap();
        let sender = PeerConnection::new(
            &manager,
            uuid::Uuid::new_v4().to_string(),
            uuid::Uuid::new_v4().to_string(),
            uuid::Uuid::new_v4().to_string(),
            uuid::Uuid::new_v4().to_string(),
            vec![],
            true,
        )
        .await
        .unwrap();
        let mut settings = SettingEngine::default();
        settings.set_include_loopback_candidate(true);
        let receiver = APIBuilder::new()
            .with_setting_engine(settings)
            .build()
            .new_peer_connection(RTCConfiguration::default())
            .await
            .unwrap();
        let release = CancellationToken::new();
        let blocker = release.clone();
        receiver.on_data_channel(Box::new(move |channel| {
            let blocker = blocker.clone();
            Box::pin(async move {
                channel.on_message(Box::new(move |_| {
                    let blocker = blocker.clone();
                    Box::pin(async move { blocker.cancelled().await })
                }));
            })
        }));
        let mut gathered = sender.pc.gathering_complete_promise().await;
        sender
            .pc
            .set_local_description(sender.pc.create_offer(None).await.unwrap())
            .await
            .unwrap();
        gathered.recv().await;
        receiver
            .set_remote_description(sender.pc.local_description().await.unwrap())
            .await
            .unwrap();
        let mut gathered = receiver.gathering_complete_promise().await;
        receiver
            .set_local_description(receiver.create_answer(None).await.unwrap())
            .await
            .unwrap();
        gathered.recv().await;
        sender
            .pc
            .set_remote_description(receiver.local_description().await.unwrap())
            .await
            .unwrap();
        let channel = sender.ready().await.unwrap();
        let cancel = CancellationToken::new();
        let sending = sender.clone();
        let task_cancel = cancel.clone();
        let producer = tokio::spawn(async move {
            let payload = vec![7; protocol::PAYLOAD];
            for index in 0..1024 {
                sending
                    .send(
                        &Frame::Data {
                            request_id: "bounded-test".into(),
                            version_id: sending.version.clone(),
                            index: 0,
                            hash: crate::transfer::models::hash(&payload),
                            offset: (index * protocol::PAYLOAD) as u64,
                            end: false,
                        },
                        &payload,
                        &task_cancel,
                    )
                    .await?;
            }
            Ok::<_, TransferError>(())
        });
        tokio::time::timeout(Duration::from_secs(5), async {
            while channel.buffered_amount().await <= 64 * 1024 {
                tokio::time::sleep(Duration::from_millis(5)).await;
            }
        })
        .await
        .unwrap();
        assert!(
            channel.buffered_amount().await <= 64 * 1024 + protocol::MAX_FRAME,
            "one bounded frame at most beyond the backpressure threshold"
        );
        cancel.cancel();
        assert!(tokio::time::timeout(Duration::from_secs(1), producer)
            .await
            .unwrap()
            .unwrap()
            .is_err());
        release.cancel();
        sender.shutdown().await;
        receiver.close().await.unwrap();
        assert!(sender.pending.lock().unwrap().is_empty());
        assert!(sender.serving.lock().unwrap().is_empty());
    }
}
