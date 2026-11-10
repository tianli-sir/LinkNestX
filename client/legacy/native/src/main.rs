use linknestx_legacy_native::{
    events::PipeEvents,
    host::Host,
    protocol::{self, Request},
    TransferError,
};
use serde_json::{json, Value};
use std::{path::PathBuf, sync::Arc};
use tokio::{
    io::AsyncWriteExt,
    sync::{mpsc, Semaphore},
};
mod instance;
#[tokio::main]
async fn main() {
    if let Err(error) = run().await {
        eprintln!("LinkNestX native service: {}", error.error);
        std::process::exit(1);
    }
}
async fn run() -> linknestx_legacy_native::Result<()> {
    let mut args = std::env::args_os().skip(1);
    if args.next().as_deref() != Some(std::ffi::OsStr::new("--stdio")) {
        return Err(TransferError::new("请通过桌面应用启动后台服务"));
    }
    let root = PathBuf::from(
        args.next()
            .ok_or_else(|| TransferError::new("缺少数据目录"))?,
    );
    if !root.is_absolute() || args.next().is_some() {
        return Err(TransferError::new("数据目录无效"));
    }
    let _install_guard = instance::InstallGuard::acquire()?;
    let (tx, mut rx) = mpsc::channel::<Value>(16);
    let writer = tokio::spawn(async move {
        let mut output = tokio::io::stdout();
        while let Some(value) = rx.recv().await {
            let mut bytes = serde_json::to_vec(&value).map_err(std::io::Error::other)?;
            if bytes.len() > protocol::MAX_RESPONSE_BYTES {
                bytes =
                    serde_json::to_vec(&json!({"id":value["id"],"error":{"error":"响应超过限制"}}))
                        .unwrap();
            }
            output
                .write_all(&(bytes.len() as u32).to_le_bytes())
                .await?;
            output.write_all(&bytes).await?;
            output.flush().await?;
        }
        Ok::<(), std::io::Error>(())
    });
    let events = PipeEvents::new(tx.clone());
    let host = Host::open(&root, events.clone())?;
    tx.send(json!({"event":"native.ready","payload":{"protocol":1}}))
        .await
        .map_err(|_| TransferError::new("桌面连接已关闭"))?;
    let mut stdin = tokio::io::stdin();
    let slots = Arc::new(Semaphore::new(16));
    let mut tasks = tokio::task::JoinSet::new();
    let mut failure = None;
    loop {
        let frame = match protocol::read_frame(&mut stdin).await {
            Ok(Some(v)) => v,
            Ok(None) => break,
            Err(e) => {
                failure = Some(e);
                break;
            }
        };
        let request = match Request::parse(&frame) {
            Ok(v) => v,
            Err(e) => {
                failure = Some(e);
                break;
            }
        };
        if request.command == "shutdown" {
            break;
        }
        let permit = match slots.clone().try_acquire_owned() {
            Ok(p) => p,
            Err(_) => {
                let _ = tx
                    .send(protocol::response(
                        request.id,
                        Err(TransferError::new("请求过多，请稍后重试")),
                    ))
                    .await;
                continue;
            }
        };
        let service = host.clone();
        let output = tx.clone();
        tasks.spawn(async move {
            let _permit = permit;
            let result = service.dispatch(&request.command, request.args).await;
            let _ = output.send(protocol::response(request.id, result)).await;
        });
        while tasks.try_join_next().is_some() {}
    }
    let _ = host.shutdown().await;
    tasks.abort_all();
    while tasks.join_next().await.is_some() {}
    events.close();
    drop(host);
    drop(events);
    drop(tx);
    let _ = tokio::time::timeout(std::time::Duration::from_secs(3), writer).await;
    match failure {
        Some(e) => Err(e),
        None => Ok(()),
    }
}
