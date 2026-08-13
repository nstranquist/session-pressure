package sessionpressure

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func TestWorkEnvironmentRemovesShellUnderscoreAndSetsCPUWeight(t *testing.T) {
	limits := defaultWorkLimits(10)
	environment, err := WorkEnvironment([]string{"PATH=/usr/bin", "_=volatile", "KEEP=value"}, limits, WorkClassTest)
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(environment, "_=volatile") || !slices.Contains(environment, "KEEP=value") || !slices.Contains(environment, "GOMAXPROCS=3") {
		t.Fatalf("work environment = %v", environment)
	}
	withCPU, err := WorkEnvironment([]string{"_=one", "GOMAXPROCS=7", "_=two"}, limits, WorkClassTest)
	if err != nil || slices.Contains(withCPU, "_=one") || slices.Contains(withCPU, "_=two") || !slices.Contains(withCPU, "GOMAXPROCS=7") {
		t.Fatalf("existing CPU environment = %v err=%v", withCPU, err)
	}
}

// Shell bookkeeping variables cannot change what `go build`/`test`/`vet`
// produces, but they are bound into the environment-keyed express reuse
// fingerprint. Leaving them in made every real invocation hash differently and
// drove the observed 0 reuse hits across 160 eligible operations (2026-07-24).
func TestWorkEnvironmentDropsShellBookkeepingSoReuseDigestIsStable(t *testing.T) {
	limits := defaultWorkLimits(10)
	base := []string{"PATH=/usr/bin", "HOME=/Users/x", "GOFLAGS=-mod=mod"}
	first, err := WorkEnvironment(append(slices.Clone(base), "_=go", "OLDPWD=/a", "SHLVL=2"), limits, WorkClassExpressTest)
	if err != nil {
		t.Fatal(err)
	}
	second, err := WorkEnvironment(append(slices.Clone(base), "_=vet", "OLDPWD=/b", "SHLVL=9"), limits, WorkClassExpressTest)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(first, second) {
		t.Fatalf("shell bookkeeping leaked into the coordinated environment:\nfirst=%v\nsecond=%v", first, second)
	}
	// Variables a workload can legitimately read must stay bound.
	if !slices.Contains(first, "GOFLAGS=-mod=mod") || !slices.Contains(first, "HOME=/Users/x") {
		t.Fatalf("dropped a workload-visible variable: %v", first)
	}
}

