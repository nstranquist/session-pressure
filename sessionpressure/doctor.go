package sessionpressure

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/nstranquist/session-pressure/frontdoor"
)

// PressureDoctor is the compact agent/operator health envelope for
// `session-pressure doctor`. It composes resident evidence only by default
// (no mandatory live sample or long self-test wait).
type PressureDoctor struct {
	SchemaVersion      int                      `json:"schema_version"`
	OK                 bool                     `json:"ok"`
	ProtectionMode     string                   `json:"protection_mode"`
	PolicyPersisted    bool                     `json:"policy_persisted"`
	EnforceAdmission   bool                     `json:"enforce_admission"`
	AutoShedCritical   bool                     `json:"auto_shed_critical"`
	Monitor            DoctorMonitor            `json:"monitor"`
	Host               DoctorHost               `json:"host"`
	Work               DoctorWork               `json:"work"`
	LaunchSoftPressure DoctorLaunch             `json:"launch_soft_pressure"`
	CoverageStatus     string                   `json:"coverage_status,omitempty"`
	SourceRevisions    *ArtifactSourceRevisions `json:"source_revisions,omitempty"`
	SourceLag          *SourceLag               `json:"source_lag,omitempty"`
	FrontDoor          *frontdoor.Status        `json:"front_door,omitempty"`
	Fixes              []string                 `json:"fixes,omitempty"`
	Warnings           []string                 `json:"warnings,omitempty"`
	SwapDerate         *WorkSwapDerateCost      `json:"swap_derate,omitempty"`
	Health             GuardHealth              `json:"health"`
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
	// MemoryAdmissionEnforced and SwapThrashEnforced show the memory guards,
	// which have their own switch (work_limits.memory_admission) and can be
	// on while enforce_admission is off.
	// Admission is what decides starts: "resource" or "weighted".
	Admission               string `json:"admission"`
	MemoryAdmissionEnforced bool   `json:"memory_admission_enforced"`
	SwapThrashEnforced      bool   `json:"swap_thrash_enforced"`
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
	// Optional: executing-binary source revisions (artifact status probe).
	SourceRevisions *ArtifactSourceRevisions
	// Optional: installed-vs-source-HEAD commit lag.
	SourceLag *SourceLag
	// Optional: the recorded front door (frontdoor.Inspect).
	FrontDoor *frontdoor.Status
	// Optional: 24h swap-thrash derate cost. Loaded only when the latch is
	// set or held in the window, so a calm host pays nothing for it.
	SwapDerate *WorkSwapDerateCost
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

			Admission:               workAdmissionMode(in.Policy.WorkLimits),
			MemoryAdmissionEnforced: WorkMemoryAdmissionEnforced(in.Policy),
			SwapThrashEnforced:      WorkSwapThrashEnforced(in.Policy),
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
		doc.Fixes = append(doc.Fixes, "session-pressure policy init")
	}
	if !health.MonitorHealthy {
		if monitorNeedsInstallOrRestart(in.Launchd, in.Latest, in.HasLatest, health) {
			doc.Fixes = append(doc.Fixes, "session-pressure monitor install")
			doc.Fixes = append(doc.Fixes, "make build")
		} else {
			doc.Fixes = append(doc.Fixes, "session-pressure --json status --live")
		}
	}
	if in.HasRecovery {
		doc.Fixes = append(doc.Fixes, "session-pressure recovery clear  # after reviewing unclean shutdown")
	}
	if catalog := ActiveAgentIdentityCatalog(); catalog != nil && catalog.OverlayError != "" {
		doc.Warnings = append(doc.Warnings, "agent identity overlay failed closed: "+catalog.OverlayError)
		doc.Fixes = append(doc.Fixes, "fix ~/.nicos-dev/session-pressure/agent-identity.json or remove it; session-pressure identity show")
	}
	doc.Warnings = append(doc.Warnings, swapThrashDoctorWarnings(in)...)
	doc.Warnings = append(doc.Warnings, memoryFamilyDoctorWarnings(in)...)
	doc.SwapDerate = in.SwapDerate
	for _, surface := range in.Coverage.Surfaces {
		if surface.ID == "agent_identity" && surface.State == CoverageAttention {
			doc.Warnings = append(doc.Warnings, "agent identity: "+surface.Detail)
			doc.Fixes = append(doc.Fixes, "session-pressure --json identity show --live")
			break
		}
	}

	if revisions := in.SourceRevisions; revisions != nil {
		copied := *revisions
		doc.SourceRevisions = &copied
		if revisions.Mismatch {
			// A warning, not a failure: the resident still protects the host,
			// but reports and policy commands may run older code than it.
			doc.Warnings = append(doc.Warnings, fmt.Sprintf("session-pressure binaries disagree on source revision (cli=%s front_door=%s helper=%s)",
				ShortRevision(revisions.CLI), ShortRevision(revisions.FrontDoor), ShortRevision(revisions.InstalledHelper)))
			doc.Fixes = append(doc.Fixes, PromoteCommand())
		}
	}
	if lag := in.SourceLag; lag != nil {
		copied := *lag
		doc.SourceLag = &copied
		if lag.Checked && (lag.Status == "behind" || lag.Status == "diverged") {
			if lag.Detail != "" {
				doc.Warnings = append(doc.Warnings, lag.Detail)
			}
			if lag.Remediation != "" {
				doc.Fixes = append(doc.Fixes, lag.Remediation)
			}
		}
	}

	if in.FrontDoor != nil {
		copied := *in.FrontDoor
		doc.FrontDoor = &copied
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
	var swapDerate *WorkSwapDerateCost
	if cost, ok := loadDoctorSwapDerate(dir, latest, hasLatest, time.Now().UTC()); ok {
		swapDerate = &cost
	}
	revisions := InspectArtifactSourceRevisions(dir)
	installedRev := revisions.InstalledHelper
	if installedRev == "" {
		installedRev = revisions.CLI
	}
	sourceLag := InspectSourceLag(ctx, installedRev, repoRoot)
	return BuildPressureDoctor(PressureDoctorInput{
		SwapDerate:      swapDerate,
		Now:             time.Now().UTC(),
		Dir:             dir,
		Policy:          policy,
		Persisted:       persisted,
		Launchd:         launchd,
		Latest:          latest,
		HasLatest:       hasLatest,
		HasRecovery:     hasRecovery,
		RecoveryErr:     recoveryErr,
		Work:            work,
		WorkErr:         workErr,
		ExpressGreen:    green.Active,
		RepoRoot:        repoRoot,
		Coverage:        coverage,
		SourceRevisions: &revisions,
		SourceLag:       &sourceLag,
		FrontDoor:       frontdoor.Inspect(dir),
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

// swapThrashDoctorWarnings explains a live storm and an over-budget derate.
// An external storm names the largest non-agent memory users: closing them is
// the only fix, because agent waits cannot end it.
func swapThrashDoctorWarnings(in PressureDoctorInput) []string {
	warnings := make([]string, 0, 2)
	if in.HasLatest && in.Latest.SwapIOAvailable && in.Latest.SwapThrashLatched {
		since := ""
		if !in.Latest.SwapThrashSince.IsZero() {
			since = fmt.Sprintf(" for %s", in.Now.Sub(in.Latest.SwapThrashSince).Round(time.Second))
		}
		if in.Latest.SwapThrashCause == SwapThrashCauseExternal {
			warning := fmt.Sprintf("swap storm%s (%.0f MB/s swap-out) is outside agent work; heavy-work derate is lifted", since, in.Latest.SwapOutMBPerSecond)
			if consumers := topNonAgentMemoryConsumers(in.Latest.TopHostConsumers, 3); consumers != "" {
				warning += "; largest non-agent memory: " + consumers
			}
			warnings = append(warnings, warning)
		} else {
			cause := in.Latest.SwapThrashCause
			if cause == "" {
				cause = "unknown"
			}
			effect := fmt.Sprintf("new heavy work is serialized to swap_thrash_capacity=%d", normalizeWorkLimits(in.Policy.WorkLimits).SwapThrashCapacity)
			if in.Policy.Enabled && in.Policy.WorkLimits.ResourceAdmission() {
				effect = "new heavy work waits while other leases run (resource admission)"
			} else if !WorkSwapThrashEnforced(in.Policy) {
				effect = "report-only (enforce_admission and memory_admission=enforce are both off)"
			}
			warnings = append(warnings, fmt.Sprintf("swap thrash%s (%.0f MB/s swap-out, cause=%s): %s",
				since, in.Latest.SwapOutMBPerSecond, cause, effect))
		}
	}
	if in.SwapDerate != nil && in.SwapDerate.Verdict == SwapDerateOverBudget {
		warnings = append(warnings, "swap derate over budget (24h): "+in.SwapDerate.Advice)
	}
	return warnings
}

// memoryFamilyDoctorWarnings names what holds memory when memory blocks new
// work, so the reader frees the largest reclaimable family first.
func memoryFamilyDoctorWarnings(in PressureDoctorInput) []string {
	if !in.HasLatest || len(in.Latest.MemoryFamilies) == 0 {
		return nil
	}
	if !EvaluateMemoryPressure(in.Latest, in.Policy).Level.AtLeast(in.Policy.BlockNewAt) {
		return nil
	}
	warning := "memory blocks new work; top memory families: " + memoryFamiliesText(in.Latest.MemoryFamilies, 3)
	if top, ok := TopReclaimableMemoryFamily(in.Latest.MemoryFamilies); ok {
		warning += fmt.Sprintf("; %.0f MB of %s restarts on demand and is safe to stop", top.ReclaimableMB, top.Family)
	}
	return []string{warning}
}

func topNonAgentMemoryConsumers(consumers []HostConsumer, limit int) string {
	ordered := make([]HostConsumer, 0, len(consumers))
	for _, consumer := range consumers {
		if consumer.Category != "agent" && consumer.RSSSumMB > 0 {
			ordered = append(ordered, consumer)
		}
	}
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].RSSSumMB > ordered[j].RSSSumMB })
	parts := make([]string, 0, limit)
	for _, consumer := range ordered[:min(limit, len(ordered))] {
		parts = append(parts, fmt.Sprintf("%s %.1f GB", consumer.Executable, consumer.RSSSumMB/1024))
	}
	return strings.Join(parts, ", ")
}

