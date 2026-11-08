package sessionpressure

import (
	"fmt"
	"strings"
	"time"
)

// WorkCalibrationReport is the privacy-safe agent/operator view of packing.
// Counts and closed enums only — never argv, workdirs, or prompts.
type WorkCalibrationReport struct {
	HostCohorts                map[string]int           `json:"host_cohorts"`
	LegacyHostEvents           int                      `json:"legacy_host_events"`
	CacheRefusals              []WorkCalibrationOutcome `json:"cache_refusals"`
	SchemaVersion              int                      `json:"schema_version"`
	Since                      time.Time                `json:"since"`
	GeneratedAt                time.Time                `json:"generated_at"`
	OperationCount             int                      `json:"operation_count"`
	EventCount                 int                      `json:"event_count"`
	ExpressTestOps             int                      `json:"express_test_operations"`
	FullTestOps                int                      `json:"full_test_operations"`
	ExpressBuildOps            int                      `json:"express_build_operations"`
	FullBuildOps               int                      `json:"full_build_operations"`
	ExpressTestShare           float64                  `json:"express_test_share"`
	ExpressBuildShare          float64                  `json:"express_build_share"`
	ReuseHits                  int                      `json:"reuse_hits"`
	CacheHits                  int                      `json:"cache_hits"`
	CacheMisses                int                      `json:"cache_misses"`
	WrapperInterruptOperations int                      `json:"wrapper_interrupt_operations"`
	// SuggestedPolicyProfile is advisory only (M2); never auto-applied.
	SuggestedPolicyProfile       string `json:"suggested_policy_profile,omitempty"`
	SuggestedPolicyProfileReason string `json:"suggested_policy_profile_reason,omitempty"`
	// SuggestedPolicyProfileFlags are extra `policy profile apply` flags, such
	// as --multi-agent for an enforcing host that keeps its profile.
	SuggestedPolicyProfileFlags []string `json:"suggested_policy_profile_flags,omitempty"`
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

	report.HostCohorts = map[string]int{}
	refusals := map[string]int{}
	for _, event := range events {
		if event.Timestamp.Before(since) || event.Timestamp.After(generatedAt) {
			continue
		}
		if event.HostID == "" {
			report.LegacyHostEvents++
		} else {
			report.HostCohorts[event.HostID+":"+event.HostOS+":"+event.HostArch+fmt.Sprintf(":%d", event.LogicalCPUs)]++
		}
		if event.ReuseRefusalReason != "" {
			refusals[string(event.ReuseRefusalReason)]++
		}
	}
	for reason, count := range refusals {
		report.CacheRefusals = append(report.CacheRefusals, WorkCalibrationOutcome{Outcome: reason, Count: count})
	}
	sortCalibrationOutcomes(report.CacheRefusals)
	outcomeCounts := map[string]int{}
	for _, event := range events {
		if event.Timestamp.Before(since) || event.Timestamp.After(generatedAt) || event.Outcome == "" {
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
	if idle := stats.ReviewSignals.QueueWaitsIdleHost; idle >= queueIdleHostHintMinimum {
		report.ThresholdRetuneHint = fmt.Sprintf("%d of %d measured queue waits (>=30s) ran while the host was under %.0f%% CPU; leased work under-uses its reserved weight, so review class weights or work capacity before CPU thresholds", idle, stats.ReviewSignals.QueueWaitsWithCPUEvidence, queueIdleHostCPUPercent)
		report.ThresholdRetuneHint += underusedLeaseClasses(stats.LeaseCPU)
	} else if totalTest >= 20 && report.ExpressTestShare < 0.2 {
		report.ThresholdRetuneHint = "low express-test share with adequate samples; prefer express-test for package-scoped go test before considering threshold changes"
	} else if stats.OperationCount < 10 {
		report.ThresholdRetuneHint = "insufficient calibration volume; no host threshold retune"
	} else {
		report.ThresholdRetuneHint = "no host threshold retune; packing/express measurement only"
	}

	if len(report.HostCohorts) > 1 {
		report.ThresholdRetuneHint = "mixed host cohorts; compare each host separately before changing reservations or thresholds"
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

// queueIdleHostHintMinimum keeps one stray wait from steering a retune.
const queueIdleHostHintMinimum = 3

// Lease CPU evidence names a class in the retune hint only with enough runs
// and when its commands kept less than half of their reserved cores busy.
const (
	leaseCPUHintMinimumRuns = 10
	leaseCPUHintUtilization = 0.5
)

// underusedLeaseClasses names the classes whose measured command CPU stays
// well under the cores their weight reserves, the direct evidence for a
// weight retune.
func underusedLeaseClasses(leaseCPU WorkLeaseCPU) string {
	var named []string
	for _, class := range leaseCPU.ByClass {
		if class.Operations >= leaseCPUHintMinimumRuns && class.ReservedCores > 0 && class.Utilization < leaseCPUHintUtilization {
			named = append(named, fmt.Sprintf("%s used %.0f%% of %.1f reserved cores over %d runs", class.Class, class.Utilization*100, class.ReservedCores, class.Operations))
		}
	}
	if len(named) == 0 {
		return ""
	}
	return "; measured: " + strings.Join(named, ", ")
}

// SuppressIfAlreadyApplied clears the advisory suggestion when the live policy
// already has multi-agent-soft effects. Suggestion remains read-only.
func (r *WorkCalibrationReport) SuppressIfAlreadyApplied(policy Policy) {
	if r == nil || r.SuggestedPolicyProfile == "" {
		return
	}
	if MatchesMultiAgentSoft(policy) || HasMultiAgentLaunchAdmission(policy) {
		r.SuggestedPolicyProfile = ""
		r.SuggestedPolicyProfileReason = ""
		r.SuggestedPolicyProfileFlags = nil
		return
	}
	if policy.Enabled && policy.EnforceAdmission && policy.Profile != "" {
		// Never advise an enforcing host to drop to observe-only: that would
		// trade away the CPU latch, memory floors, and swap-thrash derate for
		// earlier warnings. Keep its profile and add only the warnings.
		r.SuggestedPolicyProfile = policy.Profile
		r.SuggestedPolicyProfileFlags = []string{"--multi-agent"}
	}
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
