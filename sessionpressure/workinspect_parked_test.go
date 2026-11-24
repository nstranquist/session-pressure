package sessionpressure

import (
	"strings"
	"testing"
	"time"
)

func TestParseOpenLockPathsFiltersAndCompacts(t *testing.T) {
	sample := strings.Join([]string{
		"p1234",
		"f5",
		"n/tmp/work/build-cache/install-handoff.lock",
		"f6",
		"n/tmp/work/regular-source.go",
		"f7",
		"n/var/run/service/daemon.lock",
		"f8",
		"n/tmp/work/build-cache/install-handoff.lock",
		"f9",
		"nrelative/not-absolute.lock",
	}, "\n")
	got := parseOpenLockPaths(sample)
	if len(got) != 2 || got[0] != "build-cache/install-handoff.lock" || got[1] != "service/daemon.lock" {
		t.Fatalf("unexpected open lock paths: %+v", got)
	}
}

func TestClassifyStallIdentifiesParkedLockHolderAndParkedIdle(t *testing.T) {
	parkedAt := time.Date(2026, 11, 24, 18, 0, 0, 0, time.UTC)
	lease := &WorkLeaseStatus{OperationID: "0123456789abcdef0123456789abcdef", Class: WorkClassBuild, Parked: true, ParkedAt: &parkedAt}

	withLock := &WorkProcessSnapshot{PID: 42, Alive: true, State: "S", CPUPercent: 0.0, OpenLocks: []string{"ndev/install-handoff.lock"}}
	cal := WorkInspectCalibration{Class: WorkClassBuild, AgeMS: (20 * time.Minute).Milliseconds(), ParkedDurationMS: (15 * time.Minute).Milliseconds()}
	if got := classifyStallWithLease(withLock, lease, cal); got != StallParkedLock {
		t.Fatalf("expected %s, got %s", StallParkedLock, got)
	}

	withoutLock := &WorkProcessSnapshot{PID: 43, Alive: true, State: "S", CPUPercent: 0.0}
	if got := classifyStallWithLease(withoutLock, lease, cal); got != StallParkedIdle {
		t.Fatalf("expected %s, got %s", StallParkedIdle, got)
	}

	shortPark := WorkInspectCalibration{Class: WorkClassBuild, AgeMS: (3 * time.Minute).Milliseconds(), ParkedDurationMS: (2 * time.Minute).Milliseconds()}
	if got := classifyStallWithLease(withoutLock, lease, shortPark); got != StallUnknown {
		t.Fatalf("short parked lease without locks should remain %s, got %s", StallUnknown, got)
	}

	report := WorkInspectReport{
		OperationID: lease.OperationID,
		Lease:       lease,
		Process:     withLock,
		Origin:      WorkInspectOrigin{Kind: "agent", AgentKind: "codex"},
		Calibration: cal,
		StallClass:  StallParkedLock,
	}
	summary := inspectSummary(report)
	if !strings.Contains(summary, "stall=parked_lock_holder") || !strings.Contains(summary, "locks=ndev/install-handoff.lock") {
		t.Fatalf("summary missing lock-holder detail: %s", summary)
	}
	brief := formatInspectBrief(report)
	if !strings.Contains(brief, "- open_locks: ndev/install-handoff.lock") || !strings.Contains(brief, "- parked_for: 15m0s") {
		t.Fatalf("brief missing lock-holder detail:\n%s", brief)
	}
}
