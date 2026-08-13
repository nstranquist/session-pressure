package sessionpressurecmd

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nstranquist/session-pressure/internal/operationcontract"
	"github.com/nstranquist/session-pressure/internal/sessionpressure"
)

func installDirectPressureWorkCommandFactory(t *testing.T) {
	t.Helper()
	original := pressureWorkCommandFactory
	pressureWorkCommandFactory = func(target string, args []string) (*exec.Cmd, io.WriteCloser, error) {
		return exec.Command(target, args...), nil, nil
	}
	t.Cleanup(func() { pressureWorkCommandFactory = original })
}

func TestPressureWorkHelpDocumentsWarningCapacityDerating(t *testing.T) {
	for _, want := range []string{"warning_capacity", "Existing leases are never", "admission_holds"} {
		if !strings.Contains(sessionPressureWorkHelp, want) {
			t.Fatalf("work help is missing warning derating contract %q", want)
		}
	}
}

func TestParseWorkSummaryArgsSupportsExplicitHydration(t *testing.T) {
	options, err := parseWorkSummaryArgs([]string{"--full", "--since", "2h"}, "work stats")
	if err != nil || !options.full {
		t.Fatalf("options=%+v err=%v", options, err)
	}
	age := time.Since(options.since)
	if age < 119*time.Minute || age > 121*time.Minute {
		t.Fatalf("since age=%s, want about 2h", age)
	}
	if _, err := parseWorkSummaryArgs([]string{"--full", "--full"}, "work stats"); err == nil {
		t.Fatal("duplicate --full was accepted")
	}
}

func TestCompactPressureWorkStatsRetainsAggregatesAndDropsHydration(t *testing.T) {
	stats := sessionpressure.WorkStats{
		OperationCount: 3,
		ByClass: []sessionpressure.WorkClassStats{
			{Class: sessionpressure.WorkClassTest, Operations: 3},
			{Class: sessionpressure.WorkClassBuild},
		},
		CalibrationCohorts: []sessionpressure.WorkCalibrationCohort{{Class: sessionpressure.WorkClassTest}},
		ServiceLevel: sessionpressure.WorkServiceLevel{
			EvaluatedSamples: []sessionpressure.WorkClassSLOSample{{Class: sessionpressure.WorkClassTest, Samples: 3}},
		},
		PressureConditionedServiceLevel: sessionpressure.WorkPressureConditionedServiceLevel{
			ByClass: []sessionpressure.WorkPressureConditionedClass{{Class: sessionpressure.WorkClassTest, Samples: 3}},
		},
	}
	compact := compactPressureWorkStats(stats)
	if compact.OperationCount != 3 || len(compact.ByClass) != 1 || compact.ByClass[0].Class != sessionpressure.WorkClassTest {
		t.Fatalf("compact stats lost active aggregate: %+v", compact)
	}
	if len(compact.CalibrationCohorts) != 0 || len(compact.ServiceLevel.EvaluatedSamples) != 0 || len(compact.PressureConditionedServiceLevel.ByClass) != 0 {
		t.Fatalf("compact stats retained hydration-only detail: %+v", compact)
	}
	if len(stats.ByClass) != 2 || len(stats.CalibrationCohorts) != 1 {
		t.Fatalf("compaction mutated full stats: %+v", stats)
	}
}

func TestPressureWorkStatsDefaultsToBoundedContractProjection(t *testing.T) {
	rc, stdout, stderr := captureMainOutput(t, func() int {
		return cmdSessionPressureWorkStats(&Flags{JSON: true}, t.TempDir(), nil)
	})
	if rc != 0 || stderr != "" {
		t.Fatalf("rc=%d stderr=%q\n%s", rc, stderr, stdout)
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
		t.Fatal(err)
	}
	if len(stdout) >= 8192 || string(payload["output_scope"]) != `"compact"` || len(payload["calibration"]) != 0 {
		t.Fatalf("default work stats was not compact (%d bytes): %s", len(stdout), stdout)
	}
	if err := operationcontract.ValidateOutputJSON(operationcontract.All(), "nicos.session.pressure-result.v1", []byte(stdout)); err != nil {
		t.Fatalf("work stats output contract: %v\n%s", err, stdout)
	}
}

