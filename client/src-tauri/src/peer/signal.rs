use super::*;
use crate::auth::session::api_url;
use futures_util::{SinkExt, StreamExt};
use tokio_tungstenite::tungstenite::{
    client::IntoClientRequest, protocol::WebSocketConfig, Message,
};
use webrtc::{
    ice_transport::ice_candidate::RTCIceCandidateInit,
    peer_connection::sdp::session_description::RTCSessionDescription,
};

impl PeerManager {
    pub(super) async fn signal_loop(
        self: Arc<Self>,
        cancel: CancellationToken,
        mut outgoing: mpsc::Receiver<Value>,
    ) {
        let mut changes = self.cache.subscribe();
        while !cancel.is_cancelled() {
            let Ok(current) = self.auth.current().await else {
                break;
            };
            let Ok(mut url) = api_url(&current.origin, "/api/v1/p2p/ws") else {
                break;
            };
            let _ = url.set_scheme(if current.origin.starts_with("https:") {
                "wss"
            } else {
                "ws"
            });
            let Ok(mut request) = url.as_str().into_client_request() else {
                break;
            };
            let Ok(header) = format!("Bearer {}", current.session.access_token).parse() else {
                break;
            };
            request.headers_mut().insert("Authorization", header);
            let config = WebSocketConfig::default()
                .max_message_size(Some(65536))
                .max_frame_size(Some(65536));
            let connected = tokio::select! {_=cancel.cancelled()=>break,r=tokio::time::timeout(Duration::from_secs(5),tokio_tungstenite::connect_async_with_config(request,Some(config),false))=>r};
            if let Ok(Ok((mut socket, _))) = connected {
                let owner = current.owner();
                if tokio::select! {_=cancel.cancelled()=>true,r=self.recover_cache(&owner)=>r.is_err()}
                {
                    break;
                }
                let mut announced = HashMap::new();
                let mut heartbeat = tokio::time::interval(Duration::from_secs(20));
                let mut refresh = tokio::time::interval(Duration::from_secs(1));
                loop {
                    let result: Result<()> = tokio::select! {biased;
                        _=cancel.cancelled()=>break,
                        incoming=socket.next()=>{match incoming {
                            Some(Ok(Message::Text(text)))=>match serde_json::from_str::<Value>(&text){Ok(v)=>self.incoming(v).await,Err(_)=>Err(TransferError::new("信令格式无效"))},
                            Some(Ok(Message::Ping(v)))=>socket.send(Message::Pong(v)).await.map_err(|_|TransferError::new("信令断开")),
                            Some(Ok(Message::Close(_)))|Some(Err(_))|None=>break,_=>Ok(())}},
                        value=outgoing.recv()=>{let Some(value)=value else{break};tokio::time::timeout(Duration::from_secs(3),socket.send(Message::Text(value.to_string().into()))).await.map_err(|_|TransferError::new("信令发送超时")).and_then(|v|v.map_err(|_|TransferError::new("信令断开")))},
                        _=heartbeat.tick()=>{let _=tokio::time::timeout(Duration::from_secs(3),self.auth.json_for(&current.owner(),"POST","/api/v1/p2p/heartbeat",Some(json!({})))).await;tokio::time::timeout(Duration::from_secs(5),self.advertise(&current.owner(),&mut announced)).await.unwrap_or_else(|_|Err(TransferError::new("共享目录更新超时")))},
                        _=changes.changed()=>{tokio::time::timeout(Duration::from_secs(5),self.advertise(&current.owner(),&mut announced)).await.unwrap_or_else(|_|Err(TransferError::new("共享目录更新超时")))},
                        _=refresh.tick()=>{if self.auth.current().await.map_or(true,|v|v.session.access_token!=current.session.access_token){break}Ok(())}
                    };
                    if result.is_err() {
                        break;
                    }
                }
            } else if let Ok(Err(tokio_tungstenite::tungstenite::Error::Http(response))) = connected
            {
                if response.status().as_u16() == 401 {
                    let _ = tokio::time::timeout(
                        Duration::from_secs(5),
                        self.auth.refresh_session(&current),
                    )
                    .await;
                }
            }
            self.close_connections().await;
            while outgoing.try_recv().is_ok() {}
            tokio::select! {_=cancel.cancelled()=>break,_=tokio::time::sleep(Duration::from_secs(1))=>{}}
        }
    }
    async fn incoming(self: &Arc<Self>, v: Value) -> Result<()> {
        let kind = v["type"].as_str().unwrap_or("");
        if kind == "session.ready" {
            let s = self.auth.current().await?;
            if v["deviceId"] != s.session.device_id || v["userId"] != s.session.user.id {
                return Err(TransferError::new("信令身份不匹配"));
            }
            return Ok(());
        }
        let id = v["connectionId"].as_str().unwrap_or("");
        if kind == "error" || kind == "peer.close" {
            if let Some(c) = self.connections.lock().await.remove(id) {
                if kind == "peer.close" && v["code"] == "expired" {
                    c.expired.store(true, Ordering::SeqCst);
                }
                c.shutdown().await;
            }
            return Ok(());
        }
        let from = v["fromDeviceId"]
            .as_str()
            .ok_or_else(|| TransferError::new("信令缺少设备身份"))?;
        let user = v["fromUserId"]
            .as_str()
            .ok_or_else(|| TransferError::new("信令缺少账户身份"))?;
        let version = v["versionId"].as_str().unwrap_or("");
        for value in [id, from, user, version] {
            uuid::Uuid::parse_str(value).map_err(|_| TransferError::new("信令标识无效"))?;
        }
        let connection = if kind == "peer.offer" {
            if !self.policy.read().await.enabled {
                return Ok(());
            }
            let mut map = self.connections.lock().await;
            map.retain(|_, c| !c.cancel.is_cancelled());
            if map.len() >= 12 || map.contains_key(id) {
                return Err(TransferError::new("对等连接数或标识无效"));
            }
            let config = self.auth.json("GET", "/api/v1/p2p/config", None).await?;
            let ice: Vec<IceServer> = serde_json::from_value(config["iceServers"].clone())?;
            let c = PeerConnection::new(
                self,
                id.into(),
                version.into(),
                from.into(),
                user.into(),
                ice.into_iter().map(Into::into).collect(),
                false,
            )
            .await?;
            map.insert(id.into(), c.clone());
            c
        } else {
            self.connections
                .lock()
                .await
                .get(id)
                .cloned()
                .ok_or_else(|| TransferError::new("对等连接不存在"))?
        };
        if connection.remote_device != from
            || connection.remote_user != user
            || connection.version != version
        {
            return Err(TransferError::new("信令连接身份发生变化"));
        }
        match kind {
            "peer.offer" => {
                let sdp = v["payload"]["sdp"].as_str().unwrap_or("");
                connection
                    .pc
                    .set_remote_description(
                        RTCSessionDescription::offer(sdp.into())
                            .map_err(super::connection::rtc_error)?,
                    )
                    .await
                    .map_err(super::connection::rtc_error)?;
                connection.flush_ice().await?;
                connection.answer().await?;
            }
            "peer.answer" => {
                let sdp = v["payload"]["sdp"].as_str().unwrap_or("");
                connection
                    .pc
                    .set_remote_description(
                        RTCSessionDescription::answer(sdp.into())
                            .map_err(super::connection::rtc_error)?,
                    )
                    .await
                    .map_err(super::connection::rtc_error)?;
                connection.flush_ice().await?;
            }
            "peer.ice" => {
                let candidate: RTCIceCandidateInit = serde_json::from_value(v["payload"].clone())?;
                connection.add_ice(candidate).await?;
            }
            _ => return Err(TransferError::new("信令类型无效")),
        }
        Ok(())
    }
}
