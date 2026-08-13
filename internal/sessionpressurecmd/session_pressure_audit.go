package sessionpressurecmd

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/nstranquist/session-pressure/internal/atomicfile"
	"github.com/nstranquist/session-pressure/internal/hostcleanup"
	"github.com/nstranquist/session-pressure/internal/sessionpressure"
	"github.com/nstranquist/session-pressure/internal/telemetry"
)

const (
	pressureAuditPass                          = "pass"
	pressureAuditWarn                          = "warn"
	pressureAuditFail                          = "fail"
	residentBinaryTargetBytes            int64 = 12 << 20
	pressureAuditWriterLatencyMinSamples int64 = 20
	pressureAuditWriterLatencyWarnUS     int64 = 25_000
	pressureAuditWriterLatencyFailUS     int64 = 100_000
	pressureAuditScanWarn                      = 2 * time.Second
	pressureAuditScanFail                      = 10 * time.Second
	pressureAuditCleanupDurationWarnMS         = 5_000.0
	pressureAuditCleanupDurationFailMS         = 15_000.0
	pressureAuditCleanupRSSWarnMB              = 64.0
	pressureAuditCleanupRSSFailMB              = 128.0
	cliAuditCacheSchemaVersion                 = 5
	cliAuditCacheMaximumBytes                  = 4 << 20
)

type pressureAuditFinding struct {
	Severity string `json:"severity"`
	Code     string `json:"code"`
	Message  string `json:"message"`
}

type pressureAuditCategory struct {
	ID       string                 `json:"id"`
	Status   string                 `json:"status"`
	Summary  string                 `json:"summary"`
	Metrics  map[string]any         `json:"metrics,omitempty"`
	Findings []pressureAuditFinding `json:"findings"`
}

type pressureAuditReport struct {
	SchemaVersion int                     `json:"schema_version"`
	OK            bool                    `json:"ok"`
	Overall       string                  `json:"overall"`
	GeneratedAt   time.Time               `json:"generated_at"`
	Since         time.Time               `json:"since"`
	LiveSampled   bool                    `json:"live_sampled"`
	Directory     string                  `json:"directory"`
	DurationMS    float64                 `json:"duration_ms"`
	Categories    []pressureAuditCategory `json:"categories"`
}

// pressureCurrentControllerScheduler is supplemental rollout evidence. The
// requested and fixed two-hour windows remain authoritative for audit status;
// this view makes behavior since the verified resident/control activation
// visible without allowing a deployment to erase historical SLO failures.
type pressureCurrentControllerScheduler struct {
	Known                    bool                                                 `json:"known"`
	WorkObserved             bool                                                 `json:"work_observed"`
	Since                    string                                               `json:"since,omitempty"`
	Events                   int                                                  `json:"events"`
	Operations               int                                                  `json:"operations"`
	OpenOperations           int                                                  `json:"open_operations"`
	LongWaitOperations       int                                                  `json:"long_wait_operations"`
	ExpiredOwnerEvents       int                                                  `json:"expired_owner_events"`
	ClassReclassifications   int                                                  `json:"class_reclassifications"`
	FullToExpressAdjustments int                                                  `json:"full_to_express_adjustments"`
	ExpressToFullAdjustments int                                                  `json:"express_to_full_adjustments"`
	SlowdownStatus           string                                               `json:"slowdown_status,omitempty"`
	PressureConditioned      *sessionpressure.WorkPressureConditionedServiceLevel `json:"pressure_conditioned_service_level,omitempty"`
	CalibrationCohorts       []sessionpressure.WorkCalibrationCohort              `json:"calibration_cohorts,omitempty"`
}

type pressureCurrentControllerResident struct {
	Known  bool   `json:"known"`
	Since  string `json:"since,omitempty"`
	Starts int    `json:"starts"`
	Stops  int    `json:"stops"`
	Stable bool   `json:"stable"`
}

type pressureAuditJSONL struct {
	Files             int            `json:"files"`
	Rows              int            `json:"rows"`
	ParseErrors       int            `json:"parse_errors"`
	ValidationErrors  int            `json:"validation_errors"`
	ValidationClasses map[string]int `json:"validation_classes,omitempty"`
	InsecureFiles     int            `json:"insecure_files"`
}

type cliAuditCacheStats struct {
	Hits            int  `json:"hits"`
	IncrementalHits int  `json:"incremental_hits"`
	Misses          int  `json:"misses"`
	LoadError       bool `json:"load_error"`
	WriteError      bool `json:"write_error"`
}

type cliAuditCache struct {
	SchemaVersion int                           `json:"schema_version"`
	Entries       map[string]cliAuditCacheEntry `json:"entries"`
}

type cliAuditCacheEntry struct {
	SizeBytes           int64              `json:"size_bytes"`
	ModUnixNano         int64              `json:"mod_unix_nano"`
	Mode                uint32             `json:"mode"`
	ContentDigest       string             `json:"content_digest"`
	CompleteEventWindow bool               `json:"complete_event_window"`
	FirstEventUnixNano  int64              `json:"first_event_unix_nano,omitempty"`
	LastEventUnixNano   int64              `json:"last_event_unix_nano,omitempty"`
	JSONL               pressureAuditJSONL `json:"jsonl"`
	Counts              map[string]int     `json:"counts,omitempty"`
	Effective           map[string]int     `json:"effective,omitempty"`
}

type auditFileSignature struct {
	SizeBytes   int64
	ModUnixNano int64
	Mode        uint32
}

type pressureAuditSlowdownEvidence struct {
	Cohorts      int
	Breaches     int
	Deferred     int
	BreachDetail []string
	Findings     []pressureAuditFinding
}

func newPressureAuditCategory(id, summary string, metrics map[string]any, findings ...pressureAuditFinding) pressureAuditCategory {
	status := pressureAuditPass
	for _, finding := range findings {
		if finding.Severity == pressureAuditFail {
			status = pressureAuditFail
			break
		}
		if finding.Severity == pressureAuditWarn {
			status = pressureAuditWarn
		}
	}
	if findings == nil {
		findings = []pressureAuditFinding{}
	}
	return pressureAuditCategory{ID: id, Status: status, Summary: summary, Metrics: metrics, Findings: findings}
}

func pressureFinding(severity, code, message string) pressureAuditFinding {
	return pressureAuditFinding{Severity: severity, Code: code, Message: message}
}

func cmdSessionPressureAudit(g *Flags, args []string) int {
	auditStarted := time.Now()
	live := true
	full := false
	sinceDuration := 24 * time.Hour
	for index := 0; index < len(args); index++ {
		switch args[index] {
		case "--live":
			live = true
		case "--no-live":
			live = false
		case "--full":
			if full {
				return sessionPressureError("audit accepts --full only once", 2)
			}
			full = true
		case "--since":
			index++
			if index >= len(args) {
				return sessionPressureError("--since requires a duration", 2)
			}
			parsed, err := time.ParseDuration(args[index])
			if err != nil || parsed <= 0 || parsed > 30*24*time.Hour {
				return sessionPressureError("--since must be between 1ns and 720h", 2)
			}
			sinceDuration = parsed
		default:
			return sessionPressureError("unknown audit argument "+fmt.Sprintf("%q", args[index]), 2)
		}
	}

	now := time.Now().UTC()
	since := now.Add(-sinceDuration)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	runtime, err := loadPressureRuntime(ctx)
	if err != nil {
		return sessionPressureError("audit runtime: "+err.Error(), 1)
	}
	report := pressureAuditReport{
		SchemaVersion: 2, OK: true, Overall: pressureAuditPass, GeneratedAt: now,
		Since: since, LiveSampled: live, Directory: runtime.dir, Categories: []pressureAuditCategory{},
	}

	report.Categories = append(report.Categories, auditPressurePolicy(runtime))
	launchd, artifact, retention, artifactErr, retentionErr, latest, hasLatest := auditPressureInstalledState(ctx, runtime)
	report.Categories = append(report.Categories, auditPressureResident(now, runtime, launchd, artifact, retention, latest, hasLatest))

	reading := latest
	readingAvailable := hasLatest
	readingError := error(nil)
	if live {
		reading, readingError = SampleSnapshot(ctx, runtime.sampler, runtime.policy)
		readingAvailable = readingError == nil
	}
	report.Categories = append(report.Categories, auditPressureReadings(now, runtime.policy, reading, readingAvailable, readingError, latest, hasLatest, live))
	report.Categories = append(report.Categories, auditPressureQueue(ctx, runtime, since, now, launchd))
	telemetryAuditStarted := time.Now()
	telemetryCategory := auditPressureTelemetry(runtime, since, now, launchd, latest, hasLatest)
	telemetryCategory = auditPressureScanRuntime(telemetryCategory, time.Since(telemetryAuditStarted))
	report.Categories = append(report.Categories, telemetryCategory)
	report.Categories = append(report.Categories, auditPressurePerformance(runtime, artifact, latest, hasLatest))
	report.Categories = append(report.Categories, auditPressureCleanup(runtime, latest, hasLatest))
	report.Categories = append(report.Categories, auditPressureArtifacts(artifact, retention, artifactErr, retentionErr))
	report.DurationMS = float64(time.Since(auditStarted).Microseconds()) / 1000

	for _, category := range report.Categories {
		switch category.Status {
		case pressureAuditFail:
			report.Overall = pressureAuditFail
			report.OK = false
		case pressureAuditWarn:
			if report.Overall == pressureAuditPass {
				report.Overall = pressureAuditWarn
			}
		}
	}
	if g != nil && g.JSON {
		payload := map[string]any{"ok": report.OK, "action": "audit", "output_scope": "compact", "audit": projectCompactPressureAudit(report)}
		if full {
			payload["output_scope"] = "full"
			payload["audit"] = report
		}
		return emitPressure(g, payload, "", auditExit(report))
	}
	var text strings.Builder
	fmt.Fprintf(&text, "SessionPressure audit: overall=%s live=%t since=%s\n", report.Overall, report.LiveSampled, sinceDuration)
	for _, category := range report.Categories {
		fmt.Fprintf(&text, "%-9s %-22s %s\n", strings.ToUpper(category.Status), category.ID, category.Summary)
		for _, finding := range category.Findings {
			fmt.Fprintf(&text, "  - %s: %s\n", finding.Code, finding.Message)
		}
	}
	payload := map[string]any{"ok": report.OK, "action": "audit", "output_scope": "compact", "audit": projectCompactPressureAudit(report)}
	if full {
		payload["output_scope"] = "full"
		payload["audit"] = report
	}
	return emitPressure(g, payload, text.String(), auditExit(report))
}

