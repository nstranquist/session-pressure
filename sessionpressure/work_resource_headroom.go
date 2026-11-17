package sessionpressure

import (
	"fmt"
	"math"
	"slices"
	"sort"
	"strings"
	"time"
)

// Resource-headroom admission, shadow stage (WORK-1065, a candidate policy
// under WORK-3).
//
// Nico decided on 2026-11-17 that admission must not depend on a fixed,
// human-set session or slot count. It decides from live headroom and from the
// measured cost of the work, and it degrades in steps. This file extends the
// memory-headroom shadow (work_headroom.go) with CPU and disk-write headroom
// and with a ladder. Nothing here admits, refuses or changes work: the live
// queue still uses weighted capacity. Work events record the shadow decision
// so the candidate can be compared before any promotion.
//
// Every quantity scales with the host. Memory reserve is physical / 8 (from
// work_headroom.go). CPU reserve is logical CPUs / 8, at least one core. No
// constant in the decision is a session count, a slot count or a capacity.
//
// CPU headroom = logical CPUs x (1 - host CPU percent)
//              - reserve
//              - in-flight: predicted cores of leases that started less than
//                resourceCPURamp ago and are not yet in the host CPU sample.
//
// Ladder, in order: admit, throttle (lower parallelism, optional background
// disk I/O), remote (portable work goes to the gate runner), relieve (ask a
// reclaimable memory family to restart, then decide again), queue. The ladder
// never names a process to stop.

const (
	// ResourceHeadroomRuleVersion names this decision rule in events and
	// replay. Change it with any change to the estimator or the ladder.
	ResourceHeadroomRuleVersion = "mac-resource-headroom-v1"

	ResourceStepAdmit    = "admit"
	ResourceStepThrottle = "throttle"
	ResourceStepRemote   = "remote"
	ResourceStepRelieve  = "relieve"
	ResourceStepQueue    = "queue"

	ResourceDimensionNone   = "none"
	ResourceDimensionSample = "sample"
	ResourceDimensionLevel  = "level"
	ResourceDimensionSwap   = "swap"
	ResourceDimensionMemory = "memory"
	ResourceDimensionCPU    = "cpu"
	ResourceDimensionIO     = "io"

	resourceReasonFits       = "fits"
	resourceReasonNoSample   = "no_sample"
	resourceReasonCritical   = "critical"
	resourceReasonRed        = "red"
	resourceReasonRedLane    = "red_lane"
	resourceReasonThrash     = "swap_thrash"
	resourceReasonNoRoom     = "no_headroom"
	resourceReasonProgress   = "keep_progress"
	resourceReasonPartial    = "partial_headroom"
	resourceReasonDiskWrite  = "disk_write_high"
	resourceReasonFairShare  = "fair_share"
	resourceReasonReclaim    = "reclaimable_family"
	resourceReasonPortable   = "portable"
	resourceFallbackCores    = 2.0
	resourceCoresKeyMinimum  = 3
	resourceCoresScopeMin    = 5
	resourceCoresKeyPercent  = 90
	resourceCoresScopePct    = 95
	resourceReserveDivisor   = 8
	resourceFairShareMinimum = 0.5
	// resourceCPURamp is how long a new lease's predicted cores are charged.
	// The resident samples CPU every 15 s under pressure and every 90 s when
	// calm, so a lease is in the host figure after at most one calm interval.
	resourceCPURamp = 90 * time.Second
)

// coreSamples holds the average busy cores (command CPU time / runtime) of
// completed runs in one scope.
type coreHistory struct {
	byClass    map[string][]float64
	byWorkload map[string][]float64
	byKey      map[string][]float64
}

func newCoreHistory() coreHistory {
	return coreHistory{byClass: map[string][]float64{}, byWorkload: map[string][]float64{}, byKey: map[string][]float64{}}
}

// add records one completed run. Runs shorter than leaseCPUMinRuntimeMS are
// dominated by process start and say little about steady parallelism.
func (history coreHistory) add(event WorkEvent) {
	if event.Event != WorkEventCompleted || event.ThrottleCores > 0 || !event.CommandCPUAvailable || event.RuntimeMillis < leaseCPUMinRuntimeMS || event.CommandCPUMilliseconds <= 0 {
		return
	}
	cores := float64(event.CommandCPUMilliseconds) / float64(event.RuntimeMillis)
	if math.IsNaN(cores) || math.IsInf(cores, 0) {
		return
	}
	history.byClass[string(event.Class)] = append(history.byClass[string(event.Class)], cores)
	if event.Workload != "" {
		history.byWorkload[event.Workload] = append(history.byWorkload[event.Workload], cores)
	}
	if event.PlacementKey != "" {
		history.byKey[event.PlacementKey] = append(history.byKey[event.PlacementKey], cores)
	}
}

