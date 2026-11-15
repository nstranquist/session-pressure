package sessionpressure

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"time"
)

// Memory-headroom admission, shadow stage (work queue v2, 2026-11-15).
//
// The commit-delivery session asked to replace weighted capacity units with
// memory headroom: admit work when its predicted peak memory fits measured
// headroom. Nico decided on 2026-11-15 that the capped red lane stays. This
// file computes headroom and a shadow decision. Nothing here admits or
// refuses work: the live queue still uses capacity units. Work events record
// the shadow decision so the two can be compared before any promotion.
//
// Headroom = available memory (free percent x physical memory)
//          - reserve (physical / 8)
//          - in-flight: predicted peaks of running leases not yet reached
//            (sum of their predictions minus the measured RSS of leased work).
//
// Charging in-flight predictions stops a burst of admissions in one sample
// window from all seeing the same free memory.
//
// Known limit: CommandPeakRSSMB is the largest single process of a command
// (wait4), not the sum over its process tree. Parallel test binaries can
// exceed it. The in-flight term uses measured tree RSS, so only the new
// operation's own prediction carries that error.

const (
	headroomSchemaVersion   = 1
	headroomHistoryWindow   = 3 * 24 * time.Hour
	headroomFallbackPeakMB  = 1024
	headroomKeyMinSamples   = 3
	headroomScopeMinSamples = 5
	headroomMaxKeyStats     = 40
	// Decision vocabulary, recorded as "admit:<reason>" or "refuse:<reason>".
	headroomFits      = "fits"
	headroomRedLane   = "red_lane"
	headroomNoRoom    = "no_headroom"
	headroomRed       = "red"
	headroomCritical  = "critical"
	headroomThrash    = "swap_thrash"
	headroomNoSample  = "no_sample"
	headroomSampleTTL = 2 * time.Minute
	// headroomSwapOutRefuseMBPerSecond matches the default
	// Thresholds.SwapOutThrashMBPerSecond.
	headroomSwapOutRefuseMBPerSecond = 32
	predictionByKey                  = "placement_key"
	predictionByLoad                 = "workload"
	predictionByClass                = "class"
	predictionFallback               = "fallback"
)

// PeakRSSStats is one row of the peak RSS history the delivery worker reads.
type PeakRSSStats struct {
	Scope   string `json:"scope"` // class, workload or placement_key
	Name    string `json:"name"`
	Samples int    `json:"samples"`
	P50MB   int64  `json:"p50_mb"`
	P90MB   int64  `json:"p90_mb"`
}

type peakRSSHistory struct {
	byClass    map[string][]float64
	byWorkload map[string][]float64
	byKey      map[string][]float64
}

func buildPeakRSSHistory(events []WorkEvent) peakRSSHistory {
	history := peakRSSHistory{byClass: map[string][]float64{}, byWorkload: map[string][]float64{}, byKey: map[string][]float64{}}
	for _, event := range events {
		if event.Event != WorkEventCompleted || event.CommandPeakRSSMB <= 0 {
			continue
		}
		history.byClass[string(event.Class)] = append(history.byClass[string(event.Class)], event.CommandPeakRSSMB)
		if event.Workload != "" {
			history.byWorkload[event.Workload] = append(history.byWorkload[event.Workload], event.CommandPeakRSSMB)
		}
		if event.PlacementKey != "" {
			history.byKey[event.PlacementKey] = append(history.byKey[event.PlacementKey], event.CommandPeakRSSMB)
		}
	}
	return history
}

func percentileMB(values []float64, percent int) int64 {
	if len(values) == 0 {
		return 0
	}
	sorted := slices.Clone(values)
	sort.Float64s(sorted)
	rank := (percent*len(sorted) + 99) / 100
	return int64(sorted[max(rank, 1)-1] + 0.5)
}

// predict returns the p90 peak of the most specific scope with enough
// samples: the same check, then the workload, then the class.
func (history peakRSSHistory) predict(class WorkClass, workload, key string) (int64, string) {
	if samples := history.byKey[key]; key != "" && len(samples) >= headroomKeyMinSamples {
		return percentileMB(samples, 90), predictionByKey
	}
	if samples := history.byWorkload[workload]; workload != "" && len(samples) >= headroomScopeMinSamples {
		return percentileMB(samples, 90), predictionByLoad
	}
	if samples := history.byClass[string(class)]; len(samples) >= headroomScopeMinSamples {
		return percentileMB(samples, 90), predictionByClass
	}
	return headroomFallbackPeakMB, predictionFallback
}