// compactPressureAuditCategory keeps pass/warn/fail status, summary, and
// findings while dropping large nested metrics maps (scheduler SLO samples,
// telemetry scan blobs). --full restores metrics for forensic drill-down.
type compactPressureAuditCategory struct {
	ID       string                 `json:"id"`
	Status   string                 `json:"status"`
	Summary  string                 `json:"summary"`
	Findings []pressureAuditFinding `json:"findings"`
}

type compactPressureAuditView struct {
	SchemaVersion int                            `json:"schema_version"`
	OK            bool                           `json:"ok"`
	Overall       string                         `json:"overall"`
	GeneratedAt   time.Time                      `json:"generated_at"`
	Since         time.Time                      `json:"since"`
	LiveSampled   bool                           `json:"live_sampled"`
	DurationMS    float64                        `json:"duration_ms"`
	Categories    []compactPressureAuditCategory `json:"categories"`
}

func projectCompactPressureAudit(report pressureAuditReport) compactPressureAuditView {
	categories := make([]compactPressureAuditCategory, 0, len(report.Categories))
	for _, category := range report.Categories {
		findings := category.Findings
		if findings == nil {
			findings = []pressureAuditFinding{}
		}
		categories = append(categories, compactPressureAuditCategory{
			ID: category.ID, Status: category.Status, Summary: category.Summary, Findings: findings,
		})
	}
	return compactPressureAuditView{
		SchemaVersion: report.SchemaVersion, OK: report.OK, Overall: report.Overall,
		GeneratedAt: report.GeneratedAt, Since: report.Since, LiveSampled: report.LiveSampled,
		DurationMS: report.DurationMS, Categories: categories,
	}
}

func auditExit(report pressureAuditReport) int {
	if report.OK {
		return 0
	}
	return 1
}

func auditPressurePolicy(runtime pressureRuntime) pressureAuditCategory {
	metrics := map[string]any{
		"persisted": runtime.persisted, "enabled": runtime.policy.Enabled,
		"enforce_admission": runtime.policy.EnforceAdmission, "auto_shed_critical": runtime.policy.AutoShedCritical,
		"sample_interval_seconds": runtime.policy.SampleIntervalSeconds, "heartbeat_seconds": runtime.policy.HeartbeatSeconds,
		"work_wait_grace_seconds": runtime.policy.Storage.WorkWaitGraceSeconds,
	}
	findings := []pressureAuditFinding{}
	if err := runtime.policy.Validate(); err != nil {
		findings = append(findings, pressureFinding(pressureAuditFail, "policy_invalid", err.Error()))
	}
	if !runtime.persisted {
		findings = append(findings, pressureFinding(pressureAuditFail, "policy_not_persisted", "the resident has no durable policy authority"))
	}
	if !runtime.policy.Enabled {
		findings = append(findings, pressureFinding(pressureAuditFail, "monitor_disabled", "pressure monitoring is disabled"))
	}
	if !runtime.policy.EnforceAdmission {
		findings = append(findings, pressureFinding(pressureAuditWarn, "admission_observe_only", "red-pressure admission is observe-only"))
	}
	if !runtime.policy.AutoShedCritical {
		findings = append(findings, pressureFinding(pressureAuditWarn, "auto_relief_disabled", "sustained-critical automatic relief is disabled"))
	}
	return newPressureAuditCategory("policy", "durable protection policy and safety thresholds", metrics, findings...)
}

func auditPressureInstalledState(ctx context.Context, runtime pressureRuntime) (sessionpressure.LaunchdStatus, sessionpressure.InstalledArtifact, sessionpressure.ArtifactPruneReport, error, error, sessionpressure.Snapshot, bool) {
	launchd := sessionpressure.LaunchdStatus{}
	if manager, err := NewLaunchdController(runtime.dir); err == nil {
		launchd = manager.Status(ctx)
	} else {
		launchd.Detail = err.Error()
	}
	artifact, artifactErr := sessionpressure.VerifyInstalledArtifact(runtime.dir)
	retention, retentionErr := sessionpressure.PruneArtifacts(ctx, runtime.dir, false)
	latest, hasLatest := runtime.store.ReadLatest()
	return launchd, artifact, retention, artifactErr, retentionErr, latest, hasLatest
}

func auditPressureResident(now time.Time, runtime pressureRuntime, launchd sessionpressure.LaunchdStatus, artifact sessionpressure.InstalledArtifact, retention sessionpressure.ArtifactPruneReport, latest sessionpressure.Snapshot, hasLatest bool) pressureAuditCategory {
	metrics := map[string]any{
		"launchd_ok": launchd.OK, "pid": launchd.PID, "artifact_verified": launchd.ArtifactVerified,
		"artifact_sha256": launchd.ArtifactSHA256, "latest_available": hasLatest,
	}
	findings := []pressureAuditFinding{}
	if !launchd.OK {
		findings = append(findings, pressureFinding(pressureAuditFail, "resident_not_running", firstNonEmpty(launchd.Detail, "LaunchAgent is not installed, loaded, and running")))
	}
	if artifact.SHA256 == "" || !launchd.ArtifactVerified || artifact.SHA256 != launchd.ArtifactSHA256 {
		findings = append(findings, pressureFinding(pressureAuditFail, "artifact_parity_failed", "installed manifest, launchd program, and verified digest do not agree"))
	}
	health := sessionpressure.AssessGuardHealth(now, runtime.policy, runtime.persisted, launchd, latest, hasLatest)
	metrics["monitor_healthy"] = health.MonitorHealthy
	metrics["daily_driver_ready"] = health.DailyDriverReady
	metrics["latest_age_seconds"] = health.LatestAgeSeconds
	for _, reason := range health.HealthReasons {
		findings = append(findings, pressureFinding(pressureAuditFail, "resident_health", reason))
	}
	for _, reason := range health.DailyDriverReasons {
		findings = append(findings, pressureFinding(pressureAuditWarn, "daily_driver_gate", reason))
	}
	if retention.PruneCount > 0 {
		findings = append(findings, pressureFinding(pressureAuditWarn, "artifact_retention_pending", fmt.Sprintf("%d verified artifact revisions are outside retention", retention.PruneCount)))
	}
	return newPressureAuditCategory("resident", "launchd, digest parity, freshness, and daily-driver readiness", metrics, findings...)
}

