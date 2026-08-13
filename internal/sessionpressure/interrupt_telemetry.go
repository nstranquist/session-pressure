package sessionpressure

import (
	"fmt"
	"time"
)

// WrapperInterruptProjection is the sparse privacy-safe interrupt forensics
// view for main telemetry / dashboards (counts only; no argv/outcomes list).
type WrapperInterruptProjection struct {
	SchemaVersion               int       `json:"schema_version"`
	WrapperInterruptOperations  int       `json:"wrapper_interrupt_operations"`
	WrapperInterruptRatePerHour float64   `json:"wrapper_interrupt_rate_per_hour"`
	WindowHours                 float64   `json:"window_hours"`
	GeneratedAt                 time.Time `json:"generated_at"`
	// ProjectedBytes is a conservative upper bound for budget accounting.
	ProjectedBytes int `json:"projected_bytes,omitempty"`
}

// ProjectWrapperInterruptTelemetry builds a sparse projection from review
// signals. window must be positive. Pathy free-text inputs are rejected when
// provided via optional sanity check on closed labels.
func ProjectWrapperInterruptTelemetry(ops int, window time.Duration, generatedAt time.Time) (WrapperInterruptProjection, error) {
	if window <= 0 {
		return WrapperInterruptProjection{}, fmt.Errorf("window must be positive")
	}
	if ops < 0 {
		return WrapperInterruptProjection{}, fmt.Errorf("wrapper_interrupt_operations must be non-negative")
	}
	if generatedAt.IsZero() {
		generatedAt = time.Now().UTC()
	}
	hours := window.Hours()
	if hours <= 0 {
		hours = 1
	}
	rate := float64(ops) / hours
	proj := WrapperInterruptProjection{
		SchemaVersion:               1,
		WrapperInterruptOperations:  ops,
		WrapperInterruptRatePerHour: rate,
		WindowHours:                 hours,
		GeneratedAt:                 generatedAt.UTC(),
		// Compact JSON envelope budget estimate (well under daily budgets).
		ProjectedBytes: 256,
	}
	return proj, nil
}

// ProjectWrapperInterruptFromSignals sources the count from WorkReviewSignals
// (same closed classification as work stats).
func ProjectWrapperInterruptFromSignals(signals WorkReviewSignals, window time.Duration, generatedAt time.Time) (WrapperInterruptProjection, error) {
	return ProjectWrapperInterruptTelemetry(signals.WrapperInterruptOperations, window, generatedAt)
}

// FitsTelemetryBudget reports whether adding projectedBytes would stay within
// the daily budget given bytes already written today.
func FitsTelemetryBudget(bytesToday, projectedBytes, maxBytesDay int64) bool {
	if maxBytesDay <= 0 {
		return true
	}
	if projectedBytes < 0 {
		return false
	}
	return bytesToday+projectedBytes <= maxBytesDay
}
