package sessionpressure

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestHostConsumersAreAggregatedAndPromptFree(t *testing.T) {
	processes := []Process{
		{PID: 10, PPID: 1, RSSKB: 100 * 1024, CPUPercent: 1.25, CPUAvailable: true, Executable: "Google Chrome Helper", Command: "Google Chrome Helper --prompt secret-canary"},
		{PID: 11, PPID: 1, RSSKB: 50 * 1024, CPUPercent: 0.75, CPUAvailable: true, Executable: "Google Chrome Helper"},
		{PID: 20, PPID: 1, RSSKB: 80 * 1024, CPUPercent: 0.1, CPUAvailable: true, Agent: "codex", Executable: "codex", Command: "codex secret-canary"},
		{PID: 21, PPID: 20, RSSKB: 20 * 1024, CPUPercent: 0.2, CPUAvailable: true, Executable: "node", Command: "node --prompt secret-canary"},
	}
	trees := buildAgentTrees(processes)
	consumers := buildHostConsumers(processes, trees)
	if len(consumers) != 3 {
		t.Fatalf("host consumers=%+v", consumers)
	}
	chrome := consumers[0]
	if chrome.Executable != "Google_Chrome_Helper" || chrome.Category != "browser" || chrome.ProcessCount != 2 || chrome.RSSSumMB != 150 || !chrome.CPUAvailable {
		t.Fatalf("chrome aggregation=%+v", chrome)
	}
	var codex, node HostConsumer
	for _, consumer := range consumers {
		switch consumer.Executable {
		case "codex":
			codex = consumer
		case "node":
			node = consumer
		}
	}
	if codex.AgentProcessCount != 1 || node.AgentProcessCount != 1 || !codex.CPUAvailable || !node.CPUAvailable || len(trees) != 1 || !trees[0].CPUAvailable {
		t.Fatalf("agent ownership was not projected: codex=%+v node=%+v", codex, node)
	}
	body, err := json.Marshal(consumers)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "secret-canary") || strings.Contains(string(body), "--prompt") || strings.Contains(string(body), `"pid"`) {
		t.Fatalf("host-consumer projection leaked process detail: %s", body)
	}
}

func TestHostConsumersPropagateUnavailableCPUEvidence(t *testing.T) {
	consumers := buildHostConsumers([]Process{
		{PID: 10, CPUAvailable: true, Executable: "node"},
		{PID: 11, CPUAvailable: false, Executable: "node"},
	}, nil)
	if len(consumers) != 1 || consumers[0].CPUAvailable {
		t.Fatalf("partial executable CPU evidence must remain unavailable: %+v", consumers)
	}
}

func TestAnnotateProcessCPUPercentUsesCumulativeDeltaAndStableIdentity(t *testing.T) {
	previous := []Process{{PID: 42, StartedAtNS: 100, CPUTotalNS: 1_000_000_000, CPUTotalValid: true}}
	current := []Process{{PID: 42, StartedAtNS: 100, CPUTotalNS: 1_500_000_000, CPUTotalValid: true}}
	annotateProcessCPUPercent(current, previous, 2*time.Second)
	if !current[0].CPUAvailable || current[0].CPUPercent != 25 {
		t.Fatalf("native CPU delta = %+v, want available 25%%", current[0])
	}

	current[0].CPUTotalNS = previous[0].CPUTotalNS
	annotateProcessCPUPercent(current, previous, time.Second)
	if !current[0].CPUAvailable || current[0].CPUPercent != 0 {
		t.Fatalf("zero native CPU delta must be known idle: %+v", current[0])
	}
}

func TestAnnotateProcessCPUPercentRejectsPIDReuseAndRegressingCounters(t *testing.T) {
	previous := []Process{{PID: 42, StartedAtNS: 100, CPUTotalNS: 1_000, CPUTotalValid: true}}
	for name, current := range map[string]Process{
		"pid-reuse":          {PID: 42, StartedAtNS: 101, CPUTotalNS: 2_000, CPUTotalValid: true},
		"counter-regression": {PID: 42, StartedAtNS: 100, CPUTotalNS: 999, CPUTotalValid: true},
		"missing-rusage":     {PID: 42, StartedAtNS: 100},
	} {
		t.Run(name, func(t *testing.T) {
			processes := []Process{current}
			annotateProcessCPUPercent(processes, previous, time.Second)
			if processes[0].CPUAvailable {
				t.Fatalf("invalid native CPU identity became available: %+v", processes[0])
			}
		})
	}
}

