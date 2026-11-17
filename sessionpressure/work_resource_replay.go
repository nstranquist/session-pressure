package sessionpressure

import (
	"slices"
	"sort"
	"time"
)

// Resource-headroom replay (WORK-1065, under WORK-3's `work evaluate`).
//
// The scheduler replay (workreplay.go) models queue order on retained
// arrivals and runtimes and leaves host pressure out. This replay adds the
// host: for each lease that the live queue started, it rebuilds the host
// sample, the running leases and the cost history as they were, and asks the
// resource candidate what it would have done. It then looks at the host in
// the minutes after that start.
//
// It answers two questions:
//   - Safety: of the starts that a swap-thrash latch or a critical level
//     followed, how many would the candidate also have started unthrottled?
//   - Cost: per class, what share of starts would the candidate have delayed
//     (queue, remote or relieve) or throttled, beside the p95 wait that the
//     live queue gave that class?
//
// It is a counterfactual. It does not move later arrivals. The candidate's
// wait is therefore an estimate: for a start that the candidate would hold
// (queue or relieve), the replay steps through the later host samples, with
// the leases the live queue had running then, until the candidate would let
// the work start. That time is added to the wait the live queue gave. It
// cannot stand in for an elapsed shadow window.

const (
	resourceReplaySchemaVersion = 1
	// A heartbeat arrives every 5 minutes on a calm host; transitions arrive
	// when the level changes. A start with no sample this recent is skipped.
	resourceReplaySampleMaxAge = 6 * time.Minute
	// resourceReplayPressureWindow is how long after a start a thrash latch
	// or critical level counts as following it.
	resourceReplayPressureWindow = 10 * time.Minute
	// History is rebuilt at most this often in trace time. Rebuilding at
	// every start is quadratic over a seven-day trace.
	resourceReplayHistoryStep = 15 * time.Minute
	// resourceReplayMaxHold bounds the search for the sample at which a held
	// start would be released. A hold that reaches it is counted unresolved.
	resourceReplayMaxHold = time.Hour
)

// ResourceReplayHost supplies the host constants that compact heartbeats do
// not carry.
type ResourceReplayHost struct {
	PhysicalMemoryMB float64 `json:"physical_memory_mb"`
	LogicalCPUs      int     `json:"logical_cpus"`
}

// ResourceReplayClass is one class's row.
type ResourceReplayClass struct {
	Class            WorkClass      `json:"class"`
	Starts           int            `json:"starts"`
	Steps            map[string]int `json:"steps"`
	DelayedPercent   float64        `json:"delayed_percent"`
	ThrottledPercent float64        `json:"throttled_percent"`
	LiveWaitP95MS    int64          `json:"live_wait_p95_ms"`
	// CandidateWaitP95MS is the estimated p95 wait under the candidate: the
	// live wait plus the hold until a later host sample would release the
	// work. Remote and throttled starts add no hold.
	CandidateWaitP95MS int64 `json:"candidate_wait_p95_ms"`
}

// ResourceReplay is the report.
type ResourceReplay struct {
	SchemaVersion         int                   `json:"schema_version"`
	Rule                  string                `json:"rule"`
	From                  time.Time             `json:"from"`
	Host                  ResourceReplayHost    `json:"host"`
	Starts                int                   `json:"starts"`
	Evaluated             int                   `json:"evaluated"`
	NoHostSample          int                   `json:"no_host_sample"`
	Steps                 map[string]int        `json:"steps"`
	Dimensions            map[string]int        `json:"dimensions"`
	ByClass               []ResourceReplayClass `json:"by_class"`
	PressureWindowSeconds float64               `json:"pressure_window_seconds"`
	// StartsBeforePressure counts evaluated starts that thrash or critical
	// followed. CandidateUnthrottledBeforePressure is how many of them the
	// candidate would have started with a plain admit; the candidate's goal
	// is zero.
	// Held counts starts the candidate would hold (queue or relieve).
	// UnresolvedHolds reached resourceReplayMaxHold with no releasing sample.
	Held                               int      `json:"held"`
	UnresolvedHolds                    int      `json:"unresolved_holds"`
	HoldP50MS                          int64    `json:"hold_p50_ms"`
	HoldP95MS                          int64    `json:"hold_p95_ms"`
	StartsBeforePressure               int      `json:"starts_before_pressure"`
	CandidateUnthrottledBeforePressure int      `json:"candidate_unthrottled_before_pressure"`
	CandidateThrottledBeforePressure   int      `json:"candidate_throttled_before_pressure"`
	Limitations                        []string `json:"limitations"`
}

