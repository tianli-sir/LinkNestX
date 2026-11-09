use super::session::{api_url, AuthManager};
use crate::{events::EventSink, Result, TransferError};
use futures_util::{SinkExt, StreamExt};
use serde_json::{json, Value};
use std::{sync::Arc, time::Duration};
use tokio::io::{AsyncRead, AsyncWrite};
use tokio::sync::{mpsc, Mutex};
use tokio_tungstenite::tungstenite::{client::IntoClientRequest, Message};
use tokio_tungstenite::WebSocketStream;
use tokio_util::sync::CancellationToken;
#[cfg(test)]
mod tests;

pub struct SocketBridge {
    run: Mutex<
        Option<(
            CancellationToken,
            tokio::task::JoinHandle<()>,
            mpsc::Sender<Value>,
        )>,
    >,
}
impl Default for SocketBridge {
    fn default() -> Self {
        Self {
            run: Mutex::new(None),
        }
    }
}
impl SocketBridge {
    pub async fn start(&self, auth: Arc<AuthManager>, events: Arc<dyn EventSink>) {
        self.stop().await;
        let token = CancellationToken::new();
        let cancel = token.clone();
        let (tx, mut rx) = mpsc::channel::<Value>(64);
        let handle = tokio::spawn(async move {
            let mut delay = 1;
            while !cancel.is_cancelled() {
                let Ok(current) = auth.current().await else {
                    break;
                };
                events.emit("connection.changed", json!({"status":"connecting"}));
                let Ok(mut url) = api_url(&current.origin, "/api/v1/ws") else {
                    break;
                };
                let scheme = if url.scheme() == "https" { "wss" } else { "ws" };
                let _ = url.set_scheme(scheme);
                let Ok(mut request) = url.as_str().into_client_request() else {
                    break;
                };
                let Ok(header) = format!("Bearer {}", current.session.access_token).parse() else {
                    break;
                };
                request.headers_mut().insert("Authorization", header);
                let connection = tokio::select! {_=cancel.cancelled()=>break,r=tokio::time::timeout(Duration::from_secs(15),tokio_tungstenite::connect_async(request))=>r};
                match connection {
                    Ok(Ok((socket, _))) => {
                        delay = 1;
                        events.emit("connection.changed", json!({"status":"connected"}));
                        run_connected(socket, &mut rx, &cancel, events.clone()).await;
                    }
                    Ok(Err(tokio_tungstenite::tungstenite::Error::Http(response)))
                        if response.status().as_u16() == 401 =>
                    {
                        tokio::select! {_=cancel.cancelled()=>break,_=tokio::time::timeout(Duration::from_secs(10),auth.refresh_session(&current))=>{}}
                    }
                    _ => {}
                }
                events.emit("connection.changed", json!({"status":"disconnected"}));
                tokio::select! {_=cancel.cancelled()=>break,_=tokio::time::sleep(Duration::from_secs(delay))=>{}}
                delay = (delay * 2).min(30);
            }
            events.emit("connection.changed", json!({"status":"disconnected"}));
        });
        *self.run.lock().await = Some((token, handle, tx));
    }
    pub async fn send(&self, event: Value) -> Result<()> {
        let run = self.run.lock().await;
        let Some((_, _, tx)) = run.as_ref() else {
            return Err(TransferError::new("实时连接尚未建立"));
        };
        if serde_json::to_vec(&event)?.len() > 65536 {
            return Err(TransferError::new("消息过大"));
        }
        tx.try_send(event)
            .map_err(|_| TransferError::new("实时消息队列已满或已关闭"))
    }
    pub async fn stop(&self) {
        let run = self.run.lock().await.take();
        if let Some((cancel, mut handle, _)) = run {
            cancel.cancel();
            if tokio::time::timeout(Duration::from_millis(500), &mut handle)
                .await
                .is_err()
            {
                handle.abort();
                let _ = handle.await;
            }
        }
    }
}

async fn bounded_send<S: AsyncRead + AsyncWrite + Unpin>(
    socket: &mut WebSocketStream<S>,
    message: Message,
    cancel: &CancellationToken,
) -> bool {
    tokio::select! {biased;_=cancel.cancelled()=>false,result=tokio::time::timeout(Duration::from_secs(5),socket.send(message))=>matches!(result,Ok(Ok(())))}
}
async fn run_connected<S: AsyncRead + AsyncWrite + Unpin>(
    mut socket: WebSocketStream<S>,
    rx: &mut mpsc::Receiver<Value>,
    cancel: &CancellationToken,
    events: Arc<dyn EventSink>,
) {
    let mut heartbeat = tokio::time::interval(Duration::from_secs(30));
    loop {
        tokio::select! {biased;
            _=cancel.cancelled()=>break,
            _=heartbeat.tick()=>{if !bounded_send(&mut socket,Message::Text(json!({"type":"ping"}).to_string().into()),cancel).await{break}},
            outgoing=rx.recv()=>{if let Some(value)=outgoing {if !bounded_send(&mut socket,Message::Text(value.to_string().into()),cancel).await{break}}else{break}},
            incoming=socket.next()=>{match incoming {
                Some(Ok(Message::Text(text)))=>{if text.len()<=1_048_576 {if let Ok(value)=serde_json::from_str::<Value>(&text){events.emit("server.event",value)}}},
                Some(Ok(Message::Ping(bytes)))=>{if !bounded_send(&mut socket,Message::Pong(bytes),cancel).await{break}},
                Some(Ok(Message::Close(_)))|Some(Err(_))|None=>break,_=>{}
            }}
        }
    }
    // Cancellation drops the transport immediately. A normal disconnect may
    // flush a close frame, but never waits indefinitely for a non-reading peer.
    tokio::select! {biased;_=cancel.cancelled()=>{},_=tokio::time::timeout(Duration::from_millis(100),socket.close(None))=>{}}
}
