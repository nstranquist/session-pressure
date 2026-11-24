package sessionpressure

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/nstranquist/session-pressure/runnerfleet"
)

// Work queue v2 slice 6: the placement view behind the app's "Placement &
// offload" card. One read joins what the placer decided, what the runner
// did with offloaded work, the stall guard's latest episode, an ETA for each
// local waiter and the live slots of the active runner hosts. Every part is
// read-only and bounded, so the app can poll it.

const placementViewRecent = 10

// PlacementDecisionRow is one work_placement_decided control event.
type PlacementDecisionRow struct {
	At            time.Time `json:"at"`
	Placement     string    `json:"placement"`
	Reason        string    `json:"reason"`
	Class         string    `json:"class,omitempty"`
	ToolFamily    string    `json:"tool_family,omitempty"`
	LocalETAMS    int64     `json:"local_eta_ms,omitempty"`
	RemoteETAMS   int64     `json:"remote_eta_ms,omitempty"`
	LocalSamples  int       `json:"local_samples"`
	RemoteSamples int       `json:"remote_samples"`
}

// RemoteRunRow is one remote_work_finished control event.
type RemoteRunRow struct {
	At         time.Time `json:"at"`
	Operation  string    `json:"operation,omitempty"`
	Profile    string    `json:"profile,omitempty"`
	RunnerHost string    `json:"runner_host,omitempty"`
	DurationMS int64     `json:"duration_ms"`
	ExitCode   int       `json:"exit_code"`
	Failed     bool      `json:"failed"`
}

type PlacementDecisionSummary struct {
	Total    int                    `json:"total"`
	Local    int                    `json:"local"`
	Remote   int                    `json:"remote"`
	ByReason map[string]int         `json:"by_reason"`
	Recent   []PlacementDecisionRow `json:"recent"`
}

type RemoteRunSummary struct {
	Total  int            `json:"total"`
	Failed int            `json:"failed"`
	P50MS  int64          `json:"p50_ms"`
	P90MS  int64          `json:"p90_ms"`
	Recent []RemoteRunRow `json:"recent"`
}

// WaiterETA estimates when a local waiter starts. ETAMS is -1 when unknown.
type WaiterETA struct {
	OperationID string    `json:"operation_id"`
	Class       WorkClass `json:"class"`
	Weight      int       `json:"weight"`
	Position    int       `json:"position"`
	WaitMS      int64     `json:"wait_ms"`
	ETAMS       int64     `json:"eta_ms"`
	Basis       string    `json:"basis"`
}

// RunnerSlots is one runner host's live slot use.
type RunnerSlots struct {
	Name       string `json:"name"`
	State      string `json:"state"`
	Healthy    bool   `json:"healthy"`
	HeavySlots int    `json:"heavy_slots"`
	HeavyJobs  int    `json:"heavy_jobs"`
	LightSlots int    `json:"light_slots"`
	LightJobs  int    `json:"light_jobs"`
	Error      string `json:"error,omitempty"`
}

type PlacementRemoteConfig struct {
	Enabled bool   `json:"enabled"`
	Placer  bool   `json:"placer"`
	Node    bool   `json:"node"`
	Host    string `json:"host,omitempty"`
}

// ParkedLeaseStall identifies a live lease that has remained idle/parked long
// enough to warrant lock-holder inspection.
type ParkedLeaseStall struct {
	OperationID      string    `json:"operation_id"`
	Class            WorkClass `json:"class"`
	PID              int       `json:"pid"`
	Weight           int       `json:"weight"`
	ParkedAt         time.Time `json:"parked_at"`
	ParkedDurationMS int64     `json:"parked_duration_ms"`
	AgeMS            int64     `json:"age_ms"`
}

// PlacementReport is the payload of `session-pressure work placement`.
type PlacementReport struct {
	GeneratedAt    time.Time                `json:"generated_at"`
	WindowSeconds  int64                    `json:"window_seconds"`
	Remote         PlacementRemoteConfig    `json:"remote"`
	Decisions      PlacementDecisionSummary `json:"decisions"`
	RemoteRuns     RemoteRunSummary         `json:"remote_runs"`
	LocalFallbacks map[string]int           `json:"local_fallbacks"`
	ActiveStall    *QueueStall              `json:"active_stall,omitempty"`
	LastStall      *QueueStall              `json:"last_stall,omitempty"`
	ParkedStalls   []ParkedLeaseStall       `json:"parked_stalls,omitempty"`
	Waiters        []WaiterETA              `json:"waiters"`
	ETABasis       string                   `json:"eta_basis"`
	Runners        []RunnerSlots            `json:"runners,omitempty"`
	RunnersError   string                   `json:"runners_error,omitempty"`
}

const waiterETABasis = "FIFO estimate from 7-day class p50 runtimes; ignores pressure holds and lookahead"

type controlRow struct {
	Event string          `json:"event"`
	TS    time.Time       `json:"ts"`
	Attrs json.RawMessage `json:"attrs"`
}

