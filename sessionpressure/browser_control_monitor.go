package sessionpressure

import (
	"context"
	"time"
)

// Waiting work needs fresh recovery evidence even when memory pressure is
// normal. The normal two-minute sampler cadence otherwise keeps CPU held.
func recoverySampleDelay(ctx context.Context, dir string, policy Policy, normal time.Duration) time.Duration {
	if normal <= 5*time.Second {
		return normal
	}
	control, err := BrowserControlStatus(ctx, dir, "")
	if err == nil && control.Mode != "" {
		if control.Blocked {
			return 5 * time.Second
		}
		if normal > 15*time.Second {
			normal = 15 * time.Second
		}
	}
	status, err := NewWorkCoordinator(dir, policy.WorkLimits).Status(ctx)
	if err == nil && (status.AdmissionHoldCount > 0 || (status.AdmissionLatch != nil && status.AdmissionLatch.Latched)) {
		return 5 * time.Second
	}
	return normal
}
