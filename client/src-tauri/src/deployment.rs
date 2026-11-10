//! Release-time binding. No runtime environment, preference or IPC can replace it.
pub const SERVER_ORIGIN: &str = "http://192.168.31.41:18080";
pub fn configuration() -> serde_json::Value {
    serde_json::json!({"serverOrigin":SERVER_ORIGIN,"version":env!("CARGO_PKG_VERSION"),"platform":std::env::consts::OS,"capabilities":{"fileUpload":true,"nativeTransfers":true,"clipboardAttachments":true,"avatarEdit":true,"updates":true,"p2p":true}})
}
#[cfg(test)]
mod tests {
    #[test]
    fn production_binding_ignores_legacy_frontend_origin() {
        let root = tempfile::tempdir().unwrap();
        let auth = crate::auth::session::AuthManager::production(root.path()).unwrap();
        assert_eq!(
            auth.login_origin("https://attacker.invalid").unwrap(),
            super::SERVER_ORIGIN
        );
        assert_eq!(
            auth.login_origin("http://localhost:9999").unwrap(),
            super::SERVER_ORIGIN
        );
    }
}
