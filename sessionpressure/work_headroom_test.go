package sessionpressure

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func completedPeak(class WorkClass, workload, key string, mb float64) WorkEvent {
	return WorkEvent{Event: WorkEventCompleted, Class: class, Workload: workload, PlacementKey: key, CommandPeakRSSMB: mb}
}

func TestPeakRSSPredictionPrefersTheMostSpecificScope(t *testing.T) {
	key := PlacementKey("/repo", []string{"swift", "test"})
	var events []WorkEvent
	for i := range 6 {
		events = append(events, completedPeak(WorkClassTest, "", "", float64(200+i)))
	}
	for i := range 5 {
		events = append(events, completedPeak(WorkClassTest, WorkloadDeliveryDarwin, "", float64(900+i)))
	}
	events = append(events, completedPeak(WorkClassTest, "", key, 3000), completedPeak(WorkClassTest, "", key, 3100))
	events = append(events, WorkEvent{Event: WorkEventStarted, Class: WorkClassTest, CommandPeakRSSMB: 9999})
	// Probes and unwaited wrappers report about 1 MB; they are not samples.
	for range 5 {
		events = append(events, completedPeak(WorkClassTest, WorkloadDeliveryDarwin, "", 1))
	}
	history := buildPeakRSSHistory(events)

	// Two key samples are not enough: the workload decides.
	if mb, source := history.predict(WorkClassTest, WorkloadDeliveryDarwin, key); source != predictionByLoad || mb != 904 {
		t.Fatalf("workload prediction: %d %s", mb, source)
	}
	addPeak(history.byKey, key, 3200, false)
	if mb, source := history.predict(WorkClassTest, WorkloadDeliveryDarwin, key); source != predictionByKey || mb != 3200 {
		t.Fatalf("key prediction: %d %s", mb, source)
	}
	if _, source := history.predict(WorkClassTest, "", ""); source != predictionByClass {
		t.Fatalf("class prediction: %s", source)
	}
	if mb, source := history.predict(WorkClassHeavy, "", ""); source != predictionFallback || mb != headroomFallbackPeakMB {
		t.Fatalf("fallback: %d %s", mb, source)
	}
	// Non-completion rows never enter history.
	for _, sample := range history.byClass[string(WorkClassTest)].all {
		if sample == 9999 {
			t.Fatal("started event counted")
		}
	}
}

func headroomSnapshot(now time.Time, freePercent int, level Level) Snapshot {
	return Snapshot{Timestamp: now, Level: level, FreePercent: freePercent, PhysicalMemoryMB: 16384,
		CoordinatedWork: CoordinatedWorkSnapshot{Available: true, Fresh: true, RSSSumMB: 300}}
}

func TestComputeHeadroomChargesInflightPredictions(t *testing.T) {
	now := time.Now()
	var events []WorkEvent
	for range 5 {
		events = append(events, completedPeak(WorkClassBuild, "", "", 1000))
	}
	history := buildPeakRSSHistory(events)
	leases := []WorkLeaseStatus{{ID: "a", Class: WorkClassBuild}, {ID: "b", Class: WorkClassBuild}, {ID: "c", Class: WorkClassBuild, Parked: true}}
	headroom := computeHeadroom(headroomSnapshot(now, 50, LevelNormal), true, leases, history, now)
	// available 8192 - reserve 2048 - (2 x 1000 predicted - 300 measured) = 4444
	if headroom.AvailableMB != 8192 || headroom.ReserveMB != 2048 || headroom.LeaseCount != 2 || headroom.InflightMB != 1700 || headroom.HeadroomMB != 4444 || !headroom.SampleFresh {
		t.Fatalf("headroom: %+v", headroom)
	}
	// Unknown leased RSS charges the full predictions.
	snapshot := headroomSnapshot(now, 50, LevelNormal)
	snapshot.CoordinatedWork.Fresh = false
	if got := computeHeadroom(snapshot, true, leases, history, now); got.InflightMB != 2000 || got.LeasedRSSKnown {
		t.Fatalf("unknown leased RSS: %+v", got)
	}
	if got := computeHeadroom(headroomSnapshot(now.Add(-10*time.Minute), 50, LevelNormal), true, nil, history, now); got.SampleFresh {
		t.Fatal("stale sample reported fresh")
	}
}

