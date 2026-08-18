package sessionpressure

// Closed suggestion reason codes (privacy-safe; no free text from hosts).
const (
	SuggestReasonMultiAgentQueuePressure = "multi_agent_queue_pressure"
	SuggestReasonHighCancelRate          = "high_cancel_rate"
	SuggestReasonInsufficientVolume      = "insufficient_volume"
	SuggestReasonNone                    = ""
)

// SuggestPolicyProfileMinimumOps is the calibration volume gate (M2 spec).
const SuggestPolicyProfileMinimumOps = 20

// SuggestPolicyProfileCancelRate is the cancel/ops ratio for high_cancel_rate.
const SuggestPolicyProfileCancelRate = 0.15

// SuggestPolicyProfileInput is the pure input for advisory profile suggestion.
// Never contains argv or paths.
type SuggestPolicyProfileInput struct {
	OperationCount      int
	CancelledOperations int
	// QueuePressureSignal is true when soft launch would-block or queue depth
	// evidence indicates multi-agent soft pressure (caller-derived).
	QueuePressureSignal bool
	// AlreadyApplied suppresses the hint when the live policy already matches
	// multi-agent-soft (including the legacy observe + soft-knob persist).
	AlreadyApplied bool
}

// SuggestPolicyProfileResult is advisory only — never mutates policy.
type SuggestPolicyProfileResult struct {
	// Profile is empty when no suggestion; otherwise a known profile name.
	Profile string `json:"suggested_policy_profile,omitempty"`
	// Reason is a closed code; empty when Profile is empty.
	Reason string `json:"suggested_policy_profile_reason,omitempty"`
}

// SuggestPolicyProfile returns an advisory multi-agent-soft suggestion when
// calibration volume and multi-agent signals meet frozen thresholds.
// MUST NOT apply policy; MUST NOT invent suggestions below volume.
func SuggestPolicyProfile(in SuggestPolicyProfileInput) SuggestPolicyProfileResult {
	if in.AlreadyApplied {
		return SuggestPolicyProfileResult{}
	}
	if in.OperationCount < SuggestPolicyProfileMinimumOps {
		return SuggestPolicyProfileResult{}
	}
	cancelRate := 0.0
	if in.OperationCount > 0 {
		cancelRate = float64(in.CancelledOperations) / float64(in.OperationCount)
	}
	if cancelRate >= SuggestPolicyProfileCancelRate {
		return SuggestPolicyProfileResult{
			Profile: PolicyProfileMultiAgentSoft,
			Reason:  SuggestReasonHighCancelRate,
		}
	}
	if in.QueuePressureSignal {
		return SuggestPolicyProfileResult{
			Profile: PolicyProfileMultiAgentSoft,
			Reason:  SuggestReasonMultiAgentQueuePressure,
		}
	}
	return SuggestPolicyProfileResult{}
}
