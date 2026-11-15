package sessionpressure

import (
	"context"
	"strings"
	"testing"
	"time"
)

func memoryRedAdmission(level Level) Admission {
	return Admission{
		Allowed: false, Level: level, Dimension: "memory",
		Reasons: []string{"swap 12029 MB >= red 8192 MB with live warning pressure"},
	}
}

func redLightGate(class WorkClass, interactive bool, used int, peakMB int64, peakKnown bool) *workAdmissionGate {
	limits := defaultWorkLimits(10)
	return &workAdmissionGate{
		limits: limits, class: class, interactive: interactive,
		classPeakRSSP90MB: func() (int64, bool) { return peakMB, peakKnown },
		warningCapacityStatus: func() (WorkStatus, bool) {
			return WorkStatus{Used: used, Capacity: limits.Capacity}, true
		},
	}
}

// 2026-11-15: memory red held every operation for the whole episode, including
// light work from the chat Nico was using. The lane admits that work only
// under the listed bounds.
func TestInteractiveRedLaneAdmitsLightChatWorkUnderMemoryRed(t *testing.T) {
	tests := []struct {
		name        string
		class       WorkClass
		interactive bool
		level       Level
		used        int
		peakMB      int64
		peakKnown   bool
		allowed     bool
		label       string
	}{
		{"interactive express test", WorkClassExpressTest, true, LevelRed, 0, 300, true, true, RedLightAdmittedDecision},
		{"interactive express build beside nothing", WorkClassExpressBuild, true, LevelRed, 0, 600, true, true, RedLightAdmittedDecision},
		{"background work stays held", WorkClassExpressTest, false, LevelRed, 0, 300, true, false, ""},
		{"critical is never bypassed", WorkClassExpressTest, true, LevelCritical, 0, 300, true, false, "red_light_refused:level_not_red"},
		{"heavy class stays held", WorkClassBuild, true, LevelRed, 0, 300, true, false, "red_light_refused:weight_above_ceiling"},
		{"no peak RSS evidence", WorkClassExpressTest, true, LevelRed, 0, 0, false, false, "red_light_refused:peak_rss_calibration_unavailable"},
		{"class too large in memory", WorkClassExpressTest, true, LevelRed, 0, 1300, true, false, "red_light_refused:peak_rss_above_ceiling"},
		{"red lane capacity full", WorkClassExpressBuild, true, LevelRed, 1, 300, true, false, "red_light_refused:red_lane_capacity_full"},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			gate := redLightGate(testCase.class, testCase.interactive, testCase.used, testCase.peakMB, testCase.peakKnown)
			decision := gate.Observe(memoryRedAdmission(testCase.level), false)
			if decision.Allowed != testCase.allowed {
				t.Fatalf("allowed=%v want=%v decision=%+v", decision.Allowed, testCase.allowed, decision)
			}
			if label := gate.decisionLabel(); label != testCase.label {
				t.Fatalf("label=%q want=%q", label, testCase.label)
			}
			if testCase.allowed && !strings.Contains(decision.Reason, "interactive red lane") {
				t.Fatalf("reason does not name the lane: %q", decision.Reason)
			}
		})
	}
}

func TestInteractiveRedLaneNeverBypassesStorage(t *testing.T) {
	gate := redLightGate(WorkClassExpressTest, true, 0, 100, true)
	admission := Admission{Allowed: false, Level: LevelRed, Dimension: "storage", Reasons: []string{"storage available 2 GB <= red 5 GB"}}
	if decision := gate.Observe(admission, false); decision.Allowed {
		t.Fatalf("storage red admitted: %+v", decision)
	}
	if gate.redLightAdmitted {
		t.Fatal("red lane claimed a storage decision")
	}
}

// After acquisition the own lease is already in Used. Counting it again would
// revoke the admission only because the lease was granted.
func TestInteractiveRedLaneCountsOwnLeaseOnce(t *testing.T) {
	gate := redLightGate(WorkClassExpressBuild, true, 2, 300, true)
	if decision := gate.ObserveOwned(memoryRedAdmission(LevelRed), false); !decision.Allowed {
		t.Fatalf("owned lease revoked: %+v", decision)
	}
	gate = redLightGate(WorkClassExpressTest, true, 3, 300, true)
	if decision := gate.ObserveOwned(memoryRedAdmission(LevelRed), false); decision.Allowed {
		t.Fatalf("over-cap owned lease kept: %+v", decision)
	}
}

