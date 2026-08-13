package sessionpressurecmd

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nstranquist/session-pressure/internal/operationcontract"
	"github.com/nstranquist/session-pressure/internal/sessionpressure"
)

func TestCmdSessionPressurePolicyForceRestoresTunedDefaults(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("NDEV_SESSION_PRESSURE_HOME", dir)
	policy := sessionpressure.DefaultPolicy(16 * 1024)
	policy.ResourceBudgets.MaxSampleDurationMS = 1234
	policy.EnforceAdmission = true
	policy.AutoShedCritical = true
	path := sessionpressure.PolicyPath(dir)
	if err := sessionpressure.SavePolicy(path, policy); err != nil {
		t.Fatal(err)
	}
	rc, _, stderr := captureMainOutput(t, func() int {
		return cmdSessionPressure(&Flags{JSON: true}, []string{"policy", "init", "--force"})
	})
	if rc != 0 {
		t.Fatalf("policy init --force rc=%d stderr=%s", rc, stderr)
	}
	got, persisted, err := sessionpressure.LoadPolicy(path, 16*1024)
	if err != nil || !persisted {
		t.Fatalf("LoadPolicy persisted=%v err=%v", persisted, err)
	}
	if got.ResourceBudgets.MaxSampleDurationMS != 2000 || got.ResourceBudgets.MaxSampleCPUTimeMS != 50 || got.EnforceAdmission || got.AutoShedCritical {
		t.Fatalf("force init did not restore observe-only defaults: %+v", got)
	}
}

func TestCmdSessionPressureSnapshotKeepsSamplingContextAlive(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(sessionpressure.DataDirEnv, dir)
	installFakePressureSampler(t, func(ctx context.Context, _ *sessionpressure.Sampler, _ sessionpressure.Policy) (sessionpressure.Snapshot, error) {
		if err := ctx.Err(); err != nil {
			t.Fatalf("snapshot received canceled context: %v", err)
		}
		return sessionpressure.Snapshot{Level: sessionpressure.LevelNormal}, nil
	})

	rc, _, stderr := captureMainOutput(t, func() int {
		return cmdSessionPressure(&Flags{JSON: true}, []string{"snapshot"})
	})
	if rc != 0 {
		t.Fatalf("snapshot rc=%d stderr=%s", rc, stderr)
	}
}

func TestCmdSessionPressureMonitorOnceKeepsSamplingContextAlive(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(sessionpressure.DataDirEnv, dir)
	installFakePressureMonitorOnce(t, func(ctx context.Context, _ *sessionpressure.Sampler, _ *sessionpressure.TelemetryStore, _ sessionpressure.Policy) (sessionpressure.Snapshot, error) {
		if err := ctx.Err(); err != nil {
			t.Fatalf("monitor once received canceled context: %v", err)
		}
		return sessionpressure.Snapshot{Level: sessionpressure.LevelNormal}, nil
	})

	rc, _, stderr := captureMainOutput(t, func() int {
		return cmdSessionPressure(&Flags{JSON: true}, []string{"monitor", "once"})
	})
	if rc != 0 {
		t.Fatalf("monitor once rc=%d stderr=%s", rc, stderr)
	}
}

func TestCmdSessionPressureEnableRequiresLiveMonitor(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("NDEV_SESSION_PRESSURE_HOME", dir)
	fake := &fakePressureLaunchdController{status: sessionpressure.LaunchdStatus{OK: true, Installed: true, Loaded: true, PID: 77}}
	installFakePressureLaunchd(t, fake)
	rc, _, stderr := captureMainOutput(t, func() int {
		return cmdSessionPressure(&Flags{JSON: true}, []string{"policy", "enable"})
	})
	if rc != 0 || fake.ensureCalls != 1 {
		t.Fatalf("enable rc=%d ensure_calls=%d stderr=%s", rc, fake.ensureCalls, stderr)
	}
	policy, persisted, err := sessionpressure.LoadPolicy(sessionpressure.PolicyPath(dir), 16*1024)
	if err != nil || !persisted || !policy.EnforceAdmission || !policy.AutoShedCritical {
		t.Fatalf("enabled policy=%+v persisted=%v err=%v", policy, persisted, err)
	}
}

