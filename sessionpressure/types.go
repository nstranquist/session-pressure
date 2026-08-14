// Package sessionpressure protects a developer workstation from sustained
// memory pressure caused by many concurrent agent process trees.
package sessionpressure

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

func validCPUEvidence(available bool, percent float64) bool {
	return available && !math.IsNaN(percent) && !math.IsInf(percent, 0) && percent >= 0
}

const SchemaVersion = 1

// SemanticState is the privacy-safe projection of an agent hook's current
// interaction state. Unknown preserves the CPU-quiescence fallback for agents
// without compatible hooks; busy is never eligible for automatic relief.
type SemanticState string

const (
	SemanticStateUnknown SemanticState = "unknown"
	SemanticStateReady   SemanticState = "ready"
	SemanticStateBusy    SemanticState = "busy"
)

// Level is ordered from healthy to dangerous.
type Level string

const (
	LevelNormal   Level = "normal"
	LevelWarning  Level = "warning"
	LevelRed      Level = "red"
	LevelCritical Level = "critical"
)

func levelRank(level Level) int {
	switch level {
	case LevelWarning:
		return 1
	case LevelRed:
		return 2
	case LevelCritical:
		return 3
	default:
		return 0
	}
}

// StorageSnapshot is a constant-cost filesystem-capacity observation. It is a
// separate pressure dimension: its Level can gate disk-growing work but never
// contributes to memory-oriented process shedding.
type StorageSnapshot struct {
	Available          bool      `json:"available"`
	VolumePath         string    `json:"volume_path"`
	Source             string    `json:"source,omitempty"`
	CapturedAt         time.Time `json:"captured_at,omitempty,omitzero"`
	TotalBytes         int64     `json:"total_bytes,omitempty"`
	FreeBytes          int64     `json:"free_bytes,omitempty"`
	AvailableBytes     int64     `json:"available_bytes,omitempty"`
	FreePercent        float64   `json:"free_percent,omitempty"`
	Level              Level     `json:"level"`
	InstantaneousLevel Level     `json:"instantaneous_level"`
	HysteresisActive   bool      `json:"hysteresis_active,omitempty"`
	ReleaseBytes       int64     `json:"release_bytes,omitempty"`
	Reasons            []string  `json:"reasons,omitempty"`
	Error              string    `json:"error,omitempty"`
}

func maxLevel(a, b Level) Level {
	if levelRank(b) > levelRank(a) {
		return b
	}
	return a
}

// AtLeast reports whether the receiver is at or above the requested level.
func (level Level) AtLeast(want Level) bool {
	return levelRank(level) >= levelRank(want)
}

// Process is one row from the host process table. PIDs is deliberately kept
// out of durable telemetry except for tree roots; full commands are never
// persisted because agent prompts can contain sensitive text.
type Process struct {
	PID          int     `json:"pid"`
	PPID         int     `json:"ppid"`
	RSSKB        int64   `json:"rss_kb"`
	CPUPercent   float64 `json:"cpu_percent"`
	CPUAvailable bool    `json:"cpu_available"`
	Elapsed      string  `json:"elapsed,omitempty"`
	Command      string  `json:"-"`

	// Native samplers populate identity directly so macOS does not need to
	// materialize every process's potentially prompt-bearing command line.
	Agent          string `json:"-"`
	Executable     string `json:"-"`
	SessionID      string `json:"-"`
	ElapsedSeconds int64  `json:"-"`
	CPUTotalNS     uint64 `json:"-"`
	CPUTotalValid  bool   `json:"-"`
	CPUStartID     uint64 `json:"-"`
	StartedAtNS    int64  `json:"-"`
	DiskWriteBytes uint64 `json:"-"`
	DiskWriteValid bool   `json:"-"`
}

// AgentTree is one top-level Claude, Codex, Grok, or Kimi process plus descendants.
// RSS is a sum of resident-set readings and can double count shared pages; its
// name is explicit so it is not mistaken for physically unique memory.
type AgentTree struct {
	Agent            string        `json:"agent"`
	RootPID          int           `json:"root_pid"`
	SessionID        string        `json:"session_id,omitempty"`
	Executable       string        `json:"executable"`
	ProcessCount     int           `json:"process_count"`
	RSSSumMB         float64       `json:"rss_sum_mb"`
	CPUPercentSum    float64       `json:"cpu_percent_sum"`
	CPUAvailable     bool          `json:"cpu_available"`
	ElapsedSeconds   int64         `json:"elapsed_seconds,omitempty"`
	QuiescentSamples int           `json:"quiescent_samples,omitempty"`
	SemanticState    SemanticState `json:"semantic_state,omitempty"`
	SemanticStateAt  time.Time     `json:"semantic_state_at,omitempty,omitzero"`
	PIDs             []int         `json:"-"`
}