func TestInteractiveRedLaneCanBeDisabled(t *testing.T) {
	gate := redLightGate(WorkClassExpressTest, true, 0, 100, true)
	gate.limits.InteractiveRedLightCapacity = -1
	if decision := gate.Observe(memoryRedAdmission(LevelRed), false); decision.Allowed {
		t.Fatalf("disabled lane admitted: %+v", decision)
	}
	if label := gate.decisionLabel(); label != "red_light_refused:disabled" {
		t.Fatalf("label=%q", label)
	}
}

func TestPeakRSSP90NeedsEnoughCompletedSamples(t *testing.T) {
	events := []WorkEvent{
		{Event: WorkEventCompleted, CommandPeakRSSMB: 100},
		{Event: WorkEventCompleted, CommandPeakRSSMB: 200},
		{Event: WorkEventCompleted, CommandPeakRSSMB: 300},
		{Event: WorkEventCompleted, CommandPeakRSSMB: 400},
		{Event: WorkEventStarted, CommandPeakRSSMB: 9000},
	}
	if _, ok := peakRSSP90MB(events); ok {
		t.Fatal("four samples treated as evidence")
	}
	for value := 500.0; value <= 1000; value += 100 {
		events = append(events, WorkEvent{Event: WorkEventCompleted, CommandPeakRSSMB: value})
	}
	if peak, ok := peakRSSP90MB(events); !ok || peak != 900 {
		t.Fatalf("p90=%d ok=%v want 900", peak, ok)
	}
}

func TestInteractiveFromEnv(t *testing.T) {
	env := func(values map[string]string) func(string) string {
		return func(key string) string { return values[key] }
	}
	tests := []struct {
		values map[string]string
		want   bool
		source string
	}{
		{map[string]string{claudeSessionAttendedEnv: "1"}, true, interactiveSourceEnvClaude},
		{map[string]string{InteractiveEnv: "1"}, true, interactiveSourceEnvNDev},
		{map[string]string{InteractiveEnv: "0", claudeSessionAttendedEnv: "1"}, false, ""},
		{map[string]string{claudeSessionAttendedEnv: "0"}, false, ""},
		{map[string]string{}, false, ""},
	}
	for _, testCase := range tests {
		got, source := interactiveFromEnv(env(testCase.values))
		if got != testCase.want || source != testCase.source {
			t.Fatalf("%v: got=%v source=%q", testCase.values, got, source)
		}
	}
}

func TestWorkRunInteractiveComesFromAttendedSession(t *testing.T) {
	previous := workRunGetenv
	defer func() { workRunGetenv = previous }()
	workRunGetenv = func(key string) string {
		if key == claudeSessionAttendedEnv {
			return "1"
		}
		return ""
	}
	options, err := ParseWorkRunArgs([]string{"--class", "express-test", "--wait", "5m", "--", "go", "test", "./x"})
	if err != nil {
		t.Fatal(err)
	}
	// Interactive is a selector preference, never an operator pin: a pin can
	// run past the weighted cap when allow_override_beyond_capacity is on.
	if !options.Interactive || options.Priority || options.InteractiveSource != interactiveSourceEnvClaude {
		t.Fatalf("attended session: %+v", options)
	}
	options, err = ParseWorkRunArgs([]string{"--no-interactive", "--class", "express-test", "--", "go", "test", "./x"})
	if err != nil || options.Interactive {
		t.Fatalf("--no-interactive ignored: %+v err=%v", options, err)
	}
	workRunGetenv = func(string) string { return "" }
	options, err = ParseWorkRunArgs([]string{"--class", "express-test", "--", "go", "test", "./x"})
	if err != nil || options.Interactive {
		t.Fatalf("headless run marked interactive: %+v err=%v", options, err)
	}
	options, err = ParseWorkRunArgs([]string{"--interactive", "--class", "express-test", "--", "go", "test", "./x"})
	if err != nil || !options.Interactive || options.InteractiveSource != interactiveSourceFlag {
		t.Fatalf("--interactive ignored: %+v err=%v", options, err)
	}
}

func TestWorkRunDeliveryDarwinIsInteractiveWithoutPriority(t *testing.T) {
	previous := workRunGetenv
	defer func() { workRunGetenv = previous }()
	// The delivery worker runs headless: no attended-session environment.
	workRunGetenv = func(string) string { return "" }
	options, err := ParseWorkRunArgs([]string{"--workload", WorkloadDeliveryDarwin, "--class", "test", "--wait", "30m",
		"--placement", "local", "--placement-reason", "swift-macos-app", "--", "swift", "test"})
	if err != nil {
		t.Fatal(err)
	}
	// Ordering and the capped red lane only: never an operator pin.
	if !options.Interactive || options.Priority || options.InteractiveSource != interactiveSourceDeliveryDarwin {
		t.Fatalf("delivery-darwin: %+v", options)
	}
	if _, err := ParseWorkRunArgs([]string{"--workload", WorkloadDeliveryDarwin, "--no-interactive", "--", "swift", "test"}); err == nil {
		t.Fatal("--no-interactive with delivery-darwin accepted")
	}
	options, err = ParseWorkRunArgs([]string{"--workload", "precommithook", "--", "swift", "test"})
	if err != nil || options.Interactive {
		t.Fatalf("other workloads must not become interactive: %+v err=%v", options, err)
	}
}

