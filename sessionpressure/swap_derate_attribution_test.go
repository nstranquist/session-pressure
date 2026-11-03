package sessionpressure

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// swapStep is one resident sample in an attribution scenario.
type swapStep struct {
	rate       float64 // swap-out MB/s
	agentRSSMB float64 // leased work RSS; negative means inventory unavailable
}

type swapScenario struct {
	t      *testing.T
	latch  swapThrashLatch
	limits WorkLimits
	at     time.Time
}

func newSwapScenario(t *testing.T, limits WorkLimits) *swapScenario {
	return &swapScenario{t: t, limits: limits, at: time.Date(2026, 11, 2, 23, 0, 0, 0, time.UTC)}
}

// step advances 30 s (the pressure cadence) and returns the sample and any
// transition.
func (scenario *swapScenario) step(step swapStep) (Snapshot, SwapThrashTransition, bool) {
	scenario.at = scenario.at.Add(30 * time.Second)
	snapshot := Snapshot{
		Timestamp: scenario.at, PhysicalMemoryMB: 16 * 1024, SwapUsedMB: 20000,
		SwapIOAvailable: true, SwapOutMBPerSecond: step.rate,
	}
	if step.agentRSSMB >= 0 {
		snapshot.CoordinatedWork = CoordinatedWorkSnapshot{Available: true, Fresh: true, LeaseCount: 1, RSSSumMB: step.agentRSSMB}
	}
	transition, changed := scenario.latch.advance(&snapshot, DefaultPolicy(16*1024).Thresholds, scenario.limits)
	return snapshot, transition, changed
}

