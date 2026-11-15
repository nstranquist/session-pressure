package sessionpressure

import (
	"strings"
	"testing"
	"time"
)

// lagSnapshot is the 2026-11-15 15:16 UTC shape: swap above the red line,
// swap-out calm, free memory healthy, and only the Claude desktop tree at
// warning to corroborate.
func lagSnapshot(policy Policy) Snapshot {
	return Snapshot{
		FreePercent: 37, SwapUsedMB: 8916, SwapIOAvailable: true, SwapOutMBPerSecond: 0,
		SwapCalmSeconds: 16 * 60, SwapCalmStartUsedMB: 9100,
		TopAgentTrees: []AgentTree{{Agent: "claude", RootPID: 92881, RSSSumMB: policy.Thresholds.TreeWarningMB + 1475}},
	}
}

func lagPolicy() Policy {
	policy := DefaultPolicy(16 * 1024)
	policy.Thresholds.SwapLagReleaseMinutes = 15
	return policy
}

func TestSwapLagReleaseIsOffByDefault(t *testing.T) {
	policy := DefaultPolicy(16 * 1024)
	got := EvaluateMemoryPressure(lagSnapshot(policy), policy)
	if got.Level != LevelRed || got.SwapLagReleased {
		t.Fatalf("default policy must keep the historical red: level=%s released=%v", got.Level, got.SwapLagReleased)
	}
}

func TestSwapLagReleaseHoldsWarningWhenSwapIsOnlyDraining(t *testing.T) {
	policy := lagPolicy()
	got := EvaluateMemoryPressure(lagSnapshot(policy), policy)
	joined := strings.Join(got.Reasons, "; ")
	if got.Level != LevelWarning || !got.SwapLagReleased || !strings.Contains(joined, "lag release") {
		t.Fatalf("level=%s released=%v reasons=%s", got.Level, got.SwapLagReleased, joined)
	}
	// Re-evaluating a released snapshot without the window must not keep the flag.
	again := lagSnapshot(policy)
	again.SwapLagReleased = true
	again.SwapCalmSeconds = 60
	if got := EvaluateMemoryPressure(again, policy); got.Level != LevelRed || got.SwapLagReleased {
		t.Fatalf("stale flag survived: level=%s released=%v", got.Level, got.SwapLagReleased)
	}
}

func TestSwapLagReleaseKeepsEveryThrashProtection(t *testing.T) {
	policy := lagPolicy()
	for name, mutate := range map[string]func(*Snapshot){
		"thrash latched":         func(s *Snapshot) { s.SwapThrashLatched = true },
		"swap-out above release": func(s *Snapshot) { s.SwapOutMBPerSecond = policy.Thresholds.SwapOutReleaseMBPerSecond + 0.1 },
		"swap io unavailable":    func(s *Snapshot) { s.SwapIOAvailable = false },
		"window too short":       func(s *Snapshot) { s.SwapCalmSeconds = 14*60 + 59 },
		"no calm evidence":       func(s *Snapshot) { s.SwapCalmSeconds, s.SwapCalmStartUsedMB = 0, 0 },
		"swap growing":           func(s *Snapshot) { s.SwapUsedMB = s.SwapCalmStartUsedMB + defaultSwapLagMaxGrowthMB + 1 },
		"free memory at warning": func(s *Snapshot) { s.FreePercent = int(policy.Thresholds.FreeWarningPercent) },
		"live red tree":          func(s *Snapshot) { s.TopAgentTrees[0].RSSSumMB = policy.Thresholds.TreeRedMB },
		"live red free memory":   func(s *Snapshot) { s.FreePercent = int(policy.Thresholds.FreeRedPercent) },
		"agent total live red":   func(s *Snapshot) { s.AgentRSSSumMB = policy.Thresholds.AgentTotalRedMB },
		"critical swap, live red": func(s *Snapshot) {
			s.SwapUsedMB = policy.Thresholds.SwapCriticalMB
			s.SwapCalmStartUsedMB = s.SwapUsedMB
			s.TopAgentTrees[0].RSSSumMB = policy.Thresholds.TreeRedMB
		},
	} {
		snapshot := lagSnapshot(policy)
		mutate(&snapshot)
		got := EvaluateMemoryPressure(snapshot, policy)
		if got.SwapLagReleased || !got.Level.AtLeast(LevelRed) {
			t.Fatalf("%s: level=%s released=%v reasons=%v", name, got.Level, got.SwapLagReleased, got.Reasons)
		}
	}
	// Swap-out thrash still warns on the full ladder even when released.
	snapshot := lagSnapshot(policy)
	snapshot.SwapOutMBPerSecond = policy.Thresholds.SwapOutThrashMBPerSecond
	if got := Evaluate(snapshot, policy); got.SwapLagReleased || got.Level != LevelRed {
		t.Fatalf("thrash rate: level=%s released=%v", got.Level, got.SwapLagReleased)
	}
}

