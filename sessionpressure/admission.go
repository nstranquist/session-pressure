package sessionpressure

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

type AgentLaunchKind string

const (
	AgentLaunchNew          AgentLaunchKind = "new"
	AgentLaunchResume       AgentLaunchKind = "resume"
	launchQueueProbeTimeout                 = 500 * time.Millisecond
)

// ConfiguredAdmission applies the persisted admission policy to a bounded live
// host probe. Missing or observe-only policy fails open. Probe failures retain
// enforcement only when recent resident evidence exists; otherwise they fail
// open visibly so the guard cannot become a host-wide availability dependency.
func ConfiguredAdmission(ctx context.Context) Admission {
	return configuredAdmission(ctx, AdmissionForSnapshot)
}

// ConfiguredWorkHostAdmission applies the work-only CPU corroboration rule
// while keeping the canonical launch admission contract unchanged.
func ConfiguredWorkHostAdmission(ctx context.Context) Admission {
	return configuredAdmission(ctx, AdmissionForWorkSnapshot)
}

// ConfiguredAgentLaunchAdmission composes memory-gated host pressure with cheap
// work-queue backpressure for canonical agent hosts. Saturated queues and
// CPU-red throttle heavy builds via work admission without refusing chat
// launches when RAM is still healthy (kernel-panic risk stays memory-gated).
func ConfiguredAgentLaunchAdmission(ctx context.Context, kind AgentLaunchKind) Admission {
	host := configuredAdmission(ctx, AdmissionForAgentLaunchSnapshot)
	if !host.Allowed {
		return host
	}
	if kind != AgentLaunchNew && kind != AgentLaunchResume {
		host.Warning = joinAdmissionWarning(host.Warning, fmt.Sprintf("unknown agent launch kind %q; queue admission failed open", kind))
		return host
	}
	dir, err := DataDir()
	if err != nil {
		host.Warning = joinAdmissionWarning(host.Warning, "work-queue admission failed open: "+err.Error())
		return host
	}
	policy, persisted, err := LoadPolicy(PolicyPath(dir), 0)
	if err != nil || !persisted {
		if err != nil {
			host.Warning = joinAdmissionWarning(host.Warning, "work-queue admission failed open: "+err.Error())
		}
		return host
	}
	launch := policy.LaunchAdmission
	if !policy.Enabled || launch.Mode == LaunchAdmissionModeOff {
		return host
	}
	if ctx == nil {
		ctx = context.Background()
	}
	queueCtx, cancel := context.WithTimeout(ctx, launchQueueProbeTimeout)
	status, statusErr := NewWorkCoordinator(dir, policy.WorkLimits).Status(queueCtx)
	cancel()
	if statusErr != nil {
		host.Warning = joinAdmissionWarning(host.Warning, "work-queue admission failed open: "+statusErr.Error())
		return host
	}
	return AgentLaunchAdmissionForQueue(host, policy, status, kind)
}

// AgentLaunchAdmissionFromSnapshot evaluates launch admission against a caller's
// existing snapshot instead of taking a new host sample.
//
// ConfiguredAgentLaunchAdmission probes the host live on every call, which is
// correct for a launch gate and is the single dominant cost of any composite
// read that includes admission — measured at ~300ms, more than the rest of a
// board put together. A display surface already holding a snapshot has no
// reason to pay it again, and paying it produced a worse answer as well as a
// slower one: the read would show resident numbers everywhere and a
// live-probed admission beside them, two different instants presented as one.
//
// The returned Source records which snapshot the decision came from, so a
// display read can never be mistaken for a launch gate. Callers gating an
// actual launch must keep using ConfiguredAgentLaunchAdmission.
func AgentLaunchAdmissionFromSnapshot(
	snapshot Snapshot,
	policy Policy,
	persisted bool,
	status WorkStatus,
	kind AgentLaunchKind,
	source string,
) Admission {
	if strings.TrimSpace(source) == "" {
		source = "resident"
	}
	// Mirror the gating ConfiguredAgentLaunchAdmission applies before it
	// samples, so the two paths agree on when admission is even in force.
	if !persisted || !policy.Enabled || !policy.EnforceAdmission {
		return Admission{Allowed: true, Level: LevelNormal, Source: "observe-only"}
	}
	host := AdmissionForAgentLaunchSnapshot(snapshot, policy, source)
	if !host.Allowed {
		return host
	}
	if kind != AgentLaunchNew && kind != AgentLaunchResume {
		host.Warning = joinAdmissionWarning(host.Warning, fmt.Sprintf("unknown agent launch kind %q; queue admission failed open", kind))
		return host
	}
	if policy.LaunchAdmission.Mode == LaunchAdmissionModeOff {
		return host
	}
	return AgentLaunchAdmissionForQueue(host, policy, status, kind)
}

