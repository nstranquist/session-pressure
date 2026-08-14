package sessionpressure

import (
	"strings"
	"testing"
	"time"
)

func TestResidentSnapshotIsFresh(t *testing.T) {
	policy := DefaultPolicy(16 * 1024)
	now := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
	maxAge := ResidentTrustMaxAge(policy)

	fresh := Snapshot{Timestamp: now.Add(-maxAge / 2)}
	if !ResidentSnapshotIsFresh(fresh, policy, now) {
		t.Fatal("mid-window snapshot must be fresh")
	}
	// At exact max age still fresh (inclusive bound).
	atBound := Snapshot{Timestamp: now.Add(-maxAge)}
	if !ResidentSnapshotIsFresh(atBound, policy, now) {
		t.Fatal("snapshot at trust max age must be fresh")
	}
	stale := Snapshot{Timestamp: now.Add(-maxAge - time.Second)}
	if ResidentSnapshotIsFresh(stale, policy, now) {
		t.Fatal("snapshot past trust window must be stale")
	}
	zero := Snapshot{}
	if ResidentSnapshotIsFresh(zero, policy, now) {
		t.Fatal("zero timestamp must not be fresh")
	}
}

func TestAdmissionFromFreshResidentReusesAndPreservesRedBlock(t *testing.T) {
	policy := DefaultPolicy(16 * 1024)
	policy.EnforceAdmission = true
	now := time.Now().UTC()

	// FreeRedPercent default 15 — free 10% is red/block for host admission.
	redSnap := Snapshot{
		Timestamp:        now.Add(-10 * time.Second),
		FreePercent:      10,
		HostCPUPercent:   20,
		HostCPUAvailable: true,
	}
	out, ok := AdmissionFromFreshResident(redSnap, policy, now, AdmissionForSnapshot)
	if !ok {
		t.Fatal("expected resident-fresh reuse")
	}
	if out.Source != "resident-fresh" {
		t.Fatalf("source=%q want resident-fresh", out.Source)
	}
	if out.Allowed {
		t.Fatalf("red resident must not become allow: %+v", out)
	}
	if !out.Level.AtLeast(LevelRed) {
		t.Fatalf("level=%s want at least red", out.Level)
	}
}

func TestAdmissionFromFreshResidentRejectsStale(t *testing.T) {
	policy := DefaultPolicy(16 * 1024)
	now := time.Now().UTC()
	stale := Snapshot{
		Timestamp:   now.Add(-ResidentTrustMaxAge(policy) - time.Minute),
		FreePercent: 10,
	}
	out, ok := AdmissionFromFreshResident(stale, policy, now, AdmissionForSnapshot)
	if ok {
		t.Fatalf("stale must force live sample path, got %+v", out)
	}
}

func TestAdmissionFromFreshResidentAllowsHealthy(t *testing.T) {
	policy := DefaultPolicy(16 * 1024)
	policy.EnforceAdmission = true
	now := time.Now().UTC()
	okSnap := Snapshot{
		Timestamp:        now.Add(-5 * time.Second),
		FreePercent:      60,
		HostCPUPercent:   30,
		HostCPUAvailable: true,
	}
	out, ok := AdmissionFromFreshResident(okSnap, policy, now, AdmissionForSnapshot)
	if !ok || !out.Allowed {
		t.Fatalf("healthy fresh resident: ok=%v admission=%+v", ok, out)
	}
	if !strings.Contains(out.Source, "resident-fresh") {
		t.Fatalf("source=%q", out.Source)
	}
}

func TestResidentTrustMaxAgeTracksSampleInterval(t *testing.T) {
	p := DefaultPolicy(16 * 1024)
	p.SampleIntervalSeconds = 120
	got := ResidentTrustMaxAge(p)
	want := time.Duration(120*2+15) * time.Second
	if got != want {
		t.Fatalf("trust max age = %v want %v", got, want)
	}
}
