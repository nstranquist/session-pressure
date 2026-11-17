package sessionpressure

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"time"

	"github.com/nstranquist/session-pressure/internal/filelock"
)

const (
	DataDirEnv                 = "NDEV_SESSION_PRESSURE_HOME"
	DataDirEnvAlias            = "SESSION_PRESSURE_HOME"
	policyMutationLockBaseName = "policy-mutation"
)

// AcquirePolicyMutationLock serializes read-modify-write policy transactions
// across processes. Callers must acquire it before reloading policy and retain
// it until the resident lifecycle mutation has either converged or failed.
func AcquirePolicyMutationLock(ctx context.Context, dir string, timeout time.Duration) (func(), error) {
	if strings.TrimSpace(dir) == "" {
		return nil, errors.New("session pressure data directory is required for policy mutation")
	}
	unlock, err := filelock.AcquireContext(ctx, filepath.Join(dir, policyMutationLockBaseName), timeout)
	if err != nil {
		return nil, fmt.Errorf("acquire policy mutation lock: %w", err)
	}
	return unlock, nil
}

type Thresholds struct {
	HostCPUWarningPercent float64 `json:"host_cpu_warning_percent"`
	HostCPURedPercent     float64 `json:"host_cpu_red_percent"`
	FreeWarningPercent    float64 `json:"free_warning_percent"`
	FreeRedPercent        float64 `json:"free_red_percent"`
	FreeCriticalPercent   float64 `json:"free_critical_percent"`
	SwapWarningMB         float64 `json:"swap_warning_mb"`
	SwapRedMB             float64 `json:"swap_red_mb"`
	SwapCriticalMB        float64 `json:"swap_critical_mb"`
	// Swap-out rate is a throughput signal, never a blocking level. Warning
	// reports it; the resident latches thrash after sustained samples at or
	// above Thrash and releases after sustained samples at or below Release.
	SwapOutWarningMBPerSecond float64 `json:"swap_out_warning_mb_per_s"`
	SwapOutThrashMBPerSecond  float64 `json:"swap_out_thrash_mb_per_s"`
	SwapOutReleaseMBPerSecond float64 `json:"swap_out_release_mb_per_s"`
	// SwapLagReleaseMinutes stops sticky swap use from escalating a live
	// warning to red after swap-out has stayed at or under the release rate
	// for this long with swap not growing (see swapLagReleases). 0 is off.
	// SwapLagMaxGrowthMB is the swap growth the window tolerates (default 256).
	SwapLagReleaseMinutes float64 `json:"swap_lag_release_minutes,omitempty"`
	SwapLagMaxGrowthMB    float64 `json:"swap_lag_max_growth_mb,omitempty"`
	TreeWarningMB         float64 `json:"tree_warning_mb"`
	TreeRedMB             float64 `json:"tree_red_mb"`
	TreeCriticalMB        float64 `json:"tree_critical_mb"`
	AgentTotalWarningMB   float64 `json:"agent_total_warning_mb"`
	AgentTotalRedMB       float64 `json:"agent_total_red_mb"`
	AgentTotalCriticalMB  float64 `json:"agent_total_critical_mb"`
	// RSSEscalationFreePercent holds the tree and agent-total RSS rungs at
	// warning while kernel free memory is above it. Summed RSS double counts
	// shared pages, so on a large host it can pass red with 90% free. 0 is
	// off: the RSS rungs escalate on their own (the 16 GiB laptop).
	RSSEscalationFreePercent float64 `json:"rss_escalation_free_percent,omitempty"`
	// Footprint rungs judge phys_footprint sums in place of RSS sums when the
	// sample has footprint readings (macOS). All zero means off: the tree and
	// agent-total rungs then judge RSS with the bands above.
	TreeFootprintWarningMB        float64 `json:"tree_footprint_warning_mb,omitempty"`
	TreeFootprintRedMB            float64 `json:"tree_footprint_red_mb,omitempty"`
	TreeFootprintCriticalMB       float64 `json:"tree_footprint_critical_mb,omitempty"`
	AgentTotalFootprintWarningMB  float64 `json:"agent_total_footprint_warning_mb,omitempty"`
	AgentTotalFootprintRedMB      float64 `json:"agent_total_footprint_red_mb,omitempty"`
	AgentTotalFootprintCriticalMB float64 `json:"agent_total_footprint_critical_mb,omitempty"`
}

// FootprintRungsEnabled reports whether the footprint bands are set.
func (t Thresholds) FootprintRungsEnabled() bool {
	return t.TreeFootprintRedMB > 0 && t.AgentTotalFootprintRedMB > 0
}

// StoragePolicy is independent from memory-oriented admission and shedding.
// The resident observes it cheaply; only class-aware disk-growing work uses
// EnforceAdmission, and no storage level can authorize process termination.
type StoragePolicy struct {
	Enabled                bool  `json:"enabled"`
	EnforceAdmission       bool  `json:"enforce_admission"`
	WarningFreeBytes       int64 `json:"warning_free_bytes"`
	RedFreeBytes           int64 `json:"red_free_bytes"`
	CriticalFreeBytes      int64 `json:"critical_free_bytes"`
	WarningReleaseBytes    int64 `json:"warning_release_bytes"`
	RedReleaseBytes        int64 `json:"red_release_bytes"`
	CriticalReleaseBytes   int64 `json:"critical_release_bytes"`
	TargetFreeBytes        int64 `json:"target_free_bytes"`
	ProviderMinAgeSeconds  int   `json:"provider_min_age_seconds"`
	ReclaimCooldownSeconds int   `json:"reclaim_cooldown_seconds"`
	WorkWaitGraceSeconds   int   `json:"work_wait_grace_seconds"`
	BlockNewAt             Level `json:"block_new_at"`
	// RepositoryOverrides keys canonical repository roots (see
	// StorageRepositoryRoot) to their own admission floor. Absent keys keep
	// the machine-wide bands above.
	RepositoryOverrides map[string]StorageRepositoryOverride `json:"repository_overrides,omitempty"`
}

const DiskWriteProfileQuietAdaptiveV1 = "quiet-adaptive-v1"

// DiskWritePolicy owns observation and notification behavior only. Disk-write
// state is deliberately separate from pressure admission and automatic relief.
type DiskWritePolicy struct {
	Enabled                 bool   `json:"enabled"`
	NotificationsEnabled    bool   `json:"notifications_enabled"`
	SampleIntervalSeconds   int    `json:"sample_interval_seconds"`
	BaselineRetentionDays   int    `json:"baseline_retention_days"`
	Profile                 string `json:"profile"`
	TraceMaxDurationSeconds int    `json:"trace_max_duration_seconds"`
}

func defaultDiskWritePolicy() DiskWritePolicy {
	return DiskWritePolicy{
		Enabled:                 true,
		NotificationsEnabled:    false,
		SampleIntervalSeconds:   15,
		BaselineRetentionDays:   14,
		Profile:                 DiskWriteProfileQuietAdaptiveV1,
		TraceMaxDurationSeconds: 30,
	}
}

const (
	LaunchAdmissionModeSoft       = "soft"
	LaunchAdmissionModeOff        = "off"
	LaunchResumeBehaviorWarn      = "warn"
	LaunchResumeBehaviorBlock     = "block"
	defaultOldestLaunchWaitSecond = 120
)

// LaunchAdmissionPolicy adds soft demand backpressure at the canonical agent
// launch boundary. It never changes weighted work capacity and is evaluated
// independently from storage admission for already-running sessions.
type LaunchAdmissionPolicy struct {
	Mode                   string `json:"mode"`
	QueueDepthBlock        int    `json:"queue_depth_block"`
	OldestWaitBlockSeconds int    `json:"oldest_wait_block_seconds"`
	ResumeBehavior         string `json:"resume_behavior"`
}

