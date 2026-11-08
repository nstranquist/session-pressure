package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	if os.Getenv("PRESSURE_OBSERVATION_TEST_CHILD") == "overflow" {
		_, _ = os.Stdout.WriteString(strings.Repeat("x", 3*1024*1024))
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestObservationBoundsReadOutput(t *testing.T) {
	t.Setenv("PRESSURE_OBSERVATION_TEST_CHILD", "overflow")
	if _, e := readCLI(os.Args[0], "status"); e == nil || !strings.Contains(e.Error(), "bounded read") {
		t.Fatalf("oversized child output was accepted: %v", e)
	}
}

func TestObservationRejectsSuccessfulButUnhealthyStatus(t *testing.T) {
	base := map[string]any{
		"ok": true, "has_latest_monitor": true, "policy_persisted": true,
		"health":                 map[string]any{"monitor_healthy": true, "latest_monitor_fresh": true},
		"latest_monitor_summary": map[string]any{"guard_budget_ok": true},
		"launchd_summary":        map[string]any{"installed": true, "loaded": true, "artifact_verified": true, "pid": 42},
	}
	raw, _ := json.Marshal(base)
	if pid, e := observedResident(raw); e != nil || pid != 42 {
		t.Fatalf("healthy installed resident rejected: %d %v", pid, e)
	}
	for _, field := range []string{"has_latest_monitor", "policy_persisted", "health", "latest_monitor_summary", "launchd_summary"} {
		t.Run(field, func(t *testing.T) {
			var incomplete map[string]any
			_ = json.Unmarshal(raw, &incomplete)
			delete(incomplete, field)
			b, _ := json.Marshal(incomplete)
			if _, e := observedResident(b); e == nil {
				t.Fatal("successful read without required resident evidence accepted")
			}
		})
	}
}

func TestTamperedBundlePreservesInstalledAuthority(t *testing.T) {
	dir := t.TempDir()
	prefix := t.TempDir()
	old := filepath.Join(prefix, "session-pressure")
	if e := os.WriteFile(old, []byte("previous installation"), 0755); e != nil {
		t.Fatal(e)
	}
	m := manifest{Revision: strings.Repeat("a", 40), Files: map[string]string{}}
	for _, n := range names {
		if e := os.WriteFile(filepath.Join(dir, n), []byte("tampered"), 0755); e != nil {
			t.Fatal(e)
		}
		m.Files[n] = strings.Repeat("0", 64)
	}
	if e := writeJSON(filepath.Join(dir, "release.json"), m); e != nil {
		t.Fatal(e)
	}
	if e := install(dir, prefix, "none", false); e == nil {
		t.Fatal("tampered bundle accepted")
	}
	raw, e := os.ReadFile(old)
	if e != nil || string(raw) != "previous installation" {
		t.Fatalf("prior authority changed: %q %v", raw, e)
	}
	if _, e = os.Lstat(filepath.Join(prefix, ".session-pressure-releases", "current")); !os.IsNotExist(e) {
		t.Fatal("failed candidate promoted")
	}
}

func TestObserveRefusesShortSampleAsAcceptance(t *testing.T) {
	if e := observe([]string{"--out", filepath.Join(t.TempDir(), "evidence.json"), "--binary", "/bin/echo", "--samples", "3"}); e == nil {
		t.Fatal("underpowered sample accepted")
	}
}

func TestObservationRestartPreservesPriorEvidence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "evidence.json")
	if e := startObservation(path, observation{Failures: 7}); e != nil {
		t.Fatal(e)
	}
	before, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	if e = observe([]string{"--out", path, "--binary", "/bin/echo"}); e == nil || !strings.Contains(e.Error(), "unused --out") {
		t.Fatalf("restart did not preserve the original run: %v", e)
	}
	after, e := os.ReadFile(path)
	if e != nil || string(after) != string(before) {
		t.Fatalf("prior evidence changed: %q %v", after, e)
	}
}

func TestDarwinReleaseRejectsDisabledNativeReader(t *testing.T) {
	t.Setenv("CGO_ENABLED", "0")
	if _, e := releaseEnvironment("darwin", "arm64"); e == nil {
		t.Fatal("incomplete Darwin release accepted")
	}
	dir := t.TempDir()
	m := manifest{Revision: strings.Repeat("a", 40), OS: "darwin", Arch: "arm64", Files: map[string]string{}}
	for _, name := range names {
		m.Files[name] = strings.Repeat("0", 64)
	}
	if e := writeJSON(filepath.Join(dir, "release.json"), m); e != nil {
		t.Fatal(e)
	}
	if _, e := verify(dir); e == nil || !strings.Contains(e.Error(), "native SMART") {
		t.Fatalf("disabled native feature was not rejected: %v", e)
	}
}