func TestWorkGateRejectsParentExitBeforeBinding(t *testing.T) {
	gateRead, gateWrite := io.Pipe()
	if err := gateWrite.Close(); err != nil {
		t.Fatal(err)
	}
	called := false
	err := awaitWorkGate([]string{"/usr/bin/true"}, gateRead, func(string, []string, []string) error {
		called = true
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "before durable lease binding") || called {
		t.Fatalf("closed parent gate executed work: called=%v err=%v", called, err)
	}
}

func TestWorkGateClosesControlDescriptorBeforeExec(t *testing.T) {
	gateRead, gateWrite, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := gateWrite.Write([]byte{workGateToken}); err != nil {
		t.Fatal(err)
	}
	if err := gateWrite.Close(); err != nil {
		t.Fatal(err)
	}
	closedBeforeExec := false
	err = awaitWorkGate([]string{"/usr/bin/true"}, gateRead, func(string, []string, []string) error {
		_, readErr := gateRead.Read(make([]byte, 1))
		closedBeforeExec = errors.Is(readErr, os.ErrClosed)
		return nil
	})
	if err != nil || !closedBeforeExec {
		t.Fatalf("control descriptor survived exec boundary: closed=%v err=%v", closedBeforeExec, err)
	}
}

func TestRunWorkCommandRejectsNilFactoryCommandWithoutLeakingLease(t *testing.T) {
	coordinator := NewWorkCoordinator(t.TempDir(), testWorkLimits())
	code, err := RunWorkCommand(
		coordinator,
		WorkRunOptions{Class: WorkClassBuild, Wait: time.Second, Command: []string{"/usr/bin/true"}},
		func() Admission { return Admission{Allowed: true, Level: LevelNormal} },
		time.Millisecond,
		WorkRunStreams{
			Stdout: io.Discard,
			Stderr: io.Discard,
			CommandFactory: func(string, []string) (*exec.Cmd, io.WriteCloser, error) {
				return nil, nil, nil
			},
		},
	)
	if code != 1 || err == nil || !strings.Contains(err.Error(), "nil command") {
		t.Fatalf("nil command factory result: code=%d err=%v", code, err)
	}
	status, statusErr := coordinator.Status(context.Background())
	if statusErr != nil || status.Used != 0 || len(status.Leases) != 0 {
		t.Fatalf("nil command factory leaked lease: status=%+v err=%v", status, statusErr)
	}
}

func TestRunWorkCommandBindsChildBeforeOpeningGate(t *testing.T) {
	marker := t.TempDir() + "/gate-opened"
	t.Setenv("NICOS_WORK_GATE_TEST_MARKER", marker)
	coordinator := NewWorkCoordinator(t.TempDir(), testWorkLimits())
	result := make(chan struct {
		code int
		err  error
	}, 1)
	go func() {
		code, err := RunWorkCommand(
			coordinator,
			WorkRunOptions{Class: WorkClassBuild, Wait: time.Second, Command: []string{"/bin/sleep", "0.3"}},
			func() Admission { return Admission{Allowed: true, Level: LevelNormal} },
			time.Millisecond,
			WorkRunStreams{Stdout: io.Discard, Stderr: io.Discard, CommandFactory: testGatedWorkCommand},
		)
		result <- struct {
			code int
			err  error
		}{code: code, err: err}
	}()

	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := os.Stat(marker); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("gated child never received durable binding token")
		}
		time.Sleep(10 * time.Millisecond)
	}
	status, err := coordinator.Status(context.Background())
	if err != nil || len(status.Leases) != 1 || status.Leases[0].PID == coordinator.PID || status.Used != coordinator.Limits.BuildWeight {
		t.Fatalf("gate opened before child-owned lease was durable: status=%+v err=%v", status, err)
	}
	completed := <-result
	if completed.err != nil || completed.code != 0 {
		t.Fatalf("gated work result: code=%d err=%v", completed.code, completed.err)
	}
	status, err = coordinator.Status(context.Background())
	if err != nil || status.Used != 0 || len(status.Leases) != 0 {
		t.Fatalf("gated work leaked lease: status=%+v err=%v", status, err)
	}
	events, err := NewWorkEventStore(coordinator.Dir).Read(WorkEventFilter{})
	if err != nil {
		t.Fatal(err)
	}
	want := []WorkEventType{WorkEventQueued, WorkEventAcquired, WorkEventStarted, WorkEventCompleted}
	if len(events) != len(want) {
		t.Fatalf("lifecycle events=%+v", events)
	}
	for index, eventType := range want {
		if events[index].Event != eventType || events[index].CommandDigest == "" {
			t.Fatalf("lifecycle events=%+v", events)
		}
	}
}

