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
    if let Some(code) = linknestx::update::early_mode() {
        std::process::exit(code);
    }
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
            let auth = Arc::new(AuthManager::production(&root)?);
            app.manage(linknestx::background::BackgroundState::default());
            let events: Arc<dyn EventSink> = Arc::new(DesktopEvents(app.handle().clone()));
            let transfers = TransferService::open(&root, auth.clone(), events.clone())?;
            let updater = linknestx::update::UpdateService::new(
                root.clone(),
                events.clone(),
                auth.activity.clone(),
            );
            app.manage(updater.clone());
            app.manage(NativeState {
                audit_exports: Arc::new(linknestx::audit_export::AuditExportManager::new()?),
                auth,
                transfers: transfers.clone(),
                events: events.clone(),
                socket: SocketBridge::default(),
                exiting: AtomicBool::new(false),
                exit_ready: AtomicBool::new(false),
                lifecycle: Mutex::new(()),
                attachments: Mutex::new(linknestx::attachments::Registry::new(
                    root.join("attachments"),
                )?),
                destinations: Mutex::new(HashSet::new()),
            });
            let cleanup_handle = app.handle().clone();
            tauri::async_runtime::spawn(async move {
                let mut timer = tokio::time::interval(std::time::Duration::from_secs(30));
                loop {
                    timer.tick().await;
                    let state = cleanup_handle.state::<NativeState>();
                    if state.exiting.load(Ordering::SeqCst) {
                        break;
                    }
                    state.expire_attachments().await;
                }
            });
            linknestx::tray::install(app.handle())?;
            let handle = app.handle().clone();
            tauri::async_runtime::spawn(async move {
                let policy = transfers.sharing_policy().await;
                if let Err(error) = linknestx::autostart::set(&handle, policy.autostart) {
                    events.emit("service.error", serde_json::json!({"error":error.error}));
                }
            });
            // Health depends on local core/database initialization, never network reachability.
            linknestx::update::acknowledge_health(&root)?;
            updater.start();
            Ok(())
        })
        .on_webview_event(|webview, event| {
            if webview.label() != "main" {
                return;
            }
            if let tauri::WebviewEvent::DragDrop(tauri::DragDropEvent::Drop { paths, .. }) = event {
                let app = webview.app_handle().clone();
                let Some(scope) = app.state::<NativeState>().auth.selection_scope_now() else {
                    return;
                };
                let paths = paths.clone();
                tauri::async_runtime::spawn(async move {
                    let state = app.state::<NativeState>();
                    if let Err(error) = commands::native_drop(&state, scope, paths).await {
                        state
                            .events
                            .emit("service.error", serde_json::json!({"error":error.error}));
                    }
                });
            }
        })
        .on_window_event(|window, event| {
            if let tauri::WindowEvent::CloseRequested { api, .. } = event {
                api.prevent_close();
                let _ = window.hide();
            }
        })
        .invoke_handler(tauri::generate_handler![
            commands::get_update_status,
            commands::check_for_updates,
            commands::install_ready_update,
            commands::defer_update,
            commands::client_configuration,
            commands::get_transfer_preferences,
            commands::set_transfer_preferences,
            commands::pick_upload_files,
            commands::read_clipboard_attachments,
            commands::discard_attachment,
            commands::start_attachment_upload,
            commands::start_document_attachment_upload,
            commands::document_attachment_preview,
            commands::file_attachment_preview,
            commands::file_attachment_thumb,
            commands::open_external_url,
            commands::set_profile_avatar,
            commands::profile_avatar,
            commands::app_version,
            commands::login,
            commands::logout,
            commands::api_request,
            commands::reserve_audit_export,
            commands::export_audit,
            commands::cancel_audit_export,
            commands::start_download,
            commands::pause_transfer,
            commands::resume_transfer,
            commands::list_transfers,
            commands::get_background_status,
            commands::get_connection_status,
            commands::reconnect_connection,
            commands::send_server_event,
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
