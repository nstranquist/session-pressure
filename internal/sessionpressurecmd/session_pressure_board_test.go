package sessionpressurecmd

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nstranquist/session-pressure/internal/sessionpressure"
)

// TestPressureBoardReturnsOneCompositeRead is the whole point of the verb: a UI
// client should get status, work, and admission from a single process instead of
// one cold start per contract.
func TestPressureBoardReturnsOneCompositeRead(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(sessionpressure.DataDirEnv, dir)
	policy := sessionpressure.DefaultPolicy(16 * 1024)
	if err := sessionpressure.SavePolicy(sessionpressure.PolicyPath(dir), policy); err != nil {
		t.Fatal(err)
	}
	coordinator := sessionpressure.NewWorkCoordinator(dir, policy.WorkLimits)
	waiter, _, err := coordinator.RegisterWaiter(context.Background(), sessionpressure.WorkClassTest, "00000000000000000000000000000001")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = waiter.Cancel(context.Background()) }()

	_, stdout, _ := captureMainOutput(t, func() int {
		return cmdSessionPressure(&Flags{JSON: true}, []string{"board"})
	})
	var payload struct {
		Action          string                     `json:"action"`
		OutputScope     string                     `json:"output_scope"`
		Work            sessionpressure.WorkStatus `json:"work"`
		Admission       map[string]any             `json:"admission"`
		Health          map[string]any             `json:"health"`
		Coverage        map[string]any             `json:"coverage"`
		CoverageSummary map[string]any             `json:"coverage_summary"`
		Launchd         map[string]any             `json:"launchd"`
		LaunchdSummary  map[string]any             `json:"launchd_summary"`
		Doctor          map[string]any             `json:"doctor"`
		Policy          map[string]any             `json:"policy"`
	}
	if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
		t.Fatalf("board payload=%q err=%v", stdout, err)
	}
	if payload.Action != "board" {
		t.Fatalf("action=%q", payload.Action)
	}
	if payload.OutputScope != "compact" {
		t.Fatalf("output_scope=%q want compact", payload.OutputScope)
	}
	if payload.Work.QueueDepth != 1 || len(payload.Work.Waiters) != 1 {
		t.Fatalf("board work section=%+v", payload.Work)
	}
	if payload.Admission == nil || payload.Health == nil {
		t.Fatalf("board omitted an always-on section: %q", stdout)
	}
	if payload.Admission["snapshot"] != nil {
		t.Fatalf("compact board retained admission.snapshot: %q", stdout)
	}
	if payload.Coverage != nil || payload.CoverageSummary == nil {
		t.Fatalf("compact board coverage projection missing: %q", stdout)
	}
	if payload.Launchd != nil || payload.LaunchdSummary == nil {
		t.Fatalf("compact board launchd projection missing: %q", stdout)
	}
	// Opt-in sections stay absent unless requested, so a menu-bar-only client
	// keeps paying the cheapest read.
	if payload.Doctor != nil || payload.Policy != nil {
		t.Fatalf("board included an unrequested section: %q", stdout)
	}

	_, fullOut, _ := captureMainOutput(t, func() int {
		return cmdSessionPressure(&Flags{JSON: true}, []string{"board", "--full"})
	})
	var fullPayload struct {
		OutputScope     string         `json:"output_scope"`
		Coverage        map[string]any `json:"coverage"`
		CoverageSummary map[string]any `json:"coverage_summary"`
		Launchd         map[string]any `json:"launchd"`
		Admission       map[string]any `json:"admission"`
	}
	if err := json.Unmarshal([]byte(fullOut), &fullPayload); err != nil {
		t.Fatal(err)
	}
	if fullPayload.OutputScope != "full" || fullPayload.Coverage == nil || fullPayload.Launchd == nil || fullPayload.CoverageSummary != nil {
		t.Fatalf("full board projection=%q", fullOut)
	}

	_, stdout, _ = captureMainOutput(t, func() int {
		return cmdSessionPressure(&Flags{JSON: true}, []string{"board", "--include", "policy"})
	})
	if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Policy == nil {
		t.Fatalf("requested policy section missing: %q", stdout)
	}

	rc, _, stderr := captureMainOutput(t, func() int {
		return cmdSessionPressure(&Flags{JSON: true}, []string{"board", "--include", "nonsense"})
	})
	if rc != 2 || !strings.Contains(stderr, "unknown board section") {
		t.Fatalf("unknown section rc=%d stderr=%q", rc, stderr)
	}
}

// TestPressureBoardMissingVerbDetection guards the app's fallback trigger: only
// an unknown-subcommand rejection may downgrade to the per-contract fan-out.
func TestPressureBoardRejectsUnknownArgument(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(sessionpressure.DataDirEnv, dir)
	rc, _, stderr := captureMainOutput(t, func() int {
		return cmdSessionPressure(&Flags{JSON: true}, []string{"board", "--nope"})
	})
	if rc != 2 || !strings.Contains(stderr, "unknown board argument") {
		t.Fatalf("rc=%d stderr=%q", rc, stderr)
	}
}