func auditPressureReadings(now time.Time, policy sessionpressure.Policy, reading sessionpressure.Snapshot, available bool, readingErr error, latest sessionpressure.Snapshot, hasLatest, live bool) pressureAuditCategory {
	metrics := map[string]any{"available": available, "live": live}
	findings := []pressureAuditFinding{}
	if !available {
		message := "no current pressure reading is available"
		if readingErr != nil {
			message = readingErr.Error()
		}
		findings = append(findings, pressureFinding(pressureAuditFail, "sample_unavailable", message))
		return newPressureAuditCategory("readings", "memory, CPU, process, and storage sensor integrity", metrics, findings...)
	}
	metrics["level"] = reading.Level
	metrics["free_percent"] = reading.FreePercent
	metrics["host_cpu_percent"] = reading.HostCPUPercent
	metrics["host_cpu_source"] = reading.HostCPUSource
	metrics["host_cpu_live_window_ms"] = reading.HostCPULiveWindowMS
	metrics["host_cpu_rolling_available"] = reading.HostCPURollingAvailable
	metrics["process_inventory_available"] = reading.ProcessInventoryAvailable
	metrics["process_inventory_refreshed_this_sample"] = reading.ProcessInventoryFresh
	inventoryAge := reading.ProcessInventoryAgeSeconds
	if !reading.ProcessInventoryCapturedAt.IsZero() {
		inventoryAge = max(0, now.Sub(reading.ProcessInventoryCapturedAt).Seconds())
	}
	inventoryMaxAge := float64(policy.ProcessInventoryIntervalSeconds + policy.SampleIntervalSeconds + 15)
	metrics["process_inventory_age_seconds"] = inventoryAge
	metrics["process_inventory_max_age_seconds"] = inventoryMaxAge
	metrics["storage_available"] = reading.Storage.Available
	if reading.Storage.Available {
		metrics["storage_level"] = reading.Storage.Level
		metrics["storage_free_bytes"] = reading.Storage.FreeBytes
		metrics["storage_available_bytes"] = reading.Storage.AvailableBytes
		metrics["storage_free_percent"] = reading.Storage.FreePercent
		metrics["storage_hysteresis_active"] = reading.Storage.HysteresisActive
	}
	if reading.PhysicalMemoryMB <= 0 || reading.FreePercent < 0 || reading.FreePercent > 100 {
		findings = append(findings, pressureFinding(pressureAuditFail, "memory_reading_invalid", "physical memory or free-memory percentage is outside its valid range"))
	}
	if !reading.HostCPUAvailable || reading.HostCPUSource == "" {
		findings = append(findings, pressureFinding(pressureAuditFail, "cpu_reading_unavailable", firstNonEmpty(reading.HostCPUError, "host CPU evidence is unavailable")))
	}
	if live && reading.HostCPULiveWindowMS < 250 {
		findings = append(findings, pressureFinding(pressureAuditFail, "cpu_window_too_short", fmt.Sprintf("live CPU window %.1fms is below the 250ms validity floor", reading.HostCPULiveWindowMS)))
	}
	if !reading.HostCPURollingAvailable {
		findings = append(findings, pressureFinding(pressureAuditWarn, "cpu_rolling_unavailable", "a live spike cannot yet be corroborated by resident rolling CPU evidence"))
	}
	inventoryCurrent := reading.ProcessInventoryFresh ||
		(reading.ProcessInventoryAvailable && !reading.ProcessInventoryCapturedAt.IsZero() && inventoryAge >= 0 && inventoryAge <= inventoryMaxAge)
	if !reading.ProcessInventoryAvailable {
		findings = append(findings, pressureFinding(pressureAuditFail, "process_inventory_unavailable", firstNonEmpty(reading.ProcessInventoryError, "process inventory is unavailable")))
	} else if !inventoryCurrent {
		findings = append(findings, pressureFinding(pressureAuditFail, "process_inventory_stale", fmt.Sprintf("process inventory age %.1fs is outside the %.1fs freshness window", inventoryAge, inventoryMaxAge)))
	}
	if !reading.Storage.Available {
		findings = append(findings, pressureFinding(pressureAuditWarn, "storage_reading_unavailable", firstNonEmpty(reading.Storage.Error, "storage capacity evidence is unavailable")))
	} else if reading.Storage.Level.AtLeast(sessionpressure.LevelRed) {
		findings = append(findings, pressureFinding(pressureAuditFail, "storage_pressure", firstNonEmpty(strings.Join(reading.Storage.Reasons, "; "), fmt.Sprintf("storage pressure is %s", reading.Storage.Level))))
	} else if reading.Storage.Level.AtLeast(sessionpressure.LevelWarning) {
		findings = append(findings, pressureFinding(pressureAuditWarn, "storage_warning", firstNonEmpty(strings.Join(reading.Storage.Reasons, "; "), "storage capacity is inside the warning band")))
	}
	if hasLatest && latest.PhysicalMemoryMB > 0 {
		delta := reading.PhysicalMemoryMB - latest.PhysicalMemoryMB
		if delta < 0 {
			delta = -delta
		}
		if delta > latest.PhysicalMemoryMB*0.01 {
			findings = append(findings, pressureFinding(pressureAuditFail, "physical_memory_drift", "live and resident physical-memory totals differ by more than one percent"))
		}
	}
	return newPressureAuditCategory("readings", "memory, CPU, process, and storage sensor integrity", metrics, findings...)
}

func auditPressureQueue(ctx context.Context, runtime pressureRuntime, since, now time.Time, launchd sessionpressure.LaunchdStatus) pressureAuditCategory {
	metrics := map[string]any{}
	findings := []pressureAuditFinding{}
	coordinator := sessionpressure.NewWorkCoordinator(runtime.dir, runtime.policy.WorkLimits)
	status, err := coordinator.Status(ctx)
	if err != nil {
		findings = append(findings, pressureFinding(pressureAuditFail, "queue_state_unreadable", err.Error()))
		return newPressureAuditCategory("scheduler", "weighted capacity, reservations, fairness, and slowdown", metrics, findings...)
	}
	metrics["schema_version"] = status.SchemaVersion
	metrics["selector_schema_version"] = status.SelectorSchemaVersion
	metrics["scheduling_policy"] = status.SchedulingPolicy
	metrics["capacity"] = status.Capacity
	metrics["used"] = status.Used
	metrics["available"] = status.Available
	metrics["leases"] = len(status.Leases)
	metrics["queue_depth"] = status.QueueDepth
	metrics["pressure_reservations"] = status.PressureReservationCount
	metrics["reserved_weight"] = status.ReservedWeight
	overcommit, leaseWeight, capacityOK := pressureWorkCapacityInvariant(status)
	metrics["overcommit_weight"] = overcommit
	metrics["lease_weight"] = leaseWeight
	if !capacityOK {
		findings = append(findings, pressureFinding(pressureAuditFail, "capacity_invariant_failed", fmt.Sprintf("used=%d lease_weight=%d capacity=%d available=%d overcommit=%d", status.Used, leaseWeight, status.Capacity, status.Available, overcommit)))
	}
	if status.PressureReservationCount > status.QueueDepth || status.ReservedWeight < 0 {
		findings = append(findings, pressureFinding(pressureAuditFail, "reservation_invariant_failed", "pressure reservation counters do not match queue state"))
	}
	readSince := pressureCurrentControllerReadSince(since, now, launchd)
	events, diagnostics, err := sessionpressure.NewWorkEventStore(runtime.dir).ReadWithDiagnostics(sessionpressure.WorkEventFilter{Since: readSince})
	if err != nil {
		findings = append(findings, pressureFinding(pressureAuditFail, "work_audit_unreadable", err.Error()))
		return newPressureAuditCategory("scheduler", "weighted capacity, reservations, fairness, and slowdown", metrics, findings...)
	}
	// A tolerated per-operation lifecycle fault degrades the sample, it does not
	// invalidate the scheduler. Report it and keep scoring everything else.
	metrics["skipped_operations"] = diagnostics.SkippedOperations
	metrics["skipped_events"] = diagnostics.SkippedEvents
	if diagnostics.Degraded() {
		findings = append(findings, pressureFinding(pressureAuditWarn, "work_audit_skipped_operations",
			fmt.Sprintf("skipped %d operation(s) / %d event(s) with unreadable lifecycles: %s",
				diagnostics.SkippedOperations, diagnostics.SkippedEvents, strings.Join(diagnostics.Reasons, "; "))))
	}
	requestedEvents := pressureWorkEventsSince(events, since)
	stats := sessionpressure.SummarizeWorkEvents(requestedEvents, since, now)
	metrics["current_controller_scheduler"] = auditCurrentControllerScheduler(events, now, launchd)
	recentSince := now.Add(-2 * time.Hour)
	if since.After(recentSince) {
		recentSince = since
	}
	recentEvents := make([]sessionpressure.WorkEvent, 0, len(events))
	for _, event := range events {
		if !event.Timestamp.Before(recentSince) {
			recentEvents = append(recentEvents, event)
		}
	}
	recentStats := sessionpressure.SummarizeWorkEvents(recentEvents, recentSince, now)
	metrics["events"] = stats.EventCount
	metrics["operations"] = stats.OperationCount
	metrics["open_operations"] = stats.OpenOperations
	metrics["slowdown_status"] = stats.ServiceLevel.Status
	metrics["slowdown_target_p95"] = stats.ServiceLevel.TargetP95BoundedSlowdown
	metrics["long_wait_operations"] = stats.ReviewSignals.LongWaitOperations
	metrics["expired_owner_events"] = stats.ReviewSignals.ExpiredOwnerEvents
	metrics["wrapper_interrupts"] = stats.ReviewSignals.WrapperInterruptOperations
	metrics["class_reclassifications"] = stats.ReviewSignals.ClassReclassifications
	metrics["full_to_express_adjustments"] = stats.ReviewSignals.FullToExpressAdjustments
	metrics["express_to_full_adjustments"] = stats.ReviewSignals.ExpressToFullAdjustments
	metrics["recent_health_since"] = recentSince
	metrics["recent_health_events"] = recentStats.EventCount
	metrics["recent_health_operations"] = recentStats.OperationCount
	metrics["recent_slowdown_status"] = recentStats.ServiceLevel.Status
	metrics["pressure_conditioned_service_level"] = stats.PressureConditionedServiceLevel
	metrics["recent_pressure_conditioned_service_level"] = recentStats.PressureConditionedServiceLevel
	slowdown := auditPressureSlowdown(stats, recentStats)
	metrics["recent_calibration_cohorts"] = slowdown.Cohorts
	metrics["recent_cohort_breaches"] = slowdown.Breaches
	metrics["recent_cohort_deferred"] = slowdown.Deferred
	metrics["recent_cohort_breach_details"] = slowdown.BreachDetail
	findings = append(findings, slowdown.Findings...)
	findings = append(findings, auditPressureConditionedSlowdown(recentStats.PressureConditionedServiceLevel, slowdown.Breaches > 0)...)
	requestedConditioned := stats.PressureConditionedServiceLevel
	if requestedConditioned.EvidenceStatus == "invalid" && recentStats.PressureConditionedServiceLevel.EvidenceStatus != "invalid" {
		findings = append(findings, pressureFinding(
			pressureAuditFail, "pressure_conditioned_history_invalid",
			fmt.Sprintf("requested history contains %d invalid pressure-wait decompositions", requestedConditioned.InvalidDecompositionSamples),
		))
	} else if requestedConditioned.EvidenceStatus == "partial" && recentStats.PressureConditionedServiceLevel.EvidenceStatus == "complete" {
		findings = append(findings, pressureFinding(
			pressureAuditWarn, "pressure_conditioned_history_partial",
			fmt.Sprintf("requested history excludes %d legacy or window-boundary terminal-runtime samples; recent evidence is complete", requestedConditioned.LegacySchemaSamples+requestedConditioned.WindowBoundarySamples),
		))
	}
	if stats.ReviewSignals.LongWaitOperations > 0 {
		findings = append(findings, pressureFinding(pressureAuditWarn, "long_waits_observed", fmt.Sprintf("%d operations crossed the long-wait threshold", stats.ReviewSignals.LongWaitOperations)))
	}
	if stats.ReviewSignals.ExpiredOwnerEvents > 0 {
		findings = append(findings, pressureFinding(pressureAuditWarn, "owner_expiry_observed", fmt.Sprintf("%d stale work owners required reconciliation", stats.ReviewSignals.ExpiredOwnerEvents)))
	}
	return newPressureAuditCategory("scheduler", "weighted capacity, reservations, fairness, and slowdown", metrics, findings...)
}

