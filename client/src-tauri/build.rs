fn main() {
    // Tauri's resource compiler links only application binaries. The manifest
    // must also reach libtest and integration tests that import Common Controls v6.
    // Keep Tauri's icon/version resources, but embed this manifest only once.
    tauri_build::try_build(
        tauri_build::Attributes::new()
            .windows_attributes(tauri_build::WindowsAttributes::new_without_app_manifest())
            .app_manifest(tauri_build::AppManifest::new().commands(&[
                "get_update_status",
                "check_for_updates",
                "install_ready_update",
                "defer_update",
                "client_configuration",
                "get_transfer_preferences",
                "set_transfer_preferences",
                "pick_upload_files",
                "read_clipboard_attachments",
                "discard_attachment",
                "start_attachment_upload",
                "start_document_attachment_upload",
                "document_attachment_preview",
                "open_external_url",
                "set_profile_avatar",
                "profile_avatar",
                "app_version",
                "login",
                "logout",
                "api_request",
                "reserve_audit_export",
                "export_audit",
                "cancel_audit_export",
                "start_download",
                "pause_transfer",
                "resume_transfer",
                "list_transfers",
                "get_background_status",
                "send_server_event",
                "pick_download_destination",
            ])),
    )
    .expect("unable to build desktop permissions");

    println!("cargo:rerun-if-changed=windows-app.manifest");
    println!("cargo:rerun-if-changed=windows-app-manifest.rc");
    if std::env::var("CARGO_CFG_TARGET_OS").as_deref() == Ok("windows") {
        embed_resource::compile_for_everything("windows-app-manifest.rc", embed_resource::NONE)
            .manifest_required()
            .expect("unable to embed Windows Common Controls v6 manifest");
    }
}
