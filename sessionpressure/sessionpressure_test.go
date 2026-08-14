package sessionpressure

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/nstranquist/session-pressure/internal/filelock"
)

type fixtureRunner struct {
	ps       string
	pressure string
	swap     string
	physical string
	err      error
}

func (runner fixtureRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	if runner.err != nil {
		return nil, runner.err
	}
	if strings.HasSuffix(name, "/ps") {
		return []byte(runner.ps), nil
	}
	if strings.HasSuffix(name, "/memory_pressure") {
		return []byte(runner.pressure), nil
	}
	if strings.HasSuffix(name, "/sysctl") && len(args) > 1 && args[0] == "-n" {
		return []byte(runner.physical), nil
	}
	if strings.HasSuffix(name, "/sysctl") {
		return []byte(runner.swap), nil
	}
	return nil, errors.New("unexpected command")
}

const processFixture = `
100 1 40000 0.1 00:20:00 node /opt/bin/codex --yolo resume 019f5be0-7d38-7271-ba7d-8ade4a407bf0
101 100 300000 0.4 00:19:59 /opt/vendor/bin/codex --yolo
102 101 20000 0.2 00:19:00 /opt/codex-code-mode-host
200 1 50000 0.0 01:00:00 claude
201 200 100000 0.1 00:59:00 helper
300 1 90000 0.0 00:05:00 /Applications/Claude.app/Contents/Helpers/chrome-native-host
301 1 1000 0.0 00:00:01 rg codex
999 1 20480 0.1 00:10:00 /tmp/ndev session pressure monitor run
`

func testSampler(now time.Time) *Sampler {
	return &Sampler{
		runner: fixtureRunner{
			ps: processFixture, pressure: "System-wide memory free percentage: 51%\n",
			swap:     "vm.swapusage: total = 0.00M  used = 0.00M  free = 0.00M  (encrypted)\n",
			physical: "17179869184\n",
		},
		now: func() time.Time { return now }, pid: 999, role: "resident",
		peakRSS:         func() float64 { return 24 },
		logicalCPUCount: func() int { return 10 },
	}
}

func TestExecRunnerErrorsExcludePartialCommandOutput(t *testing.T) {
	t.Setenv("GO_WANT_SESSION_PRESSURE_HELPER", "1")
	_, err := (execRunner{}).Run(context.Background(), os.Args[0], "-test.run=TestExecRunnerOutputHelper")
	if err == nil {
		t.Fatal("helper command unexpectedly succeeded")
	}
	if strings.Contains(err.Error(), "prompt-secret-canary") {
		t.Fatalf("durable probe error leaked command output: %v", err)
	}
}

func TestExecRunnerOutputHelper(t *testing.T) {
	if os.Getenv("GO_WANT_SESSION_PRESSURE_HELPER") != "1" {
		return
	}
	fmt.Fprint(os.Stdout, "prompt-secret-canary")
	os.Exit(7)
}

func TestSignalTreePIDsTerminatesSacrificialProcess(t *testing.T) {
	helperCtx, helperCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer helperCancel()
	command := exec.CommandContext(helperCtx, os.Args[0], "-test.run=TestSignalTargetHelper")
	command.Env = append(os.Environ(), "GO_WANT_SESSION_PRESSURE_SIGNAL_TARGET=1")
	ready, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := command.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
			t.Errorf("kill sacrificial process: %v", err)
		}
	})
	buffer := make([]byte, len("ready\n"))
	if _, err := io.ReadFull(ready, buffer); err != nil || string(buffer) != "ready\n" {
		t.Fatalf("signal target readiness=%q err=%v", buffer, err)
	}
	if err := signalTreePIDs([]int{command.Process.Pid}, nil); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("sacrificial process did not exit cleanly after SIGTERM: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("sacrificial process ignored SIGTERM")
	}
}

func TestSignalTargetHelper(t *testing.T) {
	if os.Getenv("GO_WANT_SESSION_PRESSURE_SIGNAL_TARGET") != "1" {
		return
	}
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGTERM)
	fmt.Fprintln(os.Stdout, "ready")
	<-signals
}

func TestSamplerBuildsWholeAgentTreesWithoutCommandSubstringFalsePositives(t *testing.T) {
	now := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	sampler := testSampler(now)
	policy := DefaultPolicy(16 * 1024)
	snapshot, err := sampler.Sample(context.Background(), policy)
	if err != nil {
		t.Fatalf("Sample: %v", err)
	}
	if snapshot.Level != LevelNormal || snapshot.FreePercent != 51 || snapshot.AgentTreeCount != 2 {
		t.Fatalf("unexpected snapshot: %+v", snapshot)
	}
	if snapshot.GuardRSSMB != 20 {
		t.Fatalf("guard RSS = %.1f, want 20", snapshot.GuardRSSMB)
	}
	if snapshot.GuardPeakRSSMB != 24 {
		t.Fatalf("guard peak RSS = %.1f, want 24", snapshot.GuardPeakRSSMB)
	}
	if snapshot.LogicalCPUCount != 10 || math.Abs(snapshot.HostCPUPercent-0.09) > 0.001 || math.Abs(snapshot.AgentCPUPercent-0.08) > 0.001 || !snapshot.AgentCPUAvailable {
		t.Fatalf("unexpected normalized CPU projection: logical=%d host=%.3f agent=%.3f", snapshot.LogicalCPUCount, snapshot.HostCPUPercent, snapshot.AgentCPUPercent)
	}
	codex := snapshot.TopAgentTrees[0]
	if codex.Agent != "codex" || codex.RootPID != 100 || codex.ProcessCount != 3 || codex.SessionID == "" {
		t.Fatalf("unexpected codex tree: %+v", codex)
	}
	if codex.RSSSumMB < 351 || codex.RSSSumMB > 352 {
		t.Fatalf("codex RSS sum = %.1f", codex.RSSSumMB)
	}
	if got := snapshot.TopAgentTrees[1].RootPID; got != 200 {
		t.Fatalf("second root PID = %d, want 200", got)
	}
}

func TestSamplerPropagatesUnavailableProcessCPUToAggregateFallbacks(t *testing.T) {
	sampler := testSampler(time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC))
	sampler.hostCPUSource = func() (hostCPUTicks, error) {
		return hostCPUTicks{}, errors.New("fixture Mach CPU unavailable")
	}
	sampler.processSource = func(context.Context) ([]Process, string, error) {
		return []Process{{
			PID: 100, PPID: 1, RSSKB: 1024, CPUPercent: 0, CPUAvailable: false,
			Agent: "codex", Executable: "codex", ElapsedSeconds: 3600,
		}}, "fixture-native", nil
	}
	snapshot, err := sampler.Sample(context.Background(), DefaultPolicy(16*1024))
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.HostCPUAvailable || snapshot.AgentCPUAvailable || snapshot.HostCPUPercent != 0 || snapshot.AgentCPUPercent != 0 {
		t.Fatalf("partial process CPU evidence became authoritative: %+v", snapshot)
	}
}

func TestResidentSamplerCachesHealthyInventoryAndRefreshesOnSchedule(t *testing.T) {
	now := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	sampler := testSampler(now)
	sampler.now = func() time.Time { return now }
	var reads atomic.Int32
	sampler.processSource = func(context.Context) ([]Process, string, error) {
		reads.Add(1)
		return []Process{{PID: 999, PPID: 1, RSSKB: 2048}}, "fixture-native", nil
	}
	policy := DefaultPolicy(16 * 1024)
	first, err := sampler.Sample(context.Background(), policy)
	if err != nil || reads.Load() != 1 || !first.ProcessInventoryFresh {
		t.Fatalf("first sample reads=%d snapshot=%+v err=%v", reads.Load(), first, err)
	}
	now = now.Add(time.Duration(policy.SampleIntervalSeconds) * time.Second)
	second, err := sampler.Sample(context.Background(), policy)
	if err != nil || reads.Load() != 1 || second.ProcessInventoryFresh || second.ProcessInventoryAgeSeconds <= 0 {
		t.Fatalf("cached sample reads=%d snapshot=%+v err=%v", reads.Load(), second, err)
	}
	now = now.Add(time.Duration(policy.ProcessInventoryIntervalSeconds) * time.Second)
	third, err := sampler.Sample(context.Background(), policy)
	if err != nil || reads.Load() < 1 || third.ProcessInventoryFresh {
		t.Fatalf("scheduled refresh should leave heartbeat on cached inventory: reads=%d snapshot=%+v err=%v", reads.Load(), third, err)
	}
	deadline := time.Now().Add(time.Second)
	for reads.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if reads.Load() != 2 {
		t.Fatalf("scheduled refresh did not finish: reads=%d", reads.Load())
	}
	fourth, err := sampler.Sample(context.Background(), policy)
	got := sampler.currentProcessInventory()
	if err != nil || fourth.ProcessInventoryFresh || len(got) != 1 || got[0].PID != 999 {
		t.Fatalf("scheduled refresh did not publish cached result: reads=%d snapshot=%+v inventory=%+v err=%v", reads.Load(), fourth, got, err)
	}
}

func TestResidentScheduledInventoryRefreshDoesNotBlockHeartbeat(t *testing.T) {
	now := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	sampler := testSampler(now)
	sampler.now = func() time.Time { return now }
	var calls atomic.Int32
	refreshStarted := make(chan struct{})
	releaseRefresh := make(chan struct{})
	sampler.processSource = func(ctx context.Context) ([]Process, string, error) {
		if calls.Add(1) == 1 {
			return []Process{{PID: 999, PPID: 1, RSSKB: 2048}}, "fixture-native", nil
		}
		close(refreshStarted)
		select {
		case <-releaseRefresh:
			return []Process{{PID: 1000, PPID: 1, RSSKB: 4096}}, "fixture-native", nil
		case <-ctx.Done():
			return nil, "fixture-native", ctx.Err()
		}
	}
	policy := DefaultPolicy(16 * 1024)
	if _, err := sampler.Sample(context.Background(), policy); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Duration(policy.ProcessInventoryIntervalSeconds) * time.Second)
	started := time.Now()
	if _, err := sampler.Sample(context.Background(), policy); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(started); elapsed > residentInventoryRefreshDelay*5 {
		t.Fatalf("scheduled inventory refresh blocked heartbeat for %s", elapsed)
	}
	select {
	case <-refreshStarted:
	case <-time.After(time.Second):
		t.Fatal("scheduled inventory refresh did not start")
	}
	close(releaseRefresh)
	deadline := time.Now().Add(time.Second)
	for sampler.currentProcessInventory()[0].PID != 1000 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := sampler.currentProcessInventory(); len(got) != 1 || got[0].PID != 1000 {
		t.Fatalf("scheduled inventory refresh did not publish: %+v", got)
	}
}

func TestResidentScheduledPowerThermalRefreshDoesNotBlockHeartbeat(t *testing.T) {
	now := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	sampler := testSampler(now)
	sampler.now = func() time.Time { return now }
	var calls atomic.Int32
	refreshStarted := make(chan struct{})
	releaseRefresh := make(chan struct{})
	sampler.powerThermalSource = func(ctx context.Context) (PowerThermalStatus, error) {
		count := calls.Add(1)
		if count == 1 {
			return PowerThermalStatus{ThermalState: ThermalStateNominal, ThermalAvailable: true, Source: "fixture"}, nil
		}
		if count == 2 {
			close(refreshStarted)
			select {
			case <-releaseRefresh:
			case <-ctx.Done():
				return PowerThermalStatus{ThermalState: ThermalStateUnknown, Source: "fixture", Error: ctx.Err().Error()}, ctx.Err()
			}
		}
		return PowerThermalStatus{ThermalState: ThermalStateSerious, ThermalAvailable: true, Source: "fixture"}, nil
	}
	policy := DefaultPolicy(16 * 1024)
	first, err := sampler.Sample(context.Background(), policy)
	if err != nil || first.ThermalState != ThermalStateNominal || calls.Load() != 1 {
		t.Fatalf("first thermal sample=%+v calls=%d err=%v", first, calls.Load(), err)
	}
	now = now.Add(time.Duration(policy.SampleIntervalSeconds) * time.Second)
	started := time.Now()
	second, err := sampler.Sample(context.Background(), policy)
	if err != nil || second.ThermalState != ThermalStateNominal || time.Since(started) > residentPowerThermalRefreshDelay*5 {
		t.Fatalf("scheduled thermal refresh blocked heartbeat: snapshot=%+v calls=%d elapsed=%s err=%v", second, calls.Load(), time.Since(started), err)
	}
	select {
	case <-refreshStarted:
	case <-time.After(time.Second):
		t.Fatal("scheduled thermal refresh did not start")
	}
	// A warning sample waits for the in-flight probe before it accepts the
	// cached thermal signal. This keeps a serious state from hiding behind an
	// asynchronous normal heartbeat.
	sampler.runner = fixtureRunner{
		pressure: "System-wide memory free percentage: 24%\n",
		swap:     "vm.swapusage: total = 4096.00M used = 0.00M free = 4096.00M",
		physical: "17179869184\n",
	}
	forcedDone := make(chan Snapshot, 1)
	go func() {
		snapshot, sampleErr := sampler.Sample(context.Background(), policy)
		if sampleErr != nil {
			forcedDone <- Snapshot{PowerThermalError: sampleErr.Error()}
			return
		}
		forcedDone <- snapshot
	}()
	select {
	case snapshot := <-forcedDone:
		t.Fatalf("forced warning sample accepted stale thermal state: %+v", snapshot)
	case <-time.After(25 * time.Millisecond):
	}
	close(releaseRefresh)
	select {
	case snapshot := <-forcedDone:
		if snapshot.Level != LevelWarning || snapshot.ThermalState != ThermalStateSerious || !snapshot.ThermalAvailable {
			t.Fatalf("forced warning sample=%+v", snapshot)
		}
	case <-time.After(time.Second):
		t.Fatal("forced thermal sample did not finish")
	}
}

