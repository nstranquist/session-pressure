package sessionpressure

import "testing"

func TestSuggestPolicyProfileInsufficientVolume(t *testing.T) {
	got := SuggestPolicyProfile(SuggestPolicyProfileInput{
		OperationCount: 19, CancelledOperations: 10, QueuePressureSignal: true,
	})
	if got.Profile != "" || got.Reason != "" {
		t.Fatalf("below volume must not suggest: %+v", got)
	}
}

func TestSuggestPolicyProfileHighCancelRate(t *testing.T) {
	got := SuggestPolicyProfile(SuggestPolicyProfileInput{
		OperationCount: 20, CancelledOperations: 3, // 0.15
	})
	if got.Profile != PolicyProfileMultiAgentSoft || got.Reason != SuggestReasonHighCancelRate {
		t.Fatalf("high cancel: %+v", got)
	}
}

func TestSuggestPolicyProfileQueuePressure(t *testing.T) {
	got := SuggestPolicyProfile(SuggestPolicyProfileInput{
		OperationCount: 40, CancelledOperations: 0, QueuePressureSignal: true,
	})
	if got.Profile != PolicyProfileMultiAgentSoft || got.Reason != SuggestReasonMultiAgentQueuePressure {
		t.Fatalf("queue pressure: %+v", got)
	}
}

func TestSuggestPolicyProfileNoSignal(t *testing.T) {
	got := SuggestPolicyProfile(SuggestPolicyProfileInput{
		OperationCount: 50, CancelledOperations: 1, QueuePressureSignal: false,
	})
	if got.Profile != "" {
		t.Fatalf("no multi-agent signal: %+v", got)
	}
}

func TestSuggestPolicyProfileNeverMutatesPolicyNamesOutsideAllowlist(t *testing.T) {
	got := SuggestPolicyProfile(SuggestPolicyProfileInput{
		OperationCount: 100, CancelledOperations: 50,
	})
	if got.Profile != PolicyProfileMultiAgentSoft {
		t.Fatalf("only multi-agent-soft is allowed: %q", got.Profile)
	}
	// Explicit: never daily-driver-enforce from this helper.
	if got.Profile == PolicyProfileDailyDriverEnforce {
		t.Fatal("must not suggest enforce profile")
	}
}