func TestMemoryMomentumClassifiesAndEstimatesRed(t *testing.T) {
	base := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	observations := []memoryObservation{
		{Timestamp: base, FreePercent: 40},
		{Timestamp: base.Add(time.Minute), FreePercent: 35},
		{Timestamp: base.Add(2 * time.Minute), FreePercent: 30},
	}
	momentum, slope, eta := calculateMemoryMomentum(observations, 15)
	if momentum != MemoryMomentumRapidDecline || slope != -5 || eta == nil || *eta != 3 {
		t.Fatalf("decline momentum=%s slope=%.2f eta=%v", momentum, slope, eta)
	}

	for index := range observations {
		observations[index].FreePercent = 30 + float64(index*2)
	}
	momentum, slope, eta = calculateMemoryMomentum(observations, 15)
	if momentum != MemoryMomentumRecovering || slope != 2 || eta != nil {
		t.Fatalf("recovery momentum=%s slope=%.2f eta=%v", momentum, slope, eta)
	}

	momentum, _, _ = calculateMemoryMomentum(observations[:2], 15)
	if momentum != MemoryMomentumUnknown {
		t.Fatalf("short history momentum=%s", momentum)
	}
}

func TestSampleFailureUsesRecentResidentEvidence(t *testing.T) {
	policy := DefaultPolicy(16 * 1024)
	policy.EnforceAdmission = true
	red := Snapshot{Timestamp: time.Now(), FreePercent: 14, MemoryMomentum: MemoryMomentumUnknown, TopHostConsumers: []HostConsumer{}, TopAgentTrees: []AgentTree{}}
	admission := admissionAfterSampleFailure(errors.New("fixture probe failed"), &red, policy)
	if admission.Allowed || admission.Level != LevelRed || admission.Source != "resident-fallback" || !strings.Contains(admission.Warning, "remained enforceable") {
		t.Fatalf("recent red evidence failed open: %+v", admission)
	}

	admission = admissionAfterSampleFailure(errors.New("fixture probe failed"), nil, policy)
	if !admission.Allowed || admission.Source != "fail-open" || !strings.Contains(admission.Warning, "no recent resident evidence") {
		t.Fatalf("missing evidence fallback=%+v", admission)
	}

	staleHost := Snapshot{Timestamp: time.Now().Add(-10 * time.Minute), FreePercent: 1}
	admission = admissionAfterSampleFailure(errors.New("fixture probe failed"), &staleHost, policy)
	if !admission.Allowed || admission.Source != "fail-open" || admission.Snapshot != nil {
		t.Fatalf("stale host evidence affected fallback: %+v", admission)
	}

	staleInventory := Snapshot{
		Timestamp: time.Now(), FreePercent: 60, ProcessInventoryAvailable: true,
		ProcessInventoryCapturedAt: time.Now().Add(-10 * time.Minute),
		AgentRSSSumMB:              policy.Thresholds.AgentTotalCriticalMB,
		TopAgentTrees:              []AgentTree{{Agent: "codex", RSSSumMB: policy.Thresholds.TreeCriticalMB}},
	}
	admission = admissionAfterSampleFailure(errors.New("fixture probe failed"), &staleInventory, policy)
	if !admission.Allowed || admission.Level != LevelNormal || admission.Snapshot == nil || admission.Snapshot.ProcessInventoryAvailable {
		t.Fatalf("stale resident inventory affected fallback: %+v", admission)
	}
}

func TestCoverageReflectsDisabledOrUnreadyReliefAuthority(t *testing.T) {
	policy := DefaultPolicy(16 * 1024)
	report := AssessCoverage("", policy, GuardHealth{})
	find := func(id string) CoverageSurface {
		for _, surface := range report.Surfaces {
			if surface.ID == id {
				return surface
			}
		}
		return CoverageSurface{}
	}
	if got := find("relief_authority"); got.State != CoverageObserved || !strings.Contains(got.Detail, "disabled") {
		t.Fatalf("disabled relief coverage=%+v", got)
	}
	if got := find("probe_failure_fallback"); got.State != CoverageObserved {
		t.Fatalf("observe-only failure coverage=%+v", got)
	}

	policy.EnforceAdmission = true
	policy.AutoShedCritical = true
	report = AssessCoverage("", policy, GuardHealth{MonitorHealthy: true})
	if got := find("relief_authority"); got.State != CoverageAttention || !strings.Contains(got.Detail, "not earned") {
		t.Fatalf("unready relief coverage=%+v", got)
	}
}

