package sessionpressure

import (
	"fmt"
	"sort"
	"time"
)

// Wait attribution answers "why did agents wait?" with one number per cause.
// Total wait alone misleads: on 2026-11-02 builds waited up to 9 minutes while
// the swap derate held nothing. Loosening a gate because of wait it did not
// cause keeps the wait and drops the protection.
//
// Per operation, the cumulative event fields split the pre-start wait:
// queue_wait_ms (weighted capacity, keyed by the acquired event's blocker),
// admission_wait_ms plus pressure_wait_ms (a pressure gate, keyed by its first
// deferral dimension), and singleflight_wait_ms. Anything left is "other".

// Closed cause vocabulary.
const (
	WaitCauseQueueCapacity    = "queue_capacity"
	WaitCauseQueueFairness    = "queue_fairness"
	WaitCauseQueueReservation = "queue_reservation"
	WaitCauseQueueOther       = "queue_other"
	WaitCausePressureCPU      = "pressure_cpu"
	WaitCausePressureMemory   = "pressure_memory"
	WaitCausePressureStorage  = "pressure_storage"
	WaitCausePressureSwap     = "pressure_swap"
	WaitCausePressureOther    = "pressure_other"
	WaitCauseSingleflight     = "singleflight"
	WaitCauseOther            = "other"
)

const (
	// waitBurstGap joins long waits into one burst when the next one starts
	// within this gap of the previous one's end.
	waitBurstGap = 15 * time.Minute
	// waitBurstLimit keeps the compact payload bounded; the newest win.
	waitBurstLimit = 5
	// admissionOverheadMS is the admission sample itself. Admission wait
	// without a deferral and under this bound is not a pressure wait.
	admissionOverheadMS = 2000
)

type WorkWaitAttribution struct {
	TotalMS    int64           `json:"total_ms"`
	Operations int             `json:"operations"`
	Dominant   string          `json:"dominant,omitempty"`
	ByCause    []WorkWaitCause `json:"by_cause"`
	// SwapShare is pressure_swap wait over all wait, 0..1.
	SwapShare      float64         `json:"swap_share"`
	LongWaitBursts []WorkWaitBurst `json:"long_wait_bursts"`
}

type WorkWaitCause struct {
	Cause      string  `json:"cause"`
	WaitMS     int64   `json:"wait_ms"`
	Operations int     `json:"operations"`
	Share      float64 `json:"share"`
}

// WorkWaitBurst is a run of long waits (at least two minutes) close in time.
// A burst shows whether long waits are one event or a standing problem.
type WorkWaitBurst struct {
	Start      time.Time `json:"start"`
	End        time.Time `json:"end"`
	Operations int       `json:"operations"`
	WaitMS     int64     `json:"wait_ms"`
	MaxWaitMS  int64     `json:"max_wait_ms"`
	Dominant   string    `json:"dominant"`
}

type waitOperation struct {
	start                                      time.Time
	queueMS, admissionMS, pressureMS, flightMS int64
	totalMS                                    int64
	queueBlocker                               WorkBlocker
	dimension                                  string
	swapDeferred                               bool
}

func summarizeWaitAttribution(events []WorkEvent) WorkWaitAttribution {
	operations := map[string]*waitOperation{}
	order := make([]string, 0)
	for _, event := range events {
		operation := operations[event.OperationID]
		if operation == nil {
			operation = &waitOperation{start: event.Timestamp}
			operations[event.OperationID] = operation
			order = append(order, event.OperationID)
		}
		if event.Timestamp.Before(operation.start) {
			operation.start = event.Timestamp
		}
		operation.queueMS = max(operation.queueMS, event.QueueWaitMilliseconds)
		operation.admissionMS = max(operation.admissionMS, event.AdmissionWaitMilliseconds)
		operation.pressureMS = max(operation.pressureMS, event.PressureWaitMilliseconds)
		operation.flightMS = max(operation.flightMS, event.SingleflightWaitMS)
		operation.totalMS = max(operation.totalMS, event.WaitMilliseconds)
		if (event.Event == WorkEventAcquired || event.Event == WorkEventReacquired) && operation.queueBlocker == "" && isQueueBlocker(event.Blocker) {
			operation.queueBlocker = event.Blocker
		}
		if operation.dimension == "" && event.PressureDimension != "" {
			operation.dimension = event.PressureDimension
		}
		if event.AdmissionDecision == SwapThrashDeferredDecision {
			operation.swapDeferred = true
		}
	}
	type causeTotal struct {
		waitMS     int64
		operations int
	}
	totals := map[string]*causeTotal{}
	add := func(cause string, waitMS int64) {
		if waitMS <= 0 {
			return
		}
		total := totals[cause]
		if total == nil {
			total = &causeTotal{}
			totals[cause] = total
		}
		total.waitMS += waitMS
		total.operations++
	}
	var attribution WorkWaitAttribution
	type longWait struct {
		start, end time.Time
		waitMS     int64
		causes     map[string]int64
	}
	longWaits := make([]longWait, 0)
	for _, id := range order {
		operation := operations[id]
		parts := operation.causes()
		var opTotal int64
		for cause, waitMS := range parts {
			add(cause, waitMS)
			opTotal += waitMS
		}
		if opTotal <= 0 {
			continue
		}
		attribution.Operations++
		attribution.TotalMS += opTotal
		if opTotal >= int64((2*time.Minute)/time.Millisecond) {
			longWaits = append(longWaits, longWait{
				start: operation.start, end: operation.start.Add(time.Duration(opTotal) * time.Millisecond),
				waitMS: opTotal, causes: parts,
			})
		}
	}
	attribution.ByCause = make([]WorkWaitCause, 0, len(totals))
	for cause, total := range totals {
		share := 0.0
		if attribution.TotalMS > 0 {
			share = float64(total.waitMS) / float64(attribution.TotalMS)
		}
		attribution.ByCause = append(attribution.ByCause, WorkWaitCause{Cause: cause, WaitMS: total.waitMS, Operations: total.operations, Share: share})
	}
	sort.Slice(attribution.ByCause, func(i, j int) bool {
		if attribution.ByCause[i].WaitMS != attribution.ByCause[j].WaitMS {
			return attribution.ByCause[i].WaitMS > attribution.ByCause[j].WaitMS
		}
		return attribution.ByCause[i].Cause < attribution.ByCause[j].Cause
	})
	if len(attribution.ByCause) > 0 {
		attribution.Dominant = attribution.ByCause[0].Cause
	}
	if total := totals[WaitCausePressureSwap]; total != nil && attribution.TotalMS > 0 {
		attribution.SwapShare = float64(total.waitMS) / float64(attribution.TotalMS)
	}
	sort.Slice(longWaits, func(i, j int) bool { return longWaits[i].start.Before(longWaits[j].start) })
	bursts := make([]WorkWaitBurst, 0)
	burstCauses := make([]map[string]int64, 0)
	for _, wait := range longWaits {
		last := len(bursts) - 1
		if last < 0 || wait.start.After(bursts[last].End.Add(waitBurstGap)) {
			bursts = append(bursts, WorkWaitBurst{Start: wait.start, End: wait.end})
			burstCauses = append(burstCauses, map[string]int64{})
			last++
		}
		burst := &bursts[last]
		if wait.end.After(burst.End) {
			burst.End = wait.end
		}
		burst.Operations++
		burst.WaitMS += wait.waitMS
		burst.MaxWaitMS = max(burst.MaxWaitMS, wait.waitMS)
		for cause, waitMS := range wait.causes {
			burstCauses[last][cause] += waitMS
		}
	}
	for index := range bursts {
		bursts[index].Dominant = dominantCause(burstCauses[index])
	}
	if len(bursts) > waitBurstLimit {
		bursts = bursts[len(bursts)-waitBurstLimit:]
	}
	attribution.LongWaitBursts = bursts
	return attribution
}

