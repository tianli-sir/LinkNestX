#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn install_and_work_are_mutually_exclusive_and_release_on_drop() {
        let gate = std::sync::Arc::new(ActivityGate::default());
        let work = gate.try_enter().unwrap();
        assert!(gate.try_quiesce().is_err());
        drop(work);
        let installation = gate.try_quiesce().unwrap();
        assert!(gate.try_enter().is_err());
        assert!(gate.try_quiesce().is_err());
        drop(installation);
        assert!(gate.try_enter().is_ok());
    }
}
use crate::{Result, TransferError};
use std::sync::{Arc, Mutex};
#[derive(Default)]
struct State {
    active: usize,
    quiescing: bool,
}
#[derive(Default)]
pub struct ActivityGate {
    state: Mutex<State>,
}
pub struct ActivityPermit {
    gate: Arc<ActivityGate>,
}
pub struct InstallationPermit {
    gate: Arc<ActivityGate>,
}
impl ActivityGate {
    pub fn try_enter(self: &Arc<Self>) -> Result<ActivityPermit> {
        let mut s = self.state.lock().unwrap();
        if s.quiescing {
            return Err(TransferError::new("正在安装更新，请稍后重试"));
        }
        s.active += 1;
        Ok(ActivityPermit { gate: self.clone() })
    }
    pub fn try_quiesce(self: &Arc<Self>) -> Result<InstallationPermit> {
        let mut s = self.state.lock().unwrap();
        if s.quiescing || s.active > 0 {
            return Err(TransferError::new("活动任务结束后才能安装更新"));
        }
        s.quiescing = true;
        Ok(InstallationPermit { gate: self.clone() })
    }
    pub fn active_count(&self) -> usize {
        self.state.lock().unwrap().active
    }
}
impl Drop for ActivityPermit {
    fn drop(&mut self) {
        self.gate.state.lock().unwrap().active -= 1;
    }
}
impl Drop for InstallationPermit {
    fn drop(&mut self) {
        self.gate.state.lock().unwrap().quiescing = false;
    }
}