func percentileFloat(values []float64, percent int) float64 {
	if len(values) == 0 {
		return 0
	}
	sorted := slices.Clone(values)
	sort.Float64s(sorted)
	rank := (percent*len(sorted) + 99) / 100
	return sorted[max(rank, 1)-1]
}

// predict returns the busy cores of the most specific scope with enough
// samples, its source and its sample count. An unknown command gets the
// conservative fallback until one run has been measured in some scope.
func (history coreHistory) predict(class WorkClass, workload, key string) (float64, string, int) {
	if samples := history.byKey[key]; key != "" && len(samples) >= resourceCoresKeyMinimum {
		return percentileFloat(samples, resourceCoresKeyPercent), predictionByKey, len(samples)
	}
	if samples := history.byWorkload[workload]; workload != "" && len(samples) >= resourceCoresScopeMin {
		return percentileFloat(samples, resourceCoresScopePct), predictionByLoad, len(samples)
	}
	if samples := history.byClass[string(class)]; len(samples) >= resourceCoresScopeMin {
		return percentileFloat(samples, resourceCoresScopePct), predictionByClass, len(samples)
	}
	return resourceFallbackCores, predictionFallback, 0
}

// CPUHeadroom is the CPU part of the resource view.
type CPUHeadroom struct {
	Available         bool    `json:"available"`
	LogicalCPUs       int     `json:"logical_cpus"`
	HostCPUPercent    float64 `json:"host_cpu_percent"`
	FreeCores         float64 `json:"free_cores"`
	ReserveCores      float64 `json:"reserve_cores"`
	InflightCores     float64 `json:"inflight_cores"`
	HeadroomCores     float64 `json:"headroom_cores"`
	RampingLeaseCount int     `json:"ramping_lease_count"`
}

// ResourceHeadroom is the full view the candidate decision reads.
type ResourceHeadroom struct {
	Rule              string         `json:"rule"`
	Memory            MemoryHeadroom `json:"memory"`
	CPU               CPUHeadroom    `json:"cpu"`
	DiskWrite         DiskWriteState `json:"disk_write,omitempty"`
	ReclaimableMB     int64          `json:"reclaimable_mb"`
	ReclaimableFamily string         `json:"reclaimable_family,omitempty"`
}

func roundTenth(value float64) float64 { return math.Round(value*10) / 10 }

// computeResourceHeadroom is pure. It adds CPU, disk-write and reclaimable
// memory to the memory headroom of the same sample.
func computeResourceHeadroom(snapshot Snapshot, haveSample bool, leases []WorkLeaseStatus, history peakRSSHistory, now time.Time) ResourceHeadroom {
	headroom := ResourceHeadroom{Rule: ResourceHeadroomRuleVersion, Memory: computeHeadroom(snapshot, haveSample, leases, history, now)}
	for _, lease := range leases {
		if lease.Parked || (!lease.StartedAt.IsZero() && now.Sub(lease.StartedAt) >= resourceCPURamp) {
			continue
		}
		cores, _, _ := history.cores.predict(lease.Class, "", "")
		headroom.CPU.RampingLeaseCount++
		headroom.CPU.InflightCores += cores
	}
	headroom.CPU.InflightCores = roundTenth(headroom.CPU.InflightCores)
	if !haveSample {
		return headroom
	}
	if snapshot.DiskWrite != nil {
		headroom.DiskWrite = snapshot.DiskWrite.State
	}
	if family, ok := TopReclaimableMemoryFamily(snapshot.MemoryFamilies); ok {
		headroom.ReclaimableMB, headroom.ReclaimableFamily = int64(family.ReclaimableMB), family.Family
	}
	if snapshot.HostCPUAvailable && snapshot.LogicalCPUCount > 0 {
		cpu := &headroom.CPU
		cpu.Available, cpu.LogicalCPUs = true, snapshot.LogicalCPUCount
		cpu.HostCPUPercent = roundTenth(min(max(snapshot.HostCPUPercent, 0), 100))
		cpu.FreeCores = roundTenth(float64(cpu.LogicalCPUs) * (1 - cpu.HostCPUPercent/100))
		cpu.ReserveCores = max(1, roundTenth(float64(cpu.LogicalCPUs)/resourceReserveDivisor))
		cpu.HeadroomCores = roundTenth(cpu.FreeCores - cpu.ReserveCores - cpu.InflightCores)
	}
	return headroom
}

