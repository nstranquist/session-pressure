package sessionpressure

import (
	"fmt"
	"testing"
	"time"
)

func TestWorkloadReportPopulations(t *testing.T) {
	var events []WorkEvent
	now := time.Now()
	for i := 0; i < 20; i++ {
		events = append(events, WorkEvent{OperationID: fmt.Sprint(i), Timestamp: now, Event: WorkEventCompleted, Workload: "cli", Cohort: "warm", Class: WorkClassTest, RuntimeMillis: int64(i+1) * 10000, QueueWaitMilliseconds: 3, PressureWaitMilliseconds: 5})
	}
	events = append(events, events[0]) // Duplicate audit observations do not inflate samples.
	events = append(events, WorkEvent{OperationID: "cold", Event: WorkEventFailed, Workload: "cli", Cohort: "cold", RuntimeMillis: 300000}, WorkEvent{OperationID: "old", Event: WorkEventFailed, RuntimeMillis: 70000}, WorkEvent{OperationID: "reuse", Event: WorkEventReused, Workload: "cli", Cohort: "warm", Class: WorkClassTest}, WorkEvent{OperationID: "queued-cancel", Event: WorkEventCancelled, Workload: "cli", Cohort: "warm", Class: WorkClassTest, QueueWaitMilliseconds: 100})
	r := BuildWorkRuntimeReport(events)
	if r.LongRunCount != 17 {
		t.Fatalf("review count %d", r.LongRunCount)
	}
	if r.LongRuns[0].OperationID != "cold" {
		t.Fatal("not ranked longest first")
	}
	g := r.Workloads[0]
	if g.Samples != 20 || g.Reused != 1 || g.Cancelled != 1 || g.MedianMS != 105000 || g.P95MS == nil || *g.P95MS != 190000 || g.BudgetBreaches != 14 || g.QueueTotalMS != 160 || g.PressureTotalMS != 100 {
		t.Fatalf("warm population: %+v", g)
	}
	for _, g := range r.Workloads[1:] {
		if g.P95MS != nil || g.BudgetMS != 0 || g.Failed != 1 {
			t.Fatalf("mixed/unknown cohort %+v", g)
		}
	}
}

func TestWorkloadArguments(t *testing.T) {
	o, err := ParseWorkRunArgs([]string{"--class", "test", "--workload", "catalog", "--cohort", "warm", "--", "go", "test", "./..."})
	if err != nil || o.Workload != "catalog" || o.Cohort != "warm" {
		t.Fatalf("%+v %v", o, err)
	}
	for _, a := range [][]string{{"--workload", "secret argv", "--", "go", "test", "./..."}, {"--cohort", "unclassified", "--", "go", "test", "./..."}, {"--workload"}} {
		if _, err := ParseWorkRunArgs(a); err == nil {
			t.Fatalf("accepted %v", a)
		}
	}
}