func TestSwapThrashAttributionLiftsDerateForExternalStorm(t *testing.T) {
	limits := defaultWorkLimits(10) // release 300 s, share 25% = 4096 MB
	if limits.SwapThrashExternalReleaseSeconds != 300 || limits.SwapThrashAgentSharePercent != 25 {
		t.Fatalf("defaults release=%d share=%d", limits.SwapThrashExternalReleaseSeconds, limits.SwapThrashAgentSharePercent)
	}
	scenario := newSwapScenario(t, limits)
	if _, _, changed := scenario.step(swapStep{rate: 300, agentRSSMB: 2000}); changed {
		t.Fatal("one hot sample must not enter the latch")
	}
	snapshot, transition, changed := scenario.step(swapStep{rate: 300, agentRSSMB: 2000})
	if !changed || transition.State != SwapThrashEntered || transition.Cause != SwapThrashCauseUnattributed {
		t.Fatalf("entry transition=%+v changed=%v", transition, changed)
	}
	entered := snapshot.Timestamp
	if snapshot.SwapThrashCause != SwapThrashCauseUnattributed || !snapshot.SwapThrashSince.Equal(entered) || transition.AgentShareMB != 4096 || transition.CoordinatedRSSMB != 2000 {
		t.Fatalf("entry snapshot cause=%q since=%v transition=%+v", snapshot.SwapThrashCause, snapshot.SwapThrashSince, transition)
	}
	// Small leased work for less than the window keeps the derate.
	for i := 0; i < 9; i++ {
		snapshot, transition, changed = scenario.step(swapStep{rate: 300, agentRSSMB: 2000})
		if changed || snapshot.SwapThrashCause != SwapThrashCauseUnattributed {
			t.Fatalf("sample %d cause=%q transition=%+v", i, snapshot.SwapThrashCause, transition)
		}
	}
	// 300 s after the quiet run began, the storm is external.
	snapshot, transition, changed = scenario.step(swapStep{rate: 300, agentRSSMB: 2000})
	if !changed || transition.State != SwapThrashDerateReleased || snapshot.SwapThrashCause != SwapThrashCauseExternal {
		t.Fatalf("release transition=%+v cause=%q", transition, snapshot.SwapThrashCause)
	}
	// Leased work past the share re-engages the derate.
	snapshot, transition, changed = scenario.step(swapStep{rate: 300, agentRSSMB: 5000})
	if !changed || transition.State != SwapThrashDerateResumed || snapshot.SwapThrashCause != SwapThrashCauseAgents {
		t.Fatalf("resume transition=%+v cause=%q", transition, snapshot.SwapThrashCause)
	}
	// Proof is sticky: shrinking again lifts the derate at once.
	snapshot, transition, changed = scenario.step(swapStep{rate: 300, agentRSSMB: 1000})
	if !changed || transition.State != SwapThrashDerateReleased || snapshot.SwapThrashCause != SwapThrashCauseExternal {
		t.Fatalf("sticky proof transition=%+v cause=%q", transition, snapshot.SwapThrashCause)
	}
	// Two calm samples release the latch and report the whole episode.
	if _, _, changed = scenario.step(swapStep{rate: 10, agentRSSMB: 1000}); changed {
		t.Fatal("one calm sample released the latch")
	}
	snapshot, transition, changed = scenario.step(swapStep{rate: 10, agentRSSMB: 1000})
	if !changed || transition.State != SwapThrashReleased || transition.Cause != SwapThrashCauseExternal {
		t.Fatalf("latch release transition=%+v", transition)
	}
	if snapshot.SwapThrashLatched || snapshot.SwapThrashCause != "" || !snapshot.SwapThrashSince.IsZero() {
		t.Fatalf("released snapshot latched=%v cause=%q since=%v", snapshot.SwapThrashLatched, snapshot.SwapThrashCause, snapshot.SwapThrashSince)
	}
	// Entry at 23:01:00, release at 23:08:00: 420 s. External 23:06:00-23:06:30
	// and 23:07:00-23:08:00: 90 s.
	if transition.EpisodeSeconds != 420 || transition.ExternalSeconds != 90 || !transition.Since.Equal(entered) {
		t.Fatalf("episode=%vs external=%vs since=%v", transition.EpisodeSeconds, transition.ExternalSeconds, transition.Since)
	}
	// The storm returns inside the window: no second proof is charged.
	scenario.step(swapStep{rate: 300, agentRSSMB: 1000})
	snapshot, transition, changed = scenario.step(swapStep{rate: 300, agentRSSMB: 1000})
	if !changed || transition.State != SwapThrashEntered || transition.Cause != SwapThrashCauseExternal || snapshot.SwapThrashCause != SwapThrashCauseExternal {
		t.Fatalf("re-entry transition=%+v cause=%q", transition, snapshot.SwapThrashCause)
	}
}

func TestSwapThrashAttributionAgentDrivenStrictAndBlind(t *testing.T) {
	t.Run("agent-driven storm keeps the derate", func(t *testing.T) {
		scenario := newSwapScenario(t, defaultWorkLimits(10))
		for i := 0; i < 40; i++ {
			snapshot, transition, changed := scenario.step(swapStep{rate: 300, agentRSSMB: 6000})
			if snapshot.SwapThrashLatched && snapshot.SwapThrashCause != SwapThrashCauseAgents {
				t.Fatalf("sample %d cause=%q", i, snapshot.SwapThrashCause)
			}
			if changed && transition.State != SwapThrashEntered {
				t.Fatalf("sample %d unexpected transition=%+v", i, transition)
			}
		}
	})
	t.Run("strict policy never lifts the derate", func(t *testing.T) {
		limits := defaultWorkLimits(10)
		limits.SwapThrashExternalReleaseSeconds = -1
		scenario := newSwapScenario(t, limits)
		for i := 0; i < 60; i++ {
			snapshot, _, _ := scenario.step(swapStep{rate: 300, agentRSSMB: 0})
			if snapshot.SwapThrashCause == SwapThrashCauseExternal {
				t.Fatalf("strict policy lifted the derate at sample %d", i)
			}
		}
	})
	t.Run("missing inventory counts as small leased work", func(t *testing.T) {
		scenario := newSwapScenario(t, defaultWorkLimits(10))
		var snapshot Snapshot
		for i := 0; i < 12; i++ {
			snapshot, _, _ = scenario.step(swapStep{rate: 300, agentRSSMB: -1})
		}
		if snapshot.SwapThrashCause != SwapThrashCauseExternal {
			t.Fatalf("blind inventory cause=%q; an observability gap must not hold agents for the whole storm", snapshot.SwapThrashCause)
		}
	})
	t.Run("unavailable swap counters end the episode", func(t *testing.T) {
		scenario := newSwapScenario(t, defaultWorkLimits(10))
		scenario.step(swapStep{rate: 300, agentRSSMB: 0})
		scenario.step(swapStep{rate: 300, agentRSSMB: 0})
		scenario.at = scenario.at.Add(30 * time.Second)
		snapshot := Snapshot{Timestamp: scenario.at, PhysicalMemoryMB: 16 * 1024}
		transition, changed := scenario.latch.advance(&snapshot, DefaultPolicy(16*1024).Thresholds, scenario.limits)
		if !changed || transition.State != SwapThrashReleased || snapshot.SwapThrashLatched || snapshot.SwapThrashCause != "" {
			t.Fatalf("blind release transition=%+v snapshot cause=%q latched=%v", transition, snapshot.SwapThrashCause, snapshot.SwapThrashLatched)
		}
	})
}

