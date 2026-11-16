package sessionpressure

import (
	"context"
	"fmt"
	"math"
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
// Peaks: CommandPeakRSSMB is the largest single process (wait4). Since rule
// v2 the predictor uses max(that, CommandTreePeakRSSMB), the 1 s sampled sum
// over the process tree. A tree peak shorter than one sample interval can
// still be missed. Rows from before v2 carry only the wait4 peak and age out
// of the 72 h window.
//
// v3 (2026-11-16): a replay of 442 tree-measured runs against the 72 h
// history before each (`work headroom --replay`) found v2 under-predicting
// 27.9% of runs, against about 10% for a true p90. Two causes. Legacy
// wait4-only rows undercount trees (a Swift test: 241 MB wait4, 1,543 MB
// tree) and diluted every scope. And the class fallback met first runs of
// unusual commands at its median-ish tail. v3 uses a scope's tree-measured
// samples when it has enough, else all its samples, and takes p90 for the
// check's own key but p95 for the broader workload and class scopes. Replay:
// 13.3% misses; mean prediction 439 MB to 990 MB; 18 of 399 admitted runs
// would have read no_headroom. The placement family key did not help (17.0%
// against 16.6% without it) and is not used.

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
	// HeadroomRuleVersion names this estimator in events and the headroom
	// view, so replay never mixes it with the runner's memadmit (p95 x 1.10,
	// 5 samples, 2 GiB default). Change it with any estimator change.
	// v2 (2026-11-16): samples are max(tree peak, wait4 peak). v1 rows
	// stay valid on read.
	HeadroomRuleVersion   = "mac-headroom-tree-p90-scope-p95-ramp-v4"
	headroomRuleVersionV1 = "mac-headroom-p90-v1"
	headroomRuleVersionV2 = "mac-headroom-p90-v2"
	headroomRuleVersionV3 = "mac-headroom-tree-p90-scope-p95-v3"
	// headroomInflightRamp is how long a lease's predicted peak is charged
	// as in-flight. The charge stops a burst of admissions in one sample
	// window from all seeing the same free memory; after the ramp the
	// lease's real use is in available memory. 3 minutes is about the p90
	// runtime of completed work (159 s over 1,563 runs, 2026-11-14 to 16)
	// and six 30 s pressure samples. v4 (2026-11-16): without the ramp, one
	// 30-minute restic backup charged 3.3 GB for its whole life and held
	// enforced admission shut (headroom -763 MB).
	headroomInflightRamp      = 3 * time.Minute
	headroomKeyPercentile     = 90
	headroomScopePercentile   = 95
	headroomMinMeasuredPeakMB = 8
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

// peakSamples holds one scope's peaks; tree holds only those measured with
// the tree sampler.
type peakSamples struct {
	all  []float64
	tree []float64
}

// preferred returns the tree-measured samples when there are at least
// minimum of them, else all samples when there are enough, else nil.
func (samples peakSamples) preferred(minimum int) []float64 {
	if len(samples.tree) >= minimum {
		return samples.tree
	}
	if len(samples.all) >= minimum {
		return samples.all
	}
	return nil
}

type peakRSSHistory struct {
	byClass    map[string]peakSamples
	byWorkload map[string]peakSamples
	byKey      map[string]peakSamples
}

func addPeak(groups map[string]peakSamples, name string, peak float64, tree bool) {
	samples := groups[name]
	samples.all = append(samples.all, peak)
	if tree {
		samples.tree = append(samples.tree, peak)
	}
	groups[name] = samples
}

func buildPeakRSSHistory(events []WorkEvent) peakRSSHistory {
	history := peakRSSHistory{byClass: map[string]peakSamples{}, byWorkload: map[string]peakSamples{}, byKey: map[string]peakSamples{}}
	for _, event := range events {
		// A peak under headroomMinMeasuredPeakMB is not a measurement: argv
		// probes (`true`, `exit 0`) and wrappers whose real work ran in a
		// process wait4 never saw (440 s express-build runs at about 1 MB on
		// 2026-11-15). Counting them would predict about 1 MB for real work.
		// v2: the tree peak (tree_rss.go) when sampled; it covers the
		// parallel processes and wrapped children wait4 misses.
		peak := commandMemoryPeakMB(event)
		if event.Event != WorkEventCompleted || peak < headroomMinMeasuredPeakMB {
			continue
		}
		tree := event.CommandTreePeakSamples > 0
		addPeak(history.byClass, string(event.Class), peak, tree)
		if event.Workload != "" {
			addPeak(history.byWorkload, event.Workload, peak, tree)
		}
		if event.PlacementKey != "" {
			addPeak(history.byKey, event.PlacementKey, peak, tree)
		}
	}
	return history
}

// Memory admission modes (WorkLimits.MemoryAdmission).
const (
	MemoryAdmissionShadow  = "shadow"
	MemoryAdmissionEnforce = "enforce"
)

// memoryHeadroomHold decides whether enforced memory admission holds a new
// operation. It holds only when the predicted peak does not fit measured
// headroom (no_headroom) and at least one other coordinated lease runs, so
// the queue can always make progress: an empty queue never waits on memory
// it cannot free. Missing samples fail open. Red, critical and swap thrash
// stay with the level and swap gates.
func memoryHeadroomHold(decision ShadowMemoryDecision, runningLeases int) (bool, string) {
	if decision.Admit || decision.Reason != headroomNoRoom || runningLeases == 0 {
		return false, ""
	}
	return true, fmt.Sprintf("memory headroom: predicted peak %d MB (%s) exceeds headroom %d MB with %d coordinated leases running",
		decision.PredictedPeakMB, decision.PredictionSource, decision.HeadroomMB, runningLeases)
}

// commandMemoryPeakMB is the best measured peak of one command.
func commandMemoryPeakMB(event WorkEvent) float64 {
	return max(event.CommandPeakRSSMB, event.CommandTreePeakRSSMB)
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

// predict returns the peak of the most specific scope with enough samples:
// p90 of the same check, else p95 of the workload, else p95 of the class.
func (history peakRSSHistory) predict(class WorkClass, workload, key string) (int64, string) {
	if samples := history.byKey[key].preferred(headroomKeyMinSamples); key != "" && samples != nil {
		return percentileMB(samples, headroomKeyPercentile), predictionByKey
	}
	if samples := history.byWorkload[workload].preferred(headroomScopeMinSamples); workload != "" && samples != nil {
		return percentileMB(samples, headroomScopePercentile), predictionByLoad
	}
	if samples := history.byClass[string(class)].preferred(headroomScopeMinSamples); samples != nil {
		return percentileMB(samples, headroomScopePercentile), predictionByClass
	}
	return headroomFallbackPeakMB, predictionFallback
}

func (history peakRSSHistory) stats() []PeakRSSStats {
	var rows []PeakRSSStats
	add := func(scope string, groups map[string]peakSamples, minimum, limit int) {
		var scoped []PeakRSSStats
		for name, group := range groups {
			samples := group.preferred(minimum)
			if samples == nil {
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
	Rule               string         `json:"rule"`
	SchemaVersion      int            `json:"schema_version"`
	SampledAt          time.Time      `json:"sampled_at,omitzero"`
	SampleFresh        bool           `json:"sample_fresh"`
	Level              Level          `json:"level,omitempty"`
	PhysicalMB         int64          `json:"physical_mb"`
	AvailableMB        int64          `json:"available_mb"`
	ReserveMB          int64          `json:"reserve_mb"`
	LeaseCount         int            `json:"lease_count"`
	LeasePredictedMB   int64          `json:"lease_predicted_mb"`
	RampingLeaseCount  int            `json:"ramping_lease_count"`
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
	headroom := MemoryHeadroom{Rule: HeadroomRuleVersion, SchemaVersion: headroomSchemaVersion, PeakRSSWindowHours: int(headroomHistoryWindow.Hours())}
	for _, lease := range leases {
		if lease.Parked {
			continue
		}
		predicted := lease.PredictedPeakMB
		if predicted <= 0 {
			predicted, _ = history.predict(lease.Class, "", "")
		}
		headroom.LeaseCount++
		if lease.StartedAt.IsZero() || now.Sub(lease.StartedAt) < headroomInflightRamp {
			headroom.RampingLeaseCount++
			headroom.LeasePredictedMB += predicted
		}
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
	// Leased RSS is one sum over all leases. Subtract it only when every
	// lease is still ramping; otherwise an old lease's memory would cancel a
	// new lease's charge.
	if headroom.LeasedRSSKnown && headroom.RampingLeaseCount == headroom.LeaseCount {
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

// prediction returns this operation's predicted peak from its most specific
// scope.
func (probe *shadowMemoryProbe) prediction(now time.Time) int64 {
	if probe == nil {
		return 0
	}
	if probe.history == nil {
		history := readPeakRSSHistory(probe.dir, now)
		probe.history = &history
	}
	predicted, _ := probe.history.predict(probe.class, probe.workload, probe.key)
	return predicted
}

func (probe *shadowMemoryProbe) decide(admission Admission, leases []WorkLeaseStatus, now time.Time) ShadowMemoryDecision {
	if probe == nil {
		return ShadowMemoryDecision{}
	}
	if probe.history == nil {
		history := readPeakRSSHistory(probe.dir, now)
		probe.history = &history
	}
	// The resident sample is the one source, as for `work headroom`. The
	// admission's own snapshot is not used: a storage-warning admission
	// (ConfiguredWorkAdmission) carries no memory fields, which made every
	// live decision no_sample on 2026-11-15.
	snapshot, haveSample := NewTelemetryStore(probe.dir).ReadLatest()
	headroom := computeHeadroom(snapshot, haveSample, leases, *probe.history, now)
	// The level is the resident's too. Work admission reports normal in
	// observe mode (guards disabled 2026-11-15), which would read as "fits"
	// on a red host.
	_ = admission
	predicted, source := probe.history.predict(probe.class, probe.workload, probe.key)
	return decideMemoryAdmission(headroom, predicted, source, probe.interactive, probe.weight, probe.fastLane)
}

func (decision ShadowMemoryDecision) apply(event *WorkEvent) {
	if decision.Reason == "" {
		return
	}
	event.ShadowMemoryDecision = decision.Label()
	event.ShadowMemoryRule = HeadroomRuleVersion
	event.ShadowPredictedPeakMB = decision.PredictedPeakMB
	event.ShadowPredictionSource = decision.PredictionSource
	event.ShadowHeadroomMB = decision.HeadroomMB
}

// HeadroomReplay scores the current predictor against completed runs: each
// tree-measured run is predicted from the completed runs in the history
// window before it, as live admission would have seen them.
type HeadroomReplay struct {
	Rule            string         `json:"rule"`
	Runs            int            `json:"runs"`
	Misses          int            `json:"misses"`
	MissPercent     float64        `json:"miss_percent"`
	BigMisses       int            `json:"big_misses"`
	BigMissMB       int            `json:"big_miss_threshold_mb"`
	MeanPredictedMB int64          `json:"mean_predicted_mb"`
	MeanActualMB    int64          `json:"mean_actual_mb"`
	BySource        map[string]int `json:"runs_by_source"`
	MissesBySource  map[string]int `json:"misses_by_source"`
}

const headroomReplayBigMissMB = 1024

// ReplayHeadroomPredictor evaluates runs completed at or after from. Events
// must cover from minus the history window for a fair replay.
func ReplayHeadroomPredictor(events []WorkEvent, from time.Time) HeadroomReplay {
	sorted := slices.Clone(events)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Timestamp.Before(sorted[j].Timestamp) })
	replay := HeadroomReplay{Rule: HeadroomRuleVersion, BigMissMB: headroomReplayBigMissMB, BySource: map[string]int{}, MissesBySource: map[string]int{}}
	var predictedSum, actualSum float64
	start := 0
	for i, event := range sorted {
		peak := commandMemoryPeakMB(event)
		if event.Event != WorkEventCompleted || event.CommandTreePeakSamples == 0 || peak < headroomMinMeasuredPeakMB || event.Timestamp.Before(from) {
			continue
		}
		for start < i && sorted[start].Timestamp.Before(event.Timestamp.Add(-headroomHistoryWindow)) {
			start++
		}
		predicted, source := buildPeakRSSHistory(sorted[start:i]).predict(event.Class, event.Workload, event.PlacementKey)
		replay.Runs++
		replay.BySource[source]++
		predictedSum += float64(predicted)
		actualSum += peak
		if peak > float64(predicted) {
			replay.Misses++
			replay.MissesBySource[source]++
		}
		if peak > float64(predicted+headroomReplayBigMissMB) {
			replay.BigMisses++
		}
	}
	if replay.Runs > 0 {
		replay.MissPercent = math.Round(1000*float64(replay.Misses)/float64(replay.Runs)) / 10
		replay.MeanPredictedMB = int64(predictedSum/float64(replay.Runs) + 0.5)
		replay.MeanActualMB = int64(actualSum/float64(replay.Runs) + 0.5)
	}
	return replay
}

// ReadHeadroomReplay replays the predictor over runs completed in the last
// days, with a full history window before the first of them.
func ReadHeadroomReplay(dir string, days int, now time.Time) (HeadroomReplay, error) {
	from := now.Add(-time.Duration(days) * 24 * time.Hour)
	events, err := NewWorkEventStore(dir).completedRowsSince(from.Add(-headroomHistoryWindow))
	if err != nil {
		return HeadroomReplay{}, err
	}
	return ReplayHeadroomPredictor(events, from), nil
}
