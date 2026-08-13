//go:build !darwin && !linux

package sessionpressure

func processPeakRSSMB() float64 { return 0 }