// loadDoctorSwapDerate reads 24h of history only when the latch is set now or
// held in the window. The episode scan decodes marker rows only.
func loadDoctorSwapDerate(dir string, latest Snapshot, hasLatest bool, now time.Time) (WorkSwapDerateCost, bool) {
	since := now.Add(-24 * time.Hour)
	episodes, err := ReadSwapThrashEpisodes(dir, since, now)
	if err != nil {
		return WorkSwapDerateCost{}, false
	}
	if episodes.Count == 0 && !(hasLatest && latest.SwapThrashLatched) {
		return WorkSwapDerateCost{}, false
	}
	events, err := NewWorkEventStore(dir).Read(WorkEventFilter{Since: since})
	if err != nil {
		return WorkSwapDerateCost{}, false
	}
	cost := summarizeSwapDerate(events, since, now)
	relateSwapDerateToAllWait(&cost, summarizeWaitAttribution(events))
	cost.AttachEpisodes(episodes)
	return cost, true
}

func monitorNeedsInstallOrRestart(launchd LaunchdStatus, latest Snapshot, hasLatest bool, health GuardHealth) bool {
	if !launchd.Installed || !launchd.Loaded || !launchd.OK || !launchd.ArtifactVerified || !health.LatestMonitorFresh || !hasLatest {
		return true
	}
	if latest.GuardPID == 0 || latest.GuardPID != launchd.PID {
		return true
	}
	if launchd.ArtifactSHA256 != "" && latest.GuardBinarySHA256 != launchd.ArtifactSHA256 {
		return true
	}
	return false
}
