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
	// A manual pause cannot recover from CPU samples. Keep the normal cadence
	// until work actually waits; otherwise an idle paused host samples forever
	// at five seconds despite its two-minute normal policy.
	if err == nil && control.Mode == "auto" {
		if control.Blocked {
			return 5 * time.Second
		}
		if normal > 15*time.Second {
			normal = 15 * time.Second
		}
	}
	// This only selects a sampling deadline. It must not reconcile ownership,
	// contend with admission mutations, or write the ledger on every wakeup.
	// A stale hold can only request an extra sample; admission still validates
	// the live state at its own authority boundary.
	status, err := NewWorkCoordinator(dir, policy.WorkLimits).StatusSnapshot(ctx)
	if err == nil && (status.AdmissionHoldCount > 0 || (status.AdmissionLatch != nil && status.AdmissionLatch.Latched)) {
		return 5 * time.Second
	}
	return normal
}

// Recheck demand without taking a full host sample. An operator can enable
// automatic recovery, or work can start waiting, during a normal two-minute
// sleep. Both must receive fresh evidence without waiting for that sleep to end.
func waitForResidentSample(ctx context.Context, dir string, policy Policy, delay time.Duration) bool {
	deadline := time.Now().Add(delay)
	timer := time.NewTimer(min(delay, 5*time.Second))
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return false
		case <-timer.C:
			remaining := time.Until(deadline)
			if remaining <= 0 {
				return true
			}
			probeCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
			demand := recoverySampleDelay(probeCtx, dir, policy, delay)
			cancel()
			if demand < delay {
				return true
			}
			timer.Reset(min(time.Until(deadline), 5*time.Second))
		}
	}
}
