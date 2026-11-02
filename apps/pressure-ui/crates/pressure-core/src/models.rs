use serde::{Deserialize, Serialize};

#[derive(Debug, Clone, Copy, PartialEq, Eq, Default, Serialize, Deserialize)]
#[serde(rename_all = "lowercase")]
pub enum PressureLevel {
    #[default]
    Unknown,
    Normal,
    Warning,
    Red,
    Critical,
}

impl PressureLevel {
    pub fn label(self) -> &'static str {
        match self {
            Self::Unknown => "—",
            Self::Normal => "OK",
            Self::Warning => "WARN",
            Self::Red => "RED",
            Self::Critical => "CRIT",
        }
    }

    pub fn rank(self) -> u8 {
        match self {
            Self::Normal => 1,
            Self::Warning => 2,
            Self::Red => 3,
            Self::Critical => 4,
            Self::Unknown => 0,
        }
    }
}

#[derive(Debug, Clone, Default, Serialize, Deserialize)]
pub struct BoardEnvelope {
    #[serde(default)]
    pub ok: bool,
    #[serde(default)]
    pub action: Option<String>,
    #[serde(default)]
    pub snapshot: Option<PressureSnapshot>,
    #[serde(default)]
    pub latest_monitor: Option<PressureSnapshot>,
    #[serde(default)]
    pub health: Option<StatusHealth>,
    #[serde(default)]
    pub coverage: Option<CoverageReport>,
    #[serde(default)]
    pub admission: Option<Admission>,
    #[serde(default)]
    pub policy: Option<PressurePolicy>,
    #[serde(default)]
    pub policy_path: Option<String>,
    #[serde(default)]
    pub work: Option<WorkStatus>,
    #[serde(default)]
    pub launchd: Option<LaunchdStatus>,
    #[serde(default)]
    pub doctor: Option<DoctorEnvelope>,
    #[serde(default)]
    pub calibration: Option<WorkCalibration>,
    #[serde(default)]
    pub has_recovery_hint: bool,
    #[serde(default)]
    pub recovery_hint: Option<RecoveryHint>,
    #[serde(default)]
    pub inventory: Option<IdleInventory>,
    #[serde(default)]
    pub idle_error: Option<String>,
    #[serde(default)]
    pub events: Vec<TelemetryEvent>,
    #[serde(default)]
    pub actions: Vec<PressureAction>,
}

#[derive(Debug, Clone, Default, Serialize, Deserialize)]
pub struct PressureSnapshot {
    #[serde(default)]
    pub schema_version: Option<i64>,
    #[serde(default)]
    pub timestamp: Option<String>,
    #[serde(default)]
    pub level: PressureLevel,
    #[serde(default)]
    pub reasons: Vec<String>,
    #[serde(default)]
    pub free_percent: i64,
    #[serde(default)]
    pub physical_memory_mb: f64,
    #[serde(default)]
    pub swap_used_mb: f64,
    #[serde(default)]
    pub logical_cpu_count: i64,
    #[serde(default)]
    pub host_cpu_percent: f64,
    #[serde(default)]
    pub host_cpu_available: bool,
    #[serde(default)]
    pub host_cpu_source: Option<String>,
    #[serde(default)]
    pub agent_cpu_percent: f64,
    #[serde(default)]
    pub agent_tree_count: i64,
    #[serde(default)]
    pub agent_rss_sum_mb: f64,
    #[serde(default)]
    pub process_count: i64,
    #[serde(default)]
    pub process_rss_sum_mb: f64,
    #[serde(default)]
    pub memory_momentum: Option<String>,
    #[serde(default)]
    pub free_percent_slope_per_minute: f64,
    #[serde(default)]
    pub top_host_consumers: Vec<HostConsumer>,
    #[serde(default)]
    pub top_agent_trees: Vec<AgentTree>,
    #[serde(default)]
    pub storage: Option<StorageSnapshot>,
    #[serde(default)]
    pub disk_write: Option<DiskWriteSummary>,
    #[serde(default)]
    pub guard_pid: i64,
    #[serde(default)]
    pub guard_rss_mb: f64,
    #[serde(default)]
    pub guard_budget_ok: bool,
}

impl PressureSnapshot {
    pub fn memory_used_percent(&self) -> i64 {
        (100 - self.free_percent).clamp(0, 100)
    }
}

