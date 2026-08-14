package sessionpressure

import (
	"math"
	"time"
)

const (
	memoryMomentumWindow      = 5
	memoryMomentumMinimumSpan = 30 * time.Second
	memoryMomentumMaxETA      = 60.0
)

type memoryObservation struct {
	Timestamp   time.Time
	FreePercent float64
}

func annotateMemoryMomentum(snapshot *Snapshot, history *[]memoryObservation, redThreshold float64) {
	if snapshot == nil || history == nil || snapshot.Timestamp.IsZero() {
		return
	}
	observations := append(*history, memoryObservation{Timestamp: snapshot.Timestamp, FreePercent: float64(snapshot.FreePercent)})
	if len(observations) > memoryMomentumWindow {
		observations = observations[len(observations)-memoryMomentumWindow:]
	}
	*history = observations
	momentum, slope, eta := calculateMemoryMomentum(observations, redThreshold)
	snapshot.MemoryMomentum = momentum
	snapshot.FreePercentSlopePerMinute = slope
	snapshot.MinutesToMemoryRed = eta
	snapshot.MemoryMomentumSampleCount = len(observations)
}

func calculateMemoryMomentum(observations []memoryObservation, redThreshold float64) (MemoryMomentum, float64, *float64) {
	if len(observations) < 3 {
		return MemoryMomentumUnknown, 0, nil
	}
	first := observations[0].Timestamp
	last := observations[len(observations)-1].Timestamp
	if first.IsZero() || last.Sub(first) < memoryMomentumMinimumSpan {
		return MemoryMomentumUnknown, 0, nil
	}

	// Least-squares slope dampens a single noisy free-memory reading while
	// remaining constant-cost over the five-sample resident window.
	var sumX, sumY float64
	for _, observation := range observations {
		x := observation.Timestamp.Sub(first).Minutes()
		sumX += x
		sumY += observation.FreePercent
	}
	meanX := sumX / float64(len(observations))
	meanY := sumY / float64(len(observations))
	var numerator, denominator float64
	for _, observation := range observations {
		x := observation.Timestamp.Sub(first).Minutes()
		dx := x - meanX
		numerator += dx * (observation.FreePercent - meanY)
		denominator += dx * dx
	}
	if denominator == 0 {
		return MemoryMomentumUnknown, 0, nil
	}
	slope := math.Round((numerator/denominator)*100) / 100
	momentum := MemoryMomentumSteady
	switch {
	case slope <= -4:
		momentum = MemoryMomentumRapidDecline
	case slope <= -1:
		momentum = MemoryMomentumDeclining
	case slope >= 1:
		momentum = MemoryMomentumRecovering
	}

	var eta *float64
	current := observations[len(observations)-1].FreePercent
	if slope <= -1 {
		minutes := 0.0
		if current > redThreshold {
			minutes = (current - redThreshold) / -slope
		}
		if minutes <= memoryMomentumMaxETA {
			minutes = math.Round(minutes*10) / 10
			eta = &minutes
		}
	}
	return momentum, slope, eta
}