func (history peakRSSHistory) stats() []PeakRSSStats {
	var rows []PeakRSSStats
	add := func(scope string, groups map[string][]float64, minimum, limit int) {
		var scoped []PeakRSSStats
		for name, samples := range groups {
			if len(samples) < minimum {
				continue
			}
			scoped = append(scoped, PeakRSSStats{Scope: scope, Name: name, Samples: len(samples), P50MB: percentileMB(samples, 50), P90MB: percentileMB(samples, 90)})
		}
		sort.Slice(scoped, func(i, j int) bool {
			if scoped[i].Samples != scoped[j].Samples {
				return scoped[i].Samples > scoped[j].Samples
			}
			return scoped[i].Name < scoped[j].Name
		})
		if limit > 0 && len(scoped) > limit {
			scoped = scoped[:limit]
		}
		rows = append(rows, scoped...)
	}
	add(predictionByClass, history.byClass, 1, 0)
	add(predictionByLoad, history.byWorkload, 1, 0)
	add(predictionByKey, history.byKey, headroomKeyMinSamples, headroomMaxKeyStats)
	return rows
}

// MemoryHeadroom is the one source both Mac admission layers read
// (`session-pressure --json work headroom`).
type MemoryHeadroom struct {
	SchemaVersion      int            `json:"schema_version"`
	SampledAt          time.Time      `json:"sampled_at,omitzero"`
	SampleFresh        bool           `json:"sample_fresh"`
	Level              Level          `json:"level,omitempty"`
	PhysicalMB         int64          `json:"physical_mb"`
	AvailableMB        int64          `json:"available_mb"`
	ReserveMB          int64          `json:"reserve_mb"`
	LeaseCount         int            `json:"lease_count"`
	LeasePredictedMB   int64          `json:"lease_predicted_mb"`
	LeasedRSSMB        int64          `json:"leased_rss_mb"`
	LeasedRSSKnown     bool           `json:"leased_rss_known"`
	InflightMB         int64          `json:"inflight_mb"`
	HeadroomMB         int64          `json:"headroom_mb"`
	SwapUsedMB         int64          `json:"swap_used_mb"`
	SwapOutMBPerSecond float64        `json:"swap_out_mb_per_s"`
	SwapThrashLatched  bool           `json:"swap_thrash_latched"`
	PeakRSS            []PeakRSSStats `json:"peak_rss,omitempty"`
	PeakRSSWindowHours int            `json:"peak_rss_window_hours"`
}

// computeHeadroom is pure. A stale or missing sample yields SampleFresh false
// and the shadow decision refuses with no_sample.
func computeHeadroom(snapshot Snapshot, haveSample bool, leases []WorkLeaseStatus, history peakRSSHistory, now time.Time) MemoryHeadroom {
	headroom := MemoryHeadroom{SchemaVersion: headroomSchemaVersion, PeakRSSWindowHours: int(headroomHistoryWindow.Hours())}
	for _, lease := range leases {
		if lease.Parked {
			continue
		}
		predicted, _ := history.predict(lease.Class, "", "")
		headroom.LeaseCount++
		headroom.LeasePredictedMB += predicted
	}
	if !haveSample || snapshot.PhysicalMemoryMB <= 0 {
		return headroom
	}
	headroom.SampledAt = snapshot.Timestamp
	headroom.SampleFresh = !snapshot.Timestamp.IsZero() && now.Sub(snapshot.Timestamp) <= headroomSampleTTL
	headroom.Level = snapshot.Level
	headroom.PhysicalMB = int64(snapshot.PhysicalMemoryMB)
	headroom.AvailableMB = int64(float64(snapshot.FreePercent) / 100 * snapshot.PhysicalMemoryMB)
	headroom.ReserveMB = headroom.PhysicalMB / 8
	headroom.SwapUsedMB = int64(snapshot.SwapUsedMB)
	headroom.SwapOutMBPerSecond = snapshot.SwapOutMBPerSecond
	headroom.SwapThrashLatched = snapshot.SwapThrashLatched
	if work := snapshot.CoordinatedWork; work.Available && work.Fresh {
		headroom.LeasedRSSMB, headroom.LeasedRSSKnown = int64(work.RSSSumMB), true
	}
	headroom.InflightMB = headroom.LeasePredictedMB
	if headroom.LeasedRSSKnown {
		headroom.InflightMB = max(0, headroom.LeasePredictedMB-headroom.LeasedRSSMB)
	}
	headroom.HeadroomMB = headroom.AvailableMB - headroom.ReserveMB - headroom.InflightMB
	return headroom
}

// ShadowMemoryDecision is what memory-headroom admission would do now.
type ShadowMemoryDecision struct {
	Admit            bool
	Reason           string
	PredictedPeakMB  int64
	PredictionSource string
	HeadroomMB       int64
}

