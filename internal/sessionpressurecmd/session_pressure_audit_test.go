package sessionpressurecmd

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nstranquist/session-pressure/internal/hostcleanup"
	"github.com/nstranquist/session-pressure/internal/sessionpressure"
	"github.com/nstranquist/session-pressure/internal/telemetry"
)

func TestPressureAuditCategoryUsesWorstFinding(t *testing.T) {
	warn := newPressureAuditCategory("fixture", "fixture", nil,
		pressureFinding(pressureAuditWarn, "warmup", "still collecting evidence"))
	if warn.Status != pressureAuditWarn {
		t.Fatalf("warning category status = %q", warn.Status)
	}
	fail := newPressureAuditCategory("fixture", "fixture", nil,
		pressureFinding(pressureAuditWarn, "warmup", "still collecting evidence"),
		pressureFinding(pressureAuditFail, "corrupt", "state is invalid"))
	if fail.Status != pressureAuditFail {
		t.Fatalf("failed category status = %q", fail.Status)
	}
}

func TestPressureAuditGatesCLIWriterLatency(t *testing.T) {
	warmup := auditCLIWriterPerformance(telemetry.CLIWriterHealthReport{Known: true, LatencySamples: 19, LatencyP95US: 200_000})
	if len(warmup) != 1 || warmup[0].Code != "cli_writer_latency_warmup" {
		t.Fatalf("writer warmup findings=%+v", warmup)
	}
	failed := auditCLIWriterPerformance(telemetry.CLIWriterHealthReport{Known: true, LatencySamples: 20, LatencyP95US: 100_001})
	if len(failed) != 1 || failed[0].Severity != pressureAuditFail || failed[0].Code != "cli_writer_latency_failed" {
		t.Fatalf("writer failed findings=%+v", failed)
	}
	if findings := auditCLIWriterPerformance(telemetry.CLIWriterHealthReport{Known: true, LatencySamples: 20, LatencyP95US: 25_000}); len(findings) != 0 {
		t.Fatalf("writer target findings=%+v", findings)
	}
}

func TestPressureAuditSeparatesRepairedControllerWriterDrops(t *testing.T) {
	recent := telemetry.CLIWriterHealthReport{Known: true, Attempted: 100, Written: 97, Failed: 3}
	current := telemetry.CLIWriterHealthReport{Known: true, Attempted: 12, Written: 12}
	finding := auditCLIWriterDropFinding(recent, current, true)
	if finding.Severity != pressureAuditWarn || finding.Code != "cli_writer_prior_controller_drops" {
		t.Fatalf("repaired controller finding=%+v", finding)
	}
	current.Failed = 1
	current.Written = 11
	finding = auditCLIWriterDropFinding(recent, current, true)
	if finding.Severity != pressureAuditFail || finding.Code != "cli_writer_recent_drops" {
		t.Fatalf("current controller drop finding=%+v", finding)
	}
}

func TestPressureAuditSeparatesStableCurrentResidentFromPriorRestartChurn(t *testing.T) {
	now := time.Date(2026, 7, 23, 0, 0, 0, 0, time.UTC)
	installedAt := now.Add(-10 * time.Minute)
	events := []sessionpressure.TelemetryEvent{
		{Timestamp: installedAt.Add(-time.Hour), Event: "resident_started"},
		{Timestamp: installedAt.Add(time.Second), Event: "resident_stopped"},
		{Timestamp: installedAt.Add(2 * time.Second), Event: "resident_started"},
	}
	launchd := sessionpressure.LaunchdStatus{
		ArtifactVerified: true, ControlBinaryVerified: true,
		ArtifactInstalledAt: installedAt.Format(time.RFC3339Nano),
	}
	report := auditCurrentControllerResident(events, now, launchd)
	if !report.Known || !report.Stable || report.Starts != 1 || report.Stops != 0 || report.Since != installedAt.Add(2*time.Second).Format(time.RFC3339Nano) {
		t.Fatalf("stable current resident report=%+v", report)
	}
	events = append(events, sessionpressure.TelemetryEvent{Timestamp: installedAt.Add(3 * time.Second), Event: "resident_stopped"})
	report = auditCurrentControllerResident(events, now, launchd)
	if report.Stable || report.Stops != 1 {
		t.Fatalf("current resident stop was hidden: %+v", report)
	}
}

