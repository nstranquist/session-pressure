package sessionpressure

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// monitor install used to print "installed" as soon as launchd had a PID. A
// promote then read artifact status before the new resident had sampled, and
// reported the helper revision as unknown.
func TestWaitForResidentReportMatchesInstalledArtifactAndPID(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(t.TempDir(), residentHelperName)
	if err := os.WriteFile(source, []byte("helper"), 0o700); err != nil {
		t.Fatal(err)
	}
	installed, err := promoteArtifact(source, dir, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	store := NewTelemetryStore(dir)
	short := func() ResidentReport {
		ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
		defer cancel()
		return WaitForResidentReport(ctx, dir, 4242, 10*time.Millisecond)
	}
	if got := short(); got.Ready || !strings.Contains(got.Detail, "before the deadline") {
		t.Fatalf("no report yet: %+v", got)
	}
	// The previous resident's report names another PID and binary.
	if err := store.WriteLatest(Snapshot{GuardPID: 1111, GuardBinarySHA256: strings.Repeat("e", 64), GuardSourceRevision: "old"}); err != nil {
		t.Fatal(err)
	}
	if got := short(); got.Ready {
		t.Fatalf("the previous resident's report was taken as the new one: %+v", got)
	}
	// Right PID, wrong binary: not the installed artifact.
	if err := store.WriteLatest(Snapshot{GuardPID: 4242, GuardBinarySHA256: strings.Repeat("e", 64)}); err != nil {
		t.Fatal(err)
	}
	if got := short(); got.Ready {
		t.Fatalf("a report from another binary was accepted: %+v", got)
	}
	// The report arrives while the caller waits.
	go func() {
		time.Sleep(40 * time.Millisecond)
		_ = store.WriteLatest(Snapshot{GuardPID: 4242, GuardBinarySHA256: installed.SHA256, GuardSourceRevision: "new-rev"})
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	got := WaitForResidentReport(ctx, dir, 4242, 10*time.Millisecond)
	if !got.Ready || got.SourceRevision != "new-rev" || got.PID != 4242 || got.WaitedMS >= 5000 {
		t.Fatalf("resident report: %+v", got)
	}
}

func TestWaitForResidentReportReturnsAtOnceWithoutEvidence(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	started := time.Now()
	if got := WaitForResidentReport(ctx, t.TempDir(), 0, 0); got.Ready || !strings.Contains(got.Detail, "no resident pid") {
		t.Fatalf("no pid: %+v", got)
	}
	if got := WaitForResidentReport(ctx, t.TempDir(), 7, 0); got.Ready || !strings.Contains(got.Detail, "manifest") {
		t.Fatalf("no manifest: %+v", got)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("missing evidence must not wait for the deadline: %s", elapsed)
	}
}
