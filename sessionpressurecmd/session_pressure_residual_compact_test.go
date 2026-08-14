package sessionpressurecmd

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nstranquist/session-pressure/sessionpressure"
)

func TestWorkHistoryJSONDefaultsToCompactProjection(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(sessionpressure.DataDirEnv, dir)
	store := sessionpressure.NewWorkEventStore(dir)
	now := time.Now().UTC()
	exit := 0
	event := sessionpressure.WorkEvent{
		SchemaVersion:    sessionpressure.WorkEventSchemaVersion,
		Timestamp:        now,
		Event:            sessionpressure.WorkEventCompleted,
		OperationID:      "00000000000000000000000000000001",
		LeaseID:          "11111111111111111111111111111111",
		Class:            sessionpressure.WorkClassTest,
		Weight:           2,
		CommandDigest:    "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		Outcome:          "successful",
		ExitCode:         &exit,
		WaitMilliseconds: 10,
		RuntimeMillis:    20,
	}
	if err := store.AppendDurable(event); err != nil {
		t.Fatal(err)
	}
	rc, stdout, stderr := captureMainOutput(t, func() int {
		return cmdSessionPressureWork(&Flags{JSON: true}, []string{"history", "--limit", "5"})
	})
	if rc != 0 || stderr != "" {
		t.Fatalf("rc=%d stderr=%q stdout=%q", rc, stderr, stdout)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
		t.Fatal(err)
	}
	if payload["output_scope"] != "compact" {
		t.Fatalf("scope=%v", payload["output_scope"])
	}
	if strings.Contains(stdout, "command_digest") || strings.Contains(stdout, "deadbeef") {
		t.Fatalf("compact history retained digests: %s", stdout)
	}
	events, _ := payload["work_events"].([]any)
	if len(events) != 1 {
		t.Fatalf("events=%v", payload["work_events"])
	}
	row, _ := events[0].(map[string]any)
	if row["operation_id"] != event.OperationID || row["outcome"] != "successful" {
		t.Fatalf("row=%v", row)
	}

	rc, fullOut, _ := captureMainOutput(t, func() int {
		return cmdSessionPressureWork(&Flags{JSON: true}, []string{"history", "--limit", "5", "--full"})
	})
	if rc != 0 {
		t.Fatalf("full rc=%d", rc)
	}
	var fullPayload map[string]any
	if err := json.Unmarshal([]byte(fullOut), &fullPayload); err != nil {
		t.Fatal(err)
	}
	if fullPayload["output_scope"] != "full" || !strings.Contains(fullOut, "command_digest") {
		t.Fatalf("full history missing digests/scope: %s", fullOut)
	}
}

func TestCompactPressureAuditReportDropsMetricsKeepsFindings(t *testing.T) {
	report := pressureAuditReport{
		SchemaVersion: 2, OK: false, Overall: pressureAuditWarn,
		Categories: []pressureAuditCategory{{
			ID: "scheduler", Status: pressureAuditWarn, Summary: "slowdown breached",
			Metrics:  map[string]any{"huge": strings.Repeat("x", 200), "samples": []any{1, 2, 3}},
			Findings: []pressureAuditFinding{{Severity: pressureAuditWarn, Code: "slowdown", Message: "build p95 high"}},
		}},
	}
	compact := projectCompactPressureAudit(report)
	if compact.Overall != pressureAuditWarn || len(compact.Categories) != 1 || compact.Categories[0].ID != "scheduler" {
		t.Fatalf("compact=%+v", compact)
	}
	if len(compact.Categories[0].Findings) != 1 || compact.Categories[0].Findings[0].Code != "slowdown" {
		t.Fatalf("findings lost: %+v", compact.Categories[0])
	}
	raw, _ := json.Marshal(compact)
	if strings.Contains(string(raw), "huge") || strings.Contains(string(raw), "metrics") {
		t.Fatalf("metrics leaked into compact audit: %s", raw)
	}
}
