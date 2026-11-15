package sessionpressure

import (
	"testing"
	"time"
)

func stallStatus(now time.Time, used int, leases []string, waiters ...WorkWaiterStatus) WorkStatus {
	status := WorkStatus{Capacity: 8, Used: used, Available: 8 - used, Waiters: waiters, QueueDepth: len(waiters), DecisionReason: "protected_pressure_reservation"}
	for _, id := range leases {
		status.Leases = append(status.Leases, WorkLeaseStatus{ID: id})
	}
	return status
}

func stallWaiter(id string, class WorkClass, weight int, heartbeat time.Time) WorkWaiterStatus {
	return WorkWaiterStatus{OperationID: id, Class: class, Weight: weight, HeartbeatAt: heartbeat}
}

func TestQueueStallDetectorFiresOncePerEpisode(t *testing.T) {
	start := time.Date(2026, 11, 15, 16, 0, 0, 0, time.UTC)
	allowed := Admission{Allowed: true, Level: LevelNormal}
	var detector QueueStallDetector
	observe := func(offset time.Duration, status WorkStatus, admission Admission) (*QueueStall, *QueueStall) {
		return detector.Observe(start.Add(offset), status, admission, 8)
	}
	idle := func(at time.Duration, leases ...string) WorkStatus {
		return stallStatus(start.Add(at), 0, leases, stallWaiter("w", WorkClassBuild, 5, start.Add(at)))
	}
	if s, e := observe(0, idle(0), allowed); s != nil || e != nil {
		t.Fatal("fired at once")
	}
	if s, _ := observe(20*time.Second, idle(20*time.Second), allowed); s != nil {
		t.Fatal("fired before the threshold")
	}
	stall, _ := observe(35*time.Second, idle(35*time.Second), allowed)
	if stall == nil || stall.OperationID != "w" || stall.DurationMS != 35_000 || stall.EffectiveCapacity != 8 || stall.DecisionReason == "" {
		t.Fatalf("stall: %+v", stall)
	}
	if s, _ := observe(60*time.Second, idle(60*time.Second), allowed); s != nil {
		t.Fatal("fired twice in one episode")
	}
	// A new lease ends the episode.
	_, ended := observe(70*time.Second, idle(70*time.Second, "lease-1"), allowed)
	if ended == nil || ended.DurationMS != 70_000 || ended.EndedAt.IsZero() {
		t.Fatalf("episode end: %+v", ended)
	}
}

func TestQueueStallDetectorIgnoresPressureAndUnfitWork(t *testing.T) {
	start := time.Date(2026, 11, 15, 16, 0, 0, 0, time.UTC)
	cases := map[string]struct {
		admission Admission
		capacity  int
		status    func(at time.Time) WorkStatus
	}{
		"red holds all work": {Admission{Allowed: false, Level: LevelRed}, 8,
			func(at time.Time) WorkStatus { return stallStatus(at, 0, nil, stallWaiter("w", WorkClassTest, 2, at)) }},
		"heavy head above the derated ceiling": {Admission{Allowed: true, Level: LevelWarning}, 4,
			func(at time.Time) WorkStatus { return stallStatus(at, 0, nil, stallWaiter("w", WorkClassBuild, 5, at)) }},
		"full capacity": {Admission{Allowed: true, Level: LevelNormal}, 8,
			func(at time.Time) WorkStatus { return stallStatus(at, 7, nil, stallWaiter("w", WorkClassTest, 2, at)) }},
		"dead waiter heartbeat": {Admission{Allowed: true, Level: LevelNormal}, 8,
			func(at time.Time) WorkStatus {
				return stallStatus(at, 0, nil, stallWaiter("w", WorkClassTest, 2, at.Add(-2*time.Minute)))
			}},
		"parked pressure reservation": {Admission{Allowed: true, Level: LevelNormal}, 8,
			func(at time.Time) WorkStatus {
				waiter := stallWaiter("w", WorkClassTest, 2, at)
				waiter.PressureParked = true
				return stallStatus(at, 0, nil, waiter)
			}},
	}
	for name, tc := range cases {
		var detector QueueStallDetector
		for offset := time.Duration(0); offset <= 2*time.Minute; offset += 10 * time.Second {
			at := start.Add(offset)
			if stall, _ := detector.Observe(at, tc.status(at), tc.admission, tc.capacity); stall != nil {
				t.Errorf("%s: false stall %+v", name, stall)
				break
			}
		}
	}
	// Observe mode admits at red, so an idle queue at red is a stall.
	var observeMode QueueStallDetector
	var redStall *QueueStall
	for offset := time.Duration(0); offset <= 40*time.Second && redStall == nil; offset += 10 * time.Second {
		at := start.Add(offset)
		redStall, _ = observeMode.Observe(at, stallStatus(at, 0, nil, stallWaiter("w", WorkClassTest, 2, at)), Admission{Allowed: true, Level: LevelRed}, 1000)
	}
	if redStall == nil {
		t.Fatal("observe-mode stall at red not detected")
	}
	// A lighter successor that fits behind a heavy head is a stall.
	var detector QueueStallDetector
	var stall *QueueStall
	for offset := time.Duration(0); offset <= 40*time.Second && stall == nil; offset += 10 * time.Second {
		at := start.Add(offset)
		status := stallStatus(at, 0, nil, stallWaiter("heavy", WorkClassBuild, 5, at), stallWaiter("light", WorkClassBrowser, 2, at))
		stall, _ = detector.Observe(at, status, Admission{Allowed: true, Level: LevelWarning}, 4)
	}
	if stall == nil || stall.OperationID != "light" {
		t.Fatalf("idle successor not detected: %+v", stall)
	}
}