func defaultLaunchAdmissionPolicy(capacity int) LaunchAdmissionPolicy {
	return LaunchAdmissionPolicy{
		Mode: LaunchAdmissionModeSoft, QueueDepthBlock: max(1, capacity),
		OldestWaitBlockSeconds: defaultOldestLaunchWaitSecond, ResumeBehavior: LaunchResumeBehaviorWarn,
	}
}

type ResourceBudgets struct {
	MaxSelfRSSMB          float64 `json:"max_self_rss_mb"`
	MaxIdleCPUPercent     float64 `json:"max_idle_cpu_percent"`
	MaxPressureCPUPercent float64 `json:"max_pressure_cpu_percent"`
	MaxSampleDurationMS   float64 `json:"max_sample_duration_ms"`
	MaxSampleCPUTimeMS    float64 `json:"max_sample_cpu_time_ms"`
	MaxTelemetryBytesDay  int64   `json:"max_telemetry_bytes_per_day"`
}

// WorkLimits implements a weighted host-wide semaphore for CPU-heavy work.
// Classes use one shared capacity so unlike operations (for example, an
// emulator and a build) cannot independently saturate the same laptop.
type WorkLimits struct {
	SchedulingPolicy string `json:"scheduling_policy"`
	Capacity         int    `json:"capacity"`
	// WarningCapacity is the effective weighted ceiling while host CPU or
	// memory is at warning. It only gates new admissions: existing leases keep
	// their full weight and drain without preemption.
	WarningCapacity int `json:"warning_capacity"`
	// WarningCapacityEnabled is deliberately opt-in. Warning pressure is useful
	// for an interactive workstation, but making it a default gate turns a
	// recoverable host signal into avoidable agent wait. Balanced and Throughput
	// leave the normal weighted ceiling in place until true red pressure.
	WarningCapacityEnabled bool `json:"warning_capacity_enabled"`
	// MemoryAdmission selects memory-headroom admission (work_headroom.go):
	// "" or "shadow" records decisions only; "enforce" also holds new work
	// whose predicted peak does not fit headroom while other coordinated work
	// runs, and arms swap-thrash serialization (SwapThrashCapacity). It is its
	// own switch: it acts when the policy is enabled, also when
	// EnforceAdmission is false, so red-level host blocking can stay off while
	// the queue still respects memory. An empty queue never waits on memory
	// and a missing sample fails open.
	MemoryAdmission string `json:"memory_admission,omitempty"`
	// Admission selects what decides whether local work may start.
	// "resource" (the default and the load-time fill since 2026-11-17): measured resource
	// headroom (work_resource_headroom.go) decides, and the weighted unit
	// ceilings (Capacity, WarningCapacity, SwapThrashCapacity) are recorded
	// telemetry, not limits. "weighted" restores the weighted unit ceiling;
	// it exists only to roll back a live host. Nico, 2026-11-17: admission is
	// resource-based, with no human-set count.
	Admission string `json:"admission,omitempty"`
	// AllowOverrideBeyondCapacity lets an explicit operator override (work
	// override) admit its waiter past the weighted Capacity ceiling. It
	// widens only that logical cap: the host-pressure admission gate -
	// red-level pressure in any dimension and the shared admission latch -
	// runs before selection and still blocks the waiter outright. Default
	// false: an override changes queue order only. Turning it on trades
	// the cap protection for operator judgement; sustained oversubscription
	// can hide a misclassified weight that the cap would have exposed, so
	// pair it with weight-classification review.
	AllowOverrideBeyondCapacity bool `json:"allow_override_beyond_capacity"`
	// SwapThrashCapacity is the weighted ceiling for new heavy work while the
	// resident's swap-thrash latch is set under an enforcing profile or with
	// MemoryAdmission = enforce (WorkSwapThrashEnforced). Thrash
	// slows every process super-linearly, so serializing memory-heavy work
	// finishes the same queue sooner. A lone operation is always admitted.
	SwapThrashCapacity int `json:"swap_thrash_capacity"`
	// InteractiveRedLightCapacity bounds the interactive red lane: light work
	// (weight within FastLaneMaxWeight) from an attended agent chat may start
	// under red memory or swap pressure while all coordinated work, this
	// operation included, stays within this weighted ceiling. Zero takes the
	// default (2: two express tests or one express build). Negative disables
	// the lane. Critical pressure and storage pressure are never bypassed.
	InteractiveRedLightCapacity int `json:"interactive_red_light_capacity,omitempty"`
	// InteractiveRedLightMaxPeakRSSMB is the largest measured p90 command peak
	// RSS for a class to use the red lane. Zero takes the default (1024).
	InteractiveRedLightMaxPeakRSSMB int `json:"interactive_red_light_max_peak_rss_mb,omitempty"`
	// SwapThrashExternalReleaseSeconds bounds what an outside thrash can cost
	// agents. Holding new heavy work only helps when leased work is part of the
	// memory demand. When leased work RSS stays under SwapThrashAgentSharePercent
	// of physical memory for this long while the latch holds, the resident
	// marks the episode external and the derate stops deferring until the latch
	// releases. 0 means the default; negative never releases (strict).
	SwapThrashExternalReleaseSeconds int `json:"swap_thrash_external_release_seconds"`
	// SwapThrashAgentSharePercent is the share of physical memory that leased
	// work RSS must reach for a thrash to count as agent-driven. 0 means the
	// default.
	SwapThrashAgentSharePercent int `json:"swap_thrash_agent_share_percent"`
	// IdleParkSeconds releases a lease's weight after its process group has
	// burned under 1% of one core for this long, and re-charges it as soon as
	// the group works again. 0 means the default; negative disables parking.
	IdleParkSeconds    int     `json:"idle_park_seconds"`
	TestWeight         int     `json:"test_weight"`
	BuildWeight        int     `json:"build_weight"`
	ExpressTestWeight  int     `json:"express_test_weight"`
	ExpressBuildWeight int     `json:"express_build_weight"`
	EmulatorWeight     int     `json:"emulator_weight"`
	BrowserWeight      int     `json:"browser_weight"`
	HeavyWeight        int     `json:"heavy_weight"`
	BenchmarkWeight    int     `json:"benchmark_weight"`
	InstallWeight      int     `json:"install_weight"`
	ReclaimWeight      int     `json:"reclaim_weight"`
	CPUBlockSamples    int     `json:"cpu_block_samples"`
	CPUReleaseSamples  int     `json:"cpu_release_samples"`
	CPUReleasePercent  float64 `json:"cpu_release_percent"`
	// The fast lane lets light, short work through the CPU-only admission gate
	// when the weighted ceiling — the instrument that actually governs
	// coordinated work — still has room. It never widens that ceiling, and it
	// never applies to memory, swap, or storage pressure.
	FastLaneEnabled                  bool    `json:"fast_lane_enabled"`
	FastLaneMaxWeight                int     `json:"fast_lane_max_weight"`
	FastLaneMaxRuntimeMS             int64   `json:"fast_lane_max_runtime_ms"`
	FastLaneCoordinatedCPUCeilingPct float64 `json:"fast_lane_coordinated_cpu_ceiling_percent"`
	// FastLaneDefaultsRevision records which shipped default set produced the
	// bounds above. Without it a stale default is indistinguishable from a
	// deliberate operator choice, and a correction can never reach hosts that
	// already persisted the wrong value — which is exactly what happened when the
	// runtime ceiling was corrected from 60s to 120s and every existing policy
	// stayed inert with no supported repair path. A newer binary re-derives bounds
	// it previously shipped; anything an operator edits afterwards sets the
	// revision to fastLaneDefaultsOperatorRevision and is never touched again.
	FastLaneDefaultsRevision int `json:"fast_lane_defaults_revision,omitempty"`
}

