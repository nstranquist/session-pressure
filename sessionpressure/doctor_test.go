package sessionpressure

import (
	"strings"
	"testing"
	"time"
)

func healthyDoctorFixtures(now time.Time) (Policy, LaunchdStatus, Snapshot) {
	policy := DefaultPolicy(16 * 1024)
	policy.EnforceAdmission = false
	policy.AutoShedCritical = false
	policy.Enabled = true
	digest := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	launchd := LaunchdStatus{
		OK: true, Loaded: true, PID: 42, Installed: true,
		ArtifactPresent: true, ArtifactVerified: true, ArtifactSHA256: digest,
	}
	latest := Snapshot{
		Level: LevelNormal, Timestamp: now,
		GuardRole: "resident", GuardBudgetApplicable: true, GuardBudgetOK: true,
		GuardPID: 42, GuardBinarySHA256: digest,
		ProcessInventoryAvailable: true, ProcessInventoryCapturedAt: now,
		MonitorSamples: 10, NormalMonitorSamples: 10,
		// Keep free memory clearly green for budget evaluation.
		FreePercent: 50, PhysicalMemoryMB: 16 * 1024, HostCPUPercent: 10, HostCPUAvailable: true,
	}
	return policy, launchd, latest
}

func TestBuildPressureDoctorObserveOnlyOKWithoutEnforceOrShed(t *testing.T) {
	now := time.Now().UTC()
	policy, launchd, latest := healthyDoctorFixtures(now)
	doc := BuildPressureDoctor(PressureDoctorInput{
		Now:          now,
		Policy:       policy,
		Persisted:    true,
		Launchd:      launchd,
		Latest:       latest,
		HasLatest:    true,
		Work:         WorkStatus{Capacity: 8, Used: 1, QueueDepth: 0},
		ExpressGreen: true,
		Coverage:     CoverageReport{Status: "ready-with-explicit-boundaries"},
	})
	if !doc.OK {
		t.Fatalf("observe-only with healthy monitor must be ok: %+v warnings=%v", doc, doc.Warnings)
	}
	if doc.EnforceAdmission || doc.AutoShedCritical {
		t.Fatalf("doctor must not invent enforce/shed: %+v", doc)
	}
	if doc.SchemaVersion != 1 || doc.Monitor.Healthy == false || doc.Work.Capacity != 8 {
		t.Fatalf("doctor envelope incomplete: %+v", doc)
	}
	if doc.LaunchSoftPressure.WouldBlock {
		t.Fatalf("empty queue must not soft-block: %+v", doc.LaunchSoftPressure)
	}
}

func TestBuildPressureDoctorFixesForMissingMonitorAndPolicy(t *testing.T) {
	policy := DefaultPolicy(16 * 1024)
	doc := BuildPressureDoctor(PressureDoctorInput{
		Policy:    policy,
		Persisted: false,
		Launchd:   LaunchdStatus{OK: false, Loaded: false},
		Work:      WorkStatus{Capacity: 8},
	})
	if doc.OK {
		t.Fatalf("missing monitor should not be ok: %+v", doc)
	}
	joined := strings.Join(doc.Fixes, "\n")
	if !strings.Contains(joined, "policy init") || !strings.Contains(joined, "monitor install") {
		t.Fatalf("expected policy+monitor fixes, got %v", doc.Fixes)
	}
}

func TestBuildPressureDoctorSoftLaunchNoiseSuppressed(t *testing.T) {
	now := time.Now().UTC()
	policy, launchd, latest := healthyDoctorFixtures(now)
	policy.LaunchAdmission.QueueDepthBlock = 1
	raw := true
	doc := BuildPressureDoctor(PressureDoctorInput{
		Now:            now,
		Policy:         policy,
		Persisted:      true,
		Launchd:        launchd,
		Latest:         latest,
		HasLatest:      true,
		Work:           WorkStatus{Capacity: 8, Used: 0, QueueDepth: 0},
		SoftWouldBlock: &raw,
	})
	if !doc.LaunchSoftPressure.NoiseSuppressed || doc.LaunchSoftPressure.WouldBlock {
		t.Fatalf("idle-green soft pressure should suppress: %+v", doc.LaunchSoftPressure)
	}
}