func TestPressureAuditGatesAuditScanAndCleanupBridgeCost(t *testing.T) {
	category := auditPressureScanRuntime(newPressureAuditCategory("telemetry", "fixture", nil), pressureAuditScanFail+time.Second)
	if category.Status != pressureAuditFail || !auditHasFinding(category, "telemetry_scan_runtime_failed") {
		t.Fatalf("scan runtime category=%+v", category)
	}
	latest := sessionpressure.Snapshot{
		ResourceCleanupExecutedAt: time.Now().UTC(),
		ResourceCleanupDurationMS: pressureAuditCleanupDurationFailMS + 1,
		ResourceCleanupMaxRSSMB:   pressureAuditCleanupRSSFailMB + 1,
	}
	findings := auditCleanupBridgePerformance(true, latest, true)
	if len(findings) != 2 || findings[0].Severity != pressureAuditFail || findings[1].Severity != pressureAuditFail {
		t.Fatalf("cleanup bridge findings=%+v", findings)
	}
}

func TestPressureAuditSeparatesHistoricalAndRecentSlowdown(t *testing.T) {
	requested := sessionpressure.WorkStats{ServiceLevel: sessionpressure.WorkServiceLevel{
		Status: "breached", TargetP95BoundedSlowdown: sessionpressure.WorkBoundedSlowdownP95Target,
	}}
	recent := sessionpressure.WorkStats{CalibrationCohorts: []sessionpressure.WorkCalibrationCohort{{
		Class: sessionpressure.WorkClassBuild, Current: true, Status: "deferred", TerminalRuntimeSamples: 6, P95BoundedSlowdown: 1.1,
	}}}
	evidence := auditPressureSlowdown(requested, recent)
	if evidence.Breaches != 0 || evidence.Deferred != 1 || len(evidence.Findings) != 2 ||
		evidence.Findings[0].Code != "slowdown_evidence_insufficient" || evidence.Findings[1].Code != "historical_slowdown_breach" {
		t.Fatalf("historical/recent evidence = %+v", evidence)
	}
	recent.CalibrationCohorts[0].Status = "breached"
	recent.CalibrationCohorts[0].TerminalRuntimeSamples = 20
	recent.CalibrationCohorts[0].P95BoundedSlowdown = 5.1
	evidence = auditPressureSlowdown(requested, recent)
	if evidence.Breaches != 1 || len(evidence.Findings) != 1 || evidence.Findings[0].Severity != pressureAuditFail || evidence.Findings[0].Code != "slowdown_slo_failed" {
		t.Fatalf("recent breach evidence = %+v", evidence)
	}
}

