package sessionpressure

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"testing"
	"time"
)

func TestProjectWrapperInterruptTelemetryCountsAndRate(t *testing.T) {
	now := time.Date(2026, 7, 20, 20, 0, 0, 0, time.UTC)
	proj, err := ProjectWrapperInterruptTelemetry(24, 24*time.Hour, now)
	if err != nil {
		t.Fatal(err)
	}
	if proj.SchemaVersion != 1 || proj.WrapperInterruptOperations != 24 {
		t.Fatalf("proj=%+v", proj)
	}
	if proj.WrapperInterruptRatePerHour < 0.99 || proj.WrapperInterruptRatePerHour > 1.01 {
		t.Fatalf("rate=%v want ~1.0", proj.WrapperInterruptRatePerHour)
	}
	if proj.ProjectedBytes <= 0 {
		t.Fatal("projected bytes required for budget accounting")
	}
}

func TestProjectWrapperInterruptRejectsBadWindow(t *testing.T) {
	if _, err := ProjectWrapperInterruptTelemetry(1, 0, time.Time{}); err == nil {
		t.Fatal("expected window error")
	}
	if _, err := ProjectWrapperInterruptTelemetry(-1, time.Hour, time.Time{}); err == nil {
		t.Fatal("expected negative ops error")
	}
}

func TestProjectWrapperInterruptFromSignals(t *testing.T) {
	signals := WorkReviewSignals{WrapperInterruptOperations: 3}
	proj, err := ProjectWrapperInterruptFromSignals(signals, 3*time.Hour, time.Date(2026, 7, 20, 0, 0, 0, 0, time.UTC))
	if err != nil || proj.WrapperInterruptOperations != 3 {
		t.Fatalf("proj=%+v err=%v", proj, err)
	}
	if proj.WrapperInterruptRatePerHour < 0.99 || proj.WrapperInterruptRatePerHour > 1.01 {
		t.Fatalf("rate=%v", proj.WrapperInterruptRatePerHour)
	}
}

func TestFitsTelemetryBudget(t *testing.T) {
	if !FitsTelemetryBudget(100, 50, 200) {
		t.Fatal("should fit")
	}
	if FitsTelemetryBudget(180, 50, 200) {
		t.Fatal("should not fit")
	}
	if !FitsTelemetryBudget(0, 10, 0) {
		t.Fatal("max 0 means unlimited")
	}
}

func TestContainsPathyRejectsFreeTextOutcomes(t *testing.T) {
	// Privacy invariant used by calibration; telemetry must not project pathy labels.
	if !containsPathy("/tmp/secret") || !containsPathy(`C:\evil`) {
		t.Fatal("pathy detection")
	}
	if containsPathy("wrapper_interrupt") {
		t.Fatal("closed enum must not look pathy")
	}
}

func TestWrapperInterruptHeartbeatStreamsBoundedWorkHistory(t *testing.T) {
	const eventCount = 5000
	now := time.Date(2026, 7, 20, 20, 0, 0, 0, time.Local)
	dir := t.TempDir()
	workStore := NewWorkEventStore(dir)
	writeWrapperInterruptFixture(t, workStore, now, eventCount)
	telemetryStore := NewTelemetryStore(dir)

	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	summary := TelemetrySnapshotSummary{}
	attachWrapperInterruptToSummary(&summary, telemetryStore, DefaultPolicy(16*1024), now)
	runtime.ReadMemStats(&after)

	want := eventCount / 100
	if summary.WrapperInterruptOperations != want {
		t.Fatalf("wrapper interrupts = %d, want %d", summary.WrapperInterruptOperations, want)
	}
	if summary.WrapperInterruptRatePerHour != float64(want)/24 {
		t.Fatalf("wrapper interrupt rate = %v, want %v", summary.WrapperInterruptRatePerHour, float64(want)/24)
	}
	if allocated := after.TotalAlloc - before.TotalAlloc; allocated > 16<<20 {
		t.Fatalf("streaming %d work rows allocated %d bytes", eventCount, allocated)
	}
}

func TestCountWrapperInterruptOperationsFailsClosedOnInvalidRelevantRow(t *testing.T) {
	now := time.Date(2026, 7, 20, 20, 0, 0, 0, time.Local)
	store := NewWorkEventStore(t.TempDir())
	if err := os.WriteFile(store.dayPath(now), []byte("{\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CountWrapperInterruptOperations(now.Add(-time.Hour), now); err == nil {
		t.Fatal("invalid work row unexpectedly produced an interrupt count")
	}
}

func writeWrapperInterruptFixture(t *testing.T, store *WorkEventStore, at time.Time, count int) {
	t.Helper()
	var body bytes.Buffer
	encoder := json.NewEncoder(&body)
	for index := 0; index < count; index++ {
		outcome := "user_abort"
		if index%100 == 0 {
			outcome = "wrapper_interrupt"
		}
		event := WorkEvent{
			SchemaVersion: WorkEventSchemaVersion,
			EventID:       fmt.Sprintf("%032x", index+1),
			Timestamp:     at.Add(time.Duration(index) * time.Millisecond),
			Event:         WorkEventCancelled,
			OperationID:   fmt.Sprintf("%032x", index+count+1),
			Class:         WorkClassTest,
			Weight:        3,
			Outcome:       outcome,
		}
		if err := encoder.Encode(event); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(store.dayPath(at), body.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
}