func TestSwapThrashDerateLetsWorkThroughExternalStorm(t *testing.T) {
	limits := defaultWorkLimits(10)
	gate := &workAdmissionGate{
		limits: limits, class: WorkClassBuild, swapThrashEnforced: true,
		warningCapacityStatus: func() (WorkStatus, bool) { return WorkStatus{Used: 5, Capacity: limits.Capacity}, true },
	}
	admission := swapThrashAdmission(true)
	admission.Snapshot.SwapThrashCause = SwapThrashCauseExternal
	if decision := gate.observe(admission, false, false); !decision.Allowed {
		t.Fatalf("external storm deferred work: %+v", decision)
	}
	if label := gate.decisionLabel(); label != SwapThrashExternalDecision {
		t.Fatalf("label=%q", label)
	}
	gate = &workAdmissionGate{
		limits: limits, class: WorkClassBuild, swapThrashEnforced: true,
		warningCapacityStatus: func() (WorkStatus, bool) { return WorkStatus{Used: 5, Capacity: limits.Capacity}, true },
	}
	admission = swapThrashAdmission(true)
	admission.Snapshot.SwapThrashCause = SwapThrashCauseUnattributed
	decision := gate.observe(admission, false, false)
	if decision.Allowed || !strings.Contains(decision.Reason, "cause=unattributed") {
		t.Fatalf("unattributed storm decision=%+v", decision)
	}
}

func swapStatsEvents(base time.Time) []WorkEvent {
	event := func(op string, kind WorkEventType, offset time.Duration, mutate func(*WorkEvent)) WorkEvent {
		value := WorkEvent{SchemaVersion: WorkEventSchemaVersion, OperationID: op, Event: kind, Class: WorkClassBuild, Weight: 5, Timestamp: base.Add(offset)}
		if mutate != nil {
			mutate(&value)
		}
		return value
	}
	swapWait := func(waitMS int64) func(*WorkEvent) {
		return func(value *WorkEvent) {
			value.PressureDimension, value.AdmissionDecision, value.PressureWaitMilliseconds = "swap", SwapThrashDeferredDecision, waitMS
			value.WaitMilliseconds = waitMS
		}
	}
	return []WorkEvent{
		// Deferred 20 minutes by the derate: over the per-operation budget.
		event("long", WorkEventQueued, 0, nil),
		event("long", WorkEventAcquired, time.Second, swapWait(0)),
		event("long", WorkEventStarted, 20*time.Minute, swapWait(20*60*1000)),
		event("long", WorkEventCompleted, 21*time.Minute, func(value *WorkEvent) {
			swapWait(20 * 60 * 1000)(value)
			value.RuntimeMillis = 60 * 1000
		}),
		// Admitted through an external storm.
		event("bypass", WorkEventQueued, 0, nil),
		event("bypass", WorkEventCompleted, time.Minute, func(value *WorkEvent) {
			value.AdmissionDecision, value.RuntimeMillis = SwapThrashExternalDecision, 60*1000
		}),
		// A true memory deferral stays a memory deferral.
		event("memory", WorkEventQueued, 0, func(value *WorkEvent) { value.PressureDimension = "memory" }),
		event("memory", WorkEventCompleted, time.Minute, func(value *WorkEvent) {
			value.PressureDimension, value.RuntimeMillis = "memory", 1000
		}),
	}
}

