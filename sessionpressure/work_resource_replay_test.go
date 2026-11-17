package sessionpressure

import (
	"testing"
	"time"
)

func replayHeartbeat(at time.Time, level Level, freePercent int, hostCPU float64, thrash bool) TelemetryEvent {
	return TelemetryEvent{Timestamp: at, Event: "heartbeat", Summary: &TelemetrySnapshotSummary{Timestamp: at, Level: level,
		FreePercent: freePercent, HostCPUPercent: hostCPU, SwapThrashLatched: thrash}}
}

func replayAcquired(at time.Time, operation string, class WorkClass, waitMS int64) WorkEvent {
	return WorkEvent{Timestamp: at, Event: WorkEventAcquired, OperationID: operation, Class: class, Weight: 4,
		WaitMilliseconds: waitMS, ToolFamily: ToolFamilySwift, SessionDigest: "s1"}
}

func replayCompleted(at time.Time, operation string, class WorkClass) WorkEvent {
	return WorkEvent{Timestamp: at, Event: WorkEventCompleted, OperationID: operation, Class: class,
		CommandCPUAvailable: true, CommandCPUMilliseconds: 240_000, RuntimeMillis: 60_000,
		CommandTreePeakRSSMB: 3000, CommandTreePeakSamples: 30}
}

func TestReplayResourceHeadroomJoinsHostSamples(t *testing.T) {
	start := time.Date(2026, 11, 16, 9, 0, 0, 0, time.UTC)
	host := ResourceReplayHost{PhysicalMemoryMB: 16384, LogicalCPUs: 10}
	var events []WorkEvent
	// History before the window: six test runs at 4 busy cores and 3 GB.
	for i := range 6 {
		at := start.Add(-time.Duration(60-i) * time.Minute)
		id := string(rune('a' + i))
		events = append(events, replayAcquired(at, "h"+id, WorkClassTest, 0), replayCompleted(at.Add(time.Minute), "h"+id, WorkClassTest))
	}
	// 1: a calm host. 2: no memory headroom, and thrash follows. 3: starts
	// in a gap with no host sample. 4: a calm host again, after 2 ended.
	events = append(events,
		replayAcquired(start.Add(1*time.Minute), "op1", WorkClassTest, 1_000),
		replayCompleted(start.Add(2*time.Minute), "op1", WorkClassTest),
		replayAcquired(start.Add(11*time.Minute), "op2", WorkClassTest, 9_000),
		replayAcquired(start.Add(12*time.Minute), "op2b", WorkClassTest, 2_000),
		replayCompleted(start.Add(20*time.Minute), "op2", WorkClassTest),
		replayCompleted(start.Add(20*time.Minute), "op2b", WorkClassTest),
		replayAcquired(start.Add(40*time.Minute), "op3", WorkClassBuild, 5_000),
		replayCompleted(start.Add(41*time.Minute), "op3", WorkClassBuild),
		replayAcquired(start.Add(61*time.Minute), "op4", WorkClassTest, 3_000),
	)
	telemetry := []TelemetryEvent{
		replayHeartbeat(start, LevelNormal, 70, 10, false),
		replayHeartbeat(start.Add(10*time.Minute), LevelWarning, 20, 30, false),
		replayHeartbeat(start.Add(15*time.Minute), LevelRed, 10, 60, true),
		replayHeartbeat(start.Add(25*time.Minute), LevelNormal, 60, 10, false),
		replayHeartbeat(start.Add(60*time.Minute), LevelNormal, 70, 10, false),
		{Timestamp: start.Add(30 * time.Minute), Event: "resident_stopped"},
	}
	replay := ReplayResourceHeadroom(events, telemetry, host, 2, start)

	if replay.Rule != ResourceHeadroomRuleVersion || replay.Starts != 5 || replay.Evaluated != 4 || replay.NoHostSample != 1 {
		t.Fatalf("starts=%d evaluated=%d no_sample=%d rule=%s", replay.Starts, replay.Evaluated, replay.NoHostSample, replay.Rule)
	}
	// op2 and op2b start at 20% free: 3,276 MB available minus the 2,048 MB
	// reserve leaves 1,228 MB for a 3,000 MB run. op2 has nothing running
	// beside it and keeps progress throttled; op2b queues.
	if replay.Steps[ResourceStepAdmit] != 2 || replay.Steps[ResourceStepThrottle] != 1 || replay.Steps[ResourceStepQueue] != 1 {
		t.Fatalf("steps = %v", replay.Steps)
	}
	if replay.Dimensions[ResourceDimensionMemory] != 2 || replay.Dimensions[ResourceDimensionNone] != 2 {
		t.Fatalf("dimensions = %v", replay.Dimensions)
	}
	// Thrash followed op2 and op2b within the window. The live queue started
	// both; the candidate starts neither unthrottled.
	if replay.StartsBeforePressure != 2 || replay.CandidateUnthrottledBeforePressure != 0 || replay.CandidateThrottledBeforePressure != 1 {
		t.Fatalf("before pressure: starts=%d unthrottled=%d throttled=%d", replay.StartsBeforePressure,
			replay.CandidateUnthrottledBeforePressure, replay.CandidateThrottledBeforePressure)
	}
	if len(replay.ByClass) != 1 || replay.ByClass[0].Class != WorkClassTest {
		t.Fatalf("by_class = %+v", replay.ByClass)
	}
	row := replay.ByClass[0]
	if row.Starts != 4 || row.DelayedPercent != 25 || row.ThrottledPercent != 25 || row.LiveWaitP95MS != 9_000 {
		t.Fatalf("test class row = %+v", row)
	}
	// op2b is held at +12 min. The +15 min sample is thrashing; the +25 min
	// sample (60% free, both leases ended at +20 min) releases it. Hold 13
	// min, on top of its 2 s live wait.
	wantHold := (13 * time.Minute).Milliseconds()
	if replay.Held != 1 || replay.UnresolvedHolds != 0 || replay.HoldP50MS != wantHold || replay.HoldP95MS != wantHold {
		t.Fatalf("held=%d unresolved=%d hold p50=%d p95=%d, want one hold of %d ms", replay.Held, replay.UnresolvedHolds, replay.HoldP50MS, replay.HoldP95MS, wantHold)
	}
	if row.CandidateWaitP95MS != wantHold+2_000 {
		t.Fatalf("candidate wait p95 = %d, want %d", row.CandidateWaitP95MS, wantHold+2_000)
	}
	if len(replay.Limitations) == 0 {
		t.Fatal("the replay must state its limits")
	}
}