// causes splits one operation's wait. The parts never exceed its recorded
// pre-start wait when that is known.
func (operation *waitOperation) causes() map[string]int64 {
	parts := map[string]int64{}
	if operation.queueMS > 0 {
		cause := WaitCauseQueueOther
		switch operation.queueBlocker {
		case WorkBlockerCapacity:
			cause = WaitCauseQueueCapacity
		case WorkBlockerFairness:
			cause = WaitCauseQueueFairness
		case WorkBlockerReservation:
			cause = WaitCauseQueueReservation
		}
		parts[cause] += operation.queueMS
	}
	pressureMS := max(int64(0), operation.pressureMS)
	admissionMS := max(int64(0), operation.admissionMS)
	dimension := operation.dimension
	if dimension == "" && operation.swapDeferred {
		dimension = "swap"
	}
	if dimension == "" && admissionMS < admissionOverheadMS {
		// The admission sample, not a pressure wait.
		parts[WaitCauseOther] += admissionMS
		admissionMS = 0
	}
	if gate := pressureMS + admissionMS; gate > 0 {
		parts[pressureCause(dimension)] += gate
	}
	if operation.flightMS > 0 {
		parts[WaitCauseSingleflight] += operation.flightMS
	}
	var sum int64
	for _, waitMS := range parts {
		sum += waitMS
	}
	if operation.totalMS > sum {
		parts[WaitCauseOther] += operation.totalMS - sum
	}
	for cause, waitMS := range parts {
		if waitMS <= 0 {
			delete(parts, cause)
		}
	}
	return parts
}

func pressureCause(dimension string) string {
	switch dimension {
	case "cpu":
		return WaitCausePressureCPU
	case "memory":
		return WaitCausePressureMemory
	case "storage":
		return WaitCausePressureStorage
	case "swap":
		return WaitCausePressureSwap
	default:
		return WaitCausePressureOther
	}
}

func dominantCause(causes map[string]int64) string {
	best, bestMS := "", int64(-1)
	for cause, waitMS := range causes {
		if waitMS > bestMS || (waitMS == bestMS && cause < best) {
			best, bestMS = cause, waitMS
		}
	}
	return best
}

// relateSwapDerateToAllWait puts the derate's wait beside all wait, so a
// reader who sees long waits can tell at once whether the derate caused them.
func relateSwapDerateToAllWait(cost *WorkSwapDerateCost, attribution WorkWaitAttribution) {
	cost.AllWaitTotalMS = attribution.TotalMS
	cost.ShareOfAllWait = 0
	if attribution.TotalMS > 0 {
		cost.ShareOfAllWait = min(1, float64(cost.WaitTotalMS)/float64(attribution.TotalMS))
	}
	cost.Note = ""
	long := 0
	for _, burst := range attribution.LongWaitBursts {
		long += burst.Operations
	}
	if long == 0 || cost.ShareOfAllWait >= 0.5 {
		return
	}
	cost.Note = fmt.Sprintf("%d long waits in this window; the swap derate caused %.0f%% of all wait and %s caused most. Do not loosen the swap derate for these waits",
		long, cost.ShareOfAllWait*100, attribution.Dominant)
}
