package sessionpressure

import (
	"context"
	"os"
	"sort"
	"time"
)

// Work queue v2 slice 5: the stall guard.
//
// A stall is free capacity beside a live waiter that fits, while the host
// admits work, with no acquisition for QueueStallThreshold. On 2026-11-15 the
// v1 queue idled like this for long periods (a protected pressure reservation
// held the drain, then a protected head heavier than the derated ceiling).
// Each such bug was found by a person looking at the app. The guard makes the
// condition itself observable, whatever its cause:
//
//   - live: the resident monitor calls QueueStallDetector.Observe on every
//     sample. The first stall of an episode records work_queue_stall and
//     raises one toast. The episode end records work_queue_stall_cleared.
//   - replay: ReplayQueueStalls measures the same condition over recorded
//     work events and the resident's level timeline for `work evaluate`.
//
// Host pressure is not a stall. At red or critical, or when work admission
// is refused, the guard claims nothing. At warning the derated ceiling
// (warningEffectiveCapacity) is the capacity.

const (
	QueueStallThreshold      = 30 * time.Second
	queueStallHeartbeatLimit = 30 * time.Second
	queueStallExampleLimit   = 5
)

// QueueStall describes one stall episode.
type QueueStall struct {
	Since                time.Time `json:"since"`
	DetectedAt           time.Time `json:"detected_at"`
	EndedAt              time.Time `json:"ended_at,omitzero"`
	DurationMS           int64     `json:"duration_ms"`
	OperationID          string    `json:"operation_id"`
	Class                WorkClass `json:"class"`
	Weight               int       `json:"weight"`
	Used                 int       `json:"used"`
	EffectiveCapacity    int       `json:"effective_capacity"`
	QueueDepth           int       `json:"queue_depth"`
	Level                Level     `json:"level"`
	DecisionReason       string    `json:"decision_reason,omitempty"`
	ProtectedOperationID string    `json:"protected_operation_id,omitempty"`
}

// QueueStallDetector keeps episode state across monitor samples.
type QueueStallDetector struct {
	Threshold time.Duration
	since     time.Time
	leases    map[string]bool
	alerted   *QueueStall
}

// stallCandidate returns the first live, unparked waiter that fits now.
func stallCandidate(status WorkStatus, admission Admission, effectiveCapacity int, now time.Time) (WorkWaiterStatus, bool) {
	// Allowed already encodes the policy: with enforcement on, red refuses;
	// in observe mode (guards disabled 2026-11-15) red admits, so an idle
	// queue at red is a real stall.
	if !admission.Allowed {
		return WorkWaiterStatus{}, false
	}
	free := effectiveCapacity - status.Used
	for _, waiter := range status.Waiters {
		if waiter.PressureParked || now.Sub(waiter.HeartbeatAt) > queueStallHeartbeatLimit {
			continue
		}
		if waiter.Weight <= free {
			return waiter, true
		}
	}
	return WorkWaiterStatus{}, false
}

// Observe returns a stall the first time an episode crosses the threshold,
// and the finished episode when it ends. Any new lease ends an episode:
// the queue moved.
func (detector *QueueStallDetector) Observe(now time.Time, status WorkStatus, admission Admission, effectiveCapacity int) (started, ended *QueueStall) {
	threshold := detector.Threshold
	if threshold <= 0 {
		threshold = QueueStallThreshold
	}
	current := make(map[string]bool, len(status.Leases))
	acquired := false
	for _, lease := range status.Leases {
		current[lease.ID] = true
		if detector.leases != nil && !detector.leases[lease.ID] {
			acquired = true
		}
	}
	detector.leases = current
	candidate, stalled := stallCandidate(status, admission, effectiveCapacity, now)
	if acquired || !stalled {
		if detector.alerted != nil {
			ended = detector.alerted
			ended.EndedAt = now
			ended.DurationMS = now.Sub(ended.Since).Milliseconds()
		}
		detector.since, detector.alerted = time.Time{}, nil
		if !stalled {
			return nil, ended
		}
	}
	if detector.since.IsZero() {
		detector.since = now
	}
	if detector.alerted != nil || now.Sub(detector.since) < threshold {
		return nil, ended
	}
	detector.alerted = &QueueStall{
		Since: detector.since, DetectedAt: now, DurationMS: now.Sub(detector.since).Milliseconds(),
		OperationID: candidate.OperationID, Class: candidate.Class, Weight: candidate.Weight,
		Used: status.Used, EffectiveCapacity: effectiveCapacity, QueueDepth: status.QueueDepth, Level: admission.Level,
		DecisionReason: status.DecisionReason, ProtectedOperationID: status.ProtectedOperationID,
	}
	copied := *detector.alerted
	return &copied, ended
}