const (
	// fastLaneDefaultsRevision increments whenever the shipped fast-lane bounds
	// change. Revision 1 shipped a 60s runtime ceiling derived from the median
	// rather than the p95 and was inert against every real class; revision 2
	// corrects it to 120s.
	fastLaneDefaultsRevision = 2
	// fastLaneDefaultsOperatorRevision marks bounds an operator owns. It is
	// deliberately far above any shipped revision so no future increment can
	// silently reclaim a hand-tuned policy.
	fastLaneDefaultsOperatorRevision = 1_000_000
)

const (
	// minSwapLagReleaseMinutes keeps the lag release longer than a storm lull.
	// At swap >= 8 GB, 128 calm gaps between thrash episodes (2026-11-09 to
	// 2026-11-15, scripts/swap-lag-history) had p50 83 s and p75 313 s.
	minSwapLagReleaseMinutes = 5
	// defaultSwapLagMaxGrowthMB tolerates counter noise, not a new storm.
	defaultSwapLagMaxGrowthMB = 256
)

func defaultWorkLimits(logicalCPUCount int) WorkLimits {
	capacity := max(2, logicalCPUCount-2)
	expressTest := min(defaultExpressTestWeight, capacity)
	expressBuild := min(defaultExpressBuildWeight, capacity)
	testWeight := max(1, (capacity+2)/3)
	buildWeight := max(1, (capacity+2)/2)
	// Validate() requires express weights to be strictly lighter than the
	// matching full class. Two-core CI hosts used to land at 1/1 and 2/2.
	if expressTest >= testWeight {
		testWeight = expressTest + 1
	}
	if expressBuild >= buildWeight {
		buildWeight = expressBuild + 1
	}
	if testWeight > capacity {
		capacity = testWeight
	}
	if buildWeight > capacity {
		capacity = buildWeight
	}
	// Non-exclusive benchmarks leave residual capacity that express work can
	// still fit. Exclusive clean-host mode is the capacity-sized class.
	benchmark := max(1, capacity-expressBuild)
	if benchmark+expressTest > capacity {
		capacity = benchmark + expressTest
	}
	return WorkLimits{
		SchedulingPolicy:       WorkSchedulingPolicy,
		Capacity:               capacity,
		WarningCapacity:        max(1, capacity/2),
		WarningCapacityEnabled: true,
		// Focused/package tests usually compile less than a clean application
		// build. They can share the host with a browser while still reserving
		// meaningful headroom for the OS and resident agent trees.
		TestWeight: testWeight,
		// A strict majority keeps two compiler waves from overlapping while
		// allowing one focused test to share the target 8-unit host capacity.
		BuildWeight:                      buildWeight,
		SwapThrashCapacity:               min(capacity, buildWeight),
		SwapThrashExternalReleaseSeconds: defaultSwapThrashExternalReleaseSeconds,
		SwapThrashAgentSharePercent:      defaultSwapThrashAgentSharePercent,
		IdleParkSeconds:                  defaultIdleParkSeconds,
		ExpressTestWeight:                expressTest,
		ExpressBuildWeight:               expressBuild,
		EmulatorWeight:                   max(1, (capacity*5+7)/8),
		BrowserWeight:                    max(1, (capacity+3)/4),
		HeavyWeight:                      max(1, (capacity*3+3)/4),
		BenchmarkWeight:                  benchmark,
		// Package installs are network/disk-bound; small weight + storage-red admit.
		InstallWeight: 1,
		ReclaimWeight: 1,
		// CPU-only spikes must repeat before bounded-wait work enters the red
		// latch, then recover below warning with the same sustained evidence.
		CPUBlockSamples:   2,
		CPUReleaseSamples: 2,
		CPUReleasePercent: 80,
		// The fast lane ships enabled but deliberately narrow: only the two express
		// classes are light enough for the weight ceiling.
		//
		// The runtime ceiling is calibrated against the measured p95, not the
		// median. A first pass set it to 60 s from the 5.3 s median and the feature
		// was inert — every eligible class sits just above it (express-test p95
		// 76.6 s, express-build 74.3 s), so the gate refused 100% of candidates.
		// 120 s admits both high-volume express classes, which finish well inside
		// the 205 s median hold they were otherwise suffering, while still
		// excluding genuinely long-lived work such as browser sessions (p95 302 s).
		FastLaneEnabled:                  true,
		FastLaneMaxWeight:                max(1, min(expressBuild, capacity/4)),
		FastLaneMaxRuntimeMS:             120_000,
		FastLaneCoordinatedCPUCeilingPct: 50,
		FastLaneDefaultsRevision:         fastLaneDefaultsRevision,
	}
}

// reconcileFastLaneDefaults decides whether persisted fast-lane bounds belong to
// the operator or to a shipped default set, and re-derives the latter.
//
// Three cases, in order:
//
//   - No opinion at all (every field zero): a policy written before the fast lane
//     existed. It inherits the current defaults wholesale.
//   - Bounds carrying a shipped revision older than the current one: values this
//     project chose and has since corrected. Re-derive them. This is the case that
//     was previously unreachable — a 60s ceiling shipped in revision 1 was inert
//     against every real class, and neither reload nor `policy migrate` could
//     repair it because both preserve whatever is already persisted.
//   - Anything else: an operator's own bounds. Never touched, and marked so no
//     future revision bump can reclaim them.
func reconcileFastLaneDefaults(limits, defaults WorkLimits) WorkLimits {
	unopinionated := limits.FastLaneMaxWeight == 0 &&
		limits.FastLaneMaxRuntimeMS == 0 &&
		limits.FastLaneCoordinatedCPUCeilingPct == 0 &&
		!limits.FastLaneEnabled
	if unopinionated {
		limits.FastLaneEnabled = defaults.FastLaneEnabled
		limits.FastLaneMaxWeight = defaults.FastLaneMaxWeight
		limits.FastLaneMaxRuntimeMS = defaults.FastLaneMaxRuntimeMS
		limits.FastLaneCoordinatedCPUCeilingPct = defaults.FastLaneCoordinatedCPUCeilingPct
		limits.FastLaneDefaultsRevision = defaults.FastLaneDefaultsRevision
		return limits
	}
	if limits.FastLaneDefaultsRevision > 0 && limits.FastLaneDefaultsRevision < defaults.FastLaneDefaultsRevision {
		// Enablement is preserved: turning the fast lane off is an operator
		// decision even when the bounds around it were ours.
		enabled := limits.FastLaneEnabled
		limits.FastLaneMaxWeight = defaults.FastLaneMaxWeight
		limits.FastLaneMaxRuntimeMS = defaults.FastLaneMaxRuntimeMS
		limits.FastLaneCoordinatedCPUCeilingPct = defaults.FastLaneCoordinatedCPUCeilingPct
		limits.FastLaneDefaultsRevision = defaults.FastLaneDefaultsRevision
		limits.FastLaneEnabled = enabled
		return limits
	}
	// Fill any individually-missing bound, then mark the set operator-owned so it
	// is never re-derived again.
	if limits.FastLaneMaxWeight == 0 {
		limits.FastLaneMaxWeight = defaults.FastLaneMaxWeight
	}
	if limits.FastLaneMaxRuntimeMS == 0 {
		limits.FastLaneMaxRuntimeMS = defaults.FastLaneMaxRuntimeMS
	}
	if limits.FastLaneCoordinatedCPUCeilingPct == 0 {
		limits.FastLaneCoordinatedCPUCeilingPct = defaults.FastLaneCoordinatedCPUCeilingPct
	}
	if limits.FastLaneDefaultsRevision == 0 {
		limits.FastLaneDefaultsRevision = fastLaneDefaultsOperatorRevision
	}
	return limits
}

