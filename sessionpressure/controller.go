package sessionpressure

import "fmt"

// ControllerMode separates an ordinary busy host from a host where the
// resident has evidence that it should constrain new heavy work. Warning is
// intentionally not a blocking mode; it is only exposed for UI/telemetry and
// can opt into Interactive's arrival derating.
type ControllerMode string

const (
	ControllerModeNormal    ControllerMode = "normal"
	ControllerModeBusy      ControllerMode = "busy"
	ControllerModeStressed  ControllerMode = "stressed"
	ControllerModeEmergency ControllerMode = "emergency"
)

type ControllerDecision struct {
	Mode             ControllerMode `json:"mode"`
	CPUStress        bool           `json:"cpu_stress"`
	ThermalState     ThermalState   `json:"thermal_state"`
	LowPowerMode     bool           `json:"low_power_mode"`
	BlockWork        bool           `json:"block_work"`
	BlockAgentLaunch bool           `json:"block_agent_launch"`
	Dimension        string         `json:"dimension,omitempty"`
	Reasons          []string       `json:"reasons,omitempty"`
}

// ClassifyController applies the safety floors without changing the legacy
// pressure ladder. In particular, a busy CPU warning is not a block. CPU
// stress requires current red evidence plus either resident rolling red
// evidence or a sustained monitor confirmation; this filters one-sample spikes
// and stale/unavailable attribution.
func ClassifyController(snapshot Snapshot, policy Policy) ControllerDecision {
	// Accept both a raw fixture and an already evaluated resident snapshot. The
	// evaluation is pure and makes the controller useful as a public seam while
	// preserving the same threshold ladder as AdmissionForSnapshot.
	snapshot = Evaluate(snapshot, policy)
	redCPU := policy.Thresholds.HostCPURedPercent
	if redCPU <= 0 {
		redCPU = 95
	}
	blockSamples := max(2, policy.WorkLimits.CPUBlockSamples)
	cpuStress := snapshot.HostCPUAvailable && snapshot.HostCPUPercent >= redCPU &&
		((snapshot.HostCPURollingAvailable && snapshot.HostCPURollingPercent >= redCPU) ||
			snapshot.ConsecutiveSamples >= blockSamples)
	thermal := snapshot.ThermalState
	if thermal == "" {
		thermal = ThermalStateUnknown
	}
	memory := EvaluateMemoryPressure(snapshot, policy)
	decision := ControllerDecision{
		Mode:         ControllerModeNormal,
		CPUStress:    cpuStress,
		ThermalState: thermal,
		LowPowerMode: snapshot.LowPowerMode,
	}
	if memory.Level.AtLeast(LevelCritical) || thermal == ThermalStateCritical {
		decision.Mode = ControllerModeEmergency
		decision.BlockWork = true
		decision.BlockAgentLaunch = thermal == ThermalStateCritical || memory.Level.AtLeast(policy.BlockNewAt)
		switch {
		case memory.Level.AtLeast(LevelCritical):
			decision.Dimension = "memory"
			decision.Reasons = append(decision.Reasons, memory.Reasons...)
		case thermal == ThermalStateCritical:
			decision.Dimension = "thermal"
			decision.Reasons = append(decision.Reasons, "system thermal state is critical")
		}
		return decision
	}
	if memory.Level.AtLeast(policy.BlockNewAt) {
		decision.Mode = ControllerModeStressed
		decision.BlockWork = true
		decision.BlockAgentLaunch = true
		decision.Dimension = "memory"
		decision.Reasons = append(decision.Reasons, memory.Reasons...)
		return decision
	}
	if cpuStress || thermal == ThermalStateSerious {
		decision.Mode = ControllerModeStressed
		decision.BlockWork = true
		if cpuStress {
			decision.Dimension = "cpu"
			decision.Reasons = append(decision.Reasons, fmt.Sprintf("host CPU remains at or above red with corroborated evidence (%.1f%%)", snapshot.HostCPUPercent))
		} else {
			decision.Dimension = "thermal"
			decision.Reasons = append(decision.Reasons, "system thermal state is serious")
		}
		return decision
	}
	if snapshot.Level.AtLeast(LevelWarning) || snapshot.LowPowerMode {
		decision.Mode = ControllerModeBusy
		if snapshot.LowPowerMode {
			decision.Reasons = append(decision.Reasons, "low-power mode is enabled; background work may be deprioritized")
		}
	}
	return decision
}