func auditCurrentControllerScheduler(events []sessionpressure.WorkEvent, now time.Time, launchd sessionpressure.LaunchdStatus) pressureCurrentControllerScheduler {
	report := pressureCurrentControllerScheduler{}
	installedAt, ok := pressureVerifiedControllerInstalledAt(now, launchd)
	if !ok {
		return report
	}
	currentOperations := make(map[string]struct{})
	for _, event := range events {
		if !event.Timestamp.Before(installedAt) && (event.Event == sessionpressure.WorkEventQueued || event.Event == sessionpressure.WorkEventReused) {
			currentOperations[event.OperationID] = struct{}{}
		}
	}
	currentEvents := make([]sessionpressure.WorkEvent, 0, len(events))
	for _, event := range events {
		if _, ok := currentOperations[event.OperationID]; ok && !event.Timestamp.Before(installedAt) {
			currentEvents = append(currentEvents, event)
		}
	}
	stats := sessionpressure.SummarizeWorkEvents(currentEvents, installedAt, now)
	report.Known = true
	report.WorkObserved = stats.EventCount > 0
	report.Since = installedAt.Format(time.RFC3339Nano)
	report.Events = stats.EventCount
	report.Operations = stats.OperationCount
	report.OpenOperations = stats.OpenOperations
	report.LongWaitOperations = stats.ReviewSignals.LongWaitOperations
	report.ExpiredOwnerEvents = stats.ReviewSignals.ExpiredOwnerEvents
	report.ClassReclassifications = stats.ReviewSignals.ClassReclassifications
	report.FullToExpressAdjustments = stats.ReviewSignals.FullToExpressAdjustments
	report.ExpressToFullAdjustments = stats.ReviewSignals.ExpressToFullAdjustments
	report.SlowdownStatus = stats.ServiceLevel.Status
	report.PressureConditioned = &stats.PressureConditionedServiceLevel
	report.CalibrationCohorts = stats.CalibrationCohorts
	return report
}

func pressureVerifiedControllerInstalledAt(now time.Time, launchd sessionpressure.LaunchdStatus) (time.Time, bool) {
	if !launchd.ArtifactVerified || !launchd.ControlBinaryVerified {
		return time.Time{}, false
	}
	installedAt, err := time.Parse(time.RFC3339Nano, launchd.ArtifactInstalledAt)
	if err != nil {
		return time.Time{}, false
	}
	installedAt = installedAt.UTC()
	if installedAt.After(now.Add(5 * time.Minute)) {
		return time.Time{}, false
	}
	return installedAt, true
}

func pressureCurrentControllerReadSince(requestedSince, now time.Time, launchd sessionpressure.LaunchdStatus) time.Time {
	installedAt, ok := pressureVerifiedControllerInstalledAt(now, launchd)
	if ok && (requestedSince.IsZero() || installedAt.Before(requestedSince)) {
		return installedAt
	}
	return requestedSince
}

func pressureWorkEventsSince(events []sessionpressure.WorkEvent, since time.Time) []sessionpressure.WorkEvent {
	if since.IsZero() {
		return events
	}
	filtered := make([]sessionpressure.WorkEvent, 0, len(events))
	for _, event := range events {
		if !event.Timestamp.Before(since) {
			filtered = append(filtered, event)
		}
	}
	return filtered
}

func auditPressureConditionedSlowdown(conditioned sessionpressure.WorkPressureConditionedServiceLevel, endToEndBreached bool) []pressureAuditFinding {
	findings := []pressureAuditFinding{}
	if conditioned.InvalidDecompositionSamples > 0 {
		findings = append(findings, pressureFinding(
			pressureAuditFail, "pressure_conditioned_evidence_invalid",
			fmt.Sprintf("%d terminal-runtime samples have pressure wait that does not reconcile with end-to-end wait", conditioned.InvalidDecompositionSamples),
		))
	}
	partialSamples := conditioned.WindowBoundarySamples + conditioned.LegacySchemaSamples
	if partialSamples > 0 {
		findings = append(findings, pressureFinding(
			pressureAuditWarn, "pressure_conditioned_evidence_partial",
			fmt.Sprintf("%d terminal-runtime samples are excluded because their queued row or schema-v4 wait decomposition is unavailable", partialSamples),
		))
	}
	if conditioned.Status == "breached" {
		breaches := make([]string, 0, len(conditioned.Breaches))
		for _, breach := range conditioned.Breaches {
			breaches = append(breaches, fmt.Sprintf("%s p95=%.2f", breach.Class, breach.ObservedP95))
		}
		findings = append(findings, pressureFinding(
			pressureAuditFail, "pressure_conditioned_slo_failed",
			fmt.Sprintf("scheduler-controllable slowdown still exceeds %.2f after subtracting only measured host-pressure wait: %s", conditioned.TargetP95BoundedSlowdown, strings.Join(breaches, "; ")),
		))
	} else if endToEndBreached && conditioned.Status == "met" {
		findings = append(findings, pressureFinding(
			pressureAuditWarn, "host_pressure_slowdown_context",
			fmt.Sprintf("end-to-end slowdown is breached while the informational host-pressure-excluded view meets target; %dms across %d samples was attributed to measured host-pressure wait", conditioned.ExcludedWaitMS, conditioned.PressureAffectedSamples),
		))
	}
	return findings
}

func auditPressureSlowdown(requested, recent sessionpressure.WorkStats) pressureAuditSlowdownEvidence {
	evidence := pressureAuditSlowdownEvidence{BreachDetail: []string{}, Findings: []pressureAuditFinding{}}
	for _, cohort := range recent.CalibrationCohorts {
		if !cohort.Current {
			continue
		}
		evidence.Cohorts++
		switch cohort.Status {
		case "breached":
			evidence.Breaches++
			evidence.BreachDetail = append(evidence.BreachDetail, fmt.Sprintf(
				"%s p95=%.2f samples=%d", cohort.Class, cohort.P95BoundedSlowdown, cohort.TerminalRuntimeSamples,
			))
		case "deferred":
			evidence.Deferred++
		}
	}
	switch {
	case evidence.Breaches > 0:
		evidence.Findings = append(evidence.Findings, pressureFinding(pressureAuditFail, "slowdown_slo_failed", fmt.Sprintf(
			"recent scheduling cohort exceeds p95 bounded-slowdown target %.2f: %s",
			requested.ServiceLevel.TargetP95BoundedSlowdown, strings.Join(evidence.BreachDetail, "; "),
		)))
	case evidence.Deferred > 0:
		evidence.Findings = append(evidence.Findings, pressureFinding(pressureAuditWarn, "slowdown_evidence_insufficient", "recent cohorts do not yet have 20 terminal-runtime samples per class"))
	case evidence.Cohorts == 0:
		evidence.Findings = append(evidence.Findings, pressureFinding(pressureAuditWarn, "slowdown_evidence_missing", "no recent scheduling cohort has terminal-runtime evidence"))
	}
	if requested.ServiceLevel.Status == "breached" && evidence.Breaches == 0 {
		evidence.Findings = append(evidence.Findings, pressureFinding(pressureAuditWarn, "historical_slowdown_breach", "the requested history contains a slowdown breach that is not reproduced in the recent health window"))
	}
	return evidence
}

func pressureWorkCapacityInvariant(status sessionpressure.WorkStatus) (overcommit, leaseWeight int, ok bool) {
	expressWeight := 0
	for _, lease := range status.Leases {
		leaseWeight += lease.Weight
		if sessionpressure.IsExpressClass(lease.Class) {
			expressWeight += lease.Weight
		}
	}
	overcommit = max(0, status.Used-status.Capacity)
	ok = status.Capacity > 0 && status.Used >= 0 && leaseWeight == status.Used &&
		status.Available == max(0, status.Capacity-status.Used)
	if !ok || overcommit == 0 {
		return overcommit, leaseWeight, ok
	}
	ok = overcommit <= sessionpressure.WorkMaximumGreenExpressOvercommit && expressWeight >= overcommit
	return overcommit, leaseWeight, ok
}