// ResourceRequest is the measured cost and context of one operation.
type ResourceRequest struct {
	PredictedPeakMB   int64
	PeakSource        string
	PredictedCores    float64
	CoresSource       string
	CoreSamples       int
	Interactive       bool
	Weight            int
	FastLaneMaxWeight int
	// RemoteEligible says the command can run on the gate runner.
	RemoteEligible bool
	// RunningLeases counts other coordinated leases that are not parked.
	RunningLeases int
	// SessionShare is the share (0 to 1) of in-flight predicted cost held by
	// the requesting session's leases; SessionLeases is how many it holds.
	// Both come from leases and work events, never from agent tree RSS: one
	// desktop app tree holds every desktop session.
	SessionShare  float64
	SessionLeases int
}

// ShadowResourceDecision is what resource-headroom admission would do now.
type ShadowResourceDecision struct {
	Step           string  `json:"step"`
	Dimension      string  `json:"dimension"`
	Reason         string  `json:"reason"`
	ThrottleCores  int     `json:"throttle_cores,omitempty"`
	BackgroundIO   bool    `json:"background_io,omitempty"`
	PredictedPeak  int64   `json:"predicted_peak_mb"`
	PeakSource     string  `json:"peak_source,omitempty"`
	PredictedCores float64 `json:"predicted_cores"`
	CoresSource    string  `json:"cores_source,omitempty"`
	CoreSamples    int     `json:"core_samples"`
	HeadroomMB     int64   `json:"headroom_mb"`
	HeadroomCores  float64 `json:"headroom_cores"`
	DiskWrite      string  `json:"disk_write,omitempty"`
	ReclaimableMB  int64   `json:"reclaimable_mb,omitempty"`
}

// Label is the compact closed-vocabulary form stored on work events.
func (decision ShadowResourceDecision) Label() string {
	if decision.Step == "" {
		return ""
	}
	return decision.Step + ":" + decision.Dimension + ":" + decision.Reason
}

// Throttle returns the throttle the decision asks for; inactive unless the
// step is throttle.
func (decision ShadowResourceDecision) Throttle() WorkThrottle {
	if decision.Step != ResourceStepThrottle {
		return WorkThrottle{}
	}
	return WorkThrottle{Cores: decision.ThrottleCores, BackgroundIO: decision.BackgroundIO}
}

var (
	resourceSteps      = []string{ResourceStepAdmit, ResourceStepThrottle, ResourceStepRemote, ResourceStepRelieve, ResourceStepQueue}
	resourceDimensions = []string{ResourceDimensionNone, ResourceDimensionSample, ResourceDimensionLevel, ResourceDimensionSwap, ResourceDimensionMemory, ResourceDimensionCPU, ResourceDimensionIO}
	resourceReasons    = []string{resourceReasonFits, resourceReasonNoSample, resourceReasonCritical, resourceReasonRed, resourceReasonRedLane,
		resourceReasonThrash, resourceReasonNoRoom, resourceReasonProgress, resourceReasonPartial, resourceReasonDiskWrite,
		resourceReasonFairShare, resourceReasonReclaim, resourceReasonPortable}
)

func validShadowResourceDecision(label string) bool {
	if label == "" {
		return true
	}
	parts := strings.Split(label, ":")
	return len(parts) == 3 && slices.Contains(resourceSteps, parts[0]) && slices.Contains(resourceDimensions, parts[1]) && slices.Contains(resourceReasons, parts[2])
}

