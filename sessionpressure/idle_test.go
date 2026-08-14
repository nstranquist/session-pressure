package sessionpressure

import (
	"context"
	"errors"
	"math"
	"strings"
	"testing"
	"time"
)

func TestIdleCriteriaRejectsNonFiniteCPUCeilings(t *testing.T) {
	for _, value := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		criteria := DefaultIdleCriteria()
		criteria.MaxCPUPercent = value
		if err := criteria.Validate(); err == nil || !strings.Contains(err.Error(), "CPU ceiling") {
			t.Fatalf("CPU ceiling %v unexpectedly accepted: %v", value, err)
		}
	}
}

func TestNewIdleReapIntentIsPromptFreeAndTargetBound(t *testing.T) {
	now := time.Date(2026, 7, 14, 15, 0, 0, 0, time.UTC)
	candidate := IdleCandidate{
		Agent: "codex", RootPID: 42, SessionID: "session-42", RSSSumMB: 512,
	}
	intent := NewIdleReapIntent(candidate, LevelWarning, now)
	if intent.SchemaVersion != SchemaVersion || intent.Timestamp != now || intent.Kind != "manual_idle_tree_reap_intent" || intent.Result != "intent_recorded" ||
		intent.Level != LevelWarning || intent.RootPID != 42 || intent.Agent != "codex" || intent.SessionID != "session-42" ||
		intent.Signal != "" {
		t.Fatalf("unexpected idle reap intent: %+v", intent)
	}
}

func TestInspectIdleTreesIsBoundedAndExcludesCallerTree(t *testing.T) {
	snapshot := Snapshot{
		ProcessInventoryAvailable: true,
		ProcessInventoryFresh:     true,
		TopAgentTrees: []AgentTree{
			{Agent: "codex", Executable: "codex", RootPID: 10, SessionID: "caller", ElapsedSeconds: int64(24 * time.Hour / time.Second), CPUPercentSum: 0, CPUAvailable: true, PIDs: []int{10, 999}},
			{Agent: "claude", Executable: "claude", RootPID: 20, SessionID: "oldest", ElapsedSeconds: int64(30 * time.Hour / time.Second), CPUPercentSum: 0.1, CPUAvailable: true, RSSSumMB: 300, PIDs: []int{20}},
			{Agent: "codex", Executable: "codex", RootPID: 30, SessionID: "older", ElapsedSeconds: int64(20 * time.Hour / time.Second), CPUPercentSum: 0.2, CPUAvailable: true, RSSSumMB: 500, PIDs: []int{30}},
			{Agent: "kimi", Executable: "kimi", RootPID: 40, SessionID: "active", ElapsedSeconds: int64(40 * time.Hour / time.Second), CPUPercentSum: 2, CPUAvailable: true, PIDs: []int{40}},
		},
	}
	criteria := DefaultIdleCriteria()
	criteria.Limit = 1
	inventory, err := InspectIdleTrees(snapshot, criteria, 999)
	if err != nil {
		t.Fatal(err)
	}
	if inventory.CandidateCount != 2 || inventory.ReturnedCount != 1 || !inventory.Truncated {
		t.Fatalf("unexpected bounded inventory: %+v", inventory)
	}
	if got := inventory.Candidates[0].SessionID; got != "oldest" {
		t.Fatalf("first candidate = %q, want oldest", got)
	}
}

func TestInspectIdleTreesRequiresFreshInventoryAndSessionIdentity(t *testing.T) {
	criteria := DefaultIdleCriteria()
	if _, err := InspectIdleTrees(Snapshot{ProcessInventoryAvailable: true}, criteria, 0); err == nil {
		t.Fatal("stale inventory unexpectedly accepted")
	}
	snapshot := Snapshot{
		ProcessInventoryAvailable: true,
		ProcessInventoryFresh:     true,
		TopAgentTrees:             []AgentTree{{Agent: "codex", RootPID: 10, ElapsedSeconds: int64(24 * time.Hour / time.Second)}},
	}
	inventory, err := InspectIdleTrees(snapshot, criteria, 0)
	if err != nil || inventory.CandidateCount != 0 {
		t.Fatalf("sessionless tree must not be actionable: inventory=%+v err=%v", inventory, err)
	}
}

func TestInspectIdleTreesRejectsInvalidTreeCPUEvidence(t *testing.T) {
	criteria := DefaultIdleCriteria()
	for _, value := range []float64{math.NaN(), math.Inf(1), math.Inf(-1), -0.1} {
		snapshot := Snapshot{
			ProcessInventoryAvailable: true,
			ProcessInventoryFresh:     true,
			TopAgentTrees: []AgentTree{{
				Agent: "codex", Executable: "codex", RootPID: 10, SessionID: "session-10",
				ElapsedSeconds: int64(24 * time.Hour / time.Second), CPUPercentSum: value, CPUAvailable: true, PIDs: []int{10},
			}},
		}
		inventory, err := InspectIdleTrees(snapshot, criteria, 0)
		if err != nil || inventory.CandidateCount != 0 {
			t.Fatalf("CPU evidence %v must not be eligible: inventory=%+v err=%v", value, inventory, err)
		}
	}
}

