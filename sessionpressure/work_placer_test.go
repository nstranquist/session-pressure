package sessionpressure

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDecidePlacement(t *testing.T) {
	small := []int64{4_000, 5_000, 6_000}
	slowLocal := []int64{40_000, 50_000, 60_000}
	remote := []int64{30_000, 32_000, 35_000}
	cases := map[string]struct {
		idle    bool
		history placementHistory
		local   bool
		reason  string
	}{
		"busy mac stays remote even when local is faster": {idle: false, history: placementHistory{small, remote}, reason: "mac_busy: 3 waiters queued"},
		"idle and local faster":                           {idle: true, history: placementHistory{small, remote}, local: true, reason: "local_faster"},
		"idle but remote faster":                          {idle: true, history: placementHistory{slowLocal, remote}, reason: "remote_faster"},
		"explore a small remote check":                    {idle: true, history: placementHistory{nil, remote}, local: true, reason: "explore_local"},
		"explore a 49s remote check under 60s ceiling":    {idle: true, history: placementHistory{nil, []int64{48_870, 50_000}}, local: true, reason: "explore_local"},
		"no exploration for a large check":                {idle: true, history: placementHistory{nil, []int64{90_000, 120_000}}, reason: "remote_above_explore_ceiling"},
		"one sample is not history":                       {idle: true, history: placementHistory{[]int64{1_000}, []int64{90_000}}, reason: "no_local_history"},
		"remote history aged out":                         {idle: true, history: placementHistory{small, nil}, reason: "no_remote_history"},
		"nothing measured":                                {idle: true, reason: "no_local_history"},
	}
	for name, tc := range cases {
		got := decidePlacement(tc.idle, "3 waiters queued", tc.history)
		if got.Local != tc.local || got.Reason != tc.reason {
			t.Errorf("%s: got local=%v reason=%q, want local=%v reason=%q", name, got.Local, got.Reason, tc.local, tc.reason)
		}
	}
	got := decidePlacement(true, "", placementHistory{small, remote})
	if got.LocalETAMS != 6_000 || got.RemoteETAMS != 32_000 || got.LocalSamples != 3 || got.RemoteSamples != 3 {
		t.Fatalf("etas: %+v", got)
	}
}

func TestPercentileMS(t *testing.T) {
	values := []int64{9, 1, 5, 3, 7}
	for percent, want := range map[int]int64{50: 5, 75: 7, 100: 9, 1: 1} {
		if got := percentileMS(values, percent); got != want {
			t.Errorf("p%d = %d, want %d", percent, got, want)
		}
	}
	if percentileMS(nil, 50) != 0 {
		t.Fatal("empty percentile")
	}
}

func TestPlacementKeySeparatesDirectoriesAndArgs(t *testing.T) {
	a := PlacementKey("/repo/web", []string{"npm", "test"})
	if !validSHA256Digest(a) || a != PlacementKey("/repo/web/", []string{"npm", "test"}) {
		t.Fatalf("key not stable: %s", a)
	}
	for _, other := range []string{
		PlacementKey("/repo/api", []string{"npm", "test"}),
		PlacementKey("/repo/web", []string{"npm", "run", "test"}),
		PlacementKey("/repo/web", []string{"npm test"}),
	} {
		if other == a {
			t.Fatal("distinct checks share a key")
		}
	}
}