func TestCmdSessionPressureEnableFailureFallsBackToAdmissionOnly(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("NDEV_SESSION_PRESSURE_HOME", dir)
	fake := &fakePressureLaunchdController{ensureErr: errors.New("fixture launchd failure")}
	installFakePressureLaunchd(t, fake)
	rc, _, _ := captureMainOutput(t, func() int {
		return cmdSessionPressure(&Flags{JSON: true}, []string{"policy", "enable"})
	})
	if rc != 1 {
		t.Fatalf("enable failure rc=%d, want 1", rc)
	}
	policy, persisted, err := sessionpressure.LoadPolicy(sessionpressure.PolicyPath(dir), 16*1024)
	if err != nil || !persisted || !policy.EnforceAdmission || policy.AutoShedCritical {
		t.Fatalf("fallback policy=%+v persisted=%v err=%v", policy, persisted, err)
	}
}

func TestCmdSessionPressureObserveReinstallsWhenGracefulReloadFails(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("NDEV_SESSION_PRESSURE_HOME", dir)
	policy := sessionpressure.DefaultPolicy(16 * 1024)
	policy.EnforceAdmission = true
	policy.AutoShedCritical = true
	if err := sessionpressure.SavePolicy(sessionpressure.PolicyPath(dir), policy); err != nil {
		t.Fatal(err)
	}
	fake := &fakePressureLaunchdController{
		status:     sessionpressure.LaunchdStatus{OK: true, Installed: true, Loaded: true, PID: 77},
		restartErr: errors.New("fixture signal failure"),
	}
	installFakePressureLaunchd(t, fake)
	rc, _, stderr := captureMainOutput(t, func() int {
		return cmdSessionPressure(&Flags{JSON: true}, []string{"policy", "observe"})
	})
	if rc != 0 || fake.restartCalls != 1 || fake.installCalls != 1 {
		t.Fatalf("observe rc=%d restart_calls=%d install_calls=%d stderr=%s", rc, fake.restartCalls, fake.installCalls, stderr)
	}
	got, persisted, err := sessionpressure.LoadPolicy(sessionpressure.PolicyPath(dir), 16*1024)
	if err != nil || !persisted || got.EnforceAdmission || got.AutoShedCritical {
		t.Fatalf("observe fallback policy=%+v persisted=%v err=%v", got, persisted, err)
	}
}

func TestCmdSessionPressureUninstallLeavesObserveOnlyPolicy(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("NDEV_SESSION_PRESSURE_HOME", dir)
	policy := sessionpressure.DefaultPolicy(16 * 1024)
	policy.EnforceAdmission = true
	policy.AutoShedCritical = true
	if err := sessionpressure.SavePolicy(sessionpressure.PolicyPath(dir), policy); err != nil {
		t.Fatal(err)
	}
	fake := &fakePressureLaunchdController{status: sessionpressure.LaunchdStatus{OK: true, Installed: true, Loaded: true, PID: 77}}
	installFakePressureLaunchd(t, fake)
	rc, _, stderr := captureMainOutput(t, func() int {
		return cmdSessionPressure(&Flags{JSON: true}, []string{"monitor", "uninstall"})
	})
	if rc != 0 || fake.uninstallCalls != 1 {
		t.Fatalf("uninstall rc=%d calls=%d stderr=%s", rc, fake.uninstallCalls, stderr)
	}
	got, persisted, err := sessionpressure.LoadPolicy(sessionpressure.PolicyPath(dir), 16*1024)
	if err != nil || !persisted || got.EnforceAdmission || got.AutoShedCritical {
		t.Fatalf("uninstall policy=%+v persisted=%v err=%v", got, persisted, err)
	}
	admission := WorkHostAdmissionCheck()
	if !admission.Allowed || admission.Source != "observe-only" {
		t.Fatalf("uninstalled guard still enforced admission: %+v", admission)
	}
}