func auditPressureTelemetry(runtime pressureRuntime, since, now time.Time, launchd sessionpressure.LaunchdStatus, latest sessionpressure.Snapshot, hasLatest bool) pressureAuditCategory {
	pressureScanStarted := time.Now()
	storeAudit := scanPressureJSONL(runtime.dir, since, now)
	pressureScanDuration := time.Since(pressureScanStarted)
	cliScanStarted := time.Now()
	cliAudit, eventCounts, effective, cliCache := scanCLIOperationTelemetry(filepath.Join(runtime.dir, "cli-audit-cache-v1.json"), since, now)
	cliScanDuration := time.Since(cliScanStarted)
	writerReadStarted := time.Now()
	writer := telemetry.ReadCLIWriterHealthSince(since)
	recentWriter := telemetry.ReadCLIWriterHealthSince(now.Add(-2 * time.Hour))
	currentControllerWriter := telemetry.CLIWriterHealthReport{}
	currentControllerSince := time.Time{}
	currentControllerEvidence := false
	if writerSince, ok := pressureCurrentControllerWriterSince(now, launchd); ok {
		currentControllerSince = writerSince
		currentControllerWriter = telemetry.ReadCLIWriterHealthSince(currentControllerSince)
		currentControllerEvidence = currentControllerWriter.Known && currentControllerWriter.Attempted > 0
	}
	writerReadDuration := time.Since(writerReadStarted)
	eventReadStarted := time.Now()
	eventReadSince := pressureCurrentControllerReadSince(since, now, launchd)
	events, eventErr := runtime.store.ReadEvents(10_000, eventReadSince)
	eventReadDuration := time.Since(eventReadStarted)
	requestedEvents := pressureTelemetryEventsSince(events, since)
	residentStarts, residentStops, sampleErrors, cleanupErrors := 0, 0, 0, 0
	for _, event := range requestedEvents {
		switch event.Event {
		case "resident_started":
			residentStarts++
		case "resident_stopped":
			residentStops++
		case "sample_error":
			sampleErrors++
		case "resource_cleanup_error":
			cleanupErrors++
		}
	}
	currentResident := auditCurrentControllerResident(events, now, launchd)
	pressureCommandCount := eventCounts["session.pressure.command"] + eventCounts["session_pressure.command"]
	metrics := map[string]any{
		"sessionpressure_jsonl": storeAudit, "cli_jsonl": cliAudit, "cli_writer": writer, "cli_writer_recent_2h": recentWriter,
		"cli_writer_current_controller": currentControllerWriter, "cli_writer_current_controller_since": currentControllerSince,
		"cli_writer_current_controller_evidence": currentControllerEvidence,
		"pressure_jsonl_scan_ms":                 float64(pressureScanDuration.Microseconds()) / 1000,
		"cli_jsonl_scan_ms":                      float64(cliScanDuration.Microseconds()) / 1000,
		"cli_jsonl_cache":                        cliCache,
		"writer_health_read_ms":                  float64(writerReadDuration.Microseconds()) / 1000,
		"event_ledger_read_ms":                   float64(eventReadDuration.Microseconds()) / 1000,
		"session_pressure_commands":              pressureCommandCount, "toolguard_decisions": eventCounts["toolguard.decision"],
		"toolguard_effective": effective, "resident_starts": residentStarts, "resident_stops": residentStops,
		"resident_current_controller": currentResident,
		"sample_errors":               sampleErrors, "cleanup_errors": cleanupErrors,
	}
	findings := []pressureAuditFinding{}
	if storeAudit.ParseErrors+storeAudit.ValidationErrors+storeAudit.InsecureFiles > 0 {
		findings = append(findings, pressureFinding(pressureAuditFail, "pressure_telemetry_integrity", "SessionPressure JSONL has parse, validation, or file-permission failures"))
	}
	if cliAudit.ParseErrors+cliAudit.ValidationErrors+cliAudit.InsecureFiles > 0 {
		findings = append(findings, pressureFinding(pressureAuditFail, "cli_telemetry_integrity", "CLI JSONL has parse, privacy, or file-permission failures"))
	}
	if eventErr != nil {
		findings = append(findings, pressureFinding(pressureAuditFail, "pressure_telemetry_read", eventErr.Error()))
	}
	if !writer.Known {
		findings = append(findings, pressureFinding(pressureAuditWarn, "cli_writer_coverage_unknown", "the bounded CLI writer authority does not cover the requested window"))
	}
	if !recentWriter.Known {
		findings = append(findings, pressureFinding(pressureAuditWarn, "cli_writer_recent_coverage_unknown", "the bounded CLI writer authority does not cover the current two-hour health window"))
	} else if recentWriter.Failed > 0 || recentWriter.Gaps > 0 {
		findings = append(findings, auditCLIWriterDropFinding(recentWriter, currentControllerWriter, currentControllerEvidence))
	} else if writer.Known && (writer.Failed > 0 || writer.Gaps > 0) {
		findings = append(findings, pressureFinding(pressureAuditWarn, "cli_writer_historical_drops", fmt.Sprintf("requested history contains failed=%d gaps=%d while the current two-hour window is clean", writer.Failed, writer.Gaps)))
	}
	if pressureCommandCount == 0 {
		findings = append(findings, pressureFinding(pressureAuditWarn, "pressure_leaf_telemetry_unobserved", "no SessionPressure leaf events were observed in the requested window"))
	}
	if eventCounts["toolguard.decision"] == 0 {
		findings = append(findings, pressureFinding(pressureAuditWarn, "toolguard_telemetry_unobserved", "no Toolguard decisions were observed in the requested window"))
	}
	legacyToolguardDecisions := 0
	for decision, count := range effective {
		if decision != "allow" && decision != "ask" && decision != "deny" {
			legacyToolguardDecisions += count
		}
	}
	metrics["legacy_toolguard_decisions"] = legacyToolguardDecisions
	if legacyToolguardDecisions > 0 {
		findings = append(findings, pressureFinding(pressureAuditWarn, "legacy_toolguard_projection", fmt.Sprintf("%d historical decisions use an internal verdict instead of allow/ask/deny", legacyToolguardDecisions)))
	}
	if sampleErrors > 0 {
		findings = append(findings, pressureFinding(pressureAuditWarn, "sample_errors_observed", fmt.Sprintf("%d resident sample errors occurred in the window", sampleErrors)))
	}
	if cleanupErrors > 0 || (hasLatest && latest.ResourceCleanupStatus == "failing") {
		findings = append(findings, pressureFinding(pressureAuditWarn, "cleanup_errors_observed", fmt.Sprintf("%d cleanup errors occurred in the window", cleanupErrors)))
	}
	if residentStarts > 2 || residentStarts-residentStops > 1 {
		if currentResident.Known && currentResident.Stable {
			findings = append(findings, pressureFinding(pressureAuditWarn, "resident_prior_restart_churn", fmt.Sprintf(
				"requested history contains starts=%d stops=%d; the verified current controller is stable with starts=%d stops=%d",
				residentStarts, residentStops, currentResident.Starts, currentResident.Stops,
			)))
		} else {
			findings = append(findings, pressureFinding(pressureAuditWarn, "resident_restart_churn", fmt.Sprintf("starts=%d stops=%d", residentStarts, residentStops)))
		}
	}
	if hasLatest && latest.TelemetryProjectedBytesDay > runtime.policy.ResourceBudgets.MaxTelemetryBytesDay {
		findings = append(findings, pressureFinding(pressureAuditFail, "telemetry_budget_failed", "resident daily telemetry projection exceeds policy"))
	}
	return newPressureAuditCategory("telemetry", "durability, privacy, coverage, and resident lifecycle audit", metrics, findings...)
}

func pressureCurrentControllerWriterSince(now time.Time, launchd sessionpressure.LaunchdStatus) (time.Time, bool) {
	installedAt, ok := pressureVerifiedControllerInstalledAt(now, launchd)
	if !ok {
		return time.Time{}, false
	}
	since := installedAt.Truncate(time.Hour)
	if recentFloor := now.Add(-2 * time.Hour); since.Before(recentFloor) {
		since = recentFloor
	}
	return since, true
}

func auditCurrentControllerResident(events []sessionpressure.TelemetryEvent, now time.Time, launchd sessionpressure.LaunchdStatus) pressureCurrentControllerResident {
	report := pressureCurrentControllerResident{}
	installedAt, ok := pressureVerifiedControllerInstalledAt(now, launchd)
	if !ok {
		return report
	}
	activationAt := time.Time{}
	for _, event := range events {
		if event.Event == "resident_started" && !event.Timestamp.Before(installedAt) && (activationAt.IsZero() || event.Timestamp.Before(activationAt)) {
			activationAt = event.Timestamp
		}
	}
	if activationAt.IsZero() {
		return report
	}
	report.Known = true
	report.Since = activationAt.Format(time.RFC3339Nano)
	for _, event := range events {
		if event.Timestamp.Before(activationAt) {
			continue
		}
		switch event.Event {
		case "resident_started":
			report.Starts++
		case "resident_stopped":
			report.Stops++
		}
	}
	report.Stable = report.Starts == 1 && report.Stops == 0
	return report
}

func pressureTelemetryEventsSince(events []sessionpressure.TelemetryEvent, since time.Time) []sessionpressure.TelemetryEvent {
	if since.IsZero() {
		return events
	}
	filtered := make([]sessionpressure.TelemetryEvent, 0, len(events))
	for _, event := range events {
		if !event.Timestamp.Before(since) {
			filtered = append(filtered, event)
		}
	}
	return filtered
}

func auditCLIWriterDropFinding(recent, current telemetry.CLIWriterHealthReport, currentControllerEvidence bool) pressureAuditFinding {
	if currentControllerEvidence && current.Known && current.Attempted > 0 && current.Failed == 0 && current.Gaps == 0 {
		return pressureFinding(pressureAuditWarn, "cli_writer_prior_controller_drops", fmt.Sprintf(
			"two-hour history contains failed=%d gaps=%d before the current verified controller; its activation-hour authority is clean across %d attempts",
			recent.Failed, recent.Gaps, current.Attempted,
		))
	}
	return pressureFinding(pressureAuditFail, "cli_writer_recent_drops", fmt.Sprintf("current two-hour writer health failed=%d gaps=%d", recent.Failed, recent.Gaps))
}