// readControlRows returns the work control events at or after since.
func readControlRows(dir string, since time.Time, names ...string) []controlRow {
	file, err := os.Open(filepath.Join(dir, "work-control-events.jsonl"))
	if err != nil {
		return nil
	}
	defer file.Close()
	var rows []controlRow
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64<<10), 1<<20)
	for scanner.Scan() {
		var row controlRow
		if json.Unmarshal(scanner.Bytes(), &row) != nil || row.TS.Before(since) || !slices.Contains(names, row.Event) {
			continue
		}
		rows = append(rows, row)
	}
	return rows
}

func lastN[T any](rows []T, n int) []T {
	if len(rows) > n {
		rows = rows[len(rows)-n:]
	}
	out := slices.Clone(rows)
	slices.Reverse(out)
	return out
}

// BuildPlacementReport reads the placement view. status may be zero when the
// queue is unavailable. Runner slots are probed only when probeRunners is
// true and a fleet inventory is configured; only active hosts are probed.
func BuildPlacementReport(ctx context.Context, dir string, status WorkStatus, window time.Duration, probeRunners bool, probe runnerfleet.ProbeFunc, now time.Time) PlacementReport {
	report := PlacementReport{GeneratedAt: now.UTC(), WindowSeconds: int64(window / time.Second), LocalFallbacks: map[string]int{}, ETABasis: waiterETABasis}
	report.Decisions.ByReason = map[string]int{}
	policy, _ := RemoteWorkConfig(dir)
	report.Remote = PlacementRemoteConfig{Enabled: policy.Enabled, Placer: policy.Placer, Node: policy.Node, Host: policy.Host}

	since := now.Add(-window)
	var decisions []PlacementDecisionRow
	var runs []RemoteRunRow
	var lastStall *QueueStall
	stallCleared := false
	// Stalls are read over a longer window so an open episode is not lost.
	for _, row := range readControlRows(dir, now.Add(-max(window, 24*time.Hour)), "work_placement_decided", "remote_work_finished", "work_local_placement", "work_queue_stall", "work_queue_stall_cleared") {
		inWindow := !row.TS.Before(since)
		switch row.Event {
		case "work_placement_decided":
			var d PlacementDecisionRow
			if inWindow && json.Unmarshal(row.Attrs, &d) == nil {
				d.At = row.TS
				decisions = append(decisions, d)
			}
		case "remote_work_finished":
			var r RemoteRunRow
			if inWindow && json.Unmarshal(row.Attrs, &r) == nil {
				r.At = row.TS
				runs = append(runs, r)
			}
		case "work_local_placement":
			var attrs struct {
				Reason string `json:"reason"`
			}
			if inWindow && json.Unmarshal(row.Attrs, &attrs) == nil {
				report.LocalFallbacks[boundedText(attrs.Reason, 80)]++
			}
		case "work_queue_stall", "work_queue_stall_cleared":
			var stall QueueStall
			if json.Unmarshal(row.Attrs, &stall) != nil {
				continue
			}
			stall.DetectedAt = row.TS
			if row.Event == "work_queue_stall_cleared" {
				stall.EndedAt = row.TS
			}
			lastStall, stallCleared = &stall, row.Event == "work_queue_stall_cleared"
		}
	}
	report.Decisions.Total = len(decisions)
	for _, d := range decisions {
		// "mac_busy: 6 waiters queued" groups as "mac_busy"; recent rows keep
		// the detail.
		reason, _, _ := strings.Cut(d.Reason, ":")
		report.Decisions.ByReason[reason]++
		if d.Placement == "local" {
			report.Decisions.Local++
		} else {
			report.Decisions.Remote++
		}
	}
	report.Decisions.Recent = lastN(decisions, placementViewRecent)
	report.RemoteRuns.Total = len(runs)
	var durations []int64
	for _, r := range runs {
		if r.Failed {
			report.RemoteRuns.Failed++
		}
		durations = append(durations, r.DurationMS)
	}
	report.RemoteRuns.P50MS, report.RemoteRuns.P90MS = percentileMS(durations, 50), percentileMS(durations, 90)
	report.RemoteRuns.Recent = lastN(runs, placementViewRecent)

	report.LastStall = lastStall
	// An episode is open only while its waiter is still queued: the monitor
	// may have stopped before it could record the clear.
	if lastStall != nil && !stallCleared {
		for _, waiter := range status.Waiters {
			if waiter.OperationID == lastStall.OperationID {
				open := *lastStall
				open.DurationMS = now.Sub(open.Since).Milliseconds()
				report.ActiveStall = &open
				break
			}
		}
	}

	report.ParkedStalls = detectParkedLeaseStalls(status, now)
	report.Waiters = estimateWaiterETAs(status, classRuntimeP50(dir, now), now)

	if probeRunners && policy.Fleet != "" {
		report.Runners, report.RunnersError = runnerSlots(ctx, policy.Fleet, probe)
	}
	return report
}