func TestRunWorkCommandUsesNonConsumingFIFOReservationDuringPostQueuePressure(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("NICOS_WORK_GATE_TEST_MARKER", dir+"/gate-opened")
	coordinator := NewWorkCoordinator(dir, testWorkLimits())
	pressureObserved := make(chan struct{})
	recoverPressure := make(chan struct{})
	var admissionCalls atomic.Int32
	admissionCheck := func() Admission {
		switch admissionCalls.Add(1) {
		case 1:
			return Admission{Allowed: true, Level: LevelNormal}
		case 2:
			return Admission{Allowed: false, Level: LevelRed, Reasons: []string{"host free memory 10% <= red 15%"}}
		default:
			select {
			case <-pressureObserved:
			default:
				close(pressureObserved)
			}
			<-recoverPressure
			return Admission{Allowed: true, Level: LevelNormal}
		}
	}
	type outcome struct {
		code int
		err  error
	}
	result := make(chan outcome, 1)
	go func() {
		code, err := RunWorkCommand(
			coordinator,
			WorkRunOptions{Class: WorkClassBuild, Wait: time.Second, Progress: WorkProgressQuiet, Command: []string{"/usr/bin/true"}},
			admissionCheck,
			time.Millisecond,
			WorkRunStreams{Stdout: io.Discard, Stderr: io.Discard, CommandFactory: testGatedWorkCommand},
		)
		result <- outcome{code: code, err: err}
	}()
	select {
	case <-pressureObserved:
	case <-time.After(time.Second):
		t.Fatal("post-queue pressure was not observed")
	}
	status, err := coordinator.Status(context.Background())
	if err != nil || status.Used != 0 || len(status.Leases) != 0 || status.QueueDepth != 1 || status.PressureReservationCount != 1 || status.ReservedWeight != coordinator.Limits.BuildWeight {
		t.Fatalf("pressure reservation consumed weighted capacity: status=%+v err=%v", status, err)
	}
	events, err := NewWorkEventStore(dir).Read(WorkEventFilter{})
	if err != nil || len(events) != 3 || events[0].Event != WorkEventQueued || events[1].Event != WorkEventAcquired || events[2].Event != WorkEventReserved {
		t.Fatalf("pre-recovery lifecycle=%+v err=%v", events, err)
	}
	close(recoverPressure)
	select {
	case completed := <-result:
		if completed.err != nil || completed.code != 0 {
			t.Fatalf("post-pressure work result: code=%d err=%v", completed.code, completed.err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("work did not resume after pressure recovery")
	}
	events, err = NewWorkEventStore(dir).Read(WorkEventFilter{})
	want := []WorkEventType{WorkEventQueued, WorkEventAcquired, WorkEventReserved, WorkEventReacquired, WorkEventStarted, WorkEventCompleted}
	if err != nil || len(events) != len(want) {
		t.Fatalf("post-recovery lifecycle=%+v err=%v", events, err)
	}
	for index, eventType := range want {
		if events[index].Event != eventType {
			t.Fatalf("post-recovery lifecycle=%+v", events)
		}
	}
	if events[3].PressureWaitMilliseconds <= 0 || events[4].PrestartMilliseconds <= 0 {
		t.Fatalf("phase timing missing after reservation: reacquired=%+v started=%+v", events[3], events[4])
	}
}

func TestRunWorkCommandRechecksPressureAfterEveryReservationReacquisition(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("NICOS_WORK_GATE_TEST_MARKER", dir+"/gate-opened")
	coordinator := NewWorkCoordinator(dir, testWorkLimits())
	var admissionCalls atomic.Int32
	admissionCheck := func() Admission {
		switch admissionCalls.Add(1) {
		case 1, 3:
			return Admission{Allowed: true, Level: LevelNormal}
		case 2, 4:
			return Admission{Allowed: false, Level: LevelRed, Reasons: []string{"host free memory 10% <= red 15%"}}
		default:
			return Admission{Allowed: true, Level: LevelNormal}
		}
	}

	code, err := RunWorkCommand(
		coordinator,
		WorkRunOptions{Class: WorkClassBuild, Wait: time.Second, Progress: WorkProgressQuiet, Command: []string{"/usr/bin/true"}},
		admissionCheck,
		time.Millisecond,
		WorkRunStreams{Stdout: io.Discard, Stderr: io.Discard, CommandFactory: testGatedWorkCommand},
	)
	if err != nil || code != 0 {
		t.Fatalf("repeated pressure work result: code=%d err=%v", code, err)
	}
	if calls := admissionCalls.Load(); calls < 6 {
		t.Fatalf("admission checks=%d, want final check after second reacquisition", calls)
	}
	events, err := NewWorkEventStore(dir).Read(WorkEventFilter{})
	want := []WorkEventType{
		WorkEventQueued, WorkEventAcquired,
		WorkEventReserved, WorkEventReacquired,
		WorkEventReserved, WorkEventReacquired,
		WorkEventStarted, WorkEventCompleted,
	}
	if err != nil || len(events) != len(want) {
		t.Fatalf("repeated pressure lifecycle=%+v err=%v", events, err)
	}
	for index, eventType := range want {
		if events[index].Event != eventType {
			t.Fatalf("repeated pressure lifecycle=%+v", events)
		}
	}
	if events[2].LeaseID == events[4].LeaseID || events[3].LeaseID == events[5].LeaseID {
		t.Fatalf("pressure cycles reused lease identities: %+v", events)
	}
}

func testGatedWorkCommand(target string, args []string) (*exec.Cmd, io.WriteCloser, error) {
	gateRead, gateWrite, err := os.Pipe()
	if err != nil {
		return nil, nil, err
	}
	childArgs := []string{"-test.run=^TestWorkGateHelperProcess$", "--", target}
	childArgs = append(childArgs, args...)
	command := exec.Command(os.Args[0], childArgs...)
	command.ExtraFiles = []*os.File{gateRead}
	return command, gateWrite, nil
}

func TestWorkGateHelperProcess(t *testing.T) {
	separator := slices.Index(os.Args, "--")
	if separator < 0 || separator+1 >= len(os.Args) {
		return
	}
	gate := os.NewFile(workGateFD, "test-pressure-work-gate")
	if gate == nil {
		os.Exit(workChildErrorCode)
	}
	err := awaitWorkGate(os.Args[separator+1:], gate, func(path string, argv, environment []string) error {
		marker := os.Getenv("NICOS_WORK_GATE_TEST_MARKER")
		if marker == "" {
			return io.ErrUnexpectedEOF
		}
		if err := os.WriteFile(marker, []byte("bound\n"), 0o600); err != nil {
			return err
		}
		return syscall.Exec(path, argv, environment)
	})
	if err != nil {
		os.Exit(workChildErrorCode)
	}
}

func TestWorkRunParsesCalibratedClassAndTypedProgress(t *testing.T) {
	options, err := ParseWorkRunArgs([]string{"--class", "test", "--wait", "45s", "--progress", "jsonl", "--", "go", "test", "./..."})
	if err != nil {
		t.Fatal(err)
	}
	if options.Class != WorkClassTest || options.RequestedClass != WorkClassTest || options.Wait != 45*time.Second || options.Progress != WorkProgressJSONL || len(options.Command) != 3 {
		t.Fatalf("options=%+v", options)
	}
	if _, err := ParseWorkProgressMode("verbose"); err == nil {
		t.Fatal("unknown progress mode unexpectedly accepted")
	}
}

func TestWorkRunRejectsResidentCommands(t *testing.T) {
	resident := [][]string{
		{"--class", "browser", "--", "pnpm", "dev"},
		{"--class", "browser", "--", "npm", "run", "start"},
		{"--class", "browser", "--", "env", "PORT=3000", "vinext", "dev"},
	}
	for _, args := range resident {
		if _, err := ParseWorkRunArgs(args); err == nil || !strings.Contains(err.Error(), "ndev dev") {
			t.Fatalf("resident command should route to ndev dev: args=%v err=%v", args, err)
		}
	}
	if _, err := ParseWorkRunArgs([]string{"--class", "test", "--", "pnpm", "test"}); err != nil {
		t.Fatalf("finite command rejected: %v", err)
	}
}

func TestWorkCapacityReasonExplainsLongFiniteLease(t *testing.T) {
	reason := workCapacityReason(WorkStatus{Leases: []WorkLeaseStatus{{Class: WorkClassBrowser, AgeMS: int64((20 * time.Minute) / time.Millisecond), Review: true}}})
	if !strings.Contains(reason, "resident service") || !strings.Contains(reason, "ndev dev") {
		t.Fatalf("reason=%q", reason)
	}
}

func TestWorkAdmissionHysteresisFiltersTransientCPUAndBlocksMemoryImmediately(t *testing.T) {
	limits := testWorkLimits()
	cpuRed := Admission{Allowed: false, Level: LevelRed, Reasons: []string{"host CPU 99.0% >= red 95.0%"}, Snapshot: &Snapshot{HostCPUAvailable: true, HostCPUPercent: 99}}
	cpuNormal := Admission{Allowed: true, Level: LevelNormal, Snapshot: &Snapshot{HostCPUAvailable: true, HostCPUPercent: 70}}
	gate := &workAdmissionGate{limits: limits}
	if first := gate.Observe(cpuRed, false); first.Allowed || first.RetryInterval != WorkCPUConfirmationInterval || gate.latched {
		t.Fatalf("first CPU spike=%+v gate=%+v", first, gate)
	}
	if recovered := gate.Observe(cpuNormal, false); !recovered.Allowed || gate.latched {
		t.Fatalf("transient CPU spike latched: decision=%+v gate=%+v", recovered, gate)
	}
	if first := gate.Observe(cpuRed, false); first.Allowed {
		t.Fatalf("first sustained sample=%+v", first)
	}
	if second := gate.Observe(cpuRed, false); second.Allowed || !gate.latched {
		t.Fatalf("sustained CPU did not latch: decision=%+v gate=%+v", second, gate)
	}
	if first := gate.Observe(cpuNormal, false); first.Allowed {
		t.Fatalf("one recovery sample released latch: %+v", first)
	}
	if second := gate.Observe(cpuNormal, false); !second.Allowed || gate.latched {
		t.Fatalf("sustained recovery did not release: decision=%+v gate=%+v", second, gate)
	}
	memoryRed := Admission{Allowed: false, Level: LevelRed, Reasons: []string{"host free memory 10% <= red 15%"}}
	if decision := (&workAdmissionGate{limits: limits}).Observe(memoryRed, false); decision.Allowed || decision.Dimension != "memory" || decision.RetryInterval != WorkAdmissionRetryInterval {
		t.Fatalf("memory decision=%+v", decision)
	}
}

func TestNoWaitCPURequiresResidentRollingCorroboration(t *testing.T) {
	gate := &workAdmissionGate{limits: testWorkLimits(), cpuRedPercent: 95}
	liveSpike := Admission{
		Allowed: false, Level: LevelRed, Reasons: []string{"host CPU 99.0% >= red 95.0%"},
		Snapshot: &Snapshot{HostCPUAvailable: true, HostCPUPercent: 99, HostCPULivePercent: 99, HostCPURollingAvailable: true, HostCPURollingPercent: 60},
	}
	if decision := gate.Observe(liveSpike, true); !decision.Allowed || decision.Dimension != "cpu" {
		t.Fatalf("uncorroborated no-wait CPU spike blocked work: %+v", decision)
	}
	liveSpike.Snapshot.HostCPURollingPercent = 97
	if decision := gate.Observe(liveSpike, true); decision.Allowed || decision.Dimension != "cpu" {
		t.Fatalf("corroborated no-wait CPU red admitted work: %+v", decision)
	}
	liveSpike.Snapshot.HostCPURollingAvailable = false
	if decision := gate.Observe(liveSpike, true); !decision.Allowed {
		t.Fatalf("missing rolling evidence blocked no-wait CPU-only work: %+v", decision)
	}
}

func TestWorkProgressIsActionableInHumanAndJSONLForms(t *testing.T) {
	now := time.Date(2026, 7, 14, 16, 0, 0, 0, time.UTC)
	var human bytes.Buffer
	reporter := newWorkProgressReporter(WorkProgressHuman, &human, workTestOperation, WorkClassTest, 3, now.Add(-5*time.Second))
	reporter.now = func() time.Time { return now }
	reporter.emit(WorkProgress{Stage: "waiting", Blocker: WorkBlockerFairness, QueuePosition: 2, QueueDepth: 3, Used: 6, Capacity: 8, Available: 2, Reason: "an older waiter owns the next reservation", NextCheckSeconds: 2})
	for _, fragment := range []string{"elapsed=5s", "blocker=fairness", "queue=2/3", "capacity=6/8", "next=2s", "older waiter"} {
		if !strings.Contains(human.String(), fragment) {
			t.Fatalf("human progress missing %q: %s", fragment, human.String())
		}
	}
	var jsonl bytes.Buffer
	reporter = newWorkProgressReporter(WorkProgressJSONL, &jsonl, workTestOperation, WorkClassBrowser, 2, now)
	reporter.now = func() time.Time { return now }
	reporter.emit(WorkProgress{Stage: "waiting", Blocker: WorkBlockerCapacity, QueuePosition: 1, QueueDepth: 1, Used: 8, Capacity: 8})
	var progress WorkProgress
	if err := json.Unmarshal(bytes.TrimSpace(jsonl.Bytes()), &progress); err != nil {
		t.Fatal(err)
	}
	if progress.OperationID != workTestOperation || progress.Blocker != WorkBlockerCapacity || progress.QueuePosition != 1 || progress.Capacity != 8 {
		t.Fatalf("JSONL progress=%+v", progress)
	}
}

func TestRunWorkCommandForwardsCancellationToProcessGroupAndClosesLedger(t *testing.T) {
	dir := t.TempDir()
	gateMarker := dir + "/gate-opened"
	childPIDPath := dir + "/descendant.pid"
	t.Setenv("NICOS_WORK_GATE_TEST_MARKER", gateMarker)
	t.Setenv("NICOS_WORK_SIGNAL_TREE", childPIDPath)
	t.Setenv("CODEX_THREAD_ID", "do-not-persist-this-session-secret")
	coordinator := NewWorkCoordinator(dir, testWorkLimits())
	signals := make(chan os.Signal, 1)
	type outcome struct {
		code int
		err  error
	}
	result := make(chan outcome, 1)
	go func() {
		code, err := RunWorkCommand(
			coordinator,
			WorkRunOptions{Class: WorkClassTest, Wait: time.Second, Progress: WorkProgressQuiet, Command: []string{os.Args[0], "-test.run=^TestWorkSignalTreeHelperProcess$"}},
			func() Admission { return Admission{Allowed: true, Level: LevelNormal} },
			time.Millisecond,
			WorkRunStreams{Stdout: io.Discard, Stderr: io.Discard, Signals: signals, CommandFactory: testGatedWorkCommand},
		)
		result <- outcome{code: code, err: err}
	}()
	body := waitForTestFile(t, childPIDPath, 3*time.Second)
	childPID, err := strconv.Atoi(strings.TrimSpace(string(body)))
	if err != nil {
		t.Fatal(err)
	}
	signals <- syscall.SIGTERM
	select {
	case completed := <-result:
		if completed.err != nil || completed.code != 128+int(syscall.SIGTERM) {
			t.Fatalf("cancelled work: code=%d err=%v", completed.code, completed.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled process group did not exit")
	}
	deadline := time.Now().Add(2 * time.Second)
	for processAlive(childPID) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if processAlive(childPID) {
		t.Fatalf("descendant PID %d survived process-group cancellation", childPID)
	}
	status, err := coordinator.Status(context.Background())
	if err != nil || status.Used != 0 || len(status.Leases) != 0 || status.QueueDepth != 0 {
		t.Fatalf("cancel status=%+v err=%v", status, err)
	}
	events, err := NewWorkEventStore(dir).Read(WorkEventFilter{})
	if err != nil || len(events) != 4 || events[3].Event != WorkEventCancelled || events[3].Outcome != "wrapper_interrupt" {
		t.Fatalf("cancel events=%+v err=%v", events, err)
	}
	// P5 forensics: forwarded signals count as wrapper interrupts.
	stats := SummarizeWorkEvents(events, time.Time{}, time.Now())
	if stats.ReviewSignals.WrapperInterruptOperations < 1 {
		t.Fatalf("wrapper interrupt not counted: %+v", stats.ReviewSignals)
	}
	serialized, err := json.Marshal(events)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(serialized), "do-not-persist") || strings.Contains(string(serialized), childPIDPath) || strings.Contains(string(serialized), "-test.run") {
		t.Fatalf("work ledger leaked command/session content: %s", serialized)
	}
}

func TestFinalizeWorkLeasePersistsTerminalBeforeRelease(t *testing.T) {
	order := make([]string, 0, 2)
	terminalErr := errors.New("terminal receipt failed")
	releaseErr := errors.New("lease release failed")
	err := finalizeWorkLease(
		func() error {
			order = append(order, "terminal")
			return terminalErr
		},
		func() error {
			order = append(order, "release")
			return releaseErr
		},
	)
	if !reflect.DeepEqual(order, []string{"terminal", "release"}) {
		t.Fatalf("finalization order=%v", order)
	}
	if !errors.Is(err, terminalErr) || !errors.Is(err, releaseErr) {
		t.Fatalf("finalization did not preserve both errors: %v", err)
	}
}

func TestWorkSignalTreeHelperProcess(t *testing.T) {
	path := os.Getenv("NICOS_WORK_SIGNAL_TREE")
	if path == "" {
		return
	}
	child := exec.Command("/bin/sleep", "30")
	if err := child.Start(); err != nil {
		os.Exit(120)
	}
	if err := atomicWrite(path, []byte(fmt.Sprintf("%d\n", child.Process.Pid)), 0o600); err != nil {
		_ = child.Process.Kill()
		os.Exit(121)
	}
	if err := child.Wait(); err != nil {
		os.Exit(122)
	}
}

func waitForTestFile(t *testing.T, path string, timeout time.Duration) []byte {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		body, err := os.ReadFile(path)
		if err == nil {
			return body
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s: %v", path, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