func TestPressureWorkOverrideClearReleasesPins(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(sessionpressure.DataDirEnv, dir)
	policy := sessionpressure.DefaultPolicy(16 * 1024)
	if err := sessionpressure.SavePolicy(sessionpressure.PolicyPath(dir), policy); err != nil {
		t.Fatal(err)
	}
	coordinator := sessionpressure.NewWorkCoordinator(dir, policy.WorkLimits)
	operationID := "00000000000000000000000000000001"
	waiter, _, err := coordinator.RegisterWaiter(context.Background(), sessionpressure.WorkClassTest, operationID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = waiter.Cancel(context.Background()) }()

	rc, _, stderr := captureMainOutput(t, func() int {
		return cmdSessionPressureWork(&Flags{JSON: true}, []string{"override", "--clear", "--all", "--confirm"})
	})
	if rc != 2 || !strings.Contains(stderr, "takes no --all") {
		t.Fatalf("clear with --all rc=%d stderr=%q", rc, stderr)
	}
	if rc, _, _ := captureMainOutput(t, func() int {
		return cmdSessionPressureWork(&Flags{JSON: true}, []string{"override", "--all", "--confirm"})
	}); rc != 0 {
		t.Fatalf("pin rc=%d", rc)
	}
	rc, stdout, stderr := captureMainOutput(t, func() int {
		return cmdSessionPressureWork(&Flags{JSON: true}, []string{"override", "--clear", "--confirm"})
	})
	if rc != 0 || stderr != "" {
		t.Fatalf("clear rc=%d stderr=%q", rc, stderr)
	}
	var payload struct {
		Cleared int                        `json:"cleared"`
		Pinned  int                        `json:"pinned"`
		Work    sessionpressure.WorkStatus `json:"work"`
	}
	if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Cleared != 1 || payload.Pinned != 0 || payload.Work.OverrideOperationID != "" || payload.Work.OverrideQueueDepth != 0 {
		t.Fatalf("clear payload=%+v", payload)
	}
	// Clearing an already-clear queue must report that, not a fake success.
	rc, _, stderr = captureMainOutput(t, func() int {
		return cmdSessionPressureWork(&Flags{JSON: true}, []string{"override", "--clear", "--confirm"})
	})
	if rc == 0 || !strings.Contains(stderr, "no operator promotion sequence is pinned") {
		t.Fatalf("second clear rc=%d stderr=%q", rc, stderr)
	}
}

func TestPressureWorkHistoryFiltersByOperationID(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(sessionpressure.DataDirEnv, dir)
	policy := sessionpressure.DefaultPolicy(16 * 1024)
	if err := sessionpressure.SavePolicy(sessionpressure.PolicyPath(dir), policy); err != nil {
		t.Fatal(err)
	}
	// Two complete lifecycles in the ledger: the filter has to separate them.
	store := sessionpressure.NewWorkEventStore(dir)
	now := time.Now().UTC().Add(-time.Hour)
	wanted := "00000000000000000000000000000002"
	for index, operationID := range []string{"00000000000000000000000000000001", wanted} {
		leaseID := "1111111111111111111111111111111" + strconv.Itoa(index+1)
		for _, eventType := range []sessionpressure.WorkEventType{
			sessionpressure.WorkEventQueued,
			sessionpressure.WorkEventAcquired,
			sessionpressure.WorkEventStarted,
			sessionpressure.WorkEventCompleted,
		} {
			event := sessionpressure.WorkEvent{
				SchemaVersion: sessionpressure.WorkEventSchemaVersion,
				Timestamp:     now,
				Event:         eventType,
				OperationID:   operationID,
				Class:         sessionpressure.WorkClassTest,
				Weight:        2,
			}
			switch eventType {
			case sessionpressure.WorkEventAcquired, sessionpressure.WorkEventStarted, sessionpressure.WorkEventCompleted:
				event.LeaseID = leaseID
			}
			if eventType == sessionpressure.WorkEventCompleted {
				event.Outcome = "successful"
			}
			if err := store.AppendDurable(event); err != nil {
				t.Fatal(err)
			}
		}
	}

	_, unfiltered, _ := captureMainOutput(t, func() int {
		return cmdSessionPressureWork(&Flags{JSON: true}, []string{"history"})
	})
	var all struct {
		Count int `json:"work_event_count"`
	}
	if err := json.Unmarshal([]byte(unfiltered), &all); err != nil {
		t.Fatal(err)
	}
	if all.Count != 8 {
		t.Fatalf("unfiltered history=%d, want both lifecycles: %q", all.Count, unfiltered)
	}

	_, stdout, _ := captureMainOutput(t, func() int {
		return cmdSessionPressureWork(&Flags{JSON: true}, []string{"history", "--operation-id", wanted})
	})
	var payload struct {
		Events []sessionpressure.WorkEvent `json:"work_events"`
		Count  int                         `json:"work_event_count"`
	}
	if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Count != 4 {
		t.Fatalf("filtered history=%d, want one lifecycle: %q", payload.Count, stdout)
	}
	for _, event := range payload.Events {
		if event.OperationID != wanted {
			t.Fatalf("history filter leaked %+v", event)
		}
	}
}
