package sessionpressure

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

// WorkloadBudgets are advisory warm-host execution targets. A budget never
// cancels work, changes admission weight, or authorizes verification reuse.
// Keys are registered, non-sensitive identifiers; argv is never a label.
var workloadBudgets = map[string]int64{
	"commitdelivery.full": 180000, "commitdelivery.shard": 90000,
	"precommithook": 180000, "catalog": 120000, "cli": 60000,
	"docgraph-ask": 60000, "browser-mcp-script": 90000,
	"docs.noop": 15000, "docs.incremental": 60000, "docs.full": 180000,
	"candidate-verify-path": 960000,
}

func ValidateWorkload(workload, cohort string) error {
	if workload != "" {
		if _, ok := workloadBudgets[workload]; !ok {
			ids := make([]string, 0, len(workloadBudgets))
			for id := range workloadBudgets {
				ids = append(ids, id)
			}
			sort.Strings(ids)
			return fmt.Errorf("unregistered workload %q; want one of %s", workload, strings.Join(ids, ", "))
		}
	}
	switch cohort {
	case "", "warm", "cold", "cached", "fixture", "live":
	default:
		return fmt.Errorf("unknown workload cohort %q; want warm, cold, cached, fixture, or live", cohort)
	}
	return nil
}

type WorkloadRuntime struct {
	Workload        string    `json:"workload"`
	Cohort          string    `json:"cohort"`
	Class           WorkClass `json:"class"`
	Samples         int       `json:"samples"`
	Completed       int       `json:"completed"`
	Failed          int       `json:"failed"`
	Cancelled       int       `json:"cancelled"`
	Reused          int       `json:"reused"`
	RuntimeTotalMS  int64     `json:"runtime_total_ms"`
	QueueTotalMS    int64     `json:"queue_total_ms"`
	PressureTotalMS int64     `json:"pressure_total_ms"`
	MedianMS        int64     `json:"median_ms"`
	MaximumMS       int64     `json:"maximum_ms"`
	P95MS           *int64    `json:"p95_ms,omitempty"`
	BudgetMS        int64     `json:"budget_ms,omitempty"`
	BudgetBreaches  int       `json:"budget_breaches"`
	ReviewCount     int       `json:"review_count"`
}

type WorkRuntimeReview struct {
	OperationID    string        `json:"operation_id"`
	FinishedAt     time.Time     `json:"finished_at"`
	Workload       string        `json:"workload"`
	Cohort         string        `json:"cohort"`
	Class          WorkClass     `json:"class"`
	Outcome        WorkEventType `json:"outcome"`
	RuntimeMS      int64         `json:"runtime_ms"`
	QueueMS        int64         `json:"queue_ms"`
	PressureMS     int64         `json:"pressure_ms"`
	BudgetMS       int64         `json:"budget_ms,omitempty"`
	BudgetExceeded bool          `json:"budget_exceeded"`
	Severe         bool          `json:"severe,omitempty"`
}

type WorkRuntimeReport struct {
	ReviewThresholdMS int64               `json:"review_threshold_ms"`
	MinimumP95Samples int                 `json:"minimum_p95_samples"`
	Workloads         []WorkloadRuntime   `json:"workloads"`
	LongRuns          []WorkRuntimeReview `json:"long_runs"`
	LongRunCount      int                 `json:"long_run_count"`
	Truncated         bool                `json:"truncated"`
}