#[derive(Debug, Clone, Default, Serialize, Deserialize)]
pub struct HostConsumer {
    #[serde(default)]
    pub executable: Option<String>,
    #[serde(default)]
    pub rss_sum_mb: f64,
    #[serde(default, alias = "cpu_percent_sum")]
    pub cpu_percent: f64,
    #[serde(default)]
    pub process_count: i64,
    #[serde(default)]
    pub category: Option<String>,
}

#[derive(Debug, Clone, Default, Serialize, Deserialize)]
pub struct AgentTree {
    #[serde(default)]
    pub agent: Option<String>,
    #[serde(default)]
    pub executable: Option<String>,
    #[serde(default)]
    pub root_pid: i64,
    #[serde(default)]
    pub process_count: i64,
    #[serde(default)]
    pub rss_sum_mb: f64,
    #[serde(default)]
    pub cpu_percent_sum: f64,
    #[serde(default)]
    pub elapsed_seconds: i64,
    #[serde(default)]
    pub session_id: Option<String>,
    #[serde(default)]
    pub quiescent: bool,
}

#[derive(Debug, Clone, Default, Serialize, Deserialize)]
pub struct StorageSnapshot {
    #[serde(default)]
    pub available: bool,
    #[serde(default)]
    pub level: Option<String>,
    #[serde(default)]
    pub free_percent: f64,
    #[serde(default)]
    pub free_bytes: i64,
    #[serde(default)]
    pub total_bytes: i64,
    #[serde(default)]
    pub volume_path: Option<String>,
    #[serde(default)]
    pub source: Option<String>,
}

#[derive(Debug, Clone, Default, Serialize, Deserialize)]
pub struct DiskWriteSummary {
    #[serde(default, alias = "state")]
    pub level: Option<String>,
    #[serde(default, alias = "current_bytes_per_second")]
    pub bytes_per_second: f64,
    #[serde(default, alias = "measurement_window_seconds")]
    pub window_seconds: f64,
    #[serde(default, alias = "writer_available_count")]
    pub writer_count: i64,
    #[serde(default)]
    pub confidence: Option<String>,
    #[serde(default)]
    pub bytes_24h: i64,
    #[serde(default)]
    pub window_15m_bytes: i64,
    #[serde(default)]
    pub top_writer: Option<DiskWriter>,
}

#[derive(Debug, Clone, Default, Serialize, Deserialize)]
pub struct StatusHealth {
    #[serde(default)]
    pub operator_ready: bool,
    #[serde(default)]
    pub daily_driver_ready: bool,
    #[serde(default)]
    pub monitor_healthy: bool,
    #[serde(default)]
    pub protection_mode: Option<String>,
    #[serde(default)]
    pub operator_reasons: Vec<String>,
}

#[derive(Debug, Clone, Default, Serialize, Deserialize)]
pub struct CoverageReport {
    #[serde(default)]
    pub status: Option<String>,
    #[serde(default)]
    pub repo_root: Option<String>,
    #[serde(default)]
    pub surfaces: Vec<CoverageSurface>,
    #[serde(default)]
    pub limitations: serde_json::Value,
}

#[derive(Debug, Clone, Default, Serialize, Deserialize)]
pub struct CoverageSurface {
    #[serde(default)]
    pub id: Option<String>,
    #[serde(default)]
    pub name: Option<String>,
    #[serde(default)]
    pub status: Option<String>,
    #[serde(default)]
    pub kind: Option<String>,
}

#[derive(Debug, Clone, Default, Serialize, Deserialize)]
pub struct Admission {
    #[serde(default)]
    pub allowed: bool,
    #[serde(default)]
    pub level: Option<PressureLevel>,
    #[serde(default)]
    pub source: Option<String>,
    #[serde(default)]
    pub warning: Option<String>,
    #[serde(default)]
    pub reasons: Vec<String>,
    #[serde(default)]
    pub dimension: Option<String>,
}

#[derive(Debug, Clone, Default, Serialize, Deserialize)]
pub struct PressurePolicy {
    #[serde(default)]
    pub enabled: bool,
    #[serde(default)]
    pub enforce_admission: bool,
    #[serde(default)]
    pub auto_shed_critical: bool,
    #[serde(default)]
    pub profile: Option<String>,
    #[serde(default)]
    pub block_new_at: Option<String>,
    #[serde(default)]
    pub schema_version: Option<i64>,
}