// HostConsumer is a privacy-safe whole-host attribution bucket. It groups
// processes by executable basename and never carries PIDs, arguments,
// environment variables, paths, or prompt text. AgentProcessCount identifies
// how much of the bucket belongs to a known agent tree without granting any
// new automatic relief authority over non-agent processes.
type HostConsumer struct {
	Executable        string  `json:"executable"`
	Category          string  `json:"category"`
	ProcessCount      int     `json:"process_count"`
	RSSSumMB          float64 `json:"rss_sum_mb"`
	CPUPercentSum     float64 `json:"cpu_percent_sum"`
	CPUAvailable      bool    `json:"cpu_available"`
	AgentProcessCount int     `json:"agent_process_count"`
}

// CoordinatedWorkClassUsage is a privacy-safe aggregate for one fixed work
// class. Lease roots and process identities are used only while reducing the
// native process inventory and are never projected or persisted.
type CoordinatedWorkClassUsage struct {
	Class        WorkClass `json:"class"`
	LeaseCount   int       `json:"lease_count"`
	ProcessCount int       `json:"process_count"`
	RSSSumMB     float64   `json:"rss_sum_mb"`
	CPUPercent   float64   `json:"cpu_percent"`
	CPUAvailable bool      `json:"cpu_available"`
}

// CoordinatedWorkSnapshot attributes active pressure-work lease descendants
// inside the process inventory the sampler already owns. It is diagnostic
// evidence only: admission and relief policy deliberately ignore this bucket.
type CoordinatedWorkSnapshot struct {
	Available              bool                        `json:"available"`
	Fresh                  bool                        `json:"fresh"`
	CapturedAt             time.Time                   `json:"captured_at,omitempty,omitzero"`
	InventoryAgeSeconds    float64                     `json:"inventory_age_seconds"`
	LeaseCount             int                         `json:"lease_count"`
	AttributedLeaseCount   int                         `json:"attributed_lease_count"`
	UnattributedLeaseCount int                         `json:"unattributed_lease_count"`
	ProcessCount           int                         `json:"process_count"`
	RSSSumMB               float64                     `json:"rss_sum_mb"`
	CPUPercent             float64                     `json:"cpu_percent"`
	CPUAvailable           bool                        `json:"cpu_available"`
	ByClass                []CoordinatedWorkClassUsage `json:"by_class"`
	Error                  string                      `json:"error,omitempty"`
}

// MemoryMomentum is a bounded trend label derived from recent resident free
// memory samples. It is diagnostic only and never raises pressure or grants
// action authority.
type MemoryMomentum string

const (
	MemoryMomentumUnknown      MemoryMomentum = "unknown"
	MemoryMomentumSteady       MemoryMomentum = "steady"
	MemoryMomentumDeclining    MemoryMomentum = "declining"
	MemoryMomentumRapidDecline MemoryMomentum = "rapid_decline"
	MemoryMomentumRecovering   MemoryMomentum = "recovering"
)

