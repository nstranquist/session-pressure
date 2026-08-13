package sessionpressure

import (
	"time"
)

// WorkCalibrationReport is the privacy-safe agent/operator view of packing.
// Counts and closed enums only — never argv, workdirs, or prompts.
type WorkCalibrationReport struct {
	SchemaVersion              int       `json:"schema_version"`
	Since                      time.Time `json:"since"`
	GeneratedAt                time.Time `json:"generated_at"`
	OperationCount             int       `json:"operation_count"`
	EventCount                 int       `json:"event_count"`
	ExpressTestOps             int       `json:"express_test_operations"`
	FullTestOps                int       `json:"full_test_operations"`
	ExpressBuildOps            int       `json:"express_build_operations"`
	FullBuildOps               int       `json:"full_build_operations"`
	ExpressTestShare           float64   `json:"express_test_share"`
	ExpressBuildShare          float64   `json:"express_build_share"`
	ReuseHits                  int       `json:"reuse_hits"`
	CacheHits                  int       `json:"cache_hits"`
	CacheMisses                int       `json:"cache_misses"`
	WrapperInterruptOperations int       `json:"wrapper_interrupt_operations"`
	// SuggestedPolicyProfile is advisory only (M2); never auto-applied.
	SuggestedPolicyProfile       string `json:"suggested_policy_profile,omitempty"`
	SuggestedPolicyProfileReason string `json:"suggested_policy_profile_reason,omitempty"`
	// InterruptProjection is the sparse counts-only forensics envelope (M2).
	InterruptProjection *WrapperInterruptProjection `json:"interrupt_projection,omitempty"`
	ByClass             []WorkClassStats            `json:"by_class"`
	Outcomes            []WorkCalibrationOutcome    `json:"outcomes"`
	ReviewSignals       WorkReviewSignals           `json:"review_signals"`
	ThresholdRetuneHint string                      `json:"threshold_retune_hint,omitempty"`
}

// WorkCalibrationOutcome is a closed-enum counter (privacy-safe).
type WorkCalibrationOutcome struct {
	Outcome string `json:"outcome"`
	Count   int    `json:"count"`
}

// BuildWorkCalibrationReport aggregates SummarizeWorkEvents plus express ratios
// and closed outcome counters. One source of truth for work report/stats.
func BuildWorkCalibrationReport(events []WorkEvent, since, generatedAt time.Time) WorkCalibrationReport {
	if generatedAt.IsZero() {
		generatedAt = time.Now().UTC()
	}
	stats := SummarizeWorkEvents(events, since, generatedAt)
	report := WorkCalibrationReport{
		SchemaVersion:  1,
		Since:          since,
		GeneratedAt:    generatedAt,
		OperationCount: stats.OperationCount,
		EventCount:     stats.EventCount,
		ByClass:        stats.ByClass,
		ReviewSignals:  stats.ReviewSignals,
		CacheHits:      stats.ReviewSignals.CacheHits,
		CacheMisses:    stats.ReviewSignals.CacheMisses,
		// ReuseHits is counted below from terminal replay outcomes only. A reuse
		// event carries both ReuseStatus "hit" and a replay outcome, so seeding
		// this from CacheHits would count every hit twice.
		WrapperInterruptOperations: stats.ReviewSignals.WrapperInterruptOperations,
	}
	classOps := map[WorkClass]int{}
	for _, row := range stats.ByClass {
		classOps[row.Class] = row.Operations
		switch row.Class {
		case WorkClassExpressTest:
			report.ExpressTestOps = row.Operations
		case WorkClassTest:
			report.FullTestOps = row.Operations
		case WorkClassExpressBuild:
			report.ExpressBuildOps = row.Operations
		case WorkClassBuild:
			report.FullBuildOps = row.Operations
		}
	}
	report.ExpressTestShare = share(report.ExpressTestOps, report.ExpressTestOps+report.FullTestOps)
	report.ExpressBuildShare = share(report.ExpressBuildOps, report.ExpressBuildOps+report.FullBuildOps)

	outcomeCounts := map[string]int{}
	for _, event := range events {
		if event.Outcome == "" {
			continue
		}
		// Closed short outcomes only; drop anything that looks path-like.
		if len(event.Outcome) > 64 || containsPathy(event.Outcome) {
			continue
		}
		outcomeCounts[event.Outcome]++
		if event.Outcome == "express_reuse_hit" || event.Outcome == "successful_receipt_reused" {
			report.ReuseHits++
		}
	}
	for outcome, count := range outcomeCounts {
		report.Outcomes = append(report.Outcomes, WorkCalibrationOutcome{Outcome: outcome, Count: count})
	}
	// Stable order for tests.
	sortCalibrationOutcomes(report.Outcomes)

	// Data-driven retune hint: only suggest when samples exist; never invent thresholds.
	totalTest := report.ExpressTestOps + report.FullTestOps
	if totalTest >= 20 && report.ExpressTestShare < 0.2 {
		report.ThresholdRetuneHint = "low express-test share with adequate samples; prefer express-test for package-scoped go test before considering threshold changes"
	} else if stats.OperationCount < 10 {
		report.ThresholdRetuneHint = "insufficient calibration volume; no host threshold retune"
	} else {
		report.ThresholdRetuneHint = "no host threshold retune; packing/express measurement only"
	}

	// M2: sparse interrupt projection (counts only).
	window := generatedAt.Sub(since)
	if window <= 0 {
		window = 24 * time.Hour
	}
	if proj, err := ProjectWrapperInterruptFromSignals(stats.ReviewSignals, window, generatedAt); err == nil {
		report.InterruptProjection = &proj
	}

	// M2: advisory multi-agent-soft suggestion (never applies policy).
	// Queue pressure: long waits or reservation deferrals as multi-agent signal.
	queueSignal := stats.ReviewSignals.LongWaitOperations > 0 ||
		stats.ReviewSignals.ReservationDeferrals > 0 ||
		stats.ReviewSignals.WrapperInterruptOperations > 0
	suggest := SuggestPolicyProfile(SuggestPolicyProfileInput{
		OperationCount:      stats.OperationCount,
		CancelledOperations: stats.ReviewSignals.CancelledOperations,
		QueuePressureSignal: queueSignal,
	})
	report.SuggestedPolicyProfile = suggest.Profile
	report.SuggestedPolicyProfileReason = suggest.Reason

	_ = classOps
	return report
}

func share(part, total int) float64 {
	if total <= 0 {
		return 0
	}
	return float64(part) / float64(total)
}

func containsPathy(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] == '/' || s[i] == '\\' {
			return true
		}
	}
	return false
}

func sortCalibrationOutcomes(rows []WorkCalibrationOutcome) {
	// insertion sort — small N
	for i := 1; i < len(rows); i++ {
		j := i
		for j > 0 && rows[j-1].Outcome > rows[j].Outcome {
			rows[j-1], rows[j] = rows[j], rows[j-1]
			j--
		}
	}
}