impl PressurePolicy {
    pub fn mode_label(&self) -> String {
        if let Some(profile) = &self.profile {
            if !profile.is_empty() {
                return profile.replace('-', " ");
            }
        }
        if !self.enabled {
            return "observe".into();
        }
        if self.auto_shed_critical {
            return "full protection".into();
        }
        if self.enforce_admission {
            return "admission".into();
        }
        "observe".into()
    }
}

#[derive(Debug, Clone, Default, Serialize, Deserialize)]
pub struct WorkStatus {
    #[serde(default)]
    pub schema_version: Option<i64>,
    #[serde(default)]
    pub capacity: i64,
    #[serde(default)]
    pub used: i64,
    #[serde(default)]
    pub available: i64,
    #[serde(default)]
    pub leases: Vec<WorkLease>,
    #[serde(default)]
    pub waiters: Vec<WorkWaiter>,
    #[serde(default)]
    pub queue_depth: i64,
    #[serde(default)]
    pub state_path: Option<String>,
    #[serde(default)]
    pub scheduling_policy: Option<String>,
    #[serde(default)]
    pub decision_reason: Option<String>,
    #[serde(default)]
    pub override_operation_id: Option<String>,
    #[serde(default)]
    pub override_queue: Vec<String>,
    #[serde(default)]
    pub override_queue_depth: i64,
    #[serde(default)]
    pub admission_holds: Vec<WorkAdmissionHold>,
    #[serde(default)]
    pub admission_hold_count: i64,
    #[serde(default)]
    pub admission_latch: Option<WorkAdmissionLatch>,
}

#[derive(Debug, Clone, Default, Serialize, Deserialize)]
pub struct WorkLease {
    #[serde(default)]
    pub id: String,
    #[serde(default)]
    pub operation_id: Option<String>,
    #[serde(default, rename = "class")]
    pub class_name: String,
    #[serde(default)]
    pub weight: i64,
    #[serde(default)]
    pub pid: Option<i64>,
    #[serde(default)]
    pub started_at: Option<String>,
    #[serde(default)]
    pub age_ms: i64,
    #[serde(default)]
    pub review: bool,
    #[serde(default)]
    pub review_reason: Option<String>,
}

impl WorkLease {
    pub fn op_id(&self) -> &str {
        self.operation_id.as_deref().filter(|s| !s.is_empty()).unwrap_or(&self.id)
    }
}

#[derive(Debug, Clone, Default, Serialize, Deserialize)]
pub struct WorkWaiter {
    #[serde(default)]
    pub operation_id: String,
    #[serde(default, rename = "class")]
    pub class_name: String,
    #[serde(default)]
    pub weight: i64,
    #[serde(default)]
    pub pid: Option<i64>,
    #[serde(default)]
    pub position: Option<i64>,
    #[serde(default)]
    pub wait_ms: i64,
    #[serde(default)]
    pub protected: bool,
    #[serde(default)]
    pub protection_reason: Option<String>,
    #[serde(default)]
    pub reservation_kind: Option<String>,
    #[serde(default)]
    pub override_position: i64,
}

#[derive(Debug, Clone, Default, Serialize, Deserialize)]
pub struct WorkAdmissionHold {
    #[serde(default)]
    pub operation_id: String,
    #[serde(default, rename = "class")]
    pub class_name: String,
    #[serde(default)]
    pub weight: i64,
    #[serde(default)]
    pub pid: Option<i64>,
    #[serde(default)]
    pub held_for_ms: i64,
    #[serde(default)]
    pub dimension: Option<String>,
    #[serde(default)]
    pub reason: Option<String>,
}

#[derive(Debug, Clone, Default, Serialize, Deserialize)]
pub struct WorkAdmissionLatch {
    #[serde(default)]
    pub latched: bool,
    #[serde(default)]
    pub dimension: Option<String>,
    #[serde(default)]
    pub reason: Option<String>,
    #[serde(default)]
    pub red_samples: i64,
    #[serde(default)]
    pub recovery_samples: i64,
    #[serde(default)]
    pub release_required: i64,
}