// Snapshot is the bounded public and telemetry projection for one sample.
type Snapshot struct {
	SchemaVersion              int                     `json:"schema_version"`
	Timestamp                  time.Time               `json:"timestamp"`
	Level                      Level                   `json:"level"`
	Reasons                    []string                `json:"reasons,omitempty"`
	FreePercent                int                     `json:"free_percent"`
	PhysicalMemoryMB           float64                 `json:"physical_memory_mb"`
	SwapUsedMB                 float64                 `json:"swap_used_mb"`
	LogicalCPUCount            int                     `json:"logical_cpu_count"`
	HostCPUPercent             float64                 `json:"host_cpu_percent"`
	HostCPUAvailable           bool                    `json:"host_cpu_available"`
	HostCPUSource              string                  `json:"host_cpu_source,omitempty"`
	HostCPUSampleWindowMS      float64                 `json:"host_cpu_sample_window_ms,omitempty"`
	HostCPULivePercent         float64                 `json:"host_cpu_live_percent,omitempty"`
	HostCPULiveWindowMS        float64                 `json:"host_cpu_live_window_ms,omitempty"`
	HostCPURollingPercent      float64                 `json:"host_cpu_rolling_percent,omitempty"`
	HostCPURollingWindowMS     float64                 `json:"host_cpu_rolling_window_ms,omitempty"`
	HostCPURollingAvailable    bool                    `json:"host_cpu_rolling_available"`
	HostCPUError               string                  `json:"host_cpu_error,omitempty"`
	ThermalState               ThermalState            `json:"thermal_state,omitempty"`
	ThermalAvailable           bool                    `json:"thermal_available,omitempty"`
	LowPowerMode               bool                    `json:"low_power_mode,omitempty"`
	LowPowerModeAvailable      bool                    `json:"low_power_mode_available,omitempty"`
	PowerThermalSource         string                  `json:"power_thermal_source,omitempty"`
	PowerThermalError          string                  `json:"power_thermal_error,omitempty"`
	AgentCPUPercent            float64                 `json:"agent_cpu_percent"`
	AgentCPUAvailable          bool                    `json:"agent_cpu_available"`
	CoordinatedWork            CoordinatedWorkSnapshot `json:"coordinated_work"`
	ProcessCount               int                     `json:"process_count"`
	ProcessRSSSumMB            float64                 `json:"process_rss_sum_mb"`
	AgentTreeCount             int                     `json:"agent_tree_count"`
	AgentRSSSumMB              float64                 `json:"agent_rss_sum_mb"`
	MemoryMomentum             MemoryMomentum          `json:"memory_momentum"`
	FreePercentSlopePerMinute  float64                 `json:"free_percent_slope_per_minute,omitempty"`
	MinutesToMemoryRed         *float64                `json:"minutes_to_memory_red,omitempty"`
	MemoryMomentumSampleCount  int                     `json:"memory_momentum_sample_count,omitempty"`
	ProcessInventoryAvailable  bool                    `json:"process_inventory_available"`
	ProcessInventoryFresh      bool                    `json:"process_inventory_fresh"`
	ProcessInventoryCapturedAt time.Time               `json:"process_inventory_captured_at,omitempty,omitzero"`
	ProcessInventoryAgeSeconds float64                 `json:"process_inventory_age_seconds,omitempty"`
	ProcessInventorySource     string                  `json:"process_inventory_source,omitempty"`
	ProcessInventoryError      string                  `json:"process_inventory_error,omitempty"`
	GuardPID                   int                     `json:"guard_pid"`
	GuardBinarySHA256          string                  `json:"guard_binary_sha256,omitempty"`
	GuardRSSMB                 float64                 `json:"guard_rss_mb"`
	GuardPeakRSSMB             float64                 `json:"guard_peak_rss_mb,omitempty"`
	GuardCPUPercent            float64                 `json:"guard_cpu_percent"`
	GuardRole                  string                  `json:"guard_role"`
	GuardBudgetApplicable      bool                    `json:"guard_budget_applicable"`
	GuardBaselineProven        bool                    `json:"guard_baseline_proven"`
	MonitorSamples             int                     `json:"monitor_samples,omitempty"`
	NormalMonitorSamples       int                     `json:"normal_monitor_samples,omitempty"`
	GuardRSSMaxMB              float64                 `json:"guard_rss_max_mb,omitempty"`
	GuardCPUAvgPercent         float64                 `json:"guard_cpu_avg_percent,omitempty"`
	SampleDurationMS           float64                 `json:"sample_duration_ms"`
	SamplePhaseMS              map[string]float64      `json:"sample_phase_ms,omitempty"`
	SampleDurationAvgMS        float64                 `json:"sample_duration_avg_ms,omitempty"`
	SampleDurationP95MS        float64                 `json:"sample_duration_p95_ms,omitempty"`
	SampleDurationMaxMS        float64                 `json:"sample_duration_max_ms,omitempty"`
	SampleCPUTimeMS            float64                 `json:"sample_cpu_time_ms"`
	SampleCPUTimeAvgMS         float64                 `json:"sample_cpu_time_avg_ms,omitempty"`
	SampleCPUTimeP95MS         float64                 `json:"sample_cpu_time_p95_ms,omitempty"`
	SampleCPUTimeMaxMS         float64                 `json:"sample_cpu_time_max_ms,omitempty"`
	ObservedIntervalSeconds    float64                 `json:"observed_interval_seconds,omitempty"`
	GuardCPUDutyPercent        float64                 `json:"guard_cpu_duty_percent,omitempty"`
	GuardIdleCPUDutyPercent    float64                 `json:"guard_idle_cpu_duty_percent,omitempty"`
	TelemetryBytesToday        int64                   `json:"telemetry_bytes_today,omitempty"`
	TelemetryProjectedBytesDay int64                   `json:"telemetry_projected_bytes_per_day,omitempty"`
	GuardBudgetOK              bool                    `json:"guard_budget_ok"`
	GuardBudgetReasons         []string                `json:"guard_budget_reasons,omitempty"`
	TopHostConsumers           []HostConsumer          `json:"top_host_consumers"`
	TopAgentTrees              []AgentTree             `json:"top_agent_trees"`
	PolicySource               string                  `json:"policy_source,omitempty"`
	ConsecutiveSamples         int                     `json:"consecutive_samples,omitempty"`
	MemoryConsecutiveSamples   int                     `json:"memory_consecutive_samples,omitempty"`
	ResourceCleanupError       string                  `json:"resource_cleanup_error,omitempty"`
	GuardSampleErrors          int                     `json:"guard_sample_errors,omitempty"`
	ResourceCleanupFailures    int                     `json:"resource_cleanup_failures,omitempty"`
	ResourceCleanupStatus      string                  `json:"resource_cleanup_status,omitempty"`
	ResourceCleanupExecutedAt  time.Time               `json:"resource_cleanup_control_executed_at,omitempty,omitzero"`
	ResourceCleanupDurationMS  float64                 `json:"resource_cleanup_control_duration_ms,omitempty"`
	ResourceCleanupMaxRSSMB    float64                 `json:"resource_cleanup_control_max_rss_mb,omitempty"`
	ResidentStarts24h          int                     `json:"resident_starts_24h,omitempty"`
	Storage                    StorageSnapshot         `json:"storage"`
	DiskWrite                  *DiskWriteSummary       `json:"disk_write,omitempty"`
	DiskWriteWriters           []DiskWriter            `json:"disk_write_writers,omitempty"`

	// Internal cumulative counters let the resident account for CPU used by
	// persistence, pruning, and action bookkeeping between samples. They are
	// intentionally excluded from the durable/public projection.
	processCPUTotalMS float64
	intervalCPUTimeMS float64
}