func TestWorkStatsCountSwapDeferralsAndMeasureDerateCost(t *testing.T) {
	base := time.Date(2026, 11, 3, 1, 0, 0, 0, time.UTC)
	stats := SummarizeWorkEvents(swapStatsEvents(base), base.Add(-time.Hour), base.Add(time.Hour))
	if stats.ReviewSignals.SwapDeferrals != 1 || stats.ReviewSignals.MemoryDeferrals != 1 {
		t.Fatalf("swap=%d memory=%d; swap must not be miscounted as memory", stats.ReviewSignals.SwapDeferrals, stats.ReviewSignals.MemoryDeferrals)
	}
	if stats.ReviewSignals.SwapThrashExternalBypasses != 1 {
		t.Fatalf("external bypasses=%d", stats.ReviewSignals.SwapThrashExternalBypasses)
	}
	cost := stats.SwapDerate
	if cost.DeferredOperations != 1 || cost.ExternalBypasses != 1 || cost.WaitTotalMS != 20*60*1000 || cost.Wait.MaximumMS != 20*60*1000 {
		t.Fatalf("cost=%+v", cost)
	}
	// A two-hour window reports its plain total, not a 12x daily projection.
	if cost.WaitMinutesPerDay != 20 {
		t.Fatalf("per day=%v", cost.WaitMinutesPerDay)
	}
	if cost.Verdict != SwapDerateOverBudget || !strings.Contains(cost.Advice, "swap_thrash_external_release_seconds") || !strings.Contains(cost.Advice, "20m0s > 15m0s") {
		t.Fatalf("verdict=%s advice=%q", cost.Verdict, cost.Advice)
	}
	// Two minutes is within budget; no deferral is unexercised.
	small := SummarizeWorkEvents([]WorkEvent{
		{SchemaVersion: WorkEventSchemaVersion, OperationID: "a", Event: WorkEventCompleted, Class: WorkClassBuild, Weight: 5, Timestamp: base,
			PressureDimension: "swap", PressureWaitMilliseconds: 2 * 60 * 1000, RuntimeMillis: 1000},
	}, base.Add(-time.Hour), base)
	if small.SwapDerate.Verdict != SwapDerateWithinBudget || small.SwapDerate.Advice != "" {
		t.Fatalf("small cost=%+v", small.SwapDerate)
	}
	if none := SummarizeWorkEvents(nil, base.Add(-time.Hour), base); none.SwapDerate.Verdict != SwapDerateUnexercised || none.SwapDerate.BudgetMinutesPerDay != SwapDerateBudgetMinutesPerDay {
		t.Fatalf("empty cost=%+v", none.SwapDerate)
	}
	// Over three days, 100 minutes is 33 min/day: over the daily budget.
	long := SummarizeWorkEvents([]WorkEvent{
		{SchemaVersion: WorkEventSchemaVersion, OperationID: "a", Event: WorkEventCompleted, Class: WorkClassBuild, Weight: 5, Timestamp: base,
			PressureDimension: "swap", PressureWaitMilliseconds: 10 * 60 * 1000, RuntimeMillis: 1000},
		{SchemaVersion: WorkEventSchemaVersion, OperationID: "b", Event: WorkEventCompleted, Class: WorkClassBuild, Weight: 5, Timestamp: base,
			PressureDimension: "swap", PressureWaitMilliseconds: 90 * 60 * 1000, RuntimeMillis: 1000},
	}, base.Add(-72*time.Hour), base)
	if long.SwapDerate.Verdict != SwapDerateOverBudget || long.SwapDerate.WaitMinutesPerDay < 33 || long.SwapDerate.WaitMinutesPerDay > 34 {
		t.Fatalf("three-day cost=%+v", long.SwapDerate)
	}
}

