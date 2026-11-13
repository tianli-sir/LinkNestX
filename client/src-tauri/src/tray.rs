use crate::commands::NativeState;
use std::sync::atomic::Ordering;
use tauri::{
    menu::{Menu, MenuItem},
    tray::{MouseButton, MouseButtonState, TrayIconBuilder, TrayIconEvent},
    Manager,
};
pub fn quit(app: &tauri::AppHandle) {
    let state = app.state::<NativeState>();
    if state.exiting.swap(true, Ordering::SeqCst) {
        return;
    }
    let app = app.clone();
    tauri::async_runtime::spawn(async move {
        let state = app.state::<NativeState>();
        if let Err(error) = state.stop().await {
            state
                .events
                .emit("service.error", serde_json::json!({"error":error.error}));
            state.exiting.store(false, Ordering::SeqCst);
            return;
        }
        state.exit_ready.store(true, Ordering::SeqCst);
        app.exit(0);
    });
}
fn show(app: &tauri::AppHandle) {
    if let Some(window) = app.get_webview_window("main") {
        let _ = window.show();
        let _ = window.unminimize();
        let _ = window.set_focus();
    }
}
pub fn install(app: &tauri::AppHandle) -> tauri::Result<()> {
    let open = MenuItem::with_id(app, "open", "打开 LinkNestX", true, None::<&str>)?;
    let quit_item = MenuItem::with_id(app, "quit", "退出并停止传输", true, None::<&str>)?;
    let menu = Menu::with_items(app, &[&open, &quit_item])?;
    let mut builder = TrayIconBuilder::with_id("linknestx")
        .tooltip("LinkNestX · 后台传输服务")
        .menu(&menu)
        .show_menu_on_left_click(false);
    if let Some(icon) = app.default_window_icon() {
        builder = builder.icon(icon.clone());
    }
    builder
        .on_menu_event(|app, event| match event.id.as_ref() {
            "open" => show(app),
            "quit" => quit(app),
            _ => {}
        })
        .on_tray_icon_event(|tray, event| {
            if matches!(
                event,
                TrayIconEvent::Click {
                    button: MouseButton::Left,
                    button_state: MouseButtonState::Up,
                    ..
                }
            ) {
                show(tray.app_handle())
            }
        })
        .build(app)?;
    Ok(())
}