// MarshalJSON keeps the zero value safe at every public serialization
// boundary. Test doubles, old persisted snapshots, and partial callers may
// omit momentum, but the versioned output contract intentionally has no empty
// enum member.
func (snapshot Snapshot) MarshalJSON() ([]byte, error) {
	type snapshotJSON Snapshot
	if snapshot.MemoryMomentum == "" {
		snapshot.MemoryMomentum = MemoryMomentumUnknown
	}
	if snapshot.TopHostConsumers == nil {
		snapshot.TopHostConsumers = []HostConsumer{}
	}
	if snapshot.CoordinatedWork.ByClass == nil {
		snapshot.CoordinatedWork.ByClass = []CoordinatedWorkClassUsage{}
	}
	if snapshot.TopAgentTrees == nil {
		snapshot.TopAgentTrees = []AgentTree{}
	}
	return json.Marshal(snapshotJSON(snapshot))
}

// Admission is returned to launchers. Sampling failures are reported but fail
// open; a valid red/critical sample follows policy and can fail closed.
type Admission struct {
	Allowed    bool                        `json:"allowed"`
	Level      Level                       `json:"level"`
	Source     string                      `json:"source"`
	Dimension  string                      `json:"dimension,omitempty"`
	Reasons    []string                    `json:"reasons,omitempty"`
	Warning    string                      `json:"warning,omitempty"`
	Snapshot   *Snapshot                   `json:"snapshot,omitempty"`
	WorkQueue  *WorkQueueAdmissionEvidence `json:"work_queue,omitempty"`
	Controller *ControllerDecision         `json:"controller,omitempty"`
}

// WorkQueueAdmissionEvidence explains the bounded, privacy-safe queue signal
// used only at the canonical agent launch boundary.
type WorkQueueAdmissionEvidence struct {
	Capacity               int   `json:"capacity"`
	Used                   int   `json:"used"`
	QueueDepth             int   `json:"queue_depth"`
	OldestWaitMilliseconds int64 `json:"oldest_wait_ms"`
	QueueDepthBlock        int   `json:"queue_depth_block"`
	OldestWaitBlockMS      int64 `json:"oldest_wait_block_ms"`
	WouldBlock             bool  `json:"would_block"`
	Enforced               bool  `json:"enforced"`
}

func AdmissionForSnapshot(snapshot Snapshot, policy Policy, source string) Admission {
	snapshot = Evaluate(snapshot, policy)
	controller := ClassifyController(snapshot, policy)
	allowed := !policy.EnforceAdmission || !snapshot.Level.AtLeast(policy.BlockNewAt)
	dimension := ""
	if snapshot.Level.AtLeast(LevelWarning) {
		memory := EvaluateMemoryPressure(snapshot, policy)
		if memory.Level.AtLeast(LevelWarning) {
			dimension = "memory"
		} else {
			dimension = "cpu"
		}
	}
	admission := Admission{
		Allowed: allowed, Level: snapshot.Level, Source: source, Dimension: dimension,
		Reasons: snapshot.Reasons, Snapshot: &snapshot, Controller: &controller,
	}
	if policy.EnforceAdmission && controller.BlockWork && !admission.Level.AtLeast(policy.BlockNewAt) {
		admission.Allowed = false
		admission.Level = maxLevel(admission.Level, LevelRed)
		admission.Dimension = controller.Dimension
		admission.Reasons = append(admission.Reasons, controller.Reasons...)
	}
	if !snapshot.HostCPUAvailable && strings.TrimSpace(snapshot.HostCPUError) != "" {
		// Memory and swap evidence remain enforceable, but a partial CPU probe
		// failure must never look like a fully healthy sample to launchers.
		admission.Warning = "host CPU sampling unavailable; CPU admission failed open while memory admission remained active: " + snapshot.HostCPUError
	}
	return admission
}

// AdmissionForWorkSnapshot keeps transient CPU-red evidence advisory for
// heavy work. Memory and thermal safety floors still fail closed immediately.
func AdmissionForWorkSnapshot(snapshot Snapshot, policy Policy, source string) Admission {
	admission := AdmissionForSnapshot(snapshot, policy, source)
	if !policy.EnforceAdmission || admission.Allowed || admission.Snapshot == nil || !admission.Level.AtLeast(policy.BlockNewAt) || admission.Dimension != "cpu" {
		return admission
	}
	memory := EvaluateMemoryPressure(*admission.Snapshot, policy)
	if !memory.Level.AtLeast(policy.BlockNewAt) && admission.Controller != nil && !admission.Controller.BlockWork {
		admission.Allowed = true
	}
	return admission
}

