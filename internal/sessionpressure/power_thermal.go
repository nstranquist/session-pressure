package sessionpressure

import (
	"context"
	"fmt"
	"runtime"
	"strings"
)

// ThermalState is the small, platform-neutral projection used by the
// controller. Unknown is a valid state: thermal data is advisory unless the
// platform can prove it, and an unavailable probe must never become a global
// availability dependency.
type ThermalState string

const (
	ThermalStateUnknown  ThermalState = "unknown"
	ThermalStateNominal  ThermalState = "nominal"
	ThermalStateFair     ThermalState = "fair"
	ThermalStateSerious  ThermalState = "serious"
	ThermalStateCritical ThermalState = "critical"
)

type PowerThermalStatus struct {
	ThermalState          ThermalState `json:"thermal_state"`
	ThermalAvailable      bool         `json:"thermal_available"`
	LowPowerMode          bool         `json:"low_power_mode"`
	LowPowerModeAvailable bool         `json:"low_power_mode_available"`
	Source                string       `json:"source,omitempty"`
	Error                 string       `json:"error,omitempty"`
}

func unknownPowerThermalStatus() PowerThermalStatus {
	return PowerThermalStatus{ThermalState: ThermalStateUnknown}
}

// probePowerThermal is intentionally a best-effort, low-frequency probe. The
// resident still owns pressure decisions; this only adds the system's own
// thermal/low-power signal to the typed snapshot. Darwin's pmset output is
// parsed without retaining the raw text.
func probePowerThermal(ctx context.Context, runner commandRunner) (PowerThermalStatus, error) {
	status := unknownPowerThermalStatus()
	if runtime.GOOS != "darwin" {
		status.Source = "unavailable"
		return status, nil
	}
	if runner == nil {
		return status, fmt.Errorf("power/thermal probe runner is unavailable")
	}
	therm, thermErr := runner.Run(ctx, "/usr/bin/pmset", "-g", "therm")
	// Low Power Mode is exposed by the per-source power profile, not by
	// `pmset -g batt` (which only reports charge state).
	power, powerErr := runner.Run(ctx, "/usr/bin/pmset", "-g", "custom")
	if thermErr == nil {
		status.ThermalState, status.ThermalAvailable = parseThermalState(string(therm))
	}
	if powerErr == nil {
		status.LowPowerMode, status.LowPowerModeAvailable = parseLowPowerMode(string(power))
	}
	status.Source = "pmset"
	if thermErr != nil && powerErr != nil {
		status.Error = "thermal and low-power probes unavailable"
		return status, fmt.Errorf("pmset thermal=%v low-power=%v", thermErr, powerErr)
	}
	return status, nil
}

func parseThermalState(output string) (ThermalState, bool) {
	lower := strings.ToLower(output)
	for _, state := range []struct {
		needle string
		value  ThermalState
	}{
		{"critical", ThermalStateCritical},
		{"serious", ThermalStateSerious},
		{"fair", ThermalStateFair},
		{"nominal", ThermalStateNominal},
	} {
		if strings.Contains(lower, state.needle) {
			return state.value, true
		}
	}
	return ThermalStateUnknown, false
}

func parseLowPowerMode(output string) (bool, bool) {
	lower := strings.ToLower(output)
	for _, line := range strings.Split(lower, "\n") {
		line = strings.TrimSpace(line)
		if !strings.Contains(line, "lowpowermode") && !strings.Contains(line, "low power mode") {
			continue
		}
		if strings.Contains(line, " 1") || strings.HasSuffix(line, "=1") || strings.Contains(line, " true") {
			return true, true
		}
		if strings.Contains(line, " 0") || strings.HasSuffix(line, "=0") || strings.Contains(line, " false") {
			return false, true
		}
	}
	return false, false
}
