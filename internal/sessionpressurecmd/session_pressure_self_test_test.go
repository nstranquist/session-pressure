package sessionpressurecmd

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nstranquist/session-pressure/internal/operationcontract"
	"github.com/nstranquist/session-pressure/internal/sessionpressure"
)

func installPressureSelfTestProbe(t *testing.T, probe func(context.Context, string, time.Duration) error) {
	t.Helper()
	previousProbe := runPressureSelfTestProbe
	previousResolver := resolvePressureWorkHelper
	runPressureSelfTestProbe = probe
	resolvePressureWorkHelper = func() (string, error) { return "/fixture/immutable-helper", nil }
	t.Cleanup(func() {
		runPressureSelfTestProbe = previousProbe
		resolvePressureWorkHelper = previousResolver
	})
}

func seedReadyPressureSelfTest(t *testing.T) (string, sessionpressure.Policy) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv(sessionpressure.DataDirEnv, dir)
	policy := sessionpressure.DefaultPolicy(16 * 1024)
	policy.EnforceAdmission = true
	policy.AutoShedCritical = true
	if err := sessionpressure.SavePolicy(sessionpressure.PolicyPath(dir), policy); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	digest := strings.Repeat("d", 64)
	latest := sessionpressure.Snapshot{
		SchemaVersion: 1, Timestamp: now, Level: sessionpressure.LevelNormal,
		ProcessInventoryAvailable: true, ProcessInventoryFresh: true,
		ProcessInventoryCapturedAt: now, TopAgentTrees: []sessionpressure.AgentTree{},
		GuardPID: 77, GuardRole: "resident", GuardBinarySHA256: digest,
		GuardBudgetApplicable: true, GuardBudgetOK: true, GuardBaselineProven: true,
		MonitorSamples: 4, NormalMonitorSamples: 4,
	}
	if err := sessionpressure.NewTelemetryStore(dir).WriteLatest(latest); err != nil {
		t.Fatal(err)
	}
	installFakePressureLaunchd(t, &fakePressureLaunchdController{status: sessionpressure.LaunchdStatus{
		OK: true, Installed: true, Loaded: true, PID: 77,
		ArtifactPresent: true, ArtifactVerified: true, ArtifactSHA256: digest,
	}})
	return dir, policy
}

func TestParsePressureSelfTestArgs(t *testing.T) {
	tests := []struct {
		args    []string
		want    time.Duration
		full    bool
		wantErr bool
	}{
		{want: pressureSelfTestDefaultWait},
		{args: []string{"--wait", "0"}, want: 0},
		{args: []string{"--wait", "45s"}, want: 45 * time.Second},
		{args: []string{"--full", "--wait", "45s"}, want: 45 * time.Second, full: true},
		{args: []string{"--wait"}, wantErr: true},
		{args: []string{"--wait", "later"}, wantErr: true},
		{args: []string{"--other", "1m"}, wantErr: true},
	}
	if pressureSelfTestDefaultWait != 30*time.Second {
		t.Fatalf("default self-test wait=%s want 30s", pressureSelfTestDefaultWait)
	}
	for _, test := range tests {
		got, err := parsePressureSelfTestArgs(test.args)
		if (err != nil) != test.wantErr || got.wait != test.want || got.full != test.full {
			t.Fatalf("args=%v options=%+v err=%v want_wait=%s want_full=%v want_err=%v", test.args, got, err, test.want, test.full, test.wantErr)
		}
	}
}

func TestCmdSessionPressureSelfTestUsesBoundedDefaultQueueWait(t *testing.T) {
	seedReadyPressureSelfTest(t)
	calls := 0
	installPressureSelfTestProbe(t, func(_ context.Context, helper string, wait time.Duration) error {
		calls++
		if helper != "/fixture/immutable-helper" || wait != pressureSelfTestDefaultWait {
			t.Fatalf("helper=%q wait=%s", helper, wait)
		}
		return nil
	})
	rc, stdout, stderr := captureMainOutput(t, func() int {
		return cmdSessionPressureSelfTest(&Flags{JSON: true}, nil)
	})
	if rc != 0 || stderr != "" || calls != 1 || !strings.Contains(stdout, `"ok": true`) {
		t.Fatalf("rc=%d calls=%d stderr=%q\n%s", rc, calls, stderr, stdout)
	}
}