func TestSwapLagReleasePolicyValidation(t *testing.T) {
	for minutes, wantErr := range map[float64]bool{0: false, 4: true, -1: true, 5: false, 15: false} {
		policy := DefaultPolicy(16 * 1024)
		policy.Thresholds.SwapLagReleaseMinutes = minutes
		err := policy.Validate()
		if (err != nil) != wantErr {
			t.Fatalf("minutes=%v err=%v wantErr=%v", minutes, err, wantErr)
		}
	}
	policy := DefaultPolicy(16 * 1024)
	policy.Thresholds.SwapLagMaxGrowthMB = -1
	if policy.Validate() == nil {
		t.Fatal("negative growth must be rejected")
	}
}

func TestSwapThrashLatchTracksCalmRun(t *testing.T) {
	thresholds := DefaultPolicy(16 * 1024).Thresholds
	limits := DefaultPolicy(16 * 1024).WorkLimits
	var latch swapThrashLatch
	base := time.Date(2026, 11, 15, 15, 0, 0, 0, time.UTC)
	step := func(offset time.Duration, rate, swapMB float64) Snapshot {
		snapshot := Snapshot{Timestamp: base.Add(offset), SwapIOAvailable: true, SwapOutMBPerSecond: rate, SwapUsedMB: swapMB}
		latch.advance(&snapshot, thresholds, limits)
		return snapshot
	}
	if got := step(0, 0, 9000); got.SwapCalmSeconds != 0 || got.SwapCalmStartUsedMB != 9000 {
		t.Fatalf("first calm sample: %+v", got)
	}
	if got := step(30*time.Second, 10, 8950); got.SwapCalmSeconds != 30 || got.SwapCalmStartUsedMB != 9000 {
		t.Fatalf("second calm sample: secs=%v start=%v", got.SwapCalmSeconds, got.SwapCalmStartUsedMB)
	}
	// The band between release and thrash ends the calm run.
	if got := step(60*time.Second, 20, 8950); got.SwapCalmSeconds != 0 || got.SwapCalmStartUsedMB != 0 {
		t.Fatalf("band sample: %+v", got)
	}
	// A thrash episode, then its release: calm carries across the release.
	step(90*time.Second, 200, 9100)
	if got := step(120*time.Second, 200, 9200); !got.SwapThrashLatched {
		t.Fatal("two hot samples must latch")
	}
	step(150*time.Second, 0, 9200)
	released := step(180*time.Second, 0, 9150)
	if released.SwapThrashLatched || released.SwapCalmSeconds != 30 || released.SwapCalmStartUsedMB != 9200 {
		t.Fatalf("after release: %+v", released)
	}
	if got := step(210*time.Second, 0, 9100); got.SwapCalmSeconds != 60 {
		t.Fatalf("calm must keep counting after release: %v", got.SwapCalmSeconds)
	}
	// Unavailable counters clear the evidence.
	snapshot := Snapshot{Timestamp: base.Add(240 * time.Second), SwapIOAvailable: false, SwapUsedMB: 9100}
	latch.advance(&snapshot, thresholds, limits)
	if snapshot.SwapCalmSeconds != 0 || !latch.calmSince.IsZero() {
		t.Fatalf("unavailable: %+v", snapshot)
	}
}
