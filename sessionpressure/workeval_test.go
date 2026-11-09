package sessionpressure

import (
	"strings"
	"testing"
)

func TestWorkEvaluationOverrideSafetyAcrossWeights(t *testing.T) {
	for _, weights := range []struct {
		name          string
		browser, test int
	}{
		{"different", 1, 3},
		{"equal", 3, 3},
		{"minimum", 1, 1},
		{"full capacity", 1, 8},
	} {
		t.Run(weights.name, func(t *testing.T) {
			policy := DefaultPolicy(16 * 1024)
			policy.WorkLimits = defaultWorkLimits(10)
			policy.WorkLimits.Capacity = 8
			policy.WorkLimits.BrowserWeight = weights.browser
			policy.WorkLimits.TestWeight = weights.test
			report := EvaluateWorkSystem(policy)
			for _, scenario := range report.Scenarios {
				if scenario.ID == "operator-override-safety" {
					if !scenario.Passed {
						t.Fatal(scenario.Detail)
					}
					return
				}
			}
			t.Fatal("operator override scenario missing")
		})
	}
}

func TestWorkEvaluationFailureIncludesScenarioReason(t *testing.T) {
	policy := DefaultPolicy(16 * 1024)
	policy.WorkLimits = defaultWorkLimits(10)
	policy.WorkLimits.BrowserWeight = policy.WorkLimits.TestWeight + 1
	report := EvaluateWorkSystem(policy)
	if report.OK {
		t.Fatal("invalid calibration passed")
	}
	for _, signal := range report.ReviewSignals {
		if strings.HasPrefix(signal, "calibrated-classes: capacity=") {
			return
		}
	}
	t.Fatalf("failed calibration has no actionable reason: %v", report.ReviewSignals)
}
