package sessionpressure

import "testing"

func TestApplyPolicyProfileObserveKeepsSoftDefaults(t *testing.T) {
	base := DefaultPolicy(16 * 1024)
	got, err := ApplyPolicyProfile(base, PolicyProfileObserve, ApplyProfileOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got.EnforceAdmission || got.AutoShedCritical || !got.Enabled {
		t.Fatalf("observe profile = enforce=%v shed=%v enabled=%v", got.EnforceAdmission, got.AutoShedCritical, got.Enabled)
	}
}

func TestApplyPolicyProfileObserveEconomyCadence(t *testing.T) {
	base := DefaultPolicy(16 * 1024)
	// Defaults are hotter under pressure (15s); observe must calm the harness tax.
	if base.PressureSampleIntervalSeconds != 15 {
		t.Fatalf("default pressure sample = %d, want 15", base.PressureSampleIntervalSeconds)
	}
	got, err := ApplyPolicyProfile(base, PolicyProfileObserve, ApplyProfileOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got.SampleIntervalSeconds != 120 || got.PressureSampleIntervalSeconds != 30 || got.CriticalSampleIntervalSeconds != 10 {
		t.Fatalf("observe cadence = normal=%d pressure=%d critical=%d",
			got.SampleIntervalSeconds, got.PressureSampleIntervalSeconds, got.CriticalSampleIntervalSeconds)
	}
	if got.ProcessInventoryIntervalSeconds < 240 {
		t.Fatalf("process inventory interval = %d, want >= 240", got.ProcessInventoryIntervalSeconds)
	}
	if got.ResourceBudgets.MaxSampleCPUTimeMS < 150 {
		t.Fatalf("sample cpu budget = %v, want >= 150ms", got.ResourceBudgets.MaxSampleCPUTimeMS)
	}
	if got.ResourceBudgets.MaxIdleCPUPercent < 0.5 {
		t.Fatalf("idle duty budget = %v, want >= 0.5", got.ResourceBudgets.MaxIdleCPUPercent)
	}
	// Balanced/enforce must keep hot default cadence.
	bal, err := ApplyPolicyProfile(base, PolicyProfileBalanced, ApplyProfileOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if bal.PressureSampleIntervalSeconds != base.PressureSampleIntervalSeconds {
		t.Fatalf("balanced must not inherit economy cadence: %d vs default %d",
			bal.PressureSampleIntervalSeconds, base.PressureSampleIntervalSeconds)
	}
}

func TestApplyPolicyProfileDailyDriverEnforceAutoShedOptIn(t *testing.T) {
	base := DefaultPolicy(16 * 1024)
	noShed, err := ApplyPolicyProfile(base, PolicyProfileDailyDriverEnforce, ApplyProfileOptions{})
	if err != nil || !noShed.EnforceAdmission || noShed.AutoShedCritical {
		t.Fatalf("enforce without flag: %+v err=%v", noShed, err)
	}
	withShed, err := ApplyPolicyProfile(base, PolicyProfileDailyDriverEnforce, ApplyProfileOptions{WithAutoShed: true})
	if err != nil || !withShed.EnforceAdmission || !withShed.AutoShedCritical {
		t.Fatalf("enforce with flag: %+v err=%v", withShed, err)
	}
}

func TestApplyPolicyProfileUnknown(t *testing.T) {
	if _, err := ApplyPolicyProfile(DefaultPolicy(16*1024), "nope", ApplyProfileOptions{}); err == nil {
		t.Fatal("expected unknown profile error")
	}
}

func TestListPolicyProfilesNames(t *testing.T) {
	list := ListPolicyProfiles()
	if len(list) != 4 {
		t.Fatalf("profiles=%v", list)
	}
	want := map[string]bool{PolicyProfileBalanced: true, PolicyProfileThroughput: true, PolicyProfileInteractive: true, PolicyProfileObserve: true}
	for _, p := range list {
		if !want[p.Name] {
			t.Fatalf("unexpected %q", p.Name)
		}
	}
}

func TestWarningDeratingIsInteractiveOnly(t *testing.T) {
	base := DefaultPolicy(16 * 1024)
	if base.WorkLimits.WarningCapacityEnabled {
		t.Fatal("fresh observe policy must not derate warning capacity")
	}
	for _, name := range []string{PolicyProfileBalanced, PolicyProfileThroughput, PolicyProfileObserve} {
		got, err := ApplyPolicyProfile(base, name, ApplyProfileOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if got.WorkLimits.WarningCapacityEnabled {
			t.Fatalf("profile %s unexpectedly enables warning derating", name)
		}
	}
	interactive, err := ApplyPolicyProfile(base, PolicyProfileInteractive, ApplyProfileOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !interactive.WorkLimits.WarningCapacityEnabled || interactive.Profile != PolicyProfileInteractive {
		t.Fatalf("interactive profile=%+v", interactive)
	}
}

func TestApplyPolicyProfileMultiAgentSoftDiffersFromObserve(t *testing.T) {
	base := DefaultPolicy(16 * 1024)
	// Force capacity 8 so knobs are deterministic.
	base.WorkLimits.Capacity = 8
	observe, err := ApplyPolicyProfile(base, PolicyProfileObserve, ApplyProfileOptions{})
	if err != nil {
		t.Fatal(err)
	}
	multi, err := ApplyPolicyProfile(base, PolicyProfileMultiAgentSoft, ApplyProfileOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if multi.EnforceAdmission || multi.AutoShedCritical {
		t.Fatalf("multi-agent-soft must stay soft: enforce=%v shed=%v", multi.EnforceAdmission, multi.AutoShedCritical)
	}
	if multi.LaunchAdmission.Mode != LaunchAdmissionModeSoft {
		t.Fatalf("mode=%q", multi.LaunchAdmission.Mode)
	}
	if multi.LaunchAdmission.QueueDepthBlock == observe.LaunchAdmission.QueueDepthBlock &&
		multi.LaunchAdmission.OldestWaitBlockSeconds == observe.LaunchAdmission.OldestWaitBlockSeconds {
		t.Fatalf("multi-agent-soft must differ from observe: multi=%+v observe=%+v", multi.LaunchAdmission, observe.LaunchAdmission)
	}
	if multi.LaunchAdmission.QueueDepthBlock != max(1, 8-2) {
		t.Fatalf("queue_depth_block=%d want 6", multi.LaunchAdmission.QueueDepthBlock)
	}
	if multi.LaunchAdmission.OldestWaitBlockSeconds != multiAgentSoftOldestWaitSeconds {
		t.Fatalf("oldest_wait=%d want %d", multi.LaunchAdmission.OldestWaitBlockSeconds, multiAgentSoftOldestWaitSeconds)
	}
	if observe.LaunchAdmission.QueueDepthBlock != 8 || observe.LaunchAdmission.OldestWaitBlockSeconds != defaultOldestLaunchWaitSecond {
		t.Fatalf("observe defaults drifted: %+v", observe.LaunchAdmission)
	}
}