func TestResidentSamplerForcesFreshInventoryUnderMemoryPressure(t *testing.T) {
	now := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	sampler := testSampler(now)
	sampler.now = func() time.Time { return now }
	reads := 0
	sampler.processSource = func(context.Context) ([]Process, string, error) {
		reads++
		return []Process{{PID: 999, PPID: 1, RSSKB: 2048}}, "fixture-native", nil
	}
	policy := DefaultPolicy(16 * 1024)
	if _, err := sampler.Sample(context.Background(), policy); err != nil {
		t.Fatal(err)
	}
	sampler.runner = fixtureRunner{
		pressure: "System-wide memory free percentage: 24%",
		swap:     "vm.swapusage: total = 4096.00M used = 0.00M free = 4096.00M",
		physical: "17179869184\n",
	}
	now = now.Add(time.Duration(policy.SampleIntervalSeconds) * time.Second)
	snapshot, err := sampler.Sample(context.Background(), policy)
	if err != nil || reads != 2 || !snapshot.ProcessInventoryFresh || snapshot.Level != LevelWarning {
		t.Fatalf("pressure refresh reads=%d snapshot=%+v err=%v", reads, snapshot, err)
	}
}

func TestResidentSamplerForcesFreshInventoryWhileAgentTreeIsPressured(t *testing.T) {
	now := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	sampler := testSampler(now)
	sampler.now = func() time.Time { return now }
	reads := 0
	sampler.processSource = func(context.Context) ([]Process, string, error) {
		reads++
		return []Process{{
			PID: 100, PPID: 1, RSSKB: int64(3073 * 1024),
			Agent: "codex", Executable: "codex", ElapsedSeconds: 3600,
		}}, "fixture-native", nil
	}
	policy := DefaultPolicy(16 * 1024)
	first, err := sampler.Sample(context.Background(), policy)
	if err != nil || reads != 1 || first.Level != LevelWarning || !first.ProcessInventoryFresh {
		t.Fatalf("first pressure sample reads=%d snapshot=%+v err=%v", reads, first, err)
	}
	now = now.Add(time.Duration(policy.PressureSampleIntervalSeconds) * time.Second)
	second, err := sampler.Sample(context.Background(), policy)
	if err != nil || reads != 2 || second.Level != LevelWarning || !second.ProcessInventoryFresh {
		t.Fatalf("agent pressure refresh reads=%d snapshot=%+v err=%v", reads, second, err)
	}
}

func TestResidentNativeInventoryFailureDoesNotFallBackToPS(t *testing.T) {
	now := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	sampler := testSampler(now)
	sampler.processSource = func(context.Context) ([]Process, string, error) {
		return nil, "fixture-native", errors.New("fixture native inventory unavailable")
	}
	snapshot, err := sampler.Sample(context.Background(), DefaultPolicy(16*1024))
	if err != nil {
		t.Fatalf("resident host sample failed with native inventory: %v", err)
	}
	if snapshot.ProcessInventoryAvailable || snapshot.ProcessInventoryFresh || !strings.Contains(snapshot.ProcessInventoryError, "fixture native inventory unavailable") {
		t.Fatalf("resident did not fail lightweight: %+v", snapshot)
	}
}

func TestAdmissionHostProbeDropsStaleResidentInventory(t *testing.T) {
	now := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	sampler := testSampler(now)
	policy := DefaultPolicy(16 * 1024)
	latest := Snapshot{
		Timestamp: now.Add(-time.Minute), ProcessInventoryAvailable: true,
		ProcessInventoryCapturedAt: now.Add(-10 * time.Minute), ProcessInventorySource: "fixture",
		AgentRSSSumMB:  policy.Thresholds.AgentTotalCriticalMB,
		AgentTreeCount: 10, TopAgentTrees: []AgentTree{{Agent: "codex", RootPID: 42}},
	}
	snapshot, err := sampler.SampleHost(context.Background(), policy, &latest)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.ProcessInventoryAvailable || snapshot.HostCPUPercent != 0 || snapshot.AgentRSSSumMB != 0 || snapshot.Level != LevelNormal {
		t.Fatalf("stale inventory affected admission: %+v", snapshot)
	}
}

func TestAdmissionHostProbeMeasuresCurrentCPUInsteadOfTrustingResidentCPU(t *testing.T) {
	now := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	sampler := testSampler(now)
	hostCPUCalls := 0
	sampler.hostCPUSource = func() (hostCPUTicks, error) {
		hostCPUCalls++
		if hostCPUCalls == 1 {
			return hostCPUTicks{Busy: 100, Total: 1000}, nil
		}
		return hostCPUTicks{Busy: 195, Total: 1100}, nil
	}
	policy := DefaultPolicy(16 * 1024)
	latest := Snapshot{
		Timestamp: now.Add(-time.Second), PhysicalMemoryMB: 12345, HostCPUPercent: 42, HostCPUAvailable: true,
		HostCPUSource: "mach-host-statistics", HostCPUSampleWindowMS: 90000,
	}
	snapshot, err := sampler.SampleHost(context.Background(), policy, &latest)
	if err != nil {
		t.Fatal(err)
	}
	if hostCPUCalls < 2 || snapshot.PhysicalMemoryMB != latest.PhysicalMemoryMB || !snapshot.HostCPUAvailable || snapshot.HostCPUPercent != 95 || snapshot.Level != LevelRed || snapshot.HostCPULiveWindowMS < 250 || !snapshot.HostCPURollingAvailable || snapshot.HostCPURollingPercent != 42 {
		t.Fatalf("admission did not replace stale resident CPU with a current window: calls=%d snapshot=%+v", hostCPUCalls, snapshot)
	}
}

func TestAdmissionHostProbeWaitsForMachCountersToAdvance(t *testing.T) {
	now := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	sampler := testSampler(now)
	hostCPUCalls := 0
	sampler.hostCPUSource = func() (hostCPUTicks, error) {
		hostCPUCalls++
		if hostCPUCalls < 4 {
			return hostCPUTicks{Busy: 100, Total: 1000}, nil
		}
		return hostCPUTicks{Busy: 195, Total: 1100}, nil
	}
	snapshot, err := sampler.SampleHost(context.Background(), DefaultPolicy(16*1024), nil)
	if err != nil {
		t.Fatal(err)
	}
	if hostCPUCalls < 4 || !snapshot.HostCPUAvailable || snapshot.HostCPUPercent != 95 || snapshot.Level != LevelRed || snapshot.HostCPULiveWindowMS < 250 {
		t.Fatalf("admission accepted unchanged Mach counters: calls=%d snapshot=%+v", hostCPUCalls, snapshot)
	}
}

func TestAdmissionSurfacesPartialHostCPUFailureWhileEnforcingMemory(t *testing.T) {
	now := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	sampler := testSampler(now)
	sampler.runner = fixtureRunner{
		pressure: "System-wide memory free percentage: 14%\n",
		swap:     "vm.swapusage: total = 0.00M used = 0.00M free = 0.00M\n",
		physical: "17179869184\n",
	}
	sampler.hostCPUSource = func() (hostCPUTicks, error) {
		return hostCPUTicks{}, errors.New("fixture Mach probe unavailable")
	}
	policy := DefaultPolicy(16 * 1024)
	policy.EnforceAdmission = true
	snapshot, err := sampler.SampleHost(context.Background(), policy, nil)
	if err != nil {
		t.Fatal(err)
	}
	admission := AdmissionForSnapshot(snapshot, policy, "test")
	if admission.Allowed || admission.Level != LevelRed || admission.Warning == "" || !strings.Contains(admission.Warning, "CPU admission failed open") {
		t.Fatalf("partial CPU failure was not visible while memory remained enforced: %+v", admission)
	}
	if admission.Snapshot == nil || admission.Snapshot.HostCPUAvailable || !strings.Contains(admission.Snapshot.HostCPUError, "fixture Mach probe unavailable") {
		t.Fatalf("partial CPU failure evidence missing: %+v", admission.Snapshot)
	}
}

func TestResidentFirstSampleWaitsForMachCountersToAdvance(t *testing.T) {
	now := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	sampler := testSampler(now)
	hostCPUCalls := 0
	sampler.hostCPUSource = func() (hostCPUTicks, error) {
		hostCPUCalls++
		if hostCPUCalls == 1 {
			return hostCPUTicks{Busy: 100, Total: 1000}, nil
		}
		return hostCPUTicks{Busy: 195, Total: 1100}, nil
	}
	snapshot, err := sampler.Sample(context.Background(), DefaultPolicy(16*1024))
	if err != nil {
		t.Fatal(err)
	}
	if hostCPUCalls < 2 || snapshot.HostCPUSource != "mach-host-statistics" || snapshot.HostCPUPercent != 95 || snapshot.Level != LevelRed || snapshot.HostCPULiveWindowMS < 250 || !snapshot.HostCPURollingAvailable {
		t.Fatalf("resident first sample used an untrusted CPU baseline: calls=%d snapshot=%+v", hostCPUCalls, snapshot)
	}
}

func TestAgentForCommandRecognizesGrok(t *testing.T) {
	for _, command := range []string{
		"grok --resume 019f5be0-7d38-7271-ba7d-8ade4a407bf0",
		"node /opt/bin/grok --continue",
		"/Users/nico/.grok/bin/grok",
		"/Users/nico/.grok/downloads/grok-0.2.118-macos-aarch64",
		"grok-0.2.118-mac",
	} {
		agent, executable, ok := agentForCommand(command)
		if !ok || agent != "grok" || executable != "grok" {
			t.Fatalf("agentForCommand(%q) = %q, %q, %v", command, agent, executable, ok)
		}
	}
}

func TestSamplerKeepsAggregateTruthBeyondBoundedTopTreeProjection(t *testing.T) {
	processRows := []string{"999 1 2048 0.0 00:10:00 /tmp/ndev-session-pressure"}
	for pid := 1000; pid < 1070; pid++ {
		processRows = append(processRows, fmt.Sprintf("%d 1 1024 0.0 00:20:00 codex", pid))
	}
	sampler := testSampler(time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC))
	sampler.runner = fixtureRunner{
		ps: strings.Join(processRows, "\n") + "\n", pressure: "System-wide memory free percentage: 51%\n",
		swap: "vm.swapusage: total = 0.00M used = 0.00M free = 0.00M", physical: "17179869184",
	}
	snapshot, err := sampler.Sample(context.Background(), DefaultPolicy(16*1024))
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.AgentTreeCount != 70 || len(snapshot.TopAgentTrees) != maxProjectedAgentTrees || snapshot.AgentRSSSumMB != 70 {
		t.Fatalf("bounded projection changed aggregate truth: count=%d top=%d rss=%.1f", snapshot.AgentTreeCount, len(snapshot.TopAgentTrees), snapshot.AgentRSSSumMB)
	}
}

func TestEvaluateUsesHighestSeverityAndExplicitRSSSum(t *testing.T) {
	policy := DefaultPolicy(16 * 1024)
	snapshot := Snapshot{
		FreePercent: 7, SwapUsedMB: policy.Thresholds.SwapRedMB,
		AgentRSSSumMB: policy.Thresholds.AgentTotalWarningMB,
		TopAgentTrees: []AgentTree{{Agent: "codex", RootPID: 10, RSSSumMB: policy.Thresholds.TreeRedMB}},
	}
	got := Evaluate(snapshot, policy)
	if got.Level != LevelCritical {
		t.Fatalf("level = %s, want critical", got.Level)
	}
	if len(got.Reasons) < 4 || !strings.Contains(got.Reasons[0], "free memory") {
		t.Fatalf("reasons = %#v", got.Reasons)
	}
}