// decideResourceAdmission is pure. It walks the ladder and stops at the first
// step that keeps every dimension above its boundary.
//
// Guarantees kept from the live queue: critical and red stay with the level
// gate, and the capped red lane still admits light interactive work. A queue
// that has nothing running never waits on a resource that only its own work
// can free: it gets a throttled admit. A missing sample fails open.
func decideResourceAdmission(headroom ResourceHeadroom, request ResourceRequest) ShadowResourceDecision {
	decision := ShadowResourceDecision{
		PredictedPeak: request.PredictedPeakMB, PeakSource: request.PeakSource,
		PredictedCores: roundTenth(request.PredictedCores), CoresSource: request.CoresSource, CoreSamples: request.CoreSamples,
		HeadroomMB: headroom.Memory.HeadroomMB, HeadroomCores: headroom.CPU.HeadroomCores,
		DiskWrite: string(headroom.DiskWrite), ReclaimableMB: headroom.ReclaimableMB,
	}
	set := func(step, dimension, reason string) ShadowResourceDecision {
		decision.Step, decision.Dimension, decision.Reason = step, dimension, reason
		return decision
	}
	// away is the step for work that cannot start here now.
	away := func(dimension, reason string, deficitMB int64) ShadowResourceDecision {
		switch {
		case request.RemoteEligible:
			return set(ResourceStepRemote, dimension, resourceReasonPortable)
		case deficitMB > 0 && headroom.ReclaimableMB >= deficitMB:
			return set(ResourceStepRelieve, dimension, resourceReasonReclaim)
		}
		return set(ResourceStepQueue, dimension, reason)
	}
	throttle := func(cores int, dimension, reason string) ShadowResourceDecision {
		decision.ThrottleCores = max(cores, 1)
		return set(ResourceStepThrottle, dimension, reason)
	}
	memory := headroom.Memory
	switch {
	case !memory.SampleFresh:
		return set(ResourceStepAdmit, ResourceDimensionSample, resourceReasonNoSample)
	case memory.Level == LevelCritical:
		return away(ResourceDimensionLevel, resourceReasonCritical, 0)
	case memory.Level == LevelRed:
		if request.Interactive && request.FastLaneMaxWeight > 0 && request.Weight <= request.FastLaneMaxWeight {
			return set(ResourceStepAdmit, ResourceDimensionLevel, resourceReasonRedLane)
		}
		return away(ResourceDimensionLevel, resourceReasonRed, 0)
	case memory.SwapThrashLatched || memory.SwapOutMBPerSecond >= headroomSwapOutRefuseMBPerSecond:
		return away(ResourceDimensionSwap, resourceReasonThrash, max(request.PredictedPeakMB, 1))
	}
	if deficit := request.PredictedPeakMB - memory.HeadroomMB; deficit > 0 {
		if request.RunningLeases == 0 {
			return throttle(1, ResourceDimensionMemory, resourceReasonProgress)
		}
		return away(ResourceDimensionMemory, resourceReasonNoRoom, deficit)
	}
	if headroom.DiskWrite == DiskWriteStateHigh {
		decision.BackgroundIO = true
	}
	if cpu := headroom.CPU; cpu.Available && request.PredictedCores > cpu.HeadroomCores {
		switch {
		case cpu.HeadroomCores >= 1:
			return throttle(int(cpu.HeadroomCores), ResourceDimensionCPU, resourceReasonPartial)
		case request.RunningLeases == 0:
			return throttle(1, ResourceDimensionCPU, resourceReasonProgress)
		}
		decision.BackgroundIO = false
		return away(ResourceDimensionCPU, resourceReasonNoRoom, 0)
	}
	if decision.BackgroundIO {
		return throttle(int(math.Ceil(request.PredictedCores)), ResourceDimensionIO, resourceReasonDiskWrite)
	}
	// Fair share: under warning the session that already holds most of the
	// in-flight cost gets half its predicted parallelism, so other sessions
	// keep moving. Interactive work is not throttled for it.
	if memory.Level == LevelWarning && !request.Interactive && request.SessionLeases >= 2 && request.SessionShare >= resourceFairShareMinimum {
		return throttle(int(request.PredictedCores/2), ResourceDimensionCPU, resourceReasonFairShare)
	}
	return set(ResourceStepAdmit, ResourceDimensionNone, resourceReasonFits)
}

