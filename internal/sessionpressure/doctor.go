package sessionpressure

import (
	"context"
	"fmt"
	"time"
)

// PressureDoctor is the compact agent/operator health envelope for
// `ndev session pressure doctor`. It composes resident evidence only by default
// (no mandatory live sample or long self-test wait).
type PressureDoctor struct {
	SchemaVersion      int           `json:"schema_version"`
	OK                 bool          `json:"ok"`
	ProtectionMode     string        `json:"protection_mode"`
	PolicyPersisted    bool          `json:"policy_persisted"`
	EnforceAdmission   bool          `json:"enforce_admission"`
	AutoShedCritical   bool          `json:"auto_shed_critical"`
	Monitor            DoctorMonitor `json:"monitor"`
	Host               DoctorHost    `json:"host"`
	Work               DoctorWork    `json:"work"`
	LaunchSoftPressure DoctorLaunch  `json:"launch_soft_pressure"`
	CoverageStatus     string        `json:"coverage_status,omitempty"`
	Fixes              []string      `json:"fixes,omitempty"`
	Warnings           []string      `json:"warnings,omitempty"`
	Health             GuardHealth   `json:"health"`
}

type DoctorMonitor struct {
	Healthy    bool    `json:"healthy"`
	Fresh      bool    `json:"fresh"`
	AgeSeconds float64 `json:"age_seconds,omitempty"`
	PID        int     `json:"pid,omitempty"`
	Loaded     bool    `json:"loaded"`
	Running    bool    `json:"running"`
}

type DoctorHost struct {
	Level  Level  `json:"level"`
	Source string `json:"source,omitempty"`
}

type DoctorWork struct {
	Capacity     int  `json:"capacity"`
	Used         int  `json:"used"`
	QueueDepth   int  `json:"queue_depth"`
	ExpressGreen bool `json:"express_green"`
}

type DoctorLaunch struct {
	WouldBlock      bool `json:"would_block"`
	NoiseSuppressed bool `json:"noise_suppressed"`
	Enforced        bool `json:"enforced"`
}

// PressureDoctorInput is the injectable seam for tests.
type PressureDoctorInput struct {
	Now          time.Time
	Dir          string
	Policy       Policy
	Persisted    bool
	Launchd      LaunchdStatus
	Latest       Snapshot
	HasLatest    bool
	HasRecovery  bool
	RecoveryErr  error
	Work         WorkStatus
	WorkErr      error
	ExpressGreen bool
	RepoRoot     string
	Coverage     CoverageReport
	// Optional: if set, used instead of recomputing soft launch from Work.
	SoftWouldBlock *bool
}

// BuildPressureDoctor composes doctor from already-loaded state (no sampling).
func BuildPressureDoctor(in PressureDoctorInput) PressureDoctor {
	now := in.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	health := AssessGuardHealth(now, in.Policy, in.Persisted, in.Launchd, in.Latest, in.HasLatest).
		WithOperatorState(in.HasRecovery, in.RecoveryErr)
	doc := PressureDoctor{
		SchemaVersion:    1,
		ProtectionMode:   health.ProtectionMode,
		PolicyPersisted:  in.Persisted,
		EnforceAdmission: in.Policy.EnforceAdmission,
		AutoShedCritical: in.Policy.AutoShedCritical,
		Health:           health,
		Monitor: DoctorMonitor{
			Healthy:    health.MonitorHealthy,
			Fresh:      health.LatestMonitorFresh,
			AgeSeconds: health.LatestAgeSeconds,
			PID:        in.Launchd.PID,
			Loaded:     in.Launchd.Loaded,
			Running:    in.Launchd.OK && in.Launchd.PID > 0,
		},
		Host: DoctorHost{Level: LevelNormal, Source: "none"},
		Work: DoctorWork{
			Capacity:     in.Work.Capacity,
			Used:         in.Work.Used,
			QueueDepth:   in.Work.QueueDepth,
			ExpressGreen: in.ExpressGreen,
		},
		CoverageStatus: in.Coverage.Status,
	}
	if in.HasLatest {
		doc.Host.Level = in.Latest.Level
		doc.Host.Source = "resident"
	}
	if in.WorkErr != nil {
		doc.Warnings = append(doc.Warnings, "work queue status unavailable: "+in.WorkErr.Error())
	}

	// Soft launch pressure evidence (same predicates as admission soft path).
	wouldBlock := in.Work.QueueDepth >= max(1, in.Policy.LaunchAdmission.QueueDepthBlock)
	if len(in.Work.Waiters) > 0 && in.Policy.LaunchAdmission.OldestWaitBlockSeconds > 0 {
		// WaitMS on waiters if present
		if in.Work.Waiters[0].WaitMS >= int64(in.Policy.LaunchAdmission.OldestWaitBlockSeconds)*1000 {
			wouldBlock = true
		}
	}
	if in.SoftWouldBlock != nil {
		wouldBlock = *in.SoftWouldBlock
	}
	noiseSuppressed := softLaunchNoiseSuppressed(in.Policy, doc.Host.Level, in.Work.QueueDepth, wouldBlock)
	if noiseSuppressed {
		wouldBlock = false
	}
	doc.LaunchSoftPressure = DoctorLaunch{
		WouldBlock:      wouldBlock,
		NoiseSuppressed: noiseSuppressed,
		Enforced:        in.Policy.EnforceAdmission && in.Policy.Enabled,
	}

	// Fixes from health reasons
	for _, reason := range health.HealthReasons {
		doc.Warnings = append(doc.Warnings, reason)
	}
	for _, reason := range health.OperatorReasons {
		doc.Warnings = append(doc.Warnings, reason)
	}
	if !in.Persisted {
		doc.Fixes = append(doc.Fixes, "ndev session pressure policy init")
	}
	if !health.MonitorHealthy {
		doc.Fixes = append(doc.Fixes, "ndev session pressure monitor install")
		doc.Fixes = append(doc.Fixes, "make -C nicos-dev build-ndev-session-pressure")
	}
	if in.HasRecovery {
		doc.Fixes = append(doc.Fixes, "ndev session pressure recovery clear  # after reviewing unclean shutdown")
	}
	if catalog := ActiveAgentIdentityCatalog(); catalog != nil && catalog.OverlayError != "" {
		doc.Warnings = append(doc.Warnings, "agent identity overlay failed closed: "+catalog.OverlayError)
		doc.Fixes = append(doc.Fixes, "fix ~/.nicos-dev/session-pressure/agent-identity.json or remove it; ndev session pressure identity show")
	}
	for _, surface := range in.Coverage.Surfaces {
		if surface.ID == "agent_identity" && surface.State == CoverageAttention {
			doc.Warnings = append(doc.Warnings, "agent identity: "+surface.Detail)
			doc.Fixes = append(doc.Fixes, "ndev --json session pressure identity show --live")
			break
		}
	}

	// ok: monitor healthy and no pending recovery; observe-only is fine for ok.
	doc.OK = health.MonitorHealthy && in.RecoveryErr == nil && !in.HasRecovery
	return doc
}

