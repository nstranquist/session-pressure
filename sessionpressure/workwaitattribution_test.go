package sessionpressure

import (
	"strings"
	"testing"
	"time"
)

func TestWaitAttributionSplitsWaitByCauseAndFindsBursts(t *testing.T) {
	base := time.Date(2026, 11, 2, 23, 55, 0, 0, time.UTC)
	event := func(op string, kind WorkEventType, offset time.Duration, mutate func(*WorkEvent)) WorkEvent {
		value := WorkEvent{SchemaVersion: WorkEventSchemaVersion, OperationID: op, Event: kind, Class: WorkClassBuild, Weight: 5, Timestamp: base.Add(offset)}
		if mutate != nil {
			mutate(&value)
		}
		return value
	}
	events := []WorkEvent{
		// Five minutes behind weighted capacity.
		event("capacity", WorkEventQueued, 0, nil),
		event("capacity", WorkEventAcquired, 5*time.Minute, func(value *WorkEvent) {
			value.Blocker, value.QueueWaitMilliseconds, value.WaitMilliseconds = WorkBlockerCapacity, 300_000, 300_000
		}),
		// Two minutes held by the swap derate before the lease, then gave up.
		event("swap", WorkEventQueued, time.Minute, nil),
		event("swap", WorkEventCancelled, 3*time.Minute, func(value *WorkEvent) {
			value.AdmissionDecision, value.AdmissionWaitMilliseconds, value.WaitMilliseconds = SwapThrashDeferredDecision, 120_000, 120_000
		}),
		// The admission sample alone is not pressure.
		event("sample", WorkEventAcquired, time.Minute, func(value *WorkEvent) {
			value.Blocker, value.AdmissionWaitMilliseconds, value.WaitMilliseconds = WorkBlockerNone, 30, 30
		}),
		// One minute of post-lease CPU pressure.
		event("cpu", WorkEventReacquired, 2*time.Minute, func(value *WorkEvent) {
			value.PressureDimension, value.PressureWaitMilliseconds, value.WaitMilliseconds = "cpu", 60_000, 60_000
		}),
		event("flight", WorkEventCompleted, 2*time.Minute, func(value *WorkEvent) { value.SingleflightWaitMS = 5_000 }),
		event("residual", WorkEventStarted, 2*time.Minute, func(value *WorkEvent) { value.WaitMilliseconds = 10_000 }),
		// A second, separate burst two hours later.
		event("later", WorkEventAcquired, 2*time.Hour, func(value *WorkEvent) {
			value.Blocker, value.QueueWaitMilliseconds, value.WaitMilliseconds = WorkBlockerFairness, 180_000, 180_000
		}),
	}
	stats := SummarizeWorkEvents(events, base.Add(-time.Hour), base.Add(3*time.Hour))
	attribution := stats.WaitAttribution
	want := map[string]int64{
		WaitCauseQueueCapacity: 300_000, WaitCausePressureSwap: 120_000, WaitCauseQueueFairness: 180_000,
		WaitCausePressureCPU: 60_000, WaitCauseSingleflight: 5_000, WaitCauseOther: 10_030,
	}
	got := map[string]int64{}
	share := 0.0
	for _, cause := range attribution.ByCause {
		got[cause.Cause] = cause.WaitMS
		share += cause.Share
	}
	for cause, waitMS := range want {
		if got[cause] != waitMS {
			t.Fatalf("%s=%d want %d (all=%v)", cause, got[cause], waitMS, got)
		}
	}
	if len(got) != len(want) || attribution.TotalMS != 675_030 || attribution.Operations != 7 || share < 0.999 || share > 1.001 {
		t.Fatalf("attribution=%+v", attribution)
	}
	if attribution.Dominant != WaitCauseQueueCapacity || attribution.SwapShare < 0.177 || attribution.SwapShare > 0.178 {
		t.Fatalf("dominant=%s swap share=%v", attribution.Dominant, attribution.SwapShare)
	}
	if len(attribution.LongWaitBursts) != 2 {
		t.Fatalf("bursts=%+v", attribution.LongWaitBursts)
	}
	first, second := attribution.LongWaitBursts[0], attribution.LongWaitBursts[1]
	if first.Operations != 2 || first.MaxWaitMS != 300_000 || first.Dominant != WaitCauseQueueCapacity || !first.Start.Equal(base) || !first.End.Equal(base.Add(5*time.Minute)) {
		t.Fatalf("first burst=%+v", first)
	}
	if second.Operations != 1 || second.Dominant != WaitCauseQueueFairness {
		t.Fatalf("second burst=%+v", second)
	}
	// The derate caused 18% of all wait: the note says do not loosen it.
	cost := stats.SwapDerate
	if cost.AllWaitTotalMS != 675_030 || cost.ShareOfAllWait < 0.17 || cost.ShareOfAllWait > 0.18 {
		t.Fatalf("derate share=%+v", cost)
	}
	if !strings.Contains(cost.Note, "3 long waits") || !strings.Contains(cost.Note, "queue_capacity caused most") || !strings.Contains(cost.Note, "Do not loosen the swap derate") {
		t.Fatalf("note=%q", cost.Note)
	}
}

func TestWaitAttributionStaysQuietWhenTheDerateIsTheCause(t *testing.T) {
	base := time.Date(2026, 11, 3, 1, 0, 0, 0, time.UTC)
	stats := SummarizeWorkEvents([]WorkEvent{
		{SchemaVersion: WorkEventSchemaVersion, OperationID: "a", Event: WorkEventCompleted, Class: WorkClassBuild, Weight: 5, Timestamp: base,
			PressureDimension: "swap", AdmissionDecision: SwapThrashDeferredDecision, PressureWaitMilliseconds: 600_000, WaitMilliseconds: 600_000, RuntimeMillis: 1000},
	}, base.Add(-time.Hour), base.Add(time.Minute))
	if stats.SwapDerate.ShareOfAllWait != 1 || stats.SwapDerate.Note != "" || stats.WaitAttribution.Dominant != WaitCausePressureSwap {
		t.Fatalf("derate-caused wait cost=%+v attribution=%+v", stats.SwapDerate, stats.WaitAttribution)
	}
	if empty := SummarizeWorkEvents(nil, base, base); empty.WaitAttribution.TotalMS != 0 || empty.WaitAttribution.ByCause == nil || empty.WaitAttribution.LongWaitBursts == nil {
		t.Fatalf("empty attribution must keep non-nil lists: %+v", empty.WaitAttribution)
	}
}
