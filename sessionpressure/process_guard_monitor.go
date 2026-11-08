package sessionpressure

import (
	"context"
	"fmt"
	"os"
	"time"
)

func (monitor *Monitor) observeProcessIncidents(ctx context.Context, snapshot Snapshot) {
	if monitor.ProcessGuard == nil || !snapshot.ProcessInventoryAvailable {
		return
	}
	// Inventory is deliberately sparse (180s normally, 240s in low-power
	// profiles). Allow one missed capture and sampling jitter. A fixed 180s
	// gap would reset every normal delayed capture and suppress all alerts.
	monitor.ProcessGuard.maxInventoryGap = processIncidentEvidenceGap(monitor.Policy)
	if err := monitor.ProcessGuard.Observe(ctx, snapshot.ProcessInventoryCapturedAt, monitor.Sampler.currentProcessInventory()); err != nil {
		// Reuse bounded monitor error history. Failed persistence never grants
		// permission to stop, and never disables host-pressure sampling.
		if recordErr := monitor.Store.AppendEvent(TelemetryEvent{Timestamp: snapshot.Timestamp, Event: "process_guard_error", Error: err.Error()}); recordErr != nil {
			fmt.Fprintf(os.Stderr, "session-pressure: process guard: %v; record error: %v\n", err, recordErr)
		}
	}
}

func processIncidentEvidenceGap(policy Policy) time.Duration {
	gap := time.Duration(2*policy.ProcessInventoryIntervalSeconds+policy.SampleIntervalSeconds) * time.Second
	if gap < 3*time.Minute {
		return 3 * time.Minute
	}
	return gap
}