func TestPromotionRejectsStaleIntentAndUnintendedDowngrade(t *testing.T) {
	old := manifest{Revision: strings.Repeat("a", 40), BuiltAt: time.Now().Add(-time.Hour)}
	next := manifest{Revision: strings.Repeat("b", 40), BuiltAt: old.BuiltAt.Add(time.Minute)}
	latest := manifest{Revision: strings.Repeat("c", 40), BuiltAt: next.BuiltAt.Add(time.Minute)}
	if e := checkPromotion(old, nil, "none", false); e != nil {
		t.Fatal(e)
	}
	if e := checkPromotion(next, &old, old.Revision, false); e != nil {
		t.Fatal(e)
	}
	// An install prepared against A must not replace C after another installer wins.
	for _, rollback := range []bool{false, true} {
		if e := checkPromotion(next, &latest, old.Revision, rollback); e == nil {
			t.Fatal("stale promotion accepted")
		}
	}
	if e := checkPromotion(old, &latest, latest.Revision, false); e == nil {
		t.Fatal("unintended downgrade accepted")
	}
	if e := checkPromotion(old, &latest, latest.Revision, true); e != nil {
		t.Fatal(e)
	}
	if e := checkPromotion(latest, &latest, latest.Revision, false); e != nil {
		t.Fatal(e)
	}
	if e := checkPromotion(latest, &old, "", false); e == nil {
		t.Fatal("unpinned update accepted")
	}
	if e := checkPromotion(latest, nil, old.Revision, false); e == nil {
		t.Fatal("missing authority accepted")
	}
	old.BuiltAt = time.Time{}
	if e := checkPromotion(old, nil, "none", false); e == nil {
		t.Fatal("unknown timestamp accepted")
	}
}

func TestAppSampleRejectsMissingAmbiguousAndMalformedEvidence(t *testing.T) {
	for _, tc := range []struct {
		name, discovery, process, want string
		discoveryErr, processErr       error
	}{
		{name: "missing", discoveryErr: errors.New("exit status 1"), want: "app_discovery_failed"},
		{name: "ambiguous", discovery: "42\n43\n", want: "app_discovery_requires_one_pid"},
		{name: "invalid pid", discovery: "-1", want: "app_discovery_invalid_pid"},
		{name: "read failed", discovery: "42", processErr: errors.New("timeout"), want: "app_process_read_failed"},
		{name: "missing rss", discovery: "42", process: "0:13.41", want: "app_process_fields_invalid"},
		{name: "invalid time", discovery: "42", process: "0:NaN 1024", want: "app_cpu_time_invalid"},
		{name: "invalid rss", discovery: "42", process: "0:13.41 -1024", want: "app_rss_invalid"},
		{name: "infinite rss", discovery: "42", process: "0:13.41 +Inf", want: "app_rss_invalid"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, _, err := sampleAppWith(func(name string, args ...string) ([]byte, error) {
				if name == "pgrep" {
					return []byte(tc.discovery), tc.discoveryErr
				}
				return []byte(tc.process), tc.processErr
			})
			if err == nil || err.Error() != tc.want {
				t.Fatalf("got %v, want %s", err, tc.want)
			}
		})
	}
}

func TestAppSampleParsesActualMacOSColumns(t *testing.T) {
	calls := 0
	pid, cpu, rss, err := sampleAppWith(func(name string, args ...string) ([]byte, error) {
		calls++
		if calls == 1 {
			if name != "pgrep" || strings.Join(args, " ") != "-x NDevPressure" {
				t.Fatalf("unexpected discovery: %s %v", name, args)
			}
			return []byte("56895\n"), nil
		}
		if name != "ps" || strings.Join(args, " ") != "-p 56895 -o time= -o rss=" {
			t.Fatalf("unexpected read: %s %v", name, args)
		}
		return []byte("  0:13.41  65792\n"), nil
	})
	if err != nil || pid != 56895 || cpu != 13.41 || rss != 64.25 || calls != 2 {
		t.Fatalf("%d %g %g %v (%d calls)", pid, cpu, rss, err, calls)
	}
}

func TestAppCPUTimeSupportsLongRunningProcessesAndRejectsInvalidValues(t *testing.T) {
	for value, want := range map[string]float64{"0:13.41": 13.41, "100:00": 6000, "01:02:03": 3723, "2-03:04:05.5": 183845.5, "0-01:00:00": 3600} {
		got, err := parseAppCPUTime(value)
		if err != nil || got != want {
			t.Errorf("%q = %g, %v; want %g", value, got, err, want)
		}
	}
	for _, value := range []string{"", "1", "0:60", "1:60:00", "-1:00", "1:NaN", "1:+Inf", "1.5:00", "1-24:00:00", "0-01:00", "1e308:00:00", "1:2:3:4"} {
		if _, err := parseAppCPUTime(value); err == nil {
			t.Errorf("accepted %q", value)
		}
	}
}

func TestAppSampleFailureClearsStaleMetricsAndRetainsProductHealth(t *testing.T) {
	now := time.Now().UTC()
	s := observation{AppPID: 42, AppCPUPercent: 0.5, AppPeakRSSMiB: 90, AppObservedSeconds: 1900, AppBudgetPassed: true, AppSampleOK: true, HealthPassed: true, Checks: 10}
	s.recordAppSampleFailure(now, errors.New("app_process_read_failed"))
	if s.AppPID != 0 || s.AppCPUPercent != 0 || s.AppPeakRSSMiB != 0 || s.AppObservedSeconds != 0 || s.AppBudgetPassed || s.AppSampleOK {
		t.Fatalf("stale metrics retained: %+v", s)
	}
	if s.AppSampleFailures != 1 || s.LastAppSampleError != "app_process_read_failed" || !s.LastAppSampleFailure.Equal(now) || !s.HealthPassed || s.Checks != 10 {
		t.Fatalf("failure evidence or independent product health lost: %+v", s)
	}
	s.recordAppSampleFailure(now.Add(time.Minute), errors.New("app_discovery_failed"))
	if s.AppSampleFailures != 2 {
		t.Fatal("failure history reset")
	}
}