// AdmissionForAgentLaunchSnapshot gates new/resume agent hosts on memory
// pressure only. Full Evaluate (including CPU) stays on the snapshot for
// operators; CPU-red continues to throttle heavy work via work admission.
func AdmissionForAgentLaunchSnapshot(snapshot Snapshot, policy Policy, source string) Admission {
	full := Evaluate(snapshot, policy)
	memory := EvaluateMemoryPressure(snapshot, policy)
	controller := ClassifyController(full, policy)
	allowed := !policy.EnforceAdmission || !memory.Level.AtLeast(policy.BlockNewAt)
	admission := Admission{
		Allowed:  allowed,
		Level:    memory.Level,
		Source:   joinAdmissionSource(source, "memory-gated-launch"),
		Reasons:  append([]string(nil), memory.Reasons...),
		Snapshot: &full, Controller: &controller,
	}
	if policy.EnforceAdmission && controller.BlockAgentLaunch {
		admission.Allowed = false
		// Preserve the actual memory band for a memory-red floor. Only a
		// critical thermal or critical-memory floor should be projected as
		// critical; otherwise the controller would inflate a red diagnosis.
		if controller.ThermalState == ThermalStateCritical || memory.Level.AtLeast(LevelCritical) {
			admission.Level = maxLevel(admission.Level, LevelCritical)
		}
		admission.Dimension = controller.Dimension
		admission.Reasons = append(admission.Reasons, controller.Reasons...)
	}
	if !full.HostCPUAvailable && strings.TrimSpace(full.HostCPUError) != "" {
		admission.Warning = joinAdmissionWarning(admission.Warning,
			"host CPU sampling unavailable; agent launch uses memory gates only: "+full.HostCPUError)
	}
	if allowed && full.Level.AtLeast(LevelWarning) && levelRank(full.Level) > levelRank(memory.Level) {
		admission.Warning = joinAdmissionWarning(admission.Warning,
			"CPU pressure observed; agent launch allowed (memory gates only); heavy work remains CPU-gated")
	}
	if !allowed {
		admission.Reasons = append([]string(nil), memory.Reasons...)
	}
	return admission
}

// EvaluateStorage applies entry thresholds plus release hysteresis from the
// last resident level. A missing sample fails open visibly and never invents
// deletion authority.
func EvaluateStorage(sample StorageSnapshot, policy StoragePolicy, previous Level) StorageSnapshot {
	sample.Level = LevelNormal
	sample.InstantaneousLevel = LevelNormal
	sample.HysteresisActive = false
	sample.ReleaseBytes = 0
	sample.Reasons = nil
	if !policy.Enabled || !sample.Available {
		return sample
	}
	free := sample.AvailableBytes
	switch {
	case free < policy.CriticalFreeBytes:
		sample.InstantaneousLevel = LevelCritical
	case free < policy.RedFreeBytes:
		sample.InstantaneousLevel = LevelRed
	case free < policy.WarningFreeBytes:
		sample.InstantaneousLevel = LevelWarning
	}
	sample.Level = sample.InstantaneousLevel
	// A previously entered band releases only above its own recovery threshold.
	// More severe current evidence always wins over hysteresis.
	switch previous {
	case LevelCritical:
		if free < policy.CriticalReleaseBytes {
			sample.Level = maxLevel(sample.Level, LevelCritical)
			sample.ReleaseBytes = policy.CriticalReleaseBytes
		}
	case LevelRed:
		if free < policy.RedReleaseBytes {
			sample.Level = maxLevel(sample.Level, LevelRed)
			sample.ReleaseBytes = policy.RedReleaseBytes
		}
	case LevelWarning:
		if free < policy.WarningReleaseBytes {
			sample.Level = maxLevel(sample.Level, LevelWarning)
			sample.ReleaseBytes = policy.WarningReleaseBytes
		}
	}
	sample.HysteresisActive = levelRank(sample.Level) > levelRank(sample.InstantaneousLevel)
	if sample.Level != LevelNormal {
		if sample.HysteresisActive {
			sample.Reasons = []string{fmt.Sprintf("storage available %.1f GiB is instantaneously %s; effective %s remains latched until %.1f GiB", float64(free)/(1<<30), sample.InstantaneousLevel, sample.Level, float64(sample.ReleaseBytes)/(1<<30))}
		} else {
			sample.Reasons = []string{fmt.Sprintf("storage available %.1f GiB is %s", float64(free)/(1<<30), sample.Level)}
		}
	}
	return sample
}