func TestPressureWorkReportDefaultsToBoundedContractProjection(t *testing.T) {
	rc, stdout, stderr := captureMainOutput(t, func() int {
		return cmdSessionPressureWorkReport(&Flags{JSON: true}, t.TempDir(), nil)
	})
	if rc != 0 || stderr != "" {
		t.Fatalf("rc=%d stderr=%q\n%s", rc, stderr, stdout)
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
		t.Fatal(err)
	}
	if len(stdout) >= 4096 || string(payload["output_scope"]) != `"compact"` {
		t.Fatalf("default work report was not compact (%d bytes): %s", len(stdout), stdout)
	}
	if err := operationcontract.ValidateOutputJSON(operationcontract.All(), "nicos.session.pressure-result.v1", []byte(stdout)); err != nil {
		t.Fatalf("work report output contract: %v\n%s", err, stdout)
	}
}

func TestPressureWorkRunHandsOffToInstalledHelper(t *testing.T) {
	originalResolve := resolvePressureWorkHelper
	originalExec := execPressureWorkHelper
	sentinel := errors.New("exec intercepted")
	var gotPath string
	var gotArgs []string
	resolvePressureWorkHelper = func() (string, error) { return "/tmp/immutable-helper", nil }
	execPressureWorkHelper = func(path string, args []string, _ []string) error {
		gotPath = path
		gotArgs = append([]string(nil), args...)
		return sentinel
	}
	t.Cleanup(func() {
		resolvePressureWorkHelper = originalResolve
		execPressureWorkHelper = originalExec
	})

	code := runPressureWorkHelper([]string{"--class", "build", "--wait", "0", "--", "/usr/bin/true"})
	if code != 1 {
		t.Fatalf("runPressureWorkHelper code=%d, want intercepted exec failure", code)
	}
	if gotPath != "/tmp/immutable-helper" || !slices.Equal(gotArgs, []string{
		"/tmp/immutable-helper", "work-run", "--class", "build", "--wait", "0", "--", "/usr/bin/true",
	}) {
		t.Fatalf("helper handoff path=%q args=%q", gotPath, gotArgs)
	}
}

func TestParsePressureWorkRunArgs(t *testing.T) {
	options, err := parsePressureWorkRunArgs([]string{"--class", "build", "--no-reuse", "--wait", "30s", "--", "go", "test", "./..."})
	if err != nil {
		t.Fatal(err)
	}
	if options.class != sessionpressure.WorkClassTest || options.wait != 30*time.Second || !options.noReuse || !slices.Equal(options.command, []string{"go", "test", "./..."}) {
		t.Fatalf("unexpected options: %+v", options)
	}
	// Prefer express: package-scoped go test auto-classifies without --class.
	options, err = parsePressureWorkRunArgs([]string{"--", "go", "test", "./pkg"})
	if err != nil || options.class != sessionpressure.WorkClassExpressTest {
		t.Fatalf("auto express options=%+v err=%v", options, err)
	}
	options, err = parsePressureWorkRunArgs([]string{"--class", "test", "--priority", "--", "go", "test", "./..."})
	if err != nil || !options.priority {
		t.Fatalf("priority options=%+v err=%v", options, err)
	}
	options, err = parsePressureWorkRunArgs([]string{"--wait", "0", "--class", "emulator", "--", "nicossim", "boot", "android"})
	if err != nil || options.wait != 0 || options.class != sessionpressure.WorkClassEmulator {
		t.Fatalf("zero-wait options=%+v err=%v", options, err)
	}
	options, err = parsePressureWorkRunArgs([]string{"--class", "benchmark", "--", "ndev", "perf", "verify", "--strict"})
	if err != nil || options.class != sessionpressure.WorkClassBenchmark {
		t.Fatalf("benchmark options=%+v err=%v", options, err)
	}
}

func TestParsePressureWorkRunArgsRejectsAmbiguity(t *testing.T) {
	for _, args := range [][]string{
		{"--", "true"}, // unrecognized toolchain still requires --class
		{"--class", "build"},
		{"--class", "unknown", "--", "true"},
		{"--class", "build", "go", "test"},
		{"--class", "build", "--wait", "later", "--", "true"},
	} {
		if _, err := parsePressureWorkRunArgs(args); err == nil {
			t.Fatalf("args %#v unexpectedly accepted", args)
		}
	}
}

