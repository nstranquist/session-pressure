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
	if decision := ClassifyController(liveOnly, policy); decision.CPUStress || decision.BlockWork {
		t.Fatalf("uncorroborated CPU spike blocked work: %+v", decision)
	}
	liveOnly.HostCPURollingAvailable = true
	liveOnly.HostCPURollingPercent = 97
	decision := ClassifyController(liveOnly, policy)
	if !decision.CPUStress || !decision.BlockWork || decision.Dimension != "cpu" || decision.Mode != ControllerModeStressed {
		t.Fatalf("corroborated CPU stress=%+v", decision)
	}
}

func TestClassifyControllerThermalFloors(t *testing.T) {
	policy := DefaultPolicy(16 * 1024)
	serious := Snapshot{FreePercent: 60, HostCPUAvailable: true, HostCPUPercent: 20, ThermalState: ThermalStateSerious}
	if decision := ClassifyController(serious, policy); !decision.BlockWork || decision.BlockAgentLaunch || decision.Dimension != "thermal" {
		t.Fatalf("serious thermal controller=%+v", decision)
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

func TestClassifyControllerLowPowerModeIsAdvisory(t *testing.T) {
	policy := DefaultPolicy(16 * 1024)
	decision := ClassifyController(Snapshot{FreePercent: 60, HostCPUAvailable: true, HostCPUPercent: 20, LowPowerMode: true, ThermalState: ThermalStateNominal}, policy)
	if decision.Mode != ControllerModeBusy || decision.BlockWork || decision.BlockAgentLaunch {
		t.Fatalf("low-power controller=%+v", decision)
	}
}