func TestCmdSessionPressureSelfTestRunsImmutableHelperPath(t *testing.T) {
	seedReadyPressureSelfTest(t)
	calls := 0
	installPressureSelfTestProbe(t, func(_ context.Context, helper string, wait time.Duration) error {
		calls++
		if helper != "/fixture/immutable-helper" || wait != 30*time.Second {
			t.Fatalf("helper=%q wait=%s", helper, wait)
		}
		return nil
	})
	rc, stdout, stderr := captureMainOutput(t, func() int {
		return cmdSessionPressureSelfTest(&Flags{JSON: true}, []string{"--wait", "30s"})
	})
	if rc != 0 || stderr != "" || calls != 1 {
		t.Fatalf("rc=%d calls=%d stderr=%q\n%s", rc, calls, stderr, stdout)
	}
	var payload struct {
		OK     bool                        `json:"ok"`
		Action string                      `json:"action"`
		Health sessionpressure.GuardHealth `json:"health"`
		Work   sessionpressure.WorkStatus  `json:"work"`
	}
	if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
		t.Fatal(err)
	}
	if !payload.OK || payload.Action != "self-test" || !payload.Health.DailyDriverReady || payload.Work.Used != 0 {
		t.Fatalf("unexpected self-test payload: %+v", payload)
	}
	var projection map[string]json.RawMessage
	if err := json.Unmarshal([]byte(stdout), &projection); err != nil {
		t.Fatal(err)
	}
	if len(stdout) >= 8192 || string(projection["output_scope"]) != `"compact"` || len(projection["policy"]) != 0 || len(projection["latest_monitor"]) != 0 {
		t.Fatalf("default self-test was not compact (%d bytes): %s", len(stdout), stdout)
	}
	if err := operationcontract.ValidateOutputJSON(operationcontract.All(), "nicos.session.pressure-result.v1", []byte(stdout)); err != nil {
		t.Fatalf("self-test output contract: %v\n%s", err, stdout)
	}
}

func TestCmdSessionPressureSelfTestFullHydratesDiagnostics(t *testing.T) {
	seedReadyPressureSelfTest(t)
	installPressureSelfTestProbe(t, func(context.Context, string, time.Duration) error { return nil })
	rc, stdout, stderr := captureMainOutput(t, func() int {
		return cmdSessionPressureSelfTest(&Flags{JSON: true}, []string{"--full"})
	})
	if rc != 0 || stderr != "" {
		t.Fatalf("rc=%d stderr=%q\n%s", rc, stderr, stdout)
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
		t.Fatal(err)
	}
	if string(payload["output_scope"]) != `"full"` || len(payload["policy"]) == 0 || len(payload["policy_path"]) == 0 || len(payload["latest_monitor"]) == 0 || len(payload["launchd"]) == 0 {
		t.Fatalf("full self-test omitted diagnostics: %s", stdout)
	}
}

func TestCmdSessionPressureSelfTestDoesNotProbeUnreadyResident(t *testing.T) {
	dir, policy := seedReadyPressureSelfTest(t)
	policy.AutoShedCritical = false
	if err := sessionpressure.SavePolicy(sessionpressure.PolicyPath(dir), policy); err != nil {
		t.Fatal(err)
	}
	calls := 0
	installPressureSelfTestProbe(t, func(context.Context, string, time.Duration) error {
		calls++
		return nil
	})
	rc, stdout, _ := captureMainOutput(t, func() int {
		return cmdSessionPressureSelfTest(&Flags{JSON: true}, []string{"--wait", "0"})
	})
	if rc != 1 || calls != 0 || !strings.Contains(stdout, "not daily-driver ready") {
		t.Fatalf("rc=%d calls=%d output=%s", rc, calls, stdout)
	}
	if err := operationcontract.ValidateOutputJSON(operationcontract.All(), "nicos.session.pressure-result.v1", []byte(stdout)); err != nil {
		t.Fatalf("failure output contract: %v\n%s", err, stdout)
	}
}

func TestCmdSessionPressureSelfTestReportsProbeFailure(t *testing.T) {
	seedReadyPressureSelfTest(t)
	installPressureSelfTestProbe(t, func(context.Context, string, time.Duration) error {
		return errors.New("fixture gate failure")
	})
	rc, stdout, _ := captureMainOutput(t, func() int {
		return cmdSessionPressureSelfTest(&Flags{JSON: true}, []string{"--wait", "0"})
	})
	if rc != 1 || !strings.Contains(stdout, "fixture gate failure") {
		t.Fatalf("rc=%d output=%s", rc, stdout)
	}
}

func TestCmdSessionPressureSelfTestRejectsPolicyChangeDuringProbe(t *testing.T) {
	dir, policy := seedReadyPressureSelfTest(t)
	installPressureSelfTestProbe(t, func(context.Context, string, time.Duration) error {
		policy.AutoShedCritical = false
		return sessionpressure.SavePolicy(sessionpressure.PolicyPath(dir), policy)
	})
	rc, stdout, _ := captureMainOutput(t, func() int {
		return cmdSessionPressureSelfTest(&Flags{JSON: true}, []string{"--wait", "0"})
	})
	if rc != 1 || !strings.Contains(stdout, "policy changed during the work probe") {
		t.Fatalf("rc=%d output=%s", rc, stdout)
	}
}

func TestPressureSelfTestOutputIsBounded(t *testing.T) {
	output := &pressureSelfTestOutput{limit: 8}
	body := []byte("0123456789abcdef")
	written, err := output.Write(body)
	if err != nil || written != len(body) || output.String() != "01234567" {
		t.Fatalf("written=%d output=%q err=%v", written, output.String(), err)
	}
	if got := boundedPressureSelfTestError(strings.Repeat("x", pressureSelfTestErrorLimit+20)); len(got) != pressureSelfTestErrorLimit {
		t.Fatalf("bounded error bytes=%d", len(got))
	}
}