func TestPressureWorkEnvironmentBoundsGoParallelismAndPreservesOverride(t *testing.T) {
	limits := sessionpressure.DefaultPolicy(16 * 1024).WorkLimits
	environment, err := pressureWorkEnvironment([]string{"PATH=/usr/bin"}, limits, sessionpressure.WorkClassBuild)
	if err != nil || !slices.Contains(environment, "GOMAXPROCS=5") {
		t.Fatalf("bounded environment=%v err=%v", environment, err)
	}
	overridden, err := pressureWorkEnvironment([]string{"GOMAXPROCS=2", "PATH=/usr/bin"}, limits, sessionpressure.WorkClassBuild)
	if err != nil || !slices.Equal(overridden, []string{"GOMAXPROCS=2", "PATH=/usr/bin"}) {
		t.Fatalf("override environment=%v err=%v", overridden, err)
	}
}

func TestRunPressureWorkRechecksAdmissionAfterLease(t *testing.T) {
	installDirectPressureWorkCommandFactory(t)
	originalAdmissionCheck := WorkHostAdmissionCheck
	calls := 0
	WorkHostAdmissionCheck = func() sessionpressure.Admission {
		calls++
		if calls == 1 {
			return sessionpressure.Admission{Allowed: true, Level: sessionpressure.LevelNormal}
		}
		return sessionpressure.Admission{
			Allowed: false,
			Level:   sessionpressure.LevelRed,
			Reasons: []string{"host CPU test surge"},
			Snapshot: &sessionpressure.Snapshot{
				HostCPURollingAvailable: true,
				HostCPURollingPercent:   100,
			},
		}
	}
	t.Cleanup(func() { WorkHostAdmissionCheck = originalAdmissionCheck })

	policy := sessionpressure.DefaultPolicy(16 * 1024)
	coordinator := sessionpressure.NewWorkCoordinator(t.TempDir(), policy.WorkLimits)
	code := runPressureWorkCommand(coordinator, pressureWorkRunOptions{
		class: sessionpressure.WorkClassBuild, command: []string{"/usr/bin/true"},
	})
	if code != 11 {
		t.Fatalf("runPressureWorkCommand code=%d, want 11 (policy band for post-lease pressure denial)", code)
	}
	if calls != 2 {
		t.Fatalf("admission checks=%d, want initial and post-lease checks", calls)
	}
	status, err := coordinator.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.Used != 0 || len(status.Leases) != 0 {
		t.Fatalf("blocked post-wait admission leaked lease: %+v", status)
	}
}

func TestRunPressureWorkWaitsForPressureRecovery(t *testing.T) {
	installDirectPressureWorkCommandFactory(t)
	originalAdmissionCheck := WorkHostAdmissionCheck
	originalRetry := pressureWorkAdmissionRetryInterval
	calls := 0
	WorkHostAdmissionCheck = func() sessionpressure.Admission {
		calls++
		if calls == 1 {
			return sessionpressure.Admission{Allowed: false, Level: sessionpressure.LevelRed, Reasons: []string{"test surge"}}
		}
		return sessionpressure.Admission{Allowed: true, Level: sessionpressure.LevelNormal}
	}
	pressureWorkAdmissionRetryInterval = time.Millisecond
	t.Cleanup(func() {
		WorkHostAdmissionCheck = originalAdmissionCheck
		pressureWorkAdmissionRetryInterval = originalRetry
	})

	policy := sessionpressure.DefaultPolicy(16 * 1024)
	coordinator := sessionpressure.NewWorkCoordinator(t.TempDir(), policy.WorkLimits)
	code := runPressureWorkCommand(coordinator, pressureWorkRunOptions{
		class: sessionpressure.WorkClassBrowser, wait: time.Second, command: []string{"/bin/sleep", "0.05"},
	})
	if code != 0 {
		t.Fatalf("runPressureWorkCommand code=%d, want 0", code)
	}
	if calls < 3 {
		t.Fatalf("admission checks=%d, want red, recovered, and post-lease", calls)
	}
	status, err := coordinator.Status(context.Background())
	if err != nil || status.Used != 0 || len(status.Leases) != 0 {
		t.Fatalf("completed command leaked lease: status=%+v err=%v", status, err)
	}
}

