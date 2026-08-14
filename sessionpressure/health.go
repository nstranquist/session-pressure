package sessionpressure

import (
	"fmt"
	"time"
)

// GuardHealth separates daemon health from protection mode. An observe-only
// monitor can be healthy without being ready for unattended daily-driver use.
type GuardHealth struct {
	MonitorHealthy        bool                 `json:"monitor_healthy"`
	DailyDriverReady      bool                 `json:"daily_driver_ready"`
	OperatorReady         bool                 `json:"operator_ready"`
	ProtectionMode        string               `json:"protection_mode"`
	LatestMonitorFresh    bool                 `json:"latest_monitor_fresh"`
	LatestAgeSeconds      float64              `json:"latest_monitor_age_seconds,omitempty"`
	LatestMaxAgeSeconds   float64              `json:"latest_monitor_max_age_seconds,omitempty"`
	ResidentSamples       int                  `json:"resident_samples,omitempty"`
	ResidentNormalSamples int                  `json:"resident_normal_samples,omitempty"`
	RequiredNormalSamples int                  `json:"required_normal_samples"`
	BudgetHeadroom        *GuardBudgetHeadroom `json:"budget_headroom,omitempty"`
	HealthReasons         []string             `json:"health_reasons,omitempty"`
	DailyDriverReasons    []string             `json:"daily_driver_reasons,omitempty"`
	OperatorReasons       []string             `json:"operator_reasons,omitempty"`
}

// GuardBudgetHeadroom makes the resident's narrow operating margins explicit
// without changing the fail-closed policy evaluation. P95 needs 20 rolling
// samples before nearest-rank selection can exclude one isolated maximum.
type GuardBudgetHeadroom struct {
	RollingSampleCount                int     `json:"rolling_sample_count"`
	RollingWindowSize                 int     `json:"rolling_window_size"`
	SamplesUntilSingleOutlierExcluded int     `json:"samples_until_single_outlier_excluded"`
	P95ExcludesSingleMaximum          bool    `json:"p95_excludes_single_maximum"`
	RSSObservedMB                     float64 `json:"rss_observed_mb"`
	RSSLimitMB                        float64 `json:"rss_limit_mb"`
	RSSMarginMB                       float64 `json:"rss_margin_mb"`
	IdleDutyObservedPercent           float64 `json:"idle_duty_observed_percent"`
	IdleDutyLimitPercent              float64 `json:"idle_duty_limit_percent"`
	IdleDutyMarginPercent             float64 `json:"idle_duty_margin_percent"`
	SampleCPUP95ObservedMS            float64 `json:"sample_cpu_p95_observed_ms"`
	SampleCPUMaxObservedMS            float64 `json:"sample_cpu_max_observed_ms"`
	SampleCPULimitMS                  float64 `json:"sample_cpu_limit_ms"`
	SampleCPUMarginMS                 float64 `json:"sample_cpu_margin_ms"`
	TelemetryProjectedBytesDay        int64   `json:"telemetry_projected_bytes_per_day"`
	TelemetryLimitBytesDay            int64   `json:"telemetry_limit_bytes_per_day"`
	TelemetryMarginBytesDay           int64   `json:"telemetry_margin_bytes_per_day"`
}

const p95SingleOutlierExclusionSamples = 20

// inventoryFreshnessMaxAgeSeconds is one inventory interval plus one sample
// cadence, plus slack for a hang-ceiling sample and a doctor/self-test read
// after that sample. 15s slack alone false-reds when a 4s sample is followed
// by a 12s delayed doctor (live 2026-08-12: 376.2s > 375s).
func inventoryFreshnessMaxAgeSeconds(policy Policy) float64 {
	slack := 15.0
	if policy.ResourceBudgets.MaxSampleDurationMS > 0 {
		slack = max(slack, policy.ResourceBudgets.MaxSampleDurationMS/1000)
	}
	// Immature Evaluate uses a 10s hang ceiling even when the mature
	// duration budget is 2s.
	slack = max(slack, 10)
	slack += 15
	return float64(policy.ProcessInventoryIntervalSeconds+policy.SampleIntervalSeconds) + slack
}

