package sessionpressure

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// The swap-thrash derate holds new heavy work while the host swaps hard. It
// can only pay for itself when agents drive the storm. These budgets say when
// its measured cost is too high to keep as configured.
const (
	// SwapDerateBudgetMinutesPerDay is the total agent wait the derate may
	// cause per day. It is about the weekly CPU-gate cost, paid in one day.
	SwapDerateBudgetMinutesPerDay = 30.0
	// SwapDerateBudgetMaxWait is the longest wait one operation may pay. A
	// longer wait is the "builds queue for the whole storm" failure.
	SwapDerateBudgetMaxWait = 15 * time.Minute
)

// Closed verdict vocabulary.
const (
	SwapDerateUnexercised  = "unexercised"
	SwapDerateWithinBudget = "within_budget"
	SwapDerateOverBudget   = "over_budget"
)

// WorkSwapDerateCost is the measured agent cost of the swap-thrash derate.
// Wait is the admission plus pressure wait of operations the derate deferred.
// It can include CPU or memory wait in the same operation, so it is an upper
// bound.
type WorkSwapDerateCost struct {
	Verdict             string              `json:"verdict"`
	DeferredOperations  int                 `json:"deferred_operations"`
	ExternalBypasses    int                 `json:"external_bypasses"`
	Wait                WorkDurationStats   `json:"wait"`
	WaitTotalMS         int64               `json:"wait_total_ms"`
	WaitMinutesPerDay   float64             `json:"wait_minutes_per_day"`
	BudgetMinutesPerDay float64             `json:"budget_minutes_per_day"`
	BudgetMaxWaitMS     int64               `json:"budget_max_wait_ms"`
	Episodes            *SwapThrashEpisodes `json:"episodes,omitempty"`
	Advice              string              `json:"advice,omitempty"`
}

// SwapThrashEpisodes summarizes resident latch history in the window.
type SwapThrashEpisodes struct {
	Count            int     `json:"count"`
	Open             bool    `json:"open"`
	ExternalEpisodes int     `json:"external_episodes"`
	LatchedSeconds   float64 `json:"latched_seconds"`
	ExternalSeconds  float64 `json:"external_seconds"`
	LongestSeconds   float64 `json:"longest_seconds"`
	// Interrupted counts episodes that a resident restart ended without a
	// release event. Their length runs to the restart.
	Interrupted int `json:"interrupted,omitempty"`
}

// summarizeSwapDerate makes one pass over work events. Events carry
// cumulative wait fields, so the maximum per operation is its total.
func summarizeSwapDerate(events []WorkEvent, since, generatedAt time.Time) WorkSwapDerateCost {
	type swapOperation struct {
		deferred, external bool
		pressureMS         int64
		admissionMS        int64
	}
	operations := map[string]*swapOperation{}
	for _, event := range events {
		operation := operations[event.OperationID]
		if operation == nil {
			operation = &swapOperation{}
			operations[event.OperationID] = operation
		}
		if event.PressureDimension == "swap" || event.AdmissionDecision == SwapThrashDeferredDecision {
			operation.deferred = true
		}
		if event.AdmissionDecision == SwapThrashExternalDecision {
			operation.external = true
		}
		operation.pressureMS = max(operation.pressureMS, event.PressureWaitMilliseconds)
		operation.admissionMS = max(operation.admissionMS, event.AdmissionWaitMilliseconds)
	}
	cost := SwapDerateCostBudget()
	waits := make([]int64, 0)
	for _, operation := range operations {
		if operation.external && !operation.deferred {
			cost.ExternalBypasses++
		}
		if !operation.deferred {
			continue
		}
		// Admission wait is the pre-lease pressure gate, where the derate
		// usually holds work; pressure wait is the post-lease reservation.
		// Both are derate wait for an operation it deferred.
		wait := max(int64(0), operation.pressureMS) + max(int64(0), operation.admissionMS)
		cost.DeferredOperations++
		waits = append(waits, wait)
		cost.WaitTotalMS += wait
	}
	cost.Wait = durationStats(waits)
	cost.WaitMinutesPerDay = swapDerateMinutesPerDay(cost.WaitTotalMS, since, generatedAt, events)
	cost.judge()
	return cost
}

// SwapDerateCostBudget returns an empty cost with the budgets filled.
func SwapDerateCostBudget() WorkSwapDerateCost {
	return WorkSwapDerateCost{
		Verdict: SwapDerateUnexercised, BudgetMinutesPerDay: SwapDerateBudgetMinutesPerDay,
		BudgetMaxWaitMS: SwapDerateBudgetMaxWait.Milliseconds(),
	}
}