func TestSwapThrashEpisodesFromResidentHistory(t *testing.T) {
	base := time.Date(2026, 11, 3, 1, 0, 0, 0, time.UTC)
	at := func(minutes float64) time.Time { return base.Add(time.Duration(minutes * float64(time.Minute))) }
	transition := func(minutes float64, state, cause string, extra func(*SwapThrashTransition)) swapThrashHistoryEvent {
		value := &SwapThrashTransition{State: state, Cause: cause}
		if extra != nil {
			extra(value)
		}
		return swapThrashHistoryEvent{Timestamp: at(minutes), Event: SwapThrashTransitionEvent, SwapThrash: value}
	}
	rows := []swapThrashHistoryEvent{
		// Episode 1: agent-driven, 10 minutes.
		transition(0, SwapThrashEntered, SwapThrashCauseAgents, nil),
		transition(10, SwapThrashReleased, SwapThrashCauseAgents, func(value *SwapThrashTransition) { value.EpisodeSeconds = 600 }),
		// Episode 2: external after 5 minutes, then interrupted by a restart at 60.
		transition(20, SwapThrashEntered, SwapThrashCauseUnattributed, nil),
		transition(25, SwapThrashDerateReleased, SwapThrashCauseExternal, nil),
		{Timestamp: at(60), Event: "resident_stopped"},
		// Episode 3: still open at 100, external from entry.
		transition(90, SwapThrashEntered, SwapThrashCauseExternal, nil),
	}
	summary := summarizeSwapThrashEpisodes(rows, at(100))
	if summary.Count != 3 || !summary.Open || summary.Interrupted != 1 || summary.ExternalEpisodes != 2 {
		t.Fatalf("summary=%+v", summary)
	}
	// 10 + 40 + 10 minutes latched; 35 + 10 minutes external; longest 40.
	if summary.LatchedSeconds != 3600 || summary.ExternalSeconds != 2700 || summary.LongestSeconds != 2400 {
		t.Fatalf("latched=%v external=%v longest=%v", summary.LatchedSeconds, summary.ExternalSeconds, summary.LongestSeconds)
	}
	// A release whose entry precedes the window still counts once.
	tail := summarizeSwapThrashEpisodes([]swapThrashHistoryEvent{
		transition(5, SwapThrashReleased, SwapThrashCauseExternal, func(value *SwapThrashTransition) {
			value.EpisodeSeconds, value.ExternalSeconds = 1200, 900
		}),
	}, at(10))
	if tail.Count != 1 || tail.Open || tail.LatchedSeconds != 1200 || tail.ExternalSeconds != 900 || tail.ExternalEpisodes != 1 {
		t.Fatalf("tail=%+v", tail)
	}
}