func normalizeWorkLimits(limits WorkLimits) WorkLimits {
	if limits.Capacity <= 0 {
		limits = defaultWorkLimits(runtime.NumCPU())
		return limits
	}
	if limits.TestWeight == 0 {
		limits.TestWeight = max(1, (limits.Capacity+2)/3)
	}
	if limits.WarningCapacity == 0 {
		limits.WarningCapacity = max(1, limits.Capacity/2)
	}
	if limits.BuildWeight == 0 {
		limits.BuildWeight = max(1, (limits.Capacity+2)/2)
	}
	if limits.ExpressTestWeight == 0 {
		limits.ExpressTestWeight = min(defaultExpressTestWeight, limits.Capacity)
	}
	if limits.ExpressBuildWeight == 0 {
		limits.ExpressBuildWeight = min(defaultExpressBuildWeight, limits.Capacity)
	}
	if limits.BenchmarkWeight == 0 {
		limits.BenchmarkWeight = max(1, limits.Capacity-limits.ExpressBuildWeight)
	}
	if limits.SchedulingPolicy == "" {
		limits.SchedulingPolicy = WorkSchedulingPolicy
	}
	if limits.CPUBlockSamples == 0 {
		limits.CPUBlockSamples = 2
	}
	if limits.CPUReleaseSamples == 0 {
		limits.CPUReleaseSamples = 2
	}
	if limits.CPUReleasePercent == 0 {
		limits.CPUReleasePercent = 80
	}
	if limits.InstallWeight == 0 {
		limits.InstallWeight = 1
	}
	if limits.ReclaimWeight == 0 {
		limits.ReclaimWeight = 1
	}
	if limits.SwapThrashCapacity == 0 {
		// One full build at a time, or a test beside light express work.
		limits.SwapThrashCapacity = min(limits.Capacity, limits.BuildWeight)
	}
	if limits.SwapThrashExternalReleaseSeconds == 0 {
		limits.SwapThrashExternalReleaseSeconds = defaultSwapThrashExternalReleaseSeconds
	}
	if limits.SwapThrashAgentSharePercent == 0 {
		limits.SwapThrashAgentSharePercent = defaultSwapThrashAgentSharePercent
	}
	if limits.IdleParkSeconds == 0 {
		limits.IdleParkSeconds = defaultIdleParkSeconds
	}
	limits = reconcileFastLaneDefaults(limits, defaultWorkLimits(runtime.NumCPU()))
	return limits
}

// Policy is intentionally small and human-editable. Automatic shedding is
// gated independently from monitoring and admission control.
type Policy struct {
	SchemaVersion int `json:"schema_version"`
	// Profile is the operator-selected work style. It is additive to the
	// existing policy schema so older policy files remain readable.
	Profile                         string `json:"profile,omitempty"`
	Enabled                         bool   `json:"enabled"`
	EnforceAdmission                bool   `json:"enforce_admission"`
	AutoShedCritical                bool   `json:"auto_shed_critical"`
	SampleIntervalSeconds           int    `json:"sample_interval_seconds"`
	PressureSampleIntervalSeconds   int    `json:"pressure_sample_interval_seconds"`
	CriticalSampleIntervalSeconds   int    `json:"critical_sample_interval_seconds"`
	ProcessInventoryIntervalSeconds int    `json:"process_inventory_interval_seconds"`
	HeartbeatSeconds                int    `json:"heartbeat_seconds"`
	// SustainSamples is the minimum live window before rolling resource
	// budgets replace conservative single-sample checks.
	SustainSamples         int     `json:"sustain_samples"`
	CriticalSustainSamples int     `json:"critical_sustain_samples"`
	ActionCooldownSeconds  int     `json:"action_cooldown_seconds"`
	CandidateMinAgeSeconds int     `json:"candidate_min_age_seconds"`
	CandidateMaxCPUPercent float64 `json:"candidate_max_cpu_percent"`
	RetentionDays          int     `json:"retention_days"`
	BlockNewAt             Level   `json:"block_new_at"`
	// MemoryLevels is "calibrated" or "manual" (empty). Calibrated policies
	// take their memory thresholds and guard RSS budget from memory-levels.json
	// on every load (see memory_levels.go); manual policies keep their own.
	MemoryLevels    string                `json:"memory_levels,omitempty"`
	Thresholds      Thresholds            `json:"thresholds"`
	ResourceBudgets ResourceBudgets       `json:"resource_budgets"`
	WorkLimits      WorkLimits            `json:"work_limits"`
	LaunchAdmission LaunchAdmissionPolicy `json:"launch_admission"`
	Storage         StoragePolicy         `json:"storage"`
	DiskWrite       DiskWritePolicy       `json:"disk_write"`
}

func (policy Policy) IntervalSeconds(level Level) int {
	switch level {
	case LevelCritical:
		return policy.CriticalSampleIntervalSeconds
	case LevelWarning, LevelRed:
		return policy.PressureSampleIntervalSeconds
	default:
		return policy.SampleIntervalSeconds
	}
}

// DefaultPolicy takes its memory thresholds from the embedded
// memory_levels.json anchors for physicalMemoryMB (no host override), so the
// tuned 16 GiB laptop values and the measured large-host values live in one
// config file instead of code.
func DefaultPolicy(physicalMemoryMB float64) Policy {
	if physicalMemoryMB <= 0 {
		physicalMemoryMB = 16 * 1024
	}
	workLimits := defaultWorkLimits(runtime.NumCPU())
	workLimits.Admission = WorkAdmissionResource
	policy := basePolicy(workLimits)
	levels, err := EmbeddedMemoryLevels().Resolve(physicalMemoryMB, "")
	if err != nil {
		panic(fmt.Sprintf("embedded memory levels for %.0f MB: %v", physicalMemoryMB, err))
	}
	levels.Apply(&policy)
	return policy
}