// QueueStallEvent is the control-event projection of one stall.
func QueueStallEvent(name string, stall QueueStall) map[string]any {
	return map[string]any{
		"event": name, "since": stall.Since.UTC().Format(time.RFC3339Nano), "duration_ms": stall.DurationMS,
		"operation_id": stall.OperationID, "class": string(stall.Class), "weight": stall.Weight,
		"used": stall.Used, "effective_capacity": stall.EffectiveCapacity, "queue_depth": stall.QueueDepth,
		"level": string(stall.Level), "decision_reason": stall.DecisionReason, "protected_operation_id": stall.ProtectedOperationID,
	}
}

// RecordQueueStall writes a stall control event to the work control log.
func RecordQueueStall(dir, name string, stall QueueStall) {
	recordControlEvent(dir, QueueStallEvent(name, stall))
}

// LevelObservation is one host level from the resident's telemetry.
type LevelObservation struct {
	At    time.Time
	Level Level
}

// LevelTimeline extracts level observations from resident telemetry events
// (state transitions and heartbeats). A resident stop makes the level
// unknown until the next observation.
func LevelTimeline(events []TelemetryEvent) []LevelObservation {
	var timeline []LevelObservation
	for _, event := range events {
		switch {
		case event.Event == "resident_stopped":
			timeline = append(timeline, LevelObservation{At: event.Timestamp})
		case event.Snapshot != nil && event.Snapshot.Level != "":
			timeline = append(timeline, LevelObservation{At: event.Timestamp, Level: event.Snapshot.Level})
		case event.Summary != nil && event.Summary.Level != "":
			timeline = append(timeline, LevelObservation{At: event.Timestamp, Level: event.Summary.Level})
		}
	}
	sort.SliceStable(timeline, func(i, j int) bool { return timeline[i].At.Before(timeline[j].At) })
	return timeline
}

// WorkStallReplay is the trace measurement of stalls.
type WorkStallReplay struct {
	ThresholdSeconds    float64            `json:"threshold_seconds"`
	Episodes            int                `json:"episodes"`
	StallSeconds        float64            `json:"stall_seconds"`
	LongestStallSeconds float64            `json:"longest_stall_seconds"`
	LevelKnownSeconds   float64            `json:"level_known_seconds"`
	Examples            []WorkStallEpisode `json:"examples,omitempty"`
	Limitations         []string           `json:"limitations"`
}

type WorkStallEpisode struct {
	Start      time.Time `json:"start"`
	Seconds    float64   `json:"seconds"`
	Level      Level     `json:"level"`
	Used       int       `json:"used"`
	Capacity   int       `json:"capacity"`
	FitWeight  int       `json:"fit_weight"`
	QueueDepth int       `json:"queue_depth"`
}

type stallInterval struct {
	start, end time.Time
	weight     int
}