type resourceObservation struct {
	at       time.Time
	snapshot Snapshot
	pressure bool
}

// resourceObservations projects telemetry into host samples. A heartbeat
// summary lacks physical memory, logical CPUs, memory families and the
// disk-write state, so those come from host or stay empty.
func resourceObservations(telemetry []TelemetryEvent, host ResourceReplayHost) []resourceObservation {
	var out []resourceObservation
	for _, event := range telemetry {
		var snapshot Snapshot
		switch {
		case event.Snapshot != nil && event.Snapshot.Level != "":
			snapshot = *event.Snapshot
		case event.Summary != nil && event.Summary.Level != "":
			summary := event.Summary
			snapshot = Snapshot{Level: summary.Level, FreePercent: summary.FreePercent, SwapUsedMB: summary.SwapUsedMB,
				SwapOutMBPerSecond: summary.SwapOutMBPerSecond, SwapThrashLatched: summary.SwapThrashLatched,
				HostCPUPercent: summary.HostCPUPercent, HostCPUAvailable: true}
		default:
			continue
		}
		if snapshot.PhysicalMemoryMB <= 0 {
			snapshot.PhysicalMemoryMB = host.PhysicalMemoryMB
		}
		if snapshot.LogicalCPUCount <= 0 {
			snapshot.LogicalCPUCount = host.LogicalCPUs
		}
		out = append(out, resourceObservation{at: event.Timestamp, snapshot: snapshot,
			pressure: snapshot.SwapThrashLatched || snapshot.Level == LevelCritical})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].at.Before(out[j].at) })
	return out
}

func percentileInt64(values []int64, percent int) int64 {
	if len(values) == 0 {
		return 0
	}
	sorted := slices.Clone(values)
	slices.Sort(sorted)
	rank := (percent*len(sorted) + 99) / 100
	return sorted[max(rank, 1)-1]
}