func TestReplayResourceHeadroomWithoutTelemetry(t *testing.T) {
	start := time.Date(2026, 11, 16, 9, 0, 0, 0, time.UTC)
	replay := ReplayResourceHeadroom([]WorkEvent{replayAcquired(start.Add(time.Minute), "op1", WorkClassTest, 10)}, nil,
		ResourceReplayHost{PhysicalMemoryMB: 16384, LogicalCPUs: 10}, 2, start)
	if replay.Starts != 1 || replay.Evaluated != 0 || replay.NoHostSample != 1 || len(replay.ByClass) != 0 {
		t.Fatalf("replay without telemetry = %+v", replay)
	}
}

// The CPU-throttle score compares the throttle's parallelism limit with the
// busy cores that the same run measured unthrottled.
func TestReplayResourceHeadroomScoresTheCPUThrottle(t *testing.T) {
	start := time.Date(2026, 11, 16, 9, 0, 0, 0, time.UTC)
	host := ResourceReplayHost{PhysicalMemoryMB: 65536, LogicalCPUs: 10}
	var events []WorkEvent
	for i := range 6 {
		at := start.Add(-time.Duration(60-i) * time.Minute)
		id := string(rune('a' + i))
		events = append(events, replayAcquired(at, "h"+id, WorkClassTest, 0), replayCompleted(at.Add(time.Minute), "h"+id, WorkClassTest))
	}
	light := replayCompleted(start.Add(4*time.Minute), "light", WorkClassTest)
	light.CommandCPUMilliseconds = 30_000 // 0.5 busy cores over 60 s
	events = append(events,
		replayAcquired(start.Add(1*time.Minute), "running", WorkClassTest, 0),
		replayAcquired(start.Add(2*time.Minute), "heavy", WorkClassTest, 0),
		replayCompleted(start.Add(3*time.Minute), "heavy", WorkClassTest), // 4 busy cores over 60 s
		replayAcquired(start.Add(2*time.Minute), "light", WorkClassTest, 0),
		light,
	)
	// 95% host CPU: no core of headroom, with a lease running.
	telemetry := []TelemetryEvent{replayHeartbeat(start, LevelNormal, 80, 95, false)}
	replay := ReplayResourceHeadroom(events, telemetry, host, 2, start)

	score := replay.CPUThrottle[ResourceDimensionCPU+":"+resourceReasonSaturated]
	if score == nil {
		t.Fatalf("no cpu_saturated score: steps=%v dimensions=%v scores=%v", replay.Steps, replay.Dimensions, replay.CPUThrottle)
	}
	// running (the first start) has nothing beside it and keeps progress;
	// heavy and light start beside it and throttle to one core.
	if score.Decisions != 2 || score.Measured != 2 || score.Slowed != 1 || score.SlowedPercent != 50 {
		t.Fatalf("score = %+v", score)
	}
	// heavy averaged 4 busy cores under a 1-core limit: 60 s x (4 - 1).
	if score.AddedSecondsTotal != 180 || score.RuntimeSecondsTotal != 120 || score.AddedPercent != 150 {
		t.Fatalf("added = %v s of %v s (%v%%)", score.AddedSecondsTotal, score.RuntimeSecondsTotal, score.AddedPercent)
	}
}