func guardBudgetHeadroom(latest Snapshot, policy Policy) *GuardBudgetHeadroom {
	rollingSamples := min(latest.MonitorSamples, monitorStatsWindow)
	untilOutlierExcluded := max(0, p95SingleOutlierExclusionSamples-rollingSamples)
	return &GuardBudgetHeadroom{
		RollingSampleCount: rollingSamples, RollingWindowSize: monitorStatsWindow,
		SamplesUntilSingleOutlierExcluded: untilOutlierExcluded,
		P95ExcludesSingleMaximum:          rollingSamples >= p95SingleOutlierExclusionSamples,
		RSSObservedMB:                     latest.GuardRSSMaxMB, RSSLimitMB: policy.ResourceBudgets.MaxSelfRSSMB,
		RSSMarginMB:             policy.ResourceBudgets.MaxSelfRSSMB - latest.GuardRSSMaxMB,
		IdleDutyObservedPercent: latest.GuardIdleCPUDutyPercent, IdleDutyLimitPercent: policy.ResourceBudgets.MaxIdleCPUPercent,
		IdleDutyMarginPercent:  policy.ResourceBudgets.MaxIdleCPUPercent - latest.GuardIdleCPUDutyPercent,
		SampleCPUP95ObservedMS: latest.SampleCPUTimeP95MS, SampleCPUMaxObservedMS: latest.SampleCPUTimeMaxMS,
		SampleCPULimitMS:           policy.ResourceBudgets.MaxSampleCPUTimeMS,
		SampleCPUMarginMS:          policy.ResourceBudgets.MaxSampleCPUTimeMS - latest.SampleCPUTimeP95MS,
		TelemetryProjectedBytesDay: latest.TelemetryProjectedBytesDay, TelemetryLimitBytesDay: policy.ResourceBudgets.MaxTelemetryBytesDay,
		TelemetryMarginBytesDay: policy.ResourceBudgets.MaxTelemetryBytesDay - latest.TelemetryProjectedBytesDay,
	}
}

// WithOperatorState layers recovery-state truth over daemon readiness. This
// keeps "daily driver" as a protection-mode assessment while ensuring status
// and self-test agree about pending operator work.
func (health GuardHealth) WithOperatorState(hasRecovery bool, recoveryErr error) GuardHealth {
	health.OperatorReasons = nil
	if !health.DailyDriverReady {
		health.OperatorReasons = append(health.OperatorReasons, "resident is not daily-driver ready")
	}
	if recoveryErr != nil {
		health.OperatorReasons = append(health.OperatorReasons, "recovery state could not be read: "+recoveryErr.Error())
	} else if hasRecovery {
		health.OperatorReasons = append(health.OperatorReasons, "an unclean-shutdown recovery hint is pending review")
	}
	health.OperatorReady = len(health.OperatorReasons) == 0
	return health
}

func protectionMode(policy Policy) string {
	switch {
	case !policy.Enabled:
		return "disabled"
	case policy.EnforceAdmission && policy.AutoShedCritical:
		return "full"
	case policy.EnforceAdmission:
		return "admission-only"
	default:
		return "observe-only"
	}
}

