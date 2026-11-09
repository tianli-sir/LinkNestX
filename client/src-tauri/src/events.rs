use serde_json::Value;
pub trait EventSink: Send + Sync {
    fn emit(&self, name: &str, value: Value);
}
pub struct NoopEvents;
impl EventSink for NoopEvents {
    fn emit(&self, _: &str, _: Value) {}
}
pub struct DesktopEvents(pub tauri::AppHandle);
impl EventSink for DesktopEvents {
    fn emit(&self, name: &str, value: Value) {
        use tauri::Emitter;
        let _ = self.0.emit(name, value);
    }
}
