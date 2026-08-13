package sessionpressurecmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/nstranquist/session-pressure/internal/sessionpressure"
)

const sessionPressureHelp = `Usage: ndev [--json] session pressure <subcommand> [flags]

Protect the Mac from sustained host and storage pressure caused by concurrent work.
Monitoring is observe-only until policy enable is run explicitly.

Subcommands:
  snapshot                         Sample host/process-tree pressure without writing telemetry
  check                            Evaluate whether a canonical agent launch is admitted
  doctor                           Compact agent/operator health (monitor, policy, queue, soft-launch)
  audit [--no-live] [--since D] [--full]  Compact category pass/fail; --full adds metrics blobs
  status [--live] [--full]         Show compact health; --live samples, --full adds diagnostics
  self-test [--wait D] [--full]    Verify the installed path; default queue wait is 30s, compact JSON by default
  board [--live] [--full] [--include S]  One composite read; compact by default, --full for desktop diagnostics
  recovery [clear]                 Show or clear an unclean-shutdown session recovery hint
  telemetry [--limit N] [--since D]  Read bounded transition/heartbeat telemetry
  idle [flags]                     List old idle trees; exact --apply is graceful and audited
  cleanup <subcommand>             Pressure reclaim policy, plan, history, and resource claims
  artifact status|prune [--apply]  Verify installed helper provenance and safely retain rollbacks
  storage <subcommand>             Inspect capacity, plan/provider reclaim, receipts, and storage policy
  io <subcommand>                  Observe internal SSD writes and likely all-volume process writers
  work status                      Show the weighted heavy-work coordinator
  work history [--limit N] [--full]  Compact lifecycle rows (default 20); --full digests/forensics
  work stats|report [--since D] [--full]  Compact calibration counts; full diagnostics opt in
  work override --operation-id ID --confirm  Run one selected queued operation next without bypassing safety gates
  work run --class C [--exclusive] [--wait D] -- CMD  Hold shared host capacity while CMD runs
  policy show                      Show the effective policy
  policy profile show|apply NAME   Named balanced / throughput / interactive / observe work styles
  policy init [--force]            Write the tuned observe-only default policy
  policy migrate                   Persist filled default work weights (express/benchmark) without changing enforcement
  policy enable [--no-auto-shed]   Block launches at red; gracefully shed a quiescent tree at sustained critical
  policy observe                   Keep monitoring but disable admission blocks and automatic shedding
  monitor once                     Sample once and persist a manual telemetry event
  monitor run                      Run a foreground diagnostic loop (never automatic relief)
  monitor install [--enforce]      Install/start the low-priority user LaunchAgent
  monitor status                   Show LaunchAgent state
  monitor uninstall                Stop/remove the LaunchAgent; keep policy and telemetry
  api serve|status                 Run or inspect the explicit local control-plane server
  identity show [--live]           Show agent identity catalog, install roots, and miss diagnostics

Examples:
  ndev --json session pressure doctor
  ndev --json session pressure audit --since 24h
  ndev --json session pressure snapshot
  ndev --json session pressure self-test --wait 10m
  ndev session pressure monitor install
  ndev session pressure policy enable
  ndev session pressure policy profile apply observe
  ndev --json session pressure work report --since 24h
  ndev session pressure work run --wait 0 -- go test ./internal/sessionpressure
  ndev session pressure work run --class express-test -- go test ./internal/sessionpressure
  ndev session pressure work run --class test -- go test ./...
  ndev session pressure work run --class benchmark-exclusive -- ndev perf verify --strict
	  ndev --json session pressure storage plan --target-free 50GiB
	  ndev --json session pressure io status --live
  ndev --json session pressure telemetry --since 24h --limit 20
  ndev --json session pressure idle --min-age 12h --limit 10
  ndev session pressure idle --apply --root-pid PID --session-id ID
`

// sessionPressureInvocationAction returns a bounded, categorical leaf action
// for telemetry. It deliberately never copies flag values or child argv.
func sessionPressureInvocationAction(args []string) (string, bool) {
	for len(args) > 0 {
		switch args[0] {
		case "--json", "--yes", "--use-daemon", "--verbose", "--debug":
			args = args[1:]
		default:
			goto root
		}
	}

root:
	if len(args) < 2 || args[0] != "session" || args[1] != "pressure" {
		return "", false
	}
	return sessionPressureLeafAction(args[2:]), true
}

// sessionPressureLeafAction is the live session.pressure.command leaf. The
// vocabulary is owned by internal/sessionpressure (shared with cli-invocations
// action attrs) so the two recorders cannot drift.
func sessionPressureLeafAction(args []string) string {
	return sessionpressure.CommandLeaf(args)
}

func sessionPressureInvocationOutcome(exitCode int) string {
	switch {
	case exitCode == 0:
		return "success"
	case exitCode == 2:
		return "usage_error"
	case exitCode >= 11 && exitCode <= 15:
		return "policy_denied"
	default:
		return "failure"
	}
}

func cmdSessionPressure(g *Flags, args []string) int {
	sub := "snapshot"
	if len(args) > 0 {
		sub = args[0]
		args = args[1:]
	}
	switch sub {
	case "help", "--help", "-h":
		fmt.Print(sessionPressureHelp)
		return 0
	case "snapshot":
		return cmdSessionPressureSnapshot(g, args, false)
	case "check":
		return cmdSessionPressureCheck(g, args)
	case "doctor":
		return cmdSessionPressureDoctor(g, args)
	case "audit":
		return cmdSessionPressureAudit(g, args)
	case "status":
		return cmdSessionPressureStatus(g, args)
	case "board":
		return cmdSessionPressureBoard(g, args)
	case "self-test":
		return cmdSessionPressureSelfTest(g, args)
	case "recovery":
		return cmdSessionPressureRecovery(g, args)
	case "telemetry":
		return cmdSessionPressureTelemetry(g, args)
	case "idle":
		return cmdSessionPressureIdle(g, args)
	case "cleanup":
		return cmdSessionPressureCleanup(g, args)
	case "artifact":
		return cmdSessionPressureArtifact(g, args)
	case "storage":
		return cmdSessionPressureStorage(g, args)
	case "io":
		return cmdSessionPressureIO(g, args)
	case "work":
		return cmdSessionPressureWork(g, args)
	case "policy":
		return cmdSessionPressurePolicy(g, args)
	case "monitor":
		return cmdSessionPressureMonitor(g, args)
	case "api":
		return cmdSessionPressureAPI(g, args)
	case "identity":
		return cmdSessionPressureIdentity(g, args)
	default:
		// Top agent-misuse cluster (session.pressure invalid_input): list the
		// primary leaves so agents stop probing with invented verbs/flags.
		return sessionPressureError("unknown subcommand "+strconv.Quote(sub)+"; try: ndev session pressure --help (status|doctor|work|policy|monitor|snapshot|check|audit|recovery|telemetry|idle|cleanup|artifact|storage|io|api|identity)", 2)
	}
}