func TestCoverageReportsEnforcementAndExplicitBoundaries(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	for _, relative := range []string{".claude/hooks/toolguard.sh", ".codex/hooks/toolguard.sh", "nicos-dev/bin/toolguard"} {
		path := filepath.Join(root, relative)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"cdx", "codex", "cld", "claude", "grok", "kimi"} {
		path := filepath.Join(root, ".nicos-dev", "agent-shims", name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, relative := range []string{".claude/settings.json", ".codex/hooks.json"} {
		path := filepath.Join(root, relative)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(`{"command":"toolguard.sh"}`), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	policy := DefaultPolicy(16 * 1024)
	policy.EnforceAdmission = true
	policy.AutoShedCritical = true
	health := GuardHealth{MonitorHealthy: true, DailyDriverReady: true, OperatorReady: true}
	report := AssessCoverage(root, policy, health)
	if report.Status != "ready-with-explicit-boundaries" || len(report.Limitations) < 3 {
		t.Fatalf("coverage=%+v", report)
	}
	state := func(id string) CoverageState {
		for _, surface := range report.Surfaces {
			if surface.ID == id {
				return surface.State
			}
		}
		return ""
	}
	if state("claude_toolguard") != CoverageEnforced || state("codex_toolguard") != CoverageEnforced || state("storage_admission") != CoverageObserved || state("direct_external_launch") != CoverageObserved || state("relief_authority") != CoverageEnforced {
		t.Fatalf("unexpected coverage surfaces=%+v", report.Surfaces)
	}
	if !slices.ContainsFunc(report.Limitations, func(item string) bool { return strings.Contains(item, "not intercepted") }) {
		t.Fatalf("external boundary missing: %+v", report.Limitations)
	}
}

func TestOperatorReadinessIncludesRecoveryTruth(t *testing.T) {
	health := GuardHealth{DailyDriverReady: true}.WithOperatorState(true, nil)
	if health.OperatorReady || len(health.OperatorReasons) != 1 || !strings.Contains(health.OperatorReasons[0], "pending review") {
		t.Fatalf("pending recovery reported ready: %+v", health)
	}
	health = GuardHealth{DailyDriverReady: true}.WithOperatorState(false, nil)
	if !health.OperatorReady || len(health.OperatorReasons) != 0 {
		t.Fatalf("clean operator state not ready: %+v", health)
	}
}

func TestLoadPolicyMigratesLegacyInventoryCadence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "policy.json")
	legacy := DefaultPolicy(16 * 1024)
	legacy.ProcessInventoryIntervalSeconds = 300
	body, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, persisted, err := LoadPolicy(path, 16*1024)
	if err != nil || !persisted || loaded.ProcessInventoryIntervalSeconds != 180 {
		t.Fatalf("inventory migration persisted=%v interval=%d err=%v", persisted, loaded.ProcessInventoryIntervalSeconds, err)
	}
}

func TestStateTransitionBoundsHostConsumers(t *testing.T) {
	dir := t.TempDir()
	store := NewTelemetryStore(dir)
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	store.Now = func() time.Time { return now }
	snapshot := Snapshot{SchemaVersion: SchemaVersion, Timestamp: now, MemoryMomentum: MemoryMomentumSteady}
	for index := 0; index < 8; index++ {
		snapshot.TopHostConsumers = append(snapshot.TopHostConsumers, HostConsumer{Executable: "process", RSSSumMB: float64(100 - index)})
	}
	if err := store.AppendEvent(TelemetryEvent{Event: "state_transition", Snapshot: &snapshot}); err != nil {
		t.Fatal(err)
	}
	events, err := store.ReadEvents(1, now.Add(-time.Minute))
	if err != nil || len(events) != 1 || events[0].Snapshot == nil || len(events[0].Snapshot.TopHostConsumers) != telemetryTopHostConsumerLimit {
		t.Fatalf("bounded host transition events=%+v err=%v", events, err)
	}
	if len(snapshot.TopHostConsumers) != 8 {
		t.Fatalf("AppendEvent mutated caller host consumers: %d", len(snapshot.TopHostConsumers))
	}
}

func TestResidentTelemetryEventRateLimitsFullTransitions(t *testing.T) {
	policy := DefaultPolicy(16 * 1024)
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	event, persist := selectResidentTelemetryEvent(now, time.Time{}, time.Time{}, false, 1, policy)
	if !persist || event != "state_transition" {
		t.Fatalf("initial event=%q persist=%v", event, persist)
	}

	lastTransition := now
	lastPersist := now.Add(-time.Duration(policy.HeartbeatSeconds) * time.Second)
	event, persist = selectResidentTelemetryEvent(now.Add(30*time.Minute), lastPersist, lastTransition, false, 1, policy)
	if !persist || event != "heartbeat" {
		t.Fatalf("rate-limited transition event=%q persist=%v", event, persist)
	}

	event, persist = selectResidentTelemetryEvent(now.Add(time.Hour), now.Add(59*time.Minute), lastTransition, true, 1, policy)
	if !persist || event != "sample_recovered" {
		t.Fatalf("hourly recovery event=%q persist=%v", event, persist)
	}
}