// Action records an attempted pressure-relief action. Signal is always
// graceful SIGTERM in v1; escalation to SIGKILL is intentionally unsupported.
type Action struct {
	SchemaVersion            int           `json:"schema_version"`
	Timestamp                time.Time     `json:"timestamp"`
	Kind                     string        `json:"kind"`
	Level                    Level         `json:"level"`
	RootPID                  int           `json:"root_pid,omitempty"`
	Agent                    string        `json:"agent,omitempty"`
	SessionID                string        `json:"session_id,omitempty"`
	RSSSumMB                 float64       `json:"rss_sum_mb,omitempty"`
	SemanticState            SemanticState `json:"semantic_state,omitempty"`
	ReliefScope              string        `json:"relief_scope,omitempty"`
	PrimaryHostExecutable    string        `json:"primary_host_executable,omitempty"`
	PrimaryHostRSSSumMB      float64       `json:"primary_host_rss_sum_mb,omitempty"`
	AgentRSSSharePercent     float64       `json:"agent_rss_share_percent,omitempty"`
	Signal                   string        `json:"signal,omitempty"`
	Result                   string        `json:"result"`
	Reason                   string        `json:"reason,omitempty"`
	RevalidatedLevel         Level         `json:"revalidated_level,omitempty"`
	RevalidatedCPUPercent    float64       `json:"revalidated_cpu_percent,omitempty"`
	RevalidatedRSSSumMB      float64       `json:"revalidated_rss_sum_mb,omitempty"`
	RevalidatedSemanticState SemanticState `json:"revalidated_semantic_state,omitempty"`
	RevalidationDurationMS   float64       `json:"revalidation_sample_duration_ms,omitempty"`
	RevalidationCPUTimeMS    float64       `json:"revalidation_sample_cpu_time_ms,omitempty"`
	RevalidationGuardRSSMB   float64       `json:"revalidation_guard_rss_mb,omitempty"`
	RevalidationPeakRSSMB    float64       `json:"revalidation_guard_peak_rss_mb,omitempty"`
}

// Evaluate applies policy without mutating the sample.
func Evaluate(snapshot Snapshot, policy Policy) Snapshot {
	return evaluate(snapshot, policy, true)
}

// EvaluateMemoryPressure applies free/agent/tree/swap gates only. Agent launches
// use this so transient CPU-red from builds does not refuse chat hosts while
// memory (the kernel-panic risk) still hard-blocks at BlockNewAt.
func EvaluateMemoryPressure(snapshot Snapshot, policy Policy) Snapshot {
	return evaluate(snapshot, policy, false)
}