func TestCmdSessionPressureUninstallFailureReloadsObserveOnlyPolicy(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("NDEV_SESSION_PRESSURE_HOME", dir)
	policy := sessionpressure.DefaultPolicy(16 * 1024)
	policy.EnforceAdmission = true
	policy.AutoShedCritical = true
	if err := sessionpressure.SavePolicy(sessionpressure.PolicyPath(dir), policy); err != nil {
		t.Fatal(err)
	}
	fake := &fakePressureLaunchdController{
		status:       sessionpressure.LaunchdStatus{OK: true, Installed: true, Loaded: true, PID: 78},
		uninstallErr: errors.New("fixture bootout failure"),
	}
	installFakePressureLaunchd(t, fake)
	rc, _, stderr := captureMainOutput(t, func() int {
		return cmdSessionPressure(&Flags{JSON: true}, []string{"monitor", "uninstall"})
	})
	if rc != 1 || fake.restartCalls != 1 {
		t.Fatalf("uninstall rc=%d restart_calls=%d stderr=%s", rc, fake.restartCalls, stderr)
	}
	got, persisted, err := sessionpressure.LoadPolicy(sessionpressure.PolicyPath(dir), 16*1024)
	if err != nil || !persisted || got.EnforceAdmission || got.AutoShedCritical {
		t.Fatalf("failed uninstall policy=%+v persisted=%v err=%v", got, persisted, err)
	}
}

func TestCmdSessionPressureEnforcingInstallFailureFallsBackToAdmissionOnly(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("NDEV_SESSION_PRESSURE_HOME", dir)
	fake := &fakePressureLaunchdController{
		status:     sessionpressure.LaunchdStatus{OK: true, Installed: true, Loaded: true, PID: 78},
		installErr: errors.New("fixture bootstrap failure"),
	}
	installFakePressureLaunchd(t, fake)
	rc, _, stderr := captureMainOutput(t, func() int {
		return cmdSessionPressure(&Flags{JSON: true}, []string{"monitor", "install", "--enforce"})
	})
	if rc != 1 || fake.restartCalls != 1 {
		t.Fatalf("install rc=%d restart_calls=%d stderr=%s", rc, fake.restartCalls, stderr)
	}
	got, persisted, err := sessionpressure.LoadPolicy(sessionpressure.PolicyPath(dir), 16*1024)
	if err != nil || !persisted || !got.EnforceAdmission || got.AutoShedCritical {
		t.Fatalf("failed install policy=%+v persisted=%v err=%v", got, persisted, err)
	}
}

func TestCmdSessionPressureEnforcedReinstallFailurePreservesPolicy(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("NDEV_SESSION_PRESSURE_HOME", dir)
	policy := sessionpressure.DefaultPolicy(16 * 1024)
	policy.EnforceAdmission = true
	policy.AutoShedCritical = true
	if err := sessionpressure.SavePolicy(sessionpressure.PolicyPath(dir), policy); err != nil {
		t.Fatal(err)
	}
	fake := &fakePressureLaunchdController{
		status:     sessionpressure.LaunchdStatus{OK: true, Installed: true, Loaded: true, PID: 78},
		installErr: errors.New("fixture concurrent install failure"),
	}
	installFakePressureLaunchd(t, fake)
	rc, _, stderr := captureMainOutput(t, func() int {
		return cmdSessionPressure(&Flags{JSON: true}, []string{"monitor", "install"})
	})
	if rc != 1 || fake.restartCalls != 0 {
		t.Fatalf("install rc=%d restart_calls=%d stderr=%s", rc, fake.restartCalls, stderr)
	}
	got, persisted, err := sessionpressure.LoadPolicy(sessionpressure.PolicyPath(dir), 16*1024)
	if err != nil || !persisted || !got.EnforceAdmission || !got.AutoShedCritical {
		t.Fatalf("failed reinstall weakened policy=%+v persisted=%v err=%v", got, persisted, err)
	}
}

func TestCmdSessionPressureRepeatedInstallPreservesHealthyResident(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("NDEV_SESSION_PRESSURE_HOME", dir)
	policy := sessionpressure.DefaultPolicy(16 * 1024)
	policy.EnforceAdmission = true
	policy.AutoShedCritical = true
	if err := sessionpressure.SavePolicy(sessionpressure.PolicyPath(dir), policy); err != nil {
		t.Fatal(err)
	}
	fake := &fakePressureLaunchdController{status: sessionpressure.LaunchdStatus{OK: true, Installed: true, Loaded: true, PID: 78}}
	installFakePressureLaunchd(t, fake)
	rc, _, stderr := captureMainOutput(t, func() int {
		return cmdSessionPressure(&Flags{JSON: true}, []string{"monitor", "install", "--enforce"})
	})
	if rc != 0 || fake.installCalls != 1 || fake.restartCalls != 0 {
		t.Fatalf("repeat install rc=%d install_calls=%d restart_calls=%d stderr=%s", rc, fake.installCalls, fake.restartCalls, stderr)
	}
}