// The memory-charge score compares the old sum of peaks and the pooled
// charge with the peaks the ramping leases measured.
func TestReplayResourceHeadroomScoresTheMemoryCharge(t *testing.T) {
	start := time.Date(2026, 11, 16, 9, 0, 0, 0, time.UTC)
	host := ResourceReplayHost{PhysicalMemoryMB: 65536, LogicalCPUs: 10}
	var events []WorkEvent
	// History: test runs peak at 500 MB, one in ten at 5,000 MB.
	for i := range 20 {
		at := start.Add(-time.Duration(90-i) * time.Minute)
		id := string(rune('a' + i))
		run := replayCompleted(at.Add(time.Minute), "h"+id, WorkClassTest)
		run.CommandTreePeakRSSMB = 500
		if i%10 == 0 {
			run.CommandTreePeakRSSMB = 5000
		}
		events = append(events, replayAcquired(at, "h"+id, WorkClassTest, 0), run)
	}
	// Two leases ramp at 600 MB each when the third starts.
	for _, op := range []string{"r1", "r2"} {
		run := replayCompleted(start.Add(5*time.Minute), op, WorkClassTest)
		run.CommandTreePeakRSSMB = 600
		events = append(events, replayAcquired(start.Add(time.Minute), op, WorkClassTest, 0), run)
	}
	events = append(events, replayAcquired(start.Add(2*time.Minute), "c", WorkClassTest, 0))
	telemetry := []TelemetryEvent{replayHeartbeat(start, LevelNormal, 80, 10, false)}
	replay := ReplayResourceHeadroom(events, telemetry, host, 2, start)

	sum, pooled := replay.MemoryCharge[MemoryChargeSumOfPeaks], replay.MemoryCharge[MemoryChargePooled]
	if sum == nil || pooled == nil {
		t.Fatalf("memory charge scores = %v", replay.MemoryCharge)
	}
	// Only the third start has two leases ramping beside it.
	if sum.Scored != 1 || pooled.Scored != 1 || sum.Undercharged != 0 || pooled.Undercharged != 0 {
		t.Fatalf("sum = %+v pooled = %+v", sum, pooled)
	}
	if pooled.ChargeOverRealizedP50 >= sum.ChargeOverRealizedP50 || pooled.ChargeOverRealizedP50 < 1 {
		t.Fatalf("pooled %.2f should sit between realized and the sum of peaks %.2f", pooled.ChargeOverRealizedP50, sum.ChargeOverRealizedP50)
	}
}
