//! Task metrics measure application payload, never recovered logical progress.
#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn pause_restart_retry_and_cache_do_not_inflate_rates() {
        let mut m = Meter::new(Record::default(), 0, 1000);
        m.phase(true, 0, 1000);
        m.payload(1000, 1000, 2000);
        m.credit(0, 1000, false, 1000, 2000);
        m.credit(0, 1000, false, 1000, 2000);
        m.payload(500, 2000, 3000);
        m.credit(1, 1000, true, 2000, 3000);
        m.stop(2000, 3000);
        let s = m.snapshot(2000, 3000, 2000, 4000);
        assert_eq!(s.network_bytes, 1500);
        assert_eq!(s.credited_network_bytes, 1000);
        assert_eq!(s.cache_bytes, 1000);
        assert_eq!(s.transfer_duration_ms, Some(2000));
        assert_eq!(s.average_bytes_per_second, Some(500.0));
        assert_eq!(s.current_bytes_per_second, 0.0);
        assert_eq!(s.eta_seconds, None);
        let record = serde_json::from_str(&serde_json::to_string(&m.record).unwrap()).unwrap();
        let mut resumed = Meter::new(record, 0, 3_603_000);
        resumed.phase(true, 0, 3_603_000);
        resumed.credit(0, 1000, false, 1000, 3_604_000);
        resumed.stop(1000, 3_604_000);
        let s = resumed.snapshot(1000, 3_604_000, 2000, 4000);
        assert_eq!(s.active_duration_ms, Some(3000));
        assert_eq!(s.credited_network_bytes, 1000);
        assert_eq!(s.wall_duration_ms, Some(3_603_000));
    }
    #[test]
    fn concurrent_payloads_share_time_and_stall_and_sleep_clear_eta() {
        let mut m = Meter::new(Record::default(), 0, 1000);
        m.phase(true, 0, 1000);
        for i in 0..4 {
            m.payload(1000, 1000, 2000);
            m.credit(i, 1000, false, 1000, 2000);
        }
        let s = m.snapshot(1000, 2000, 4000, 8000);
        assert_eq!(s.transfer_duration_ms, Some(1000));
        assert_eq!(s.average_bytes_per_second, Some(4000.0));
        assert!(s.eta_seconds.is_some());
        let s = m.snapshot(5001, 6001, 4000, 8000);
        assert_eq!(s.current_bytes_per_second, 0.0);
        assert_eq!(s.eta_seconds, None);
        let before = s.active_duration_ms;
        let s = m.snapshot(3_605_001, 3_606_001, 4000, 8000);
        assert_eq!(s.active_duration_ms, before);
    }
}
use serde::{Deserialize, Serialize};
use std::{
    collections::{BTreeSet, HashMap, VecDeque},
    sync::{Arc, Mutex, OnceLock, Weak},
    time::Instant,
};
const WINDOW: u64 = 4000;
#[derive(Debug, Clone, Default, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct Snapshot {
    pub schema_version: u32,
    /// False for legacy records whose pre-measurement task history is unknown.
    #[serde(default)]
    pub history_complete: bool,
    pub measured_since: Option<i64>,
    pub active_duration_ms: Option<u64>,
    pub transfer_duration_ms: Option<u64>,
    pub wall_duration_ms: Option<u64>,
    pub network_bytes: u64,
    pub credited_network_bytes: u64,
    pub cache_bytes: u64,
    pub resumed_bytes: u64,
    pub current_bytes_per_second: f64,
    pub average_bytes_per_second: Option<f64>,
    pub eta_seconds: Option<u64>,
    pub revision: u64,
}
#[derive(Debug, Clone, Default, Serialize, Deserialize)]
pub struct Record {
    pub snapshot: Snapshot,
    credited: BTreeSet<usize>,
    cached: BTreeSet<usize>,
}
impl Record {
    pub(super) fn fresh(snapshot: Snapshot) -> Self {
        Self {
            snapshot,
            ..Default::default()
        }
    }
}
pub struct Meter {
    pub record: Record,
    last: u64,
    last_wall: i64,
    window_start: u64,
    active: bool,
    transferring: bool,
    payloads: VecDeque<(u64, u64)>,
    credits: VecDeque<(u64, u64)>,
}
impl Meter {
    pub fn new(mut record: Record, now: u64, wall: i64) -> Self {
        let s = &mut record.snapshot;
        s.schema_version = 1;
        s.measured_since.get_or_insert(wall);
        s.active_duration_ms.get_or_insert(0);
        s.transfer_duration_ms.get_or_insert(0);
        s.wall_duration_ms.get_or_insert(0);
        s.current_bytes_per_second = 0.0;
        s.eta_seconds = None;
        Self {
            record,
            last: now,
            last_wall: wall,
            window_start: now,
            active: true,
            transferring: false,
            payloads: VecDeque::new(),
            credits: VecDeque::new(),
        }
    }
    fn tick(&mut self, now: u64, wall: i64) {
        let delta = now.saturating_sub(self.last);
        let wall_delta = wall.saturating_sub(self.last_wall).max(0) as u64;
        self.last = now;
        self.last_wall = wall;
        // A missed heartbeat beyond five seconds means sleep or a suspended process.
        // Exclude that unobserved interval rather than charge offline time as work.
        if delta > 5000 || wall_delta > 5000 {
            self.payloads.clear();
            self.credits.clear();
            self.window_start = now;
        } else if self.active {
            *self.record.snapshot.active_duration_ms.get_or_insert(0) += delta;
            if self.transferring {
                *self.record.snapshot.transfer_duration_ms.get_or_insert(0) += delta;
            }
        }
        if self.active {
            self.record.snapshot.wall_duration_ms = Some(
                wall.saturating_sub(self.record.snapshot.measured_since.unwrap_or(wall))
                    .max(0) as u64,
            );
        }
        while self
            .payloads
            .front()
            .is_some_and(|(t, _)| now.saturating_sub(*t) >= WINDOW)
        {
            self.payloads.pop_front();
        }
        while self
            .credits
            .front()
            .is_some_and(|(t, _)| now.saturating_sub(*t) >= WINDOW)
        {
            self.credits.pop_front();
        }
        self.record.snapshot.revision = self.record.snapshot.revision.saturating_add(1);
    }
    pub fn phase(&mut self, transferring: bool, now: u64, wall: i64) {
        self.tick(now, wall);
        if self.transferring != transferring {
            self.payloads.clear();
            self.credits.clear();
            self.window_start = now;
        }
        self.transferring = transferring;
    }
    pub fn payload(&mut self, bytes: u64, now: u64, wall: i64) {
        self.tick(now, wall);
        if self.active && self.transferring {
            self.record.snapshot.network_bytes += bytes;
            self.payloads.push_back((now, bytes));
        }
    }
    pub fn credit(&mut self, index: usize, bytes: u64, cache: bool, now: u64, wall: i64) {
        self.tick(now, wall);
        if cache {
            if self.record.cached.insert(index) {
                self.record.snapshot.cache_bytes += bytes;
            }
        } else if self.record.credited.insert(index) {
            self.record.snapshot.credited_network_bytes += bytes;
            self.credits.push_back((now, bytes));
        }
    }
    pub fn stop(&mut self, now: u64, wall: i64) {
        self.tick(now, wall);
        self.active = false;
        self.transferring = false;
        self.payloads.clear();
        self.credits.clear();
    }
    pub fn snapshot(&mut self, now: u64, wall: i64, done: u64, total: u64) -> Snapshot {
        self.tick(now, wall);
        let span = now.saturating_sub(self.window_start).min(WINDOW);
        let s = &mut self.record.snapshot;
        s.current_bytes_per_second = if self.active && self.transferring && span >= 250 {
            self.payloads.iter().map(|(_, n)| *n).sum::<u64>() as f64 * 1000.0 / span as f64
        } else {
            0.0
        };
        s.average_bytes_per_second = match (s.transfer_duration_ms, s.credited_network_bytes) {
            (Some(ms), bytes) if ms > 0 && bytes > 0 => Some(bytes as f64 * 1000.0 / ms as f64),
            _ => None,
        };
        let credit = self.credits.iter().map(|(_, n)| *n).sum::<u64>();
        s.eta_seconds =
            if self.active && self.transferring && span >= 1000 && credit > 0 && done < total {
                Some(((total - done) as f64 / (credit as f64 * 1000.0 / span as f64)).ceil() as u64)
            } else {
                None
            };
        s.clone()
    }
}
pub struct Live {
    started: Instant,
    meter: Mutex<Meter>,
}
impl Live {
    pub fn new(record: Record) -> Arc<Self> {
        Arc::new(Self {
            started: Instant::now(),
            meter: Mutex::new(Meter::new(record, 0, chrono::Utc::now().timestamp_millis())),
        })
    }
    fn now(&self) -> (u64, i64) {
        (
            self.started.elapsed().as_millis() as u64,
            chrono::Utc::now().timestamp_millis(),
        )
    }
    pub fn phase(&self, state: &super::models::TaskState) {
        use super::models::TaskState::*;
        let (n, w) = self.now();
        let mut m = self.meter.lock().unwrap();
        if matches!(state, Paused | Completed | Failed) {
            m.stop(n, w);
        } else {
            m.phase(matches!(state, Transferring), n, w);
        }
    }
    pub fn payload(&self, bytes: usize) {
        let (n, w) = self.now();
        self.meter.lock().unwrap().payload(bytes as u64, n, w);
    }
    pub fn credit(&self, index: usize, bytes: u64, cache: bool) {
        let (n, w) = self.now();
        self.meter.lock().unwrap().credit(index, bytes, cache, n, w);
    }
    pub fn resumed(&self, bytes: u64) {
        let mut m = self.meter.lock().unwrap();
        m.record.snapshot.resumed_bytes = m.record.snapshot.resumed_bytes.max(bytes);
    }
    pub fn record(&self, done: u64, total: u64) -> Record {
        let (n, w) = self.now();
        let mut m = self.meter.lock().unwrap();
        m.snapshot(n, w, done, total);
        m.record.clone()
    }
}
type Registry = Mutex<HashMap<(String, String), Weak<Live>>>;
fn registry() -> &'static Registry {
    static R: OnceLock<Registry> = OnceLock::new();
    R.get_or_init(Mutex::default)
}
pub fn register(owner: &str, task: &str, live: &Arc<Live>) {
    registry()
        .lock()
        .unwrap()
        .insert((owner.into(), task.into()), Arc::downgrade(live));
}
pub fn observer(owner: &str, task: &str) -> Option<Arc<Live>> {
    registry()
        .lock()
        .unwrap()
        .get(&(owner.into(), task.into()))
        .and_then(Weak::upgrade)
}
pub fn remove(owner: &str, task: &str) {
    registry()
        .lock()
        .unwrap()
        .remove(&(owner.into(), task.into()));
}