func auditPressurePerformance(runtime pressureRuntime, artifact sessionpressure.InstalledArtifact, latest sessionpressure.Snapshot, hasLatest bool) pressureAuditCategory {
	evaluation := sessionpressure.EvaluateWorkSystem(runtime.policy)
	writer := telemetry.ReadCLIWriterHealthSince(time.Now().UTC().Add(-2 * time.Hour))
	metrics := map[string]any{
		"resident_binary_bytes": artifact.SizeBytes, "resident_binary_target_bytes": residentBinaryTargetBytes,
		"work_evaluation_ok": evaluation.OK, "selector_p95_us": evaluation.SelectorBenchmark.P95Microseconds,
		"evaluation_runtime_ms": evaluation.RuntimeMilliseconds,
		"cli_writer_known":      writer.Known, "cli_writer_latency_scope": writer.LatencyScope, "cli_writer_latency_samples": writer.LatencySamples,
		"cli_writer_latency_p95_us": writer.LatencyP95US, "cli_writer_latency_max_us": writer.LatencyMaxUS,
	}
	findings := auditCLIWriterPerformance(writer)
	if artifact.SizeBytes <= 0 {
		findings = append(findings, pressureFinding(pressureAuditFail, "resident_size_unknown", "installed resident binary size is unavailable"))
	} else if artifact.SizeBytes > residentBinaryTargetBytes {
		findings = append(findings, pressureFinding(pressureAuditFail, "resident_binary_oversize", fmt.Sprintf("%d bytes exceeds the 12MiB target", artifact.SizeBytes)))
	}
	if !evaluation.OK {
		findings = append(findings, pressureFinding(pressureAuditFail, "scheduler_evaluation_failed", strings.Join(evaluation.ReviewSignals, "; ")))
	}
	if hasLatest {
		metrics["guard_rss_max_mb"] = latest.GuardRSSMaxMB
		metrics["guard_idle_cpu_percent"] = latest.GuardIdleCPUDutyPercent
		metrics["sample_cpu_p95_ms"] = latest.SampleCPUTimeP95MS
		metrics["sample_wall_p95_ms"] = latest.SampleDurationP95MS
		metrics["telemetry_projected_bytes_day"] = latest.TelemetryProjectedBytesDay
		metrics["guard_budget_ok"] = latest.GuardBudgetOK
		if !latest.GuardBudgetOK {
			findings = append(findings, pressureFinding(pressureAuditFail, "resident_budget_failed", strings.Join(latest.GuardBudgetReasons, "; ")))
		}
		if latest.MonitorSamples < runtime.policy.SustainSamples {
			findings = append(findings, pressureFinding(pressureAuditWarn, "rolling_budget_warmup", "resident rolling performance evidence has not reached the sustain threshold"))
		}
	} else {
		findings = append(findings, pressureFinding(pressureAuditFail, "performance_evidence_missing", "latest resident performance evidence is unavailable"))
	}
	return newPressureAuditCategory("performance", "resident budgets, scheduler benchmarks, CLI writer latency, and wire cost", metrics, findings...)
}

func auditCLIWriterPerformance(writer telemetry.CLIWriterHealthReport) []pressureAuditFinding {
	if !writer.Known {
		return []pressureAuditFinding{pressureFinding(pressureAuditWarn, "cli_writer_latency_unknown", "CLI writer latency authority does not cover the last two hours")}
	}
	if writer.LatencySamples < pressureAuditWriterLatencyMinSamples {
		return []pressureAuditFinding{pressureFinding(pressureAuditWarn, "cli_writer_latency_warmup", fmt.Sprintf("%d/%d CLI writer latency samples collected", writer.LatencySamples, pressureAuditWriterLatencyMinSamples))}
	}
	if writer.LatencyP95US > pressureAuditWriterLatencyFailUS {
		return []pressureAuditFinding{pressureFinding(pressureAuditFail, "cli_writer_latency_failed", fmt.Sprintf("CLI writer p95 is %dus above the %dus ceiling", writer.LatencyP95US, pressureAuditWriterLatencyFailUS))}
	}
	if writer.LatencyP95US > pressureAuditWriterLatencyWarnUS {
		return []pressureAuditFinding{pressureFinding(pressureAuditWarn, "cli_writer_latency_slow", fmt.Sprintf("CLI writer p95 is %dus above the %dus target", writer.LatencyP95US, pressureAuditWriterLatencyWarnUS))}
	}
	return nil
}

func auditPressureScanRuntime(category pressureAuditCategory, duration time.Duration) pressureAuditCategory {
	if category.Metrics == nil {
		category.Metrics = map[string]any{}
	}
	category.Metrics["scan_runtime_ms"] = float64(duration.Microseconds()) / 1000
	findings := append([]pressureAuditFinding(nil), category.Findings...)
	if duration > pressureAuditScanFail {
		findings = append(findings, pressureFinding(pressureAuditFail, "telemetry_scan_runtime_failed", fmt.Sprintf("telemetry audit scan took %s above the %s ceiling", duration.Round(time.Millisecond), pressureAuditScanFail)))
	} else if duration > pressureAuditScanWarn {
		findings = append(findings, pressureFinding(pressureAuditWarn, "telemetry_scan_runtime_slow", fmt.Sprintf("telemetry audit scan took %s above the %s target", duration.Round(time.Millisecond), pressureAuditScanWarn)))
	}
	return newPressureAuditCategory(category.ID, category.Summary, category.Metrics, findings...)
}

func auditPressureCleanup(runtime pressureRuntime, latest sessionpressure.Snapshot, hasLatest bool) pressureAuditCategory {
	policy, persisted, policyErr := hostcleanup.LoadPolicy(runtime.dir)
	claims, claimsErr := hostcleanup.NewClaimStore(runtime.dir).List()
	actions, actionsErr := hostcleanup.ReadActions(runtime.dir, 100)
	claimSummary := summarizeCleanupClaims(claims)
	processOnly := policy.ProcessOnly()
	metrics := map[string]any{
		"persisted": persisted, "enabled": policy.Enabled, "enforce": policy.Enforce,
		"auto_process_only_graduation_scheduled":    policy.AutoGraduateProcessOnly,
		"auto_native_provider_graduation_scheduled": policy.AutoGraduateNative,
		"process_only": processOnly, "active_claims": claimSummary.Active, "stale_claims": claimSummary.Stale, "recent_actions": len(actions),
		"process_cleanup_opt_in_claims": claimSummary.ProcessOptIn,
		"active_process_cleanup_claims": claimSummary.ActiveProcessOptIn,
		"stale_process_cleanup_claims":  claimSummary.StaleProcessOptIn,
		"observation_started_at":        policy.ObservationStartedAt,
		"observation_remaining_seconds": policy.ObservationRemaining(time.Now()).Seconds(),
		"control_performance_available": hasLatest && !latest.ResourceCleanupExecutedAt.IsZero(),
	}
	if graduationAt := policy.ProcessOnlyGraduationAt(); !graduationAt.IsZero() {
		metrics["process_only_graduation_at"] = graduationAt
		metrics["browser_graduation_at"] = policy.BrowserGraduationAt()
		metrics["dev_session_graduation_at"] = policy.DevGraduationAt()
		metrics["docker_workspace_graduation_at"] = policy.DockerGraduationAt()
	}
	if hasLatest && !latest.ResourceCleanupExecutedAt.IsZero() {
		metrics["control_executed_at"] = latest.ResourceCleanupExecutedAt
		metrics["control_duration_ms"] = latest.ResourceCleanupDurationMS
		metrics["control_max_rss_mb"] = latest.ResourceCleanupMaxRSSMB
	}
	findings := []pressureAuditFinding{}
	if policyErr != nil {
		findings = append(findings, pressureFinding(pressureAuditFail, "cleanup_policy_invalid", policyErr.Error()))
	}
	if claimsErr != nil {
		findings = append(findings, pressureFinding(pressureAuditFail, "cleanup_claims_invalid", claimsErr.Error()))
	}
	if actionsErr != nil {
		findings = append(findings, pressureFinding(pressureAuditFail, "cleanup_actions_unreadable", actionsErr.Error()))
	}
	if !persisted {
		findings = append(findings, pressureFinding(pressureAuditWarn, "cleanup_policy_not_persisted", "cleanup remains at built-in observe defaults"))
	} else if !policy.Enabled {
		findings = append(findings, pressureFinding(pressureAuditWarn, "cleanup_disabled", "typed cleanup evaluation is disabled"))
	} else if !policy.Enforce && !policy.AutoGraduateProcessOnly {
		findings = append(findings, pressureFinding(pressureAuditWarn, "cleanup_observation", "cleanup is collecting plans and claims but not acting automatically"))
	}
	if policyErr == nil && policy.Enforce {
		if err := policy.ValidateEnforcement(time.Now().UTC()); err != nil {
			findings = append(findings, pressureFinding(pressureAuditFail, "cleanup_enforcement_unsafe", err.Error()))
		}
	}
	if policy.Enforce && processOnly && claimSummary.ProcessOptIn == 0 {
		findings = append(findings, pressureFinding(pressureAuditWarn, "process_cleanup_unarmed", "process-only cleanup has no explicit cleanup_on_stale process claims and therefore cannot reclaim a process"))
	}
	if claimSummary.Stale > 0 {
		findings = append(findings, pressureFinding(pressureAuditWarn, "stale_cleanup_claims", fmt.Sprintf("%d stale resource claims await review", claimSummary.Stale)))
	}
	if hasLatest && latest.ResourceCleanupError != "" {
		findings = append(findings, pressureFinding(pressureAuditFail, "cleanup_bridge_failing", latest.ResourceCleanupError))
	}
	findings = append(findings, auditCleanupBridgePerformance(policy.Enforce, latest, hasLatest)...)
	return newPressureAuditCategory("cleanup", "claim safety, observation gate, action ledger, and rollout scope", metrics, findings...)
}