func (decision ShadowMemoryDecision) Label() string {
	if decision.Admit {
		return "admit:" + decision.Reason
	}
	return "refuse:" + decision.Reason
}

// decideMemoryAdmission keeps Nico's 2026-11-15 rule: at red, only red-lane
// candidates (interactive, weight within the fast lane) are admitted. The
// live red lane adds peak RSS and lane-capacity checks; the shadow mirrors
// only its eligibility.
func decideMemoryAdmission(headroom MemoryHeadroom, predicted int64, source string, interactive bool, weight, fastLaneMaxWeight int) ShadowMemoryDecision {
	decision := ShadowMemoryDecision{PredictedPeakMB: predicted, PredictionSource: source, HeadroomMB: headroom.HeadroomMB}
	switch {
	case !headroom.SampleFresh:
		decision.Reason = headroomNoSample
	case headroom.Level == LevelCritical:
		decision.Reason = headroomCritical
	case headroom.Level == LevelRed:
		decision.Reason = headroomRed
		if interactive && fastLaneMaxWeight > 0 && weight <= fastLaneMaxWeight {
			decision.Admit, decision.Reason = true, headroomRedLane
		}
	case headroom.SwapThrashLatched || headroom.SwapOutMBPerSecond >= headroomSwapOutRefuseMBPerSecond:
		// "No swap-out in the window": free memory overstates headroom while
		// the kernel is evicting (165 MB/s swap-out beside 3 GB headroom,
		// observed 2026-11-15 16:47Z).
		decision.Reason = headroomThrash
	case predicted <= headroom.HeadroomMB:
		decision.Admit, decision.Reason = true, headroomFits
	default:
		decision.Reason = headroomNoRoom
	}
	return decision
}

func validShadowMemoryDecision(label string) bool {
	switch label {
	case "", "admit:" + headroomFits, "admit:" + headroomRedLane,
		"refuse:" + headroomNoRoom, "refuse:" + headroomRed, "refuse:" + headroomCritical,
		"refuse:" + headroomThrash, "refuse:" + headroomNoSample:
		return true
	}
	return false
}

func readPeakRSSHistory(dir string, now time.Time) peakRSSHistory {
	events, err := NewWorkEventStore(dir).completedRowsSince(now.Add(-headroomHistoryWindow))
	if err != nil {
		return buildPeakRSSHistory(nil)
	}
	return buildPeakRSSHistory(events)
}

// ReadMemoryHeadroom assembles the public headroom view from the resident's
// latest sample, the published queue snapshot and completed work history.
func ReadMemoryHeadroom(ctx context.Context, dir string, coordinator *WorkCoordinator, now time.Time) (MemoryHeadroom, error) {
	if coordinator == nil {
		return MemoryHeadroom{}, fmt.Errorf("work coordinator is required")
	}
	status, err := coordinator.StatusSnapshot(ctx)
	if err != nil {
		return MemoryHeadroom{}, err
	}
	snapshot, ok := NewTelemetryStore(dir).ReadLatest()
	history := readPeakRSSHistory(dir, now)
	headroom := computeHeadroom(snapshot, ok, status.Leases, history, now)
	headroom.PeakRSS = history.stats()
	return headroom, nil
}

// shadowMemoryProbe computes the shadow decision for one operation from an
// admission sample. History is read once per process.
type shadowMemoryProbe struct {
	dir         string
	class       WorkClass
	workload    string
	key         string
	interactive bool
	weight      int
	fastLane    int
	history     *peakRSSHistory
}

func (probe *shadowMemoryProbe) decide(admission Admission, leases []WorkLeaseStatus, now time.Time) ShadowMemoryDecision {
	if probe == nil {
		return ShadowMemoryDecision{}
	}
	if probe.history == nil {
		history := readPeakRSSHistory(probe.dir, now)
		probe.history = &history
	}
	var snapshot Snapshot
	if admission.Snapshot != nil {
		snapshot = *admission.Snapshot
	}
	headroom := computeHeadroom(snapshot, admission.Snapshot != nil, leases, *probe.history, now)
	if admission.Level != "" {
		// The work admission level is authoritative for this operation.
		headroom.Level = admission.Level
	}
	predicted, source := probe.history.predict(probe.class, probe.workload, probe.key)
	return decideMemoryAdmission(headroom, predicted, source, probe.interactive, probe.weight, probe.fastLane)
}

func (decision ShadowMemoryDecision) apply(event *WorkEvent) {
	if decision.Reason == "" {
		return
	}
	event.ShadowMemoryDecision = decision.Label()
	event.ShadowPredictedPeakMB = decision.PredictedPeakMB
	event.ShadowPredictionSource = decision.PredictionSource
	event.ShadowHeadroomMB = decision.HeadroomMB
}