func TestReadSwapThrashEpisodesDecodesOnlyMarkedRows(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC()
	store := NewTelemetryStore(dir)
	store.Now = func() time.Time { return now }
	entered := SwapThrashTransition{State: SwapThrashEntered, Cause: SwapThrashCauseUnattributed}
	if err := store.AppendEvent(TelemetryEvent{Timestamp: now.Add(-10 * time.Minute), Event: SwapThrashTransitionEvent, SwapThrash: &entered}); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendEvent(TelemetryEvent{Timestamp: now.Add(-5 * time.Minute), Event: "heartbeat", Snapshot: &Snapshot{Timestamp: now}}); err != nil {
		t.Fatal(err)
	}
	// An old day file outside the window is not opened.
	old := filepath.Join(dir, "snapshots-"+now.Add(-72*time.Hour).Local().Format("20060102")+".jsonl")
	if err := os.WriteFile(old, []byte("{not json \"event\":\"swap_thrash_transition\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	summary, err := ReadSwapThrashEpisodes(dir, now.Add(-time.Hour), now)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Count != 1 || !summary.Open || summary.LatchedSeconds < 599 || summary.LatchedSeconds > 601 {
		t.Fatalf("summary=%+v", summary)
	}
}

func TestSwapThrashReleasePolicyValidation(t *testing.T) {
	policy := DefaultPolicy(16 * 1024)
	policy.WorkLimits.SwapThrashExternalReleaseSeconds = 30
	if err := policy.Validate(); err == nil || !strings.Contains(err.Error(), "swap_thrash_external_release_seconds") {
		t.Fatalf("30 s release validated: %v", err)
	}
	policy.WorkLimits.SwapThrashExternalReleaseSeconds = -1
	if err := policy.Validate(); err != nil {
		t.Fatalf("strict release rejected: %v", err)
	}
	policy.WorkLimits.SwapThrashAgentSharePercent = 101
	if err := policy.Validate(); err == nil || !strings.Contains(err.Error(), "swap_thrash_agent_share_percent") {
		t.Fatalf("101%% share validated: %v", err)
	}
	legacy := normalizeWorkLimits(WorkLimits{Capacity: 8, BuildWeight: 5})
	if legacy.SwapThrashExternalReleaseSeconds != defaultSwapThrashExternalReleaseSeconds || legacy.SwapThrashAgentSharePercent != defaultSwapThrashAgentSharePercent {
		t.Fatalf("legacy fill release=%d share=%d", legacy.SwapThrashExternalReleaseSeconds, legacy.SwapThrashAgentSharePercent)
	}
}

func TestHeartbeatSummaryKeepsSwapThrashState(t *testing.T) {
	summary := compactTelemetrySummary(Snapshot{SwapOutMBPerSecond: 300, SwapThrashLatched: true, SwapThrashCause: SwapThrashCauseExternal})
	if !summary.SwapThrashLatched || summary.SwapThrashCause != SwapThrashCauseExternal || summary.SwapOutMBPerSecond != 300 {
		t.Fatalf("summary=%+v", summary)
	}
}

func TestDoctorExplainsSwapStormAndOverBudgetDerate(t *testing.T) {
	now := time.Date(2026, 11, 3, 1, 0, 0, 0, time.UTC)
	latest := Snapshot{
		Timestamp: now, SwapIOAvailable: true, SwapOutMBPerSecond: 420, SwapThrashLatched: true,
		SwapThrashCause: SwapThrashCauseExternal, SwapThrashSince: now.Add(-40 * time.Minute),
		TopHostConsumers: []HostConsumer{
			{Executable: "claude", Category: "agent", RSSSumMB: 9000},
			{Executable: "Cursor_Helper", Category: "editor", RSSSumMB: 7782},
			{Executable: "Google_Chrome_He", Category: "browser", RSSSumMB: 10240},
		},
	}
	cost := SwapDerateCostBudget()
	cost.DeferredOperations, cost.Wait.MaximumMS = 1, (20 * time.Minute).Milliseconds()
	cost.judge()
	warnings := swapThrashDoctorWarnings(PressureDoctorInput{Now: now, Policy: DefaultPolicy(16 * 1024), Latest: latest, HasLatest: true, SwapDerate: &cost})
	if len(warnings) != 2 {
		t.Fatalf("warnings=%q", warnings)
	}
	if !strings.Contains(warnings[0], "for 40m0s") || !strings.Contains(warnings[0], "derate is lifted") ||
		!strings.Contains(warnings[0], "Google_Chrome_He 10.0 GB, Cursor_Helper 7.6 GB") || strings.Contains(warnings[0], "claude") {
		t.Fatalf("external warning=%q", warnings[0])
	}
	if !strings.HasPrefix(warnings[1], "swap derate over budget (24h): ") {
		t.Fatalf("budget warning=%q", warnings[1])
	}
	latest.SwapThrashCause = SwapThrashCauseAgents
	warnings = swapThrashDoctorWarnings(PressureDoctorInput{Now: now, Policy: DefaultPolicy(16 * 1024), Latest: latest, HasLatest: true})
	if len(warnings) != 1 || !strings.Contains(warnings[0], "cause=agents") || !strings.Contains(warnings[0], "swap_thrash_capacity=") {
		t.Fatalf("agent warning=%q", warnings)
	}
	if calm := swapThrashDoctorWarnings(PressureDoctorInput{Now: now, Latest: Snapshot{SwapIOAvailable: true}, HasLatest: true}); len(calm) != 0 {
		t.Fatalf("calm host warnings=%q", calm)
	}
}

func TestSwapThrashEpisodeResumesAcrossResidentRestart(t *testing.T) {
	now := time.Date(2026, 11, 3, 1, 0, 0, 0, time.UTC)
	since := now.Add(-20 * time.Minute)
	latest := Snapshot{Timestamp: now.Add(-time.Minute), SwapIOAvailable: true, SwapThrashLatched: true, SwapThrashCause: SwapThrashCauseExternal, SwapThrashSince: since}
	var latch swapThrashLatch
	latch.resume(latest, now)
	snapshot := Snapshot{Timestamp: now.Add(30 * time.Second), PhysicalMemoryMB: 16 * 1024, SwapIOAvailable: true, SwapOutMBPerSecond: 300}
	transition, changed := latch.advance(&snapshot, DefaultPolicy(16*1024).Thresholds, defaultWorkLimits(10))
	// The first sample keeps the latch and the external proof, and records
	// the continuation once.
	if !snapshot.SwapThrashLatched || snapshot.SwapThrashCause != SwapThrashCauseExternal || !snapshot.SwapThrashSince.Equal(since) {
		t.Fatalf("resumed snapshot latched=%v cause=%q since=%v", snapshot.SwapThrashLatched, snapshot.SwapThrashCause, snapshot.SwapThrashSince)
	}
	if !changed || transition.State != SwapThrashEntered || !transition.Resumed || !transition.Since.Equal(since) {
		t.Fatalf("resume transition=%+v changed=%v", transition, changed)
	}
	snapshot.Timestamp = snapshot.Timestamp.Add(30 * time.Second)
	if _, changed = latch.advance(&snapshot, DefaultPolicy(16*1024).Thresholds, defaultWorkLimits(10)); changed {
		t.Fatal("continuation recorded twice")
	}
	// Stale or calm predecessors are not resumed.
	for _, stale := range []Snapshot{
		{Timestamp: now.Add(-10 * time.Minute), SwapIOAvailable: true, SwapThrashLatched: true},
		{Timestamp: now, SwapIOAvailable: true},
	} {
		var fresh swapThrashLatch
		fresh.resume(stale, now)
		if fresh.latched {
			t.Fatalf("resumed from %+v", stale)
		}
	}
	// History joins the two halves into one 30-minute episode.
	row := func(at time.Time, value SwapThrashTransition) swapThrashHistoryEvent {
		return swapThrashHistoryEvent{Timestamp: at, Event: SwapThrashTransitionEvent, SwapThrash: &value}
	}
	rows := []swapThrashHistoryEvent{
		row(since, SwapThrashTransition{State: SwapThrashEntered, Cause: SwapThrashCauseAgents, Since: since}),
		{Timestamp: now.Add(-2 * time.Minute), Event: "resident_stopped"},
		{Timestamp: now.Add(-90 * time.Second), Event: "resident_started"},
		row(now.Add(30*time.Second), SwapThrashTransition{State: SwapThrashEntered, Resumed: true, Cause: SwapThrashCauseAgents, Since: since}),
		row(now.Add(10*time.Minute), SwapThrashTransition{State: SwapThrashReleased, Cause: SwapThrashCauseAgents, Since: since, EpisodeSeconds: 1800}),
	}
	summary := summarizeSwapThrashEpisodes(rows, now.Add(time.Hour))
	if summary.Count != 1 || summary.Interrupted != 0 || summary.LatchedSeconds != 1800 || summary.Open {
		t.Fatalf("joined summary=%+v", summary)
	}
	// Without a resumed entry the restart ends the episode.
	rows = append(rows[:3:3], row(now.Add(5*time.Minute), SwapThrashTransition{State: SwapThrashEntered, Cause: SwapThrashCauseAgents, Since: now.Add(5 * time.Minute)}))
	summary = summarizeSwapThrashEpisodes(rows, now.Add(10*time.Minute))
	if summary.Count != 2 || summary.Interrupted != 1 || summary.LatchedSeconds != 18*60+5*60 {
		t.Fatalf("split summary=%+v", summary)
	}
}

// Seen in dogfood: the derate held a test before its lease and the agent's
// --wait ran out. The terminal event carried the label but no dimension, and
// all wait sat in admission_wait_ms, so the cost read zero.
func TestSwapDerateCostCountsPreLeaseAdmissionWait(t *testing.T) {
	base := time.Date(2026, 11, 3, 2, 0, 0, 0, time.UTC)
	stats := SummarizeWorkEvents([]WorkEvent{
		{SchemaVersion: WorkEventSchemaVersion, OperationID: "held", Event: WorkEventQueued, Class: WorkClassTest, Weight: 3, Timestamp: base, Blocker: WorkBlockerNone},
		{SchemaVersion: WorkEventSchemaVersion, OperationID: "held", Event: WorkEventCancelled, Class: WorkClassTest, Weight: 3, Timestamp: base.Add(12 * time.Second),
			AdmissionDecision: SwapThrashDeferredDecision, WaitMilliseconds: 12048, AdmissionWaitMilliseconds: 12048, Outcome: "interrupted"},
	}, base.Add(-time.Hour), base.Add(time.Minute))
	if cost := stats.SwapDerate; cost.DeferredOperations != 1 || cost.WaitTotalMS != 12048 || cost.Verdict != SwapDerateWithinBudget {
		t.Fatalf("pre-lease derate cost=%+v", cost)
	}
}

func TestPressureTimeoutTerminalEventNamesTheGate(t *testing.T) {
	dir := t.TempDir()
	coordinator := NewWorkCoordinator(dir, testWorkLimits())
	admissionCheck := func() Admission {
		return Admission{Allowed: false, Level: LevelRed, Reasons: []string{"host free memory 10% <= red 15%"}}
	}
	code, err := RunWorkCommand(coordinator,
		WorkRunOptions{Class: WorkClassBuild, Wait: 50 * time.Millisecond, Progress: WorkProgressQuiet, Command: []string{"/usr/bin/true"}},
		admissionCheck, time.Millisecond, WorkRunStreams{Stdout: io.Discard, Stderr: io.Discard})
	if err == nil || code == 0 {
		t.Fatalf("held work ran: code=%d err=%v", code, err)
	}
	events, readErr := NewWorkEventStore(dir).Read(WorkEventFilter{})
	if readErr != nil || len(events) == 0 {
		t.Fatalf("events=%+v err=%v", events, readErr)
	}
	terminal := events[len(events)-1]
	if (terminal.Event != WorkEventCancelled && terminal.Event != WorkEventFailed) || terminal.PressureDimension != "memory" || terminal.PressureLevel != LevelRed || terminal.PressureReason == "" {
		t.Fatalf("terminal event lost the gate: %+v", terminal)
	}
	if stats := SummarizeWorkEvents(events, time.Time{}, time.Now()); stats.ReviewSignals.MemoryDeferrals != 1 {
		t.Fatalf("memory deferrals=%d", stats.ReviewSignals.MemoryDeferrals)
	}
}
