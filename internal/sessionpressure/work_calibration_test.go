package sessionpressure

import (
	"fmt"
	"testing"
	"time"
)

func TestBuildWorkCalibrationReportExpressShareAndPrivacy(t *testing.T) {
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	since := now.Add(-24 * time.Hour)
	events := []WorkEvent{
		{OperationID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Event: WorkEventCompleted, Class: WorkClassExpressTest, Outcome: "express_reuse_hit", Timestamp: now},
		{OperationID: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", Event: WorkEventCompleted, Class: WorkClassExpressTest, Outcome: "successful", Timestamp: now},
		{OperationID: "cccccccccccccccccccccccccccccccc", Event: WorkEventCompleted, Class: WorkClassTest, Outcome: "successful", Timestamp: now},
		{OperationID: "dddddddddddddddddddddddddddddddd", Event: WorkEventCompleted, Class: WorkClassBuild, Outcome: "successful", Timestamp: now},
		// Pathy outcome must be dropped from counters.
		{OperationID: "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee", Event: WorkEventCompleted, Class: WorkClassExpressBuild, Outcome: "/tmp/secret-cmd", Timestamp: now},
	}
	report := BuildWorkCalibrationReport(events, since, now)
	if report.SchemaVersion != 1 {
		t.Fatalf("schema=%d", report.SchemaVersion)
	}
	if report.ExpressTestOps != 2 || report.FullTestOps != 1 {
		t.Fatalf("test ops express=%d full=%d", report.ExpressTestOps, report.FullTestOps)
	}
	if report.ExpressTestShare < 0.66 || report.ExpressTestShare > 0.67 {
		t.Fatalf("express test share=%v", report.ExpressTestShare)
	}
	for _, o := range report.Outcomes {
		if containsPathy(o.Outcome) {
			t.Fatalf("pathy outcome leaked: %+v", o)
		}
	}
	if report.ThresholdRetuneHint == "" {
		t.Fatal("expected threshold retune hint")
	}
	if report.InterruptProjection == nil || report.InterruptProjection.WrapperInterruptOperations < 0 {
		t.Fatalf("expected interrupt projection: %+v", report.InterruptProjection)
	}
}

// A replay event carries both ReuseStatus "hit" and a terminal replay outcome.
// Counting each path independently reported two hits for one replay.
func TestBuildWorkCalibrationReportCountsEachReuseHitOnce(t *testing.T) {
	now := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)
	since := now.Add(-24 * time.Hour)
	events := []WorkEvent{
		{OperationID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Event: WorkEventReused, Class: WorkClassExpressTest, Outcome: "successful_receipt_reused", ReuseStatus: "hit", Timestamp: now},
		{OperationID: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", Event: WorkEventCompleted, Class: WorkClassExpressTest, Outcome: "successful", ReuseStatus: "miss", Timestamp: now},
	}
	report := BuildWorkCalibrationReport(events, since, now)
	if report.ReuseHits != 1 {
		t.Fatalf("reuse_hits=%d, want 1 (one replay counted once)", report.ReuseHits)
	}
	if report.CacheHits != 1 || report.CacheMisses != 1 {
		t.Fatalf("cache_hits=%d cache_misses=%d, want 1/1", report.CacheHits, report.CacheMisses)
	}
}

func TestBuildWorkCalibrationReportSuggestsMultiAgentSoft(t *testing.T) {
	now := time.Date(2026, 7, 20, 18, 0, 0, 0, time.UTC)
	since := now.Add(-24 * time.Hour)
	events := make([]WorkEvent, 0, 25)
	for i := 0; i < 20; i++ {
		id := fmt.Sprintf("%032x", i+1)
		ev := WorkEvent{
			OperationID: id, Event: WorkEventCompleted, Class: WorkClassTest,
			Outcome: "successful", Timestamp: now,
		}
		if i < 4 {
			ev.Event = WorkEventCancelled
			ev.Outcome = "user_abort"
		}
		events = append(events, ev)
	}
	// One wrapper interrupt cancel adds queue-ish pressure path via interrupt signal.
	events = append(events, WorkEvent{
		OperationID: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		Event:       WorkEventCancelled, Class: WorkClassBuild, Outcome: "wrapper_interrupt", Timestamp: now,
	})
	report := BuildWorkCalibrationReport(events, since, now)
	if report.SuggestedPolicyProfile != PolicyProfileMultiAgentSoft {
		// 5 cancels / 21 ops ≈ 0.23 >= 0.15 → high_cancel_rate
		t.Fatalf("expected multi-agent-soft suggestion, got profile=%q reason=%q ops=%d cancels via signals=%+v",
			report.SuggestedPolicyProfile, report.SuggestedPolicyProfileReason, report.OperationCount, report.ReviewSignals)
	}
	if report.SuggestedPolicyProfileReason == "" {
		t.Fatal("expected closed reason code")
	}
}
