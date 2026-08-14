package sessionpressure

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestReadHookSessionStateProjectsOnlySemanticFields(t *testing.T) {
	dir := t.TempDir()
	sessionID := "019f5be0-7d38-7271-ba7d-8ade4a407bf0"
	payload := map[string]any{
		"session_id": sessionID, "tool": "codex", "state": "ready",
		"last_user_prompt_at": int64(100), "last_stop_at": int64(101),
		"last_user_prompt": "private would-be prompt", "last_user_prompt_hash": "private hash",
	}
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, sessionID+".json"), body, 0o600); err != nil {
		t.Fatal(err)
	}
	state, at, ok := readHookSessionState(dir, sessionID, "codex")
	if !ok || state != SemanticStateReady || !at.Equal(time.Unix(101, 0).UTC()) {
		t.Fatalf("state=%q at=%v ok=%v", state, at, ok)
	}
	trees := []AgentTree{{Agent: "codex", SessionID: sessionID}}
	enrichSemanticStates(trees, dir)
	projection, err := json.Marshal(trees[0])
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(projection), "private") || !strings.Contains(string(projection), `"semantic_state":"ready"`) {
		t.Fatalf("unsafe or missing projection: %s", projection)
	}
}

func TestReadHookSessionStateRejectsUntrustedOrIncoherentFiles(t *testing.T) {
	dir := t.TempDir()
	write := func(id, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, id+".json"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("mismatch", `{"session_id":"other","tool":"codex","state":"ready","last_stop_at":2}`)
	write("wrong-tool", `{"session_id":"wrong-tool","tool":"claude","state":"ready","last_stop_at":2}`)
	write("stale-ready", `{"session_id":"stale-ready","tool":"codex","state":"ready","last_user_prompt_at":3,"last_stop_at":2}`)
	write("oversize", strings.Repeat("x", maxSessionStateBytes+1))
	for _, id := range []string{"mismatch", "wrong-tool", "stale-ready", "oversize"} {
		if state, _, ok := readHookSessionState(dir, id, "codex"); ok || state != SemanticStateUnknown {
			t.Fatalf("%s accepted: state=%q ok=%v", id, state, ok)
		}
	}
	if _, _, ok := readHookSessionState(dir, "../escape", "codex"); ok {
		t.Fatal("unsafe session ID accepted")
	}
}

func TestReliefSelectionPrefersReadyAndExcludesBusy(t *testing.T) {
	policy := DefaultPolicy(16 * 1024)
	trees := []AgentTree{
		{RootPID: 10, RSSSumMB: 1200, CPUAvailable: true, ElapsedSeconds: 5000, QuiescentSamples: policy.CriticalSustainSamples, SemanticState: SemanticStateBusy},
		{RootPID: 20, RSSSumMB: 1000, CPUAvailable: true, ElapsedSeconds: 5000, QuiescentSamples: policy.CriticalSustainSamples},
		{RootPID: 30, RSSSumMB: 700, CPUAvailable: true, ElapsedSeconds: 5000, QuiescentSamples: policy.CriticalSustainSamples, SemanticState: SemanticStateReady},
	}
	got, ok := selectReliefCandidate(trees, policy)
	if !ok || got.RootPID != 30 {
		t.Fatalf("candidate=%+v ok=%v", got, ok)
	}
	trees[2].SemanticState = SemanticStateBusy
	got, ok = selectReliefCandidate(trees, policy)
	if !ok || got.RootPID != 20 {
		t.Fatalf("unknown fallback candidate=%+v ok=%v", got, ok)
	}
}

func TestReliefSelectionRejectsUnavailableCPUEvidence(t *testing.T) {
	policy := DefaultPolicy(16 * 1024)
	for name, tree := range map[string]AgentTree{
		"unavailable": {RootPID: 10, RSSSumMB: 1200, CPUPercentSum: 0, CPUAvailable: false, ElapsedSeconds: 5000, QuiescentSamples: policy.CriticalSustainSamples},
		"nan":         {RootPID: 10, RSSSumMB: 1200, CPUPercentSum: math.NaN(), CPUAvailable: true, ElapsedSeconds: 5000, QuiescentSamples: policy.CriticalSustainSamples},
		"infinity":    {RootPID: 10, RSSSumMB: 1200, CPUPercentSum: math.Inf(1), CPUAvailable: true, ElapsedSeconds: 5000, QuiescentSamples: policy.CriticalSustainSamples},
	} {
		t.Run(name, func(t *testing.T) {
			if candidate, ok := selectReliefCandidate([]AgentTree{tree}, policy); ok {
				t.Fatalf("invalid CPU evidence acquired relief authority: %+v", candidate)
			}
		})
	}
}

func TestSemanticRevalidationFailsClosed(t *testing.T) {
	ready := AgentTree{RootPID: 42, SemanticState: SemanticStateReady}
	for name, current := range map[string]AgentTree{
		"became-busy": {RootPID: 42, SemanticState: SemanticStateBusy},
		"lost-ready":  {RootPID: 42, SemanticState: SemanticStateUnknown},
	} {
		t.Run(name, func(t *testing.T) {
			if err := validateSemanticRevalidation(ready, current); err == nil {
				t.Fatal("semantic drift accepted")
			}
		})
	}
	if err := validateSemanticRevalidation(AgentTree{RootPID: 42}, AgentTree{RootPID: 42}); err != nil {
		t.Fatalf("unknown fallback rejected: %v", err)
	}
}
