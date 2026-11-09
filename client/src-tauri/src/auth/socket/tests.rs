use super::*;
use crate::events::NoopEvents;
use tokio_tungstenite::{tungstenite::protocol::Role, WebSocketStream};

#[tokio::test]
async fn stop_cancels_an_actually_backpressured_websocket_write() {
    // A real WebSocket codec over a tiny in-memory transport models a peer
    // that accepts a connection but never drains writes, without opening a port.
    let (writer, mut unread_peer) = tokio::io::duplex(64);
    let socket = WebSocketStream::from_raw_socket(writer, Role::Client, None).await;
    let token = CancellationToken::new();
    let cancel = token.clone();
    let (tx, mut rx) = mpsc::channel(2);
    tx.send(json!({"body":"x".repeat(65000)})).await.unwrap();
    let task = tokio::spawn(async move {
        run_connected(socket, &mut rx, &cancel, Arc::new(NoopEvents)).await;
    });
    let mut prefix = [0u8; 64];
    use tokio::io::AsyncReadExt;
    tokio::time::timeout(
        Duration::from_millis(300),
        unread_peer.read_exact(&mut prefix),
    )
    .await
    .unwrap()
    .unwrap();
    assert!(
        !task.is_finished(),
        "writer should be blocked by the unread transport"
    );
    let bridge = SocketBridge {
        run: Mutex::new(Some((token, task, tx))),
    };
    tokio::time::timeout(Duration::from_millis(300), bridge.stop())
        .await
        .expect("stop must cancel the write");
    assert!(bridge.run.lock().await.is_none());
}

#[tokio::test]
async fn stop_aborts_a_worker_that_does_not_cooperate() {
    let token = CancellationToken::new();
    let (tx, _rx) = mpsc::channel(1);
    let (dropped_tx, dropped_rx) = tokio::sync::oneshot::channel();
    struct Dropped(Option<tokio::sync::oneshot::Sender<()>>);
    impl Drop for Dropped {
        fn drop(&mut self) {
            if let Some(tx) = self.0.take() {
                let _ = tx.send(());
            }
        }
    }
    let task = tokio::spawn(async move {
        let _guard = Dropped(Some(dropped_tx));
        std::future::pending::<()>().await;
    });
    tokio::task::yield_now().await;
    let bridge = SocketBridge {
        run: Mutex::new(Some((token, task, tx))),
    };
    tokio::time::timeout(Duration::from_secs(2), bridge.stop())
        .await
        .expect("stop must have a hard bound");
    dropped_rx
        .await
        .expect("worker resources should be dropped");
}
