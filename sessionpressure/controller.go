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
	Mode         ControllerMode `json:"mode"`
	CPUStress    bool           `json:"cpu_stress"`
	ThermalState ThermalState   `json:"thermal_state"`
	LowPowerMode bool           `json:"low_power_mode"`
	// BlockWork is true ONLY for hard freeze/OOM/kernel-failure survival risks
	// (memory red/critical or thermal serious/critical). Pure CPU saturation
	// never sets BlockWork.
	BlockWork bool `json:"block_work"`
	// ThrottleWork is the non-blocking soft-throttle signal for callers to cap
	// parallel local concurrency or apply background QoS under sustained CPU
	// saturation or non-external swap thrash without aborting or blocking work.
	ThrottleWork     bool `json:"throttle_work,omitempty"`
	BlockAgentLaunch bool `json:"block_agent_launch"`
	// SwapThrash mirrors the resident latch. It never sets BlockWork or
	// BlockAgentLaunch; the work gate derates heavy-work concurrency instead.
	SwapThrash bool `json:"swap_thrash,omitempty"`
	// SwapThrashCause is the resident attribution; "external" means the
	// derate no longer defers work this episode.
	SwapThrashCause string   `json:"swap_thrash_cause,omitempty"`
	Dimension       string   `json:"dimension,omitempty"`
	Reasons         []string `json:"reasons,omitempty"`
}

// ClassifyController applies the safety floors without changing the legacy
// pressure ladder. Hard work blocking (BlockWork) is reserved exclusively for
// real survival risks (memory red/critical or thermal serious/critical).
// Corroborated CPU saturation sets CPUStress and ThrottleWork (non-blocking)
// with Mode=ControllerModeStressed for clear telemetry/UI visibility without
// setting BlockWork=true when memory and thermal are healthy.
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
	swapThrash := snapshot.SwapIOAvailable && snapshot.SwapThrashLatched
	nonExternalSwapThrash := swapThrash && snapshot.SwapThrashCause != SwapThrashCauseExternal
	decision := ControllerDecision{
		Mode:         ControllerModeNormal,
		CPUStress:    cpuStress,
		ThermalState: thermal,
		LowPowerMode: snapshot.LowPowerMode,
		ThrottleWork: cpuStress || nonExternalSwapThrash,
		SwapThrash:   swapThrash,
	}
	if decision.SwapThrash {
		decision.SwapThrashCause = snapshot.SwapThrashCause
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
	if thermal == ThermalStateSerious {
		decision.Mode = ControllerModeStressed
		decision.BlockWork = true
		decision.Dimension = "thermal"
		decision.Reasons = append(decision.Reasons, "system thermal state is serious")
		return decision
	}
	if cpuStress {
		decision.Mode = ControllerModeStressed
		decision.Dimension = "cpu"
		decision.Reasons = append(decision.Reasons, fmt.Sprintf("host CPU remains at or above red with corroborated evidence (%.1f%%); soft-throttling without hard work block", snapshot.HostCPUPercent))
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