// ReplayQueueStalls reconstructs capacity-queue occupancy from work events
// and measures time when a waiter fit, the host level allowed work, and no
// lease started. Queue entry is acquired_at minus queue_wait_ms, so only
// waiters that later acquired are seen.
func ReplayQueueStalls(events []WorkEvent, levels []LevelObservation, limits WorkLimits, threshold time.Duration) WorkStallReplay {
	if threshold <= 0 {
		threshold = QueueStallThreshold
	}
	limits = normalizeWorkLimits(limits)
	replay := WorkStallReplay{ThresholdSeconds: threshold.Seconds(), Limitations: []string{
		"queue entry is reconstructed as acquired_at - queue_wait_ms; waiters that never acquired are not seen",
		"host level comes from resident state transitions and heartbeats; only normal-level time is judged, because the enforcement mode and warning derate at event time are not recorded",
		"capacity is the nominal capacity recorded on the work events at that time, not the current policy",
		"storage admission is per repository and is not reconstructed",
	}}
	var waiters, leases []stallInterval
	type capacityPoint struct {
		at       time.Time
		capacity int
	}
	var capacityPoints []capacityPoint
	open := map[string]WorkEvent{}
	var points []time.Time
	for _, event := range events {
		if event.Capacity > 0 {
			capacityPoints = append(capacityPoints, capacityPoint{at: event.Timestamp, capacity: event.Capacity})
		}
		switch event.Event {
		case WorkEventAcquired, WorkEventReacquired:
			if event.QueueWaitMilliseconds > 0 {
				start := event.Timestamp.Add(-time.Duration(event.QueueWaitMilliseconds) * time.Millisecond)
				waiters = append(waiters, stallInterval{start: start, end: event.Timestamp, weight: event.Weight})
				points = append(points, start)
			}
			open[event.OperationID] = event
			points = append(points, event.Timestamp)
		case WorkEventCompleted, WorkEventFailed, WorkEventCancelled, WorkEventExpired, WorkEventReserved:
			if started, ok := open[event.OperationID]; ok {
				leases = append(leases, stallInterval{start: started.Timestamp, end: event.Timestamp, weight: started.Weight})
				delete(open, event.OperationID)
				points = append(points, event.Timestamp)
			}
		}
	}
	for _, level := range levels {
		points = append(points, level.At)
	}
	if len(points) < 2 {
		return replay
	}
	sort.Slice(points, func(i, j int) bool { return points[i].Before(points[j]) })
	leaseStarts := map[int64]bool{}
	for _, lease := range leases {
		leaseStarts[lease.start.UnixNano()] = true
	}
	for _, started := range open {
		leaseStarts[started.Timestamp.UnixNano()] = true
	}
	sort.SliceStable(capacityPoints, func(i, j int) bool { return capacityPoints[i].at.Before(capacityPoints[j].at) })
	capacityAt := func(at time.Time) int {
		index := sort.Search(len(capacityPoints), func(i int) bool { return capacityPoints[i].at.After(at) }) - 1
		if index < 0 {
			if len(capacityPoints) > 0 {
				return capacityPoints[0].capacity
			}
			return limits.Capacity
		}
		return capacityPoints[index].capacity
	}
	levelAt := func(at time.Time) Level {
		index := sort.Search(len(levels), func(i int) bool { return levels[i].At.After(at) }) - 1
		if index < 0 {
			return ""
		}
		return levels[index].Level
	}
	var episode *WorkStallEpisode
	finish := func() {
		if episode != nil && episode.Seconds >= threshold.Seconds() {
			replay.Episodes++
			replay.StallSeconds += episode.Seconds - threshold.Seconds()
			replay.LongestStallSeconds = max(replay.LongestStallSeconds, episode.Seconds)
			if len(replay.Examples) < queueStallExampleLimit {
				replay.Examples = append(replay.Examples, *episode)
			}
		}
		episode = nil
	}
	for index := 0; index+1 < len(points); index++ {
		start, end := points[index], points[index+1]
		if !end.After(start) {
			continue
		}
		if leaseStarts[start.UnixNano()] {
			finish()
		}
		level := levelAt(start)
		capacity := 0
		if level == LevelNormal {
			capacity = capacityAt(start)
		}
		if level != "" {
			replay.LevelKnownSeconds += end.Sub(start).Seconds()
		}
		used, depth, fit := 0, 0, 0
		for _, lease := range leases {
			if !lease.start.After(start) && lease.end.After(start) {
				used += lease.weight
			}
		}
		for _, started := range open {
			if !started.Timestamp.After(start) {
				used += started.Weight
			}
		}
		for _, waiter := range waiters {
			if !waiter.start.After(start) && waiter.end.After(start) {
				depth++
				if fit == 0 && waiter.weight <= capacity-used {
					fit = waiter.weight
				}
			}
		}
		if capacity == 0 || fit == 0 {
			finish()
			continue
		}
		if episode == nil {
			episode = &WorkStallEpisode{Start: start, Level: level, Used: used, Capacity: capacity, FitWeight: fit, QueueDepth: depth}
		}
		episode.Seconds += end.Sub(start).Seconds()
	}
	finish()
	return replay
}