func TestEvaluateUsesStickySwapOnlyToCorroborateLivePressure(t *testing.T) {
	policy := DefaultPolicy(16 * 1024)
	sticky := Evaluate(Snapshot{FreePercent: 60, SwapUsedMB: policy.Thresholds.SwapCriticalMB}, policy)
	if sticky.Level != LevelNormal || len(sticky.Reasons) != 0 {
		t.Fatalf("historical swap alone must not look like active pressure: %+v", sticky)
	}
	corroboratedRed := Evaluate(Snapshot{FreePercent: 20, SwapUsedMB: policy.Thresholds.SwapRedMB}, policy)
	if corroboratedRed.Level != LevelRed || !strings.Contains(strings.Join(corroboratedRed.Reasons, " "), "with live warning pressure") {
		t.Fatalf("red swap should escalate live warning one rung: %+v", corroboratedRed)
	}
	corroboratedCritical := Evaluate(Snapshot{FreePercent: 12, SwapUsedMB: policy.Thresholds.SwapCriticalMB}, policy)
	if corroboratedCritical.Level != LevelCritical || !strings.Contains(strings.Join(corroboratedCritical.Reasons, " "), "with live red pressure") {
		t.Fatalf("critical swap should escalate live red one rung: %+v", corroboratedCritical)
	}
}

func TestEvaluateCPUWarnsAndBlocksWithoutActivatingCriticalMemoryRelief(t *testing.T) {
	policy := DefaultPolicy(16 * 1024)
	warning := Evaluate(Snapshot{FreePercent: 60, HostCPUPercent: policy.Thresholds.HostCPUWarningPercent}, policy)
	if warning.Level != LevelWarning || !strings.Contains(strings.Join(warning.Reasons, " "), "host CPU") {
		t.Fatalf("CPU warning was not classified: %+v", warning)
	}
	red := Evaluate(Snapshot{
		FreePercent: 60, HostCPUPercent: policy.Thresholds.HostCPURedPercent,
		SwapUsedMB: policy.Thresholds.SwapCriticalMB,
	}, policy)
	if red.Level != LevelRed {
		t.Fatalf("CPU-only red with historical swap must stay red, not trigger critical relief: %+v", red)
	}
	policy.EnforceAdmission = true
	admission := AdmissionForSnapshot(red, policy, "test")
	if admission.Allowed || admission.Level != LevelRed {
		t.Fatalf("CPU red must block canonical launches: %+v", admission)
	}
}

func TestNormalizedCPUPercentBoundsAggregateProcessCPU(t *testing.T) {
	for _, test := range []struct {
		raw   float64
		cores int
		want  float64
	}{
		{raw: 850, cores: 10, want: 85},
		{raw: 1200, cores: 10, want: 100},
		{raw: -1, cores: 10, want: 0},
		{raw: 50, cores: 0, want: 0},
	} {
		if got := normalizedCPUPercent(test.raw, test.cores); got != test.want {
			t.Fatalf("normalizedCPUPercent(%.1f, %d) = %.1f, want %.1f", test.raw, test.cores, got, test.want)
		}
	}
}

func TestSamplerHostCPUUsesFreshCounterDeltaWithoutProcessInventory(t *testing.T) {
	now := time.Date(2026, 7, 13, 15, 0, 0, 0, time.UTC)
	sampler := &Sampler{
		now: func() time.Time { return now },
		hostCPUSource: func() (hostCPUTicks, error) {
			return hostCPUTicks{Busy: 150, Total: 1100}, nil
		},
		hostCPULast:   hostCPUTicks{Busy: 100, Total: 1000},
		hostCPULastAt: now.Add(-time.Second),
	}
	percent, window, err := sampler.sampleHostCPU(context.Background(), true)
	if err != nil || percent != 50 || window != time.Second {
		t.Fatalf("fresh host CPU percent=%.1f window=%s err=%v", percent, window, err)
	}
	if percent, ok := hostCPUPercentBetween(hostCPUTicks{Busy: 10, Total: 100}, hostCPUTicks{Busy: 9, Total: 110}); ok || percent != 0 {
		t.Fatalf("counter regression must be rejected: percent=%.1f ok=%v", percent, ok)
	}
}

func TestParsersHandleMacOutputs(t *testing.T) {
	free, err := parseFreePercent("System-wide memory free percentage: 8%")
	if err != nil || free != 8 {
		t.Fatalf("free=%d err=%v", free, err)
	}
	for body, want := range map[string]float64{
		"vm.swapusage: total = 16.00G used = 8.50G free = 7.50G":  8704,
		"vm.swapusage: total = 0.00M used = 512.00M free = 0.00M": 512,
	} {
		got, err := parseSwapUsedMB(body)
		if err != nil || got != want {
			t.Fatalf("parseSwapUsedMB(%q) = %.1f, %v; want %.1f", body, got, err, want)
		}
	}
	if got := parseElapsedSeconds("2-03:04:05"); got != 183845 {
		t.Fatalf("elapsed = %d", got)
	}
}

func TestDefaultPolicyIsObserveOnlyAndValid(t *testing.T) {
	policy := DefaultPolicy(16 * 1024)
	if policy.EnforceAdmission || policy.AutoShedCritical {
		t.Fatalf("default policy must be observe-only: %+v", policy)
	}
	if err := policy.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if policy.Thresholds.TreeWarningMB != 3072 || policy.Thresholds.AgentTotalCriticalMB != 13312 {
		t.Fatalf("unexpected tuned thresholds: %+v", policy.Thresholds)
	}
	if policy.IntervalSeconds(LevelNormal) != 90 || policy.IntervalSeconds(LevelWarning) != 15 || policy.IntervalSeconds(LevelRed) != 15 || policy.IntervalSeconds(LevelCritical) != 5 {
		t.Fatalf("unexpected adaptive intervals: %+v", policy)
	}
	if policy.ProcessInventoryIntervalSeconds != 180 {
		t.Fatalf("unexpected healthy inventory interval: %d", policy.ProcessInventoryIntervalSeconds)
	}
	expectedPolicyLimits := defaultWorkLimits(runtime.NumCPU())
	expectedPolicyLimits.WarningCapacityEnabled = false
	if policy.WorkLimits != expectedPolicyLimits {
		t.Fatalf("unexpected host work limits: %+v", policy.WorkLimits)
	}
	if got := defaultWorkLimits(10); got != (WorkLimits{
		SchedulingPolicy: WorkSchedulingPolicy,
		Capacity:         8, WarningCapacity: 4, WarningCapacityEnabled: true, TestWeight: 3, BuildWeight: 5, ExpressTestWeight: 1, ExpressBuildWeight: 2,
		EmulatorWeight: 5, BrowserWeight: 2, HeavyWeight: 6, BenchmarkWeight: 6, InstallWeight: 1, ReclaimWeight: 1,
		CPUBlockSamples: 2, CPUReleaseSamples: 2, CPUReleasePercent: 80,
		// The fast lane defaults on but narrow: only the express classes are light
		// enough for the weight ceiling, and only sub-minute measured work is short
		// enough. It can only reduce blocking, never widen the weighted ceiling.
		FastLaneEnabled: true, FastLaneMaxWeight: 2, FastLaneMaxRuntimeMS: 120_000,
		FastLaneCoordinatedCPUCeilingPct: 50, FastLaneDefaultsRevision: fastLaneDefaultsRevision,
	}) {
		t.Fatalf("unexpected 10-core work limits: %+v", got)
	}
}

func TestGuardBudgetDoesNotUseImmatureP95(t *testing.T) {
	policy := DefaultPolicy(16 * 1024)
	policy.ResourceBudgets.MaxSampleDurationMS = 2000
	policy.ResourceBudgets.MaxSampleCPUTimeMS = 100
	// After SustainSamples the rolling p95 still equals the boot-time max.
	// Budget the current cheap sample, not the isolated 6.6s login inventory.
	immature := Snapshot{
		Level: LevelNormal, GuardRole: "resident", GuardBudgetApplicable: true,
		MonitorSamples: policy.SustainSamples + 3, NormalMonitorSamples: policy.SustainSamples + 2,
		SampleDurationMS: 236, SampleDurationP95MS: 6605.634, SampleDurationMaxMS: 6605.634,
		SampleCPUTimeMS: 40, SampleCPUTimeP95MS: 107.43, SampleCPUTimeMaxMS: 107.43,
		GuardRSSMB: 15, GuardIdleCPUDutyPercent: 0.14,
	}
	evaluated := Evaluate(immature, policy)
	if !evaluated.GuardBudgetOK {
		t.Fatalf("immature p95 must not fail budget: %v", evaluated.GuardBudgetReasons)
	}
	mature := immature
	mature.MonitorSamples = p95SingleOutlierExclusionSamples
	evaluated = Evaluate(mature, policy)
	if evaluated.GuardBudgetOK || !strings.Contains(strings.Join(evaluated.GuardBudgetReasons, " "), "rolling sample p95 6605.6 ms") {
		t.Fatalf("mature p95 must fail duration budget: ok=%v reasons=%v", evaluated.GuardBudgetOK, evaluated.GuardBudgetReasons)
	}
	// Current-sample CPU over budget still fails before p95 maturity.
	hot := immature
	hot.SampleCPUTimeMS = 106.4
	evaluated = Evaluate(hot, policy)
	if evaluated.GuardBudgetOK || !strings.Contains(strings.Join(evaluated.GuardBudgetReasons, " "), "current sample CPU 106.4 ms") {
		t.Fatalf("current CPU over budget must still fail: ok=%v reasons=%v", evaluated.GuardBudgetOK, evaluated.GuardBudgetReasons)
	}
	// A 4s wall sample with cheap CPU is host wait, not a hang.
	wait := immature
	wait.SampleDurationMS = 4140
	wait.SampleCPUTimeMS = 40
	evaluated = Evaluate(wait, policy)
	if !evaluated.GuardBudgetOK {
		t.Fatalf("immature wall wait must not fail 2s duration budget: %v", evaluated.GuardBudgetReasons)
	}
	hung := wait
	hung.SampleDurationMS = 11000
	evaluated = Evaluate(hung, policy)
	if evaluated.GuardBudgetOK || !strings.Contains(strings.Join(evaluated.GuardBudgetReasons, " "), "hang budget") {
		t.Fatalf("immature hang must fail: ok=%v reasons=%v", evaluated.GuardBudgetOK, evaluated.GuardBudgetReasons)
	}
}

func TestGuardBudgetIgnoresLifetimePeakRSS(t *testing.T) {
	policy := DefaultPolicy(16 * 1024)
	stats := monitorStats{}
	for range policy.SustainSamples {
		stats.add(Snapshot{
			Level: LevelNormal, SampleDurationMS: 40, SampleCPUTimeMS: 10,
			GuardRSSMB: 17, GuardPeakRSSMB: 88, GuardCPUPercent: 0.01,
		}, float64(policy.SampleIntervalSeconds))
	}
	snapshot := Snapshot{
		Level: LevelNormal, GuardRole: "resident", GuardBudgetApplicable: true,
		GuardRSSMB: 17, GuardPeakRSSMB: 88,
	}
	stats.apply(&snapshot, policy)
	if snapshot.GuardRSSMaxMB != 17 {
		t.Fatalf("rolling max RSS = %.1f, want current-only 17 (lifetime peak must not latch)", snapshot.GuardRSSMaxMB)
	}
	evaluated := Evaluate(snapshot, policy)
	if !evaluated.GuardBudgetOK {
		t.Fatalf("lifetime ru_maxrss spike failed budget: %v", evaluated.GuardBudgetReasons)
	}
	snapshot.GuardRSSMB = 40
	stats.add(Snapshot{
		Level: LevelNormal, SampleDurationMS: 40, SampleCPUTimeMS: 10,
		GuardRSSMB: 40, GuardPeakRSSMB: 88, GuardCPUPercent: 0.01,
	}, float64(policy.SampleIntervalSeconds))
	stats.apply(&snapshot, policy)
	evaluated = Evaluate(snapshot, policy)
	if evaluated.GuardBudgetOK || !strings.Contains(strings.Join(evaluated.GuardBudgetReasons, " "), "self RSS 40.0 MB") {
		t.Fatalf("current RSS above budget should fail: ok=%v reasons=%v", evaluated.GuardBudgetOK, evaluated.GuardBudgetReasons)
	}
}

func TestMonitorStatsUseRollingP95AndProjectedSparseTelemetry(t *testing.T) {
	policy := DefaultPolicy(16 * 1024)
	stats := monitorStats{}
	for index := 0; index < 20; index++ {
		duration := 40.0
		if index == 0 {
			duration = 550
		}
		stats.add(Snapshot{SampleDurationMS: duration, SampleCPUTimeMS: 30, GuardRSSMB: 8, GuardCPUPercent: 0.1}, 15)
	}
	stats.recordEventBytes("heartbeat", 500)
	stats.recordEventBytes("state_transition", 2600)
	stats.sampleErrors = 2
	stats.resourceCleanupFailures = 3
	snapshot := Snapshot{GuardRole: "resident", GuardBudgetApplicable: true, TelemetryBytesToday: 2600}
	stats.apply(&snapshot, policy)
	if snapshot.MonitorSamples != 20 || snapshot.SampleDurationP95MS != 40 || snapshot.SampleDurationMaxMS != 550 {
		t.Fatalf("unexpected rolling duration stats: %+v", snapshot)
	}
	if snapshot.TelemetryProjectedBytesDay != 528384 {
		t.Fatalf("telemetry projection = %d, want 528384", snapshot.TelemetryProjectedBytesDay)
	}
	stats.recordActionBytes(400)
	stats.apply(&snapshot, policy)
	if snapshot.TelemetryProjectedBytesDay != 643584 {
		t.Fatalf("action-aware telemetry projection = %d, want 643584", snapshot.TelemetryProjectedBytesDay)
	}
	if snapshot.SampleCPUTimeP95MS != 30 || math.Abs(snapshot.GuardCPUDutyPercent-0.2) > 0.0001 {
		t.Fatalf("unexpected CPU stats: %+v", snapshot)
	}
	if snapshot.GuardSampleErrors != 2 || snapshot.ResourceCleanupFailures != 3 {
		t.Fatalf("closed guard failure counters missing: %+v", snapshot)
	}
	evaluated := Evaluate(snapshot, policy)
	if !evaluated.GuardBudgetOK {
		t.Fatalf("one latency outlier should not fail rolling p95 budget: %+v", evaluated.GuardBudgetReasons)
	}
}