func TestPressureAuditReportsCurrentControllerSchedulerWithoutMaskingHistory(t *testing.T) {
	now := time.Date(2026, 7, 22, 23, 0, 0, 0, time.UTC)
	installedAt := now.Add(-10 * time.Minute)
	beforeOperation := "00000000000000000000000000000001"
	currentOperation := "00000000000000000000000000000002"
	boundaryOperation := "00000000000000000000000000000003"
	exit := 0
	events := []sessionpressure.WorkEvent{
		{SchemaVersion: sessionpressure.WorkEventSchemaVersion, Timestamp: installedAt.Add(-time.Minute), OperationID: beforeOperation, Event: sessionpressure.WorkEventQueued, Class: sessionpressure.WorkClassBuild, Weight: 5},
		{SchemaVersion: sessionpressure.WorkEventSchemaVersion, Timestamp: installedAt.Add(-30 * time.Second), OperationID: beforeOperation, Event: sessionpressure.WorkEventCompleted, Class: sessionpressure.WorkClassBuild, Weight: 5, RuntimeMillis: 100, ExitCode: &exit},
		{SchemaVersion: sessionpressure.WorkEventSchemaVersion, Timestamp: installedAt.Add(-time.Second), OperationID: boundaryOperation, Event: sessionpressure.WorkEventQueued, Class: sessionpressure.WorkClassTest, Weight: 3},
		{SchemaVersion: sessionpressure.WorkEventSchemaVersion, Timestamp: installedAt.Add(30 * time.Second), OperationID: boundaryOperation, Event: sessionpressure.WorkEventCompleted, Class: sessionpressure.WorkClassTest, Weight: 3, RuntimeMillis: 100, ExitCode: &exit},
		{SchemaVersion: sessionpressure.WorkEventSchemaVersion, Timestamp: installedAt.Add(time.Minute), OperationID: currentOperation, Event: sessionpressure.WorkEventQueued, Class: sessionpressure.WorkClassExpressBuild, RequestedClass: sessionpressure.WorkClassBuild, Weight: 2},
		{SchemaVersion: sessionpressure.WorkEventSchemaVersion, Timestamp: installedAt.Add(2 * time.Minute), OperationID: currentOperation, Event: sessionpressure.WorkEventCompleted, Class: sessionpressure.WorkClassExpressBuild, Weight: 2, WaitMilliseconds: 100, RuntimeMillis: 1000, ExitCode: &exit},
	}
	launchd := sessionpressure.LaunchdStatus{
		ArtifactVerified: true, ControlBinaryVerified: true,
		ArtifactInstalledAt: installedAt.Format(time.RFC3339Nano),
	}
	report := auditCurrentControllerScheduler(events, now, launchd)
	if !report.Known || !report.WorkObserved || report.Events != 2 || report.Operations != 1 || report.OpenOperations != 0 ||
		report.ClassReclassifications != 1 || report.FullToExpressAdjustments != 1 || report.ExpressToFullAdjustments != 0 ||
		report.Since != installedAt.Format(time.RFC3339Nano) || report.PressureConditioned == nil || len(report.CalibrationCohorts) != 1 || report.CalibrationCohorts[0].Class != sessionpressure.WorkClassExpressBuild {
		t.Fatalf("current controller scheduler report=%+v", report)
	}
	if readSince := pressureCurrentControllerReadSince(now.Add(-time.Minute), now, launchd); !readSince.Equal(installedAt) {
		t.Fatalf("current controller read since=%s want installation=%s", readSince, installedAt)
	}
	if unknown := auditCurrentControllerScheduler(events, now, sessionpressure.LaunchdStatus{}); unknown.Known {
		t.Fatalf("unverified controller unexpectedly known: %+v", unknown)
	}
}

func TestPressureAuditKeepsRequestedAndControllerWindowsSeparate(t *testing.T) {
	now := time.Date(2026, 7, 22, 23, 0, 0, 0, time.UTC)
	installedAt := now.Add(-time.Hour)
	requestedSince := now.Add(-10 * time.Minute)
	launchd := sessionpressure.LaunchdStatus{
		ArtifactVerified: true, ControlBinaryVerified: true,
		ArtifactInstalledAt: installedAt.Format(time.RFC3339Nano),
	}
	workEvents := []sessionpressure.WorkEvent{
		{Timestamp: installedAt.Add(time.Minute), OperationID: "old", Event: sessionpressure.WorkEventQueued},
		{Timestamp: requestedSince.Add(time.Minute), OperationID: "current", Event: sessionpressure.WorkEventQueued},
	}
	requestedWork := pressureWorkEventsSince(workEvents, requestedSince)
	if len(requestedWork) != 1 || requestedWork[0].OperationID != "current" {
		t.Fatalf("requested work window=%+v", requestedWork)
	}
	telemetryEvents := []sessionpressure.TelemetryEvent{
		{Timestamp: installedAt.Add(time.Second), Event: "resident_started"},
		{Timestamp: requestedSince.Add(time.Second), Event: "heartbeat"},
	}
	requestedTelemetry := pressureTelemetryEventsSince(telemetryEvents, requestedSince)
	if len(requestedTelemetry) != 1 || requestedTelemetry[0].Event != "heartbeat" {
		t.Fatalf("requested telemetry window=%+v", requestedTelemetry)
	}
	resident := auditCurrentControllerResident(telemetryEvents, now, launchd)
	if !resident.Known || !resident.Stable || resident.Starts != 1 || resident.Since != installedAt.Add(time.Second).Format(time.RFC3339Nano) {
		t.Fatalf("controller resident window=%+v", resident)
	}
	writerSince, ok := pressureCurrentControllerWriterSince(now, launchd)
	if !ok || !writerSince.Equal(installedAt.Truncate(time.Hour)) {
		t.Fatalf("fresh controller writer since=%s ok=%t", writerSince, ok)
	}
	launchd.ArtifactInstalledAt = now.Add(-24 * time.Hour).Format(time.RFC3339Nano)
	writerSince, ok = pressureCurrentControllerWriterSince(now, launchd)
	if !ok || !writerSince.Equal(now.Add(-2*time.Hour)) {
		t.Fatalf("mature controller writer since=%s ok=%t", writerSince, ok)
	}
}

