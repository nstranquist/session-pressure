package sessionpressure

import (
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"
)

func TestAttachWrapperInterruptToSummaryUnderBudget(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 7, 20, 21, 0, 0, 0, time.UTC)
	policy := DefaultPolicy(16 * 1024)
	policy.ResourceBudgets.MaxTelemetryBytesDay = 1 << 20
	if err := SavePolicy(PolicyPath(dir), policy); err != nil {
		t.Fatal(err)
	}
	work := NewWorkEventStore(dir)
	work.Now = func() time.Time { return now }
	for i, outcome := range []string{"wrapper_interrupt", "agent_kill", "user_abort"} {
		event := WorkEvent{
			SchemaVersion: WorkEventSchemaVersion,
			EventID:       hex32(i + 1),
			OperationID:   hex32(100 + i),
			Event:         WorkEventCancelled,
			Class:         WorkClassTest,
			Weight:        1,
			Outcome:       outcome,
			Timestamp:     now.Add(-time.Duration(i) * time.Minute),
		}
		if err := work.AppendDurable(event); err != nil {
			t.Fatalf("AppendDurable: %v", err)
		}
	}
	tel := NewTelemetryStore(dir)
	tel.Now = func() time.Time { return now }
	summary := compactTelemetrySummary(Snapshot{Timestamp: now, Level: LevelNormal, GuardBudgetOK: true})
	attachWrapperInterruptToSummary(&summary, tel, policy, now)
	if summary.WrapperInterruptOperations != 2 {
		t.Fatalf("wrapper_interrupt_operations=%d want 2 summary=%+v", summary.WrapperInterruptOperations, summary)
	}
	if summary.WrapperInterruptRatePerHour <= 0 {
		t.Fatalf("rate=%v", summary.WrapperInterruptRatePerHour)
	}
}

func TestHeartbeatAppendEventIncludesWrapperInterrupts(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 7, 20, 21, 30, 0, 0, time.UTC)
	policy := DefaultPolicy(16 * 1024)
	policy.ResourceBudgets.MaxTelemetryBytesDay = 1 << 20
	if err := SavePolicy(PolicyPath(dir), policy); err != nil {
		t.Fatal(err)
	}
	work := NewWorkEventStore(dir)
	work.Now = func() time.Time { return now }
	if err := work.AppendDurable(WorkEvent{
		SchemaVersion: WorkEventSchemaVersion, EventID: hex32(9), OperationID: hex32(10),
		Event: WorkEventCancelled, Class: WorkClassBuild, Weight: 1, Outcome: "wrapper_interrupt", Timestamp: now,
	}); err != nil {
		t.Fatal(err)
	}
	tel := NewTelemetryStore(dir)
	tel.Now = func() time.Time { return now }
	snap := Snapshot{SchemaVersion: SchemaVersion, Timestamp: now, Level: LevelNormal, FreePercent: 50, GuardBudgetOK: true}
	if err := tel.AppendEvent(TelemetryEvent{Timestamp: now, Event: "heartbeat", Snapshot: &snap}); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(tel.dayPath(now))
	if err != nil {
		t.Fatal(err)
	}
	lines := splitJSONL(body)
	if len(lines) == 0 {
		t.Fatal("empty telemetry day file")
	}
	var event TelemetryEvent
	if err := json.Unmarshal(lines[len(lines)-1], &event); err != nil {
		t.Fatal(err)
	}
	if event.Summary == nil {
		t.Fatal("heartbeat summary missing")
	}
	if event.Snapshot != nil {
		t.Fatal("heartbeat must drop full snapshot")
	}
	if event.Summary.WrapperInterruptOperations < 1 {
		t.Fatalf("heartbeat summary missing interrupt projection: %+v", event.Summary)
	}
}

func TestAttachWrapperInterruptOmitsUnderBudgetPressure(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 7, 20, 21, 0, 0, 0, time.UTC)
	policy := DefaultPolicy(16 * 1024)
	policy.ResourceBudgets.MaxTelemetryBytesDay = 100
	if err := SavePolicy(PolicyPath(dir), policy); err != nil {
		t.Fatal(err)
	}
	work := NewWorkEventStore(dir)
	work.Now = func() time.Time { return now }
	if err := work.AppendDurable(WorkEvent{
		SchemaVersion: WorkEventSchemaVersion, EventID: hex32(1), OperationID: hex32(2),
		Event: WorkEventCancelled, Class: WorkClassTest, Weight: 1, Outcome: "wrapper_interrupt", Timestamp: now,
	}); err != nil {
		t.Fatal(err)
	}
	tel := NewTelemetryStore(dir)
	tel.Now = func() time.Time { return now }
	// Fill day file so BytesForDay exceeds tiny budget for projection headroom.
	big := make([]byte, 200)
	for i := range big {
		big[i] = 'x'
	}
	if err := os.WriteFile(tel.dayPath(now), append(big, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	summary := compactTelemetrySummary(Snapshot{Timestamp: now, Level: LevelNormal})
	attachWrapperInterruptToSummary(&summary, tel, policy, now)
	if summary.WrapperInterruptOperations != 0 || summary.WrapperInterruptRatePerHour != 0 {
		t.Fatalf("budget pressure must omit interrupt fields: %+v", summary)
	}
}

func TestFitsTelemetryBudgetGatesProjection(t *testing.T) {
	if !FitsTelemetryBudget(0, 256, 1<<20) {
		t.Fatal("empty day must fit")
	}
	if FitsTelemetryBudget(1<<20-10, 256, 1<<20) {
		t.Fatal("near-full day must not fit")
	}
}

func hex32(n int) string {
	return fmt.Sprintf("%032x", n)
}

func splitJSONL(b []byte) [][]byte {
	var lines [][]byte
	start := 0
	for i := 0; i < len(b); i++ {
		if b[i] == '\n' {
			if i > start {
				lines = append(lines, b[start:i])
			}
			start = i + 1
		}
	}
	if start < len(b) {
		lines = append(lines, b[start:])
	}
	return lines
}
