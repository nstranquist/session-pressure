package sessionpressure

import "testing"

func TestClassifyControllerDoesNotBlockAWarning(t *testing.T) {
	policy := DefaultPolicy(16 * 1024)
	snapshot := Snapshot{
		FreePercent: 60, HostCPUAvailable: true, HostCPUPercent: policy.Thresholds.HostCPUWarningPercent,
		ThermalState: ThermalStateNominal,
	}
	decision := ClassifyController(snapshot, policy)
	if decision.Mode != ControllerModeBusy || decision.BlockWork || decision.BlockAgentLaunch {
		t.Fatalf("warning controller=%+v", decision)
	}
}

func TestClassifyControllerRequiresCorroboratedCPUStress(t *testing.T) {
	policy := DefaultPolicy(16 * 1024)
	liveOnly := Snapshot{FreePercent: 60, HostCPUAvailable: true, HostCPUPercent: 99, ThermalState: ThermalStateNominal}
	if decision := ClassifyController(liveOnly, policy); decision.CPUStress || decision.ThrottleWork || decision.BlockWork {
		t.Fatalf("uncorroborated CPU spike stressed or blocked work: %+v", decision)
	}
	liveOnly.HostCPURollingAvailable = true
	liveOnly.HostCPURollingPercent = 97
	decision := ClassifyController(liveOnly, policy)
	if !decision.CPUStress || !decision.ThrottleWork || decision.BlockWork || decision.BlockAgentLaunch || decision.Dimension != "cpu" || decision.Mode != ControllerModeStressed {
		t.Fatalf("corroborated CPU stress should soft-throttle without hard BlockWork: %+v", decision)
	}
}

func TestClassifyControllerThermalFloors(t *testing.T) {
	policy := DefaultPolicy(16 * 1024)
	serious := Snapshot{FreePercent: 60, HostCPUAvailable: true, HostCPUPercent: 20, ThermalState: ThermalStateSerious}
	if decision := ClassifyController(serious, policy); !decision.BlockWork || decision.BlockAgentLaunch || decision.Dimension != "thermal" {
		t.Fatalf("serious thermal controller=%+v", decision)
	}
	// Thermal serious must still hard-block with dimension=thermal even when CPU is also saturated.
	seriousHotCPU := Snapshot{
		FreePercent: 60, HostCPUAvailable: true, HostCPUPercent: 100,
		HostCPURollingAvailable: true, HostCPURollingPercent: 99,
		ThermalState: ThermalStateSerious,
	}
	if decision := ClassifyController(seriousHotCPU, policy); !decision.BlockWork || !decision.CPUStress || !decision.ThrottleWork || decision.BlockAgentLaunch || decision.Dimension != "thermal" {
		t.Fatalf("serious thermal + CPU saturation controller=%+v", decision)
	}
	critical := serious
	critical.ThermalState = ThermalStateCritical
	decision := ClassifyController(critical, policy)
	if !decision.BlockWork || !decision.BlockAgentLaunch || decision.Mode != ControllerModeEmergency {
		t.Fatalf("critical thermal controller=%+v", decision)
	}
}

func TestClassifyControllerMemoryFloorIsExplicit(t *testing.T) {
	policy := DefaultPolicy(16 * 1024)
	snapshot := Snapshot{FreePercent: int(policy.Thresholds.FreeRedPercent), HostCPUAvailable: true, HostCPUPercent: 20, ThermalState: ThermalStateNominal}
	decision := ClassifyController(snapshot, policy)
	if decision.Mode != ControllerModeStressed || !decision.BlockWork || !decision.BlockAgentLaunch || decision.Dimension != "memory" {
		t.Fatalf("memory red controller=%+v", decision)
	}
}

