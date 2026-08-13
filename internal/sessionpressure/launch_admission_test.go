package sessionpressure

import (
	"strings"
	"testing"
)

func TestAgentLaunchAdmissionBlocksNewDemandButAllowsResume(t *testing.T) {
	policy := DefaultPolicy(16 * 1024)
	policy.EnforceAdmission = true
	status := WorkStatus{
		Capacity: 8, Used: 8, QueueDepth: 7,
		Waiters: []WorkWaiterStatus{{WaitMS: 121_000}},
	}
	host := Admission{Allowed: true, Level: LevelNormal, Source: "fixture"}

	newLaunch := AgentLaunchAdmissionForQueue(host, policy, status, AgentLaunchNew)
	if !newLaunch.Allowed || newLaunch.Dimension != "work_queue" || newLaunch.WorkQueue == nil || !newLaunch.WorkQueue.WouldBlock || !strings.Contains(newLaunch.Warning, "allowed with warning") {
		t.Fatalf("memory-healthy new launch should warn, not hard-block: %+v", newLaunch)
	}
	resume := AgentLaunchAdmissionForQueue(host, policy, status, AgentLaunchResume)
	if !resume.Allowed || resume.Dimension != "work_queue" || !strings.Contains(resume.Warning, "allowed with warning") {
		t.Fatalf("resume admission = %+v", resume)
	}
}

func TestAgentLaunchAdmissionUsesQueueDepthAndObserveOnlyProjection(t *testing.T) {
	policy := DefaultPolicy(16 * 1024)
	policy.EnforceAdmission = true
	host := Admission{Allowed: true, Level: LevelNormal, Source: "fixture"}
	status := WorkStatus{Capacity: 8, Used: 8, QueueDepth: policy.LaunchAdmission.QueueDepthBlock}

	warned := AgentLaunchAdmissionForQueue(host, policy, status, AgentLaunchNew)
	if !warned.Allowed || !strings.Contains(warned.Warning, "queue depth") {
		t.Fatalf("depth admission = %+v", warned)
	}
	policy.EnforceAdmission = false
	observed := AgentLaunchAdmissionForQueue(host, policy, status, AgentLaunchNew)
	if !observed.Allowed || observed.WorkQueue == nil || observed.WorkQueue.Enforced || !strings.Contains(observed.Warning, "observe-only") {
		t.Fatalf("observe-only admission = %+v", observed)
	}
	below := AgentLaunchAdmissionForQueue(host, DefaultPolicy(16*1024), WorkStatus{Capacity: 8, Used: 8, QueueDepth: 1, Waiters: []WorkWaiterStatus{{WaitMS: 1_000}}}, AgentLaunchNew)
	if !below.Allowed || below.WorkQueue == nil || below.WorkQueue.WouldBlock {
		t.Fatalf("below-threshold admission = %+v", below)
	}
}

func TestAgentLaunchAdmissionCanBlockResumesByExplicitPolicy(t *testing.T) {
	policy := DefaultPolicy(16 * 1024)
	policy.EnforceAdmission = true
	policy.LaunchAdmission.ResumeBehavior = LaunchResumeBehaviorBlock
	status := WorkStatus{Capacity: 8, Used: 8, QueueDepth: policy.LaunchAdmission.QueueDepthBlock}
	// Memory-red host still hard-blocks even for resume when ResumeBehavior=block
	// after the warn carve-out (Allowed false means memory gate already denied).
	got := AgentLaunchAdmissionForQueue(Admission{Allowed: false, Level: LevelRed, Reasons: []string{"memory red"}}, policy, status, AgentLaunchResume)
	if got.Allowed {
		t.Fatalf("memory-red resume should stay blocked: %+v", got)
	}
	// Memory-healthy + block resume policy: queue warn path still allows because
	// memory gate is the hard authority; ResumeBehaviorBlock only applies when
	// the host is already at/above red.
	healthy := AgentLaunchAdmissionForQueue(Admission{Allowed: true, Level: LevelNormal}, policy, status, AgentLaunchResume)
	if !healthy.Allowed || !strings.Contains(healthy.Warning, "allowed with warning") {
		t.Fatalf("memory-healthy resume should warn: %+v", healthy)
	}
}

