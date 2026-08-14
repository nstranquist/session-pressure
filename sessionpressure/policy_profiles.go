package sessionpressure

import (
	"fmt"
	"sort"
	"strings"
)

// Policy profile names are stable operator vocabulary under
// `ndev session pressure policy profile`.
const (
	PolicyProfileBalanced    = "balanced"
	PolicyProfileThroughput  = "throughput"
	PolicyProfileInteractive = "interactive"
	PolicyProfileObserve     = "observe"

	// Legacy names remain accepted as aliases so existing launch agents and
	// operator scripts do not fail during the profile vocabulary migration.
	PolicyProfileMultiAgentSoft     = "multi-agent-soft"
	PolicyProfileDailyDriverEnforce = "daily-driver-enforce"
)

// PolicyProfile describes a named fill helper. Profiles never invent a new L1
// verb and never enable auto-shed unless ApplyProfileOptions.WithAutoShed is
// explicitly set on an enforcing profile.
type PolicyProfile struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// ApplyProfileOptions controls optional destructive flags on apply.
type ApplyProfileOptions struct {
	// WithAutoShed is honored only for enforcing profiles. Observe and legacy
	// advisory aliases always force AutoShedCritical=false.
	WithAutoShed bool
}

// multi-agent soft launch knobs (observe-only; never hard enforce by default).
// Earlier soft-warn under parallel agent load than capacity/120 observe defaults.
const (
	multiAgentSoftOldestWaitSeconds = 60
)

// multiAgentSoftLaunchAdmission returns soft launch knobs for multi-agent waves.
// Differs from defaultLaunchAdmissionPolicy by lower queue-depth and oldest-wait
// soft thresholds while remaining Mode=soft.
func multiAgentSoftLaunchAdmission(capacity int) LaunchAdmissionPolicy {
	capacity = max(1, capacity)
	return LaunchAdmissionPolicy{
		Mode:                   LaunchAdmissionModeSoft,
		QueueDepthBlock:        max(1, capacity-2),
		OldestWaitBlockSeconds: multiAgentSoftOldestWaitSeconds,
		ResumeBehavior:         LaunchResumeBehaviorWarn,
	}
}

// applyObserveEconomyCadence reduces resident sampling frequency and slightly
// relaxes sample CPU budgets for observe/multi-agent soft profiles.
// Rationale (finish-loop self-improve KEP, 2026-08-04): under parallel agents
// the monitor itself was failing its own 50ms sample CPU / 0.25% idle-duty
// budgets while contributing a large share of ndev CLI volume. Calmer cadence
// cuts self-tax without enabling enforce/shed.
func applyObserveEconomyCadence(policy *Policy) {
	if policy == nil {
		return
	}
	policy.SampleIntervalSeconds = 120
	policy.PressureSampleIntervalSeconds = 30
	policy.CriticalSampleIntervalSeconds = 10
	if policy.ProcessInventoryIntervalSeconds < 240 {
		policy.ProcessInventoryIntervalSeconds = 240
	}
	if policy.HeartbeatSeconds < policy.SampleIntervalSeconds {
		policy.HeartbeatSeconds = policy.SampleIntervalSeconds * 2
	}
	// Sample CPU budget: a fresh inventory plus identity lsof on a
	// multi-agent host measures ~115ms process CPU. 100ms fails the
	// required identity path; 150ms still fails closed on true thrash.
	if policy.ResourceBudgets.MaxSampleCPUTimeMS < 150 {
		policy.ResourceBudgets.MaxSampleCPUTimeMS = 150
	}
	if policy.ResourceBudgets.MaxIdleCPUPercent < 0.5 {
		policy.ResourceBudgets.MaxIdleCPUPercent = 0.5
	}
}

// ListPolicyProfiles returns the stable profile catalog in operator order.
func ListPolicyProfiles() []PolicyProfile {
	return []PolicyProfile{
		{Name: PolicyProfileBalanced, Description: "Protected daily-driver mode; full weighted capacity; CPU warning never blocks new work"},
		{Name: PolicyProfileThroughput, Description: "Protected throughput mode; full weighted capacity and green express work"},
		{Name: PolicyProfileInteractive, Description: "Protected interactive mode; warning pressure derates only new work to preserve responsiveness"},
		{Name: PolicyProfileObserve, Description: "Monitor and record telemetry; no admission blocks or automatic shedding"},
	}
}