func evaluate(snapshot Snapshot, policy Policy, includeCPU bool) Snapshot {
	level := LevelNormal
	type breach struct {
		level  Level
		reason string
	}
	breaches := make([]breach, 0, 8)
	add := func(candidate Level, format string, args ...any) {
		level = maxLevel(level, candidate)
		breaches = append(breaches, breach{level: candidate, reason: fmt.Sprintf(format, args...)})
	}

	free := float64(snapshot.FreePercent)
	switch {
	case free <= policy.Thresholds.FreeCriticalPercent:
		add(LevelCritical, "host free memory %d%% <= critical %.0f%%", snapshot.FreePercent, policy.Thresholds.FreeCriticalPercent)
	case free <= policy.Thresholds.FreeRedPercent:
		add(LevelRed, "host free memory %d%% <= red %.0f%%", snapshot.FreePercent, policy.Thresholds.FreeRedPercent)
	case free <= policy.Thresholds.FreeWarningPercent:
		add(LevelWarning, "host free memory %d%% <= warning %.0f%%", snapshot.FreePercent, policy.Thresholds.FreeWarningPercent)
	}

	switch {
	case snapshot.AgentRSSSumMB >= policy.Thresholds.AgentTotalCriticalMB:
		add(LevelCritical, "agent RSS sum %.0f MB >= critical %.0f MB", snapshot.AgentRSSSumMB, policy.Thresholds.AgentTotalCriticalMB)
	case snapshot.AgentRSSSumMB >= policy.Thresholds.AgentTotalRedMB:
		add(LevelRed, "agent RSS sum %.0f MB >= red %.0f MB", snapshot.AgentRSSSumMB, policy.Thresholds.AgentTotalRedMB)
	case snapshot.AgentRSSSumMB >= policy.Thresholds.AgentTotalWarningMB:
		add(LevelWarning, "agent RSS sum %.0f MB >= warning %.0f MB", snapshot.AgentRSSSumMB, policy.Thresholds.AgentTotalWarningMB)
	}

	if len(snapshot.TopAgentTrees) > 0 {
		top := snapshot.TopAgentTrees[0]
		switch {
		case top.RSSSumMB >= policy.Thresholds.TreeCriticalMB:
			add(LevelCritical, "%s tree %d RSS sum %.0f MB >= critical %.0f MB", top.Agent, top.RootPID, top.RSSSumMB, policy.Thresholds.TreeCriticalMB)
		case top.RSSSumMB >= policy.Thresholds.TreeRedMB:
			add(LevelRed, "%s tree %d RSS sum %.0f MB >= red %.0f MB", top.Agent, top.RootPID, top.RSSSumMB, policy.Thresholds.TreeRedMB)
		case top.RSSSumMB >= policy.Thresholds.TreeWarningMB:
			add(LevelWarning, "%s tree %d RSS sum %.0f MB >= warning %.0f MB", top.Agent, top.RootPID, top.RSSSumMB, policy.Thresholds.TreeWarningMB)
		}
	}

	// macOS swap use is intentionally sticky after pressure subsides. Treat it
	// as corroboration for a current free-memory or agent-tree breach, not as a
	// standalone active-pressure signal. It can escalate that live signal by at
	// most one rung, preventing historical swap from causing chronic polling,
	// launch blocks, or automatic shedding.
	// CPU-only pressure deliberately does not corroborate sticky swap. This
	// prevents a busy build on a host with historical swap from escalating to
	// critical and activating memory-oriented automatic tree shedding.
	liveMemoryLevel := level
	switch {
	case snapshot.SwapUsedMB >= policy.Thresholds.SwapCriticalMB && liveMemoryLevel.AtLeast(LevelRed):
		add(LevelCritical, "swap %.0f MB >= critical %.0f MB with live %s pressure", snapshot.SwapUsedMB, policy.Thresholds.SwapCriticalMB, liveMemoryLevel)
	case snapshot.SwapUsedMB >= policy.Thresholds.SwapRedMB && liveMemoryLevel.AtLeast(LevelWarning):
		add(LevelRed, "swap %.0f MB >= red %.0f MB with live %s pressure", snapshot.SwapUsedMB, policy.Thresholds.SwapRedMB, liveMemoryLevel)
	case snapshot.SwapUsedMB >= policy.Thresholds.SwapWarningMB && liveMemoryLevel.AtLeast(LevelWarning):
		add(LevelWarning, "swap %.0f MB >= warning %.0f MB with live %s pressure", snapshot.SwapUsedMB, policy.Thresholds.SwapWarningMB, liveMemoryLevel)
	}

	// Host CPU is normalized across logical cores. It can warn or block heavy
	// work admission, but it never reaches critical on its own: automatic
	// relief is intentionally reserved for corroborated memory pressure.
	if includeCPU {
		switch {
		case (snapshot.HostCPUAvailable || snapshot.HostCPUPercent > 0) && snapshot.HostCPUPercent >= policy.Thresholds.HostCPURedPercent:
			add(LevelRed, "host CPU %.1f%% >= red %.1f%%", snapshot.HostCPUPercent, policy.Thresholds.HostCPURedPercent)
		case (snapshot.HostCPUAvailable || snapshot.HostCPUPercent > 0) && snapshot.HostCPUPercent >= policy.Thresholds.HostCPUWarningPercent:
			add(LevelWarning, "host CPU %.1f%% >= warning %.1f%%", snapshot.HostCPUPercent, policy.Thresholds.HostCPUWarningPercent)
		}
	}

	sort.SliceStable(breaches, func(i, j int) bool { return levelRank(breaches[i].level) > levelRank(breaches[j].level) })
	reasons := make([]string, 0, len(breaches))
	for _, item := range breaches {
		reasons = append(reasons, item.reason)
	}
	snapshot.Level = level
	snapshot.Reasons = reasons
	snapshot.GuardBudgetOK = true
	snapshot.GuardBudgetReasons = nil
	if !snapshot.GuardBudgetApplicable {
		return snapshot
	}
	// Budget on current / rolling self-RSS only. Darwin ru_maxrss (GuardPeakRSSMB)
	// is process-lifetime and never decays after a one-time startup spike, so
	// folding it into the gate falsely fails a healthy resident for its whole life.
	rssMetric := snapshot.GuardRSSMB
	cpuMetric := snapshot.GuardCPUPercent
	durationMetric := snapshot.SampleDurationMS
	cpuTimeMetric := snapshot.SampleCPUTimeMS
	useRollingP95 := false
	intervalSeconds := float64(policy.SampleIntervalSeconds)
	if snapshot.ObservedIntervalSeconds > 0 {
		intervalSeconds = snapshot.ObservedIntervalSeconds
	}
	if intervalSeconds > 0 && snapshot.SampleCPUTimeMS > 0 {
		cpuMetric = snapshot.SampleCPUTimeMS / (intervalSeconds * 10)
	}
	if snapshot.MonitorSamples >= policy.SustainSamples {
		if snapshot.GuardRSSMaxMB > 0 {
			rssMetric = snapshot.GuardRSSMaxMB
		}
		// Nearest-rank p95 equals the isolated maximum until 20 samples.
		// Using it after only SustainSamples (4) makes a login/boot inventory
		// sample fail the resident for ~40 minutes — the same class of latch
		// as lifetime ru_maxrss. Budget the current sample until p95 can
		// drop one outlier; then use the rolling p95.
		if snapshot.MonitorSamples >= p95SingleOutlierExclusionSamples {
			useRollingP95 = true
			if snapshot.SampleDurationP95MS > 0 {
				durationMetric = snapshot.SampleDurationP95MS
			}
			if snapshot.SampleCPUTimeP95MS > 0 {
				cpuTimeMetric = snapshot.SampleCPUTimeP95MS
			}
		}
	}
	if rssMetric > policy.ResourceBudgets.MaxSelfRSSMB {
		snapshot.GuardBudgetOK = false
		snapshot.GuardBudgetReasons = append(snapshot.GuardBudgetReasons,
			fmt.Sprintf("self RSS %.1f MB > budget %.1f MB", rssMetric, policy.ResourceBudgets.MaxSelfRSSMB))
	}
	if snapshot.Level == LevelNormal {
		if snapshot.NormalMonitorSamples >= policy.SustainSamples {
			cpuMetric = snapshot.GuardIdleCPUDutyPercent
		}
		if cpuMetric > policy.ResourceBudgets.MaxIdleCPUPercent {
			snapshot.GuardBudgetOK = false
			snapshot.GuardBudgetReasons = append(snapshot.GuardBudgetReasons,
				fmt.Sprintf("normal-state self CPU duty %.3f%% > idle budget %.3f%%", cpuMetric, policy.ResourceBudgets.MaxIdleCPUPercent))
		}
	} else if cpuMetric > policy.ResourceBudgets.MaxPressureCPUPercent {
		snapshot.GuardBudgetOK = false
		snapshot.GuardBudgetReasons = append(snapshot.GuardBudgetReasons,
			fmt.Sprintf("pressure-state self CPU duty %.3f%% > pressure budget %.3f%%", cpuMetric, policy.ResourceBudgets.MaxPressureCPUPercent))
	}
	durationLimit := policy.ResourceBudgets.MaxSampleDurationMS
	if !useRollingP95 {
		// Wall clock includes host scheduling and lock waits. The 2s
		// ceiling is a mature p95 tightness gate. Before p95 can drop
		// one max, only treat a clear stall as a duration failure.
		durationLimit = max(durationLimit, 10_000)
	}
	if durationMetric > durationLimit {
		snapshot.GuardBudgetOK = false
		if useRollingP95 {
			snapshot.GuardBudgetReasons = append(snapshot.GuardBudgetReasons,
				fmt.Sprintf("rolling sample p95 %.1f ms > budget %.1f ms", durationMetric, policy.ResourceBudgets.MaxSampleDurationMS))
		} else {
			snapshot.GuardBudgetReasons = append(snapshot.GuardBudgetReasons,
				fmt.Sprintf("current sample duration %.1f ms > hang budget %.1f ms", durationMetric, durationLimit))
		}
	}
	if cpuTimeMetric > policy.ResourceBudgets.MaxSampleCPUTimeMS {
		snapshot.GuardBudgetOK = false
		if useRollingP95 {
			snapshot.GuardBudgetReasons = append(snapshot.GuardBudgetReasons,
				fmt.Sprintf("rolling sample CPU p95 %.1f ms > budget %.1f ms", cpuTimeMetric, policy.ResourceBudgets.MaxSampleCPUTimeMS))
		} else {
			snapshot.GuardBudgetReasons = append(snapshot.GuardBudgetReasons,
				fmt.Sprintf("current sample CPU %.1f ms > budget %.1f ms", cpuTimeMetric, policy.ResourceBudgets.MaxSampleCPUTimeMS))
		}
	}
	telemetryMetric := snapshot.TelemetryBytesToday
	if snapshot.TelemetryProjectedBytesDay > telemetryMetric {
		telemetryMetric = snapshot.TelemetryProjectedBytesDay
	}
	if telemetryMetric > policy.ResourceBudgets.MaxTelemetryBytesDay {
		snapshot.GuardBudgetOK = false
		snapshot.GuardBudgetReasons = append(snapshot.GuardBudgetReasons,
			fmt.Sprintf("telemetry projection %d bytes > daily budget %d", telemetryMetric, policy.ResourceBudgets.MaxTelemetryBytesDay))
	}
	return snapshot
}