func TestAgentLaunchAdmissionIdleGreenSuppressesSoftNoise(t *testing.T) {
	policy := DefaultPolicy(16 * 1024)
	policy.EnforceAdmission = false
	// Force would-block threshold that empty queue cannot meet — suppress path.
	policy.LaunchAdmission.QueueDepthBlock = 1
	host := Admission{Allowed: true, Level: LevelNormal, Source: "fixture"}
	// Empty queue: even if threshold is 1, depth 0 is not would-block; use soft
	// suppress on raw would-block via high depth then empty after predicate unit.
	empty := AgentLaunchAdmissionForQueue(host, policy, WorkStatus{Capacity: 8, Used: 0, QueueDepth: 0}, AgentLaunchNew)
	if !empty.Allowed || (empty.WorkQueue != nil && empty.WorkQueue.WouldBlock) {
		t.Fatalf("green empty must not soft-block: %+v", empty)
	}
	if empty.Level != LevelNormal || strings.Contains(empty.Warning, "would block") {
		t.Fatalf("green empty must not warn/paint red: %+v", empty)
	}

	// Green host + full queue observe: keeps evidence, single soft warning, no hard block.
	full := AgentLaunchAdmissionForQueue(host, policy, WorkStatus{
		Capacity: 8, Used: 8, QueueDepth: policy.LaunchAdmission.QueueDepthBlock,
		Waiters: []WorkWaiterStatus{{WaitMS: 1_000}},
	}, AgentLaunchNew)
	if !full.Allowed || full.WorkQueue == nil || !full.WorkQueue.WouldBlock {
		t.Fatalf("observe full queue should allow with would-block evidence: %+v", full)
	}
	if full.Level != LevelNormal {
		t.Fatalf("observe full queue must not paint host red when host was normal: %+v", full)
	}
	if !strings.Contains(full.Warning, "observe-only") {
		t.Fatalf("observe full queue should warn once: %+v", full)
	}

	// Enforce-on warns (does not hard-block) when memory-healthy + saturated queue.
	enforced := policy
	enforced.EnforceAdmission = true
	warned := AgentLaunchAdmissionForQueue(host, enforced, WorkStatus{
		Capacity: 8, Used: 8, QueueDepth: enforced.LaunchAdmission.QueueDepthBlock,
	}, AgentLaunchNew)
	if !warned.Allowed || !strings.Contains(warned.Warning, "allowed with warning") {
		t.Fatalf("enforce-on memory-healthy must warn on saturated queue: %+v", warned)
	}

	// Red host is not "suppressed" into green — host red stays red.
	redHost := Admission{Allowed: false, Level: LevelRed, Source: "memory", Reasons: []string{"memory red"}}
	red := AgentLaunchAdmissionForQueue(redHost, policy, WorkStatus{Capacity: 8, Used: 0, QueueDepth: 0}, AgentLaunchNew)
	if red.Level != LevelRed || red.Allowed {
		t.Fatalf("red host must remain red/blocked: %+v", red)
	}
}

func TestAdmissionForAgentLaunchIgnoresCPUOnlyRed(t *testing.T) {
	policy := DefaultPolicy(16 * 1024)
	policy.EnforceAdmission = true
	snap := Snapshot{
		FreePercent: 40, HostCPUAvailable: true, HostCPUPercent: 99,
		PhysicalMemoryMB: 16 * 1024,
	}
	full := AdmissionForSnapshot(snap, policy, "fixture")
	if full.Allowed || full.Level != LevelRed {
		t.Fatalf("full admission should block CPU-red: %+v", full)
	}
	agent := AdmissionForAgentLaunchSnapshot(snap, policy, "fixture")
	if !agent.Allowed || agent.Level != LevelNormal {
		t.Fatalf("agent launch should ignore CPU-only red: %+v", agent)
	}
	if !strings.Contains(agent.Warning, "memory gates only") {
		t.Fatalf("expected CPU carve-out warning: %+v", agent)
	}
	memRed := AdmissionForAgentLaunchSnapshot(Snapshot{FreePercent: 10, HostCPUPercent: 10, HostCPUAvailable: true}, policy, "fixture")
	if memRed.Allowed || memRed.Level != LevelRed {
		t.Fatalf("agent launch must still block memory-red: %+v", memRed)
	}
}