func TestCmdSessionPressureEnforceRestartsPreservedResidentForPolicyReload(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("NDEV_SESSION_PRESSURE_HOME", dir)
	policy := sessionpressure.DefaultPolicy(16 * 1024)
	if err := sessionpressure.SavePolicy(sessionpressure.PolicyPath(dir), policy); err != nil {
		t.Fatal(err)
	}
	fake := &fakePressureLaunchdController{status: sessionpressure.LaunchdStatus{OK: true, Installed: true, Loaded: true, PID: 78}}
	installFakePressureLaunchd(t, fake)
	rc, _, stderr := captureMainOutput(t, func() int {
		return cmdSessionPressure(&Flags{JSON: true}, []string{"monitor", "install", "--enforce"})
	})
	if rc != 0 || fake.installCalls != 1 || fake.restartCalls != 1 {
		t.Fatalf("enforcing install rc=%d install_calls=%d restart_calls=%d stderr=%s", rc, fake.installCalls, fake.restartCalls, stderr)
	}
}

func TestCmdSessionPressureInstallReloadsPolicyAfterTransactionLock(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("NDEV_SESSION_PRESSURE_HOME", dir)
	initial := sessionpressure.DefaultPolicy(16 * 1024)
	initial.EnforceAdmission = false
	initial.AutoShedCritical = false
	if err := sessionpressure.SavePolicy(sessionpressure.PolicyPath(dir), initial); err != nil {
		t.Fatal(err)
	}
	installFakePressureLaunchd(t, &fakePressureLaunchdController{status: sessionpressure.LaunchdStatus{OK: true, Installed: true, Loaded: true, PID: 78}})
	installFakePressurePolicyMutationLock(t, func(context.Context, string, time.Duration) (func(), error) {
		// Model another process committing enforcement immediately before this
		// installer acquires the transaction. The installer must reload this
		// state under the lock instead of writing its stale initial snapshot.
		current, persisted, err := sessionpressure.LoadPolicy(sessionpressure.PolicyPath(dir), 16*1024)
		if err != nil || !persisted {
			t.Fatalf("load concurrent policy: persisted=%v err=%v", persisted, err)
		}
		current.EnforceAdmission = true
		current.AutoShedCritical = true
		if err := sessionpressure.SavePolicy(sessionpressure.PolicyPath(dir), current); err != nil {
			t.Fatal(err)
		}
		return func() {}, nil
	})
	rc, _, stderr := captureMainOutput(t, func() int {
		return cmdSessionPressure(&Flags{JSON: true}, []string{"monitor", "install"})
	})
	if rc != 0 {
		t.Fatalf("install rc=%d stderr=%s", rc, stderr)
	}
	got, persisted, err := sessionpressure.LoadPolicy(sessionpressure.PolicyPath(dir), 16*1024)
	if err != nil || !persisted || !got.EnforceAdmission || !got.AutoShedCritical {
		t.Fatalf("install overwrote concurrent enforcement: policy=%+v persisted=%v err=%v", got, persisted, err)
	}
}

func TestCmdSessionPressureStatusReportsResidentHealthWhenLiveSampleFails(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("NDEV_SESSION_PRESSURE_HOME", dir)
	policy := sessionpressure.DefaultPolicy(16 * 1024)
	if err := sessionpressure.SavePolicy(sessionpressure.PolicyPath(dir), policy); err != nil {
		t.Fatal(err)
	}
	digest := strings.Repeat("a", 64)
	latest := sessionpressure.Snapshot{
		SchemaVersion: 1, Timestamp: time.Now().UTC(), Level: sessionpressure.LevelNormal,
		ProcessInventoryAvailable: true, ProcessInventoryCapturedAt: time.Now().UTC(),
		GuardPID: 77, GuardRole: "resident", GuardBinarySHA256: digest,
		GuardBudgetApplicable: true, GuardBudgetOK: true, GuardBaselineProven: true,
		MonitorSamples: 4, NormalMonitorSamples: 4,
	}
	if err := sessionpressure.NewTelemetryStore(dir).WriteLatest(latest); err != nil {
		t.Fatal(err)
	}
	fake := &fakePressureLaunchdController{status: sessionpressure.LaunchdStatus{
		OK: true, Installed: true, Loaded: true, PID: 77,
		ArtifactPresent: true, ArtifactVerified: true, ArtifactSHA256: digest,
	}}
	installFakePressureLaunchd(t, fake)
	installFakePressureSampler(t, func(context.Context, *sessionpressure.Sampler, sessionpressure.Policy) (sessionpressure.Snapshot, error) {
		return sessionpressure.Snapshot{}, errors.New("fixture live sample failure")
	})
	rc, stdout, stderr := captureMainOutput(t, func() int {
		return cmdSessionPressure(&Flags{JSON: true}, []string{"status", "--live"})
	})
	if rc != 1 {
		t.Fatalf("status rc=%d want=1 stderr=%s", rc, stderr)
	}
	var payload struct {
		SampleError string                      `json:"sample_error"`
		Snapshot    *sessionpressure.Snapshot   `json:"snapshot"`
		Health      sessionpressure.GuardHealth `json:"health"`
	}
	if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
		t.Fatalf("decode status: %v\n%s", err, stdout)
	}
	if payload.SampleError != "fixture live sample failure" || payload.Snapshot != nil || !payload.Health.MonitorHealthy {
		t.Fatalf("partial status did not preserve resident health: %+v", payload)
	}
}