// A window shorter than a day reports its plain total: projecting one wait
// in a two-hour window to a daily rate would overstate it twelve times.
func swapDerateMinutesPerDay(totalMS int64, since, generatedAt time.Time, events []WorkEvent) float64 {
	start := since
	if start.IsZero() && len(events) > 0 {
		start = events[0].Timestamp
	}
	days := 1.0
	if !start.IsZero() && generatedAt.After(start) {
		days = max(1, generatedAt.Sub(start).Hours()/24)
	}
	return float64(totalMS) / float64(time.Minute/time.Millisecond) / days
}

func (cost *WorkSwapDerateCost) judge() {
	cost.Advice = ""
	if cost.DeferredOperations == 0 {
		cost.Verdict = SwapDerateUnexercised
		return
	}
	reasons := make([]string, 0, 2)
	if cost.WaitMinutesPerDay > cost.BudgetMinutesPerDay {
		reasons = append(reasons, fmt.Sprintf("%.0f min/day of derate wait > %.0f budget", cost.WaitMinutesPerDay, cost.BudgetMinutesPerDay))
	}
	if cost.Wait.MaximumMS > cost.BudgetMaxWaitMS {
		reasons = append(reasons, fmt.Sprintf("one operation waited %s > %s", time.Duration(cost.Wait.MaximumMS)*time.Millisecond, time.Duration(cost.BudgetMaxWaitMS)*time.Millisecond))
	}
	if len(reasons) == 0 {
		cost.Verdict = SwapDerateWithinBudget
		return
	}
	cost.Verdict = SwapDerateOverBudget
	cost.Advice = strings.Join(reasons, "; ") + ". Loosen in policy.json work_limits: lower swap_thrash_external_release_seconds (min 60) to lift the derate sooner in outside storms, raise swap_thrash_agent_share_percent, or set swap_thrash_capacity to capacity to turn the derate off"
}

// AttachEpisodes adds resident latch history. Episodes do not change the
// verdict: the verdict is about agent wait, and a long storm with no wait is
// the derate working as intended.
func (cost *WorkSwapDerateCost) AttachEpisodes(episodes SwapThrashEpisodes) {
	cost.Episodes = &episodes
}

// swapThrashHistoryEvent is the narrow decode of telemetry rows the episode
// summary needs. Other rows are skipped before JSON decoding.
type swapThrashHistoryEvent struct {
	Timestamp  time.Time             `json:"timestamp"`
	Event      string                `json:"event"`
	SwapThrash *SwapThrashTransition `json:"swap_thrash,omitempty"`
}

var swapThrashHistoryMarkers = [][]byte{
	[]byte(`"event":"` + SwapThrashTransitionEvent + `"`),
	[]byte(`"event":"resident_started"`),
	[]byte(`"event":"resident_stopped"`),
}

// ReadSwapThrashEpisodes summarizes latch transitions since the cutoff. It
// reads only day files that can hold rows in the window and decodes only
// rows that carry a marker, so the cost stays small on a 14-day history.
func ReadSwapThrashEpisodes(dir string, since, now time.Time) (SwapThrashEpisodes, error) {
	matches, err := filepath.Glob(filepath.Join(dir, "snapshots-*.jsonl"))
	if err != nil {
		return SwapThrashEpisodes{}, err
	}
	sort.Strings(matches)
	// Day files are named by local date; keep one day of slack for zones.
	cutoff := ""
	if !since.IsZero() {
		cutoff = "snapshots-" + since.Local().Add(-24*time.Hour).Format("20060102") + ".jsonl"
	}
	rows := make([]swapThrashHistoryEvent, 0)
	for _, path := range matches {
		if cutoff != "" && filepath.Base(path) < cutoff {
			continue
		}
		file, err := os.Open(path)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return SwapThrashEpisodes{}, err
		}
		scanner := bufio.NewScanner(file)
		scanner.Buffer(make([]byte, 4096), 2<<20)
		for scanner.Scan() {
			line := scanner.Bytes()
			if !hasSwapThrashHistoryMarker(line) {
				continue
			}
			var row swapThrashHistoryEvent
			if json.Unmarshal(line, &row) != nil {
				continue
			}
			if !since.IsZero() && row.Timestamp.Before(since) {
				continue
			}
			rows = append(rows, row)
		}
		scanErr := scanner.Err()
		_ = file.Close()
		if scanErr != nil {
			return SwapThrashEpisodes{}, scanErr
		}
	}
	return summarizeSwapThrashEpisodes(rows, now), nil
}