#[derive(Debug, Clone, Default, Serialize, Deserialize)]
pub struct LaunchdStatus {
    #[serde(default)]
    pub ok: Option<bool>,
    #[serde(default)]
    pub label: Option<String>,
    #[serde(default)]
    pub installed: bool,
    #[serde(default)]
    pub loaded: bool,
    #[serde(default)]
    pub pid: Option<i64>,
    #[serde(default)]
    pub plist_path: Option<String>,
    #[serde(default)]
    pub artifact_present: Option<bool>,
    #[serde(default)]
    pub artifact_verified: Option<bool>,
}

#[derive(Debug, Clone, Default, Serialize, Deserialize)]
pub struct DoctorEnvelope {
    #[serde(default)]
    pub ok: bool,
    #[serde(default)]
    pub protection_mode: Option<String>,
    #[serde(default)]
    pub warnings: Vec<String>,
    #[serde(default)]
    pub fixes: Vec<serde_json::Value>,
}

#[derive(Debug, Clone, Default, Serialize, Deserialize)]
pub struct WorkCalibration {
    #[serde(default)]
    pub operation_count: i64,
    #[serde(default)]
    pub wrapper_interrupt_operations: i64,
    #[serde(default)]
    pub suggested_policy_profile: Option<String>,
    #[serde(default)]
    pub suggested_policy_profile_reason: Option<String>,
}

#[derive(Debug, Clone, Default, Serialize, Deserialize)]
pub struct RecoveryHint {
    #[serde(default)]
    pub reason: Option<String>,
    #[serde(default)]
    pub recovery_command: Option<String>,
    #[serde(default)]
    pub detected_at: Option<String>,
}

#[derive(Debug, Clone, Default, Serialize, Deserialize)]
pub struct IdleInventory {
    #[serde(default)]
    pub candidate_count: i64,
    #[serde(default)]
    pub candidates: Vec<AgentTree>,
}

#[derive(Debug, Clone, Default, Serialize, Deserialize)]
pub struct TelemetryEvent {
    #[serde(default)]
    pub timestamp: Option<String>,
    #[serde(default)]
    pub event: Option<String>,
    #[serde(default)]
    pub level: Option<String>,
    #[serde(default)]
    pub reason: Option<String>,
    #[serde(default)]
    pub summary: Option<TelemetrySummary>,
}

#[derive(Debug, Clone, Default, Serialize, Deserialize)]
pub struct TelemetrySummary {
    #[serde(default)]
    pub level: Option<String>,
    #[serde(default)]
    pub free_percent: i64,
    #[serde(default)]
    pub host_cpu_percent: f64,
    #[serde(default)]
    pub agent_tree_count: i64,
    #[serde(default)]
    pub agent_rss_sum_mb: f64,
}

#[derive(Debug, Clone, Default, Serialize, Deserialize)]
pub struct PressureAction {
    #[serde(default)]
    pub timestamp: Option<String>,
    #[serde(default)]
    pub action: Option<String>,
    #[serde(default)]
    pub outcome: Option<String>,
}

#[derive(Debug, Clone, Default, Serialize, Deserialize)]
pub struct WorkInspectEnvelope {
    #[serde(default)]
    pub ok: bool,
    #[serde(default)]
    pub inspect: Option<WorkInspectReport>,
}

#[derive(Debug, Clone, Default, Serialize, Deserialize)]
pub struct WorkInspectReport {
    #[serde(default)]
    pub operation_id: String,
    #[serde(default)]
    pub stall_class: String,
    #[serde(default)]
    pub summary: String,
    #[serde(default)]
    pub origin: WorkInspectOrigin,
    #[serde(default)]
    pub process: Option<WorkProcessSnapshot>,
    #[serde(default)]
    pub calibration: WorkInspectCalibration,
    #[serde(default)]
    pub output: WorkInspectOutput,
    #[serde(default)]
    pub suggested_actions: Vec<WorkInspectAction>,
}

impl WorkInspectReport {
    pub fn preview_text(&self) -> Option<&str> {
        self.output
            .text
            .as_deref()
            .filter(|s| !s.is_empty())
            .or(self.origin.schedule_snippet.as_deref().filter(|s| !s.is_empty()))
    }

