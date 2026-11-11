//go:build !darwin

package sessionpressure

// Preserve the existing inventory-based accounting outside the Darwin resident.
func platformCurrentRSSSource() func() (float64, error) { return nil }
