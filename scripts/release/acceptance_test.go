package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestAcceptanceCollectionFailureClearsPreviousPass(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "acceptance.json")
	if err := writeJSON(out, acceptanceReport{Accepted: true}); err != nil {
		t.Fatal(err)
	}
	if err := acceptance([]string{"--observation", filepath.Join(dir, "missing.json"), "--out", out}); err == nil {
		t.Fatal("missing observer accepted")
	}
	raw, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	var r acceptanceReport
	if err = json.Unmarshal(raw, &r); err != nil || r.Accepted || len(r.Pending) == 0 {
		t.Fatalf("stale pass retained: %s %v", raw, err)
	}
}

func acceptanceFixture(t *testing.T, now time.Time) (observation, acceptanceReport) {
	t.Helper()
	writer := map[string]any{"known": true, "attempted": 100, "written": 100, "failed": 0, "gaps": 0, "latency_samples": 100, "latency_p95_us": 25000}
	audit, _ := json.Marshal(map[string]any{"audit": map[string]any{"generated_at": now, "categories": []any{map[string]any{"id": "telemetry", "metrics": map[string]any{"cli_writer_recent_2h": writer, "product_writer": writer}}}}})
	evaluation, _ := json.Marshal(map[string]any{"work_evaluation": map[string]any{"ok": true, "scenario_count": 22, "passed": 22, "failed": 0, "replay": map[string]any{"trace_end": now.Add(-time.Hour), "promotion_ready": true, "safety_gates_pass": true, "performance_gates_pass": true, "calibration_gates_pass": true, "selector_gate_pass": true}}})
	s := observation{Accepted72H: true, Started: now.Add(-73 * time.Hour), Deadline: now.Add(-time.Hour), Updated: now, HealthPassed: true, ContinuityPassed: true, AppBudgetPassed: true}
	return s, acceptanceReport{ArtifactIdentityPassed: true, Audit: audit, Evaluation: evaluation, Pending: []string{}}
}

func TestCombinedAcceptanceRequiresIndependentGates(t *testing.T) {
	now := time.Now().UTC()
	for _, name := range []string{"pass", "short_run", "restart", "failed_health", "artifact", "missing_audit", "missing_replay", "slow_writer", "lost_writer", "stale_audit", "failed_replay"} {
		t.Run(name, func(t *testing.T) {
			s, r := acceptanceFixture(t, now)
			switch name {
			case "short_run":
				s.Started = now.Add(-time.Hour)
			case "restart":
				s.ResidentRestarts = 1
			case "failed_health":
				s.Failures = 1
			case "artifact":
				r.ArtifactIdentityPassed = false
			case "missing_audit":
				r.Audit = nil
			case "missing_replay":
				r.Evaluation = nil
			case "slow_writer", "lost_writer", "stale_audit":
				var a map[string]any
				_ = json.Unmarshal(r.Audit, &a)
				audit := a["audit"].(map[string]any)
				if name == "stale_audit" {
					audit["generated_at"] = now.Add(-6 * time.Minute)
				} else {
					metrics := audit["categories"].([]any)[0].(map[string]any)["metrics"].(map[string]any)
					w := metrics["product_writer"].(map[string]any)
					if name == "slow_writer" {
						w["latency_p95_us"] = 25001
					} else {
						w["written"] = 99
					}
				}
				r.Audit, _ = json.Marshal(a)
			case "failed_replay":
				var e map[string]any
				_ = json.Unmarshal(r.Evaluation, &e)
				e["work_evaluation"].(map[string]any)["replay"].(map[string]any)["performance_gates_pass"] = false
				r.Evaluation, _ = json.Marshal(e)
			}
			r.evaluate(s, now)
			if r.Accepted != (name == "pass") {
				t.Fatalf("accepted=%t pending=%v", r.Accepted, r.Pending)
			}
		})
	}
}