func TestDecideMemoryAdmission(t *testing.T) {
	fresh := MemoryHeadroom{SampleFresh: true, Level: LevelNormal, HeadroomMB: 1000}
	cases := map[string]struct {
		headroom    MemoryHeadroom
		predicted   int64
		interactive bool
		weight      int
		want        string
	}{
		"fits":                      {fresh, 900, false, 5, "admit:fits"},
		"no room":                   {fresh, 1100, false, 5, "refuse:no_headroom"},
		"no sample":                 {MemoryHeadroom{Level: LevelNormal, HeadroomMB: 9000}, 1, false, 1, "refuse:no_sample"},
		"critical":                  {MemoryHeadroom{SampleFresh: true, Level: LevelCritical, HeadroomMB: 9000}, 1, true, 1, "refuse:critical"},
		"red background":            {MemoryHeadroom{SampleFresh: true, Level: LevelRed, HeadroomMB: 9000}, 1, false, 1, "refuse:red"},
		"red lane kept (Nico)":      {MemoryHeadroom{SampleFresh: true, Level: LevelRed, HeadroomMB: -500}, 200, true, 2, "admit:red_lane"},
		"red lane is weight capped": {MemoryHeadroom{SampleFresh: true, Level: LevelRed, HeadroomMB: 9000}, 200, true, 5, "refuse:red"},
		"swap-out rate":             {MemoryHeadroom{SampleFresh: true, Level: LevelWarning, SwapOutMBPerSecond: 40, HeadroomMB: 9000}, 1, false, 1, "refuse:swap_thrash"},
		"swap thrash":               {MemoryHeadroom{SampleFresh: true, Level: LevelWarning, SwapThrashLatched: true, HeadroomMB: 9000}, 1, false, 1, "refuse:swap_thrash"},
	}
	for name, tc := range cases {
		got := decideMemoryAdmission(tc.headroom, tc.predicted, predictionByClass, tc.interactive, tc.weight, 2)
		if got.Label() != tc.want || !validShadowMemoryDecision(got.Label()) {
			t.Errorf("%s: got %s, want %s", name, got.Label(), tc.want)
		}
	}
}

func TestShadowMemoryFieldsValidate(t *testing.T) {
	event := validWorkEvent(time.Now().UTC(), WorkEventQueued)
	event.EventID = workEventID(event.OperationID, event.Event, event.LeaseID)
	ShadowMemoryDecision{Admit: false, Reason: headroomNoRoom, PredictedPeakMB: 900, PredictionSource: predictionByClass, HeadroomMB: -120}.apply(&event)
	if err := event.Validate(); err != nil || event.ShadowMemoryRule != HeadroomRuleVersion {
		t.Fatalf("valid shadow fields rejected or unversioned: %v %q", err, event.ShadowMemoryRule)
	}
	event.ShadowMemoryRule = "runner-memadmit-p95"
	if err := event.Validate(); err == nil {
		t.Fatal("foreign rule version accepted")
	}
	event.ShadowMemoryRule = HeadroomRuleVersion
	event.ShadowMemoryDecision = "admit:because"
	if err := event.Validate(); err == nil {
		t.Fatal("unknown shadow decision accepted")
	}
	event.ShadowMemoryDecision, event.ShadowPredictionSource = "admit:fits", "guess"
	if err := event.Validate(); err == nil {
		t.Fatal("unknown prediction source accepted")
	}
}

func TestReadMemoryHeadroomFromStores(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC()
	body, _ := json.Marshal(headroomSnapshot(now, 40, LevelWarning))
	if err := os.WriteFile(filepath.Join(dir, "latest.json"), body, 0o600); err != nil {
		t.Fatal(err)
	}
	if NewTelemetryStore(dir).LatestPath() != filepath.Join(dir, "latest.json") {
		t.Skip("latest path layout changed")
	}
	coordinator := NewWorkCoordinator(dir, defaultWorkLimits(8))
	headroom, err := ReadMemoryHeadroom(context.Background(), dir, coordinator, now)
	if err != nil {
		t.Fatal(err)
	}
	if !headroom.SampleFresh || headroom.Level != LevelWarning || headroom.AvailableMB != 6553 || headroom.LeaseCount != 0 {
		t.Fatalf("headroom: %+v", headroom)
	}
}