func interactiveTestWaiter(id string, class WorkClass, weight int, interactive bool) WorkWaiterRecord {
	return WorkWaiterRecord{OperationID: id, Class: class, Weight: weight, Interactive: interactive}
}

func TestSelectorPrefersInteractiveWorkThatFits(t *testing.T) {
	waiters := []WorkWaiterRecord{
		interactiveTestWaiter("background-test", WorkClassTest, 3, false),
		interactiveTestWaiter("chat-build", WorkClassBuild, 5, true),
		interactiveTestWaiter("chat-express", WorkClassExpressTest, 1, true),
	}
	ordinary := workSelection{SelectedOperationID: "background-test", DecisionReason: "fifo_head_fits"}
	// 4 units free: the chat build (5) does not fit, the chat express test does.
	got := preferInteractiveWaiter(waiters, ordinary, 4, 8)
	if got.SelectedOperationID != "chat-express" || got.DecisionReason != "interactive_priority" ||
		len(got.BypassedIndexes) != 1 || got.BypassedIndexes[0] != 0 {
		t.Fatalf("selection=%+v", got)
	}
}

func TestSelectorInteractivePreferenceKeepsProtectionAndCapacity(t *testing.T) {
	waiters := []WorkWaiterRecord{
		interactiveTestWaiter("background-build", WorkClassBuild, 5, false),
		interactiveTestWaiter("chat-express", WorkClassExpressTest, 1, true),
	}
	drain := workSelection{ProtectedOperationID: "background-build", DecisionReason: "protected_bypass_age"}
	if got := preferInteractiveWaiter(waiters, drain, 4, 8); got.SelectedOperationID != "" || got.ProtectedOperationID != "background-build" {
		t.Fatalf("protected head lost its drain: %+v", got)
	}
	full := workSelection{DecisionReason: "fifo_head_does_not_fit"}
	if got := preferInteractiveWaiter(waiters, full, 8, 8); got.SelectedOperationID != "" {
		t.Fatalf("interactive admitted past capacity: %+v", got)
	}
	// An unprotected head that does not fit is passed once, and charged for it.
	got := preferInteractiveWaiter(waiters, full, 4, 8)
	if got.SelectedOperationID != "chat-express" || len(got.BypassedIndexes) != 1 {
		t.Fatalf("selection=%+v", got)
	}
	parked := time.Now()
	waiters[1].ReservationKind, waiters[1].ReservedAt = WorkReservationPressure, &parked
	if got := preferInteractiveWaiter(waiters, full, 4, 8); got.SelectedOperationID != "" {
		t.Fatalf("parked interactive reservation selected: %+v", got)
	}
}

// End to end through the coordinator: a background waiter queued first, an
// interactive waiter second, and room for one. The interactive one acquires.
func TestCoordinatorAdmitsInteractiveWaiterFirst(t *testing.T) {
	background := testWorkCoordinator(t)
	background.Limits = defaultWorkLimits(10)
	chat := *background // same test process; the stamp is per coordinator
	chat.Interactive = true
	ctx := context.Background()

	blocker, _, err := background.Acquire(ctx, WorkClassBuild)
	if err != nil {
		t.Fatal(err)
	}
	backgroundWaiter, _, err := background.RegisterWaiter(ctx, WorkClassTest, strings.Repeat("a", 32))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = backgroundWaiter.Cancel(ctx) }()
	chatWaiter, _, err := chat.RegisterWaiter(ctx, WorkClassExpressTest, strings.Repeat("b", 32))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = chatWaiter.Cancel(ctx) }()
	if err := blocker.Release(ctx); err != nil {
		t.Fatal(err)
	}
	lease, _, err := chatWaiter.TryAcquire(ctx)
	if err != nil {
		t.Fatalf("interactive waiter did not go first: %v", err)
	}
	defer func() { _ = lease.Release(ctx) }()
	if _, _, err := backgroundWaiter.TryAcquire(ctx); err != nil {
		t.Fatalf("background waiter blocked although capacity remains: %v", err)
	}
}