type pressureRuntime struct {
	dir       string
	path      string
	policy    sessionpressure.Policy
	persisted bool
	sampler   *sessionpressure.Sampler
	store     *sessionpressure.TelemetryStore
}

type LaunchdController interface {
	Install(context.Context) (sessionpressure.LaunchdStatus, error)
	Uninstall(context.Context) (sessionpressure.LaunchdStatus, error)
	Restart(context.Context) (sessionpressure.LaunchdStatus, error)
	EnsureRunning(context.Context) (sessionpressure.LaunchdStatus, error)
	Status(context.Context) sessionpressure.LaunchdStatus
}

var NewLaunchdController = func(dir string) (LaunchdController, error) {
	return sessionpressure.NewLaunchdManager("", dir)
}

var AcquirePolicyMutationLockHook = sessionpressure.AcquirePolicyMutationLock

type pressurePolicyMutation struct {
	Runtime pressureRuntime
	Context context.Context
	cancel  context.CancelFunc
	unlock  func()
}

func beginPressurePolicyMutation(dir string) (*pressurePolicyMutation, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	unlock, err := AcquirePolicyMutationLockHook(ctx, dir, 20*time.Second)
	if err != nil {
		cancel()
		return nil, err
	}
	runtime, err := loadPressureRuntime(ctx)
	if err != nil {
		unlock()
		cancel()
		return nil, fmt.Errorf("reload policy under mutation lock: %w", err)
	}
	return &pressurePolicyMutation{Runtime: runtime, Context: ctx, cancel: cancel, unlock: unlock}, nil
}

func (mutation *pressurePolicyMutation) Close() {
	if mutation == nil {
		return
	}
	if mutation.unlock != nil {
		mutation.unlock()
		mutation.unlock = nil
	}
	if mutation.cancel != nil {
		mutation.cancel()
		mutation.cancel = nil
	}
}

var LaunchdManagementAllowed = func() bool {
	_, overridden := os.LookupEnv(sessionpressure.DataDirEnv)
	if overridden {
		return false
	}
	_, aliased := os.LookupEnv(sessionpressure.DataDirEnvAlias)
	return !aliased
}

var SampleSnapshot = func(ctx context.Context, sampler *sessionpressure.Sampler, policy sessionpressure.Policy) (sessionpressure.Snapshot, error) {
	return sampler.Sample(ctx, policy)
}

var RunMonitorOnce = func(ctx context.Context, sampler *sessionpressure.Sampler, store *sessionpressure.TelemetryStore, policy sessionpressure.Policy) (sessionpressure.Snapshot, error) {
	return sessionpressure.NewMonitor(sampler, store, policy).RunOnce(ctx, "manual")
}

var appendPressureAction = func(store *sessionpressure.TelemetryStore, action sessionpressure.Action) error {
	return store.AppendActionDurable(action)
}

var reapPressureIdleTree = sessionpressure.ReapIdleTree

func loadPressureRuntime(ctx context.Context) (pressureRuntime, error) {
	dir, err := sessionpressure.DataDir()
	if err != nil {
		return pressureRuntime{}, err
	}
	sampler := sessionpressure.NewSampler()
	physicalMB, err := sampler.PhysicalMemoryMB(ctx)
	if err != nil {
		return pressureRuntime{}, err
	}
	path := sessionpressure.PolicyPath(dir)
	policy, persisted, err := sessionpressure.LoadPolicy(path, physicalMB)
	if err != nil {
		return pressureRuntime{}, err
	}
	sampler.WithWorkCoordinator(sessionpressure.NewWorkCoordinator(dir, policy.WorkLimits))
	return pressureRuntime{
		dir: dir, path: path, policy: policy, persisted: persisted,
		sampler: sampler, store: sessionpressure.NewTelemetryStore(dir),
	}, nil
}