func TestReplayQueueStallsFromTrace(t *testing.T) {
	base := time.Date(2026, 11, 15, 16, 0, 0, 0, time.UTC)
	at := func(seconds int) time.Time { return base.Add(time.Duration(seconds) * time.Second) }
	acquired := func(op string, seconds, weight int, queueWaitSeconds int) WorkEvent {
		return WorkEvent{Event: WorkEventAcquired, OperationID: op, Timestamp: at(seconds), Weight: weight, Capacity: 8, QueueWaitMilliseconds: int64(queueWaitSeconds) * 1000}
	}
	ended := func(op string, seconds int) WorkEvent {
		return WorkEvent{Event: WorkEventCompleted, OperationID: op, Timestamp: at(seconds)}
	}
	// a runs 0-100. b (weight 2) queued at 100 but acquired only at 400: a
	// 300 s stall while normal. c queued at 500, acquired at 520 with a red
	// host from 450: no stall. Normal again from 600.
	events := []WorkEvent{acquired("a", 0, 5, 0), ended("a", 100), acquired("b", 400, 2, 300), ended("b", 450), acquired("c", 520, 2, 20), ended("c", 530)}
	levels := []LevelObservation{{At: at(0), Level: LevelNormal}, {At: at(450), Level: LevelRed}, {At: at(600), Level: LevelNormal}}
	replay := ReplayQueueStalls(events, levels, defaultWorkLimits(8), 30*time.Second)
	if replay.Episodes != 1 || replay.LongestStallSeconds != 300 || replay.StallSeconds != 270 {
		t.Fatalf("replay: %+v", replay)
	}
	if example := replay.Examples[0]; !example.Start.Equal(at(100)) || example.FitWeight != 2 || example.Used != 0 {
		t.Fatalf("example: %+v", example)
	}
	if len(replay.Limitations) == 0 {
		t.Fatal("limitations must be stated")
	}
	// The capacity in force at event time decides, not today's policy: with
	// capacity 1000 in the current policy, a weight-8 waiter beside 6 used
	// at an 8-unit capacity is not a stall.
	heavy := []WorkEvent{acquired("x", 0, 6, 0), acquired("y", 300, 8, 200), ended("x", 299), ended("y", 310)}
	if got := ReplayQueueStalls(heavy, levels, WorkLimits{Capacity: 1000}, 30*time.Second); got.Episodes != 0 {
		t.Fatalf("current policy capacity used for history: %+v", got)
	}
	// Unknown level is never judged.
	if got := ReplayQueueStalls(events, nil, defaultWorkLimits(8), 30*time.Second); got.Episodes != 0 {
		t.Fatalf("judged without a level: %+v", got)
	}
}

func TestLevelTimelineReadsTransitionsHeartbeatsAndStops(t *testing.T) {
	base := time.Date(2026, 11, 15, 16, 0, 0, 0, time.UTC)
	timeline := LevelTimeline([]TelemetryEvent{
		{Timestamp: base.Add(2 * time.Minute), Event: "heartbeat", Summary: &TelemetrySnapshotSummary{Level: LevelWarning}},
		{Timestamp: base, Event: "state_transition", Snapshot: &Snapshot{Level: LevelRed}},
		{Timestamp: base.Add(3 * time.Minute), Event: "resident_stopped"},
		{Timestamp: base.Add(time.Minute), Event: "disk_write_state_transition"},
	})
	if len(timeline) != 3 || timeline[0].Level != LevelRed || timeline[1].Level != LevelWarning || timeline[2].Level != "" {
		t.Fatalf("timeline: %+v", timeline)
	}
}