    pub fn command(&self) -> Option<&str> {
        self.process.as_ref().and_then(|p| p.command.as_deref())
    }
}

#[derive(Debug, Clone, Default, Serialize, Deserialize)]
pub struct WorkInspectOrigin {
    #[serde(default)]
    pub kind: String,
    #[serde(default)]
    pub schedule_id: Option<String>,
    #[serde(default)]
    pub schedule_name: Option<String>,
    #[serde(default)]
    pub schedule_snippet: Option<String>,
    #[serde(default)]
    pub agent_kind: Option<String>,
}

#[derive(Debug, Clone, Default, Serialize, Deserialize)]
pub struct WorkProcessSnapshot {
    #[serde(default)]
    pub pid: i64,
    #[serde(default)]
    pub ppid: Option<i64>,
    #[serde(default)]
    pub command: Option<String>,
    #[serde(default)]
    pub cwd: Option<String>,
    #[serde(default)]
    pub state: Option<String>,
    #[serde(default)]
    pub parent_dead: bool,
    #[serde(default)]
    pub alive: bool,
}

#[derive(Debug, Clone, Default, Serialize, Deserialize)]
pub struct WorkInspectCalibration {
    #[serde(default)]
    pub age_ms: i64,
    #[serde(default)]
    pub class_p95_runtime_ms: Option<i64>,
    #[serde(default)]
    pub class_p95_known: bool,
}

#[derive(Debug, Clone, Default, Serialize, Deserialize)]
pub struct WorkInspectOutput {
    #[serde(default)]
    pub source: String,
    #[serde(default)]
    pub text: Option<String>,
    #[serde(default)]
    pub truncated: bool,
    #[serde(default)]
    pub byte_count: Option<i64>,
}

#[derive(Debug, Clone, Default, Serialize, Deserialize)]
pub struct WorkInspectAction {
    #[serde(default)]
    pub id: String,
    #[serde(default)]
    pub available: bool,
    #[serde(default)]
    pub reason: Option<String>,
    #[serde(default)]
    pub command: Option<String>,
}

#[derive(Debug, Clone, Default, Serialize, Deserialize)]
pub struct WorkHistoryEnvelope {
    #[serde(default)]
    pub ok: bool,
    #[serde(default)]
    pub work_events: Vec<WorkLifecycleEvent>,
}

#[derive(Debug, Clone, Default, Serialize, Deserialize)]
pub struct WorkLifecycleEvent {
    #[serde(default)]
    pub event_id: Option<String>,
    #[serde(default)]
    pub timestamp: Option<String>,
    #[serde(default)]
    pub event: String,
    #[serde(default)]
    pub operation_id: String,
    #[serde(default)]
    pub outcome: Option<String>,
    #[serde(default)]
    pub class: Option<String>,
    #[serde(default)]
    pub wait_ms: Option<i64>,
    #[serde(default)]
    pub runtime_ms: Option<i64>,
}

#[derive(Debug, Clone, Default, Serialize, Deserialize)]
pub struct StorageProvidersEnvelope {
    #[serde(default)]
    pub ok: bool,
    #[serde(default)]
    pub providers: Vec<StorageProvider>,
}

#[derive(Debug, Clone, Default, Serialize, Deserialize)]
pub struct StorageProvider {
    #[serde(default)]
    pub id: String,
    #[serde(default)]
    pub title: Option<String>,
    #[serde(default)]
    pub reclaimable_bytes: i64,
    #[serde(default)]
    pub available: bool,
}

#[derive(Debug, Clone, Default, Serialize, Deserialize)]
pub struct DiskWriteStatusEnvelope {
    #[serde(default)]
    pub ok: bool,
    #[serde(default)]
    pub summary: Option<DiskWriteSummary>,
    #[serde(default)]
    pub writers: Vec<DiskWriter>,
}

#[derive(Debug, Clone, Default, Serialize, Deserialize)]
pub struct DiskWriter {
    #[serde(default)]
    pub executable: Option<String>,
    #[serde(default)]
    pub pid: Option<i64>,
    #[serde(default)]
    pub bytes_per_second: f64,
    #[serde(default)]
    pub rss_mb: f64,
}