func TestSoftLaunchNoiseSuppressedPredicate(t *testing.T) {
	policy := DefaultPolicy(16 * 1024)
	if softLaunchNoiseSuppressed(policy, LevelNormal, 0, false) {
		t.Fatal("no raw block → no suppress")
	}
	if !softLaunchNoiseSuppressed(policy, LevelNormal, 0, true) {
		t.Fatal("normal+empty+raw → suppress")
	}
	if softLaunchNoiseSuppressed(policy, LevelNormal, 3, true) {
		t.Fatal("non-empty queue must not suppress")
	}
	if softLaunchNoiseSuppressed(policy, LevelRed, 0, true) {
		t.Fatal("red host must not suppress solely on empty queue noise path for host-red context")
	}
}

func TestSummarizePressureDoctorCheck(t *testing.T) {
	ok, detail, fixes := SummarizePressureDoctorCheck(PressureDoctor{
		OK: true, ProtectionMode: "observe-only",
		Monitor: DoctorMonitor{Healthy: true},
		Work:    DoctorWork{Used: 1, Capacity: 8, ExpressGreen: true},
		Host:    DoctorHost{Level: LevelNormal, Source: "resident"},
		Fixes:   []string{"none"},
	})
	if !ok || !strings.Contains(detail, "observe-only") || !strings.Contains(detail, "host_level=normal") {
		t.Fatalf("summary ok=%v detail=%q", ok, detail)
	}
	if len(fixes) != 1 {
		t.Fatalf("fixes=%v", fixes)
	}
}

func TestBuildPressureDoctorBudgetFailureKeepsLiveStatusFixNotReinstall(t *testing.T) {
	now := time.Now().UTC()
	policy, launchd, latest := healthyDoctorFixtures(now)
	latest.MonitorSamples = 30
	latest.SampleDurationMS = 2500
	latest.SampleDurationP95MS = 2500
	doc := BuildPressureDoctor(PressureDoctorInput{
		Now:       now,
		Policy:    policy,
		Persisted: true,
		Launchd:   launchd,
		Latest:    latest,
		HasLatest: true,
		Work:      WorkStatus{Capacity: 8, Used: 0},
	})
	if doc.OK || doc.Monitor.Healthy {
		t.Fatalf("budget failure should fail monitor health: %+v", doc)
	}
	joined := strings.Join(doc.Fixes, "\n")
	if strings.Contains(joined, "monitor install") || strings.Contains(joined, "make build") {
		t.Fatalf("running verified resident should not suggest monitor install/make build: %v", doc.Fixes)
	}
	if !strings.Contains(joined, "status --live") {
		t.Fatalf("expected live status diagnostic fix, got %v", doc.Fixes)
	}
}

func TestLoadDoctorSwapDerateRelatesToAllWait(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC()
	events := NewWorkEventStore(dir)
	for _, ev := range []WorkEvent{
		{
			Timestamp: now.Add(-2 * time.Minute), Event: WorkEventCompleted,
			OperationID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", LeaseID: "11111111111111111111111111111111", Class: WorkClassBuild, Weight: 5,
			AdmissionDecision: SwapThrashDeferredDecision, PressureDimension: "swap",
			AdmissionWaitMilliseconds: 60_000, WaitMilliseconds: 60_000, RuntimeMillis: 10_000,
		},
		{
			Timestamp: now.Add(-1 * time.Minute), Event: WorkEventCompleted,
			OperationID: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", LeaseID: "22222222222222222222222222222222", Class: WorkClassBuild, Weight: 5,
			QueueWaitMilliseconds: 180_000, WaitMilliseconds: 180_000, RuntimeMillis: 10_000,
		},
	} {
		if err := events.AppendDurable(ev); err != nil {
			t.Fatal(err)
		}
	}
	cost, ok := loadDoctorSwapDerate(dir, Snapshot{SwapThrashLatched: true}, true, now)
	if !ok {
		t.Fatal("expected swap derate cost")
	}
	if cost.WaitTotalMS != 60_000 {
		t.Fatalf("WaitTotalMS=%d want 60000", cost.WaitTotalMS)
	}
	if cost.AllWaitTotalMS != 240_000 {
		t.Fatalf("AllWaitTotalMS=%d want 240000", cost.AllWaitTotalMS)
	}
	if cost.ShareOfAllWait < 0.24 || cost.ShareOfAllWait > 0.26 {
		t.Fatalf("ShareOfAllWait=%v want ~0.25", cost.ShareOfAllWait)
	}
}
