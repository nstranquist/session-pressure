package sessionpressurecmd

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/nstranquist/session-pressure/sessionpressure"
)

func TestPressureWorkEvaluateUsesRequestedWindow(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(sessionpressure.DataDirEnv, dir)
	policy := sessionpressure.DefaultPolicy(16 * 1024)
	if err := sessionpressure.SavePolicy(sessionpressure.PolicyPath(dir), policy); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	store := sessionpressure.NewWorkEventStore(dir)
	for i, age := range []time.Duration{48 * time.Hour, time.Hour} {
		at := now.Add(-age)
		id := fmt.Sprintf("%032x", i+1)
		for _, event := range []sessionpressure.WorkEvent{
			{Event: sessionpressure.WorkEventQueued, OperationID: id, Class: sessionpressure.WorkClassTest, Weight: policy.WorkLimits.TestWeight, Timestamp: at},
			{Event: sessionpressure.WorkEventCompleted, OperationID: id, LeaseID: id, Class: sessionpressure.WorkClassTest, Weight: policy.WorkLimits.TestWeight, Timestamp: at.Add(time.Second), RuntimeMillis: 1000, Outcome: "completed"},
		} {
			if err := store.AppendDurable(event); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, tc := range []struct {
		name   string
		args   []string
		count  int
		window string
	}{
		{"default", []string{"evaluate"}, 1, "24h0m0s"},
		{"seven-day", []string{"evaluate", "--since", "168h"}, 2, "168h0m0s"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rc, out, stderr := captureMainOutput(t, func() int { return cmdSessionPressureWork(&Flags{JSON: true}, tc.args) })
			// Two operations cannot satisfy promotion sample requirements.
			if rc != 1 || stderr != "" {
				t.Fatalf("rc=%d stderr=%q out=%s", rc, stderr, out)
			}
			var payload struct {
				Window     string                               `json:"window"`
				Since      time.Time                            `json:"since"`
				Evaluation sessionpressure.WorkEvaluationReport `json:"work_evaluation"`
			}
			if err := json.Unmarshal([]byte(out), &payload); err != nil {
				t.Fatal(err)
			}
			if payload.Window != tc.window || payload.Since.IsZero() || payload.Evaluation.Replay == nil || payload.Evaluation.Replay.Candidate.Operations != tc.count {
				t.Fatalf("wrong window/population: %s", out)
			}
		})
	}
}

func TestPressureWorkEvaluateRejectsInvalidWindow(t *testing.T) {
	t.Setenv(sessionpressure.DataDirEnv, t.TempDir())
	for _, args := range [][]string{{"--since"}, {"--since", "0s"}, {"--since", "-1h"}, {"--since", "7d"}, {"--since", "1h", "extra"}, {"--full"}} {
		rc, _, stderr := captureMainOutput(t, func() int { return cmdSessionPressureWork(&Flags{JSON: true}, append([]string{"evaluate"}, args...)) })
		if rc != 2 || !strings.Contains(stderr, "work evaluate") {
			t.Fatalf("args=%v rc=%d stderr=%q", args, rc, stderr)
		}
	}
}
