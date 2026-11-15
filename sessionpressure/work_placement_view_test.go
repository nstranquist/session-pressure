package sessionpressure

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nstranquist/session-pressure/runnerfleet"
)

func TestEstimateWaiterETAsSimulatesFIFO(t *testing.T) {
	now := time.Date(2026, 11, 15, 18, 0, 0, 0, time.UTC)
	p50 := map[WorkClass]int64{WorkClassBuild: 60_000, WorkClassTest: 20_000}
	status := WorkStatus{Used: 6, Available: 2,
		Leases: []WorkLeaseStatus{
			{ID: "a", Class: WorkClassBuild, Weight: 5, StartedAt: now.Add(-20 * time.Second)}, // ends +40 s
			{ID: "b", Class: WorkClassTest, Weight: 1, StartedAt: now.Add(-30 * time.Second)},  // overdue: ends now
			{ID: "p", Class: WorkClassHeavy, Weight: 6, Parked: true},
		},
		Waiters: []WorkWaiterStatus{
			{OperationID: "w2", Class: WorkClassTest, Weight: 2, Position: 2},
			{OperationID: "w1", Class: WorkClassBuild, Weight: 5, Position: 1},
			{OperationID: "w3", Class: WorkClassBrowser, Weight: 2, Position: 3},
			{OperationID: "w4", Class: WorkClassTest, Weight: 2, Position: 4},
			{OperationID: "parked", Class: WorkClassTest, Weight: 2, Position: 5, PressureParked: true},
		}}
	etas := estimateWaiterETAs(status, p50, now)
	want := []struct {
		id  string
		eta int64
	}{{"w1", 40_000}, {"w2", 40_000}, {"w3", 60_000}, {"w4", -1}, {"parked", -1}}
	if len(etas) != len(want) {
		t.Fatalf("etas: %+v", etas)
	}
	for i, w := range want {
		if etas[i].OperationID != w.id || etas[i].ETAMS != w.eta {
			t.Fatalf("eta %d: got %+v want %+v", i, etas[i], w)
		}
	}
	// w1 (5) needs a (5) to finish; w2 (2) then fits beside w1 at +40 s.
	// w3 (2) starts when w2 ends at +60 s. Its class is unmeasured, so w4's
	// ETA is unknown, not guessed.
	if etas[3].Basis == "" || etas[4].Basis != "parked by host pressure" {
		t.Fatalf("bases: %+v", etas)
	}
}