func TestReadPlacementHistoryJoinsLocalAndRemote(t *testing.T) {
	dir := t.TempDir()
	key := PlacementKey("/repo/web", []string{"npm", "test"})
	otherKey := PlacementKey("/repo/api", []string{"npm", "test"})
	now := time.Now().UTC()
	store := NewWorkEventStore(dir)
	for _, eventType := range []WorkEventType{WorkEventQueued, WorkEventAcquired, WorkEventStarted, WorkEventCompleted} {
		event := validWorkEvent(now, eventType)
		event.PlacementKey = key
		if eventType == WorkEventCompleted {
			event.RuntimeMillis = 5_500
		}
		if eventType == WorkEventStarted {
			event.RuntimeMillis = 1 // only completions count
		}
		if err := store.AppendDurable(event); err != nil {
			t.Fatal(err)
		}
	}
	var body bytes.Buffer
	for _, row := range []map[string]any{
		{"event": "remote_work_finished", "ts": now.Format(time.RFC3339Nano), "attrs": map[string]any{"duration_ms": 32_000, "placement_key": key}},
		{"event": "remote_work_finished", "ts": now.Format(time.RFC3339Nano), "attrs": map[string]any{"duration_ms": 9_000, "failed": true, "placement_key": key}},
		{"event": "remote_work_finished", "ts": now.Add(-8 * 24 * time.Hour).Format(time.RFC3339Nano), "attrs": map[string]any{"duration_ms": 1, "placement_key": key}},
		{"event": "remote_work_finished", "ts": now.Format(time.RFC3339Nano), "attrs": map[string]any{"duration_ms": 77, "placement_key": otherKey}},
		{"event": "work_placement_decided", "ts": now.Format(time.RFC3339Nano), "attrs": map[string]any{"duration_ms": 5, "placement_key": key}},
	} {
		line, _ := json.Marshal(row)
		body.Write(append(line, '\n'))
	}
	if err := os.WriteFile(filepath.Join(dir, "work-control-events.jsonl"), body.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	history, _ := readPlacementHistory(dir, PlacementKeys{Exact: key}, now)
	if len(history.localMS) != 1 || history.localMS[0] != 5_500 {
		t.Fatalf("local history: %v", history.localMS)
	}
	if len(history.remoteMS) != 1 || history.remoteMS[0] != 32_000 {
		t.Fatalf("remote history (failed, old, other key and non-finish rows must drop): %v", history.remoteMS)
	}
}

func TestPlaceLocallyRecordsEveryDecision(t *testing.T) {
	dir := t.TempDir()
	key := PlacementKeys{Exact: PlacementKey("/repo/web", []string{"npm", "test"})}
	options := WorkRunOptions{Class: WorkClassTest, Command: []string{"npm", "test"}}
	options.placementProbe = func() (bool, string) { return false, "2 waiters queued" }
	var stderr bytes.Buffer
	if placeLocally(dir, RemoteWorkPolicy{Placer: false}, options, key, ToolFamilyNode, &stderr) {
		t.Fatal("disabled placer chose local")
	}
	if placeLocally(dir, RemoteWorkPolicy{Placer: true}, options, key, ToolFamilyNode, &stderr) {
		t.Fatal("busy Mac chose local")
	}
	options.Placement = WorkPlacementRemote
	options.placementProbe = func() (bool, string) { return true, "" }
	if placeLocally(dir, RemoteWorkPolicy{Placer: true}, options, key, ToolFamilyNode, &stderr) {
		t.Fatal("explicit remote placement was overridden")
	}
	// An idle Mac with a small remote history explores locally, loudly.
	options.Placement = WorkPlacementAuto
	for range 2 {
		recordControlEvent(dir, map[string]any{"event": "remote_work_finished", "duration_ms": 30_000, "failed": false, "placement_key": key.Exact})
	}
	if !placeLocally(dir, RemoteWorkPolicy{Placer: true}, options, key, ToolFamilyNode, &stderr) {
		t.Fatal("idle Mac with a small check did not explore locally")
	}
	if !strings.Contains(stderr.String(), "placer kept this check on the idle Mac (explore_local by key history") {
		t.Fatalf("local decision was silent: %q", stderr.String())
	}
	raw, err := os.ReadFile(filepath.Join(dir, "work-control-events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	decisions := 0
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if strings.Contains(line, `"work_placement_decided"`) {
			decisions++
		}
	}
	if got := decisions; got != 2 {
		t.Fatalf("decisions recorded = %d, want 2 (busy and explore; disabled and explicit are not decisions)", got)
	}
}

func TestMacIdleProbeUsesTheResidentLevel(t *testing.T) {
	dir := t.TempDir()
	coordinator := NewWorkCoordinator(dir, defaultWorkLimits(8))
	observeOnly := func() Admission { return Admission{Allowed: true, Level: LevelNormal, Source: "observe-only"} }
	probe := macIdleProbe(coordinator, WorkClassTest, observeOnly)
	if idle, reason := probe(); idle || reason != "no fresh resident sample" {
		t.Fatalf("no sample: idle=%v %q", idle, reason)
	}
	write := func(level Level) {
		body, _ := json.Marshal(Snapshot{Timestamp: time.Now().UTC(), Level: level, FreePercent: 30, PhysicalMemoryMB: 16384})
		if err := os.WriteFile(filepath.Join(dir, "latest.json"), body, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// Observe mode admits at red; the placer must still see a busy Mac.
	write(LevelRed)
	if idle, reason := probe(); idle || reason != "host level red" {
		t.Fatalf("red host: idle=%v %q", idle, reason)
	}
	write(LevelNormal)
	if idle, reason := probe(); !idle {
		t.Fatalf("normal idle host: %q", reason)
	}
}