// basePolicy holds every default except the memory ladder, which comes from
// memory_levels.json through MemoryLevels.Apply.
func basePolicy(workLimits WorkLimits) Policy {
	return Policy{
		SchemaVersion:    SchemaVersion,
		Profile:          PolicyProfileObserve,
		Enabled:          true,
		EnforceAdmission: false,
		AutoShedCritical: false,
		// Native process attribution refreshes every two normal samples. At 180s
		// the inventory remains inside admission's 195s trust window while the
		// 90-second host probe keeps the resident below its idle CPU budget.
		SampleIntervalSeconds:           90,
		PressureSampleIntervalSeconds:   15,
		CriticalSampleIntervalSeconds:   5,
		ProcessInventoryIntervalSeconds: 180,
		HeartbeatSeconds:                300,
		SustainSamples:                  4,
		CriticalSustainSamples:          2,
		ActionCooldownSeconds:           300,
		CandidateMinAgeSeconds:          600,
		CandidateMaxCPUPercent:          2.5,
		RetentionDays:                   14,
		BlockNewAt:                      LevelRed,
		Thresholds: Thresholds{
			HostCPUWarningPercent: 85,
			HostCPURedPercent:     95,
			// 8 MB/s sustained is ~0.7 TB/day of SSD writes; 32 MB/s is
			// ~115 GB/hour. The observed 2026-11-02 storm ran 69-607 MB/s.
			SwapOutWarningMBPerSecond: 8,
			SwapOutThrashMBPerSecond:  32,
			SwapOutReleaseMBPerSecond: 16,
		},
		ResourceBudgets: ResourceBudgets{
			// MaxSelfRSSMB comes from memory_levels.json (guard_max_self_rss_mb).
			MaxIdleCPUPercent:     0.25,
			MaxPressureCPUPercent: 6,
			MaxSampleDurationMS:   2000,
			MaxSampleCPUTimeMS:    50,
			MaxTelemetryBytesDay:  1 << 20,
		},
		WorkLimits: func() WorkLimits {
			// The low-level defaults remain useful to deterministic scheduler
			// tests, but a freshly initialized policy must not block normal agent
			// work merely because the host entered the warning band.
			workLimits.WarningCapacityEnabled = false
			return workLimits
		}(),
		LaunchAdmission: defaultLaunchAdmissionPolicy(workLimits.Capacity),
		Storage: StoragePolicy{
			Enabled:             true,
			EnforceAdmission:    false,
			WarningFreeBytes:    50 << 30,
			RedFreeBytes:        25 << 30,
			CriticalFreeBytes:   10 << 30,
			WarningReleaseBytes: 60 << 30,
			// P4: narrower hysteresis (25→30 GiB) so latched red is escapable
			// by normal in-flight completion without a 10 GiB reclaim cliff.
			RedReleaseBytes: 30 << 30,
			// A 5 GiB recovery band matches red hysteresis without doubling
			// the provisional 10 GiB emergency reserve for lightweight work.
			CriticalReleaseBytes:   15 << 30,
			TargetFreeBytes:        50 << 30,
			ProviderMinAgeSeconds:  2 * 60 * 60,
			ReclaimCooldownSeconds: 60 * 60,
			WorkWaitGraceSeconds:   60,
			BlockNewAt:             LevelRed,
		},
		DiskWrite: defaultDiskWritePolicy(),
	}
}