func auditCleanupBridgePerformance(enforce bool, latest sessionpressure.Snapshot, hasLatest bool) []pressureAuditFinding {
	if !enforce {
		return nil
	}
	if !hasLatest || latest.ResourceCleanupExecutedAt.IsZero() {
		return []pressureAuditFinding{pressureFinding(pressureAuditWarn, "cleanup_bridge_performance_warmup", "enforced cleanup has no resident control-process performance sample yet")}
	}
	findings := []pressureAuditFinding{}
	if latest.ResourceCleanupDurationMS > pressureAuditCleanupDurationFailMS {
		findings = append(findings, pressureFinding(pressureAuditFail, "cleanup_bridge_latency_failed", fmt.Sprintf("cleanup control took %.1fms above the %.0fms ceiling", latest.ResourceCleanupDurationMS, pressureAuditCleanupDurationFailMS)))
	} else if latest.ResourceCleanupDurationMS > pressureAuditCleanupDurationWarnMS {
		findings = append(findings, pressureFinding(pressureAuditWarn, "cleanup_bridge_latency_slow", fmt.Sprintf("cleanup control took %.1fms above the %.0fms target", latest.ResourceCleanupDurationMS, pressureAuditCleanupDurationWarnMS)))
	}
	if latest.ResourceCleanupMaxRSSMB > pressureAuditCleanupRSSFailMB {
		findings = append(findings, pressureFinding(pressureAuditFail, "cleanup_bridge_rss_failed", fmt.Sprintf("cleanup control peaked at %.1fMiB above the %.0fMiB ceiling", latest.ResourceCleanupMaxRSSMB, pressureAuditCleanupRSSFailMB)))
	} else if latest.ResourceCleanupMaxRSSMB > pressureAuditCleanupRSSWarnMB {
		findings = append(findings, pressureFinding(pressureAuditWarn, "cleanup_bridge_rss_high", fmt.Sprintf("cleanup control peaked at %.1fMiB above the %.0fMiB target", latest.ResourceCleanupMaxRSSMB, pressureAuditCleanupRSSWarnMB)))
	}
	return findings
}

func auditPressureArtifacts(artifact sessionpressure.InstalledArtifact, retention sessionpressure.ArtifactPruneReport, artifactErr, retentionErr error) pressureAuditCategory {
	metrics := map[string]any{
		"active_sha256": artifact.SHA256, "active_path": artifact.Path, "version": artifact.Version,
		"size_bytes": artifact.SizeBytes, "revisions": len(retention.Entries), "rollback_count": retention.RollbackCount,
		"prune_count": retention.PruneCount, "reclaim_bytes": retention.ReclaimBytes,
	}
	findings := []pressureAuditFinding{}
	if artifactErr != nil {
		findings = append(findings, pressureFinding(pressureAuditFail, "artifact_verification_failed", artifactErr.Error()))
	} else if artifact.SHA256 == "" {
		findings = append(findings, pressureFinding(pressureAuditFail, "artifact_unverified", "no verified installed artifact is available"))
	}
	if retentionErr != nil {
		findings = append(findings, pressureFinding(pressureAuditFail, "artifact_retention_unreadable", retentionErr.Error()))
	}
	if retention.PruneCount > 0 {
		findings = append(findings, pressureFinding(pressureAuditWarn, "artifact_prune_available", fmt.Sprintf("%d verified revisions can be safely pruned", retention.PruneCount)))
	}
	for _, entry := range retention.Entries {
		if entry.SkipReason != "" {
			findings = append(findings, pressureFinding(pressureAuditWarn, "artifact_unmanaged_entry", entry.SkipReason))
		}
	}
	return newPressureAuditCategory("artifacts", "atomic publication provenance and active-plus-two rollback retention", metrics, findings...)
}

func scanPressureJSONL(dir string, since, now time.Time) pressureAuditJSONL {
	result := pressureAuditJSONL{}
	patterns := []string{"snapshots-*.jsonl", "actions-*.jsonl", "work-events-*.jsonl", "resource-cleanup-actions.jsonl"}
	type scanTarget struct {
		path string
		work bool
	}
	targets := []scanTarget{}
	for _, pattern := range patterns {
		paths, _ := filepath.Glob(filepath.Join(dir, pattern))
		for _, path := range paths {
			if !pressureTelemetryPathInWindow(path, since, now) {
				continue
			}
			targets = append(targets, scanTarget{path: path, work: strings.Contains(filepath.Base(path), "work-events-")})
		}
	}
	results := make(chan pressureAuditJSONL, len(targets))
	semaphore := make(chan struct{}, 4)
	for _, target := range targets {
		target := target
		go func() {
			semaphore <- struct{}{}
			defer func() { <-semaphore }()
			local := pressureAuditJSONL{}
			scanAuditJSONLFile(target.path, &local, func(line []byte) error {
				if target.work {
					var event sessionpressure.WorkEvent
					if err := json.Unmarshal(line, &event); err != nil {
						return err
					}
					return event.Validate()
				}
				var row map[string]any
				return json.Unmarshal(line, &row)
			})
			results <- local
		}()
	}
	for range targets {
		mergePressureAuditJSONL(&result, <-results)
	}
	return result
}

func pressureTelemetryPathInWindow(path string, since, now time.Time) bool {
	return pressureTelemetryPathInWindowAtLocation(path, since, now, time.Local)
}

func pressureTelemetryPathInWindowAtLocation(path string, since, now time.Time, location *time.Location) bool {
	base := filepath.Base(path)
	if base == "resource-cleanup-actions.jsonl" {
		return true
	}
	stem := strings.TrimSuffix(base, ".jsonl")
	separator := strings.LastIndexByte(stem, '-')
	if separator < 0 || len(stem)-separator-1 != len("20060102") {
		return false
	}
	if location == nil {
		return false
	}
	day, err := time.ParseInLocation("20060102", stem[separator+1:], location)
	if err != nil {
		return false
	}
	sinceLocal, nowLocal := since.In(location), now.Add(5*time.Minute).In(location)
	sinceDay := time.Date(sinceLocal.Year(), sinceLocal.Month(), sinceLocal.Day(), 0, 0, 0, 0, location)
	nowDay := time.Date(nowLocal.Year(), nowLocal.Month(), nowLocal.Day(), 0, 0, 0, 0, location)
	return !day.Before(sinceDay) && !day.After(nowDay)
}

func scanCLIOperationTelemetry(cachePath string, since, now time.Time) (pressureAuditJSONL, map[string]int, map[string]int, cliAuditCacheStats) {
	dir := filepath.Dir(telemetry.DefaultSourceTelemetryPathAt(telemetry.CLIInvocationSourceID, now))
	paths, _ := filepath.Glob(filepath.Join(dir, "events-*.jsonl"))
	windowPaths := make([]string, 0, len(paths))
	for _, path := range paths {
		if cliTelemetryPathInWindow(path, since, now) {
			windowPaths = append(windowPaths, path)
		}
	}
	return scanCLIAuditPaths(cachePath, windowPaths, since, now)
}

func scanCLIAuditPaths(cachePath string, paths []string, since, now time.Time) (pressureAuditJSONL, map[string]int, map[string]int, cliAuditCacheStats) {
	result := pressureAuditJSONL{}
	counts := map[string]int{}
	effective := map[string]int{}
	cache, cacheErr := readCLIAuditCache(cachePath)
	stats := cliAuditCacheStats{LoadError: cacheErr != nil}
	type cliScanResult struct {
		jsonl               pressureAuditJSONL
		counts              map[string]int
		effective           map[string]int
		cacheKey            string
		cache               *cliAuditCacheEntry
		cacheHit            bool
		incrementalHit      bool
		completeEventWindow bool
		firstEventUnixNano  int64
		lastEventUnixNano   int64
	}
	results := make(chan cliScanResult, len(paths))
	semaphore := make(chan struct{}, 4)
	for _, path := range paths {
		path := path
		go func() {
			semaphore <- struct{}{}
			defer func() { <-semaphore }()
			cacheKey := filepath.Base(path)
			cached, cachedOK := cache.Entries[cacheKey]
			if cachedOK && cliAuditCacheEntryCurrent(path, cached, since, now) {
				results <- cliScanResult{
					jsonl: cached.JSONL, counts: cached.Counts, effective: cached.Effective,
					cacheKey: cacheKey, cache: &cached, cacheHit: true,
				}
				return
			}
			local := cliScanResult{
				jsonl: pressureAuditJSONL{ValidationClasses: map[string]int{}}, counts: map[string]int{}, effective: map[string]int{},
				cacheKey: cacheKey, completeEventWindow: true,
			}
			before, beforeErr := auditFileSignatureAt(path)
			scanLine := func(line []byte) error {
				var event telemetry.Event
				if err := json.Unmarshal(line, &event); err != nil {
					return err
				}
				at, err := time.Parse(time.RFC3339Nano, event.TS)
				if err != nil {
					return err
				}
				eventUnixNano := at.UnixNano()
				if local.firstEventUnixNano == 0 || eventUnixNano < local.firstEventUnixNano {
					local.firstEventUnixNano = eventUnixNano
				}
				if eventUnixNano > local.lastEventUnixNano {
					local.lastEventUnixNano = eventUnixNano
				}
				if at.Before(since) || at.After(now.Add(5*time.Minute)) {
					local.completeEventWindow = false
					return nil
				}
				if violations := telemetry.ValidateEventPrivacy(event); len(violations) > 0 {
					for _, violation := range violations {
						local.jsonl.ValidationClasses[violation.Path+":"+violation.Kind]++
					}
					return fmt.Errorf("privacy validation failed")
				}
				local.counts[event.Event]++
				if event.Event == "toolguard.decision" {
					if decision, _ := event.Attrs["decision"].(string); decision != "" {
						local.effective[decision]++
					}
				}
				return nil
			}
			if beforeErr == nil && cachedOK && cliAuditCacheEntryAppendable(path, cached, before, since, now) {
				local.jsonl = clonePressureAuditJSONL(cached.JSONL)
				local.counts = cloneAuditCounts(cached.Counts)
				local.effective = cloneAuditCounts(cached.Effective)
				local.cacheHit = true
				local.incrementalHit = true
				local.completeEventWindow = cached.CompleteEventWindow
				local.firstEventUnixNano = cached.FirstEventUnixNano
				local.lastEventUnixNano = cached.LastEventUnixNano
				scanAuditJSONLFileFromOffset(path, cached.SizeBytes, &local.jsonl, scanLine)
			} else {
				scanAuditJSONLFile(path, &local.jsonl, scanLine)
			}
			if beforeErr == nil && local.jsonl.InsecureFiles == 0 {
				if digest, stable := digestStableAuditFile(path, before); stable {
					entry := cliAuditCacheEntry{
						SizeBytes: before.SizeBytes, ModUnixNano: before.ModUnixNano, Mode: before.Mode, ContentDigest: digest,
						CompleteEventWindow: local.completeEventWindow, FirstEventUnixNano: local.firstEventUnixNano, LastEventUnixNano: local.lastEventUnixNano,
						JSONL: local.jsonl, Counts: local.counts, Effective: local.effective,
					}
					local.cache = &entry
				}
			}
			results <- local
		}()
	}
	nextCache := cliAuditCache{SchemaVersion: cliAuditCacheSchemaVersion, Entries: map[string]cliAuditCacheEntry{}}
	for range paths {
		local := <-results
		if local.cacheHit {
			stats.Hits++
			if local.incrementalHit {
				stats.IncrementalHits++
			}
		} else {
			stats.Misses++
		}
		if local.cache != nil {
			nextCache.Entries[local.cacheKey] = *local.cache
		}
		mergePressureAuditJSONL(&result, local.jsonl)
		for key, count := range local.counts {
			counts[key] += count
		}
		for key, count := range local.effective {
			effective[key] += count
		}
	}
	if err := writeCLIAuditCache(cachePath, nextCache); err != nil {
		stats.WriteError = true
	}
	return result, counts, effective, stats
}