func TestCmdSessionPressureStatusDoesNotSampleUnlessLiveIsExplicit(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("NDEV_SESSION_PRESSURE_HOME", dir)
	policy := sessionpressure.DefaultPolicy(16 * 1024)
	if err := sessionpressure.SavePolicy(sessionpressure.PolicyPath(dir), policy); err != nil {
		t.Fatal(err)
	}
	digest := strings.Repeat("b", 64)
	latest := sessionpressure.Snapshot{
		SchemaVersion: 1, Timestamp: time.Now().UTC(), Level: sessionpressure.LevelNormal,
		ProcessInventoryAvailable: true, ProcessInventoryCapturedAt: time.Now().UTC(),
		GuardPID: 78, GuardRole: "resident", GuardBinarySHA256: digest,
		GuardBudgetApplicable: true, GuardBudgetOK: true, GuardBaselineProven: true,
		MonitorSamples: 4, NormalMonitorSamples: 4,
	}
	if err := sessionpressure.NewTelemetryStore(dir).WriteLatest(latest); err != nil {
		t.Fatal(err)
	}
	installFakePressureLaunchd(t, &fakePressureLaunchdController{status: sessionpressure.LaunchdStatus{
		OK: true, Installed: true, Loaded: true, PID: 78,
		ArtifactPresent: true, ArtifactVerified: true, ArtifactSHA256: digest,
	}})
	sampleCalls := 0
	installFakePressureSampler(t, func(context.Context, *sessionpressure.Sampler, sessionpressure.Policy) (sessionpressure.Snapshot, error) {
		sampleCalls++
		return sessionpressure.Snapshot{}, errors.New("default status invoked a live process scan")
	})
	rc, stdout, stderr := captureMainOutput(t, func() int {
		return cmdSessionPressure(&Flags{JSON: true}, []string{"status"})
	})
	if rc != 0 || stderr != "" || sampleCalls != 0 {
		t.Fatalf("status rc=%d sample_calls=%d stderr=%q\n%s", rc, sampleCalls, stderr, stdout)
	}
	var payload struct {
		OK          bool                        `json:"ok"`
		SampleError string                      `json:"sample_error"`
		Health      sessionpressure.GuardHealth `json:"health"`
	}
	if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
		t.Fatal(err)
	}
	if !payload.OK || payload.SampleError != "" || !payload.Health.MonitorHealthy {
		t.Fatalf("unexpected persisted status: %+v", payload)
	}
	var compact map[string]json.RawMessage
	if err := json.Unmarshal([]byte(stdout), &compact); err != nil {
		t.Fatal(err)
	}
	if len(stdout) >= 8192 || len(compact["policy"]) != 0 || len(compact["policy_path"]) != 0 || string(compact["output_scope"]) != `"compact"` {
		t.Fatalf("default status was not compact (%d bytes): %s", len(stdout), stdout)
	}
	rc, fullStdout, stderr := captureMainOutput(t, func() int {
		return cmdSessionPressure(&Flags{JSON: true}, []string{"status", "--full"})
	})
	if rc != 0 || stderr != "" || sampleCalls != 0 {
		t.Fatalf("full status rc=%d sample_calls=%d stderr=%q\n%s", rc, sampleCalls, stderr, fullStdout)
	}
	var full map[string]json.RawMessage
	if err := json.Unmarshal([]byte(fullStdout), &full); err != nil {
		t.Fatal(err)
	}
	if len(full["policy"]) == 0 || len(full["policy_path"]) == 0 || string(full["output_scope"]) != `"full"` || !strings.Contains(string(full["coverage"]), `"surfaces"`) {
		t.Fatalf("full status omitted diagnostics: %s", fullStdout)
	}
}