func (policy Policy) Validate() error {
	if policy.SchemaVersion != SchemaVersion {
		return fmt.Errorf("unsupported schema_version %d", policy.SchemaVersion)
	}
	if policy.Profile != "" && !validPolicyProfileName(policy.Profile) {
		return fmt.Errorf("unknown policy profile %q", policy.Profile)
	}
	if policy.SampleIntervalSeconds < 5 || policy.SampleIntervalSeconds > 300 {
		return errors.New("sample_interval_seconds must be between 5 and 300")
	}
	if policy.PressureSampleIntervalSeconds < 5 || policy.PressureSampleIntervalSeconds > policy.SampleIntervalSeconds {
		return errors.New("pressure_sample_interval_seconds must be between 5 and the normal sample interval")
	}
	if policy.CriticalSampleIntervalSeconds < 5 || policy.CriticalSampleIntervalSeconds > policy.PressureSampleIntervalSeconds {
		return errors.New("critical_sample_interval_seconds must be between 5 and the pressure sample interval")
	}
	if policy.ProcessInventoryIntervalSeconds < policy.SampleIntervalSeconds || policy.ProcessInventoryIntervalSeconds > 3600 {
		return errors.New("process_inventory_interval_seconds must be between the normal sample interval and 3600")
	}
	if policy.HeartbeatSeconds < policy.SampleIntervalSeconds || policy.HeartbeatSeconds > 86400 {
		return errors.New("heartbeat_seconds must be at least the sample interval and at most 86400")
	}
	if policy.SustainSamples < 1 || policy.CriticalSustainSamples < 1 {
		return errors.New("sustain sample counts must be positive")
	}
	if policy.AutoShedCritical && !policy.EnforceAdmission {
		return errors.New("auto_shed_critical requires enforce_admission")
	}
	if policy.ActionCooldownSeconds < 60 {
		return errors.New("action_cooldown_seconds must be at least 60")
	}
	if policy.CandidateMinAgeSeconds < 0 || policy.CandidateMaxCPUPercent < 0 {
		return errors.New("candidate age and CPU limits cannot be negative")
	}
	if policy.RetentionDays < 1 || policy.RetentionDays > 365 {
		return errors.New("retention_days must be between 1 and 365")
	}
	if policy.BlockNewAt != LevelRed && policy.BlockNewAt != LevelCritical {
		return errors.New("block_new_at must be red or critical")
	}
	if policy.MemoryLevels != "" && policy.MemoryLevels != MemoryLevelsCalibrated && policy.MemoryLevels != MemoryLevelsManual {
		return fmt.Errorf("memory_levels must be %q or %q (got %q)", MemoryLevelsCalibrated, MemoryLevelsManual, policy.MemoryLevels)
	}
	if t := policy.Thresholds.RSSEscalationFreePercent; t < 0 || t > 100 {
		return errors.New("rss_escalation_free_percent must be between 0 (off) and 100")
	}
	b := policy.ResourceBudgets
	if b.MaxSelfRSSMB <= 0 || b.MaxIdleCPUPercent <= 0 || b.MaxPressureCPUPercent <= 0 || b.MaxSampleDurationMS <= 0 || b.MaxSampleCPUTimeMS <= 0 || b.MaxTelemetryBytesDay <= 0 {
		return errors.New("all resource budgets must be positive")
	}
	w := normalizeWorkLimits(policy.WorkLimits)
	t := policy.Thresholds
	if w.Capacity < 1 || w.TestWeight < 1 || w.BuildWeight < 1 || w.ExpressTestWeight < 1 || w.ExpressBuildWeight < 1 || w.EmulatorWeight < 1 || w.BrowserWeight < 1 || w.HeavyWeight < 1 || w.BenchmarkWeight < 1 || w.ReclaimWeight < 1 {
		return errors.New("work capacity and weights must be positive")
	}
	if w.IdleParkSeconds > 0 && w.IdleParkSeconds < minIdleParkSeconds {
		return fmt.Errorf("work idle_park_seconds must be negative (disabled) or at least %d", minIdleParkSeconds)
	}
	if w.SwapThrashCapacity < 1 || w.SwapThrashCapacity > w.Capacity {
		return errors.New("work swap_thrash_capacity must be between 1 and capacity")
	}
	if w.InteractiveRedLightCapacity > w.Capacity {
		return errors.New("work interactive_red_light_capacity must not exceed capacity")
	}
	if w.InteractiveRedLightMaxPeakRSSMB < 0 {
		return errors.New("work interactive_red_light_max_peak_rss_mb must not be negative")
	}
	if w.SwapThrashExternalReleaseSeconds > 0 && w.SwapThrashExternalReleaseSeconds < minSwapThrashExternalReleaseSeconds {
		return fmt.Errorf("work swap_thrash_external_release_seconds must be negative (strict) or at least %d", minSwapThrashExternalReleaseSeconds)
	}
	if w.SwapThrashAgentSharePercent < 1 || w.SwapThrashAgentSharePercent > 100 {
		return errors.New("work swap_thrash_agent_share_percent must be between 1 and 100")
	}
	switch w.MemoryAdmission {
	case "", MemoryAdmissionShadow, MemoryAdmissionEnforce:
	default:
		return fmt.Errorf("work memory_admission %q must be shadow or enforce", w.MemoryAdmission)
	}
	switch w.Admission {
	case "", WorkAdmissionResource, WorkAdmissionWeighted:
	default:
		return fmt.Errorf("work admission %q must be %s or %s", w.Admission, WorkAdmissionResource, WorkAdmissionWeighted)
	}
	if w.WarningCapacity < 1 || w.WarningCapacity > w.Capacity {
		return errors.New("work warning_capacity must be positive and no greater than capacity")
	}
	if w.TestWeight > w.Capacity || w.BuildWeight > w.Capacity || w.ExpressTestWeight > w.Capacity || w.ExpressBuildWeight > w.Capacity || w.EmulatorWeight > w.Capacity || w.BrowserWeight > w.Capacity || w.HeavyWeight > w.Capacity || w.BenchmarkWeight > w.Capacity || w.ReclaimWeight > w.Capacity {
		return errors.New("work weights cannot exceed capacity")
	}
	if w.ExpressTestWeight >= w.TestWeight || w.ExpressBuildWeight >= w.BuildWeight {
		return errors.New("express weights must be strictly lighter than full test/build weights")
	}
	// Non-exclusive benchmarks must leave residual capacity that package-scoped
	// express work can still fit. Clean-host exclusivity is a separate class.
	if w.BenchmarkWeight+w.ExpressTestWeight > w.Capacity {
		return errors.New("benchmark_weight must leave residual capacity for express-test")
	}
	if w.SchedulingPolicy != WorkSchedulingPolicy && w.SchedulingPolicy != WorkSchedulingPolicyFIFO {
		return fmt.Errorf("work scheduling_policy must be %q or %q", WorkSchedulingPolicy, WorkSchedulingPolicyFIFO)
	}
	launch := policy.LaunchAdmission
	if launch.Mode != LaunchAdmissionModeSoft && launch.Mode != LaunchAdmissionModeOff {
		return fmt.Errorf("launch admission mode must be %q or %q", LaunchAdmissionModeSoft, LaunchAdmissionModeOff)
	}
	if launch.QueueDepthBlock < 1 || launch.OldestWaitBlockSeconds < 1 {
		return errors.New("launch admission queue depth and oldest-wait thresholds must be positive")
	}
	if launch.ResumeBehavior != LaunchResumeBehaviorWarn && launch.ResumeBehavior != LaunchResumeBehaviorBlock {
		return fmt.Errorf("launch resume_behavior must be %q or %q", LaunchResumeBehaviorWarn, LaunchResumeBehaviorBlock)
	}
	if w.CPUBlockSamples < 1 || w.CPUReleaseSamples < 1 {
		return errors.New("work CPU hysteresis sample counts must be positive")
	}
	if !(w.CPUReleasePercent > 0 && w.CPUReleasePercent < t.HostCPUWarningPercent) {
		return errors.New("work cpu_release_percent must be positive and below the host CPU warning threshold")
	}
	if !(0 < t.HostCPUWarningPercent && t.HostCPUWarningPercent < t.HostCPURedPercent && t.HostCPURedPercent <= 100) {
		return errors.New("host CPU thresholds must satisfy 0 < warning < red <= 100")
	}
	if !(t.FreeCriticalPercent < t.FreeRedPercent && t.FreeRedPercent < t.FreeWarningPercent && t.FreeWarningPercent <= 100) {
		return errors.New("free-memory thresholds must satisfy critical < red < warning <= 100")
	}
	if !(t.SwapWarningMB < t.SwapRedMB && t.SwapRedMB < t.SwapCriticalMB) {
		return errors.New("swap thresholds must satisfy warning < red < critical")
	}
	if !(0 < t.SwapOutWarningMBPerSecond && t.SwapOutWarningMBPerSecond < t.SwapOutThrashMBPerSecond) {
		return errors.New("swap-out rate thresholds must satisfy 0 < warning < thrash")
	}
	if !(0 < t.SwapOutReleaseMBPerSecond && t.SwapOutReleaseMBPerSecond < t.SwapOutThrashMBPerSecond) {
		return errors.New("swap-out rate thresholds must satisfy 0 < release < thrash")
	}
	if t.SwapLagReleaseMinutes < 0 || (t.SwapLagReleaseMinutes > 0 && t.SwapLagReleaseMinutes < minSwapLagReleaseMinutes) {
		return fmt.Errorf("swap_lag_release_minutes must be 0 (off) or at least %d", minSwapLagReleaseMinutes)
	}
	if t.SwapLagMaxGrowthMB < 0 {
		return errors.New("swap_lag_max_growth_mb must not be negative")
	}
	if !(t.TreeWarningMB < t.TreeRedMB && t.TreeRedMB < t.TreeCriticalMB) {
		return errors.New("tree thresholds must satisfy warning < red < critical")
	}
	if !(t.AgentTotalWarningMB < t.AgentTotalRedMB && t.AgentTotalRedMB < t.AgentTotalCriticalMB) {
		return errors.New("agent-total thresholds must satisfy warning < red < critical")
	}
	footprint := []float64{t.TreeFootprintWarningMB, t.TreeFootprintRedMB, t.TreeFootprintCriticalMB, t.AgentTotalFootprintWarningMB, t.AgentTotalFootprintRedMB, t.AgentTotalFootprintCriticalMB}
	anyFootprint := false
	for _, value := range footprint {
		anyFootprint = anyFootprint || value != 0
	}
	if anyFootprint {
		if !(0 < t.TreeFootprintWarningMB && t.TreeFootprintWarningMB < t.TreeFootprintRedMB && t.TreeFootprintRedMB < t.TreeFootprintCriticalMB) {
			return errors.New("tree footprint thresholds must satisfy 0 < warning < red < critical, or all be 0 (off)")
		}
		if !(0 < t.AgentTotalFootprintWarningMB && t.AgentTotalFootprintWarningMB < t.AgentTotalFootprintRedMB && t.AgentTotalFootprintRedMB < t.AgentTotalFootprintCriticalMB) {
			return errors.New("agent-total footprint thresholds must satisfy 0 < warning < red < critical, or all be 0 (off)")
		}
	}
	s := policy.Storage
	if s.Enabled {
		if !(0 < s.CriticalFreeBytes && s.CriticalFreeBytes < s.RedFreeBytes && s.RedFreeBytes < s.WarningFreeBytes) {
			return errors.New("storage entry thresholds must satisfy 0 < critical < red < warning")
		}
		if s.WarningReleaseBytes <= s.WarningFreeBytes || s.RedReleaseBytes <= s.RedFreeBytes || s.CriticalReleaseBytes <= s.CriticalFreeBytes {
			return errors.New("storage release thresholds must exceed their entry thresholds")
		}
		if s.TargetFreeBytes < s.WarningFreeBytes {
			return errors.New("storage target_free_bytes must be at least warning_free_bytes")
		}
		if s.ProviderMinAgeSeconds < 0 || s.ReclaimCooldownSeconds < 60 {
			return errors.New("storage provider age cannot be negative and reclaim cooldown must be at least 60 seconds")
		}
		if s.WorkWaitGraceSeconds < 15 || s.WorkWaitGraceSeconds > 600 {
			return errors.New("storage work_wait_grace_seconds must be between 15 and 600")
		}
		if s.BlockNewAt != LevelRed && s.BlockNewAt != LevelCritical {
			return errors.New("storage block_new_at must be red or critical")
		}
	}
	if err := validateStorageRepositoryOverrides(s.RepositoryOverrides); err != nil {
		return err
	}
	disk := policy.DiskWrite
	if disk.Profile != DiskWriteProfileQuietAdaptiveV1 {
		return fmt.Errorf("disk-write profile must be %q", DiskWriteProfileQuietAdaptiveV1)
	}
	if disk.SampleIntervalSeconds < 5 || disk.SampleIntervalSeconds > 60 {
		return errors.New("disk-write sample_interval_seconds must be between 5 and 60")
	}
	if disk.BaselineRetentionDays < 1 || disk.BaselineRetentionDays > 30 {
		return errors.New("disk-write baseline_retention_days must be between 1 and 30")
	}
	if disk.TraceMaxDurationSeconds < 5 || disk.TraceMaxDurationSeconds > 30 {
		return errors.New("disk-write trace_max_duration_seconds must be between 5 and 30")
	}
	return nil
}