// observeQueueStall runs the live stall guard on one resident sample. It
// reads the published queue snapshot without the mutation lock, so a sample
// never waits on a worker. Failures are silent: the guard is observation.
// persistedPolicyCache holds the last persisted policy the resident read,
// keyed by file modification time and size.
type persistedPolicyCache struct {
	modTime time.Time
	size    int64
	policy  Policy
	loaded  bool
}

// persistedPolicy returns the persisted policy, re-read only when the file
// changes. A read or decode error keeps the last good copy. loaded is false
// while no persisted copy has loaded; current is true only when the returned
// copy is what the file holds now.
func (monitor *Monitor) persistedPolicy(physicalMemoryMB float64) (policy Policy, loaded, current bool) {
	cache := &monitor.policyCache
	if monitor.Store == nil {
		return cache.policy, cache.loaded, false
	}
	path := PolicyPath(monitor.Store.Dir)
	info, err := os.Stat(path)
	if err != nil {
		return cache.policy, cache.loaded, false
	}
	if cache.loaded && info.ModTime().Equal(cache.modTime) && info.Size() == cache.size {
		return cache.policy, true, true
	}
	policy, persisted, err := LoadPolicy(path, physicalMemoryMB)
	if err != nil || !persisted {
		return cache.policy, cache.loaded, false
	}
	*cache = persistedPolicyCache{modTime: info.ModTime(), size: info.Size(), policy: policy, loaded: true}
	return policy, true, true
}

// stallGuardPolicy is the persisted policy, or the resident's in-memory copy
// while none has loaded.
func (monitor *Monitor) stallGuardPolicy(physicalMemoryMB float64) Policy {
	if policy, loaded, _ := monitor.persistedPolicy(physicalMemoryMB); loaded {
		return policy
	}
	return monitor.Policy
}

func (monitor *Monitor) observeQueueStall(snapshot Snapshot) {
	if monitor == nil || monitor.WorkCoordinator == nil || monitor.Store == nil || snapshot.GuardRole != "resident" {
		return
	}
	status, err := monitor.WorkCoordinator.StatusSnapshot(context.Background())
	if err != nil {
		return
	}
	// Judge with the persisted policy, not the copy loaded at resident start:
	// a capacity edit without a restart otherwise produces false stalls
	// (2026-11-16 00:50Z: capacity 1000 in memory, 16 on disk, 16/16 used).
	policy := monitor.stallGuardPolicy(snapshot.PhysicalMemoryMB)
	admission := AdmissionForWorkSnapshot(snapshot, policy, "resident-stall-guard")
	capacity := warningEffectiveCapacity(admission, normalizeWorkLimits(policy.WorkLimits))
	started, ended := monitor.queueStalls.Observe(snapshot.Timestamp, status, admission, capacity)
	if ended != nil {
		RecordQueueStall(monitor.Store.Dir, "work_queue_stall_cleared", *ended)
	}
	if started != nil {
		RecordQueueStall(monitor.Store.Dir, "work_queue_stall", *started)
		if monitor.QueueStallNotifier != nil {
			_ = monitor.QueueStallNotifier(*started)
		}
	}
}