// sessionCostShare returns the share of in-flight predicted cost that the
// session's leases hold, and how many leases it has. owners maps a lease's
// operation ID to its session digest (from work events). Cost is predicted
// peak memory as a share of all leases plus predicted cores as a share of all
// leases, averaged, so neither dimension dominates.
func sessionCostShare(leases []WorkLeaseStatus, owners map[string]string, session string, history peakRSSHistory) (float64, int) {
	if session == "" {
		return 0, 0
	}
	var totalMB, sessionMB, totalCores, sessionCores float64
	count := 0
	for _, lease := range leases {
		if lease.Parked {
			continue
		}
		peak := float64(lease.PredictedPeakMB)
		if peak <= 0 {
			predicted, _ := history.predict(lease.Class, "", "")
			peak = float64(predicted)
		}
		cores, _, _ := history.cores.predict(lease.Class, "", "")
		totalMB += peak
		totalCores += cores
		if owners[lease.OperationID] == session {
			sessionMB += peak
			sessionCores += cores
			count++
		}
	}
	if count == 0 || totalMB <= 0 || totalCores <= 0 {
		return 0, count
	}
	return (sessionMB/totalMB + sessionCores/totalCores) / 2, count
}

// leaseSessionDigests maps each lease's operation ID to the session digest on
// its queued event. A read fault returns an empty map: fair share is then
// skipped, never guessed.
func (store *WorkEventStore) leaseSessionDigests(leases []WorkLeaseStatus) map[string]string {
	owners := map[string]string{}
	if len(leases) == 0 {
		return owners
	}
	db, err := store.workEventIndex()
	if err != nil {
		return owners
	}
	if err := store.syncWorkEventIndex(db, ""); err != nil {
		return owners
	}
	placeholders := make([]string, 0, len(leases))
	args := []any{string(WorkEventQueued)}
	for _, lease := range leases {
		placeholders = append(placeholders, "?")
		args = append(args, lease.OperationID)
	}
	rows, err := queryWorkEventRows(db, nil, `SELECT operation_id, row_json FROM work_events WHERE event = ? AND operation_id IN (`+strings.Join(placeholders, ",")+`)`, args...)
	if err != nil {
		return owners
	}
	for _, row := range rows {
		if row.SessionDigest != "" {
			owners[row.OperationID] = row.SessionDigest
		}
	}
	return owners
}

// shadowResourceProbe computes the candidate decision for one operation. It
// reuses the memory probe's history, so the event index is read once.
type shadowResourceProbe struct {
	memory         *shadowMemoryProbe
	session        string
	remoteEligible bool
	// owners resolves lease operation IDs to session digests. Nil skips the
	// fair-share input.
	owners func([]WorkLeaseStatus) map[string]string
}

func (probe *shadowResourceProbe) decide(leases []WorkLeaseStatus, now time.Time) ShadowResourceDecision {
	if probe == nil || probe.memory == nil {
		return ShadowResourceDecision{}
	}
	memory := probe.memory
	if memory.history == nil {
		history := readPeakRSSHistory(memory.dir, now)
		memory.history = &history
	}
	history := *memory.history
	snapshot, haveSample := NewTelemetryStore(memory.dir).ReadLatest()
	headroom := computeResourceHeadroom(snapshot, haveSample, leases, history, now)
	request := ResourceRequest{Interactive: memory.interactive, Weight: memory.weight, FastLaneMaxWeight: memory.fastLane, RemoteEligible: probe.remoteEligible}
	request.PredictedPeakMB, request.PeakSource = history.predict(memory.class, memory.workload, memory.key)
	request.PredictedCores, request.CoresSource, request.CoreSamples = history.cores.predict(memory.class, memory.workload, memory.key)
	for _, lease := range leases {
		if !lease.Parked {
			request.RunningLeases++
		}
	}
	if probe.owners != nil && probe.session != "" && request.RunningLeases > 0 {
		request.SessionShare, request.SessionLeases = sessionCostShare(leases, probe.owners(leases), probe.session, history)
	}
	return decideResourceAdmission(headroom, request)
}

func (decision ShadowResourceDecision) apply(event *WorkEvent) {
	if decision.Step == "" {
		return
	}
	event.ShadowResourceDecision = decision.Label()
	event.ShadowResourceRule = ResourceHeadroomRuleVersion
	event.ShadowPredictedCores = decision.PredictedCores
	event.ShadowHeadroomCores = decision.HeadroomCores
	event.ShadowThrottleCores = decision.ThrottleCores
}

// Detail is the one-line reason for people and logs.
func (decision ShadowResourceDecision) Detail() string {
	return fmt.Sprintf("%s (%s, %s): predicted %d MB and %.1f cores (%s, %d samples) against headroom %d MB and %.1f cores",
		decision.Step, decision.Dimension, decision.Reason, decision.PredictedPeak, decision.PredictedCores,
		decision.CoresSource, decision.CoreSamples, decision.HeadroomMB, decision.HeadroomCores)
}