func TestMonitorStatsReserveFirstAutoReliefAction(t *testing.T) {
	policy := DefaultPolicy(16 * 1024)
	policy.EnforceAdmission = true
	policy.AutoShedCritical = true
	stats := monitorStats{}
	stats.add(Snapshot{Level: LevelNormal, SampleDurationMS: 40, SampleCPUTimeMS: 10, GuardRSSMB: 8}, 90)
	stats.recordEventBytes("heartbeat", 500)
	stats.recordEventBytes("state_transition", 1800)
	snapshot := Snapshot{GuardRole: "resident", GuardBudgetApplicable: true}
	stats.apply(&snapshot, policy)
	want := projectedRecurringEventBytes*int64(86400/policy.HeartbeatSeconds) + projectedTransitionEventBytes*24 + maxProjectedActionRecordBytes*int64((24*time.Hour)/reliefRevalidationRetryInterval) + projectedDiskWriteEventBytes*projectedDiskWriteEventsDay
	if snapshot.TelemetryProjectedBytesDay != want {
		t.Fatalf("first-action telemetry projection=%d, want %d", snapshot.TelemetryProjectedBytesDay, want)
	}
	if evaluated := Evaluate(snapshot, policy); !evaluated.GuardBudgetOK {
		t.Fatalf("bounded first-action reserve exceeds daily budget: %v", evaluated.GuardBudgetReasons)
	}
}

func TestMonitorStatsDoNotProjectTransitionRowsAtHeartbeatFrequency(t *testing.T) {
	policy := DefaultPolicy(16 * 1024)
	stats := monitorStats{}
	stats.add(Snapshot{Level: LevelNormal, SampleDurationMS: 20, SampleCPUTimeMS: 5, GuardRSSMB: 8}, 90)
	stats.recordEventBytes("heartbeat", 486)
	for range 20 {
		stats.recordEventBytes("state_transition", 2600)
	}
	snapshot := Snapshot{GuardRole: "resident", GuardBudgetApplicable: true}
	stats.apply(&snapshot, policy)
	if got, want := snapshot.TelemetryProjectedBytesDay, int64(528384); got != want {
		t.Fatalf("kind-aware projection=%d want=%d", got, want)
	}
	if evaluated := Evaluate(snapshot, policy); !evaluated.GuardBudgetOK {
		t.Fatalf("transition-heavy but bounded telemetry failed budget: %v", evaluated.GuardBudgetReasons)
	}
}

func TestMonitorStatsWeightAdaptiveIntervalsAndSeparateIdleDuty(t *testing.T) {
	policy := DefaultPolicy(16 * 1024)
	stats := monitorStats{}
	stats.add(Snapshot{Level: LevelNormal, SampleCPUTimeMS: 30, SampleDurationMS: 100, GuardRSSMB: 5}, 60)
	stats.add(Snapshot{Level: LevelNormal, SampleCPUTimeMS: 30, SampleDurationMS: 100, GuardRSSMB: 5}, 60)
	stats.add(Snapshot{Level: LevelWarning, SampleCPUTimeMS: 30, SampleDurationMS: 100, GuardRSSMB: 5}, 15)
	snapshot := Snapshot{
		Level: LevelWarning, FreePercent: 20, GuardRole: "resident", GuardBudgetApplicable: true,
		SampleCPUTimeMS: 30, ObservedIntervalSeconds: 15,
	}
	stats.apply(&snapshot, policy)
	if snapshot.NormalMonitorSamples != 2 {
		t.Fatalf("normal samples = %d, want 2", snapshot.NormalMonitorSamples)
	}
	stats.apply(&snapshot, policy)
	if snapshot.NormalMonitorSamples != 2 {
		t.Fatalf("reapplying stats accumulated normal samples: %d", snapshot.NormalMonitorSamples)
	}
	if math.Abs(snapshot.GuardCPUDutyPercent-(90.0/(135*10))) > 0.0001 {
		t.Fatalf("weighted duty = %.4f", snapshot.GuardCPUDutyPercent)
	}
	if math.Abs(snapshot.GuardIdleCPUDutyPercent-0.05) > 0.0001 {
		t.Fatalf("idle duty = %.4f, want 0.05", snapshot.GuardIdleCPUDutyPercent)
	}
	if evaluated := Evaluate(snapshot, policy); !evaluated.GuardBudgetOK {
		t.Fatalf("pressure cadence must use pressure budget: %v", evaluated.GuardBudgetReasons)
	}
	policy.ResourceBudgets.MaxPressureCPUPercent = 0.1
	if evaluated := Evaluate(snapshot, policy); evaluated.GuardBudgetOK || !strings.Contains(strings.Join(evaluated.GuardBudgetReasons, " "), "pressure-state") {
		t.Fatalf("pressure duty ceiling was not enforced: %+v", evaluated)
	}
}

func TestMonitorStatsDutyIncludesResidentWorkBetweenSamples(t *testing.T) {
	policy := DefaultPolicy(16 * 1024)
	stats := monitorStats{}
	for index := 0; index < policy.SustainSamples; index++ {
		stats.add(Snapshot{
			Level: LevelNormal, SampleCPUTimeMS: 100, intervalCPUTimeMS: 150,
			SampleDurationMS: 100, GuardRSSMB: 5,
		}, 90)
	}
	snapshot := Snapshot{Level: LevelNormal}
	stats.apply(&snapshot, policy)
	if snapshot.SampleCPUTimeAvgMS != 100 {
		t.Fatalf("probe-only CPU average = %.3f, want 100", snapshot.SampleCPUTimeAvgMS)
	}
	if math.Abs(snapshot.GuardIdleCPUDutyPercent-(600.0/(360*10))) > 0.0001 {
		t.Fatalf("whole-loop idle duty = %.4f, want %.4f", snapshot.GuardIdleCPUDutyPercent, 600.0/(360*10))
	}
}

func TestResidentBaselineProofSurvivesPressureWindowRollover(t *testing.T) {
	policy := DefaultPolicy(16 * 1024)
	stats := monitorStats{}
	for index := 0; index < policy.SustainSamples; index++ {
		stats.add(Snapshot{Level: LevelNormal, SampleCPUTimeMS: 10}, 60)
	}
	snapshot := Snapshot{Level: LevelNormal}
	stats.apply(&snapshot, policy)
	if !snapshot.GuardBaselineProven {
		t.Fatalf("normal baseline was not proven: %+v", snapshot)
	}
	for index := 0; index < monitorStatsWindow+1; index++ {
		stats.add(Snapshot{Level: LevelWarning, SampleCPUTimeMS: 10}, 15)
	}
	stats.apply(&snapshot, policy)
	if snapshot.NormalMonitorSamples != 0 || !snapshot.GuardBaselineProven {
		t.Fatalf("pressure rollover erased resident baseline proof: %+v", snapshot)
	}
}

type recordingSignaler struct {
	trees  []AgentTree
	result terminationResult
	err    error
}

func (signaler *recordingSignaler) Terminate(tree AgentTree, _ Policy) (terminationResult, error) {
	signaler.trees = append(signaler.trees, tree)
	result := signaler.result
	if result.Snapshot.Level == "" {
		result.Snapshot.Level = LevelCritical
	}
	if result.Tree.RootPID == 0 {
		result.Tree = tree
	}
	return result, signaler.err
}

func TestCriticalReliefRequiresSustainAndChoosesQuiescentTree(t *testing.T) {
	now := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	policy := DefaultPolicy(16 * 1024)
	policy.EnforceAdmission = true
	policy.AutoShedCritical = true
	signaler := &recordingSignaler{}
	dir := t.TempDir()
	if err := SavePolicy(PolicyPath(dir), policy); err != nil {
		t.Fatal(err)
	}
	monitor := &Monitor{Policy: policy, Store: NewTelemetryStore(dir), Signaler: signaler, Now: func() time.Time { return now }}
	snapshot := Snapshot{
		Level: LevelCritical, GuardRole: "resident", GuardBudgetOK: true, GuardBaselineProven: true, ProcessInventoryFresh: true,
		NormalMonitorSamples: policy.SustainSamples, ConsecutiveSamples: policy.CriticalSustainSamples,
		ProcessRSSSumMB: 2000, AgentRSSSumMB: 1000,
		TopHostConsumers: []HostConsumer{{Executable: "Google_Chrome_Helper", RSSSumMB: 800}},
		TopAgentTrees: []AgentTree{
			{Agent: "codex", RootPID: 10, RSSSumMB: 1000, CPUPercentSum: 8, CPUAvailable: true, ElapsedSeconds: 5000},
			{Agent: "codex", RootPID: 20, RSSSumMB: 700, CPUPercentSum: 0.2, CPUAvailable: true, ElapsedSeconds: 5000, QuiescentSamples: 2, PIDs: []int{20, 21}},
			{Agent: "codex", RootPID: 25, RSSSumMB: 600, CPUPercentSum: 0.1, CPUAvailable: true, ElapsedSeconds: 5000, QuiescentSamples: 2, PIDs: []int{25}},
			{Agent: "claude", RootPID: 30, RSSSumMB: 900, CPUPercentSum: 0.1, CPUAvailable: true, ElapsedSeconds: 60},
		},
	}
	action, acted := monitor.maybeRelieve(snapshot, time.Time{})
	if !acted || action.Result != "signal_sent" || action.RootPID != 20 || action.ReliefScope != "agent_trees_only" || action.PrimaryHostExecutable != "Google_Chrome_Helper" || action.AgentRSSSharePercent != 50 {
		t.Fatalf("unexpected action: acted=%v %+v", acted, action)
	}
	if len(signaler.trees) != 1 || signaler.trees[0].RootPID != 20 {
		t.Fatalf("signaled trees = %+v", signaler.trees)
	}
	if _, acted := monitor.maybeRelieve(snapshot, now.Add(-time.Minute)); acted {
		t.Fatal("cooldown should suppress a second action")
	}
}

func TestCriticalReliefRecordsPostSignalExitEvidence(t *testing.T) {
	now := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	policy := DefaultPolicy(16 * 1024)
	policy.EnforceAdmission = true
	policy.AutoShedCritical = true
	dir := t.TempDir()
	if err := SavePolicy(PolicyPath(dir), policy); err != nil {
		t.Fatal(err)
	}
	snapshot := Snapshot{
		PhysicalMemoryMB: 16 * 1024, Level: LevelCritical, GuardRole: "resident", GuardBudgetOK: true,
		GuardBaselineProven: true, ProcessInventoryFresh: true, ConsecutiveSamples: policy.CriticalSustainSamples,
		TopAgentTrees: []AgentTree{{Agent: "codex", RootPID: 20, RSSSumMB: 700, CPUPercentSum: 0.2, CPUAvailable: true, ElapsedSeconds: 5000, QuiescentSamples: 2}},
	}
	for _, tt := range []struct {
		name   string
		result terminationResult
		want   string
	}{
		{name: "confirmed", result: terminationResult{SignalAttempted: true, ExitChecked: true, TreeExited: true}, want: "tree_exit_confirmed"},
		{name: "unconfirmed", result: terminationResult{SignalAttempted: true, ExitChecked: true}, want: "signal_sent_unconfirmed"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			signaler := &recordingSignaler{result: tt.result}
			monitor := &Monitor{Policy: policy, Store: NewTelemetryStore(dir), Signaler: signaler, Now: func() time.Time { return now }}
			action, acted := monitor.maybeRelieve(snapshot, time.Time{})
			if !acted || action.Result != tt.want || action.Signal != "SIGTERM" {
				t.Fatalf("post-signal action: acted=%v action=%+v", acted, action)
			}
		})
	}
}