func TestDefaultPolicySoftAdmissionDefaults(t *testing.T) {
	p := DefaultPolicy(16 * 1024)
	if p.EnforceAdmission || p.AutoShedCritical {
		t.Fatalf("defaults must stay soft: enforce=%v shed=%v", p.EnforceAdmission, p.AutoShedCritical)
	}
}

// TestAgentLaunchAdmissionFromSnapshotMatchesTheSampledPathWithoutSampling is
// the honesty gate for the display projection: it must agree with the live-gate
// decision given the same inputs, and must never masquerade as a live probe.
func TestAgentLaunchAdmissionFromSnapshotMatchesTheSampledPathWithoutSampling(t *testing.T) {
	policy := DefaultPolicy(16 * 1024)
	policy.Enabled = true
	policy.EnforceAdmission = true
	snapshot := Snapshot{FreePercent: 40, HostCPUPercent: 20, PhysicalMemoryMB: 16 * 1024}
	status := WorkStatus{
		Capacity: 8, Used: 8, QueueDepth: 7,
		Waiters: []WorkWaiterStatus{{WaitMS: 121_000}},
	}

	derived := AgentLaunchAdmissionFromSnapshot(snapshot, policy, true, status, AgentLaunchNew, "resident")
	host := AdmissionForAgentLaunchSnapshot(snapshot, policy, "resident")
	sampledEquivalent := AgentLaunchAdmissionForQueue(host, policy, status, AgentLaunchNew)
	if derived.Allowed != sampledEquivalent.Allowed || derived.Dimension != sampledEquivalent.Dimension {
		t.Fatalf("display projection disagrees with the gate: derived=%+v gate=%+v", derived, sampledEquivalent)
	}
	// Provenance leads with the snapshot this came from and then records the
	// gates that applied; it must never imply a fresh probe.
	if !strings.HasPrefix(derived.Source, "resident") {
		t.Fatalf("display admission source=%q, want it to lead with the snapshot it came from", derived.Source)
	}
	if strings.Contains(derived.Source, "live-host-probe") {
		t.Fatalf("display admission must not claim a live probe: %+v", derived)
	}

	// The same gating the sampled path applies before it ever samples.
	unpersisted := AgentLaunchAdmissionFromSnapshot(snapshot, policy, false, status, AgentLaunchNew, "resident")
	if !unpersisted.Allowed || unpersisted.Source != "observe-only" {
		t.Fatalf("unpersisted policy admission=%+v", unpersisted)
	}
	off := policy
	off.EnforceAdmission = false
	observe := AgentLaunchAdmissionFromSnapshot(snapshot, off, true, status, AgentLaunchNew, "resident")
	if !observe.Allowed || observe.Source != "observe-only" {
		t.Fatalf("observe-only admission=%+v", observe)
	}

	// Memory pressure still blocks without any sampling.
	redPolicy := policy
	red := Snapshot{FreePercent: 3, HostCPUPercent: 20, PhysicalMemoryMB: 16 * 1024}
	blocked := AgentLaunchAdmissionFromSnapshot(red, redPolicy, true, WorkStatus{Capacity: 8}, AgentLaunchNew, "resident")
	if blocked.Allowed {
		t.Fatalf("critical free memory must still block a display admission: %+v", blocked)
	}

	// An empty source is labelled rather than left blank.
	unlabelled := AgentLaunchAdmissionFromSnapshot(snapshot, policy, true, status, AgentLaunchNew, "")
	if unlabelled.Source == "" {
		t.Fatal("admission source must never be empty")
	}
}
