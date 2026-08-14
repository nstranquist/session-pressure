package sessionpressurecmd

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nstranquist/session-pressure/internal/operationcontract"
	"github.com/nstranquist/session-pressure/sessionpressure"
)

func installPressureIdleApplyFakes(
	t *testing.T,
	appendAction func(*sessionpressure.TelemetryStore, sessionpressure.Action) error,
	reap func(context.Context, *sessionpressure.Sampler, sessionpressure.Policy, sessionpressure.IdleCandidate, sessionpressure.IdleCriteria) (sessionpressure.Action, error),
) {
	t.Helper()
	previousAppend := appendPressureAction
	previousReap := reapPressureIdleTree
	appendPressureAction = appendAction
	reapPressureIdleTree = reap
	t.Cleanup(func() {
		appendPressureAction = previousAppend
		reapPressureIdleTree = previousReap
	})
}

func seedPressureIdleApply(t *testing.T) (sessionID string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv(sessionpressure.DataDirEnv, dir)
	policy := sessionpressure.DefaultPolicy(16 * 1024)
	if err := sessionpressure.SavePolicy(sessionpressure.PolicyPath(dir), policy); err != nil {
		t.Fatal(err)
	}
	sessionID = "019f5be0-7d38-7271-ba7d-8ade4a407bf0"
	installFakePressureSampler(t, func(context.Context, *sessionpressure.Sampler, sessionpressure.Policy) (sessionpressure.Snapshot, error) {
		return sessionpressure.Snapshot{
			SchemaVersion: 1, Timestamp: time.Now().UTC(), Level: sessionpressure.LevelNormal,
			PhysicalMemoryMB: 16 * 1024, FreePercent: 50,
			ProcessInventoryAvailable: true, ProcessInventoryFresh: true, AgentTreeCount: 1,
			TopAgentTrees: []sessionpressure.AgentTree{{
				Agent: "codex", Executable: "codex", RootPID: 4242, SessionID: sessionID,
				ElapsedSeconds: int64(24 * time.Hour / time.Second), CPUPercentSum: 0.1, CPUAvailable: true,
				RSSSumMB: 512, ProcessCount: 2, PIDs: []int{4242, 4243},
			}},
			GuardRole: "operator", GuardBudgetOK: true, SampleDurationMS: 1, SampleCPUTimeMS: 1,
		}, nil
	})
	return sessionID
}

func TestCmdSessionPressureIdleRejectsNonFiniteCPUCeilings(t *testing.T) {
	for _, value := range []string{"NaN", "+Inf", "-Inf"} {
		rc, _, stderr := captureMainOutput(t, func() int {
			return cmdSessionPressureIdle(&Flags{JSON: true}, []string{"--max-cpu", value})
		})
		if rc != 2 || !strings.Contains(stderr, "CPU ceiling") {
			t.Fatalf("--max-cpu %s rc=%d stderr=%q", value, rc, stderr)
		}
	}
}

func TestCmdSessionPressureIdleRefusesToReapWithoutDurableIntent(t *testing.T) {
	sessionID := seedPressureIdleApply(t)
	reapCalls := 0
	installPressureIdleApplyFakes(t,
		func(*sessionpressure.TelemetryStore, sessionpressure.Action) error {
			return errors.New("fixture audit store unavailable")
		},
		func(context.Context, *sessionpressure.Sampler, sessionpressure.Policy, sessionpressure.IdleCandidate, sessionpressure.IdleCriteria) (sessionpressure.Action, error) {
			reapCalls++
			return sessionpressure.Action{}, nil
		},
	)
	rc, _, stderr := captureMainOutput(t, func() int {
		return cmdSessionPressureIdle(nil, []string{"--apply", "--root-pid", "4242", "--session-id", sessionID})
	})
	if rc != 1 || reapCalls != 0 || !strings.Contains(stderr, "intent could not be persisted") {
		t.Fatalf("rc=%d reap_calls=%d stderr=%q", rc, reapCalls, stderr)
	}
}

func TestCmdSessionPressureIdlePersistsIntentBeforeReapAndResultAfter(t *testing.T) {
	sessionID := seedPressureIdleApply(t)
	var actions []sessionpressure.Action
	installPressureIdleApplyFakes(t,
		func(_ *sessionpressure.TelemetryStore, action sessionpressure.Action) error {
			actions = append(actions, action)
			return nil
		},
		func(_ context.Context, _ *sessionpressure.Sampler, _ sessionpressure.Policy, candidate sessionpressure.IdleCandidate, _ sessionpressure.IdleCriteria) (sessionpressure.Action, error) {
			if len(actions) != 1 || actions[0].Kind != "manual_idle_tree_reap_intent" || actions[0].Result != "intent_recorded" {
				t.Fatalf("reap reached before durable intent: %+v", actions)
			}
			return sessionpressure.Action{
				Kind: "manual_idle_tree_reap", Level: sessionpressure.LevelNormal,
				RootPID: candidate.RootPID, Agent: candidate.Agent, SessionID: candidate.SessionID,
				Signal: "SIGTERM", Result: "tree_exit_confirmed",
			}, nil
		},
	)
	rc, stdout, stderr := captureMainOutput(t, func() int {
		return cmdSessionPressureIdle(&Flags{JSON: true}, []string{"--apply", "--root-pid", "4242", "--session-id", sessionID})
	})
	if rc != 0 || stderr != "" {
		t.Fatalf("rc=%d stdout=%q stderr=%q", rc, stdout, stderr)
	}
	if err := operationcontract.ValidateOutputJSON(operationcontract.All(), "nicos.session.pressure-result.v1", []byte(stdout)); err != nil {
		t.Fatalf("idle apply output contract: %v\n%s", err, stdout)
	}
	if len(actions) != 2 || actions[1].Kind != "manual_idle_tree_reap" || actions[1].Result != "tree_exit_confirmed" {
		t.Fatalf("unexpected durable action sequence: %+v", actions)
	}
}

func TestCmdSessionPressureIdleReportsResultPersistenceFailureAfterDurableIntent(t *testing.T) {
	sessionID := seedPressureIdleApply(t)
	appendCalls := 0
	reapCalls := 0
	installPressureIdleApplyFakes(t,
		func(_ *sessionpressure.TelemetryStore, action sessionpressure.Action) error {
			appendCalls++
			if appendCalls == 1 && action.Kind == "manual_idle_tree_reap_intent" {
				return nil
			}
			return errors.New("fixture final audit unavailable")
		},
		func(_ context.Context, _ *sessionpressure.Sampler, _ sessionpressure.Policy, candidate sessionpressure.IdleCandidate, _ sessionpressure.IdleCriteria) (sessionpressure.Action, error) {
			reapCalls++
			return sessionpressure.Action{
				SchemaVersion: sessionpressure.SchemaVersion, Kind: "manual_idle_tree_reap", Level: sessionpressure.LevelNormal,
				RootPID: candidate.RootPID, Agent: candidate.Agent, SessionID: candidate.SessionID,
				Signal: "SIGTERM", Result: "tree_exit_confirmed",
			}, nil
		},
	)
	rc, _, stderr := captureMainOutput(t, func() int {
		return cmdSessionPressureIdle(nil, []string{"--apply", "--root-pid", "4242", "--session-id", sessionID})
	})
	if rc != 1 || reapCalls != 1 || appendCalls != 2 || !strings.Contains(stderr, "durable intent remains recorded") {
		t.Fatalf("rc=%d reap_calls=%d append_calls=%d stderr=%q", rc, reapCalls, appendCalls, stderr)
	}
}
