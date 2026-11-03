package sessionpressure

import (
	"slices"
	"testing"
)

func TestMultiAgentOptionLayersSoftLaunchKnobsWithoutDroppingEnforcement(t *testing.T) {
	base := DefaultPolicy(16 * 1024)
	balanced, err := ApplyPolicyProfile(base, PolicyProfileBalanced, ApplyProfileOptions{MultiAgent: true})
	if err != nil {
		t.Fatal(err)
	}
	if !balanced.EnforceAdmission || balanced.Profile != PolicyProfileBalanced || !HasMultiAgentLaunchAdmission(balanced) {
		t.Fatalf("balanced --multi-agent policy=%+v launch=%+v", balanced, balanced.LaunchAdmission)
	}
	if MatchesMultiAgentSoft(balanced) {
		t.Fatal("an enforcing policy must not read as the observe-only multi-agent-soft profile")
	}
	plain, err := ApplyPolicyProfile(base, PolicyProfileBalanced, ApplyProfileOptions{})
	if err != nil || HasMultiAgentLaunchAdmission(plain) {
		t.Fatalf("plain balanced gained multi-agent knobs: %+v err=%v", plain.LaunchAdmission, err)
	}
}

func TestCalibrationNeverAdvisesAnEnforcingHostToDropProtection(t *testing.T) {
	queuePressure := func() *WorkCalibrationReport {
		return &WorkCalibrationReport{
			SuggestedPolicyProfile:       PolicyProfileMultiAgentSoft,
			SuggestedPolicyProfileReason: SuggestReasonMultiAgentQueuePressure,
		}
	}
	base := DefaultPolicy(16 * 1024)
	balanced, err := ApplyPolicyProfile(base, PolicyProfileBalanced, ApplyProfileOptions{})
	if err != nil {
		t.Fatal(err)
	}
	report := queuePressure()
	report.SuppressIfAlreadyApplied(balanced)
	if report.SuggestedPolicyProfile != PolicyProfileBalanced || !slices.Equal(report.SuggestedPolicyProfileFlags, []string{"--multi-agent"}) ||
		report.SuggestedPolicyProfileReason != SuggestReasonMultiAgentQueuePressure {
		t.Fatalf("enforcing host suggestion=%+v", report)
	}
	withKnobs, err := ApplyPolicyProfile(base, PolicyProfileBalanced, ApplyProfileOptions{MultiAgent: true})
	if err != nil {
		t.Fatal(err)
	}
	report = queuePressure()
	report.SuppressIfAlreadyApplied(withKnobs)
	if report.SuggestedPolicyProfile != "" || report.SuggestedPolicyProfileFlags != nil {
		t.Fatalf("applied multi-agent knobs still suggested: %+v", report)
	}
	observe, err := ApplyPolicyProfile(base, PolicyProfileObserve, ApplyProfileOptions{})
	if err != nil {
		t.Fatal(err)
	}
	report = queuePressure()
	report.SuppressIfAlreadyApplied(observe)
	if report.SuggestedPolicyProfile != PolicyProfileMultiAgentSoft || report.SuggestedPolicyProfileFlags != nil {
		t.Fatalf("observe host lost the multi-agent-soft suggestion: %+v", report)
	}
	report = &WorkCalibrationReport{}
	report.SuppressIfAlreadyApplied(balanced)
	if report.SuggestedPolicyProfile != "" {
		t.Fatalf("no evidence produced a suggestion: %+v", report)
	}
}
