package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nstranquist/session-pressure/internal/filelock"
)

func TestParseFootprintBytesAndPhysicalFootprintBudget(t *testing.T) {
	sample := "NDevPressure [77625]: 64-bit    Footprint: 118500000 B\n\nAuxiliary data:\n    phys_footprint: 124256256 B\n    phys_footprint_peak: 200000000 B\n"
	mib, ok := parseFootprintBytes(sample)
	if !ok || mib < 118 || mib > 119 {
		t.Fatalf("expected ~118.5 MiB, got %v (ok=%v)", mib, ok)
	}
	now := time.Now().UTC()
	s := observation{ContinuityPassed: true}
	var w appObservationWindow
	// Shared clean RSS is 191.7 MiB, while real phys_footprint is 118.5 MiB (<= 128 MiB).
	s.recordAppSampleWithFootprint(now, 42, 10, 191.7, mib, true, &w)
	s.recordAppSampleWithFootprint(now.Add(30*time.Minute), 42, 11, 192.0, mib, true, &w)
	if !s.AppBudgetPassed || s.AppMemoryBudgetBy != "phys_footprint" {
		t.Fatalf("expected physical footprint budget pass, got %+v", s)
	}
	// Exceeding 128 MiB physical footprint must fail.
	s.recordAppSampleWithFootprint(now.Add(35*time.Minute), 42, 11.2, 210.0, 135.0, true, &w)
	if s.AppBudgetPassed {
		t.Fatalf("physical footprint above 128 MiB must fail budget: %+v", s)
	}
}

func TestObserveSleepAwareContinuityAndBoundedMaxDeadline(t *testing.T) {
	start := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	s := observation{
		MonitoringStarted: start,
		Deadline:          start.Add(72 * time.Hour),
		MaxDeadline:       start.Add(144 * time.Hour),
		ContinuityPassed:  true,
	}
	// Host slept 2 hours: wall jumped 2h1m while monotonic awake gap stayed 1m.
	s.recordSampleGap(2*time.Hour+time.Minute, time.Minute, time.Minute)
	if !s.ContinuityPassed || s.SleepPauses != 1 || s.SleepPauseSeconds != 7200 {
		t.Fatalf("sleep pause should not fail continuity: %+v", s)
	}
	if !s.Deadline.Equal(start.Add(74 * time.Hour)) {
		t.Fatalf("deadline not extended by sleep delta: %v", s.Deadline)
	}
	// Massive sleep cannot push deadline past MaxDeadline (prevents runaway schedule).
	s.recordSampleGap(200*time.Hour, time.Minute, time.Minute)
	if !s.Deadline.Equal(s.MaxDeadline) {
		t.Fatalf("deadline exceeded MaxDeadline: %v > %v", s.Deadline, s.MaxDeadline)
	}
	// Real observer hang (awakeGap > 2*interval + 30s) must fail ContinuityPassed.
	s.recordSampleGap(5*time.Minute, 5*time.Minute, time.Minute)
	if s.ContinuityPassed {
		t.Fatalf("observer hang did not fail continuity: %+v", s)
	}
}

func TestObservePreventsDuplicateRunViaLock(t *testing.T) {
	out := filepath.Join(t.TempDir(), "evidence.json")
	release, err := filelock.AcquireContext(context.Background(), out+".lock", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	err = observe([]string{"--out", out, "--binary", "/bin/echo"})
	if err == nil || !strings.Contains(err.Error(), "observe lock busy") {
		t.Fatalf("duplicate observe was not blocked by lock: %v", err)
	}
}

func TestAcceptanceFactoryWriterCurrentControllerAndBudget(t *testing.T) {
	now := time.Now().UTC()
	s, r := acceptanceFixture(t, now)
	var a map[string]any
	_ = json.Unmarshal(r.Audit, &a)
	metrics := a["audit"].(map[string]any)["categories"].([]any)[0].(map[string]any)["metrics"].(map[string]any)
	// Recent 2h factory writer had a 250ms burst, while current controller is at 100ms histogram bound.
	metrics["cli_writer_recent_2h"] = map[string]any{"known": true, "attempted": 500, "written": 500, "failed": 0, "gaps": 0, "latency_samples": 500, "latency_p95_us": 250000}
	metrics["cli_writer_current_controller"] = map[string]any{"known": true, "attempted": 200, "written": 200, "failed": 0, "gaps": 0, "latency_samples": 200, "latency_p95_us": 100000}
	metrics["cli_writer_current_controller_evidence"] = true
	r.Audit, _ = json.Marshal(a)
	r.evaluate(s, now)
	if !r.WriterPassed || !r.Accepted {
		t.Fatalf("current-controller factory writer at 100ms histogram bound should pass: pending=%v", r.Pending)
	}
	// Exceeding 100ms on current controller must fail.
	metrics["cli_writer_current_controller"].(map[string]any)["latency_p95_us"] = 100001
	r.Audit, _ = json.Marshal(a)
	r.Pending = []string{}
	r.evaluate(s, now)
	if r.WriterPassed || r.Accepted {
		t.Fatalf("factory writer above 100ms must fail acceptance")
	}
}