func detectParkedLeaseStalls(status WorkStatus, now time.Time) []ParkedLeaseStall {
	var stalls []ParkedLeaseStall
	for _, lease := range status.Leases {
		if !lease.Parked || lease.ParkedAt == nil {
			continue
		}
		parkedDur := now.Sub(*lease.ParkedAt)
		if parkedDur < ParkedLeaseStallThreshold {
			continue
		}
		stalls = append(stalls, ParkedLeaseStall{
			OperationID:      lease.OperationID,
			Class:            lease.Class,
			PID:              lease.PID,
			Weight:           lease.Weight,
			ParkedAt:         lease.ParkedAt.UTC(),
			ParkedDurationMS: parkedDur.Milliseconds(),
			AgeMS:            lease.AgeMS,
		})
	}
	sort.SliceStable(stalls, func(i, j int) bool {
		return stalls[i].ParkedDurationMS > stalls[j].ParkedDurationMS
	})
	return stalls
}

// classRuntimeP50 returns the 7-day p50 runtime of completed work by class.
func classRuntimeP50(dir string, now time.Time) map[WorkClass]int64 {
	events, err := NewWorkEventStore(dir).completedRowsSince(now.Add(-placementHistoryWindow))
	if err != nil {
		return nil
	}
	byClass := map[WorkClass][]int64{}
	for _, event := range events {
		if event.Event == WorkEventCompleted && event.RuntimeMillis > 0 {
			byClass[event.Class] = append(byClass[event.Class], event.RuntimeMillis)
		}
	}
	p50 := map[WorkClass]int64{}
	for class, values := range byClass {
		if len(values) >= placementMinSamples {
			p50[class] = percentileMS(values, 50)
		}
	}
	return p50
}

// estimateWaiterETAs simulates strict FIFO admission: each waiter starts when
// leases and earlier waiters, finishing at their class p50, free enough
// capacity. An unmeasured class makes every later ETA unknown.
func estimateWaiterETAs(status WorkStatus, p50 map[WorkClass]int64, now time.Time) []WaiterETA {
	waiters := slices.Clone(status.Waiters)
	sort.SliceStable(waiters, func(i, j int) bool { return waiters[i].Position < waiters[j].Position })
	capacity := status.Used + status.Available
	type running struct {
		finish time.Time
		weight int
	}
	var active []running
	used := 0
	unknown := ""
	for _, lease := range status.Leases {
		if lease.Parked {
			continue
		}
		used += lease.Weight
		runtime, ok := p50[lease.Class]
		if !ok {
			unknown = "lease class " + string(lease.Class) + " unmeasured"
			continue
		}
		active = append(active, running{finish: maxTime(lease.StartedAt.Add(time.Duration(runtime)*time.Millisecond), now), weight: lease.Weight})
	}
	clock := now
	etas := make([]WaiterETA, 0, len(waiters))
	for _, waiter := range waiters {
		eta := WaiterETA{OperationID: waiter.OperationID, Class: waiter.Class, Weight: waiter.Weight, Position: waiter.Position, WaitMS: waiter.WaitMS, ETAMS: -1}
		switch {
		case waiter.PressureParked:
			eta.Basis = "parked by host pressure"
		case unknown != "":
			eta.Basis = unknown
		case waiter.Weight > capacity:
			eta.Basis = "weight above current capacity"
			unknown = "blocked by an earlier waiter"
		default:
			sort.Slice(active, func(i, j int) bool { return active[i].finish.Before(active[j].finish) })
			for used+waiter.Weight > capacity && len(active) > 0 {
				clock = maxTime(clock, active[0].finish)
				used -= active[0].weight
				active = active[1:]
			}
			if used+waiter.Weight > capacity {
				eta.Basis = unknown
				if eta.Basis == "" {
					eta.Basis = "capacity held by unmeasured leases"
				}
				unknown = eta.Basis
				break
			}
			eta.ETAMS, eta.Basis = clock.Sub(now).Milliseconds(), "fifo_p50"
			used += waiter.Weight
			if runtime, ok := p50[waiter.Class]; ok {
				active = append(active, running{finish: clock.Add(time.Duration(runtime) * time.Millisecond), weight: waiter.Weight})
			} else {
				unknown = "earlier waiter class " + string(waiter.Class) + " unmeasured"
			}
		}
		etas = append(etas, eta)
	}
	return etas
}

func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

// runnerSlots probes the active hosts of the fleet inventory.
func runnerSlots(ctx context.Context, fleetPath string, probe runnerfleet.ProbeFunc) ([]RunnerSlots, string) {
	config, _, err := runnerfleet.Load(fleetPath)
	if err != nil {
		return nil, "fleet inventory: " + boundedText(err.Error(), 120)
	}
	var active runnerfleet.Config
	active.Schema, active.Generation = config.Schema, config.Generation
	for _, host := range config.Hosts {
		if host.State == "active" {
			active.Hosts = append(active.Hosts, host)
		}
	}
	var slots []RunnerSlots
	for _, o := range runnerfleet.Observe(ctx, active, probe) {
		slots = append(slots, RunnerSlots{Name: o.Host.Name, State: o.Host.State, Healthy: o.Healthy,
			HeavySlots: o.Host.HeavySlots, HeavyJobs: o.HeavyJobs, LightSlots: o.Host.LightSlots, LightJobs: o.LightJobs,
			Error: boundedText(o.Error, 120)})
	}
	return slots, ""
}