func DataDir() (string, error) {
	if value := os.Getenv(DataDirEnv); value != "" {
		return filepath.Abs(value)
	}
	if value := os.Getenv(DataDirEnvAlias); value != "" {
		return filepath.Abs(value)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".nicos-dev", "session-pressure"), nil
}

func PolicyPath(dir string) string { return filepath.Join(dir, "policy.json") }

func LoadPolicy(path string, physicalMemoryMB float64) (Policy, bool, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			policy := DefaultPolicy(physicalMemoryMB)
			return policy, false, nil
		}
		return Policy{}, false, err
	}
	var policy Policy
	if err := json.Unmarshal(body, &policy); err != nil {
		return Policy{}, true, fmt.Errorf("decode policy: %w", err)
	}
	defaults := DefaultPolicy(physicalMemoryMB)
	if strings.TrimSpace(policy.Profile) == "" {
		if policy.EnforceAdmission {
			policy.Profile = PolicyProfileBalanced
		} else {
			policy.Profile = PolicyProfileObserve
		}
	}
	if policy.WorkLimits.SchedulingPolicy == "" {
		policy.WorkLimits.SchedulingPolicy = defaults.WorkLimits.SchedulingPolicy
	}
	if policy.WorkLimits.Capacity == 0 {
		policy.WorkLimits.Capacity = defaults.WorkLimits.Capacity
	}
	if policy.WorkLimits.TestWeight == 0 {
		policy.WorkLimits.TestWeight = defaults.WorkLimits.TestWeight
	}
	if policy.WorkLimits.BuildWeight == 0 {
		policy.WorkLimits.BuildWeight = defaults.WorkLimits.BuildWeight
	}
	// Migrate the exact shipped weight-6 formula to the safer-packing profile.
	// Any other positive value remains an explicit operator-owned calibration.
	legacyBuildWeight := max(1, (policy.WorkLimits.Capacity*2+2)/3)
	if policy.WorkLimits.BuildWeight == legacyBuildWeight && defaults.WorkLimits.BuildWeight != legacyBuildWeight {
		policy.WorkLimits.BuildWeight = defaults.WorkLimits.BuildWeight
	}
	if policy.WorkLimits.ExpressTestWeight == 0 {
		policy.WorkLimits.ExpressTestWeight = defaults.WorkLimits.ExpressTestWeight
	}
	if policy.WorkLimits.ExpressBuildWeight == 0 {
		policy.WorkLimits.ExpressBuildWeight = defaults.WorkLimits.ExpressBuildWeight
	}
	if policy.WorkLimits.BenchmarkWeight == 0 {
		// Soften the historical exclusive-by-default benchmark. Operators who
		// need clean-host evidence opt into benchmark-exclusive / --exclusive.
		policy.WorkLimits.BenchmarkWeight = defaults.WorkLimits.BenchmarkWeight
	}
	if policy.WorkLimits.EmulatorWeight == 0 {
		policy.WorkLimits.EmulatorWeight = defaults.WorkLimits.EmulatorWeight
	}
	if policy.WorkLimits.BrowserWeight == 0 {
		policy.WorkLimits.BrowserWeight = defaults.WorkLimits.BrowserWeight
	}
	if policy.WorkLimits.HeavyWeight == 0 {
		policy.WorkLimits.HeavyWeight = defaults.WorkLimits.HeavyWeight
	}
	if policy.WorkLimits.InstallWeight == 0 {
		policy.WorkLimits.InstallWeight = defaults.WorkLimits.InstallWeight
	}
	if policy.WorkLimits.ReclaimWeight == 0 {
		policy.WorkLimits.ReclaimWeight = defaults.WorkLimits.ReclaimWeight
	}
	if policy.WorkLimits.WarningCapacity == 0 {
		policy.WorkLimits.WarningCapacity = defaults.WorkLimits.WarningCapacity
	}
	if policy.WorkLimits.IdleParkSeconds == 0 {
		policy.WorkLimits.IdleParkSeconds = defaultIdleParkSeconds
	}
	if policy.WorkLimits.Admission == "" {
		// Nico, 2026-11-17: admission is resource-based on every host. A
		// policy written before the field existed moves to it on load;
		// "weighted" stays only as an explicit rollback.
		policy.WorkLimits.Admission = WorkAdmissionResource
	}
	if policy.WorkLimits.SwapThrashCapacity == 0 {
		// Derive from this policy's own ceiling, not the host default: an
		// operator-tuned capacity must not inherit a mismatched derate.
		policy.WorkLimits.SwapThrashCapacity = min(policy.WorkLimits.Capacity, policy.WorkLimits.BuildWeight)
	}
	if policy.WorkLimits.SwapThrashExternalReleaseSeconds == 0 {
		policy.WorkLimits.SwapThrashExternalReleaseSeconds = defaultSwapThrashExternalReleaseSeconds
	}
	if policy.WorkLimits.SwapThrashAgentSharePercent == 0 {
		policy.WorkLimits.SwapThrashAgentSharePercent = defaultSwapThrashAgentSharePercent
	}
	if policy.WorkLimits.CPUBlockSamples == 0 {
		policy.WorkLimits.CPUBlockSamples = defaults.WorkLimits.CPUBlockSamples
	}
	if policy.WorkLimits.CPUReleaseSamples == 0 {
		policy.WorkLimits.CPUReleaseSamples = defaults.WorkLimits.CPUReleaseSamples
	}
	if policy.WorkLimits.CPUReleasePercent == 0 {
		policy.WorkLimits.CPUReleasePercent = defaults.WorkLimits.CPUReleasePercent
	}
	policy.WorkLimits = reconcileFastLaneDefaults(policy.WorkLimits, defaults.WorkLimits)
	if policy.LaunchAdmission.Mode == "" {
		policy.LaunchAdmission = defaultLaunchAdmissionPolicy(policy.WorkLimits.Capacity)
	} else {
		if policy.LaunchAdmission.QueueDepthBlock == 0 {
			policy.LaunchAdmission.QueueDepthBlock = defaults.LaunchAdmission.QueueDepthBlock
		}
		if policy.LaunchAdmission.OldestWaitBlockSeconds == 0 {
			policy.LaunchAdmission.OldestWaitBlockSeconds = defaults.LaunchAdmission.OldestWaitBlockSeconds
		}
		if policy.LaunchAdmission.ResumeBehavior == "" {
			policy.LaunchAdmission.ResumeBehavior = defaults.LaunchAdmission.ResumeBehavior
		}
	}
	if policy.Thresholds.HostCPUWarningPercent == 0 {
		policy.Thresholds.HostCPUWarningPercent = defaults.Thresholds.HostCPUWarningPercent
	}
	if policy.Thresholds.HostCPURedPercent == 0 {
		policy.Thresholds.HostCPURedPercent = defaults.Thresholds.HostCPURedPercent
	}
	if policy.Thresholds.SwapOutWarningMBPerSecond == 0 {
		policy.Thresholds.SwapOutWarningMBPerSecond = defaults.Thresholds.SwapOutWarningMBPerSecond
	}
	if policy.Thresholds.SwapOutThrashMBPerSecond == 0 {
		policy.Thresholds.SwapOutThrashMBPerSecond = defaults.Thresholds.SwapOutThrashMBPerSecond
	}
	if policy.Thresholds.SwapOutReleaseMBPerSecond == 0 {
		policy.Thresholds.SwapOutReleaseMBPerSecond = defaults.Thresholds.SwapOutReleaseMBPerSecond
	}
	if policy.ResourceBudgets.MaxSampleCPUTimeMS == 0 {
		policy.ResourceBudgets.MaxSampleCPUTimeMS = defaults.ResourceBudgets.MaxSampleCPUTimeMS
	}
	if policy.ResourceBudgets.MaxSampleDurationMS == 0 {
		policy.ResourceBudgets.MaxSampleDurationMS = defaults.ResourceBudgets.MaxSampleDurationMS
	}
	// Migrate both the original ps-era ceiling and the temporary 500ms dogfood
	// ceiling. Native inventory made 500ms realistic while idle, but live load
	// showed scheduler outliers above it; 2s remains strict enough to catch a
	// recurring ps regression without making readiness depend on one outlier.
	if policy.ResourceBudgets.MaxSampleDurationMS == 10000 || policy.ResourceBudgets.MaxSampleDurationMS == 500 {
		policy.ResourceBudgets.MaxSampleDurationMS = defaults.ResourceBudgets.MaxSampleDurationMS
	}
	if policy.ResourceBudgets.MaxSampleCPUTimeMS == 300 {
		policy.ResourceBudgets.MaxSampleCPUTimeMS = defaults.ResourceBudgets.MaxSampleCPUTimeMS
	}
	// The 100ms observe floor predates identity lsof on fresh inventory
	// (~115ms measured). Lift the previous floor so persisted observe
	// policies do not fail the required identity path every refresh.
	if policy.ResourceBudgets.MaxSampleCPUTimeMS == 100 {
		policy.ResourceBudgets.MaxSampleCPUTimeMS = 150
	}
	if policy.ResourceBudgets.MaxPressureCPUPercent == 0 {
		policy.ResourceBudgets.MaxPressureCPUPercent = defaults.ResourceBudgets.MaxPressureCPUPercent
	}
	if policy.PressureSampleIntervalSeconds == 0 {
		policy.PressureSampleIntervalSeconds = defaults.PressureSampleIntervalSeconds
	}
	if policy.CriticalSampleIntervalSeconds == 0 {
		policy.CriticalSampleIntervalSeconds = defaults.CriticalSampleIntervalSeconds
	}
	if policy.ProcessInventoryIntervalSeconds == 0 {
		policy.ProcessInventoryIntervalSeconds = defaults.ProcessInventoryIntervalSeconds
	}
	// The original healthy-state inventory interval (300s) exceeded the live
	// admission projection's 195s trust window. Migrate that shipped default to
	// the native-sampler 180s cadence; policies using any other explicit value
	// remain operator-owned.
	if policy.ProcessInventoryIntervalSeconds == 300 {
		policy.ProcessInventoryIntervalSeconds = defaults.ProcessInventoryIntervalSeconds
	}
	// Policies written before storage pressure shipped have an all-zero storage
	// block. Migrate them to observation-only defaults without silently granting
	// new admission or reclaim authority.
	if policy.Storage.WarningFreeBytes == 0 {
		overrides := policy.Storage.RepositoryOverrides
		policy.Storage = defaults.Storage
		policy.Storage.RepositoryOverrides = overrides
	}
	if policy.Storage.WorkWaitGraceSeconds == 0 {
		policy.Storage.WorkWaitGraceSeconds = defaults.Storage.WorkWaitGraceSeconds
	}
	// Policies written before disk-write observation have an all-zero block.
	// Fill the complete observe-only profile without opting the operator into
	// notifications or any enforcement authority.
	if policy.DiskWrite.Profile == "" {
		policy.DiskWrite = defaults.DiskWrite
	} else {
		if policy.DiskWrite.SampleIntervalSeconds == 0 {
			policy.DiskWrite.SampleIntervalSeconds = defaults.DiskWrite.SampleIntervalSeconds
		}
		if policy.DiskWrite.BaselineRetentionDays == 0 {
			policy.DiskWrite.BaselineRetentionDays = defaults.DiskWrite.BaselineRetentionDays
		}
		if policy.DiskWrite.TraceMaxDurationSeconds == 0 {
			policy.DiskWrite.TraceMaxDurationSeconds = defaults.DiskWrite.TraceMaxDurationSeconds
		}
	}
	if policy.MemoryLevels == MemoryLevelsCalibrated {
		levels, err := ResolveHostMemoryLevels(filepath.Dir(path), physicalMemoryMB)
		if err != nil {
			return Policy{}, true, err
		}
		levels.Apply(&policy)
	}
	if err := policy.Validate(); err != nil {
		return Policy{}, true, fmt.Errorf("validate policy: %w", err)
	}
	return policy, true, nil
}