func writeControlRows(t *testing.T, dir string, rows ...map[string]any) {
	t.Helper()
	file, err := os.OpenFile(filepath.Join(dir, "work-control-events.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	for _, row := range rows {
		body, _ := json.Marshal(row)
		_, _ = file.Write(append(body, '\n'))
	}
}

func TestBuildPlacementReportJoinsControlEvents(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC()
	ts := func(ago time.Duration) string { return now.Add(-ago).Format(time.RFC3339Nano) }
	stall := func(name string, ago time.Duration) map[string]any {
		return map[string]any{"event": name, "ts": ts(ago), "attrs": QueueStallEvent(name, QueueStall{Since: now.Add(-ago - 30*time.Second), OperationID: "w", Class: WorkClassTest, Weight: 2, EffectiveCapacity: 8})}
	}
	writeControlRows(t, dir,
		map[string]any{"event": "work_placement_decided", "ts": ts(8 * time.Hour), "attrs": map[string]any{"placement": "local", "reason": "local_faster"}},
		map[string]any{"event": "work_placement_decided", "ts": ts(time.Hour), "attrs": map[string]any{"placement": "local", "reason": "explore_local"}},
		map[string]any{"event": "work_placement_decided", "ts": ts(2 * time.Minute), "attrs": map[string]any{"placement": "remote", "reason": "mac_busy: 6 waiters queued"}},
		map[string]any{"event": "work_placement_decided", "ts": ts(time.Minute), "attrs": map[string]any{"placement": "remote", "reason": "no_local_history"}},
		map[string]any{"event": "remote_work_finished", "ts": ts(time.Minute), "attrs": map[string]any{"duration_ms": 30_000, "failed": false, "runner_host": "runner-3"}},
		map[string]any{"event": "remote_work_finished", "ts": ts(2 * time.Minute), "attrs": map[string]any{"duration_ms": 90_000, "failed": true, "exit_code": 1}},
		map[string]any{"event": "work_local_placement", "ts": ts(time.Minute), "attrs": map[string]any{"reason": "swift-macos-app"}},
		stall("work_queue_stall", 2*time.Minute),
	)
	queued := WorkStatus{Waiters: []WorkWaiterStatus{{OperationID: "w", Class: WorkClassTest, Weight: 2, Position: 1}}, Available: 8}
	report := BuildPlacementReport(context.Background(), dir, queued, 6*time.Hour, false, nil, now)
	if report.Decisions.Total != 3 || report.Decisions.Local != 1 || report.Decisions.ByReason["explore_local"] != 1 || report.Decisions.ByReason["mac_busy"] != 1 || report.Decisions.Recent[0].Reason != "no_local_history" || report.Decisions.Recent[1].Reason != "mac_busy: 6 waiters queued" {
		t.Fatalf("decisions: %+v", report.Decisions)
	}
	if report.RemoteRuns.Total != 2 || report.RemoteRuns.Failed != 1 || report.RemoteRuns.P50MS != 30_000 || report.RemoteRuns.P90MS != 90_000 {
		t.Fatalf("remote runs: %+v", report.RemoteRuns)
	}
	if report.LocalFallbacks["swift-macos-app"] != 1 {
		t.Fatalf("fallbacks: %+v", report.LocalFallbacks)
	}
	if report.ActiveStall == nil || report.ActiveStall.OperationID != "w" || report.ActiveStall.DurationMS < 150_000 {
		t.Fatalf("open stall not reported: %+v", report.ActiveStall)
	}
	// The waiter left the queue without a recorded clear: not open.
	if got := BuildPlacementReport(context.Background(), dir, WorkStatus{}, 6*time.Hour, false, nil, now); got.ActiveStall != nil || got.LastStall == nil {
		t.Fatalf("stale stall reported open: %+v", got.ActiveStall)
	}
	writeControlRows(t, dir, stall("work_queue_stall_cleared", time.Minute))
	if got := BuildPlacementReport(context.Background(), dir, queued, 6*time.Hour, false, nil, now); got.ActiveStall != nil || got.LastStall.EndedAt.IsZero() {
		t.Fatalf("cleared stall reported open: %+v", got)
	}
}

func TestRunnerSlotsProbesOnlyActiveHosts(t *testing.T) {
	dir := t.TempDir()
	commit := "0123456789abcdef0123456789abcdef01234567"
	host := func(name, state string) runnerfleet.Host {
		return runnerfleet.Host{Name: name, User: "builder", State: state, Provider: "hetzner-cloud", Platform: "linux/amd64", SourceCommit: commit, Weight: 1, HeavySlots: 8, LightSlots: 2, MaxShards: 1, Repositories: []string{"*"}, Profiles: []string{"linux-native"}}
	}
	config := runnerfleet.Config{Schema: runnerfleet.Schema, Generation: "g1", Hosts: []runnerfleet.Host{host("runner-3", "active"), host("runner-1", "reserve")}}
	body, _ := json.Marshal(config)
	path := filepath.Join(dir, "fleet.json")
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := runnerfleet.Load(path); err != nil {
		t.Skipf("fixture fleet does not validate: %v", err)
	}
	var probed []string
	slots, errText := runnerSlots(context.Background(), path, func(_ context.Context, h runnerfleet.Host) runnerfleet.Observation {
		probed = append(probed, h.Name)
		return runnerfleet.Observation{Host: h, Healthy: true, HeavyJobs: 3, LightJobs: 1}
	})
	if errText != "" || len(probed) != 1 || probed[0] != "runner-3" || len(slots) != 1 || slots[0].HeavyJobs != 3 || slots[0].HeavySlots != 8 {
		t.Fatalf("slots %+v err %q probed %v", slots, errText, probed)
	}
}
