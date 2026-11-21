package helpermain

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nstranquist/session-pressure/sessionpressure"
)

func TestNotifyQueueStallQueuesWorkToast(t *testing.T) {
	inbox := filepath.Join(t.TempDir(), "notifications", "inbox.jsonl")
	t.Setenv("NDEV_TOAST_INBOX", inbox)
	err := notifyQueueStall(sessionpressure.QueueStall{
		DurationMS:        35000,
		Used:              8,
		EffectiveCapacity: 24,
		Class:             sessionpressure.WorkClassBuild,
		Weight:            5,
		DecisionReason:    "protected_bounded_drain",
	})
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(inbox)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "ndev-pressure://work") || !strings.Contains(string(body), "Work queue stalled") {
		t.Fatalf("unexpected stall toast: %s", body)
	}
}

func TestConfigureMonitorWiresAllResidentCallbacks(t *testing.T) {
	dir := t.TempDir()
	policy := sessionpressure.DefaultPolicy(16 * 1024)
	sampler := sessionpressure.NewResidentSampler()
	store := sessionpressure.NewTelemetryStore(dir)
	coord := sessionpressure.NewWorkCoordinator(dir, policy.WorkLimits)
	monitor := configureMonitor(dir, sampler, store, policy, coord, 2)
	if monitor.WorkCoordinator != coord {
		t.Fatal("WorkCoordinator not wired")
	}
	if monitor.QueueStallNotifier == nil {
		t.Fatal("QueueStallNotifier not wired")
	}
	if monitor.DiskObserver == nil || monitor.DiskNotifier == nil {
		t.Fatal("DiskObserver/DiskNotifier not wired")
	}
	if monitor.ProcessGuard == nil || monitor.ProcessGuard.Notify == nil {
		t.Fatal("ProcessGuard.Notify not wired")
	}
	if !monitor.EnforceBrowsers {
		t.Fatal("resident must enforce host browser control")
	}
}

// work run writes the wrapper's exit report and keeps the path away from the
// wrapped command. A refused argument is session-pressure's own status.
func TestRunWorkCommandReportsOwnUsageError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "exit-report")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(sessionpressure.WorkExitReportEnv, path)
	if code := runWorkCommand([]string{"--no-such-flag"}); code != 2 {
		t.Fatalf("code=%d", code)
	}
	if _, set := os.LookupEnv(sessionpressure.WorkExitReportEnv); set {
		t.Fatal("report path left in the environment")
	}
	report, ok, err := sessionpressure.ReadWorkExitReport(path)
	if err != nil || !ok || report.Source != sessionpressure.WorkExitSourceSessionPressure || !report.AppliesTo(2) || report.AppliesTo(0) {
		t.Fatalf("report=%+v ok=%v err=%v", report, ok, err)
	}
}