func TestInspectIdleTreesRejectsUnavailableCPUEvidence(t *testing.T) {
	snapshot := Snapshot{
		ProcessInventoryAvailable: true,
		ProcessInventoryFresh:     true,
		TopAgentTrees: []AgentTree{{
			Agent: "codex", Executable: "codex", RootPID: 10, SessionID: "session-10",
			ElapsedSeconds: int64(24 * time.Hour / time.Second), CPUPercentSum: 0, CPUAvailable: false, PIDs: []int{10},
		}},
	}
	inventory, err := InspectIdleTrees(snapshot, DefaultIdleCriteria(), 0)
	if err != nil || inventory.CandidateCount != 0 {
		t.Fatalf("unavailable CPU evidence must fail closed: inventory=%+v err=%v", inventory, err)
	}
}

func TestReapIdleTreeRejectsRenewedActivityOnFinalSample(t *testing.T) {
	const sessionID = "019f5be0-7d38-7271-ba7d-8ade4a407bf0"
	expectedTree := AgentTree{
		Agent: "codex", Executable: "codex", RootPID: 20, SessionID: sessionID,
		ElapsedSeconds: int64(13 * time.Hour / time.Second), CPUPercentSum: 0.1, PIDs: []int{20, 21},
	}
	expected := idleCandidate(expectedTree)
	sampler := testSampler(time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC))
	sampler.role = "operator"
	sampler.runner = fixtureRunner{
		ps:       "20 1 1000 1.5 13:00:05 codex resume " + sessionID + "\n21 20 1000 0.2 13:00:04 helper\n",
		pressure: "System-wide memory free percentage: 50%", swap: "vm.swapusage: total = 0.00M used = 0.00M free = 0.00M", physical: "17179869184",
	}
	criteria := DefaultIdleCriteria()
	criteria.MinAge = time.Hour
	action, err := ReapIdleTree(context.Background(), sampler, DefaultPolicy(16*1024), expected, criteria)
	if err == nil || action.Signal != "" || action.Result != "revalidation_rejected" || !strings.Contains(err.Error(), "became active") {
		t.Fatalf("renewed activity must fail closed: action=%+v err=%v", action, err)
	}
}

func TestReapIdleTreeRejectsNonFiniteCPUOnFinalSample(t *testing.T) {
	const sessionID = "019f5be0-7d38-7271-ba7d-8ade4a407bf0"
	expectedTree := AgentTree{
		Agent: "codex", Executable: "codex", RootPID: 20, SessionID: sessionID,
		ElapsedSeconds: int64(13 * time.Hour / time.Second), CPUPercentSum: 0.1, PIDs: []int{20},
	}
	sampler := testSampler(time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC))
	sampler.role = "operator"
	sampler.runner = fixtureRunner{
		ps:       "20 1 1000 NaN 13:00:05 codex resume " + sessionID + "\n",
		pressure: "System-wide memory free percentage: 50%", swap: "vm.swapusage: total = 0.00M used = 0.00M free = 0.00M", physical: "17179869184",
	}
	criteria := DefaultIdleCriteria()
	criteria.MinAge = time.Hour
	action, err := ReapIdleTree(context.Background(), sampler, DefaultPolicy(16*1024), idleCandidate(expectedTree), criteria)
	if err == nil || action.Signal != "" || action.Result != "revalidation_rejected" || !strings.Contains(err.Error(), "invalid CPU evidence") {
		t.Fatalf("non-finite CPU must fail closed: action=%+v err=%v", action, err)
	}
}

func TestReapIdleTreeSampleFailureKeepsValidActionEnvelope(t *testing.T) {
	expectedTree := AgentTree{
		Agent: "codex", Executable: "codex", RootPID: 20, SessionID: "session-20",
		ElapsedSeconds: int64(13 * time.Hour / time.Second), CPUPercentSum: 0.1, PIDs: []int{20},
	}
	sampler := testSampler(time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC))
	sampler.runner = fixtureRunner{err: errors.New("fixture unavailable")}
	criteria := DefaultIdleCriteria()
	criteria.MinAge = time.Hour
	action, err := ReapIdleTree(context.Background(), sampler, DefaultPolicy(16*1024), idleCandidate(expectedTree), criteria)
	if err == nil || action.SchemaVersion != SchemaVersion || action.Level != LevelNormal || action.RevalidatedLevel != "" ||
		action.Result != "error" || action.Signal != "" {
		t.Fatalf("sample failure produced invalid action envelope: action=%+v err=%v", action, err)
	}
}