func TestPressureWorkHistoryStatsAndEvaluationExposeTypedJSON(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(sessionpressure.DataDirEnv, dir)
	policy := sessionpressure.DefaultPolicy(16 * 1024)
	if err := sessionpressure.SavePolicy(sessionpressure.PolicyPath(dir), policy); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	event := sessionpressure.WorkEvent{
		Event: sessionpressure.WorkEventQueued, OperationID: "00000000000000000000000000000001",
		Class: sessionpressure.WorkClassTest, Weight: policy.WorkLimits.TestWeight,
		CommandDigest: sessionpressure.CommandShapeDigest("/usr/bin/go", 2), PressureLevel: sessionpressure.LevelNormal,
	}
	store := sessionpressure.NewWorkEventStore(dir)
	store.Now = func() time.Time { return now }
	if err := store.AppendDurable(event); err != nil {
		t.Fatal(err)
	}

	for _, testCase := range []struct {
		name       string
		args       []string
		wantAction string
		wantField  string
		wantRC     int
	}{
		{name: "history", args: []string{"history", "--since", "1h", "--event", "queued"}, wantAction: "work.history", wantField: "work_events"},
		{name: "stats", args: []string{"stats", "--since", "1h"}, wantAction: "work.stats", wantField: "work_stats"},
		{name: "evaluate", args: []string{"evaluate"}, wantAction: "work.evaluate", wantField: "work_evaluation", wantRC: 1},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			rc, stdout, stderr := captureMainOutput(t, func() int {
				return cmdSessionPressureWork(&Flags{JSON: true}, testCase.args)
			})
			if rc != testCase.wantRC || stderr != "" {
				t.Fatalf("rc=%d stderr=%q stdout=%q", rc, stderr, stdout)
			}
			var payload map[string]json.RawMessage
			if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
				t.Fatal(err)
			}
			var action string
			if err := json.Unmarshal(payload["action"], &action); err != nil || action != testCase.wantAction || len(payload[testCase.wantField]) == 0 {
				t.Fatalf("payload=%s action=%q err=%v", stdout, action, err)
			}
		})
	}
}

func TestPressureWorkHistoryRejectsUnknownEventBeforeReadingLedger(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(sessionpressure.DataDirEnv, dir)
	if err := sessionpressure.SavePolicy(sessionpressure.PolicyPath(dir), sessionpressure.DefaultPolicy(16*1024)); err != nil {
		t.Fatal(err)
	}
	rc, _, stderr := captureMainOutput(t, func() int {
		return cmdSessionPressureWork(&Flags{JSON: true}, []string{"history", "--event", "mystery"})
	})
	if rc != 2 || !strings.Contains(stderr, "unknown work event") {
		t.Fatalf("rc=%d stderr=%q", rc, stderr)
	}
}

func TestPressureWorkStatusTextSurfacesLongLeaseReview(t *testing.T) {
	status := sessionpressure.WorkStatus{
		Capacity: 8, Used: 2, Available: 6, StatePath: "/private/state.json",
		Leases: []sessionpressure.WorkLeaseStatus{{
			Class: sessionpressure.WorkClassBrowser, Weight: 2, AgeMS: int64((16 * time.Minute) / time.Millisecond),
			Review: true, ReviewReason: "finite lease exceeded review age; use ndev dev",
		}},
	}
	text := pressureWorkStatusText(status)
	for _, want := range []string{"review_leases=1", "review class=browser", "age=16m0s", "use ndev dev"} {
		if !strings.Contains(text, want) {
			t.Fatalf("status text missing %q: %s", want, text)
		}
	}
	if strings.Contains(text, "operation_id") || strings.Contains(text, "owner_identity") {
		t.Fatalf("status text leaked private identity: %s", text)
	}
}