func readCLIAuditCache(path string) (cliAuditCache, error) {
	empty := cliAuditCache{SchemaVersion: cliAuditCacheSchemaVersion, Entries: map[string]cliAuditCacheEntry{}}
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return empty, nil
	}
	if err != nil {
		return empty, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 || info.Size() > cliAuditCacheMaximumBytes {
		return empty, fmt.Errorf("CLI audit cache is not a bounded private regular file")
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return empty, err
	}
	var cache cliAuditCache
	if err := json.Unmarshal(body, &cache); err != nil || cache.SchemaVersion != cliAuditCacheSchemaVersion || cache.Entries == nil {
		return empty, fmt.Errorf("CLI audit cache is invalid")
	}
	return cache, nil
}

func writeCLIAuditCache(path string, cache cliAuditCache) error {
	body, err := json.Marshal(cache)
	if err != nil {
		return err
	}
	if len(body) > cliAuditCacheMaximumBytes {
		return fmt.Errorf("CLI audit cache exceeds %d bytes", cliAuditCacheMaximumBytes)
	}
	return atomicfile.WriteFile(path, append(body, '\n'), 0o600)
}

func auditFileSignatureAt(path string) (auditFileSignature, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return auditFileSignature{}, err
	}
	return auditFileSignature{SizeBytes: info.Size(), ModUnixNano: info.ModTime().UnixNano(), Mode: uint32(info.Mode())}, nil
}

func cliAuditCacheEntryCurrent(path string, entry cliAuditCacheEntry, since, now time.Time) bool {
	// Counts and privacy results are window-filtered. Reuse is exact only when
	// the cached scan included every timestamped event in the shard and the
	// current moving window still contains that entire event-time range.
	if !entry.CompleteEventWindow ||
		(entry.FirstEventUnixNano != 0 && entry.FirstEventUnixNano < since.UnixNano()) ||
		(entry.LastEventUnixNano != 0 && entry.LastEventUnixNano > now.Add(5*time.Minute).UnixNano()) {
		return false
	}
	signature, err := auditFileSignatureAt(path)
	if err != nil || signature.SizeBytes != entry.SizeBytes || signature.ModUnixNano != entry.ModUnixNano || signature.Mode != entry.Mode {
		return false
	}
	digest, stable := digestStableAuditFile(path, signature)
	return stable && digest == entry.ContentDigest
}

func cliAuditCacheEntryAppendable(path string, entry cliAuditCacheEntry, current auditFileSignature, since, now time.Time) bool {
	// An append-only shard can reuse its already-validated prefix when every
	// cached event remains inside the moving window. Prefix verification keeps
	// truncation, replacement, and in-place mutation fail closed; only the new
	// suffix needs JSON decoding and privacy validation.
	if !entry.CompleteEventWindow || entry.SizeBytes < 0 || current.SizeBytes < entry.SizeBytes ||
		current.Mode != entry.Mode || current.Mode&uint32(os.ModeSymlink) != 0 ||
		(entry.FirstEventUnixNano != 0 && entry.FirstEventUnixNano < since.UnixNano()) ||
		(entry.LastEventUnixNano != 0 && entry.LastEventUnixNano > now.Add(5*time.Minute).UnixNano()) {
		return false
	}
	if entry.SizeBytes > 0 && !auditFilePrefixEndsWithNewline(path, entry.SizeBytes) {
		return false
	}
	return digestAuditFilePrefix(path, entry.SizeBytes) == entry.ContentDigest
}

func auditFilePrefixEndsWithNewline(path string, size int64) bool {
	file, err := os.Open(path)
	if err != nil {
		return false
	}
	defer file.Close()
	last := []byte{0}
	read, err := file.ReadAt(last, size-1)
	return err == nil && read == 1 && last[0] == '\n'
}

func digestAuditFilePrefix(path string, size int64) string {
	if size < 0 {
		return ""
	}
	file, err := os.Open(path)
	if err != nil {
		return ""
	}
	digest := sha256.New()
	written, copyErr := io.CopyN(digest, file, size)
	closeErr := file.Close()
	if copyErr != nil || closeErr != nil || written != size {
		return ""
	}
	return "sha256:" + hex.EncodeToString(digest.Sum(nil))
}

func digestStableAuditFile(path string, expected auditFileSignature) (string, bool) {
	file, err := os.Open(path)
	if err != nil {
		return "", false
	}
	digest := sha256.New()
	_, copyErr := io.Copy(digest, file)
	closeErr := file.Close()
	after, statErr := auditFileSignatureAt(path)
	if copyErr != nil || closeErr != nil || statErr != nil || after != expected {
		return "", false
	}
	return "sha256:" + hex.EncodeToString(digest.Sum(nil)), true
}

func mergePressureAuditJSONL(target *pressureAuditJSONL, added pressureAuditJSONL) {
	if target == nil {
		return
	}
	target.Files += added.Files
	target.Rows += added.Rows
	target.ParseErrors += added.ParseErrors
	target.ValidationErrors += added.ValidationErrors
	target.InsecureFiles += added.InsecureFiles
	if len(added.ValidationClasses) > 0 {
		if target.ValidationClasses == nil {
			target.ValidationClasses = map[string]int{}
		}
		for class, count := range added.ValidationClasses {
			target.ValidationClasses[class] += count
		}
	}
}

func clonePressureAuditJSONL(value pressureAuditJSONL) pressureAuditJSONL {
	value.ValidationClasses = cloneAuditCounts(value.ValidationClasses)
	return value
}

func cloneAuditCounts(values map[string]int) map[string]int {
	cloned := make(map[string]int, len(values))
	for key, value := range values {
		cloned[key] = value
	}
	return cloned
}

func cliTelemetryPathInWindow(path string, since, now time.Time) bool {
	base := filepath.Base(path)
	stem := strings.TrimPrefix(base, "events-")
	if stem == base || len(stem) < len("2006-01-02") {
		return false
	}
	day, err := time.Parse("2006-01-02", stem[:len("2006-01-02")])
	if err != nil {
		return false
	}
	sinceUTC, nowUTC := since.UTC(), now.UTC()
	sinceDay := time.Date(sinceUTC.Year(), sinceUTC.Month(), sinceUTC.Day(), 0, 0, 0, 0, time.UTC)
	nowDay := time.Date(nowUTC.Year(), nowUTC.Month(), nowUTC.Day(), 0, 0, 0, 0, time.UTC)
	return !day.Before(sinceDay) && !day.After(nowDay)
}

func scanAuditJSONLFile(path string, result *pressureAuditJSONL, validate func([]byte) error) {
	if result == nil {
		return
	}
	info, err := os.Lstat(path)
	if err != nil {
		result.ParseErrors++
		return
	}
	result.Files++
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		result.InsecureFiles++
	}
	file, err := os.Open(path)
	if err != nil {
		result.ParseErrors++
		return
	}
	defer file.Close()
	scanAuditJSONLReader(file, result, validate)
}

func scanAuditJSONLFileFromOffset(path string, offset int64, result *pressureAuditJSONL, validate func([]byte) error) {
	if result == nil || offset < 0 {
		return
	}
	file, err := os.Open(path)
	if err != nil {
		result.ParseErrors++
		return
	}
	defer file.Close()
	if _, err := file.Seek(offset, io.SeekStart); err != nil {
		result.ParseErrors++
		return
	}
	scanAuditJSONLReader(file, result, validate)
}

func scanAuditJSONLReader(file io.Reader, result *pressureAuditJSONL, validate func([]byte) error) {
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 4096), 2<<20)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		result.Rows++
		if err := validate(line); err != nil {
			if json.Valid(line) {
				result.ValidationErrors++
			} else {
				result.ParseErrors++
			}
		}
	}
	if scanner.Err() != nil {
		result.ParseErrors++
	}
}