#[derive(Debug, Clone, Default)]
pub struct PressureBoard {
    pub snapshot: PressureSnapshot,
    pub health: Option<StatusHealth>,
    pub coverage: Option<CoverageReport>,
    pub admission: Option<Admission>,
    pub policy: Option<PressurePolicy>,
    pub policy_path: Option<String>,
    pub work: Option<WorkStatus>,
    pub launchd: Option<LaunchdStatus>,
    pub doctor: Option<DoctorEnvelope>,
    pub calibration: Option<WorkCalibration>,
    pub has_recovery_hint: bool,
    pub recovery_hint: Option<RecoveryHint>,
    pub idle_candidates: Vec<AgentTree>,
    pub idle_error: Option<String>,
    pub telemetry_events: Vec<TelemetryEvent>,
    pub relief_actions: Vec<PressureAction>,
    pub binary_path: String,
    pub refreshed_at: String,
    pub live_sample: bool,
}

impl PressureBoard {
    pub fn from_envelope(env: BoardEnvelope, binary_path: String, live: bool) -> Self {
        let snapshot = env
            .snapshot
            .or(env.latest_monitor)
            .unwrap_or_default();
        Self {
            snapshot,
            health: env.health,
            coverage: env.coverage,
            admission: env.admission,
            policy: env.policy,
            policy_path: env.policy_path,
            work: env.work,
            launchd: env.launchd,
            doctor: env.doctor,
            calibration: env.calibration,
            has_recovery_hint: env.has_recovery_hint,
            recovery_hint: env.recovery_hint,
            idle_candidates: env.inventory.map(|i| i.candidates).unwrap_or_default(),
            idle_error: env.idle_error,
            telemetry_events: env.events,
            relief_actions: env.actions,
            binary_path,
            refreshed_at: chrono_now(),
            live_sample: live,
        }
    }

    pub fn level(&self) -> PressureLevel {
        self.snapshot.level
    }
}

fn chrono_now() -> String {
    // Keep this crate chrono-free; UI formats relative locally.
    std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .map(|d| format!("{}", d.as_secs()))
        .unwrap_or_default()
}

#[derive(Debug, Clone, Copy, PartialEq, Eq, Default)]
pub enum Section {
    #[default]
    Overview,
    Trees,
    DiskWrites,
    Storage,
    Work,
    Policy,
    Monitor,
    Telemetry,
}

impl Section {
    pub fn all() -> &'static [Section] {
        &[
            Self::Overview,
            Self::Trees,
            Self::DiskWrites,
            Self::Storage,
            Self::Work,
            Self::Policy,
            Self::Monitor,
            Self::Telemetry,
        ]
    }

    pub fn title(self) -> &'static str {
        match self {
            Self::Overview => "Overview",
            Self::Trees => "Agent Trees",
            Self::DiskWrites => "Disk Writes",
            Self::Storage => "Storage",
            Self::Work => "Work Queue",
            Self::Policy => "Policy",
            Self::Monitor => "Monitor",
            Self::Telemetry => "Telemetry",
        }
    }

    pub fn shortcut(self) -> char {
        match self {
            Self::Overview => '1',
            Self::Trees => '2',
            Self::DiskWrites => '3',
            Self::Storage => '4',
            Self::Work => '5',
            Self::Policy => '6',
            Self::Monitor => '7',
            Self::Telemetry => '8',
        }
    }
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub enum WorkSelection {
    Lease { operation_id: String, class_name: String, weight: i64, pid: Option<i64>, lease_id: String },
    Waiter { operation_id: String, class_name: String, weight: i64, pid: Option<i64> },
    Historical { operation_id: String, class_name: String, weight: i64, previous: String },
}

impl WorkSelection {
    pub fn operation_id(&self) -> &str {
        match self {
            Self::Lease { operation_id, .. }
            | Self::Waiter { operation_id, .. }
            | Self::Historical { operation_id, .. } => operation_id,
        }
    }

    pub fn is_lease(&self) -> bool {
        matches!(self, Self::Lease { .. })
    }

    pub fn is_waiter(&self) -> bool {
        matches!(self, Self::Waiter { .. })
    }

    pub fn class_name(&self) -> &str {
        match self {
            Self::Lease { class_name, .. }
            | Self::Waiter { class_name, .. }
            | Self::Historical { class_name, .. } => class_name,
        }
    }
}