func TestPressureWorkOverrideRequiresConfirmationAndReturnsTypedJSON(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(sessionpressure.DataDirEnv, dir)
	policy := sessionpressure.DefaultPolicy(16 * 1024)
	if err := sessionpressure.SavePolicy(sessionpressure.PolicyPath(dir), policy); err != nil {
		t.Fatal(err)
	}
	coordinator := sessionpressure.NewWorkCoordinator(dir, policy.WorkLimits)
	operationID := "00000000000000000000000000000001"
	waiter, _, err := coordinator.RegisterWaiter(context.Background(), sessionpressure.WorkClassTest, operationID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = waiter.Cancel(context.Background()) }()

	rc, _, stderr := captureMainOutput(t, func() int {
		return cmdSessionPressureWork(&Flags{JSON: true}, []string{"override", "--operation-id", operationID})
	})
	if rc != 2 || !strings.Contains(stderr, "requires --confirm") {
		t.Fatalf("unconfirmed override rc=%d stderr=%q", rc, stderr)
	}
	rc, stdout, stderr := captureMainOutput(t, func() int {
		return cmdSessionPressureWork(&Flags{JSON: true}, []string{"override", "--operation-id", operationID, "--confirm"})
	})
	if rc != 0 || stderr != "" {
		t.Fatalf("override rc=%d stderr=%q stdout=%q", rc, stderr, stdout)
	}
	var payload struct {
		Action   string                             `json:"action"`
		OK       bool                               `json:"ok"`
		Override sessionpressure.WorkOverrideResult `json:"override"`
		Work     sessionpressure.WorkStatus         `json:"work"`
	}
	if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
		t.Fatal(err)
	}
	if !payload.OK || payload.Action != "work.override" || payload.Override.OperationID != operationID || payload.Work.OverrideOperationID != operationID {
		t.Fatalf("override payload=%+v", payload)
	}
}

func TestPressureWorkOverrideAllPinsWholeQueue(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(sessionpressure.DataDirEnv, dir)
	policy := sessionpressure.DefaultPolicy(16 * 1024)
	if err := sessionpressure.SavePolicy(sessionpressure.PolicyPath(dir), policy); err != nil {
		t.Fatal(err)
	}
	coordinator := sessionpressure.NewWorkCoordinator(dir, policy.WorkLimits)
	first := "00000000000000000000000000000001"
	second := "00000000000000000000000000000002"
	firstWaiter, _, err := coordinator.RegisterWaiter(context.Background(), sessionpressure.WorkClassTest, first)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = firstWaiter.Cancel(context.Background()) }()
	secondWaiter, _, err := coordinator.RegisterWaiter(context.Background(), sessionpressure.WorkClassBuild, second)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = secondWaiter.Cancel(context.Background()) }()

	rc, _, stderr := captureMainOutput(t, func() int {
		return cmdSessionPressureWork(&Flags{JSON: true}, []string{"override", "--all", "--operation-id", first, "--confirm"})
	})
	if rc != 2 || !strings.Contains(stderr, "not both") {
		t.Fatalf("mixed --all and --operation-id rc=%d stderr=%q", rc, stderr)
	}
	rc, _, stderr = captureMainOutput(t, func() int {
		return cmdSessionPressureWork(&Flags{JSON: true}, []string{"override", "--all"})
	})
	if rc != 2 || !strings.Contains(stderr, "requires --confirm") {
		t.Fatalf("unconfirmed --all rc=%d stderr=%q", rc, stderr)
	}

	rc, stdout, stderr := captureMainOutput(t, func() int {
		return cmdSessionPressureWork(&Flags{JSON: true}, []string{"override", "--all", "--confirm"})
	})
	if rc != 0 || stderr != "" {
		t.Fatalf("override --all rc=%d stderr=%q stdout=%q", rc, stderr, stdout)
	}
	var payload struct {
		Action    string                               `json:"action"`
		OK        bool                                 `json:"ok"`
		Pinned    int                                  `json:"pinned"`
		Override  sessionpressure.WorkOverrideResult   `json:"override"`
		Overrides []sessionpressure.WorkOverrideResult `json:"overrides"`
		Work      sessionpressure.WorkStatus           `json:"work"`
	}
	if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
		t.Fatal(err)
	}
	if !payload.OK || payload.Pinned != 2 || len(payload.Overrides) != 2 {
		t.Fatalf("override --all payload=%+v", payload)
	}
	// `override` must stay the head so a single-override reader is unaffected.
	if payload.Override.OperationID != first || payload.Overrides[1].OperationID != second {
		t.Fatalf("sequence order=%+v", payload.Overrides)
	}
	if payload.Overrides[0].OverridePosition != 1 || payload.Overrides[1].OverridePosition != 2 {
		t.Fatalf("override positions=%+v", payload.Overrides)
	}
	if payload.Work.OverrideOperationID != first || payload.Work.OverrideQueueDepth != 2 {
		t.Fatalf("work status=%+v", payload.Work)
	}
	if len(payload.Work.OverrideQueue) != 1 || payload.Work.OverrideQueue[0] != second {
		t.Fatalf("pending tail=%+v", payload.Work.OverrideQueue)
	}
}
