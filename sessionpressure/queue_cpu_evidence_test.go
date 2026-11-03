package sessionpressure

import (
	"context"
	"io"
	"math"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestQueueCPUEvidencePrefersRollingHostCPU(t *testing.T) {
	snapshot := &Snapshot{
		HostCPUAvailable: true, HostCPUPercent: 91,
		HostCPURollingAvailable: true, HostCPURollingPercent: 22,
		CoordinatedWork: CoordinatedWorkSnapshot{Available: true, CPUAvailable: true, CPUPercent: 12, InventoryAgeSeconds: 185},
	}
	got := queueCPUEvidenceFrom(Admission{Snapshot: snapshot})
	// The leased-work figure carries its inventory age: the resident refreshes
	// it once per sample interval, so it can predate the block by minutes.
	if !got.hostAvailable || got.hostPercent != 22 || !got.workAvailable || got.workPercent != 12 || got.workInventoryAgeSeconds != 185 {
		t.Fatalf("rolling evidence=%+v", got)
	}
	snapshot.HostCPURollingAvailable = false
	if got := queueCPUEvidenceFrom(Admission{Snapshot: snapshot}); !got.hostAvailable || got.hostPercent != 91 {
		t.Fatalf("instant fallback=%+v", got)
	}
	snapshot.HostCPUPercent = math.NaN()
	snapshot.CoordinatedWork.CPUPercent = 140
	if got := queueCPUEvidenceFrom(Admission{Snapshot: snapshot}); got.hostAvailable || got.workAvailable {
		t.Fatalf("invalid percentages became evidence: %+v", got)
	}
	if got := queueCPUEvidenceFrom(Admission{}); got != (queueCPUEvidence{}) {
		t.Fatalf("no snapshot=%+v", got)
	}
}

func TestWorkEventValidateBoundsQueueCPUEvidence(t *testing.T) {
	now := time.Date(2026, 11, 3, 2, 0, 0, 0, time.UTC)
	event := validWorkEvent(now, WorkEventAcquired)
	event.EventID = "0123456789abcdef0123456789abcdef" // the store assigns one on append
	event.QueueHostCPUAvailable, event.QueueHostCPUPercent = true, 37.5
	if err := event.Validate(); err != nil {
		t.Fatalf("valid evidence rejected: %v", err)
	}
	event.QueueHostCPUPercent = 101
	if err := event.Validate(); err == nil || !strings.Contains(err.Error(), "queue CPU percent") {
		t.Fatalf("out-of-range percent: %v", err)
	}
	event.QueueHostCPUAvailable, event.QueueHostCPUPercent = false, 20
	if err := event.Validate(); err == nil || !strings.Contains(err.Error(), "requires available evidence") {
		t.Fatalf("percent without evidence: %v", err)
	}
}

func queueWaitAcquired(operation string, blocker WorkBlocker, waitMS int64, hostAvailable bool, hostPercent float64) WorkEvent {
	return WorkEvent{
		OperationID: operation, Class: WorkClassTest, Event: WorkEventAcquired, Blocker: blocker,
		WaitMilliseconds: waitMS, QueueWaitMilliseconds: waitMS,
		QueueHostCPUAvailable: hostAvailable, QueueHostCPUPercent: hostPercent,
	}
}

func TestSummarizeSplitsQueueWaitsByHostCPU(t *testing.T) {
	now := time.Date(2026, 11, 3, 2, 0, 0, 0, time.UTC)
	events := []WorkEvent{
		queueWaitAcquired("00000000000000000000000000000011", WorkBlockerCapacity, 45_000, true, 20),    // idle host
		queueWaitAcquired("00000000000000000000000000000012", WorkBlockerReservation, 90_000, true, 35), // idle host
		queueWaitAcquired("00000000000000000000000000000013", WorkBlockerCapacity, 45_000, true, 80),    // busy host
		queueWaitAcquired("00000000000000000000000000000014", WorkBlockerCapacity, 10_000, true, 10),    // too short
		queueWaitAcquired("00000000000000000000000000000015", WorkBlockerStateLock, 45_000, true, 10),   // not a weight wait
		queueWaitAcquired("00000000000000000000000000000016", WorkBlockerCapacity, 45_000, false, 0),    // no evidence
	}
	signals := SummarizeWorkEvents(events, now.Add(-time.Hour), now).ReviewSignals
	if signals.QueueWaitsWithCPUEvidence != 3 || signals.QueueWaitsIdleHost != 2 {
		t.Fatalf("with_evidence=%d idle_host=%d", signals.QueueWaitsWithCPUEvidence, signals.QueueWaitsIdleHost)
	}
}

func TestCalibrationHintNamesIdleHostQueueWaits(t *testing.T) {
	now := time.Date(2026, 11, 3, 2, 0, 0, 0, time.UTC)
	var events []WorkEvent
	for _, operation := range []string{"00000000000000000000000000000021", "00000000000000000000000000000022", "00000000000000000000000000000023"} {
		events = append(events, queueWaitAcquired(operation, WorkBlockerCapacity, 60_000, true, 25))
	}
	report := BuildWorkCalibrationReport(events, now.Add(-time.Hour), now)
	if !strings.Contains(report.ThresholdRetuneHint, "3 of 3 measured queue waits") || !strings.Contains(report.ThresholdRetuneHint, "class weights or work capacity") {
		t.Fatalf("hint=%q", report.ThresholdRetuneHint)
	}
	report = BuildWorkCalibrationReport(events[:2], now.Add(-time.Hour), now)
	if strings.Contains(report.ThresholdRetuneHint, "queue waits") {
		t.Fatalf("two waits steered a retune: %q", report.ThresholdRetuneHint)
	}
}

// End to end: a run that queues behind held weight records the host CPU of
// its first block on the acquired event.
func TestRunWorkCommandRecordsQueueCPUEvidenceOnAcquired(t *testing.T) {
	dir := t.TempDir()
	coordinator := NewWorkCoordinator(dir, testWorkLimits())
	ctx := context.Background()
	held, _, err := coordinator.Acquire(ctx, WorkClassHeavy) // weight 6 of 8
	if err != nil {
		t.Fatal(err)
	}
	admission := func() Admission {
		return Admission{Allowed: true, Level: LevelNormal, Snapshot: &Snapshot{HostCPURollingAvailable: true, HostCPURollingPercent: 22}}
	}
	result := make(chan error, 1)
	go func() {
		_, runErr := RunWorkCommand(coordinator,
			WorkRunOptions{Class: WorkClassBuild, Wait: 5 * time.Second, Progress: WorkProgressQuiet, Command: []string{"/usr/bin/true"}},
			admission, time.Millisecond, WorkRunStreams{Stdout: io.Discard, Stderr: io.Discard,
				CommandFactory: func(target string, args []string) (*exec.Cmd, io.WriteCloser, error) {
					return exec.Command(target, args...), nil, nil
				}})
		result <- runErr
	}()
	deadline := time.Now().Add(3 * time.Second)
	for {
		status, statusErr := coordinator.Status(ctx)
		if statusErr == nil && status.QueueDepth == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("build never queued: %+v %v", status, statusErr)
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err := held.Release(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	events, err := NewWorkEventStore(dir).Read(WorkEventFilter{})
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if event.Event != WorkEventAcquired {
			continue
		}
		if event.Blocker != WorkBlockerCapacity || !event.QueueHostCPUAvailable || event.QueueHostCPUPercent != 22 {
			t.Fatalf("acquired event lacks queue CPU evidence: %+v", event)
		}
		return
	}
	t.Fatalf("no acquired event: %+v", events)
}