// ResidentTrustMaxAge is the age bound for reusing a resident monitor snapshot
// without a live SampleHost. Matches the inventory trust window already used by
// admissionAfterSampleFailure (2× normal sample interval + 15s).
func ResidentTrustMaxAge(policy Policy) time.Duration {
	sec := policy.SampleIntervalSeconds
	if sec < 5 {
		sec = 5
	}
	return time.Duration(sec*2+15) * time.Second
}

// ResidentSnapshotIsFresh reports whether snapshot.Timestamp is within the
// resident trust window relative to now.
func ResidentSnapshotIsFresh(snapshot Snapshot, policy Policy, now time.Time) bool {
	if snapshot.Timestamp.IsZero() {
		return false
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	age := now.Sub(snapshot.Timestamp)
	maxAge := ResidentTrustMaxAge(policy)
	return age >= -5*time.Second && age <= maxAge
}

// AdmissionFromFreshResident evaluates admit against a resident snapshot when
// it is trust-fresh. ok is false when the caller must live-sample.
// Source is always "resident-fresh" so display/launch paths can distinguish
// reuse from a live probe. Red/block from the resident snapshot is preserved
// (never upgraded to allow).
func AdmissionFromFreshResident(
	snapshot Snapshot,
	policy Policy,
	now time.Time,
	admit func(Snapshot, Policy, string) Admission,
) (Admission, bool) {
	if admit == nil {
		admit = AdmissionForSnapshot
	}
	if !ResidentSnapshotIsFresh(snapshot, policy, now) {
		return Admission{}, false
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	sanitized := sanitizeResidentFallback(snapshot, policy, now)
	out := admit(sanitized, policy, "resident-fresh")
	snapCopy := sanitized
	out.Snapshot = &snapCopy
	return out, true
}

func configuredAdmission(ctx context.Context, admit func(Snapshot, Policy, string) Admission) Admission {
	if admit == nil {
		admit = AdmissionForSnapshot
	}
	dir, err := DataDir()
	if err != nil {
		return Admission{Allowed: true, Level: LevelNormal, Source: "fail-open", Warning: err.Error()}
	}
	path := PolicyPath(dir)
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return Admission{Allowed: true, Level: LevelNormal, Source: "unconfigured"}
	}
	policy, persisted, err := LoadPolicy(path, 0)
	if err != nil {
		return Admission{Allowed: true, Level: LevelNormal, Source: "fail-open", Warning: err.Error()}
	}
	if !persisted || !policy.Enabled || !policy.EnforceAdmission {
		return Admission{Allowed: true, Level: LevelNormal, Source: "observe-only"}
	}
	var latest *Snapshot
	if value, ok := NewTelemetryStore(dir).ReadLatest(); ok {
		// Harness-tax slice: when the resident monitor already has a trust-fresh
		// snapshot, skip SampleHost (~300ms) and admit from resident evidence.
		// Red remains red; stale/missing resident falls through to live probe.
		if out, ok := AdmissionFromFreshResident(value, policy, time.Now().UTC(), admit); ok {
			return out
		}
		age := time.Since(value.Timestamp)
		maxAge := ResidentTrustMaxAge(policy)
		if age >= -5*time.Second && age <= maxAge {
			latest = &value
		}
	}
	if ctx == nil {
		ctx = context.Background()
	}
	probeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	snapshot, sampleErr := NewSampler().SampleHost(probeCtx, policy, latest)
	if sampleErr != nil {
		fallback := admissionAfterSampleFailure(sampleErr, latest, policy)
		if fallback.Snapshot == nil {
			return fallback
		}
		out := admit(*fallback.Snapshot, policy, fallback.Source)
		out.Warning = joinAdmissionWarning(fallback.Warning, out.Warning)
		return out
	}
	source := "live-host-probe"
	if snapshot.ProcessInventoryAvailable {
		source += "+monitor-inventory"
	}
	return admit(snapshot, policy, source)
}

// AgentLaunchAdmissionForQueue is the deterministic policy seam used by the
// live launcher and replayable tests.
func AgentLaunchAdmissionForQueue(host Admission, policy Policy, status WorkStatus, kind AgentLaunchKind) Admission {
	launch := policy.LaunchAdmission
	oldestWaitMS := int64(0)
	if len(status.Waiters) > 0 {
		oldestWaitMS = status.Waiters[0].WaitMS
	}
	oldestThresholdMS := int64(time.Duration(launch.OldestWaitBlockSeconds) * time.Second / time.Millisecond)
	wouldBlock := status.QueueDepth >= launch.QueueDepthBlock || oldestWaitMS >= oldestThresholdMS
	// Idle-green noise suppression: empty queue or host-normal with no waiters
	// cannot be real soft saturation — do not warn or elevate level.
	if softLaunchNoiseSuppressed(policy, host.Level, status.QueueDepth, wouldBlock) {
		wouldBlock = false
	}
	evidence := &WorkQueueAdmissionEvidence{
		Capacity: status.Capacity, Used: status.Used, QueueDepth: status.QueueDepth,
		OldestWaitMilliseconds: oldestWaitMS, QueueDepthBlock: launch.QueueDepthBlock,
		OldestWaitBlockMS: oldestThresholdMS, WouldBlock: wouldBlock, Enforced: policy.EnforceAdmission,
	}
	host.WorkQueue = evidence
	if !wouldBlock {
		return host
	}
	reasons := make([]string, 0, 2)
	if status.QueueDepth >= launch.QueueDepthBlock {
		reasons = append(reasons, fmt.Sprintf("work queue depth %d >= new-launch threshold %d", status.QueueDepth, launch.QueueDepthBlock))
	}
	if oldestWaitMS >= oldestThresholdMS {
		reasons = append(reasons, fmt.Sprintf("oldest work waiter %s >= new-launch threshold %s", time.Duration(oldestWaitMS)*time.Millisecond, time.Duration(oldestThresholdMS)*time.Millisecond))
	}
	reason := strings.Join(reasons, "; ")
	// Observe-only: keep WorkQueue evidence but do not paint host red when
	// the host level is still normal (saturation is queue-only soft pressure).
	if !policy.EnforceAdmission {
		if host.Level != LevelNormal {
			host.Level = maxLevel(host.Level, LevelRed)
		}
		host.Dimension = "work_queue"
		host.Source = joinAdmissionSource(host.Source, "work-queue")
		host.Warning = joinAdmissionWarning(host.Warning, "work queue would block a new agent launch while admission is observe-only: "+reason)
		return host
	}
	host.Dimension = "work_queue"
	host.Source = joinAdmissionSource(host.Source, "work-queue")
	// Memory-healthy launches: queue saturation warns instead of hard-blocking.
	// Chat hosts should keep working; the work coordinator still serializes
	// heavy builds/tests under CPU/memory pressure.
	if host.Allowed && !host.Level.AtLeast(LevelRed) {
		host.Warning = joinAdmissionWarning(host.Warning, "work queue is saturated; agent launch allowed with warning: "+reason)
		return host
	}
	host.Level = maxLevel(host.Level, LevelRed)
	if kind == AgentLaunchResume && launch.ResumeBehavior == LaunchResumeBehaviorWarn {
		host.Warning = joinAdmissionWarning(host.Warning, "work queue is saturated; explicit resume allowed by policy: "+reason)
		return host
	}
	host.Allowed = false
	host.Reasons = append(host.Reasons, reason)
	return host
}

func joinAdmissionWarning(existing, added string) string {
	if strings.TrimSpace(existing) == "" {
		return added
	}
	if strings.TrimSpace(added) == "" {
		return existing
	}
	return existing + "; " + added
}

func joinAdmissionSource(existing, added string) string {
	if strings.TrimSpace(existing) == "" {
		return added
	}
	if strings.TrimSpace(added) == "" || strings.Contains(existing, added) {
		return existing
	}
	return existing + "+" + added
}

func admissionAfterSampleFailure(sampleErr error, latest *Snapshot, policy Policy) Admission {
	if latest != nil {
		now := time.Now().UTC()
		if ResidentSnapshotIsFresh(*latest, policy, now) {
			fallback := sanitizeResidentFallback(*latest, policy, now)
			admission := AdmissionForSnapshot(fallback, policy, "resident-fallback")
			admission.Warning = "live pressure sampling unavailable; recent resident evidence remained enforceable: " + sampleErr.Error()
			return admission
		}
	}
	return Admission{
		Allowed: true,
		Level:   LevelNormal,
		Source:  "fail-open",
		Warning: "pressure sampling unavailable and no recent resident evidence was available; admission failed open: " + sampleErr.Error(),
	}
}

func sanitizeResidentFallback(snapshot Snapshot, policy Policy, now time.Time) Snapshot {
	capturedAt := snapshot.ProcessInventoryCapturedAt
	if capturedAt.IsZero() {
		capturedAt = snapshot.Timestamp
	}
	age := now.Sub(capturedAt)
	maxAge := time.Duration(policy.SampleIntervalSeconds*2+15) * time.Second
	if !snapshot.ProcessInventoryAvailable || age < -5*time.Second || age > maxAge {
		snapshot.ProcessCount = 0
		snapshot.ProcessRSSSumMB = 0
		snapshot.AgentTreeCount = 0
		snapshot.AgentRSSSumMB = 0
		snapshot.AgentCPUPercent = 0
		snapshot.AgentCPUAvailable = false
		snapshot.CoordinatedWork = CoordinatedWorkSnapshot{ByClass: []CoordinatedWorkClassUsage{}}
		snapshot.TopAgentTrees = []AgentTree{}
		snapshot.TopHostConsumers = []HostConsumer{}
		snapshot.ProcessInventoryAvailable = false
		snapshot.ProcessInventoryFresh = false
		snapshot.ProcessInventoryAgeSeconds = max(0, age.Seconds())
	}
	return snapshot
}
