#![cfg_attr(not(debug_assertions), windows_subsystem = "windows")]

use linknestx::{
    auth::{session::AuthManager, socket::SocketBridge},
    commands::{self, NativeState},
    events::{DesktopEvents, EventSink},
    transfer::service::TransferService,
};
use std::{
    collections::HashSet,
    sync::{
        atomic::{AtomicBool, Ordering},
        Arc,
    },
};
use tauri::Manager;
use tokio::sync::Mutex;
fn main() {
    let app = tauri::Builder::default()
        .plugin(tauri_plugin_autostart::init(
            tauri_plugin_autostart::MacosLauncher::LaunchAgent,
            None,
        ))
        .setup(|app| {
            let root = app.path().app_data_dir()?;
            std::fs::create_dir_all(&root)?;
            #[cfg(unix)]
            {
                use std::os::unix::fs::PermissionsExt;
                std::fs::set_permissions(&root, std::fs::Permissions::from_mode(0o700))?;
            }
            let auth = Arc::new(AuthManager::new(&root)?);
            let events: Arc<dyn EventSink> = Arc::new(DesktopEvents(app.handle().clone()));
            let transfers = TransferService::open(&root, auth.clone(), events.clone())?;
            app.manage(NativeState {
                auth,
                transfers: transfers.clone(),
                events: events.clone(),
                socket: SocketBridge::default(),
                exiting: AtomicBool::new(false),
                exit_ready: AtomicBool::new(false),
                lifecycle: Mutex::new(()),
                uploads: Mutex::new(HashSet::new()),
                destinations: Mutex::new(HashSet::new()),
            });
            linknestx::tray::install(app.handle())?;
            let handle = app.handle().clone();
            tauri::async_runtime::spawn(async move {
                let policy = transfers.sharing_policy().await;
                if let Err(error) = linknestx::autostart::set(&handle, policy.autostart) {
                    events.emit("service.error", serde_json::json!({"error":error.error}));
                }
            });
            Ok(())
        })
        .on_window_event(|window, event| {
            if let tauri::WindowEvent::CloseRequested { api, .. } = event {
                api.prevent_close();
                let _ = window.hide();
            }
        })
        .invoke_handler(tauri::generate_handler![
            commands::app_version,
            commands::login,
            commands::logout,
            commands::api_request,
            commands::start_download,
            commands::start_upload,
            commands::pause_transfer,
            commands::resume_transfer,
            commands::list_transfers,
            commands::get_sharing_policy,
            commands::set_sharing_policy,
            commands::send_server_event,
            commands::pick_upload_file,
            commands::pick_download_destination
        ])
        .build(tauri::generate_context!())
        .expect("unable to initialize LinkNestX");
    app.run(|app, event| {
        if let tauri::RunEvent::ExitRequested { api, .. } = event {
            if !app.state::<NativeState>().exit_ready.load(Ordering::SeqCst) {
                api.prevent_exit();
                linknestx::tray::quit(app);
            }
        }
    });
}
