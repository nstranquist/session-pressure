package sessionpressure

import (
	"io"
	"math"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"
)

// The leased command is usually a driver (go build, go test, make) whose real
// work runs in descendants it waits for. wait4 rusage must include them, or
// lease CPU reads a near-idle driver.
func TestCommandResourceUsageIncludesWaitedForDescendants(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("rusage descendant accounting is checked on darwin and linux")
	}
	// The outer shell only waits; all CPU is in the inner shell's loop.
	command := exec.Command("/bin/sh", "-c", `/bin/sh -c 'i=0; while [ $i -lt 300000 ]; do i=$((i+1)); done'`)
	if err := command.Run(); err != nil {
		t.Fatal(err)
	}
	usage := commandResourceUsage(command.ProcessState)
	if !usage.available || usage.cpuMS < 50 {
		t.Fatalf("descendant CPU missing from the command's rusage: %+v", usage)
	}
	if usage.peakRSSMB <= 0 || usage.peakRSSMB > 1024 {
		t.Fatalf("peak RSS unit conversion is wrong: %.3f MB", usage.peakRSSMB)
	}
	if got := commandResourceUsage(nil); got.available {
		t.Fatalf("an unreaped command has no usage: %+v", got)
	}
}

func TestReservedCoresNeedsMeasuredUsageAndValidLimits(t *testing.T) {
	measured := workCommandUsage{available: true, cpuMS: 1}
	if got := measured.reservedCores(5, 8, 10); got != 6.25 {
		t.Fatalf("build weight 5 of 8 on 10 cores reserved %.2f", got)
	}
	for _, test := range []struct {
		usage                         workCommandUsage
		weight, capacity, logicalCPUs int
	}{{workCommandUsage{}, 5, 8, 10}, {measured, 0, 8, 10}, {measured, 5, 0, 10}, {measured, 5, 8, 0}} {
		if got := test.usage.reservedCores(test.weight, test.capacity, test.logicalCPUs); got != 0 {
			t.Fatalf("%+v reserved %.2f, want 0", test, got)
		}
	}
}

func leaseCPUTerminal(class WorkClass, weight int, runtimeMS, cpuMS int64, reserved float64) WorkEvent {
	return WorkEvent{Event: WorkEventCompleted, Class: class, Weight: weight, RuntimeMillis: runtimeMS,
		CommandCPUAvailable: true, CommandCPUMilliseconds: cpuMS, CommandPeakRSSMB: 300, LeaseReservedCores: reserved}
}

func TestSummarizeLeaseCPUComparesCoresWithReservedWeight(t *testing.T) {
	events := []WorkEvent{
		leaseCPUTerminal(WorkClassBuild, 5, 10_000, 20_000, 6.25), // 2 cores
		leaseCPUTerminal(WorkClassBuild, 5, 30_000, 30_000, 6.25), // 1 core
		leaseCPUTerminal(WorkClassBuild, 5, 50, 500, 6.25),        // start-up noise
		{Event: WorkEventCompleted, Class: WorkClassBuild, Weight: 5, RuntimeMillis: 5_000},
		{Event: WorkEventStarted, Class: WorkClassBuild, Weight: 5, RuntimeMillis: 5_000, CommandCPUAvailable: true, CommandCPUMilliseconds: 5_000},
		leaseCPUTerminal(WorkClassTest, 3, 4_000, 6_000, 0), // recorded before reservations
	}
	got := summarizeLeaseCPU(events)
	if got.Operations != 3 || len(got.ByClass) != 2 {
		t.Fatalf("lease CPU population: %+v", got)
	}
	rows := map[WorkClass]WorkLeaseCPUClass{}
	for _, row := range got.ByClass {
		rows[row.Class] = row
	}
	build := rows[WorkClassBuild]
	if build.Class != WorkClassBuild || build.Operations != 2 || build.AvgCores != 1.25 || build.P50Cores != 1 || build.P90Cores != 2 {
		t.Fatalf("build cores: %+v", build)
	}
	if build.ReservedCores != 6.25 || build.Utilization != 0.2 || build.PeakRSSMBMax != 300 {
		t.Fatalf("build reservation: %+v", build)
	}
	test := rows[WorkClassTest]
	if test.Class != WorkClassTest || test.AvgCores != 1.5 || test.ReservedCores != 0 || test.Utilization != 0 {
		t.Fatalf("a class with no reservation must not report utilization: %+v", test)
	}
}

