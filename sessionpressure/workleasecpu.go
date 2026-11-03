package sessionpressure

import (
	"math"
	"os"
	"sort"
)

// Lease CPU answers "does each class's weight fit what its work uses?".
// A class weight is a guess about how much of the host a lease needs. The
// leased command's own wait4 rusage is the measurement: CPU time over runtime
// is the average number of cores the lease kept busy. It costs no sampling and
// it is exact for every descendant the command waited for (compile, link, and
// test steps under go build and go test).

// leaseCPUMinRuntimeMS keeps sub-100 ms runs, whose cores ratio is mostly
// process start-up noise, out of the per-run percentiles and totals.
const leaseCPUMinRuntimeMS = 100

type WorkLeaseCPU struct {
	Operations int                 `json:"operations"`
	ByClass    []WorkLeaseCPUClass `json:"by_class"`
}

type WorkLeaseCPUClass struct {
	Class WorkClass `json:"class"`
	// Weight is the most recent terminal event's weight for the class.
	Weight     int   `json:"weight"`
	Operations int   `json:"operations"`
	RuntimeMS  int64 `json:"runtime_ms"`
	CPUMS      int64 `json:"cpu_ms"`
	// AvgCores is runtime-weighted: total CPU over total runtime.
	AvgCores float64 `json:"avg_cores"`
	P50Cores float64 `json:"p50_cores"`
	P90Cores float64 `json:"p90_cores"`
	// ReservedCores is the runtime-weighted mean of the cores each lease's
	// weight reserved. Utilization is CPU over runtime x reserved cores, for
	// the runs that recorded a reservation; 0 when none did.
	ReservedCores float64 `json:"reserved_cores"`
	Utilization   float64 `json:"utilization"`
	PeakRSSMBP90  float64 `json:"peak_rss_mb_p90"`
	PeakRSSMBMax  float64 `json:"peak_rss_mb_max"`
}

// workCommandUsage is a reaped leased command's rusage.
type workCommandUsage struct {
	available bool
	cpuMS     int64
	peakRSSMB float64
}

func commandResourceUsage(state *os.ProcessState) workCommandUsage {
	if state == nil {
		return workCommandUsage{}
	}
	cpu := state.UserTime() + state.SystemTime()
	if cpu < 0 {
		return workCommandUsage{}
	}
	usage := workCommandUsage{available: true, cpuMS: cpu.Milliseconds()}
	if peak, ok := processStatePeakRSSMB(state); ok && !math.IsNaN(peak) && !math.IsInf(peak, 0) {
		usage.peakRSSMB = peak
	}
	return usage
}

// reservedCores is the host share that weight reserves at nominal capacity.
// It is recorded only with measured usage, so the two always describe one run.
func (usage workCommandUsage) reservedCores(weight, capacity, logicalCPUs int) float64 {
	if !usage.available || weight <= 0 || capacity <= 0 || logicalCPUs <= 0 {
		return 0
	}
	return float64(weight) / float64(capacity) * float64(logicalCPUs)
}

func summarizeLeaseCPU(events []WorkEvent) WorkLeaseCPU {
	type classRuns struct {
		row                  WorkLeaseCPUClass
		cores, peaks         []float64
		reservedCPUMS        int64
		reservedCoreRuntimeS float64
		reservedRuntimeMS    int64
	}
	byClass := map[WorkClass]*classRuns{}
	result := WorkLeaseCPU{ByClass: []WorkLeaseCPUClass{}}
	for _, event := range events {
		if !isWorkTerminalEvent(event.Event) || !event.CommandCPUAvailable || event.RuntimeMillis < leaseCPUMinRuntimeMS || event.CommandCPUMilliseconds < 0 {
			continue
		}
		runs := byClass[event.Class]
		if runs == nil {
			runs = &classRuns{row: WorkLeaseCPUClass{Class: event.Class}}
			byClass[event.Class] = runs
		}
		result.Operations++
		runs.row.Operations++
		runs.row.Weight = event.Weight
		runs.row.RuntimeMS += event.RuntimeMillis
		runs.row.CPUMS += event.CommandCPUMilliseconds
		runs.cores = append(runs.cores, float64(event.CommandCPUMilliseconds)/float64(event.RuntimeMillis))
		if event.CommandPeakRSSMB > 0 {
			runs.peaks = append(runs.peaks, event.CommandPeakRSSMB)
		}
		if event.LeaseReservedCores > 0 {
			runs.reservedCPUMS += event.CommandCPUMilliseconds
			runs.reservedRuntimeMS += event.RuntimeMillis
			runs.reservedCoreRuntimeS += event.LeaseReservedCores * float64(event.RuntimeMillis)
		}
	}
	for _, class := range AllWorkClasses() {
		runs := byClass[class]
		if runs == nil {
			continue
		}
		row := runs.row
		row.AvgCores = roundRatio(float64(row.CPUMS) / float64(row.RuntimeMS))
		row.P50Cores = roundRatio(percentileOf(runs.cores, 0.50))
		row.P90Cores = roundRatio(percentileOf(runs.cores, 0.90))
		if runs.reservedRuntimeMS > 0 && runs.reservedCoreRuntimeS > 0 {
			row.ReservedCores = roundRatio(runs.reservedCoreRuntimeS / float64(runs.reservedRuntimeMS))
			row.Utilization = roundRatio(float64(runs.reservedCPUMS) / runs.reservedCoreRuntimeS)
		}
		row.PeakRSSMBP90 = roundRatio(percentileOf(runs.peaks, 0.90))
		for _, peak := range runs.peaks {
			row.PeakRSSMBMax = max(row.PeakRSSMBMax, roundRatio(peak))
		}
		result.ByClass = append(result.ByClass, row)
	}
	return result
}

// percentileOf is the nearest-rank percentile; 0 for no values.
func percentileOf(values []float64, quantile float64) float64 {
	if len(values) == 0 {
		return 0
	}
	sorted := append([]float64(nil), values...)
	sort.Float64s(sorted)
	rank := int(math.Ceil(quantile*float64(len(sorted)))) - 1
	return sorted[min(max(rank, 0), len(sorted)-1)]
}