// BuildWorkRuntimeReport uses all terminal observations, including failures.
// Old events remain unknown. Reuse is counted separately from actual runtime.
// Cohorts are explicit declarations, never inferred from elapsed time. Only
// warm runs get warm budgets; cold/unknown populations cannot dilute their SLO.
func BuildWorkRuntimeReport(events []WorkEvent) WorkRuntimeReport {
	r := WorkRuntimeReport{ReviewThresholdMS: 60000, MinimumP95Samples: 20, Workloads: []WorkloadRuntime{}, LongRuns: []WorkRuntimeReview{}}
	terminal := map[string]WorkEvent{}
	for _, e := range events {
		switch e.Event {
		case WorkEventCompleted, WorkEventFailed, WorkEventCancelled, WorkEventExpired, WorkEventReused:
		default:
			continue
		}
		if old, ok := terminal[e.OperationID]; !ok || e.Timestamp.After(old.Timestamp) {
			terminal[e.OperationID] = e
		}
	}
	type key struct {
		workload, cohort string
		class            WorkClass
	}
	groups := map[key]*WorkloadRuntime{}
	values := map[key][]int64{}
	for _, e := range terminal {
		w, c := e.Workload, e.Cohort
		if w == "" {
			w = "unknown"
		}
		if c == "" {
			c = "unknown"
		}
		k := key{w, c, e.Class}
		g := groups[k]
		if g == nil {
			g = &WorkloadRuntime{Workload: w, Cohort: c, Class: e.Class}
			if c == "warm" {
				g.BudgetMS = workloadBudgets[w]
			}
			groups[k] = g
		}
		if e.Event == WorkEventReused {
			g.Reused++
			continue
		}
		if e.Event == WorkEventCompleted {
			g.Completed++
		} else if e.Event == WorkEventFailed {
			g.Failed++
		} else {
			g.Cancelled++
		}
		// A cancelled operation can terminate in the queue without executing.
		// Keep the outcome and wait cost but do not add a zero runtime sample.
		g.QueueTotalMS += e.QueueWaitMilliseconds
		g.PressureTotalMS += e.PressureWaitMilliseconds
		if e.RuntimeMillis <= 0 {
			continue
		}
		g.Samples++
		g.RuntimeTotalMS += e.RuntimeMillis
		values[k] = append(values[k], e.RuntimeMillis)
		breach := g.BudgetMS > 0 && e.RuntimeMillis > g.BudgetMS
		if breach {
			g.BudgetBreaches++
		}
		if e.RuntimeMillis >= r.ReviewThresholdMS || breach {
			g.ReviewCount++
			r.LongRuns = append(r.LongRuns, WorkRuntimeReview{OperationID: e.OperationID, FinishedAt: e.Timestamp, Workload: w, Cohort: c, Class: e.Class, Outcome: e.Event, RuntimeMS: e.RuntimeMillis, QueueMS: e.QueueWaitMilliseconds, PressureMS: e.PressureWaitMilliseconds, BudgetMS: g.BudgetMS, BudgetExceeded: breach, Severe: w == "candidate-verify-path" && e.RuntimeMillis > 1200000})
		}
	}
	for k, g := range groups {
		v := values[k]
		sort.Slice(v, func(i, j int) bool { return v[i] < v[j] })
		if len(v) > 0 {
			g.MedianMS = v[len(v)/2]
			if len(v)%2 == 0 {
				g.MedianMS = v[len(v)/2-1] + (v[len(v)/2]-v[len(v)/2-1])/2
			}
			g.MaximumMS = v[len(v)-1]
		}
		if len(v) >= r.MinimumP95Samples {
			p := v[int(math.Ceil(float64(len(v))*.95))-1]
			g.P95MS = &p
		}
		r.Workloads = append(r.Workloads, *g)
	}
	sort.Slice(r.Workloads, func(i, j int) bool {
		a, b := r.Workloads[i], r.Workloads[j]
		if a.RuntimeTotalMS != b.RuntimeTotalMS {
			return a.RuntimeTotalMS > b.RuntimeTotalMS
		}
		if a.Workload != b.Workload {
			return a.Workload < b.Workload
		}
		if a.Cohort != b.Cohort {
			return a.Cohort < b.Cohort
		}
		return a.Class < b.Class
	})
	sort.Slice(r.LongRuns, func(i, j int) bool {
		a, b := r.LongRuns[i], r.LongRuns[j]
		if a.RuntimeMS != b.RuntimeMS {
			return a.RuntimeMS > b.RuntimeMS
		}
		return a.OperationID < b.OperationID
	})
	r.LongRunCount = len(r.LongRuns)
	if len(r.LongRuns) > 1000 {
		r.LongRuns = r.LongRuns[:1000]
		r.Truncated = true
	}
	return r
}