// softLaunchNoiseSuppressed is true when soft/observe mode would only produce
// empty-queue or green-host noise rather than real saturation signal.
func softLaunchNoiseSuppressed(policy Policy, hostLevel Level, queueDepth int, rawWouldBlock bool) bool {
	if !rawWouldBlock {
		return false
	}
	// Only suppress when host is normal and queue is empty — cannot be real soft block.
	if hostLevel == LevelNormal && queueDepth == 0 {
		return true
	}
	// Observe-only + host normal + queue empty waiters already covered.
	if !policy.EnforceAdmission && hostLevel == LevelNormal && queueDepth == 0 {
		return true
	}
	return false
}

// LoadPressureDoctorFromDir loads resident state and builds the doctor envelope.
func LoadPressureDoctorFromDir(ctx context.Context, dir string, repoRoot string) (PressureDoctor, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if dir == "" {
		var err error
		dir, err = DataDir()
		if err != nil {
			return PressureDoctor{}, err
		}
	}
	path := PolicyPath(dir)
	policy, persisted, err := LoadPolicy(path, 0)
	if err != nil {
		return PressureDoctor{}, err
	}
	manager, err := NewLaunchdManager("", dir)
	if err != nil {
		return PressureDoctor{}, err
	}
	statusCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	launchd := manager.Status(statusCtx)
	cancel()
	latest, hasLatest := NewTelemetryStore(dir).ReadLatest()
	_, hasRecovery, recoveryErr := LoadRecoveryHint(dir)
	workCtx, workCancel := context.WithTimeout(ctx, launchQueueProbeTimeout)
	work, workErr := NewWorkCoordinator(dir, policy.WorkLimits).Status(workCtx)
	workCancel()
	coord := NewWorkCoordinator(dir, policy.WorkLimits)
	green := coord.greenExpressWindow()
	health := AssessGuardHealth(time.Now().UTC(), policy, persisted, launchd, latest, hasLatest)
	var trees []AgentTree
	if hasLatest {
		trees = latest.TopAgentTrees
	}
	coverage := AssessCoverageDetailed(CoverageAssessment{
		RepoRoot: repoRoot,
		Policy:   policy,
		Health:   health,
		Trees:    trees,
		Catalog:  ActiveAgentIdentityCatalog(),
	})
	return BuildPressureDoctor(PressureDoctorInput{
		Now:          time.Now().UTC(),
		Dir:          dir,
		Policy:       policy,
		Persisted:    persisted,
		Launchd:      launchd,
		Latest:       latest,
		HasLatest:    hasLatest,
		HasRecovery:  hasRecovery,
		RecoveryErr:  recoveryErr,
		Work:         work,
		WorkErr:      workErr,
		ExpressGreen: green.Active,
		RepoRoot:     repoRoot,
		Coverage:     coverage,
	}), nil
}

// SummarizePressureDoctorCheck is the teammate doctor one-liner.
func SummarizePressureDoctorCheck(doc PressureDoctor) (statusOK bool, detail string, fixes []string) {
	detail = fmt.Sprintf("mode=%s monitor_healthy=%v queue=%d/%d express_green=%v",
		doc.ProtectionMode, doc.Monitor.Healthy, doc.Work.Used, doc.Work.Capacity, doc.Work.ExpressGreen)
	if doc.Host.Source != "" && doc.Host.Source != "none" {
		detail += " host_level=" + string(doc.Host.Level)
	}
	return doc.OK, detail, append([]string(nil), doc.Fixes...)
}