func cmdSessionPressureSnapshot(g *Flags, args []string, persist bool) int {
	if len(args) != 0 {
		return sessionPressureError("snapshot accepts no arguments", 2)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	runtime, err := loadPressureRuntime(ctx)
	if err != nil {
		return sessionPressureError(err.Error(), 1)
	}
	var snapshot sessionpressure.Snapshot
	if persist {
		snapshot, err = RunMonitorOnce(ctx, runtime.sampler, runtime.store, runtime.policy)
	} else {
		snapshot, err = SampleSnapshot(ctx, runtime.sampler, runtime.policy)
	}
	if err != nil {
		return sessionPressureError(err.Error(), 1)
	}
	if runtime.persisted {
		snapshot.PolicySource = runtime.path
	} else {
		snapshot.PolicySource = "built-in-default"
	}
	payload := map[string]any{"ok": true, "action": "snapshot", "snapshot": snapshot}
	if persist {
		payload["action"] = "monitor.once"
	}
	return emitPressure(g, payload, snapshot.Summary()+"\n", 0)
}

func cmdSessionPressureCheck(g *Flags, args []string) int {
	if len(args) != 0 {
		return sessionPressureError("check accepts no arguments", 2)
	}
	admission := AgentLaunchAdmissionCheck(sessionpressure.AgentLaunchNew)
	exit := 0
	if !admission.Allowed {
		// A denial is a policy decision, not a command failure. Exit inside
		// the reserved policy band (11-15) so invocation telemetry classifies
		// it policy_block/policy_denied instead of unknown_new — red-pressure
		// windows were producing hundreds of unclassifiable "failures" per day.
		exit = 11
	}
	text := fmt.Sprintf("allowed=%v level=%s source=%s", admission.Allowed, admission.Level, admission.Source)
	if admission.Warning != "" {
		text += " warning=" + admission.Warning
	} else if len(admission.Reasons) > 0 {
		text += " reason=" + admission.Reasons[0]
	}
	payload := map[string]any{"ok": admission.Allowed, "action": "check", "admission": admission}
	return emitPressure(g, payload, text+"\n", exit)
}

func cmdSessionPressureStatus(g *Flags, args []string) int {
	live := false
	full := false
	for _, arg := range args {
		switch arg {
		case "--live":
			live = true
		case "--full":
			full = true
		default:
			return sessionPressureError("status accepts only --live and --full", 2)
		}
	}
	runtimeCtx, runtimeCancel := context.WithTimeout(context.Background(), 5*time.Second)
	runtime, err := loadPressureRuntime(runtimeCtx)
	runtimeCancel()
	if err != nil {
		return sessionPressureError(err.Error(), 1)
	}
	manager, err := NewLaunchdController(runtime.dir)
	if err != nil {
		return sessionPressureError(err.Error(), 1)
	}
	statusCtx, statusCancel := context.WithTimeout(context.Background(), 5*time.Second)
	launchd := manager.Status(statusCtx)
	statusCancel()
	latest, hasLatest := runtime.store.ReadLatest()
	recovery, hasRecovery, recoveryErr := sessionpressure.LoadRecoveryHint(runtime.dir)
	health := sessionpressure.AssessGuardHealth(time.Now().UTC(), runtime.policy, runtime.persisted, launchd, latest, hasLatest).WithOperatorState(hasRecovery, recoveryErr)
	repoRoot, repoRootErr := nicosToolsRepoRoot()
	if repoRootErr != nil {
		repoRoot = ""
	}
	var identityTrees []sessionpressure.AgentTree
	if hasLatest {
		identityTrees = latest.TopAgentTrees
	}
	coverage := sessionpressure.AssessCoverageDetailed(sessionpressure.CoverageAssessment{
		RepoRoot: repoRoot,
		Policy:   runtime.policy,
		Health:   health,
		Trees:    identityTrees,
		Catalog:  sessionpressure.ActiveAgentIdentityCatalog(),
	})
	ok := health.MonitorHealthy && recoveryErr == nil && !hasRecovery
	payload := map[string]any{
		"ok": ok, "action": "status", "output_scope": "compact",
		"policy_persisted":   runtime.persisted,
		"has_latest_monitor": hasLatest, "has_recovery_hint": hasRecovery,
		"health": health, "launchd_summary": compactPressureLaunchdStatus(launchd),
		"coverage_summary": compactPressureCoverage(coverage),
	}
	if full {
		payload["output_scope"] = "full"
		payload["policy"] = runtime.policy
		payload["policy_path"] = runtime.path
		payload["launchd"] = launchd
		delete(payload, "launchd_summary")
		payload["coverage"] = coverage
		delete(payload, "coverage_summary")
	}
	if repoRootErr != nil {
		payload["coverage_error"] = repoRootErr.Error()
	}
	text := ""
	if live {
		sampleCtx, sampleCancel := context.WithTimeout(context.Background(), 5*time.Second)
		snapshot, sampleErr := SampleSnapshot(sampleCtx, runtime.sampler, runtime.policy)
		sampleCancel()
		if sampleErr == nil {
			if runtime.policy.DiskWrite.Enabled {
				diskCtx, diskCancel := context.WithTimeout(context.Background(), 3*time.Second)
				diskReport, diskErr := sessionpressure.LiveDiskWriteReport(diskCtx, runtime.dir, runtime.policy.DiskWrite, time.Second)
				diskCancel()
				if diskErr == nil {
					if hasLatest && latest.DiskWrite != nil {
						age := time.Since(latest.DiskWrite.CapturedAt)
						if age >= -5*time.Second && age <= 5*time.Minute {
							diskReport = sessionpressure.MergeLiveDiskWriteReport(diskReport, *latest.DiskWrite)
						}
					}
					diskSummary := diskReport.Summary
					snapshot.DiskWrite = &diskSummary
					if full {
						snapshot.DiskWriteWriters = diskReport.Writers
					}
				} else {
					payload["disk_write_sample_error"] = diskErr.Error()
				}
			}
			payload["snapshot_summary"] = compactPressureStatusSnapshot(snapshot)
			if full {
				payload["snapshot"] = snapshot
				delete(payload, "snapshot_summary")
			}
			text = snapshot.Summary()
		} else {
			payload["sample_error"] = sampleErr.Error()
			text = "live_sample_error=" + strconv.Quote(sampleErr.Error())
			ok = false
			payload["ok"] = false
		}
	} else if hasLatest {
		text = latest.Summary()
	}
	if hasLatest {
		payload["latest_monitor_summary"] = compactPressureStatusSnapshot(latest)
		if full {
			payload["latest_monitor"] = latest
			delete(payload, "latest_monitor_summary")
		}
	}
	if hasRecovery {
		payload["recovery_hint"] = recovery
	}
	if recoveryErr != nil {
		payload["recovery_hint_error"] = recoveryErr.Error()
	}
	text += fmt.Sprintf(" monitor_healthy=%v daily_driver_ready=%v operator_ready=%v protection=%s coverage=%s launchd_loaded=%v policy_persisted=%v", health.MonitorHealthy, health.DailyDriverReady, health.OperatorReady, health.ProtectionMode, coverage.Status, launchd.Loaded, runtime.persisted)
	if hasLatest {
		text += fmt.Sprintf(" resident_budget_ok=%v resident_samples=%d", latest.GuardBudgetOK, latest.MonitorSamples)
	}
	if hasRecovery {
		text += " recovery_hint=true"
	}
	if recoveryErr != nil {
		text += " recovery_hint_error=" + strconv.Quote(recoveryErr.Error())
	}
	text += "\n"
	exit := 0
	if !ok {
		exit = 1
	}
	return emitPressure(g, payload, text, exit)
}

type pressureCompactLaunchdStatus struct {
	OK               bool   `json:"ok"`
	Label            string `json:"label"`
	Installed        bool   `json:"installed"`
	Loaded           bool   `json:"loaded"`
	PID              int    `json:"pid,omitempty"`
	ArtifactPresent  bool   `json:"artifact_present"`
	ArtifactVerified bool   `json:"artifact_verified"`
}

func compactPressureLaunchdStatus(status sessionpressure.LaunchdStatus) pressureCompactLaunchdStatus {
	return pressureCompactLaunchdStatus{
		OK: status.OK, Label: status.Label,
		Installed: status.Installed, Loaded: status.Loaded, PID: status.PID,
		ArtifactPresent: status.ArtifactPresent, ArtifactVerified: status.ArtifactVerified,
	}
}

type pressureCompactCoverage struct {
	Status      string `json:"status"`
	Enforced    int    `json:"enforced"`
	Coordinated int    `json:"coordinated"`
	Observed    int    `json:"observed"`
	Attention   int    `json:"attention"`
	Limitations int    `json:"limitation_count"`
}

func compactPressureCoverage(report sessionpressure.CoverageReport) pressureCompactCoverage {
	summary := pressureCompactCoverage{Status: report.Status, Limitations: len(report.Limitations)}
	for _, surface := range report.Surfaces {
		switch surface.State {
		case sessionpressure.CoverageEnforced:
			summary.Enforced++
		case sessionpressure.CoverageCoordinated:
			summary.Coordinated++
		case sessionpressure.CoverageObserved:
			summary.Observed++
		case sessionpressure.CoverageAttention:
			summary.Attention++
		}
	}
	return summary
}

type pressureCompactStatusSnapshot struct {
	Timestamp                  time.Time                         `json:"timestamp"`
	Level                      sessionpressure.Level             `json:"level"`
	Reasons                    []string                          `json:"reasons,omitempty"`
	FreePercent                int                               `json:"free_percent"`
	SwapUsedMB                 float64                           `json:"swap_used_mb"`
	HostCPUPercent             float64                           `json:"host_cpu_percent"`
	AgentTreeCount             int                               `json:"agent_tree_count"`
	AgentRSSSumMB              float64                           `json:"agent_rss_sum_mb"`
	SampleRole                 string                            `json:"sample_role"`
	SelfRSSMB                  float64                           `json:"self_rss_mb"`
	SelfCPUPercent             float64                           `json:"self_cpu_percent"`
	SelfBudgetApplicable       bool                              `json:"self_budget_applicable"`
	CoordinatedWork            pressureCompactCoordinatedWork    `json:"coordinated_work"`
	MemoryMomentum             sessionpressure.MemoryMomentum    `json:"memory_momentum"`
	GuardBudgetOK              bool                              `json:"guard_budget_ok"`
	GuardBudgetReasons         []string                          `json:"guard_budget_reasons,omitempty"`
	MonitorSamples             int                               `json:"monitor_samples"`
	NormalMonitorSamples       int                               `json:"normal_monitor_samples"`
	TelemetryBytesToday        int64                             `json:"telemetry_bytes_today,omitempty"`
	TelemetryProjectedBytesDay int64                             `json:"telemetry_projected_bytes_per_day,omitempty"`
	ResourceCleanupExecutedAt  time.Time                         `json:"resource_cleanup_control_executed_at,omitempty,omitzero"`
	ResourceCleanupDurationMS  float64                           `json:"resource_cleanup_control_duration_ms,omitempty"`
	ResourceCleanupMaxRSSMB    float64                           `json:"resource_cleanup_control_max_rss_mb,omitempty"`
	StorageLevel               sessionpressure.Level             `json:"storage_level,omitempty"`
	DiskWrite                  *sessionpressure.DiskWriteSummary `json:"disk_write,omitempty"`
}

type pressureCompactCoordinatedWork struct {
	Available              bool      `json:"available"`
	Fresh                  bool      `json:"fresh"`
	CapturedAt             time.Time `json:"captured_at,omitempty,omitzero"`
	InventoryAgeSeconds    float64   `json:"inventory_age_seconds"`
	LeaseCount             int       `json:"lease_count"`
	AttributedLeaseCount   int       `json:"attributed_lease_count"`
	UnattributedLeaseCount int       `json:"unattributed_lease_count"`
	ProcessCount           int       `json:"process_count"`
	CPUPercent             float64   `json:"cpu_percent"`
	CPUAvailable           bool      `json:"cpu_available"`
}

// compactPressureAdmission keeps the launch decision and queue evidence while
// dropping the nested host snapshot. Callers that need the snapshot already
// have latest_monitor_summary / snapshot_summary on the same board/status read.
func compactPressureAdmission(admission sessionpressure.Admission) sessionpressure.Admission {
	admission.Snapshot = nil
	return admission
}

func compactPressureDiskWriteSummary(summary *sessionpressure.DiskWriteSummary) *sessionpressure.DiskWriteSummary {
	if summary == nil {
		return nil
	}
	compact := *summary
	// Ordinary pressure status is the cheap agent heartbeat. Detailed process
	// coverage and the likely-writer lead remain available from `io status` and
	// `io top`; dropping them here preserves meaningful headroom under the 4 KiB
	// default payload budget as process counts and executable names fluctuate.
	compact.MeasurementWindowSeconds = 0
	compact.BaselineAgeSeconds = 0
	compact.DeviceCount = 0
	compact.TotalPIDCount = 0
	compact.AccessiblePIDCount = 0
	compact.WriterAvailableCount = 0
	compact.TopWriter = nil
	return &compact
}

func compactPressureStatusSnapshot(snapshot sessionpressure.Snapshot) pressureCompactStatusSnapshot {
	return pressureCompactStatusSnapshot{
		Timestamp: snapshot.Timestamp, Level: snapshot.Level, Reasons: append([]string(nil), snapshot.Reasons...),
		FreePercent: snapshot.FreePercent, SwapUsedMB: snapshot.SwapUsedMB, HostCPUPercent: snapshot.HostCPUPercent,
		AgentTreeCount: snapshot.AgentTreeCount, AgentRSSSumMB: snapshot.AgentRSSSumMB, MemoryMomentum: snapshot.MemoryMomentum,
		SampleRole: snapshot.GuardRole, SelfRSSMB: snapshot.GuardRSSMB, SelfCPUPercent: snapshot.GuardCPUPercent,
		SelfBudgetApplicable: snapshot.GuardBudgetApplicable,
		CoordinatedWork: pressureCompactCoordinatedWork{
			Available: snapshot.CoordinatedWork.Available, Fresh: snapshot.CoordinatedWork.Fresh,
			CapturedAt: snapshot.CoordinatedWork.CapturedAt, LeaseCount: snapshot.CoordinatedWork.LeaseCount,
			InventoryAgeSeconds:    snapshot.CoordinatedWork.InventoryAgeSeconds,
			AttributedLeaseCount:   snapshot.CoordinatedWork.AttributedLeaseCount,
			UnattributedLeaseCount: snapshot.CoordinatedWork.UnattributedLeaseCount,
			ProcessCount:           snapshot.CoordinatedWork.ProcessCount, CPUPercent: snapshot.CoordinatedWork.CPUPercent,
			CPUAvailable: snapshot.CoordinatedWork.CPUAvailable,
		},
		GuardBudgetOK: snapshot.GuardBudgetOK, GuardBudgetReasons: append([]string(nil), snapshot.GuardBudgetReasons...),
		MonitorSamples: snapshot.MonitorSamples, NormalMonitorSamples: snapshot.NormalMonitorSamples,
		TelemetryBytesToday: snapshot.TelemetryBytesToday, TelemetryProjectedBytesDay: snapshot.TelemetryProjectedBytesDay,
		ResourceCleanupExecutedAt: snapshot.ResourceCleanupExecutedAt, ResourceCleanupDurationMS: snapshot.ResourceCleanupDurationMS,
		ResourceCleanupMaxRSSMB: snapshot.ResourceCleanupMaxRSSMB,
		StorageLevel:            snapshot.Storage.Level,
		DiskWrite:               compactPressureDiskWriteSummary(snapshot.DiskWrite),
	}
}

func cmdSessionPressureRecovery(g *Flags, args []string) int {
	clear := len(args) == 1 && args[0] == "clear"
	if len(args) > 1 || (len(args) == 1 && !clear) {
		return sessionPressureError("recovery accepts only the optional clear action", 2)
	}
	dir, err := sessionpressure.DataDir()
	if err != nil {
		return sessionPressureError(err.Error(), 1)
	}
	if clear {
		if err := sessionpressure.ClearRecoveryHint(dir); err != nil {
			return sessionPressureError(err.Error(), 1)
		}
		return emitPressure(g, map[string]any{"ok": true, "action": "recovery.clear", "cleared": true, "path": sessionpressure.RecoveryHintPath(dir)}, "cleared session pressure recovery hint\n", 0)
	}
	hint, found, err := sessionpressure.LoadRecoveryHint(dir)
	if err != nil {
		return sessionPressureError(err.Error(), 1)
	}
	payload := map[string]any{"ok": true, "action": "recovery.show", "found": found, "path": sessionpressure.RecoveryHintPath(dir)}
	if !found {
		return emitPressure(g, payload, "no unclean-shutdown recovery hint\n", 0)
	}
	payload["hint"] = hint
	return emitPressure(g, payload, hint.RecoveryCommand+"\n", 0)
}

func cmdSessionPressureTelemetry(g *Flags, args []string) int {
	limit := 20
	since := time.Now().Add(-24 * time.Hour)
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--limit":
			i++
			if i >= len(args) {
				return sessionPressureError("--limit requires an integer", 2)
			}
			value, err := strconv.Atoi(args[i])
			if err != nil || value < 1 || value > 10000 {
				return sessionPressureError("--limit must be between 1 and 10000", 2)
			}
			limit = value
		case "--since":
			i++
			if i >= len(args) {
				return sessionPressureError("--since requires a duration", 2)
			}
			duration, err := time.ParseDuration(args[i])
			if err != nil || duration <= 0 {
				return sessionPressureError("--since must be a positive duration such as 24h", 2)
			}
			since = time.Now().Add(-duration)
		default:
			return sessionPressureError("unknown telemetry argument "+strconv.Quote(args[i]), 2)
		}
	}
	dir, err := sessionpressure.DataDir()
	if err != nil {
		return sessionPressureError(err.Error(), 1)
	}
	store := sessionpressure.NewTelemetryStore(dir)
	events, err := store.ReadEvents(limit, since)
	if err != nil {
		return sessionPressureError(err.Error(), 1)
	}
	actions, err := store.ReadActions(limit, since)
	if err != nil {
		return sessionPressureError(err.Error(), 1)
	}
	payload := map[string]any{
		"ok": true, "action": "telemetry", "events": events, "count": len(events),
		"actions": actions, "action_count": len(actions), "since": since.UTC(), "directory": dir,
	}
	if g != nil && g.JSON {
		return emitPressure(g, payload, "", 0)
	}
	for _, event := range events {
		if event.Snapshot != nil {
			fmt.Printf("%s\t%s\t%s\n", event.Timestamp.Local().Format(time.RFC3339), event.Event, event.Snapshot.Summary())
		} else if event.Summary != nil {
			fmt.Printf("%s\t%s\tlevel=%s free=%d%% swap=%.0fMB cpu=%.1f%% agents=%d/%.0fMB resident_self=%.1fMB\n", event.Timestamp.Local().Format(time.RFC3339), event.Event, event.Summary.Level, event.Summary.FreePercent, event.Summary.SwapUsedMB, event.Summary.HostCPUPercent, event.Summary.AgentTreeCount, event.Summary.AgentRSSSumMB, event.Summary.GuardRSSMB)
		} else {
			fmt.Printf("%s\t%s\t%s\n", event.Timestamp.Local().Format(time.RFC3339), event.Event, event.Error)
		}
	}
	for _, action := range actions {
		target := action.Agent
		if action.RootPID > 0 {
			target += ":" + strconv.Itoa(action.RootPID)
		}
		if action.SessionID != "" {
			target += " session=" + action.SessionID
		}
		fmt.Printf("%s\taction\t%s\t%s\t%s\n", action.Timestamp.Local().Format(time.RFC3339), action.Kind, action.Result, target)
	}
	return 0
}

