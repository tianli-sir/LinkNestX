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
    let mut pixels = vec![0u8; 32 * 32 * 4];
    for y in 0..32 {
        for x in 0..32 {
            let p = (y * 32 + x) * 4;
            let mark = (8..13).contains(&x) && (7..25).contains(&y)
                || (8..25).contains(&x) && (20..25).contains(&y);
            pixels[p..p + 4].copy_from_slice(if mark {
                &[255, 255, 255, 255]
            } else {
                &[14, 116, 144, 255]
            });
        }
    }
    TrayIconBuilder::with_id("linknestx")
        .icon(tauri::image::Image::new_owned(pixels, 32, 32))
        .tooltip("LinkNestX · 后台传输服务")
        .menu(&menu)
        .show_menu_on_left_click(false)
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