func TestCmdSessionPressureJSONConformsToGeneratedOutputContract(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	pressureDir := filepath.Join(home, "pressure")
	t.Setenv("NDEV_SESSION_PRESSURE_HOME", pressureDir)
	if err := sessionpressure.NewTelemetryStore(pressureDir).AppendAction(sessionpressure.Action{
		Timestamp: time.Now().UTC(), Kind: "graceful_tree_shed", Level: sessionpressure.LevelCritical,
		RootPID: 42, Agent: "codex", Result: "revalidation_rejected", Reason: "fixture",
	}); err != nil {
		t.Fatal(err)
	}
	fixtureSample := func(context.Context, *sessionpressure.Sampler, sessionpressure.Policy) (sessionpressure.Snapshot, error) {
		return sessionpressure.Snapshot{
			SchemaVersion: 1, Timestamp: time.Now().UTC(), Level: sessionpressure.LevelNormal,
			FreePercent: 50, PhysicalMemoryMB: 16 * 1024, ProcessCount: 1,
			ProcessInventoryAvailable: true, ProcessInventoryFresh: true,
			TopAgentTrees: []sessionpressure.AgentTree{}, GuardPID: os.Getpid(),
			GuardRole: "operator", GuardBudgetOK: true, SampleDurationMS: 1, SampleCPUTimeMS: 1,
		}, nil
	}
	installFakePressureSampler(t, fixtureSample)
	installFakePressureMonitorOnce(t, func(ctx context.Context, sampler *sessionpressure.Sampler, store *sessionpressure.TelemetryStore, policy sessionpressure.Policy) (sessionpressure.Snapshot, error) {
		snapshot, err := fixtureSample(ctx, sampler, policy)
		if err != nil {
			return sessionpressure.Snapshot{}, err
		}
		if err := store.AppendEvent(sessionpressure.TelemetryEvent{Timestamp: snapshot.Timestamp, Event: "manual", Snapshot: &snapshot}); err != nil {
			return sessionpressure.Snapshot{}, err
		}
		snapshot.TelemetryBytesToday = store.BytesForDay(snapshot.Timestamp)
		return snapshot, nil
	})
	commands := [][]string{
		{"pressure", "policy", "init"},
		{"pressure", "policy", "show"},
		{"pressure", "snapshot"},
		{"pressure", "check"},
		{"pressure", "monitor", "once"},
		{"pressure", "status"},
		{"pressure", "recovery"},
		{"pressure", "telemetry", "--since", "1h", "--limit", "1"},
		{"pressure", "idle", "--min-age", "300h", "--limit", "1"},
		{"pressure", "storage", "status"},
		{"pressure", "storage", "providers"},
		{"pressure", "storage", "plan", "--target-free", "50GiB"},
		{"pressure", "storage", "apply", "--provider", "pnpm-store"},
		{"pressure", "storage", "history", "--since", "1h", "--limit", "1"},
		{"pressure", "work", "status"},
		{"pressure", "monitor", "status"},
	}
	for _, args := range commands {
		t.Run(strings.Join(args[1:], "_"), func(t *testing.T) {
			rc, stdout, stderr := captureMainOutput(t, func() int {
				return cmdSessionPressure(&Flags{JSON: true}, args[1:])
			})
			wantRC := 0
			if slices.Equal(args, []string{"pressure", "status"}) || slices.Equal(args, []string{"pressure", "monitor", "status"}) {
				wantRC = 1 // The isolated fixture intentionally has no resident LaunchAgent.
			}
			if rc != wantRC {
				t.Fatalf("cmdSession(%v) rc=%d want=%d stderr=%s", args, rc, wantRC, stderr)
			}
			if err := operationcontract.ValidateOutputJSON(operationcontract.All(), "nicos.session.pressure-result.v1", []byte(stdout)); err != nil {
				t.Fatalf("cmdSession(%v) output contract: %v\n%s", args, err, stdout)
			}
		})
	}
}