func (snapshot Snapshot) Summary() string {
	top := "none"
	if len(snapshot.TopAgentTrees) > 0 {
		tree := snapshot.TopAgentTrees[0]
		top = fmt.Sprintf("%s:%d %.0fMB", tree.Agent, tree.RootPID, tree.RSSSumMB)
	}
	selfLabel := "self"
	switch snapshot.GuardRole {
	case "resident":
		selfLabel = "resident_self"
	case "operator":
		selfLabel = "operator_self"
	}
	parts := []string{
		fmt.Sprintf("level=%s", snapshot.Level),
		fmt.Sprintf("free=%d%%", snapshot.FreePercent),
		fmt.Sprintf("swap=%.0fMB", snapshot.SwapUsedMB),
		fmt.Sprintf("cpu=%.1f%%", snapshot.HostCPUPercent),
		fmt.Sprintf("agents=%d/%.0fMB", snapshot.AgentTreeCount, snapshot.AgentRSSSumMB),
		"top=" + top,
		fmt.Sprintf("%s=%.1fMB/%.2f%%", selfLabel, snapshot.GuardRSSMB, snapshot.GuardCPUPercent),
		fmt.Sprintf("sample=%.1fms", snapshot.SampleDurationMS),
		fmt.Sprintf("storage=%s/%.1fGiB", snapshot.Storage.Level, float64(snapshot.Storage.AvailableBytes)/(1<<30)),
	}
	if snapshot.CoordinatedWork.Available {
		parts = append(parts, fmt.Sprintf("work=%d/%d/%.1f%%", snapshot.CoordinatedWork.LeaseCount, snapshot.CoordinatedWork.ProcessCount, snapshot.CoordinatedWork.CPUPercent))
	}
	if len(snapshot.Reasons) > 0 {
		parts = append(parts, "reason="+snapshot.Reasons[0])
	}
	return strings.Join(parts, " ")
}