func TestCriticalReliefRevalidationCooldownCanAdvanceToAnotherCandidate(t *testing.T) {
	now := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	policy := DefaultPolicy(16 * 1024)
	policy.EnforceAdmission = true
	policy.AutoShedCritical = true
	dir := t.TempDir()
	if err := SavePolicy(PolicyPath(dir), policy); err != nil {
		t.Fatal(err)
	}
	signaler := &recordingSignaler{}
	monitor := &Monitor{Policy: policy, Store: NewTelemetryStore(dir), Signaler: signaler, Now: func() time.Time { return now }}
	snapshot := Snapshot{
		PhysicalMemoryMB: 16 * 1024, Level: LevelCritical, GuardRole: "resident", GuardBudgetOK: true,
		GuardBaselineProven: true, ProcessInventoryFresh: true, ConsecutiveSamples: policy.CriticalSustainSamples,
		TopAgentTrees: []AgentTree{
			{Agent: "codex", RootPID: 20, RSSSumMB: 900, CPUPercentSum: 0.2, CPUAvailable: true, ElapsedSeconds: 5000, QuiescentSamples: 2},
			{Agent: "codex", RootPID: 25, RSSSumMB: 700, CPUPercentSum: 0.2, CPUAvailable: true, ElapsedSeconds: 5000, QuiescentSamples: 2},
		},
	}
	rejected := map[string]bool{reliefTreeKey(snapshot.TopAgentTrees[0]): true}
	if _, acted := monitor.maybeRelieveExcluding(snapshot, time.Time{}, now.Add(-2*time.Minute), rejected); acted {
		t.Fatal("revalidation retry occurred before the bounded retry interval")
	}
	action, acted := monitor.maybeRelieveExcluding(snapshot, time.Time{}, now.Add(-reliefRevalidationRetryInterval), rejected)
	if !acted || action.RootPID != 25 {
		t.Fatalf("relief did not advance past rejected candidate: acted=%v action=%+v", acted, action)
	}
}

func TestCriticalReliefRejectsStaleInMemoryPolicy(t *testing.T) {
	policy := DefaultPolicy(16 * 1024)
	policy.EnforceAdmission = true
	policy.AutoShedCritical = true
	dir := t.TempDir()
	persisted := policy
	persisted.AutoShedCritical = false
	if err := SavePolicy(PolicyPath(dir), persisted); err != nil {
		t.Fatal(err)
	}
	signaler := &recordingSignaler{}
	monitor := &Monitor{Policy: policy, Store: NewTelemetryStore(dir), Signaler: signaler, Now: time.Now}
	snapshot := Snapshot{
		PhysicalMemoryMB: 16 * 1024, Level: LevelCritical, GuardRole: "resident",
		GuardBudgetOK: true, GuardBaselineProven: true, ProcessInventoryFresh: true, ConsecutiveSamples: policy.CriticalSustainSamples,
		TopAgentTrees: []AgentTree{{Agent: "codex", RootPID: 20, RSSSumMB: 700, CPUPercentSum: 0.2, CPUAvailable: true, ElapsedSeconds: 5000, QuiescentSamples: 2}},
	}
	if _, acted := monitor.maybeRelieve(snapshot, time.Time{}); acted || len(signaler.trees) != 0 {
		t.Fatalf("stale in-memory action policy acquired authority: acted=%v trees=%+v", acted, signaler.trees)
	}
}

func TestCriticalReliefWithoutCandidateDoesNotBecomeAnAttempt(t *testing.T) {
	policy := DefaultPolicy(16 * 1024)
	policy.EnforceAdmission = true
	policy.AutoShedCritical = true
	dir := t.TempDir()
	if err := SavePolicy(PolicyPath(dir), policy); err != nil {
		t.Fatal(err)
	}
	signaler := &recordingSignaler{}
	monitor := &Monitor{Policy: policy, Store: NewTelemetryStore(dir), Signaler: signaler, Now: time.Now}
	snapshot := Snapshot{
		PhysicalMemoryMB: 16 * 1024, Level: LevelCritical, GuardRole: "resident",
		GuardBudgetOK: true, GuardBaselineProven: true, ProcessInventoryFresh: true, ConsecutiveSamples: policy.CriticalSustainSamples,
		TopAgentTrees: []AgentTree{{Agent: "codex", RootPID: 20, RSSSumMB: 700, CPUPercentSum: 9, CPUAvailable: true, ElapsedSeconds: 5000, QuiescentSamples: 0}},
	}
	if action, acted := monitor.maybeRelieve(snapshot, time.Time{}); acted || action.Result != "" || len(signaler.trees) != 0 {
		t.Fatalf("no-candidate decision became an action: acted=%v action=%+v trees=%+v", acted, action, signaler.trees)
	}
	snapshot.TopAgentTrees[0].CPUPercentSum = 0.2
	snapshot.TopAgentTrees[0].QuiescentSamples = policy.CriticalSustainSamples
	if action, acted := monitor.maybeRelieve(snapshot, time.Time{}); !acted || action.Result != "signal_sent" || len(signaler.trees) != 1 {
		t.Fatalf("later eligible candidate was not attempted immediately: acted=%v action=%+v trees=%+v", acted, action, signaler.trees)
	}
}

func TestCriticalReliefRequiresMatureBudgetCleanResident(t *testing.T) {
	policy := DefaultPolicy(16 * 1024)
	policy.EnforceAdmission = true
	policy.AutoShedCritical = true
	signaler := &recordingSignaler{}
	monitor := &Monitor{Policy: policy, Signaler: signaler, Now: time.Now}
	snapshot := Snapshot{
		Level: LevelCritical, GuardRole: "resident", GuardBudgetOK: true,
		NormalMonitorSamples: policy.SustainSamples - 1, ConsecutiveSamples: policy.CriticalSustainSamples,
		TopAgentTrees: []AgentTree{{Agent: "codex", RootPID: 20, RSSSumMB: 700, CPUPercentSum: 0.2, CPUAvailable: true, ElapsedSeconds: 5000, QuiescentSamples: 2}},
	}
	if _, acted := monitor.maybeRelieve(snapshot, time.Time{}); acted {
		t.Fatal("immature resident budget window acquired action authority")
	}
	snapshot.NormalMonitorSamples = policy.SustainSamples
	snapshot.GuardBaselineProven = true
	snapshot.GuardBudgetOK = false
	if _, acted := monitor.maybeRelieve(snapshot, time.Time{}); acted {
		t.Fatal("budget-failing resident acquired action authority")
	}
}