func validPolicyProfileName(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case PolicyProfileBalanced, PolicyProfileThroughput, PolicyProfileInteractive, PolicyProfileObserve,
		PolicyProfileMultiAgentSoft, PolicyProfileDailyDriverEnforce:
		return true
	default:
		return false
	}
}

// ApplyPolicyProfile fills policy fields for a named profile onto base.
// Base physical-memory-derived thresholds and work weights are preserved where
// the profile does not need to touch them.
func ApplyPolicyProfile(base Policy, name string, opts ApplyProfileOptions) (Policy, error) {
	name = strings.ToLower(strings.TrimSpace(name))
	policy := base
	if policy.SchemaVersion == 0 {
		policy = DefaultPolicy(16 * 1024)
	}
	policy.Enabled = true
	switch name {
	case PolicyProfileBalanced, PolicyProfileDailyDriverEnforce:
		policy.Profile = PolicyProfileBalanced
		policy.EnforceAdmission = true
		policy.AutoShedCritical = opts.WithAutoShed
		policy.WorkLimits.WarningCapacityEnabled = false
		policy.LaunchAdmission = defaultLaunchAdmissionPolicy(policy.WorkLimits.Capacity)
		policy.LaunchAdmission.Mode = LaunchAdmissionModeSoft
		// Storage remains independently operator-controlled. A work-style
		// profile must never silently grant reclaim authority.
	case PolicyProfileThroughput:
		policy.Profile = PolicyProfileThroughput
		policy.EnforceAdmission = true
		policy.AutoShedCritical = opts.WithAutoShed
		policy.WorkLimits.WarningCapacityEnabled = false
		policy.LaunchAdmission = defaultLaunchAdmissionPolicy(policy.WorkLimits.Capacity)
		policy.LaunchAdmission.Mode = LaunchAdmissionModeSoft
		// Keep the shipped fast lane, but do not widen capacity or bypass red
		// memory/storage gates.
	case PolicyProfileInteractive:
		policy.Profile = PolicyProfileInteractive
		policy.EnforceAdmission = true
		policy.AutoShedCritical = opts.WithAutoShed
		policy.WorkLimits.WarningCapacityEnabled = true
		policy.LaunchAdmission = defaultLaunchAdmissionPolicy(policy.WorkLimits.Capacity)
		policy.LaunchAdmission.Mode = LaunchAdmissionModeSoft
	case PolicyProfileObserve:
		policy.Profile = PolicyProfileObserve
		policy.EnforceAdmission = false
		policy.AutoShedCritical = false
		policy.WorkLimits.WarningCapacityEnabled = false
		policy.LaunchAdmission = defaultLaunchAdmissionPolicy(policy.WorkLimits.Capacity)
		policy.LaunchAdmission.Mode = LaunchAdmissionModeSoft
		// Storage remains observe-only by default.
		policy.Storage.EnforceAdmission = false
		// Calmer cadence under multi-agent observe load (reduce harness self-tax).
		applyObserveEconomyCadence(&policy)
	case PolicyProfileMultiAgentSoft:
		// Compatibility alias for the former advisory multi-agent profile. It
		// remains observe-only and does not enable warning derating.
		policy.Profile = PolicyProfileObserve
		policy.EnforceAdmission = false
		policy.AutoShedCritical = false
		policy.WorkLimits.WarningCapacityEnabled = false
		// Soft-only earlier backpressure for multi-agent waves (KEP follow-ups).
		policy.LaunchAdmission = multiAgentSoftLaunchAdmission(policy.WorkLimits.Capacity)
		policy.Storage.EnforceAdmission = false
		applyObserveEconomyCadence(&policy)
	default:
		names := make([]string, 0, 3)
		for _, p := range ListPolicyProfiles() {
			names = append(names, p.Name)
		}
		sort.Strings(names)
		return Policy{}, fmt.Errorf("unknown policy profile %q; want one of %s", name, strings.Join(names, ", "))
	}
	if opts.WithAutoShed && !policy.EnforceAdmission {
		return Policy{}, fmt.Errorf("auto-shed requires enforce admission")
	}
	if err := policy.Validate(); err != nil {
		return Policy{}, err
	}
	return policy, nil
}