// ReplayResourceHeadroom evaluates leases acquired at or after from. events
// must hold the completed runs of the history window before from for a fair
// cost model; telemetry must cover the same span as the starts.
func ReplayResourceHeadroom(events []WorkEvent, telemetry []TelemetryEvent, host ResourceReplayHost, fastLaneMaxWeight int, from time.Time) ResourceReplay {
	replay := ResourceReplay{SchemaVersion: resourceReplaySchemaVersion, Rule: ResourceHeadroomRuleVersion, From: from, Host: host,
		Steps: map[string]int{}, Dimensions: map[string]int{}, ByClass: []ResourceReplayClass{},
		PressureWindowSeconds: resourceReplayPressureWindow.Seconds(),
		Limitations: []string{
			"counterfactual: later arrivals are not moved; candidate_wait_p95_ms is the live wait plus an estimated hold, not a simulated queue",
			"the hold estimate reads later host samples that include the held work itself, because the live queue ran it; this overstates pressure and the hold",
			"host samples are resident heartbeats (5 min when calm) and level transitions; a start is evaluated against the latest sample up to 6 min old",
			"heartbeats carry no memory families or disk-write state, so the relieve step and the io dimension appear only at level transitions",
			"running leases are rebuilt from acquired and terminal events; a lease whose terminal event was lost stays open until the trace ends",
			"a hold with no releasing sample inside one hour is counted as one hour and as unresolved",
			"an elapsed shadow window, not this replay, is the evidence for promotion",
		}}
	sorted := slices.Clone(events)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Timestamp.Before(sorted[j].Timestamp) })
	observations := resourceObservations(telemetry, host)

	// Lease intervals, from acquired to terminal. A lease with no terminal
	// event stays open to the end of the trace.
	type leaseInterval struct {
		status  WorkLeaseStatus
		session string
		end     time.Time
	}
	var intervals []leaseInterval
	intervalIndex := map[string]int{}
	for _, event := range sorted {
		switch {
		case event.Event == WorkEventAcquired:
			intervalIndex[event.OperationID] = len(intervals)
			intervals = append(intervals, leaseInterval{session: event.SessionDigest, status: WorkLeaseStatus{OperationID: event.OperationID,
				Class: event.Class, Weight: event.Weight, StartedAt: event.Timestamp, PredictedPeakMB: event.ShadowPredictedPeakMB}})
		case isWorkTerminalEvent(event.Event):
			if index, ok := intervalIndex[event.OperationID]; ok && intervals[index].end.IsZero() {
				intervals[index].end = event.Timestamp
			}
		}
	}
	// leasesAt returns the leases running at the instant, without operation.
	// intervals is in start order, so the scan stops at the first later start.
	leasesAt := func(at time.Time, operation string) ([]WorkLeaseStatus, map[string]string) {
		var leases []WorkLeaseStatus
		owners := map[string]string{}
		for _, interval := range intervals {
			if interval.status.StartedAt.After(at) {
				break
			}
			if interval.status.OperationID == operation || (!interval.end.IsZero() && !interval.end.After(at)) {
				continue
			}
			leases = append(leases, interval.status)
			owners[interval.status.OperationID] = interval.session
		}
		return leases, owners
	}

	type classTally struct {
		row            ResourceReplayClass
		waits, heldFor []int64
	}
	classes := map[WorkClass]*classTally{}
	var holds []int64
	var history peakRSSHistory
	var historyAt time.Time
	historyStart, observationIndex := 0, 0
	for i, event := range sorted {
		if event.Event != WorkEventAcquired || event.Timestamp.Before(from) {
			continue
		}
		replay.Starts++
		for observationIndex+1 < len(observations) && !observations[observationIndex+1].at.After(event.Timestamp) {
			observationIndex++
		}
		if len(observations) == 0 || observations[observationIndex].at.After(event.Timestamp) ||
			event.Timestamp.Sub(observations[observationIndex].at) > resourceReplaySampleMaxAge {
			replay.NoHostSample++
			continue
		}
		if historyAt.IsZero() || event.Timestamp.Sub(historyAt) >= resourceReplayHistoryStep {
			for historyStart < i && sorted[historyStart].Timestamp.Before(event.Timestamp.Add(-headroomHistoryWindow)) {
				historyStart++
			}
			history, historyAt = buildPeakRSSHistory(sorted[historyStart:i]), event.Timestamp
		}
		request := ResourceRequest{Interactive: event.Interactive, Weight: event.Weight, FastLaneMaxWeight: fastLaneMaxWeight,
			RemoteEligible: event.ToolFamily == ToolFamilyGo || event.ToolFamily == ToolFamilyNode}
		request.PredictedPeakMB, request.PeakSource = history.predict(event.Class, event.Workload, event.PlacementKey)
		request.PredictedCores, request.CoresSource, request.CoreSamples = history.cores.predict(event.Class, event.Workload, event.PlacementKey)
		// decideAt asks the candidate at one host sample. The sample age was
		// checked against the replay bound; it is stamped with the decision
		// time so the live two-minute freshness rule passes.
		decideAt := func(observation resourceObservation, at time.Time) ShadowResourceDecision {
			leases, owners := leasesAt(at, event.OperationID)
			snapshot := observation.snapshot
			snapshot.Timestamp = at
			asked := request
			asked.RunningLeases = len(leases)
			asked.SessionShare, asked.SessionLeases = sessionCostShare(leases, owners, event.SessionDigest, history)
			return decideResourceAdmission(computeResourceHeadroom(snapshot, true, leases, history, at), asked)
		}
		decision := decideAt(observations[observationIndex], event.Timestamp)

		replay.Evaluated++
		replay.Steps[decision.Step]++
		replay.Dimensions[decision.Dimension]++
		tally := classes[event.Class]
		if tally == nil {
			tally = &classTally{row: ResourceReplayClass{Class: event.Class, Steps: map[string]int{}}}
			classes[event.Class] = tally
		}
		tally.row.Starts++
		tally.row.Steps[decision.Step]++
		tally.waits = append(tally.waits, event.WaitMilliseconds)
		hold := int64(0)
		if decision.Step == ResourceStepQueue || decision.Step == ResourceStepRelieve {
			replay.Held++
			hold = resourceReplayMaxHold.Milliseconds()
			released := false
			for next := observationIndex + 1; next < len(observations) && observations[next].at.Sub(event.Timestamp) <= resourceReplayMaxHold; next++ {
				if step := decideAt(observations[next], observations[next].at).Step; step != ResourceStepQueue && step != ResourceStepRelieve {
					hold, released = observations[next].at.Sub(event.Timestamp).Milliseconds(), true
					break
				}
			}
			if !released {
				replay.UnresolvedHolds++
			}
			holds = append(holds, hold)
		}
		tally.heldFor = append(tally.heldFor, event.WaitMilliseconds+hold)

		followed := false
		for next := observationIndex + 1; next < len(observations) && !observations[next].at.After(event.Timestamp.Add(resourceReplayPressureWindow)); next++ {
			if observations[next].pressure {
				followed = true
				break
			}
		}
		if followed {
			replay.StartsBeforePressure++
			switch decision.Step {
			case ResourceStepAdmit:
				replay.CandidateUnthrottledBeforePressure++
			case ResourceStepThrottle:
				replay.CandidateThrottledBeforePressure++
			}
		}
	}
	replay.HoldP50MS, replay.HoldP95MS = percentileInt64(holds, 50), percentileInt64(holds, 95)
	for _, tally := range classes {
		row := tally.row
		delayed := row.Steps[ResourceStepQueue] + row.Steps[ResourceStepRemote] + row.Steps[ResourceStepRelieve]
		row.DelayedPercent = roundTenth(100 * float64(delayed) / float64(row.Starts))
		row.ThrottledPercent = roundTenth(100 * float64(row.Steps[ResourceStepThrottle]) / float64(row.Starts))
		row.LiveWaitP95MS = percentileInt64(tally.waits, 95)
		row.CandidateWaitP95MS = percentileInt64(tally.heldFor, 95)
		replay.ByClass = append(replay.ByClass, row)
	}
	sort.Slice(replay.ByClass, func(i, j int) bool { return replay.ByClass[i].Class < replay.ByClass[j].Class })
	return replay
}

