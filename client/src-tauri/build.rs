fn main() {
    tauri_build::try_build(tauri_build::Attributes::new().app_manifest(
        tauri_build::AppManifest::new().commands(&[
            "app_version",
            "login",
            "logout",
            "api_request",
            "start_download",
            "start_upload",
            "pause_transfer",
            "resume_transfer",
            "list_transfers",
            "get_sharing_policy",
            "set_sharing_policy",
            "send_server_event",
            "pick_upload_file",
            "pick_download_destination",
        ]),
    ))
    .expect("unable to build desktop permissions")
}
