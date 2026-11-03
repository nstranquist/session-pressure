package sessionpressurecmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nstranquist/session-pressure/sessionpressure"
)

func writeInstalledHelperFixture(t *testing.T, dir string) string {
	t.Helper()
	digest := strings.Repeat("a", 64)
	path := filepath.Join(dir, "artifacts", "sha256-"+digest, "session-pressure-helper")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("helper"), 0o700); err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(sessionpressure.InstalledArtifact{SchemaVersion: 2, SHA256: digest, Path: path, InstalledAt: time.Now().UTC()})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sessionpressure.ArtifactManifestPath(dir), body, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, found, err := sessionpressure.LoadInstalledArtifact(dir); err != nil || !found {
		t.Fatalf("fixture manifest rejected: found=%v err=%v", found, err)
	}
	return digest
}

func TestCmdMonitorInstallWaitsForTheNewResidentsReport(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("NDEV_SESSION_PRESSURE_HOME", dir)
	if err := sessionpressure.SavePolicy(sessionpressure.PolicyPath(dir), sessionpressure.DefaultPolicy(16*1024)); err != nil {
		t.Fatal(err)
	}
	digest := writeInstalledHelperFixture(t, dir)
	if err := sessionpressure.NewTelemetryStore(dir).WriteLatest(sessionpressure.Snapshot{GuardPID: 78, GuardBinarySHA256: digest, GuardSourceRevision: "0123456789abcdef"}); err != nil {
		t.Fatal(err)
	}
	installFakePressureLaunchd(t, &fakePressureLaunchdController{status: sessionpressure.LaunchdStatus{OK: true, Installed: true, Loaded: true, PID: 78}})
	rc, stdout, stderr := captureMainOutput(t, func() int {
		return cmdSessionPressure(&Flags{}, []string{"monitor", "install"})
	})
	if rc != 0 || !strings.Contains(stdout, "resident reporting after") || !strings.Contains(stdout, "(source 0123456789ab)") {
		t.Fatalf("rc=%d stdout=%q stderr=%q", rc, stdout, stderr)
	}
}

func TestCmdMonitorInstallWarnsWhenTheResidentDoesNotReport(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("NDEV_SESSION_PRESSURE_HOME", dir)
	if err := sessionpressure.SavePolicy(sessionpressure.PolicyPath(dir), sessionpressure.DefaultPolicy(16*1024)); err != nil {
		t.Fatal(err)
	}
	writeInstalledHelperFixture(t, dir)
	previous := ResidentReportTimeout
	ResidentReportTimeout = 200 * time.Millisecond
	t.Cleanup(func() { ResidentReportTimeout = previous })
	installFakePressureLaunchd(t, &fakePressureLaunchdController{status: sessionpressure.LaunchdStatus{OK: true, Installed: true, Loaded: true, PID: 79}})
	rc, stdout, stderr := captureMainOutput(t, func() int {
		return cmdSessionPressure(&Flags{JSON: true}, []string{"monitor", "install"})
	})
	if rc != 0 {
		t.Fatalf("a slow first report must warn, not fail: rc=%d stderr=%q", rc, stderr)
	}
	var payload struct {
		ResidentReport sessionpressure.ResidentReport `json:"resident_report"`
	}
	if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
		t.Fatalf("decode %q: %v", stdout, err)
	}
	if payload.ResidentReport.Ready || payload.ResidentReport.PID != 79 || payload.ResidentReport.WaitedMS < 150 {
		t.Fatalf("resident report: %+v", payload.ResidentReport)
	}
}