func TestShadowMemoryProbeReadsTheResidentSample(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC()
	body, _ := json.Marshal(headroomSnapshot(now, 50, LevelNormal))
	if err := os.WriteFile(filepath.Join(dir, "latest.json"), body, 0o600); err != nil {
		t.Fatal(err)
	}
	probe := &shadowMemoryProbe{dir: dir, class: WorkClassTest, weight: 2, fastLane: 2}
	// A storage-warning admission carries no memory snapshot.
	decision := probe.decide(Admission{Allowed: true, Level: LevelWarning, Source: "storage"}, nil, now)
	if decision.Label() != "admit:fits" || decision.PredictionSource != predictionFallback || decision.HeadroomMB != 8192-2048 {
		t.Fatalf("decision: %+v", decision)
	}
}

func treePeak(class WorkClass, key string, mb float64, at time.Time) WorkEvent {
	event := completedPeak(class, "", key, mb)
	event.CommandTreePeakRSSMB, event.CommandTreePeakSamples, event.Timestamp = mb, 10, at
	return event
}

func TestPeakRSSPredictionPrefersTreeSamplesAndWidensBroadScopes(t *testing.T) {
	key := PlacementKey("/repo", []string{"swift", "test"})
	var events []WorkEvent
	// Legacy rows saw only the largest process (wait4) and understate.
	for range 20 {
		events = append(events, completedPeak(WorkClassTest, "", key, 250))
	}
	history := buildPeakRSSHistory(events)
	if mb, source := history.predict(WorkClassTest, "", key); mb != 250 || source != predictionByKey {
		t.Fatalf("legacy only: %d %s", mb, source)
	}
	// Three tree-measured runs replace the twenty legacy rows for this key.
	for _, mb := range []float64{1500, 1600, 1550} {
		events = append(events, treePeak(WorkClassTest, key, mb, time.Time{}))
	}
	if mb, source := buildPeakRSSHistory(events).predict(WorkClassTest, "", key); mb != 1600 || source != predictionByKey {
		t.Fatalf("tree preferred: %d %s", mb, source)
	}
	// The class scope uses p95, not p90: rank 19 of 20 against rank 18.
	var class []WorkEvent
	for i := range 20 {
		class = append(class, treePeak(WorkClassBuild, "", float64(100*(i+1)), time.Time{}))
	}
	if mb, source := buildPeakRSSHistory(class).predict(WorkClassBuild, "", ""); mb != 1900 || source != predictionByClass {
		t.Fatalf("class p95: %d %s", mb, source)
	}
}

func TestReplayHeadroomPredictorUsesOnlyEarlierRuns(t *testing.T) {
	start := time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC)
	var events []WorkEvent
	for i := range 5 {
		events = append(events, treePeak(WorkClassTest, "", 500, start.Add(time.Duration(i)*time.Minute)))
	}
	// A later run far above the history is a big miss; it cannot predict itself.
	events = append(events, treePeak(WorkClassTest, "", 2000, start.Add(10*time.Minute)))
	// A run older than the window before it is not history.
	stale := treePeak(WorkClassTest, "", 9000, start.Add(-4*24*time.Hour))
	events = append(events, stale)
	replay := ReplayHeadroomPredictor(events, start.Add(5*time.Minute))
	if replay.Runs != 1 || replay.Misses != 1 || replay.BigMisses != 1 || replay.MeanPredictedMB != 500 || replay.BySource[predictionByClass] != 1 {
		t.Fatalf("replay %+v", replay)
	}
	// From the start, the first five predict from the fallback and fewer runs.
	all := ReplayHeadroomPredictor(events, start)
	if all.Runs != 6 || all.BySource[predictionFallback] != 5 {
		t.Fatalf("replay all %+v", all)
	}
}