func cmdSessionPressureIdle(g *Flags, args []string) int {
	criteria := sessionpressure.DefaultIdleCriteria()
	apply := false
	rootPID := 0
	sessionID := ""
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--min-age":
			i++
			if i >= len(args) {
				return sessionPressureError("--min-age requires a duration", 2)
			}
			value, err := time.ParseDuration(args[i])
			if err != nil {
				return sessionPressureError("--min-age must be a duration such as 12h", 2)
			}
			criteria.MinAge = value
		case "--max-cpu":
			i++
			if i >= len(args) {
				return sessionPressureError("--max-cpu requires a percentage", 2)
			}
			value, err := strconv.ParseFloat(args[i], 64)
			if err != nil {
				return sessionPressureError("--max-cpu must be a number", 2)
			}
			criteria.MaxCPUPercent = value
		case "--limit":
			i++
			if i >= len(args) {
				return sessionPressureError("--limit requires an integer", 2)
			}
			value, err := strconv.Atoi(args[i])
			if err != nil {
				return sessionPressureError("--limit must be an integer", 2)
			}
			criteria.Limit = value
		case "--apply":
			apply = true
		case "--root-pid":
			i++
			if i >= len(args) {
				return sessionPressureError("--root-pid requires an integer", 2)
			}
			value, err := strconv.Atoi(args[i])
			if err != nil || value <= 0 {
				return sessionPressureError("--root-pid must be a positive integer", 2)
			}
			rootPID = value
		case "--session-id":
			i++
			if i >= len(args) || strings.TrimSpace(args[i]) == "" {
				return sessionPressureError("--session-id requires a non-empty value", 2)
			}
			sessionID = strings.TrimSpace(args[i])
		default:
			return sessionPressureError("unknown idle argument "+strconv.Quote(args[i]), 2)
		}
	}
	if err := criteria.Validate(); err != nil {
		return sessionPressureError(err.Error(), 2)
	}
	if apply && (rootPID <= 0 || sessionID == "") {
		return sessionPressureError("--apply requires both --root-pid and --session-id from the current idle inventory", 2)
	}
	if !apply && (rootPID > 0 || sessionID != "") {
		return sessionPressureError("--root-pid and --session-id are accepted only with --apply", 2)
	}

	loadCtx, loadCancel := context.WithTimeout(context.Background(), 5*time.Second)
	runtime, err := loadPressureRuntime(loadCtx)
	loadCancel()
	if err != nil {
		return sessionPressureError(err.Error(), 1)
	}
	sampleCtx, sampleCancel := context.WithTimeout(context.Background(), 5*time.Second)
	snapshot, err := SampleSnapshot(sampleCtx, runtime.sampler, runtime.policy)
	sampleCancel()
	if err != nil {
		return sessionPressureError(err.Error(), 1)
	}
	inventory, err := sessionpressure.InspectIdleTrees(snapshot, criteria, os.Getpid())
	if err != nil {
		return sessionPressureError(err.Error(), 1)
	}
	payload := map[string]any{
		"ok": true, "action": "idle.inspect", "apply": false,
		"sampled_at": snapshot.Timestamp, "level": snapshot.Level,
		"agent_tree_count": snapshot.AgentTreeCount,
		"criteria": map[string]any{
			"min_age_seconds": int64(criteria.MinAge / time.Second),
			"max_cpu_percent": criteria.MaxCPUPercent,
			"limit":           criteria.Limit,
		},
		"inventory": inventory,
	}
	if !apply {
		if g != nil && g.JSON {
			return emitPressure(g, payload, "", 0)
		}
		if len(inventory.Candidates) == 0 {
			return emitPressure(g, payload, "no old idle agent trees matched the conservative boundary\n", 0)
		}
		var text strings.Builder
		fmt.Fprintf(&text, "%d old idle agent tree(s); inspect before applying:\n", inventory.CandidateCount)
		for _, candidate := range inventory.Candidates {
			fmt.Fprintf(&text, "  %s pid=%d session=%s age=%s cpu=%.2f%% rss=%.0fMB\n", candidate.Agent, candidate.RootPID, candidate.SessionID, (time.Duration(candidate.ElapsedSeconds) * time.Second).Round(time.Minute), candidate.CPUPercentSum, candidate.RSSSumMB)
		}
		return emitPressure(g, payload, text.String(), 0)
	}

	var selected sessionpressure.IdleCandidate
	for _, candidate := range inventory.Candidates {
		if candidate.RootPID == rootPID && candidate.SessionID == sessionID {
			selected = candidate
			break
		}
	}
	if selected.RootPID == 0 {
		return sessionPressureError("the exact PID/session pair is not eligible in the current fresh inventory", 1)
	}
	intent := sessionpressure.NewIdleReapIntent(selected, snapshot.Level, time.Now())
	if err := appendPressureAction(runtime.store, intent); err != nil {
		return sessionPressureError("refusing idle cleanup because action intent could not be persisted: "+err.Error(), 1)
	}
	reapCtx, reapCancel := context.WithTimeout(context.Background(), 20*time.Second)
	action, reapErr := reapPressureIdleTree(reapCtx, runtime.sampler, runtime.policy, selected, criteria)
	reapCancel()
	if appendErr := appendPressureAction(runtime.store, action); appendErr != nil {
		if reapErr != nil {
			return sessionPressureError(fmt.Sprintf("%v; persist action result after durable intent: %v", reapErr, appendErr), 1)
		}
		return sessionPressureError("tree signal completed but final action result persistence failed; durable intent remains recorded: "+appendErr.Error(), 1)
	}
	payload["action"] = "idle.apply"
	payload["apply"] = true
	payload["result"] = action
	if reapErr != nil {
		payload["ok"] = false
		if g != nil && g.JSON {
			return emitPressure(g, payload, "", 1)
		}
		return emitPressure(g, payload, "idle cleanup refused: "+reapErr.Error()+"\n", 1)
	}
	text := fmt.Sprintf("idle cleanup %s for %s pid=%d session=%s\n", action.Result, action.Agent, action.RootPID, action.SessionID)
	return emitPressure(g, payload, text, 0)
}