// AssessGuardHealth evaluates persisted policy, launchd state, freshness, and
// resident self-budget evidence without taking another sample.
func AssessGuardHealth(now time.Time, policy Policy, persisted bool, launchd LaunchdStatus, latest Snapshot, hasLatest bool) GuardHealth {
	health := GuardHealth{ProtectionMode: protectionMode(policy), RequiredNormalSamples: policy.SustainSamples}
	if !persisted {
		health.HealthReasons = append(health.HealthReasons, "policy is not persisted")
	}
	if !policy.Enabled {
		health.HealthReasons = append(health.HealthReasons, "policy is disabled")
	}
	if !launchd.OK {
		health.HealthReasons = append(health.HealthReasons, "LaunchAgent is not installed, loaded, and running")
	}
	if launchd.ArtifactSHA256 == "" || !launchd.ArtifactPresent || !launchd.ArtifactVerified {
		health.HealthReasons = append(health.HealthReasons, "immutable resident artifact is not installed and digest-verified")
	}
	if !hasLatest {
		health.HealthReasons = append(health.HealthReasons, "resident monitor has not written latest telemetry")
	} else {
		health.BudgetHeadroom = guardBudgetHeadroom(latest, policy)
		health.ResidentSamples = latest.MonitorSamples
		health.ResidentNormalSamples = latest.NormalMonitorSamples
		age := now.Sub(latest.Timestamp).Seconds()
		maxAge := float64(policy.IntervalSeconds(latest.Level)*2 + 15)
		health.LatestAgeSeconds = age
		health.LatestMaxAgeSeconds = maxAge
		health.LatestMonitorFresh = age >= -5 && age <= maxAge
		if !health.LatestMonitorFresh {
			health.HealthReasons = append(health.HealthReasons, fmt.Sprintf("resident telemetry age %.1fs is outside the %.1fs freshness window", age, maxAge))
		}
		if latest.GuardRole != "resident" || !latest.GuardBudgetApplicable {
			health.HealthReasons = append(health.HealthReasons, "latest telemetry is not resident budget evidence")
		}
		if !latest.ProcessInventoryAvailable {
			health.HealthReasons = append(health.HealthReasons, "resident process inventory is unavailable")
		} else {
			capturedAt := latest.ProcessInventoryCapturedAt
			if capturedAt.IsZero() {
				capturedAt = latest.Timestamp
			}
			inventoryAge := now.Sub(capturedAt).Seconds()
			inventoryMaxAge := inventoryFreshnessMaxAgeSeconds(policy)
			if inventoryAge < -5 || inventoryAge > inventoryMaxAge {
				health.HealthReasons = append(health.HealthReasons, fmt.Sprintf("resident process inventory age %.1fs is outside the %.1fs freshness window", inventoryAge, inventoryMaxAge))
			}
		}
		if latest.GuardPID == 0 || latest.GuardPID != launchd.PID {
			health.HealthReasons = append(health.HealthReasons, fmt.Sprintf("latest telemetry writer pid %d does not match launchd pid %d", latest.GuardPID, launchd.PID))
		}
		if launchd.ArtifactSHA256 != "" && latest.GuardBinarySHA256 != launchd.ArtifactSHA256 {
			health.HealthReasons = append(health.HealthReasons, "resident binary digest does not match the installed artifact")
		}
		effectiveBudget := Evaluate(latest, policy)
		// Doctor follows the current control Evaluate, not the helper's
		// persisted GuardBudgetOK. A just-shipped budget rule must not wait
		// for the next helper install to stop a false-red daily-driver.
		if !effectiveBudget.GuardBudgetOK {
			health.HealthReasons = append(health.HealthReasons, "resident guard resource budget is failing under the effective policy")
		}
		if latest.ResourceCleanupError != "" {
			health.HealthReasons = append(health.HealthReasons, "typed resource cleanup is failing: "+latest.ResourceCleanupError)
		}
	}
	health.MonitorHealthy = len(health.HealthReasons) == 0
	if !policy.EnforceAdmission {
		health.DailyDriverReasons = append(health.DailyDriverReasons, "red-level launch admission is disabled")
	}
	if !policy.AutoShedCritical {
		health.DailyDriverReasons = append(health.DailyDriverReasons, "automatic sustained-critical relief is disabled")
	}
	if hasLatest && !latest.GuardBaselineProven {
		health.DailyDriverReasons = append(health.DailyDriverReasons, fmt.Sprintf("resident baseline is not proven; need %d normal samples from this pid", policy.SustainSamples))
	}
	if !health.MonitorHealthy {
		health.DailyDriverReasons = append(health.DailyDriverReasons, "resident monitor is unhealthy")
	}
	health.DailyDriverReady = len(health.DailyDriverReasons) == 0
	health.OperatorReady = health.DailyDriverReady
	return health
}