func hasSwapThrashHistoryMarker(line []byte) bool {
	for _, marker := range swapThrashHistoryMarkers {
		if bytes.Contains(line, marker) {
			return true
		}
	}
	return false
}

func summarizeSwapThrashEpisodes(rows []swapThrashHistoryEvent, now time.Time) SwapThrashEpisodes {
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].Timestamp.Before(rows[j].Timestamp) })
	var summary SwapThrashEpisodes
	// Per-episode state. external sums closed external segments and
	// externalSince is the open one. A resident restart suspends the episode:
	// a resumed entry with the same Since continues it, anything else ends it
	// at the restart.
	open, proven, suspended := false, false, false
	var start, externalSince, suspendedAt time.Time
	external := 0.0
	externalUntil := func(end time.Time) float64 {
		if externalSince.IsZero() {
			return external
		}
		return external + max(0, end.Sub(externalSince).Seconds())
	}
	closeEpisode := func(end time.Time, externalSeconds float64) {
		length := max(0, end.Sub(start).Seconds())
		summary.LatchedSeconds += length
		summary.ExternalSeconds += min(length, max(0, externalSeconds))
		summary.LongestSeconds = max(summary.LongestSeconds, length)
		if proven {
			summary.ExternalEpisodes++
		}
		open, proven, suspended, externalSince, external = false, false, false, time.Time{}, 0
	}
	endSuspended := func() {
		if open && suspended {
			summary.Interrupted++
			closeEpisode(suspendedAt, externalUntil(suspendedAt))
		}
	}
	continues := func(transition *SwapThrashTransition) bool {
		return open && suspended && !transition.Since.IsZero() && transition.Since.Sub(start).Abs() <= time.Second
	}
	for _, row := range rows {
		if row.Event == "resident_started" || row.Event == "resident_stopped" {
			// The latch lives in resident memory; a restart suspends it.
			if open && !suspended {
				suspended, suspendedAt = true, row.Timestamp
			}
			continue
		}
		transition := row.SwapThrash
		if transition == nil {
			continue
		}
		switch transition.State {
		case SwapThrashEntered:
			if transition.Resumed && continues(transition) {
				suspended = false
				if transition.Cause == SwapThrashCauseExternal && externalSince.IsZero() {
					proven, externalSince = true, row.Timestamp
				}
				continue
			}
			endSuspended()
			if open {
				// Two entries without a release: the first ended unseen.
				summary.Interrupted++
				closeEpisode(row.Timestamp, externalUntil(row.Timestamp))
			}
			summary.Count++
			open, proven, start, externalSince, external = true, false, row.Timestamp, time.Time{}, 0
			if transition.Resumed && !transition.Since.IsZero() && transition.Since.Before(row.Timestamp) {
				// Continues an episode that began before the window.
				start = transition.Since
			}
			if transition.Cause == SwapThrashCauseExternal {
				proven, externalSince = true, row.Timestamp
			}
		case SwapThrashDerateReleased:
			if open && !suspended && externalSince.IsZero() {
				proven, externalSince = true, row.Timestamp
			}
		case SwapThrashDerateResumed:
			if open && !suspended && !externalSince.IsZero() {
				external, externalSince = externalUntil(row.Timestamp), time.Time{}
			}
		case SwapThrashReleased:
			if continues(transition) {
				suspended = false
			} else {
				endSuspended()
			}
			if !open {
				// The episode began before the window: count what it reported.
				summary.Count++
				open, start = true, row.Timestamp.Add(-time.Duration(transition.EpisodeSeconds*float64(time.Second)))
			}
			if transition.Cause == SwapThrashCauseExternal {
				proven = true
			}
			// The resident's own sum is authoritative when present.
			externalSeconds := externalUntil(row.Timestamp)
			if transition.ExternalSeconds > 0 {
				externalSeconds = transition.ExternalSeconds
			}
			closeEpisode(row.Timestamp, externalSeconds)
		}
	}
	endSuspended()
	if open {
		summary.Open = true
		length := max(0, now.Sub(start).Seconds())
		summary.LatchedSeconds += length
		summary.ExternalSeconds += min(length, externalUntil(now))
		summary.LongestSeconds = max(summary.LongestSeconds, length)
		if proven {
			summary.ExternalEpisodes++
		}
	}
	return summary
}