func TestPressureAuditConditionedEvidenceNeverMasksEndToEndFailure(t *testing.T) {
	conditioned := sessionpressure.WorkPressureConditionedServiceLevel{
		Status: "met", TargetP95BoundedSlowdown: sessionpressure.WorkBoundedSlowdownP95Target,
		PressureAffectedSamples: 20, ExcludedWaitMS: 120_000,
	}
	findings := auditPressureConditionedSlowdown(conditioned, true)
	if len(findings) != 1 || findings[0].Severity != pressureAuditWarn || findings[0].Code != "host_pressure_slowdown_context" {
		t.Fatalf("conditioned met context=%+v", findings)
	}

	conditioned.Status = "breached"
	conditioned.Breaches = []sessionpressure.WorkClassSLOBreach{{Class: sessionpressure.WorkClassBuild, ObservedP95: 6.2}}
	conditioned.WindowBoundarySamples = 2
	conditioned.LegacySchemaSamples = 1
	conditioned.InvalidDecompositionSamples = 1
	findings = auditPressureConditionedSlowdown(conditioned, true)
	if len(findings) != 3 || findings[0].Code != "pressure_conditioned_evidence_invalid" || findings[0].Severity != pressureAuditFail ||
		findings[1].Code != "pressure_conditioned_evidence_partial" || findings[1].Severity != pressureAuditWarn ||
		findings[2].Code != "pressure_conditioned_slo_failed" || findings[2].Severity != pressureAuditFail {
		t.Fatalf("conditioned breached evidence=%+v", findings)
	}
}

func TestPressureAuditArtifactsPreservesVerificationErrors(t *testing.T) {
	category := auditPressureArtifacts(
		sessionpressure.InstalledArtifact{}, sessionpressure.ArtifactPruneReport{},
		errors.New("verify fixture"), errors.New("retention fixture"),
	)
	if category.Status != pressureAuditFail || !auditHasFinding(category, "artifact_verification_failed") || !auditHasFinding(category, "artifact_retention_unreadable") {
		t.Fatalf("artifact error category=%+v", category)
	}
}

