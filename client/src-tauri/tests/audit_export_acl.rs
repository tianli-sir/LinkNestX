use linknestx::{
    audit_export::AuditExportManager,
    auth::{session::AuthManager, socket::SocketBridge},
    commands::{self, NativeState},
    events::NoopEvents,
    transfer::service::TransferService,
};
use serde_json::{json, Value};
use std::{
    collections::HashSet,
    sync::{atomic::AtomicBool, Arc},
};
use tauri::{
    ipc::{CallbackFn, InvokeBody},
    test::{get_ipc_response, mock_builder, MockRuntime, INVOKE_KEY},
    webview::InvokeRequest,
    Manager, WebviewWindow, WebviewWindowBuilder,
};
use tokio::sync::Mutex;

fn call(
    window: &WebviewWindow<MockRuntime>,
    origin: &str,
    command: &str,
    args: Value,
) -> std::result::Result<Value, Value> {
    get_ipc_response(
        window,
        InvokeRequest {
            cmd: command.into(),
            callback: CallbackFn(0),
            error: CallbackFn(1),
            url: origin.parse().unwrap(),
            body: InvokeBody::Json(args),
            headers: Default::default(),
            invoke_key: INVOKE_KEY.to_string(),
        },
    )
    .map(|body| body.deserialize::<Value>().unwrap())
}

#[test]
fn audit_export_commands_cross_the_real_local_capability_boundary_and_deny_remote_pages() {
    let root = tempfile::tempdir().unwrap();
    let auth = Arc::new(AuthManager::new(root.path()).unwrap());
    let events = Arc::new(NoopEvents);
    let transfers = TransferService::open(root.path(), auth.clone(), events.clone()).unwrap();
    let app = mock_builder()
        .manage(linknestx::background::BackgroundState::default())
        .manage(NativeState {
            audit_exports: Arc::new(AuditExportManager::new().unwrap()),
            auth,
            transfers,
            socket: SocketBridge::default(),
            events,
            exiting: AtomicBool::new(false),
            exit_ready: AtomicBool::new(false),
            lifecycle: Mutex::new(()),
            attachments: Mutex::new(
                linknestx::attachments::Registry::new(root.path().join("attachments")).unwrap(),
            ),
            destinations: Mutex::new(HashSet::new()),
        })
        .invoke_handler(tauri::generate_handler![
            commands::client_configuration,
            commands::get_transfer_preferences,
            commands::discard_attachment,
            commands::start_attachment_upload,
            commands::reserve_audit_export,
            commands::export_audit,
            commands::cancel_audit_export,
            commands::get_background_status
        ])
        // Use the app's generated permission manifest and main-local capability, not empty mock ACL.
        .build(tauri::generate_context!())
        .unwrap();
    let window = WebviewWindowBuilder::new(&app, "main", Default::default())
        .build()
        .unwrap();
    let other_window = WebviewWindowBuilder::new(&app, "no-export-capability", Default::default())
        .build()
        .unwrap();
    let result = || {
        let local = "tauri://localhost";
        assert_eq!(
            call(&window, local, "get_background_status", json!({})).unwrap()["activeUploads"],
            0
        );
        assert_eq!(
            call(
                &window,
                local,
                "client_configuration",
                json!({"serverOrigin":"https://evil.invalid"})
            )
            .unwrap()["serverOrigin"],
            linknestx::deployment::SERVER_ORIGIN
        );
        assert!(call(
            &window,
            local,
            "start_upload",
            json!({"source":"/etc/passwd","groupId":"group"})
        )
        .is_err());
        assert!(call(
            &window,
            local,
            "set_sharing_policy",
            json!({"policy":{"enabled":false}})
        )
        .is_err());
        assert!(call(
            &window,
            local,
            "start_attachment_upload",
            json!({"sourceHandle":"/etc/passwd","groupId":"group","clientAttachmentId":"draft"})
        )
        .is_err());
        let preferences = call(&window, local, "get_transfer_preferences", json!({})).unwrap();
        assert!(preferences.get("enabled").is_none());
        assert!(preferences.get("cacheQuotaBytes").is_none());
        let id = call(&window, local, "reserve_audit_export", json!({}))
            .expect("bundled main window must be allowed to reserve export");
        assert!(id.as_str().is_some_and(|id| !id.is_empty()));
        call(
            &window,
            local,
            "cancel_audit_export",
            json!({"exportId":id}),
        )
        .expect("bundled main window must be allowed to cancel export");
        let outcome = call(
            &window,
            local,
            "export_audit",
            json!({"exportId":id,"format":"csv","action":""}),
        )
        .expect("bundled main window must reach export's native cancellation guard");
        assert_eq!(outcome, json!({"status":"cancelled"}));
        for (command, args) in [
            ("client_configuration", json!({})),
            ("get_transfer_preferences", json!({})),
            ("discard_attachment", json!({"sourceHandle":"fake"})),
            (
                "start_attachment_upload",
                json!({"sourceHandle":"/etc/passwd","groupId":"group","clientAttachmentId":"draft"}),
            ),
            ("get_background_status", json!({})),
            ("reserve_audit_export", json!({})),
            ("cancel_audit_export", json!({"exportId":"unknown"})),
            (
                "export_audit",
                json!({"exportId":"unknown","format":"json","action":""}),
            ),
        ] {
            for (window, origin) in [(&window, "https://example.com"), (&other_window, local)] {
                let error = call(window, origin, command, args.clone()).expect_err(
                    "only the bundled main window may receive native export permissions",
                );
                assert!(
                    error
                        .as_str()
                        .is_some_and(|message| message.contains("not allowed")),
                    "expected an ACL denial, got {error}"
                );
            }
        }
    };
    let checked = std::panic::catch_unwind(std::panic::AssertUnwindSafe(result));
    tauri::async_runtime::block_on(app.state::<NativeState>().stop()).unwrap();
    if let Err(panic) = checked {
        std::panic::resume_unwind(panic);
    }
}