func TestSnapshotJSONNormalizesEmptyMemoryMomentum(t *testing.T) {
	body, err := json.Marshal(Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		MemoryMomentum   MemoryMomentum `json:"memory_momentum"`
		TopHostConsumers []HostConsumer `json:"top_host_consumers"`
		TopAgentTrees    []AgentTree    `json:"top_agent_trees"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.MemoryMomentum != MemoryMomentumUnknown {
		t.Fatalf("memory_momentum=%q want=%q", decoded.MemoryMomentum, MemoryMomentumUnknown)
	}
	if decoded.TopHostConsumers == nil || decoded.TopAgentTrees == nil {
		t.Fatalf("bounded arrays must serialize as []: host=%v agents=%v", decoded.TopHostConsumers, decoded.TopAgentTrees)
	}
}

func TestOperatorSnapshotCannotRelieveSessions(t *testing.T) {
	policy := DefaultPolicy(16 * 1024)
	policy.EnforceAdmission = true
	policy.AutoShedCritical = true
	signaler := &recordingSignaler{}
	monitor := &Monitor{Policy: policy, Signaler: signaler, Now: time.Now}
	snapshot := Snapshot{
		Level: LevelCritical, GuardRole: "operator", ConsecutiveSamples: policy.CriticalSustainSamples,
		TopAgentTrees: []AgentTree{{Agent: "codex", RootPID: 20, RSSSumMB: 700, CPUPercentSum: 0.2, CPUAvailable: true, ElapsedSeconds: 5000, QuiescentSamples: 2}},
	}
	if _, acted := monitor.maybeRelieve(snapshot, time.Time{}); acted || len(signaler.trees) != 0 {
		t.Fatalf("operator sample acquired action authority: acted=%v trees=%+v", acted, signaler.trees)
	}
}

func TestGracefulSignalerRevalidatesIdentityAndSignalsFreshTreeLeafFirst(t *testing.T) {
	current := `
20 1 1000 0.1 00:20:05 codex resume 019f5be0-7d38-7271-ba7d-8ade4a407bf0
21 20 1000 0.1 00:20:04 helper
22 21 1000 0.1 00:20:03 helper
90 1 1000 0.1 00:10:00 unrelated
`
	var signaled []int
	sampler := testSampler(time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC))
	sampler.runner = fixtureRunner{
		ps: current, pressure: "System-wide memory free percentage: 7%", swap: "vm.swapusage: total = 0.00M used = 0.00M free = 0.00M", physical: "17179869184",
	}
	policy := DefaultPolicy(16 * 1024)
	policyPath := PolicyPath(t.TempDir())
	if err := SavePolicy(policyPath, policy); err != nil {
		t.Fatal(err)
	}
	signaler := osProcessSignaler{
		sampler: sampler, policyPath: policyPath,
		signalPID: func(pid int) error {
			signaled = append(signaled, pid)
			return nil
		},
	}
	tree := AgentTree{
		Agent: "codex", Executable: "codex", RootPID: 20, ElapsedSeconds: 1200,
		SessionID: "019f5be0-7d38-7271-ba7d-8ade4a407bf0", PIDs: []int{20, 21, 22, 9999},
	}
	if _, err := signaler.Terminate(tree, policy); err != nil {
		t.Fatal(err)
	}
	want := []int{22, 21, 20}
	if !slices.Equal(signaled, want) {
		t.Fatalf("signaled PIDs = %v, want fresh leaf-first tree %v", signaled, want)
	}
}

func TestGracefulSignalerRejectsPolicyChangeDuringFinalSample(t *testing.T) {
	policy := DefaultPolicy(16 * 1024)
	policy.EnforceAdmission = true
	policy.AutoShedCritical = true
	policyPath := PolicyPath(t.TempDir())
	if err := SavePolicy(policyPath, policy); err != nil {
		t.Fatal(err)
	}
	sampler := testSampler(time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC))
	sampler.runner = fixtureRunner{
		ps: "20 1 1000 0.1 00:20:05 codex\n", pressure: "System-wide memory free percentage: 7%",
		swap: "vm.swapusage: total = 0.00M used = 0.00M free = 0.00M", physical: "17179869184",
	}
	sampler.peakRSS = func() float64 {
		downgraded := policy
		downgraded.AutoShedCritical = false
		if err := SavePolicy(policyPath, downgraded); err != nil {
			t.Fatalf("downgrade policy during sample: %v", err)
		}
		return 5
	}
	signaled := false
	signaler := osProcessSignaler{sampler: sampler, policyPath: policyPath, signalPID: func(int) error { signaled = true; return nil }}
	_, err := signaler.Terminate(AgentTree{Agent: "codex", Executable: "codex", RootPID: 20, ElapsedSeconds: 1200, PIDs: []int{20}}, policy)
	if err == nil || signaled || !strings.Contains(err.Error(), "policy changed") {
		t.Fatalf("mid-revalidation policy downgrade must win: signaled=%v err=%v", signaled, err)
	}
}

func TestGracefulSignalerRejectsNewDescendantAfterQuiescence(t *testing.T) {
	sampler := testSampler(time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC))
	sampler.runner = fixtureRunner{
		ps:       "20 1 1000 0.1 00:20:05 codex\n21 20 1000 0.1 00:20:04 helper\n22 21 1000 0.0 00:00:01 new-helper\n",
		pressure: "System-wide memory free percentage: 7%", swap: "vm.swapusage: total = 0.00M used = 0.00M free = 0.00M", physical: "17179869184",
	}
	signaled := false
	signaler := osProcessSignaler{sampler: sampler, signalPID: func(int) error { signaled = true; return nil }}
	tree := AgentTree{Agent: "codex", Executable: "codex", RootPID: 20, ElapsedSeconds: 1200, PIDs: []int{20, 21}}
	_, err := signaler.Terminate(tree, DefaultPolicy(16*1024))
	if err == nil || signaled || !strings.Contains(err.Error(), "added descendant") {
		t.Fatalf("new descendant must reject relief: signaled=%v err=%v", signaled, err)
	}
}

func TestGracefulSignalerRejectsReusedRootPID(t *testing.T) {
	signaled := false
	sampler := testSampler(time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC))
	sampler.runner = fixtureRunner{
		ps: "20 1 1000 0.1 00:00:01 codex\n", pressure: "System-wide memory free percentage: 7%", swap: "vm.swapusage: total = 0.00M used = 0.00M free = 0.00M", physical: "17179869184",
	}
	signaler := osProcessSignaler{
		sampler: sampler,
		signalPID: func(int) error {
			signaled = true
			return nil
		},
	}
	_, err := signaler.Terminate(AgentTree{Agent: "codex", Executable: "codex", RootPID: 20, ElapsedSeconds: 1200}, DefaultPolicy(16*1024))
	if err == nil || signaled || !strings.Contains(err.Error(), "reused") {
		t.Fatalf("reused root must fail closed before signaling: signaled=%v err=%v", signaled, err)
	}
}

func TestGracefulSignalerRejectsTreeThatBecameActive(t *testing.T) {
	sampler := testSampler(time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC))
	sampler.runner = fixtureRunner{
		ps:       "20 1 1000 9.0 00:20:05 codex\n21 20 1000 3.0 00:20:04 helper\n",
		pressure: "System-wide memory free percentage: 7%", swap: "vm.swapusage: total = 0.00M used = 0.00M free = 0.00M", physical: "17179869184",
	}
	signaled := false
	signaler := osProcessSignaler{sampler: sampler, signalPID: func(int) error { signaled = true; return nil }}
	policy := DefaultPolicy(16 * 1024)
	_, err := signaler.Terminate(AgentTree{Agent: "codex", Executable: "codex", RootPID: 20, ElapsedSeconds: 1200, PIDs: []int{20, 21}}, policy)
	if err == nil || signaled || !strings.Contains(err.Error(), "became active") {
		t.Fatalf("active final tree must fail closed: signaled=%v err=%v", signaled, err)
	}
}

func TestGracefulSignalerRejectsRecoveredHost(t *testing.T) {
	sampler := testSampler(time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC))
	sampler.runner = fixtureRunner{
		ps: "20 1 1000 0.1 00:20:05 codex\n", pressure: "System-wide memory free percentage: 60%",
		swap: "vm.swapusage: total = 0.00M used = 0.00M free = 0.00M", physical: "17179869184",
	}
	signaled := false
	signaler := osProcessSignaler{sampler: sampler, signalPID: func(int) error { signaled = true; return nil }}
	_, err := signaler.Terminate(AgentTree{Agent: "codex", Executable: "codex", RootPID: 20, ElapsedSeconds: 1200}, DefaultPolicy(16*1024))
	if err == nil || signaled || !strings.Contains(err.Error(), "recovered") {
		t.Fatalf("recovered host must fail closed: signaled=%v err=%v", signaled, err)
	}
}

func TestQuiescentCandidateMustPersistAcrossSamples(t *testing.T) {
	policy := DefaultPolicy(16 * 1024)
	streak := map[int]int{}
	trees := []AgentTree{{RootPID: 42, ElapsedSeconds: 1000, CPUPercentSum: 0.2, CPUAvailable: true}}
	annotateQuiescence(trees, policy, streak)
	if trees[0].QuiescentSamples != 1 {
		t.Fatalf("first quiescent sample = %d", trees[0].QuiescentSamples)
	}
	annotateQuiescence(trees, policy, streak)
	if trees[0].QuiescentSamples != 2 {
		t.Fatalf("second quiescent sample = %d", trees[0].QuiescentSamples)
	}
	trees[0].CPUPercentSum = 9
	annotateQuiescence(trees, policy, streak)
	if trees[0].QuiescentSamples != 0 {
		t.Fatalf("active sample should reset streak: %+v", trees[0])
	}
	trees[0].CPUPercentSum = 0
	trees[0].CPUAvailable = false
	annotateQuiescence(trees, policy, streak)
	if trees[0].QuiescentSamples != 0 {
		t.Fatalf("unavailable CPU evidence should reset streak: %+v", trees[0])
	}
	annotateQuiescence(nil, policy, streak)
	if len(streak) != 0 {
		t.Fatalf("missing process should be pruned: %+v", streak)
	}
}

func TestTelemetryIsSparseAndLatestIsBoundedProjection(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	sampler := testSampler(now)
	store := NewTelemetryStore(dir)
	store.Now = func() time.Time { return now }
	monitor := NewMonitor(sampler, store, DefaultPolicy(16*1024))
	snapshot, err := monitor.RunOnce(context.Background(), "manual")
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if snapshot.TelemetryBytesToday <= 0 {
		t.Fatalf("telemetry bytes = %d", snapshot.TelemetryBytesToday)
	}
	events, err := store.ReadEvents(10, now.Add(-time.Hour))
	if err != nil || len(events) != 1 || events[0].Event != "manual" {
		t.Fatalf("events=%+v err=%v", events, err)
	}
	latest, err := os.ReadFile(store.LatestPath())
	if err != nil || strings.Contains(string(latest), "/opt/bin/codex") {
		t.Fatalf("latest read/privacy: err=%v body=%s", err, latest)
	}
}

func TestTelemetryHistoryBoundsTreesWithoutChangingAggregateTruth(t *testing.T) {
	dir := t.TempDir()
	store := NewTelemetryStore(dir)
	now := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	store.Now = func() time.Time { return now }
	snapshot := Snapshot{
		SchemaVersion: SchemaVersion, Timestamp: now, AgentTreeCount: 20, AgentRSSSumMB: 2048,
		SampleCPUTimeP95MS: 17, GuardSampleErrors: 2, ResourceCleanupFailures: 1,
		ResourceCleanupStatus: "failing", ResidentStarts24h: 3,
	}
	for pid := 1; pid <= 20; pid++ {
		snapshot.TopAgentTrees = append(snapshot.TopAgentTrees, AgentTree{Agent: "codex", RootPID: pid, RSSSumMB: float64(100 - pid)})
	}
	if err := store.AppendEvent(TelemetryEvent{Event: "heartbeat", Snapshot: &snapshot}); err != nil {
		t.Fatal(err)
	}
	events, err := store.ReadEvents(1, now.Add(-time.Minute))
	if err != nil || len(events) != 1 || events[0].Snapshot != nil || events[0].Summary == nil {
		t.Fatalf("events=%+v err=%v", events, err)
	}
	got := events[0].Summary
	if got.AgentTreeCount != 20 || got.AgentRSSSumMB != 2048 {
		t.Fatalf("history bounds lost aggregate truth: %+v", got)
	}
	if got.SampleCPUTimeP95MS != 17 || got.GuardSampleErrors != 2 || got.ResourceCleanupFailures != 1 || got.ResourceCleanupStatus != "failing" || got.ResidentStarts24h != 3 {
		t.Fatalf("compact operational telemetry missing: %+v", got)
	}
	if len(snapshot.TopAgentTrees) != 20 {
		t.Fatalf("AppendEvent mutated caller snapshot: %d trees", len(snapshot.TopAgentTrees))
	}
}

func TestTelemetryBoundsDurableErrorAndActionText(t *testing.T) {
	dir := t.TempDir()
	store := NewTelemetryStore(dir)
	now := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	store.Now = func() time.Time { return now }
	long := strings.Repeat("sensitive-line\n", 100)
	if err := store.AppendEvent(TelemetryEvent{Event: "sample_error", Error: long}); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendAction(Action{Kind: "test", Level: LevelCritical, Result: "error", Reason: long}); err != nil {
		t.Fatal(err)
	}
	events, err := store.ReadEvents(1, now.Add(-time.Minute))
	if err != nil || len(events) != 1 || strings.Contains(events[0].Error, "\n") || len([]rune(events[0].Error)) > durableTextLimit+3 {
		t.Fatalf("unbounded durable event: events=%+v err=%v", events, err)
	}
	action, found, err := store.LastAction()
	if err != nil || !found || strings.Contains(action.Reason, "\n") || len([]rune(action.Reason)) > actionReasonLimit+3 {
		t.Fatalf("unbounded durable action: action=%+v found=%v err=%v", action, found, err)
	}
}

func TestAppendActionDurableWritesReadableAuditRow(t *testing.T) {
	store := NewTelemetryStore(t.TempDir())
	action := Action{Kind: "manual_idle_tree_reap_intent", Level: LevelNormal, Result: "intent_recorded"}
	if err := store.AppendActionDurable(action); err != nil {
		t.Fatal(err)
	}
	got, ok, err := store.LastAction()
	if err != nil || !ok || got.SchemaVersion != SchemaVersion || got.Kind != action.Kind || got.Result != action.Result {
		t.Fatalf("durable action row got=%+v ok=%v err=%v", got, ok, err)
	}
}

func TestProductionActionWireSizeFitsReservedProjection(t *testing.T) {
	dir := t.TempDir()
	store := NewTelemetryStore(dir)
	now := time.Date(2026, 7, 13, 12, 0, 0, 999999999, time.UTC)
	store.Now = func() time.Time { return now }
	action := Action{
		Timestamp: now, Kind: "graceful_tree_shed", Level: LevelCritical, RootPID: 999999,
		Agent: strings.Repeat("a", 100), SessionID: strings.Repeat("s", 200), RSSSumMB: 99999.999,
		SemanticState: SemanticStateReady, ReliefScope: "agent_trees_only",
		PrimaryHostExecutable: strings.Repeat("host", 30), PrimaryHostRSSSumMB: 99999.999, AgentRSSSharePercent: 99.999,
		Signal: "SIGTERM", Result: "signal_sent_unconfirmed", Reason: strings.Repeat("reason ", 100),
		RevalidatedLevel: LevelCritical, RevalidatedCPUPercent: 999.999, RevalidatedRSSSumMB: 99999.999,
		RevalidatedSemanticState: SemanticStateReady,
		RevalidationDurationMS:   99999.999, RevalidationCPUTimeMS: 99999.999,
		RevalidationGuardRSSMB: 99999.999, RevalidationPeakRSSMB: 99999.999,
	}
	if err := store.AppendAction(action); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(store.actionDayPath(now))
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() > maxProjectedActionRecordBytes {
		t.Fatalf("bounded production action row=%d bytes, reserve=%d", info.Size(), maxProjectedActionRecordBytes)
	}
}

func TestConfirmTreeExitDistinguishesGoneAndLiveProcesses(t *testing.T) {
	if !confirmTreeExit([]int{99999999}, 0) {
		t.Fatal("missing process was not confirmed exited")
	}
	if confirmTreeExit([]int{os.Getpid()}, 0) {
		t.Fatal("live process was falsely confirmed exited")
	}
}

func TestMalformedActionCooldownHistoryFailsMonitorClosed(t *testing.T) {
	dir := t.TempDir()
	store := NewTelemetryStore(dir)
	path := store.actionDayPath(time.Now())
	if err := os.WriteFile(path, []byte("{partial-action\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.LastAction(); err == nil || !strings.Contains(err.Error(), filepath.Base(path)) {
		t.Fatalf("malformed cooldown history did not fail closed: %v", err)
	}
	monitor := NewMonitor(nil, store, DefaultPolicy(16*1024))
	if err := monitor.Run(context.Background()); err == nil || !strings.Contains(err.Error(), "cooldown state") {
		t.Fatalf("monitor started without provable cooldown history: %v", err)
	}
}

func TestActionTelemetryRotatesAccountsAndPrunesWithSnapshots(t *testing.T) {
	dir := t.TempDir()
	store := NewTelemetryStore(dir)
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.Local)
	store.Now = func() time.Time { return now }
	old := now.AddDate(0, 0, -20)
	recent := now.AddDate(0, 0, -1)
	for _, timestamp := range []time.Time{old, recent, now} {
		if err := store.AppendAction(Action{Timestamp: timestamp, Kind: "test", Level: LevelCritical, Result: "skipped"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.AppendEvent(TelemetryEvent{Timestamp: now, Event: "heartbeat"}); err != nil {
		t.Fatal(err)
	}
	actionInfo, err := os.Stat(store.actionDayPath(now))
	if err != nil {
		t.Fatal(err)
	}
	eventInfo, err := os.Stat(store.dayPath(now))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := store.BytesForDay(now), actionInfo.Size()+eventInfo.Size(); got != want {
		t.Fatalf("daily telemetry bytes = %d, want %d", got, want)
	}
	if err := store.Prune(14); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(store.actionDayPath(old)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("old action shard was not pruned: %v", err)
	}
	if _, err := os.Stat(store.actionDayPath(recent)); err != nil {
		t.Fatalf("recent action shard was pruned: %v", err)
	}
	actions, err := store.ReadActions(10, recent.Add(-time.Minute))
	if err != nil || len(actions) != 2 || !actions[0].Timestamp.Equal(recent) || !actions[1].Timestamp.Equal(now) {
		t.Fatalf("ReadActions actions=%+v err=%v", actions, err)
	}
	action, found, err := store.LastAction()
	if err != nil || !found || !action.Timestamp.Equal(now) {
		t.Fatalf("LastAction action=%+v found=%v err=%v", action, found, err)
	}
}

func TestOperatorRunOnceDoesNotReplaceResidentLatest(t *testing.T) {
	dir := t.TempDir()
	store := NewTelemetryStore(dir)
	now := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	resident := Snapshot{SchemaVersion: SchemaVersion, Timestamp: now.Add(-time.Minute), Level: LevelNormal, GuardRole: "resident", GuardBudgetApplicable: true, GuardBudgetOK: true}
	if err := store.WriteLatest(resident); err != nil {
		t.Fatal(err)
	}
	sampler := testSampler(now)
	sampler.role = "operator"
	monitor := NewMonitor(sampler, store, DefaultPolicy(16*1024))
	if _, err := monitor.RunOnce(context.Background(), "manual"); err != nil {
		t.Fatal(err)
	}
	latest, ok := store.ReadLatest()
	if !ok || !latest.Timestamp.Equal(resident.Timestamp) || latest.GuardRole != "resident" {
		t.Fatalf("operator probe replaced resident latest: %+v ok=%v", latest, ok)
	}
}

func TestAssessGuardHealthSeparatesHealthyMonitorFromDailyDriver(t *testing.T) {
	now := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	policy := DefaultPolicy(16 * 1024)
	launchd := LaunchdStatus{OK: true, Installed: true, Loaded: true, PID: 42, ArtifactPresent: true, ArtifactVerified: true, ArtifactSHA256: strings.Repeat("a", 64)}
	latest := Snapshot{Timestamp: now.Add(-time.Minute), Level: LevelNormal, ProcessInventoryAvailable: true, ProcessInventoryCapturedAt: now.Add(-time.Minute), GuardPID: 42, GuardRole: "resident", GuardBinarySHA256: launchd.ArtifactSHA256, GuardBudgetApplicable: true, GuardBudgetOK: true, GuardBaselineProven: true, MonitorSamples: 4, NormalMonitorSamples: 4}
	health := AssessGuardHealth(now, policy, true, launchd, latest, true)
	if !health.MonitorHealthy || health.DailyDriverReady || health.ProtectionMode != "observe-only" {
		t.Fatalf("unexpected observe health: %+v", health)
	}
	if health.BudgetHeadroom == nil || health.BudgetHeadroom.RollingSampleCount != 4 || health.BudgetHeadroom.SamplesUntilSingleOutlierExcluded != 16 || health.BudgetHeadroom.P95ExcludesSingleMaximum {
		t.Fatalf("unexpected warm-up headroom: %+v", health.BudgetHeadroom)
	}
	policy.EnforceAdmission = true
	policy.AutoShedCritical = true
	health = AssessGuardHealth(now, policy, true, launchd, latest, true)
	if !health.MonitorHealthy || !health.DailyDriverReady || health.ProtectionMode != "full" {
		t.Fatalf("unexpected full health: %+v", health)
	}
	immature := latest
	immature.MonitorSamples = 1
	immature.NormalMonitorSamples = 1
	immature.GuardBaselineProven = false
	health = AssessGuardHealth(now, policy, true, launchd, immature, true)
	if !health.MonitorHealthy || health.DailyDriverReady || health.ResidentNormalSamples != 1 || health.RequiredNormalSamples != 4 {
		t.Fatalf("immature budget window reported ready: %+v", health)
	}
	wrongWriter := latest
	wrongWriter.GuardPID = 41
	health = AssessGuardHealth(now, policy, true, launchd, wrongWriter, true)
	if health.MonitorHealthy || health.DailyDriverReady || !slices.ContainsFunc(health.HealthReasons, func(reason string) bool { return strings.Contains(reason, "does not match launchd pid") }) {
		t.Fatalf("previous resident pid reported healthy: %+v", health)
	}
	stale := latest
	stale.Timestamp = now.Add(-4 * time.Minute)
	health = AssessGuardHealth(now, policy, true, launchd, stale, true)
	if health.MonitorHealthy || health.DailyDriverReady || health.LatestMonitorFresh {
		t.Fatalf("stale latest reported healthy: %+v", health)
	}
	policy.ResourceBudgets.MaxSampleCPUTimeMS = 1
	oldBudgetResult := latest
	oldBudgetResult.MonitorSamples = p95SingleOutlierExclusionSamples
	oldBudgetResult.SampleCPUTimeP95MS = 2
	oldBudgetResult.GuardBudgetOK = true
	health = AssessGuardHealth(now, policy, true, launchd, oldBudgetResult, true)
	if health.MonitorHealthy || !slices.ContainsFunc(health.HealthReasons, func(reason string) bool { return strings.Contains(reason, "effective policy") }) {
		t.Fatalf("effective budget drift reported healthy: %+v", health)
	}
	if health.BudgetHeadroom == nil || health.BudgetHeadroom.SampleCPUMarginMS != -1 {
		t.Fatalf("failing CPU budget margin was not exposed: %+v", health.BudgetHeadroom)
	}
	// Live 2026-08-12: 240s inventory + 120s sample + 15s slack = 375s.
	// A 3.8s sample plus a delayed doctor read landed at 376.2s and false-red.
	policy.ProcessInventoryIntervalSeconds = 240
	policy.SampleIntervalSeconds = 120
	policy.ResourceBudgets.MaxSampleDurationMS = 2000
	late := latest
	late.Timestamp = now.Add(-12 * time.Second)
	late.ProcessInventoryCapturedAt = now.Add(-376 * time.Second)
	health = AssessGuardHealth(now, policy, true, launchd, late, true)
	if !health.MonitorHealthy {
		t.Fatalf("inventory age 376s must stay healthy under 240+120 cadence: %+v", health)
	}
}

func TestGuardBudgetHeadroomExplainsSingleOutlierP95Recovery(t *testing.T) {
	policy := DefaultPolicy(16 * 1024)
	latest := Snapshot{
		MonitorSamples: 20, GuardRSSMaxMB: 27.25, GuardIdleCPUDutyPercent: 0.10,
		SampleCPUTimeP95MS: 37, SampleCPUTimeMaxMS: 57,
		TelemetryProjectedBytesDay: 971520,
	}
	headroom := guardBudgetHeadroom(latest, policy)
	if headroom.RollingSampleCount != 20 || headroom.SamplesUntilSingleOutlierExcluded != 0 || !headroom.P95ExcludesSingleMaximum {
		t.Fatalf("20-sample p95 maturity was not explicit: %+v", headroom)
	}
	if headroom.RSSMarginMB != policy.ResourceBudgets.MaxSelfRSSMB-27.25 || headroom.SampleCPUMarginMS != 13 || headroom.TelemetryMarginBytesDay != policy.ResourceBudgets.MaxTelemetryBytesDay-971520 {
		t.Fatalf("unexpected budget margins: %+v", headroom)
	}
}

func TestLifecycleDistinguishesCleanRestartFromUncleanRecovery(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 7, 13, 12, 0, 0, 0, time.Local)
	clock := func() time.Time { return now }
	first, hint, err := StartLifecycle(dir, 100, clock)
	if err != nil || hint != nil {
		t.Fatalf("first StartLifecycle hint=%+v err=%v", hint, err)
	}
	if err := first.MarkClean(); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Minute)
	_, hint, err = StartLifecycle(dir, 101, clock)
	if err != nil || hint != nil {
		t.Fatalf("clean restart hint=%+v err=%v", hint, err)
	}
	// Leave the second lifecycle unclean and give recovery a precise last sample.
	lastSample := Snapshot{SchemaVersion: SchemaVersion, Timestamp: now.Add(2 * time.Minute).UTC(), Level: LevelRed}
	if err := NewTelemetryStore(dir).WriteLatest(lastSample); err != nil {
		t.Fatal(err)
	}
	now = now.Add(3 * time.Minute)
	third, hint, err := StartLifecycle(dir, 102, clock)
	if err != nil || hint == nil {
		t.Fatalf("unclean restart hint=%+v err=%v", hint, err)
	}
	if hint.LastLevel != LevelRed || !strings.Contains(hint.RecoveryCommand, "ndev session recover --around") || !strings.Contains(hint.RecoveryCommand, "--include-resume-command") {
		t.Fatalf("unexpected recovery hint: %+v", hint)
	}
	loaded, found, err := LoadRecoveryHint(dir)
	if err != nil || !found || loaded.RecoveryCommand != hint.RecoveryCommand {
		t.Fatalf("LoadRecoveryHint found=%v hint=%+v err=%v", found, loaded, err)
	}
	if err := third.MarkClean(); err != nil {
		t.Fatal(err)
	}
	if err := ClearRecoveryHint(dir); err != nil {
		t.Fatal(err)
	}
	if _, found, err := LoadRecoveryHint(dir); err != nil || found {
		t.Fatalf("hint should be cleared: found=%v err=%v", found, err)
	}
}

func TestPolicyRoundTripUsesPrivateMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "policy.json")
	policy := DefaultPolicy(16 * 1024)
	if err := SavePolicy(path, policy); err != nil {
		t.Fatalf("SavePolicy: %v", err)
	}
	loaded, persisted, err := LoadPolicy(path, 16*1024)
	if err != nil || !persisted || loaded.BlockNewAt != LevelRed {
		t.Fatalf("LoadPolicy: persisted=%v policy=%+v err=%v", persisted, loaded, err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("mode=%v err=%v", info.Mode().Perm(), err)
	}
}

func TestLoadPolicyBackfillsCPUAndWorkLimitsForExistingPolicy(t *testing.T) {
	path := filepath.Join(t.TempDir(), "policy.json")
	expected := DefaultPolicy(16 * 1024)
	legacy := expected
	legacy.Thresholds.HostCPUWarningPercent = 0
	legacy.Thresholds.HostCPURedPercent = 0
	legacy.ResourceBudgets.MaxSampleDurationMS = 10000
	legacy.ResourceBudgets.MaxSampleCPUTimeMS = 300
	legacy.WorkLimits = WorkLimits{}
	body, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, persisted, err := LoadPolicy(path, 16*1024)
	if err != nil || !persisted || loaded.Thresholds.HostCPUWarningPercent != 85 || loaded.Thresholds.HostCPURedPercent != 95 || loaded.ResourceBudgets.MaxSampleDurationMS != 2000 || loaded.ResourceBudgets.MaxSampleCPUTimeMS != 50 || loaded.WorkLimits != expected.WorkLimits {
		t.Fatalf("legacy policy CPU backfill: persisted=%v policy=%+v err=%v", persisted, loaded, err)
	}
	legacy = expected
	legacy.ResourceBudgets.MaxSampleDurationMS = 500
	body, err = json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, persisted, err = LoadPolicy(path, 16*1024)
	if err != nil || !persisted || loaded.ResourceBudgets.MaxSampleDurationMS != 2000 {
		t.Fatalf("temporary dogfood wall budget was not migrated: persisted=%v policy=%+v err=%v", persisted, loaded, err)
	}
	legacy = expected
	legacy.WorkLimits.BuildWeight = 6
	body, err = json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, persisted, err = LoadPolicy(path, 16*1024)
	if err != nil || !persisted || loaded.WorkLimits.BuildWeight != 5 {
		t.Fatalf("shipped build weight was not migrated: persisted=%v policy=%+v err=%v", persisted, loaded, err)
	}
	legacy.WorkLimits.BuildWeight = 4
	body, err = json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, persisted, err = LoadPolicy(path, 16*1024)
	if err != nil || !persisted || loaded.WorkLimits.BuildWeight != 4 {
		t.Fatalf("operator build weight was overwritten: persisted=%v policy=%+v err=%v", persisted, loaded, err)
	}
}

func TestAdmissionFailsOpenOnSamplerErrorAndBlocksAtRed(t *testing.T) {
	policy := DefaultPolicy(16 * 1024)
	policy.EnforceAdmission = true
	failing := &Sampler{runner: fixtureRunner{err: errors.New("fixture unavailable")}, now: time.Now, pid: 1}
	if admission := CheckAdmission(context.Background(), failing, policy); !admission.Allowed || admission.Warning == "" {
		t.Fatalf("expected fail-open warning: %+v", admission)
	}
	now := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	sampler := testSampler(now)
	sampler.runner = fixtureRunner{
		ps: processFixture, pressure: "System-wide memory free percentage: 14%",
		swap: "vm.swapusage: total = 0.00M used = 0.00M free = 0.00M", physical: "17179869184",
	}
	if admission := CheckAdmission(context.Background(), sampler, policy); admission.Allowed || admission.Level != LevelRed {
		t.Fatalf("expected red block: %+v", admission)
	}
}

func TestLaunchdPlistRunsLowPriorityNativeMonitor(t *testing.T) {
	plist := renderLaunchdPlist("/tmp/a&b/ndev", "/tmp/ndev", strings.Repeat("a", 64), "/Users/nico", "/tmp/data", "/tmp/out", "/tmp/err")
	for _, want := range []string{"com.nicos.session-pressure", "<string>Standard</string>", "LowPriorityIO", "Umask", "<integer>63</integer>", "<integer>5</integer>", "/tmp/a&amp;b/ndev"} {
		if !strings.Contains(plist, want) {
			t.Fatalf("plist missing %q: %s", want, plist)
		}
	}
}

func TestSnapshotSummaryNamesTheSampledProcessRole(t *testing.T) {
	operator := Snapshot{GuardRole: "operator", GuardRSSMB: 39.2, GuardCPUPercent: 0.34}
	if summary := operator.Summary(); !strings.Contains(summary, "operator_self=39.2MB/0.34%") || strings.Contains(summary, "guard=") {
		t.Fatalf("operator summary=%q", summary)
	}
	resident := Snapshot{GuardRole: "resident", GuardRSSMB: 10.7, GuardCPUPercent: 0.04}
	if summary := resident.Summary(); !strings.Contains(summary, "resident_self=10.7MB/0.04%") || strings.Contains(summary, "guard=") {
		t.Fatalf("resident summary=%q", summary)
	}
}

func TestLaunchdPlistParityAllowsAValidPersistedControlBinaryPath(t *testing.T) {
	installed := []byte(renderLaunchdPlist("/tmp/guard", "/repo-a/bin/ndev", strings.Repeat("a", 64), "/Users/nico", "/tmp/data", "/tmp/out", "/tmp/err"))
	expected := []byte(renderLaunchdPlist("/tmp/guard", "/repo-b/bin/ndev", strings.Repeat("b", 64), "/Users/nico", "/tmp/data", "/tmp/out", "/tmp/err"))
	if !bytes.Equal(normalizedControlBinaryPlist(installed), normalizedControlBinaryPlist(expected)) {
		t.Fatal("control binary path should not create settings drift")
	}
	if got := controlBinaryFromPlist(installed); got != "/repo-a/bin/ndev" {
		t.Fatalf("control binary=%q", got)
	}
	if got := controlBinaryDigestFromPlist(installed); got != strings.Repeat("a", 64) {
		t.Fatalf("control binary digest=%q", got)
	}
}

func TestLaunchdInstallReliesOnBootstrapRunAtLoadWithoutBlockingKickstart(t *testing.T) {
	home := t.TempDir()
	binary := filepath.Join(home, "bin", "ndev-session-pressure")
	controlBinary := filepath.Join(home, "bin", "ndev")
	if err := os.MkdirAll(filepath.Dir(binary), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(binary, []byte("fixture"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(controlBinary, []byte("fixture"), 0o755); err != nil {
		t.Fatal(err)
	}
	manager := &LaunchdManager{Home: home, Binary: binary, ControlBinary: controlBinary, DataDir: filepath.Join(home, "data")}
	var calls []string
	bootstrapped := false
	manager.Launchctl = func(_ context.Context, args ...string) ([]byte, error) {
		calls = append(calls, strings.Join(args, " "))
		if len(args) > 0 && args[0] == "print" {
			if !bootstrapped {
				return []byte("not found"), errors.New("not loaded")
			}
			return []byte("state = running\n\tpid = 123\n"), nil
		}
		if len(args) > 0 && args[0] == "bootstrap" {
			bootstrapped = true
		}
		if len(args) > 0 && args[0] == "bootout" {
			bootstrapped = false
		}
		return nil, nil
	}
	status, err := manager.Install(context.Background())
	if err != nil || !status.OK || status.PID != 123 {
		t.Fatalf("Install status=%+v err=%v calls=%v", status, err, calls)
	}
	artifact, found, artifactErr := LoadInstalledArtifact(manager.DataDir)
	if artifactErr != nil || !found || artifact.Path != status.ArtifactPath || !status.ArtifactVerified {
		t.Fatalf("installed artifact=%+v found=%v status=%+v err=%v", artifact, found, status, artifactErr)
	}
	plistBody, readErr := os.ReadFile(manager.PlistPath())
	if readErr != nil || !strings.Contains(string(plistBody), artifact.Path) || strings.Contains(string(plistBody), "<string>"+binary+"</string>") {
		t.Fatalf("plist did not pin promoted artifact: artifact=%s err=%v\n%s", artifact.Path, readErr, plistBody)
	}
	for _, call := range calls {
		if strings.Contains(call, "kickstart") {
			t.Fatalf("install must not block on kickstart: %v", calls)
		}
	}
	for _, path := range []string{manager.stdoutPath(), manager.stderrPath()} {
		info, statErr := os.Stat(path)
		if statErr != nil {
			t.Fatalf("stat log %s: %v", path, statErr)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("log %s mode=%v", path, info.Mode().Perm())
		}
	}
	firstInstalledAt := artifact.InstalledAt
	callsBeforeRepeat := len(calls)
	repeated, repeatErr := manager.Install(context.Background())
	if repeatErr != nil || !repeated.OK || repeated.PID != status.PID {
		t.Fatalf("repeated Install status=%+v err=%v", repeated, repeatErr)
	}
	for _, call := range calls[callsBeforeRepeat:] {
		if strings.Contains(call, "bootout") || strings.Contains(call, "bootstrap") || strings.Contains(call, "enable") {
			t.Fatalf("same-artifact install restarted healthy resident: %v", calls[callsBeforeRepeat:])
		}
	}
	repeatedArtifact, repeatedFound, repeatedArtifactErr := LoadInstalledArtifact(manager.DataDir)
	if repeatedArtifactErr != nil || !repeatedFound || !repeatedArtifact.InstalledAt.Equal(firstInstalledAt) {
		t.Fatalf("same-artifact install rewrote manifest: first=%s repeated=%+v found=%v err=%v", firstInstalledAt, repeatedArtifact, repeatedFound, repeatedArtifactErr)
	}
	if repeated.ControlBinaryPath == controlBinary || !strings.HasPrefix(repeated.ControlBinaryPath, filepath.Join(manager.DataDir, "control-artifacts")+string(filepath.Separator)) {
		t.Fatalf("cleanup controller was not promoted independently of its source: %+v", repeated)
	}
	legacyPlist := renderLaunchdPlist(artifact.Path, controlBinary, repeated.ControlBinarySHA256, home, manager.DataDir, manager.stdoutPath(), manager.stderrPath())
	if err := os.WriteFile(manager.PlistPath(), []byte(legacyPlist), 0o644); err != nil {
		t.Fatal(err)
	}
	if legacy := manager.Status(context.Background()); !legacy.OK || legacy.ControlBinaryPath != controlBinary {
		t.Fatalf("valid legacy controller setup was not represented accurately: %+v", legacy)
	}
	migrated, migrateErr := manager.Install(context.Background())
	if migrateErr != nil || !migrated.OK || migrated.ControlBinaryPath == controlBinary || !isPromotedControlArtifact(migrated.ControlBinaryPath, manager.DataDir, migrated.ControlBinarySHA256) {
		t.Fatalf("legacy controller was not migrated into managed retention: status=%+v err=%v", migrated, migrateErr)
	}
	if err := os.Remove(controlBinary); err != nil {
		t.Fatal(err)
	}
	if afterSourceRemoval := manager.Status(context.Background()); !afterSourceRemoval.OK || !afterSourceRemoval.ControlBinaryVerified {
		t.Fatalf("promoted cleanup controller did not survive source removal: %+v", afterSourceRemoval)
	}
}

func TestLaunchdInstallRejectsLoadedJobWithoutLivePID(t *testing.T) {
	home := t.TempDir()
	binary := filepath.Join(home, "bin", "ndev-session-pressure")
	if err := os.MkdirAll(filepath.Dir(binary), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(binary, []byte("fixture"), 0o755); err != nil {
		t.Fatal(err)
	}
	manager := &LaunchdManager{Home: home, Binary: binary, ControlBinary: binary, DataDir: filepath.Join(home, "data")}
	bootstrapped := false
	manager.Launchctl = func(_ context.Context, args ...string) ([]byte, error) {
		if len(args) > 0 && args[0] == "print" {
			if !bootstrapped {
				return []byte("not found"), errors.New("not loaded")
			}
			return []byte("state = running\n"), nil
		}
		if len(args) > 0 && args[0] == "bootstrap" {
			bootstrapped = true
		}
		return nil, nil
	}
	status, err := manager.Install(context.Background())
	if err == nil || status.OK || !status.Loaded || !strings.Contains(err.Error(), "live pid") {
		t.Fatalf("Install accepted pid-less job: status=%+v err=%v", status, err)
	}
}

func TestLaunchdInstallHonorsCrossProcessLock(t *testing.T) {
	home := t.TempDir()
	binary := filepath.Join(home, "bin", "ndev-session-pressure")
	if err := os.MkdirAll(filepath.Dir(binary), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(binary, []byte("fixture"), 0o755); err != nil {
		t.Fatal(err)
	}
	dataDir := filepath.Join(home, "data")
	unlock, err := filelock.Acquire(filepath.Join(dataDir, launchdInstallLockName), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()

	launchCalls := 0
	manager := &LaunchdManager{Home: home, Binary: binary, DataDir: dataDir}
	manager.Launchctl = func(context.Context, ...string) ([]byte, error) {
		launchCalls++
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 75*time.Millisecond)
	defer cancel()
	if _, err := manager.Install(ctx); err == nil || !strings.Contains(err.Error(), "acquire launchd install lock") {
		t.Fatalf("Install lock error=%v", err)
	}
	if launchCalls != 0 {
		t.Fatalf("install reached launchctl while lock was held: calls=%d", launchCalls)
	}
}

func TestSessionPressureBinaryForPublishedControlExecutable(t *testing.T) {
	binDir := filepath.Join(t.TempDir(), "bin")
	cases := []struct {
		name       string
		executable string
		want       string
	}{
		{
			name:       "content addressed publication",
			executable: filepath.Join(binDir, "session-pressure-publish.artifacts", "sha256-deadbeef"),
			want:       filepath.Join(binDir, residentHelperName),
		},
		{
			name:       "ordinary sibling binary",
			executable: filepath.Join(binDir, "session-pressure"),
			want:       filepath.Join(binDir, residentHelperName),
		},
		{
			name:       "resident artifact is already the helper",
			executable: filepath.Join(binDir, "artifacts", "sha256-deadbeef", residentHelperName),
			want:       filepath.Join(binDir, "artifacts", "sha256-deadbeef", residentHelperName),
		},
		{
			name:       "legacy helper name is still accepted",
			executable: filepath.Join(binDir, "artifacts", "sha256-deadbeef", "ndev-session-pressure"),
			want:       filepath.Join(binDir, "artifacts", "sha256-deadbeef", "ndev-session-pressure"),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := sessionPressureBinaryForExecutable(tc.executable); got != tc.want {
				t.Fatalf("binary=%q want %q", got, tc.want)
			}
		})
	}
}

func TestPolicyMutationLockSerializesCrossProcessTransactions(t *testing.T) {
	dir := t.TempDir()
	unlock, err := AcquirePolicyMutationLock(context.Background(), dir, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 75*time.Millisecond)
	defer cancel()
	if _, err := AcquirePolicyMutationLock(ctx, dir, time.Second); err == nil || !strings.Contains(err.Error(), "acquire policy mutation lock") {
		t.Fatalf("policy mutation lock error=%v", err)
	}
}

func TestLaunchdUninstallWaitsForConfirmedUnload(t *testing.T) {
	home := t.TempDir()
	manager := &LaunchdManager{Home: home, DataDir: filepath.Join(home, "data")}
	if err := os.MkdirAll(filepath.Dir(manager.PlistPath()), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manager.PlistPath(), []byte("fixture"), 0o644); err != nil {
		t.Fatal(err)
	}
	printCalls := 0
	manager.Launchctl = func(_ context.Context, args ...string) ([]byte, error) {
		if len(args) > 0 && args[0] == "print" {
			printCalls++
			if printCalls < 3 {
				return []byte("state = running\n\tpid = 123\n"), nil
			}
			return []byte("not found"), errors.New("not loaded")
		}
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	status, err := manager.Uninstall(ctx)
	if err != nil || status.Loaded || status.Installed || printCalls < 3 {
		t.Fatalf("Uninstall returned before unload: status=%+v calls=%d err=%v", status, printCalls, err)
	}
}

func TestLaunchdUninstallFailsWhenJobRemainsLoaded(t *testing.T) {
	home := t.TempDir()
	manager := &LaunchdManager{Home: home, DataDir: filepath.Join(home, "data")}
	if err := os.MkdirAll(filepath.Dir(manager.PlistPath()), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manager.PlistPath(), []byte("fixture"), 0o644); err != nil {
		t.Fatal(err)
	}
	manager.Launchctl = func(_ context.Context, args ...string) ([]byte, error) {
		if len(args) > 0 && args[0] == "print" {
			return []byte("state = running\n\tpid = 123\n"), nil
		}
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	status, err := manager.Uninstall(ctx)
	if err == nil || !status.Loaded || !strings.Contains(err.Error(), "wait for launch agent unload") {
		t.Fatalf("Uninstall accepted loaded job: status=%+v err=%v", status, err)
	}
}

func TestLaunchdRestartWaitsForReplacementPID(t *testing.T) {
	manager := &LaunchdManager{Home: t.TempDir(), DataDir: t.TempDir()}
	pid := 123
	manager.Launchctl = func(_ context.Context, args ...string) ([]byte, error) {
		if len(args) > 0 && args[0] == "print" {
			return []byte(fmt.Sprintf("state = running\n\tpid = %d\n", pid)), nil
		}
		return nil, nil
	}
	manager.SignalPID = func(got int, signal os.Signal) error {
		if got != 123 || signal != os.Interrupt {
			t.Fatalf("signal pid=%d signal=%v", got, signal)
		}
		pid = 456
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	status, err := manager.Restart(ctx)
	if err != nil || status.PID != 456 {
		t.Fatalf("Restart status=%+v err=%v", status, err)
	}
}

func TestLaunchdRestartRejectsAbsentService(t *testing.T) {
	manager := &LaunchdManager{Home: t.TempDir(), DataDir: t.TempDir()}
	manager.Launchctl = func(_ context.Context, _ ...string) ([]byte, error) {
		return []byte("not found"), errors.New("not loaded")
	}
	if _, err := manager.Restart(context.Background()); !errors.Is(err, ErrLaunchAgentNotRunning) {
		t.Fatalf("Restart error=%v, want ErrLaunchAgentNotRunning", err)
	}
}