func TestCalibrationHintNamesUnderusedLeaseClasses(t *testing.T) {
	now := time.Date(2026, 11, 3, 2, 0, 0, 0, time.UTC)
	var events []WorkEvent
	for _, operation := range []string{"00000000000000000000000000000031", "00000000000000000000000000000032", "00000000000000000000000000000033"} {
		events = append(events, queueWaitAcquired(operation, WorkBlockerCapacity, 60_000, true, 25))
	}
	for run := 0; run < leaseCPUHintMinimumRuns; run++ {
		events = append(events, leaseCPUTerminal(WorkClassBuild, 5, 10_000, 12_500, 6.25))
	}
	report := BuildWorkCalibrationReport(events, now.Add(-time.Hour), now)
	if !strings.Contains(report.ThresholdRetuneHint, "measured: build used 20% of 6.2 reserved cores over 10 runs") {
		t.Fatalf("hint=%q", report.ThresholdRetuneHint)
	}
	report = BuildWorkCalibrationReport(events[:3+leaseCPUHintMinimumRuns-1], now.Add(-time.Hour), now)
	if strings.Contains(report.ThresholdRetuneHint, "measured:") {
		t.Fatalf("too few runs named a class: %q", report.ThresholdRetuneHint)
	}
}

func TestWorkEventValidateBoundsCommandResourceEvidence(t *testing.T) {
	base := validWorkEvent(time.Date(2026, 11, 3, 2, 0, 0, 0, time.UTC), WorkEventCompleted)
	base.EventID = "0123456789abcdef0123456789abcdef" // the store assigns one on append
	base.CommandCPUAvailable, base.CommandCPUMilliseconds, base.CommandPeakRSSMB, base.LeaseReservedCores = true, 1200, 310.5, 6.25
	if err := base.Validate(); err != nil {
		t.Fatalf("fixture invalid: %v", err)
	}
	for name, mutate := range map[string]func(*WorkEvent){
		"negative cpu":         func(event *WorkEvent) { event.CommandCPUAvailable, event.CommandCPUMilliseconds = true, -1 },
		"cpu without evidence": func(event *WorkEvent) { event.CommandCPUAvailable, event.CommandCPUMilliseconds = false, 10 },
		"nan reserved":         func(event *WorkEvent) { event.LeaseReservedCores = math.NaN() },
		"negative peak":        func(event *WorkEvent) { event.CommandPeakRSSMB = -1 },
		"infinite age":         func(event *WorkEvent) { event.QueueCoordinatedInventoryAgeSeconds = math.Inf(1) },
	} {
		event := base
		mutate(&event)
		if err := event.Validate(); err == nil {
			t.Fatalf("%s: accepted %+v", name, event)
		}
	}
}

func TestRunWorkCommandRecordsCommandCPUOnTerminalEvent(t *testing.T) {
	dir := t.TempDir()
	limits := testWorkLimits()
	coordinator := NewWorkCoordinator(dir, limits)
	admission := func() Admission { return Admission{Allowed: true, Level: LevelNormal, Snapshot: &Snapshot{}} }
	code, err := RunWorkCommand(coordinator,
		WorkRunOptions{Class: WorkClassBuild, Wait: 5 * time.Second, Progress: WorkProgressQuiet,
			Command: []string{"/bin/sh", "-c", "i=0; while [ $i -lt 100000 ]; do i=$((i+1)); done"}},
		admission, time.Millisecond, WorkRunStreams{Stdout: io.Discard, Stderr: io.Discard,
			CommandFactory: func(target string, args []string) (*exec.Cmd, io.WriteCloser, error) {
				return exec.Command(target, args...), nil, nil
			}})
	if err != nil || code != 0 {
		t.Fatalf("run code=%d err=%v", code, err)
	}
	events, err := NewWorkEventStore(dir).Read(WorkEventFilter{})
	if err != nil {
		t.Fatal(err)
	}
	weight, err := limits.Weight(WorkClassBuild)
	if err != nil {
		t.Fatal(err)
	}
	want := float64(weight) / float64(limits.Capacity) * float64(runtime.NumCPU())
	for _, event := range events {
		if event.Event != WorkEventCompleted {
			continue
		}
		if !event.CommandCPUAvailable || event.CommandCPUMilliseconds <= 0 || event.LeaseReservedCores != want {
			t.Fatalf("completed event lacks command CPU: %+v (want reserved %.2f)", event, want)
		}
		return
	}
	t.Fatalf("no completed event: %+v", events)
}