func TestClassifyControllerSwapThrashSoftThrottleRespectsCause(t *testing.T) {
	policy := DefaultPolicy(16 * 1024)
	agentThrash := Snapshot{
		FreePercent: 55, HostCPUAvailable: true, HostCPUPercent: 20,
		ThermalState: ThermalStateNominal, SwapIOAvailable: true,
		SwapOutMBPerSecond: 64, SwapThrashLatched: true, SwapThrashCause: "agent",
	}
	if decision := ClassifyController(agentThrash, policy); !decision.SwapThrash || !decision.ThrottleWork || decision.BlockWork || decision.BlockAgentLaunch {
		t.Fatalf("non-external swap thrash controller=%+v", decision)
	}
	externalThrash := agentThrash
	externalThrash.SwapThrashCause = SwapThrashCauseExternal
	if decision := ClassifyController(externalThrash, policy); !decision.SwapThrash || decision.ThrottleWork || decision.BlockWork || decision.BlockAgentLaunch {
		t.Fatalf("external swap thrash controller=%+v", decision)
	}
}

func TestAdmissionForWorkSnapshotSeparatesCPUSaturationFromSurvivalBlocks(t *testing.T) {
	policy := DefaultPolicy(16 * 1024)
	policy.EnforceAdmission = true

	// Pure CPU saturation with healthy RAM and nominal thermal: soft-throttled, not hard-blocked.
	cpuOnly := AdmissionForWorkSnapshot(Snapshot{
		FreePercent: 59, SwapUsedMB: 2385, HostCPUAvailable: true, HostCPUPercent: 100,
		HostCPURollingAvailable: true, HostCPURollingPercent: 98, ConsecutiveSamples: 3,
		ThermalState: ThermalStateNominal, SwapIOAvailable: true, SwapOutMBPerSecond: 0,
	}, policy, "test")
	if !cpuOnly.Allowed || cpuOnly.Level != LevelRed || cpuOnly.Dimension != "cpu" {
		t.Fatalf("CPU-only saturation should remain allowed for work: %+v", cpuOnly)
	}
	if cpuOnly.Controller == nil || !cpuOnly.Controller.CPUStress || !cpuOnly.Controller.ThrottleWork || cpuOnly.Controller.BlockWork {
		t.Fatalf("CPU-only controller=%+v", cpuOnly.Controller)
	}

	// Thermal serious + saturated CPU: hard-blocked with thermal dimension.
	thermalAndCPU := AdmissionForWorkSnapshot(Snapshot{
		FreePercent: 59, HostCPUAvailable: true, HostCPUPercent: 100,
		HostCPURollingAvailable: true, HostCPURollingPercent: 98,
		ThermalState: ThermalStateSerious,
	}, policy, "test")
	if thermalAndCPU.Allowed || thermalAndCPU.Dimension != "thermal" || thermalAndCPU.Controller == nil || !thermalAndCPU.Controller.BlockWork {
		t.Fatalf("thermal serious + CPU saturation must hard-block on thermal: %+v", thermalAndCPU)
	}

	// Memory red + saturated CPU: hard-blocked with memory dimension.
	memoryAndCPU := AdmissionForWorkSnapshot(Snapshot{
		FreePercent: int(policy.Thresholds.FreeRedPercent), HostCPUAvailable: true, HostCPUPercent: 100,
		HostCPURollingAvailable: true, HostCPURollingPercent: 98,
		ThermalState: ThermalStateNominal,
	}, policy, "test")
	if memoryAndCPU.Allowed || memoryAndCPU.Dimension != "memory" || memoryAndCPU.Controller == nil || !memoryAndCPU.Controller.BlockWork {
		t.Fatalf("memory red + CPU saturation must hard-block on memory: %+v", memoryAndCPU)
	}
}

func TestClassifyControllerLowPowerModeIsAdvisory(t *testing.T) {
	policy := DefaultPolicy(16 * 1024)
	decision := ClassifyController(Snapshot{FreePercent: 60, HostCPUAvailable: true, HostCPUPercent: 20, LowPowerMode: true, ThermalState: ThermalStateNominal}, policy)
	if decision.Mode != ControllerModeBusy || decision.BlockWork || decision.BlockAgentLaunch {
		t.Fatalf("low-power controller=%+v", decision)
	}
}