func cmdSessionPressurePolicy(g *Flags, args []string) int {
	sub := "show"
	if len(args) > 0 {
		sub = args[0]
		args = args[1:]
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	runtime, err := loadPressureRuntime(ctx)
	if err != nil {
		return sessionPressureError(err.Error(), 1)
	}
	switch sub {
	case "show":
		if len(args) != 0 {
			return sessionPressureError("policy show accepts no arguments", 2)
		}
		payload := map[string]any{"ok": true, "action": "policy.show", "policy": runtime.policy, "path": runtime.path, "persisted": runtime.persisted}
		body, marshalErr := json.MarshalIndent(runtime.policy, "", "  ")
		if marshalErr != nil {
			return sessionPressureError("encode pressure policy: "+marshalErr.Error(), 1)
		}
		return emitPressure(g, payload, string(body)+"\n", 0)
	case "init":
		force := len(args) == 1 && args[0] == "--force"
		if len(args) > 1 || (len(args) == 1 && !force) {
			return sessionPressureError("policy init accepts only --force", 2)
		}
		mutation, err := beginPressurePolicyMutation(runtime.dir)
		if err != nil {
			return sessionPressureError(err.Error(), 1)
		}
		defer mutation.Close()
		runtime = mutation.Runtime
		if runtime.persisted && !force {
			return sessionPressureError("policy already exists; pass --force to replace it", 1)
		}
		physicalMB, err := runtime.sampler.PhysicalMemoryMB(mutation.Context)
		if err != nil {
			return sessionPressureError(err.Error(), 1)
		}
		runtime.policy = sessionpressure.DefaultPolicy(physicalMB)
		if err := sessionpressure.SavePolicy(runtime.path, runtime.policy); err != nil {
			return sessionPressureError(err.Error(), 1)
		}
		if err := restartPressureMonitor(runtime.dir); err != nil {
			return sessionPressureError("policy saved but resident monitor did not confirm reload: "+err.Error(), 1)
		}
		return emitPressure(g, map[string]any{"ok": true, "action": "policy.init", "policy": runtime.policy, "path": runtime.path}, "initialized observe-only policy at "+runtime.path+"\n", 0)
	case "migrate":
		if len(args) != 0 {
			return sessionPressureError("policy migrate accepts no arguments", 2)
		}
		// Persist LoadPolicy's in-memory fills (express/benchmark weights, etc.)
		// without resetting enable/enforce/auto-shed operator choices.
		mutation, err := beginPressurePolicyMutation(runtime.dir)
		if err != nil {
			return sessionPressureError(err.Error(), 1)
		}
		defer mutation.Close()
		runtime = mutation.Runtime
		if err := sessionpressure.SavePolicy(runtime.path, runtime.policy); err != nil {
			return sessionPressureError(err.Error(), 1)
		}
		if err := restartPressureMonitor(runtime.dir); err != nil {
			return sessionPressureError("policy migrated but resident monitor did not confirm reload: "+err.Error(), 1)
		}
		return emitPressure(g, map[string]any{
			"ok": true, "action": "policy.migrate", "policy": runtime.policy, "path": runtime.path,
		}, "migrated effective policy weights to "+runtime.path+"\n", 0)
	case "enable":
		noAutoShed := len(args) == 1 && args[0] == "--no-auto-shed"
		if len(args) > 1 || (len(args) == 1 && !noAutoShed) {
			return sessionPressureError("policy enable accepts only --no-auto-shed", 2)
		}
		mutation, err := beginPressurePolicyMutation(runtime.dir)
		if err != nil {
			return sessionPressureError(err.Error(), 1)
		}
		defer mutation.Close()
		runtime = mutation.Runtime
		runtime.policy.Enabled = true
		runtime.policy.EnforceAdmission = true
		runtime.policy.AutoShedCritical = !noAutoShed
		runtime.policy.Profile = sessionpressure.PolicyProfileBalanced
		runtime.policy.WorkLimits.WarningCapacityEnabled = false
		if err := sessionpressure.SavePolicy(runtime.path, runtime.policy); err != nil {
			return sessionPressureError(err.Error(), 1)
		}
		launchd, err := ensurePressureMonitor(runtime.dir)
		if err != nil {
			// Never leave a failed activation armed for destructive action on a
			// later implicit launchd restart. Admission remains enabled as the
			// safe fallback and the command reports failure.
			runtime.policy.AutoShedCritical = false
			fallbackErr := sessionpressure.SavePolicy(runtime.path, runtime.policy)
			if fallbackErr == nil {
				fallbackErr = restartPressureMonitor(runtime.dir)
			}
			if fallbackErr != nil {
				return sessionPressureError(fmt.Sprintf("resident monitor activation failed: %v; admission-only fallback also failed: %v", err, fallbackErr), 1)
			}
			return sessionPressureError("resident monitor activation failed; policy reverted to admission-only: "+err.Error(), 1)
		}
		return emitPressure(g, map[string]any{"ok": true, "action": "policy.enable", "policy": runtime.policy, "path": runtime.path, "launchd": launchd}, fmt.Sprintf("enabled admission guard; auto_shed_critical=%v launchd_pid=%d\n", runtime.policy.AutoShedCritical, launchd.PID), 0)
	case "observe":
		if len(args) != 0 {
			return sessionPressureError("policy observe accepts no arguments", 2)
		}
		mutation, err := beginPressurePolicyMutation(runtime.dir)
		if err != nil {
			return sessionPressureError(err.Error(), 1)
		}
		defer mutation.Close()
		runtime = mutation.Runtime
		runtime.policy.Enabled = true
		runtime.policy.EnforceAdmission = false
		runtime.policy.AutoShedCritical = false
		runtime.policy.Profile = sessionpressure.PolicyProfileObserve
		runtime.policy.WorkLimits.WarningCapacityEnabled = false
		if err := sessionpressure.SavePolicy(runtime.path, runtime.policy); err != nil {
			return sessionPressureError(err.Error(), 1)
		}
		if err := restartPressureMonitor(runtime.dir); err != nil {
			return sessionPressureError("policy saved but resident monitor did not confirm reload: "+err.Error(), 1)
		}
		return emitPressure(g, map[string]any{"ok": true, "action": "policy.observe", "policy": runtime.policy, "path": runtime.path}, "set observe-only policy\n", 0)
	case "profile":
		return cmdSessionPressurePolicyProfile(g, runtime, args)
	default:
		return sessionPressureError("unknown policy subcommand "+strconv.Quote(sub), 2)
	}
}

func cmdSessionPressureMonitor(g *Flags, args []string) int {
	sub := "status"
	if len(args) > 0 {
		sub = args[0]
		args = args[1:]
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	runtime, err := loadPressureRuntime(ctx)
	cancel()
	if err != nil {
		return sessionPressureError(err.Error(), 1)
	}
	manager, err := NewLaunchdController(runtime.dir)
	if err != nil {
		return sessionPressureError(err.Error(), 1)
	}
	switch sub {
	case "once":
		return cmdSessionPressureSnapshot(g, args, true)
	case "run":
		if len(args) != 0 {
			return sessionPressureError("monitor run accepts no arguments", 2)
		}
		if !runtime.persisted {
			return sessionPressureError("policy is not initialized; run session pressure policy init", 1)
		}
		loopCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		monitor := sessionpressure.NewMonitor(runtime.sampler, runtime.store, runtime.policy)
		if err := monitor.Run(loopCtx); err != nil {
			return sessionPressureError(err.Error(), 1)
		}
		return 0
	case "install":
		enforce := len(args) == 1 && args[0] == "--enforce"
		if len(args) > 1 || (len(args) == 1 && !enforce) {
			return sessionPressureError("monitor install accepts only --enforce", 2)
		}
		mutation, err := beginPressurePolicyMutation(runtime.dir)
		if err != nil {
			return sessionPressureError(err.Error(), 1)
		}
		defer mutation.Close()
		runtime = mutation.Runtime
		policyWasEnabled := runtime.policy.Enabled
		enforceWasEnabled := runtime.policy.EnforceAdmission
		autoShedWasEnabled := runtime.policy.AutoShedCritical
		runtime.policy.Enabled = true
		if !runtime.persisted {
			runtime.policy.EnforceAdmission = enforce
			runtime.policy.AutoShedCritical = enforce
			if err := sessionpressure.SavePolicy(runtime.path, runtime.policy); err != nil {
				return sessionPressureError(err.Error(), 1)
			}
		} else {
			if enforce {
				runtime.policy.EnforceAdmission = true
				runtime.policy.AutoShedCritical = true
			}
			if err := sessionpressure.SavePolicy(runtime.path, runtime.policy); err != nil {
				return sessionPressureError(err.Error(), 1)
			}
		}
		policyChanged := runtime.policy.Enabled != policyWasEnabled ||
			runtime.policy.EnforceAdmission != enforceWasEnabled ||
			runtime.policy.AutoShedCritical != autoShedWasEnabled
		priorStatus := manager.Status(mutation.Context)
		status, err := manager.Install(mutation.Context)
		if err == nil && policyChanged && priorStatus.OK && status.PID == priorStatus.PID {
			// An idempotent same-artifact install preserves the resident. Policy
			// changes are the exception: restart so the process reloads them.
			status, err = manager.Restart(mutation.Context)
		}
		if err != nil {
			// A partially successful bootstrap may already have loaded a resident
			// with the just-written policy. Disarm automatic relief on disk and,
			// when possible, force that resident to reload the safe fallback.
			if runtime.policy.AutoShedCritical && !autoShedWasEnabled {
				runtime.policy.AutoShedCritical = false
				fallbackErr := sessionpressure.SavePolicy(runtime.path, runtime.policy)
				if fallbackErr == nil && status.Loaded && status.PID > 0 {
					fallbackCtx, fallbackCancel := context.WithTimeout(context.Background(), 15*time.Second)
					restarted, restartErr := manager.Restart(fallbackCtx)
					fallbackCancel()
					status = restarted
					fallbackErr = restartErr
				}
				if fallbackErr != nil {
					return sessionPressureError(fmt.Sprintf("monitor install failed: %v; admission-only fallback also failed: %v", err, fallbackErr), 1)
				}
				return sessionPressureError("monitor install failed; policy reverted to admission-only: "+err.Error(), 1)
			}
			return sessionPressureError(err.Error(), 1)
		}
		payload := map[string]any{"ok": true, "action": "monitor.install", "launchd": status}
		return emitPressure(g, payload, fmt.Sprintf("installed %s pid=%d policy=%s\n", status.Label, status.PID, map[bool]string{true: "enforced", false: "observe-only"}[runtime.policy.EnforceAdmission]), 0)
	case "status":
		if len(args) != 0 {
			return sessionPressureError("monitor status accepts no arguments", 2)
		}
		statusCtx, statusCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer statusCancel()
		status := manager.Status(statusCtx)
		payload := map[string]any{"ok": status.OK, "action": "monitor.status", "launchd": status}
		exit := 0
		if !status.OK {
			exit = 1
		}
		return emitPressure(g, payload, fmt.Sprintf("%s installed=%v loaded=%v pid=%d plist=%s\n", status.Label, status.Installed, status.Loaded, status.PID, status.PlistPath), exit)
	case "uninstall":
		if len(args) != 0 {
			return sessionPressureError("monitor uninstall accepts no arguments", 2)
		}
		mutation, err := beginPressurePolicyMutation(runtime.dir)
		if err != nil {
			return sessionPressureError(err.Error(), 1)
		}
		defer mutation.Close()
		runtime = mutation.Runtime
		// Uninstall is the break-glass stop. Persist observe-only policy first so
		// canonical launches cannot continue probing/blocking after the resident
		// process is gone and a future reinstall cannot silently revive actions.
		runtime.policy.Enabled = true
		runtime.policy.EnforceAdmission = false
		runtime.policy.AutoShedCritical = false
		if err := sessionpressure.SavePolicy(runtime.path, runtime.policy); err != nil {
			return sessionPressureError("disable policy mutations before uninstall: "+err.Error(), 1)
		}
		status, err := manager.Uninstall(mutation.Context)
		if err != nil {
			if status.Loaded && status.PID > 0 {
				// launchctl can fail after the policy file has changed. If the old
				// process is still alive, make it reload observe-only policy before
				// reporting the incomplete uninstall.
				fallbackCtx, fallbackCancel := context.WithTimeout(context.Background(), 15*time.Second)
				reloaded, reloadErr := manager.Restart(fallbackCtx)
				fallbackCancel()
				if reloadErr != nil {
					return sessionPressureError(fmt.Sprintf("monitor uninstall failed: %v; policy is observe-only but resident reload failed: %v", err, reloadErr), 1)
				}
				return sessionPressureError(fmt.Sprintf("monitor uninstall failed: %v; resident remains loaded in observe-only mode at pid %d", err, reloaded.PID), 1)
			}
			return sessionPressureError("monitor uninstall failed; policy is observe-only: "+err.Error(), 1)
		}
		payload := map[string]any{"ok": true, "action": "monitor.uninstall", "launchd": status, "policy": runtime.policy, "path": runtime.path}
		return emitPressure(g, payload, "uninstalled "+status.Label+"; policy is observe-only and telemetry is retained\n", 0)
	default:
		return sessionPressureError("unknown monitor subcommand "+strconv.Quote(sub), 2)
	}
}

var WorkHostAdmissionCheck = func() sessionpressure.Admission {
	return sessionpressure.ConfiguredWorkHostAdmission(context.Background())
}

var AgentLaunchAdmissionCheck = func(kind sessionpressure.AgentLaunchKind) sessionpressure.Admission {
	return sessionpressure.ConfiguredAgentLaunchAdmission(context.Background(), kind)
}

func emitPressure(g *Flags, payload any, text string, exit int) int {
	if g != nil && g.JSON {
		body, err := json.MarshalIndent(payload, "", "  ")
		if err != nil {
			return sessionPressureError(err.Error(), 1)
		}
		fmt.Println(string(body))
		return exit
	}
	fmt.Print(text)
	return exit
}

func sessionPressureError(message string, exit int) int {
	message = strings.TrimSpace(message)
	if message == "" {
		message = "unknown error"
	}
	fmt.Fprintln(os.Stderr, "ndev session pressure:", message)
	return exit
}

func restartPressureMonitor(dir string) error {
	if !LaunchdManagementAllowed() {
		return nil
	}
	manager, err := NewLaunchdController(dir)
	if err != nil {
		return err
	}
	statusCtx, statusCancel := context.WithTimeout(context.Background(), 5*time.Second)
	status := manager.Status(statusCtx)
	statusCancel()
	if !status.Loaded {
		return nil
	}
	restartErr := sessionpressure.ErrLaunchAgentNotRunning
	if status.PID > 0 {
		restartCtx, restartCancel := context.WithTimeout(context.Background(), 15*time.Second)
		restarted, callErr := manager.Restart(restartCtx)
		restartCancel()
		status = restarted
		restartErr = callErr
		if restartErr == nil {
			return nil
		}
	}
	// A downgrade such as policy observe must not leave an old in-memory
	// action policy running merely because graceful replacement failed. A full
	// bootout/bootstrap is the second safe convergence path.
	installCtx, installCancel := context.WithTimeout(context.Background(), 20*time.Second)
	installed, installErr := manager.Install(installCtx)
	installCancel()
	if installErr == nil && installed.OK {
		return nil
	}
	return fmt.Errorf("restart failed: %v; reinstall failed: %v", restartErr, installErr)
}

func ensurePressureMonitor(dir string) (sessionpressure.LaunchdStatus, error) {
	if !LaunchdManagementAllowed() {
		return sessionpressure.LaunchdStatus{}, fmt.Errorf("cannot ensure launchd while %s overrides the resident data directory", sessionpressure.DataDirEnv)
	}
	manager, err := NewLaunchdController(dir)
	if err != nil {
		return sessionpressure.LaunchdStatus{}, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	return manager.EnsureRunning(ctx)
}