// ReadResourceReplay runs the replay for `work evaluate`. events is the
// evaluator's window; the completed runs of the history window before it are
// read here so the first starts are not predicted from an empty history.
func ReadResourceReplay(dir string, events []WorkEvent, telemetry []TelemetryEvent, limits WorkLimits, since time.Time) ResourceReplay {
	host := ResourceReplayHost{}
	if latest, ok := NewTelemetryStore(dir).ReadLatest(); ok {
		host.PhysicalMemoryMB, host.LogicalCPUs = latest.PhysicalMemoryMB, latest.LogicalCPUCount
	}
	combined := events
	if earlier, err := NewWorkEventStore(dir).completedRowsSince(since.Add(-headroomHistoryWindow)); err == nil {
		combined = make([]WorkEvent, 0, len(events)+len(earlier))
		for _, event := range earlier {
			if event.Timestamp.Before(since) {
				combined = append(combined, event)
			}
		}
		combined = append(combined, events...)
	}
	replay := ReplayResourceHeadroom(combined, telemetry, host, normalizeWorkLimits(limits).FastLaneMaxWeight, since)
	if host.PhysicalMemoryMB <= 0 || host.LogicalCPUs <= 0 {
		replay.Limitations = append(replay.Limitations, "no resident sample gave the host size; heartbeat-only samples were evaluated without memory or CPU headroom")
	}
	return replay
}