func TestScanPressureJSONLReportsParseValidationAndModeFailures(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	good := `{"schema_version":1,"timestamp":"` + now.Format(time.RFC3339Nano) + `","event":"heartbeat"}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, "snapshots-20260721.jsonl"), []byte(good), 0o600); err != nil {
		t.Fatal(err)
	}
	invalidWork := `{"schema_version":4,"timestamp":"` + now.Format(time.RFC3339Nano) + `","event":"surprise"}` + "\n"
	workPath := filepath.Join(dir, "work-events-20260721.jsonl")
	if err := os.WriteFile(workPath, []byte(invalidWork), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(workPath, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "actions-20260721.jsonl"), []byte("not-json\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "work-events-20260719.jsonl"), []byte("historical-invalid\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	report := scanPressureJSONL(dir, now.Add(-24*time.Hour), now)
	if report.Files != 3 || report.Rows != 3 || report.ParseErrors != 1 || report.ValidationErrors != 1 || report.InsecureFiles != 1 {
		t.Fatalf("JSONL audit = %+v", report)
	}
}

func TestPressureTelemetryPathInWindowUsesCompactLocalDay(t *testing.T) {
	since := time.Date(2026, 7, 20, 18, 0, 0, 0, time.UTC)
	now := time.Date(2026, 7, 21, 18, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		path string
		want bool
	}{
		{"snapshots-20260720.jsonl", true},
		{"work-events-20260721.jsonl", true},
		{"actions-20260719.jsonl", false},
		{"work-events-20260722.jsonl", false},
		{"resource-cleanup-actions.jsonl", true},
		{"invalid.jsonl", false},
	} {
		if got := pressureTelemetryPathInWindow(test.path, since, now); got != test.want {
			t.Errorf("pressureTelemetryPathInWindow(%q)=%t want %t", test.path, got, test.want)
		}
	}
}

func TestPressureTelemetryPathInWindowPreservesLocalShardAcrossUTCMidnight(t *testing.T) {
	chicago := time.FixedZone("America/Chicago-test", -5*60*60)
	since := time.Date(2026, 7, 23, 0, 12, 0, 0, time.UTC)
	now := time.Date(2026, 7, 23, 0, 22, 0, 0, time.UTC)
	if !pressureTelemetryPathInWindowAtLocation("snapshots-20260722.jsonl", since, now, chicago) {
		t.Fatal("local July 22 shard was hidden after UTC midnight")
	}
	if pressureTelemetryPathInWindowAtLocation("snapshots-20260723.jsonl", since, now, chicago) {
		t.Fatal("future local July 23 shard unexpectedly selected")
	}

	// A window spanning local midnight must retain both adjacent shard days.
	since = time.Date(2026, 7, 23, 4, 59, 0, 0, time.UTC)
	now = time.Date(2026, 7, 23, 5, 1, 0, 0, time.UTC)
	for _, path := range []string{"snapshots-20260722.jsonl", "snapshots-20260723.jsonl"} {
		if !pressureTelemetryPathInWindowAtLocation(path, since, now, chicago) {
			t.Fatalf("local-midnight window omitted %s", path)
		}
	}
}

func TestPressureAuditReadingsRejectShortCPUWindow(t *testing.T) {
	snapshot := sessionpressure.Snapshot{
		PhysicalMemoryMB: 16 << 10, FreePercent: 40,
		HostCPUAvailable: true, HostCPUSource: "host_processor_ticks", HostCPULiveWindowMS: 10,
		ProcessInventoryAvailable: true, ProcessInventoryFresh: true,
		Storage: sessionpressure.StorageSnapshot{Available: true},
	}
	category := auditPressureReadings(time.Now().UTC(), sessionpressure.DefaultPolicy(16<<10), snapshot, true, nil, sessionpressure.Snapshot{}, false, true)
	if category.Status != pressureAuditFail {
		t.Fatalf("short CPU window category = %+v", category)
	}
	found := false
	for _, finding := range category.Findings {
		if finding.Code == "cpu_window_too_short" {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing CPU window finding: %+v", category.Findings)
	}
}

func TestPressureAuditReadingsSurfacesStorageLevel(t *testing.T) {
	base := sessionpressure.Snapshot{
		PhysicalMemoryMB: 16 << 10, FreePercent: 40,
		HostCPUAvailable: true, HostCPUSource: "host_processor_ticks", HostCPULiveWindowMS: 250,
		HostCPURollingAvailable: true, ProcessInventoryAvailable: true, ProcessInventoryFresh: true,
	}
	tests := []struct {
		name       string
		level      sessionpressure.Level
		wantStatus string
		wantCode   string
	}{
		{name: "normal", level: sessionpressure.LevelNormal, wantStatus: pressureAuditPass},
		{name: "warning", level: sessionpressure.LevelWarning, wantStatus: pressureAuditWarn, wantCode: "storage_warning"},
		{name: "red", level: sessionpressure.LevelRed, wantStatus: pressureAuditFail, wantCode: "storage_pressure"},
		{name: "critical", level: sessionpressure.LevelCritical, wantStatus: pressureAuditFail, wantCode: "storage_pressure"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			snapshot := base
			snapshot.Storage = sessionpressure.StorageSnapshot{
				Available: true, Level: test.level, FreeBytes: 12 << 30,
				AvailableBytes: 10 << 30, FreePercent: 7.5, Reasons: []string{"fixture storage reason"},
			}
			category := auditPressureReadings(time.Now().UTC(), sessionpressure.DefaultPolicy(16<<10), snapshot, true, nil, sessionpressure.Snapshot{}, false, true)
			if category.Status != test.wantStatus {
				t.Fatalf("storage %s category = %+v", test.level, category)
			}
			if test.wantCode != "" && !auditHasFinding(category, test.wantCode) {
				t.Fatalf("storage %s missing %q: %+v", test.level, test.wantCode, category.Findings)
			}
			if got := category.Metrics["storage_level"]; got != test.level {
				t.Fatalf("storage_level = %#v want %q", got, test.level)
			}
		})
	}
}

func TestPressureAuditReadingsAcceptsCurrentCachedInventoryAndRejectsStaleInventory(t *testing.T) {
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	policy := sessionpressure.DefaultPolicy(16 << 10)
	base := sessionpressure.Snapshot{
		PhysicalMemoryMB: 16 << 10, FreePercent: 40,
		HostCPUAvailable: true, HostCPUSource: "host_processor_ticks", HostCPULiveWindowMS: 90_000,
		HostCPURollingAvailable: true, ProcessInventoryAvailable: true, ProcessInventoryFresh: false,
		Storage: sessionpressure.StorageSnapshot{Available: true},
	}
	current := base
	current.ProcessInventoryCapturedAt = now.Add(-time.Duration(policy.ProcessInventoryIntervalSeconds) * time.Second)
	if category := auditPressureReadings(now, policy, current, true, nil, sessionpressure.Snapshot{}, false, false); category.Status != pressureAuditPass {
		t.Fatalf("current cached inventory category = %+v", category)
	}
	stale := base
	stale.ProcessInventoryCapturedAt = now.Add(-time.Duration(policy.ProcessInventoryIntervalSeconds+policy.SampleIntervalSeconds+16) * time.Second)
	category := auditPressureReadings(now, policy, stale, true, nil, sessionpressure.Snapshot{}, false, false)
	if category.Status != pressureAuditFail || !auditHasFinding(category, "process_inventory_stale") {
		t.Fatalf("stale inventory category = %+v", category)
	}
}

func TestCLITelemetryPathInWindowRejectsHealthAndHistoricalShards(t *testing.T) {
	since := time.Date(2026, 7, 20, 18, 0, 0, 0, time.UTC)
	now := time.Date(2026, 7, 21, 18, 0, 0, 0, time.UTC)
	tests := []struct {
		path string
		want bool
	}{
		{"events-2026-07-20-s01.jsonl", true},
		{"events-2026-07-21.jsonl", true},
		{"events-2026-07-19-s63.jsonl", false},
		{"events-2026-07-22-s01.jsonl", false},
		{"events-health-2026-07-21-s01.jsonl", false},
		{"events-invalid.jsonl", false},
	}
	for _, test := range tests {
		if got := cliTelemetryPathInWindow(test.path, since, now); got != test.want {
			t.Errorf("cliTelemetryPathInWindow(%q)=%t want %t", test.path, got, test.want)
		}
	}
}

func TestCLIAuditCacheReusesOnlyContentVerifiedShard(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 7, 22, 21, 0, 0, 0, time.UTC)
	path := filepath.Join(dir, "events-2026-07-22-s01.jsonl")
	ok := true
	event := telemetry.Event{
		SchemaVersion: telemetry.SchemaVersion, TS: now.Format(time.RFC3339Nano),
		Source: telemetry.CLIInvocationSourceID, Surface: "ndev", Command: "ndev session",
		Event: "session.pressure.command", Level: "info", OK: &ok,
		Attrs:   map[string]any{"action": "audit", "exit_code": int64(0), "outcome": "success"},
		Privacy: telemetry.PrivacyLocal,
	}
	body, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(body, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	cachePath := filepath.Join(dir, "cache.json")
	first, counts, _, stats := scanCLIAuditPaths(cachePath, []string{path}, now.Add(-time.Hour), now.Add(time.Hour))
	if first.Rows != 1 || first.ValidationErrors != 0 || counts[event.Event] != 1 || stats.Hits != 0 || stats.Misses != 1 || stats.WriteError {
		t.Fatalf("first CLI audit scan=%+v counts=%+v cache=%+v", first, counts, stats)
	}
	second, counts, _, stats := scanCLIAuditPaths(cachePath, []string{path}, now.Add(-time.Hour), now.Add(time.Hour))
	if second.Rows != 1 || second.ValidationErrors != 0 || counts[event.Event] != 1 || stats.Hits != 1 || stats.Misses != 0 || stats.LoadError {
		t.Fatalf("cached CLI audit scan=%+v counts=%+v cache=%+v", second, counts, stats)
	}
	invalid := event
	invalid.Attrs = map[string]any{"unsafe": "/Users/private/source"}
	invalidBody, err := json.Marshal(invalid)
	if err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write(append(invalidBody, '\n')); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	third, _, _, stats := scanCLIAuditPaths(cachePath, []string{path}, now.Add(-time.Hour), now.Add(time.Hour))
	if third.Rows != 2 || third.ValidationErrors != 1 || stats.Hits != 1 || stats.IncrementalHits != 1 || stats.Misses != 0 {
		t.Fatalf("append-only CLI audit scan=%+v cache=%+v", third, stats)
	}
	// In-place prefix mutation must not inherit the cached validation result,
	// even when file size and event timestamps remain unchanged.
	rewritten, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	rewrittenText := strings.Replace(string(rewritten), `"action":"audit"`, `"action":"other"`, 1)
	if rewrittenText == string(rewritten) || len(rewrittenText) != len(rewritten) {
		t.Fatal("test fixture did not produce a same-size prefix mutation")
	}
	if err := os.WriteFile(path, []byte(rewrittenText), 0o600); err != nil {
		t.Fatal(err)
	}
	fourth, _, _, stats := scanCLIAuditPaths(cachePath, []string{path}, now.Add(-time.Hour), now.Add(time.Hour))
	if fourth.Rows != 2 || fourth.ValidationErrors != 1 || stats.Hits != 0 || stats.IncrementalHits != 0 || stats.Misses != 1 {
		t.Fatalf("prefix-mutated CLI audit scan=%+v cache=%+v", fourth, stats)
	}
}

func TestCLIAuditCacheDoesNotReuseAcrossMovingWindowBoundary(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 7, 22, 21, 0, 0, 0, time.UTC)
	path := filepath.Join(dir, "events-2026-07-22-s01.jsonl")
	ok := true
	events := []telemetry.Event{
		{SchemaVersion: telemetry.SchemaVersion, TS: now.Add(-90 * time.Minute).Format(time.RFC3339Nano), Source: telemetry.CLIInvocationSourceID, Surface: "ndev", Command: "ndev old", Event: "old.event", Level: "info", OK: &ok, Privacy: telemetry.PrivacyLocal},
		{SchemaVersion: telemetry.SchemaVersion, TS: now.Format(time.RFC3339Nano), Source: telemetry.CLIInvocationSourceID, Surface: "ndev", Command: "ndev current", Event: "current.event", Level: "info", OK: &ok, Privacy: telemetry.PrivacyLocal},
	}
	var body []byte
	for _, event := range events {
		encoded, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		body = append(body, append(encoded, '\n')...)
	}
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	cachePath := filepath.Join(dir, "cache.json")
	_, counts, _, first := scanCLIAuditPaths(cachePath, []string{path}, now.Add(-2*time.Hour), now.Add(time.Minute))
	if first.Misses != 1 || counts["old.event"] != 1 || counts["current.event"] != 1 {
		t.Fatalf("initial scan counts=%+v cache=%+v", counts, first)
	}
	_, counts, _, moved := scanCLIAuditPaths(cachePath, []string{path}, now.Add(-30*time.Minute), now.Add(time.Minute))
	if moved.Hits != 0 || moved.Misses != 1 || counts["old.event"] != 0 || counts["current.event"] != 1 {
		t.Fatalf("moving-window scan reused stale summary: counts=%+v cache=%+v", counts, moved)
	}
}

func TestPressureWorkCapacityInvariantAllowsOnlyBoundedExpressOvercommit(t *testing.T) {
	tests := []struct {
		name   string
		status sessionpressure.WorkStatus
		want   bool
	}{
		{
			name: "ordinary capacity",
			status: sessionpressure.WorkStatus{Capacity: 8, Used: 7, Available: 1, Leases: []sessionpressure.WorkLeaseStatus{
				{Class: sessionpressure.WorkClassBuild, Weight: 5}, {Class: sessionpressure.WorkClassExpressBuild, Weight: 2},
			}},
			want: true,
		},
		{
			name: "green express overcommit",
			status: sessionpressure.WorkStatus{Capacity: 8, Used: 9, Available: 0, Leases: []sessionpressure.WorkLeaseStatus{
				{Class: sessionpressure.WorkClassBuild, Weight: 5}, {Class: sessionpressure.WorkClassTest, Weight: 3}, {Class: sessionpressure.WorkClassExpressTest, Weight: 1},
			}},
			want: true,
		},
		{
			name: "unattributed overcommit",
			status: sessionpressure.WorkStatus{Capacity: 8, Used: 9, Available: 0, Leases: []sessionpressure.WorkLeaseStatus{
				{Class: sessionpressure.WorkClassHeavy, Weight: 6}, {Class: sessionpressure.WorkClassTest, Weight: 3},
			}},
		},
		{
			name: "over hard ceiling",
			status: sessionpressure.WorkStatus{Capacity: 8, Used: 11, Available: 0, Leases: []sessionpressure.WorkLeaseStatus{
				{Class: sessionpressure.WorkClassBuild, Weight: 5}, {Class: sessionpressure.WorkClassTest, Weight: 3}, {Class: sessionpressure.WorkClassExpressBuild, Weight: 2}, {Class: sessionpressure.WorkClassExpressTest, Weight: 1},
			}},
		},
		{
			name: "ledger mismatch",
			status: sessionpressure.WorkStatus{Capacity: 8, Used: 5, Available: 3, Leases: []sessionpressure.WorkLeaseStatus{
				{Class: sessionpressure.WorkClassTest, Weight: 3},
			}},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, _, got := pressureWorkCapacityInvariant(test.status)
			if got != test.want {
				t.Fatalf("capacity invariant = %t want %t for %+v", got, test.want, test.status)
			}
		})
	}
}

func TestPressureAuditCleanupWarnsWhenProcessOnlyHasNoOptInClaims(t *testing.T) {
	dir := t.TempDir()
	policy := hostcleanup.DefaultPolicy()
	policy.Enforce = true
	policy.BrowserEnabled = false
	policy.DevSessionEnabled = false
	policy.DockerWorkspaceEnabled = false
	policy.ObservationStartedAt = time.Now().Add(-hostcleanup.MinimumObservationWindow - time.Hour)
	if err := hostcleanup.SavePolicy(dir, policy); err != nil {
		t.Fatal(err)
	}

	category := auditPressureCleanup(pressureRuntime{dir: dir}, sessionpressure.Snapshot{}, false)
	if category.Status != pressureAuditWarn || !auditHasFinding(category, "process_cleanup_unarmed") {
		t.Fatalf("unarmed cleanup category=%+v", category)
	}
	if _, err := hostcleanup.NewClaimStore(dir).Acquire(
		hostcleanup.ResourceProcess, "audit-fixture", "audit-test", time.Minute, true, os.Getpid(), "",
	); err != nil {
		t.Fatal(err)
	}
	category = auditPressureCleanup(pressureRuntime{dir: dir}, sessionpressure.Snapshot{}, false)
	if auditHasFinding(category, "process_cleanup_unarmed") {
		t.Fatalf("opted-in cleanup still reported unarmed: %+v", category)
	}
}

func TestPressureAuditCleanupAcceptsScheduledSafeGraduation(t *testing.T) {
	dir := t.TempDir()
	policy := hostcleanup.DefaultPolicy()
	policy.AutoGraduateProcessOnly = true
	policy.ObservationStartedAt = time.Now().UTC()
	if err := hostcleanup.SavePolicy(dir, policy); err != nil {
		t.Fatal(err)
	}
	category := auditPressureCleanup(pressureRuntime{dir: dir}, sessionpressure.Snapshot{}, false)
	if category.Status != pressureAuditPass || auditHasFinding(category, "cleanup_observation") {
		t.Fatalf("scheduled cleanup category=%+v", category)
	}
	if scheduled, ok := category.Metrics["auto_process_only_graduation_scheduled"].(bool); !ok || !scheduled {
		t.Fatalf("scheduled metric=%#v", category.Metrics["auto_process_only_graduation_scheduled"])
	}
}

func TestPressureAuditCleanupFailsUnsafeEnforcementPolicy(t *testing.T) {
	now := time.Now().UTC()
	tests := []struct {
		name      string
		configure func(*hostcleanup.Policy)
	}{
		{
			name: "observation window incomplete",
			configure: func(policy *hostcleanup.Policy) {
				policy.ObservationStartedAt = now
			},
		},
		{
			name: "native providers not graduated",
			configure: func(policy *hostcleanup.Policy) {
				policy.ObservationStartedAt = now.Add(-hostcleanup.MinimumObservationWindow - time.Hour)
				policy.BrowserEnabled = true
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			policy := hostcleanup.DefaultPolicy()
			policy.Enforce = true
			policy.BrowserEnabled = false
			policy.DevSessionEnabled = false
			policy.DockerWorkspaceEnabled = false
			test.configure(&policy)
			if err := hostcleanup.SavePolicy(dir, policy); err != nil {
				t.Fatal(err)
			}
			category := auditPressureCleanup(pressureRuntime{dir: dir}, sessionpressure.Snapshot{}, false)
			if category.Status != pressureAuditFail || !auditHasFinding(category, "cleanup_enforcement_unsafe") {
				t.Fatalf("unsafe cleanup category=%+v", category)
			}
		})
	}
}

func auditHasFinding(category pressureAuditCategory, code string) bool {
	for _, finding := range category.Findings {
		if strings.EqualFold(finding.Code, code) {
			return true
		}
	}
	return false
}