// Equal reports whether two policies are identical. Policy holds a map, so
// it cannot use ==; nil and empty repository overrides are equal.
func (policy Policy) Equal(other Policy) bool {
	if !maps.Equal(policy.Storage.RepositoryOverrides, other.Storage.RepositoryOverrides) {
		return false
	}
	policy.Storage.RepositoryOverrides, other.Storage.RepositoryOverrides = nil, nil
	return reflect.DeepEqual(policy, other)
}

func SavePolicy(path string, policy Policy) error {
	if err := policy.Validate(); err != nil {
		return err
	}
	body, err := json.MarshalIndent(policy, "", "  ")
	if err != nil {
		return err
	}
	body = append(body, '\n')
	return atomicWrite(path, body, 0o600)
}

func atomicWrite(path string, body []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := fmt.Sprintf("%s.tmp-%d", path, time.Now().UnixNano())
	if err := os.WriteFile(tmp, body, mode); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// Work admission modes (WorkLimits.Admission).
const (
	WorkAdmissionResource = "resource"
	WorkAdmissionWeighted = "weighted"
	// unboundedWorkCapacity is the coordinator ceiling under resource
	// admission. It is not a limit: no host runs this many leases, so the
	// weighted selection keeps only its ordering role. An exclusive class
	// still weighs the whole ceiling, so it still runs alone.
	unboundedWorkCapacity = 1 << 20
)

// ResourceAdmission reports whether measured resource headroom decides
// admission, not the weighted unit ceiling.
func (limits WorkLimits) ResourceAdmission() bool {
	return limits.Admission == WorkAdmissionResource
}

// admissionLimits returns the limits the coordinator admits against. Under
// resource admission the unit ceilings do not decide: the capacity is
// unbounded and the warning ceiling is off. Policy files and telemetry keep
// the configured values.
func (limits WorkLimits) admissionLimits() WorkLimits {
	if !limits.ResourceAdmission() {
		return limits
	}
	limits.Capacity = unboundedWorkCapacity
	limits.WarningCapacityEnabled = false
	return limits
}

// WorkResourceAdmissionLive reports whether resource-headroom admission
// decides local work. It does not depend on EnforceAdmission.
func WorkResourceAdmissionLive(policy Policy) bool {
	return policy.Enabled && policy.WorkLimits.ResourceAdmission()
}

// WorkMemoryAdmissionEnforced reports whether memory headroom holds new work:
// always under resource admission, else with memory_admission = enforce. It
// does not depend on EnforceAdmission.
func WorkMemoryAdmissionEnforced(policy Policy) bool {
	return policy.Enabled && (policy.WorkLimits.ResourceAdmission() || policy.WorkLimits.MemoryAdmission == MemoryAdmissionEnforce)
}

// WorkSwapThrashEnforced reports whether swap thrash holds new work: under
// resource admission (the ladder), under an enforcing profile, or when memory
// admission is enforced on its own.
func WorkSwapThrashEnforced(policy Policy) bool {
	return policy.Enabled && (policy.WorkLimits.ResourceAdmission() || policy.EnforceAdmission || policy.WorkLimits.MemoryAdmission == MemoryAdmissionEnforce)
}

func workAdmissionMode(limits WorkLimits) string {
	if limits.ResourceAdmission() {
		return WorkAdmissionResource
	}
	return WorkAdmissionWeighted
}
