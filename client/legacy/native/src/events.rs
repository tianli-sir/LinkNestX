use serde_json::{json, Value};
use std::sync::Arc;
use tokio::sync::{broadcast, mpsc, Notify};
use tokio_util::sync::CancellationToken;
pub trait EventSink: Send + Sync {
    fn emit(&self, name: &str, value: Value);
    fn subscribe(&self) -> Option<broadcast::Receiver<()>> {
        None
    }
}
pub struct NoopEvents;
impl EventSink for NoopEvents {
    fn emit(&self, _: &str, _: Value) {}
}
pub struct PipeEvents {
    pub output: mpsc::Sender<Value>,
    pub changes: broadcast::Sender<()>,
    overflow: Arc<Notify>,
    cancel: CancellationToken,
}
impl PipeEvents {
    pub fn new(output: mpsc::Sender<Value>) -> Arc<Self> {
        let overflow = Arc::new(Notify::new());
        let cancel = CancellationToken::new();
        let worker_output = output.clone();
        let worker_notify = overflow.clone();
        let worker_cancel = cancel.clone();
        tokio::spawn(async move {
            loop {
                tokio::select! {
                    biased;
                    _ = worker_cancel.cancelled() => break,
                    _ = worker_notify.notified() => {}
                }
                // A single coalesced refresh recovers all snapshots without retaining
                // an unbounded event backlog when the desktop stops reading its pipe.
                tokio::select! {
                    biased;
                    _ = worker_cancel.cancelled() => break,
                    result = worker_output.send(json!({"event":"native.resync","payload":null})) => {
                        if result.is_err() { break; }
                    }
                }
            }
        });
        Arc::new(Self {
            output,
            changes: broadcast::channel(8).0,
            overflow,
            cancel,
        })
    }
    pub fn close(&self) {
        self.cancel.cancel();
    }
}
impl EventSink for PipeEvents {
    fn emit(&self, name: &str, payload: Value) {
        if name == "lifecycle.refresh" || name == "server.event" {
            let _ = self.changes.send(());
        }
        if self
            .output
            .try_send(json!({"event":name,"payload":payload}))
            .is_err()
        {
            self.overflow.notify_one();
        }
    }
    fn subscribe(&self) -> Option<broadcast::Receiver<()>> {
        Some(self.changes.subscribe())
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    #[tokio::test]
    async fn backpressure_requires_resync_instead_of_silently_losing_events() {
        let (tx, mut rx) = mpsc::channel(1);
        let events = PipeEvents::new(tx);
        events.emit(
            "transfer.updated",
            json!({"id":"task","state":"downloading"}),
        );
        for _ in 0..100 {
            events.emit("server.event", json!({"type":"message.created"}));
        }
        assert_eq!(rx.recv().await.unwrap()["event"], "transfer.updated");
        let recovery = tokio::time::timeout(std::time::Duration::from_secs(1), rx.recv())
            .await
            .unwrap()
            .unwrap();
        assert_eq!(recovery["event"], "native.resync");
        events.close();
        drop(events);
        assert!(
            tokio::time::timeout(std::time::Duration::from_secs(1), rx.recv())
                .await
                .unwrap()
                .is_none()
        );
    }
}
