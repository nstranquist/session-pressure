package sessionpressure

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/nstranquist/session-pressure/pkg/processtree"
)

type commandRunner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

type execRunner struct{}

func (execRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	command := processtree.CommandContext(ctx, name, args...)
	output, err := command.Output()
	if err != nil {
		// Probe stdout can contain the complete process command table. Never add
		// command output to an error: sampler errors are intentionally durable and
		// may be shown by admission checks. The executable class plus wrapped OS
		// error is enough to diagnose exit, signal, and deadline failures safely.
		return nil, fmt.Errorf("%s: %w", filepath.Base(name), err)
	}
	return output, nil
}

// Sampler owns the only recurring subprocess work. Physical memory is cached;
// each subsequent sample starts two short-lived host probes. On macOS the
// bounded process inventory is native libproc/sysctl work, not a ps subprocess.
type Sampler struct {
	runner             commandRunner
	now                func() time.Time
	pid                int
	role               string
	cpuNow             func() time.Duration
	peakRSS            func() float64
	logicalCPUCount    func() int
	processSource      func(context.Context) ([]Process, string, error)
	hostCPUSource      func() (hostCPUTicks, error)
	physicalSource     func() (float64, error)
	swapSource         func() (float64, error)
	powerThermalSource func(context.Context) (PowerThermalStatus, error)
	storageSource      func(string) (StorageCapacity, error)
	workStatusSource   func(context.Context) (WorkStatus, error)
	sessionStateDir    string
	binarySHA256       string

	physicalMu          sync.Mutex
	physicalMB          float64
	inventoryMu         sync.Mutex
	inventory           []Process
	inventoryCapturedAt time.Time
	inventorySource     string
	inventoryAgentRSSMB float64
	inventoryTopRSSMB   float64
	// Scheduled resident refreshes run outside the heartbeat. Pressure-forced
	// refreshes still wait for an in-flight refresh (or perform their own
	// synchronous read) so a warning cannot silently use an old inventory.
	inventoryRefreshInFlight bool
	inventoryRefreshDone     chan struct{}
	inventoryRefreshError    string
	powerThermalMu           sync.Mutex
	powerThermal             PowerThermalStatus
	powerThermalCapturedAt   time.Time
	powerThermalInFlight     bool
	powerThermalDone         chan struct{}
	hostCPUMu                sync.Mutex
	hostCPULast              hostCPUTicks
	hostCPULastAt            time.Time
	hostCPULastWallAt        time.Time
}

type hostCPUTicks struct {
	Busy  uint64
	Total uint64
}

const (
	hostCPUInitialPollInterval         = 50 * time.Millisecond
	hostCPUInitialMaxWait              = 1250 * time.Millisecond
	hostCPUMinimumLiveWindow           = 250 * time.Millisecond
	processCPUWarmupInterval           = 100 * time.Millisecond
	residentInventoryRefreshDelay      = 10 * time.Millisecond
	residentInventoryRefreshTimeout    = 10 * time.Second
	residentPowerThermalRefreshDelay   = 10 * time.Millisecond
	residentPowerThermalRefreshTimeout = 5 * time.Second
)

// Keep latest/status useful while bounding every heartbeat below the daily
// telemetry budget even on a machine with dozens of agent roots.
const maxProjectedAgentTrees = 24

func NewSampler() *Sampler {
	return &Sampler{runner: execRunner{}, now: time.Now, pid: os.Getpid(), role: "operator", cpuNow: processCPUTime, peakRSS: processPeakRSSMB, logicalCPUCount: runtime.NumCPU, processSource: nativeProcesses, hostCPUSource: nativeHostCPUTicks, physicalSource: nativePhysicalMemoryMB, swapSource: nativeSwapUsedMB, powerThermalSource: func(ctx context.Context) (PowerThermalStatus, error) { return probePowerThermal(ctx, execRunner{}) }, storageSource: nativeStorageCapacity, sessionStateDir: defaultSessionStateDir()}
}

func NewResidentSampler() *Sampler {
	return &Sampler{runner: execRunner{}, now: time.Now, pid: os.Getpid(), role: "resident", cpuNow: processCPUTime, peakRSS: processPeakRSSMB, logicalCPUCount: runtime.NumCPU, processSource: nativeProcesses, hostCPUSource: nativeHostCPUTicks, physicalSource: nativePhysicalMemoryMB, swapSource: nativeSwapUsedMB, powerThermalSource: func(ctx context.Context) (PowerThermalStatus, error) { return probePowerThermal(ctx, execRunner{}) }, storageSource: nativeStorageCapacity, sessionStateDir: defaultSessionStateDir(), binarySHA256: currentExecutableSHA256()}
}

// SessionStateDir returns the hook session-state directory used for semantic
// enrichment and identity session hints.
func (sampler *Sampler) SessionStateDir() string {
	if sampler == nil || strings.TrimSpace(sampler.sessionStateDir) == "" {
		return defaultSessionStateDir()
	}
	return sampler.sessionStateDir
}

// WithWorkCoordinator enables diagnostic-only attribution of active work
// leases. Sampling and admission remain functional when the coordinator is nil
// or unavailable; no pressure decision consumes the resulting bucket.
func (sampler *Sampler) WithWorkCoordinator(coordinator *WorkCoordinator) *Sampler {
	if sampler == nil {
		return nil
	}
	if coordinator == nil {
		sampler.workStatusSource = nil
		return sampler
	}
	sampler.workStatusSource = coordinator.Status
	return sampler
}

func (sampler *Sampler) probePowerThermal(ctx context.Context) PowerThermalStatus {
	status := unknownPowerThermalStatus()
	if sampler == nil || sampler.powerThermalSource == nil {
		return status
	}
	value, err := sampler.powerThermalSource(ctx)
	if value.ThermalState == "" {
		value.ThermalState = ThermalStateUnknown
	}
	if err != nil && value.Error == "" {
		value.Error = err.Error()
	}
	if len(value.Error) > 256 {
		value.Error = value.Error[:256]
	}
	return value
}

// samplePowerThermal keeps the resident heartbeat free of pmset subprocess
// cost. The first resident sample and pressure-forced samples stay
// synchronous. A healthy resident sample returns the cached typed signal and
// schedules the next refresh in the background. A forced sample waits for an
// in-flight refresh before it accepts the cache.
func (sampler *Sampler) samplePowerThermal(ctx context.Context, policy Policy, force bool) PowerThermalStatus {
	if sampler == nil || sampler.powerThermalSource == nil || sampler.role != "resident" {
		return sampler.probePowerThermal(ctx)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	interval := time.Duration(policy.SampleIntervalSeconds) * time.Second
	if interval <= 0 {
		interval = 90 * time.Second
	}
	waitedForRefresh := false
	for {
		sampler.powerThermalMu.Lock()
		now := sampler.now().UTC()
		cached := sampler.powerThermal
		capturedAt := sampler.powerThermalCapturedAt
		age := now.Sub(capturedAt)
		if !force && !capturedAt.IsZero() && age >= 0 && age < interval {
			sampler.powerThermalMu.Unlock()
			return cached
		}
		if !force && !capturedAt.IsZero() {
			if !sampler.powerThermalInFlight {
				done := make(chan struct{})
				sampler.powerThermalInFlight = true
				sampler.powerThermalDone = done
				sampler.powerThermal.Error = ""
				sampler.powerThermalMu.Unlock()
				sampler.schedulePowerThermalRefresh(done)
				return cached
			}
			sampler.powerThermalMu.Unlock()
			return cached
		}
		if force && sampler.powerThermalInFlight {
			done := sampler.powerThermalDone
			sampler.powerThermalMu.Unlock()
			select {
			case <-done:
				waitedForRefresh = true
				continue
			case <-ctx.Done():
				return cached
			}
		}
		if force && waitedForRefresh && !capturedAt.IsZero() && cached.Error == "" {
			sampler.powerThermalMu.Unlock()
			return cached
		}
		sampler.powerThermalMu.Unlock()

		value := sampler.probePowerThermal(ctx)
		capturedAt = sampler.now().UTC()
		sampler.powerThermalMu.Lock()
		sampler.powerThermal = value
		sampler.powerThermalCapturedAt = capturedAt
		sampler.powerThermalMu.Unlock()
		return value
	}
}

func (sampler *Sampler) schedulePowerThermalRefresh(done chan struct{}) {
	time.AfterFunc(residentPowerThermalRefreshDelay, func() {
		ctx, cancel := context.WithTimeout(context.Background(), residentPowerThermalRefreshTimeout)
		defer cancel()
		value := sampler.probePowerThermal(ctx)
		capturedAt := sampler.now().UTC()
		sampler.powerThermalMu.Lock()
		sampler.powerThermal = value
		sampler.powerThermalCapturedAt = capturedAt
		sampler.powerThermalInFlight = false
		close(done)
		sampler.powerThermalDone = nil
		sampler.powerThermalMu.Unlock()
	})
}

func (sampler *Sampler) currentProcessInventory() []Process {
	if sampler == nil {
		return nil
	}
	sampler.inventoryMu.Lock()
	defer sampler.inventoryMu.Unlock()
	return append([]Process(nil), sampler.inventory...)
}

func (sampler *Sampler) PhysicalMemoryMB(ctx context.Context) (float64, error) {
	sampler.physicalMu.Lock()
	defer sampler.physicalMu.Unlock()
	if sampler.physicalMB > 0 {
		return sampler.physicalMB, nil
	}
	if sampler.physicalSource != nil {
		if physicalMB, err := sampler.physicalSource(); err == nil && physicalMB > 0 {
			sampler.physicalMB = physicalMB
			return sampler.physicalMB, nil
		}
	}
	output, err := sampler.runner.Run(ctx, "/usr/sbin/sysctl", "-n", "hw.memsize")
	if err != nil {
		return 0, err
	}
	bytes, err := strconv.ParseFloat(strings.TrimSpace(string(output)), 64)
	if err != nil || bytes <= 0 {
		return 0, fmt.Errorf("parse hw.memsize %q", strings.TrimSpace(string(output)))
	}
	sampler.physicalMB = bytes / (1024 * 1024)
	return sampler.physicalMB, nil
}

func (sampler *Sampler) swapUsedMB(ctx context.Context) (float64, error) {
	if sampler.swapSource != nil {
		if swapUsedMB, err := sampler.swapSource(); err == nil && swapUsedMB >= 0 {
			return swapUsedMB, nil
		}
	}
	output, err := sampler.runner.Run(ctx, "/usr/sbin/sysctl", "vm.swapusage")
	if err != nil {
		return 0, err
	}
	return parseSwapUsedMB(string(output))
}

func (sampler *Sampler) Sample(ctx context.Context, policy Policy) (Snapshot, error) {
	started := sampler.now()
	cpuStarted := time.Duration(0)
	if sampler.cpuNow != nil {
		cpuStarted = sampler.cpuNow()
	}
	phase := func(mark *time.Time) float64 {
		elapsed := float64(sampler.now().Sub(*mark).Microseconds()) / 1000
		*mark = sampler.now()
		return elapsed
	}
	phases := map[string]float64{}
	mark := started
	physicalMB, err := sampler.PhysicalMemoryMB(ctx)
	if err != nil {
		return Snapshot{}, err
	}
	phases["physical"] = phase(&mark)
	pressureOutput, err := sampler.runner.Run(ctx, "/usr/bin/memory_pressure", "-Q")
	if err != nil {
		return Snapshot{}, err
	}
	phases["memory_pressure"] = phase(&mark)
	freePercent, err := parseFreePercent(string(pressureOutput))
	if err != nil {
		return Snapshot{}, err
	}
	swapUsedMB, err := sampler.swapUsedMB(ctx)
	if err != nil {
		return Snapshot{}, err
	}
	phases["swap"] = phase(&mark)
	forcePowerThermal := sampler.role != "resident" || float64(freePercent) <= policy.Thresholds.FreeWarningPercent
	powerThermal := sampler.samplePowerThermal(ctx, policy, forcePowerThermal)
	phases["power_thermal"] = phase(&mark)
	// Every sampler requires a real Mach counter delta. The resident waits only
	// on its first sample; later calls already carry a baseline and return
	// immediately. This prevents a saturated host from looking normal during a
	// new resident's readiness warm-up while preserving the cheap steady path.
	hostCPUPercent, hostCPUWindow, hostCPUErr := sampler.sampleHostCPU(ctx, true)
	phases["host_cpu"] = phase(&mark)
	forceInventory := sampler.role != "resident" ||
		float64(freePercent) <= policy.Thresholds.FreeWarningPercent ||
		sampler.inventoryNeedsPressureRefresh(policy)
	processes, inventoryAt, inventoryFresh, inventorySource, inventoryErr := sampler.processInventory(ctx, policy, forceInventory)
	phases["inventory"] = phase(&mark)
	processes = enrichSampleProcesses(ctx, processes, sampler.sessionStateDir, inventoryFresh, sampler.role)
	phases["identity"] = phase(&mark)
	allTrees := buildAgentTrees(processes)
	sampler.recordInventoryPressure(allTrees)
	topHostConsumers := buildHostConsumers(processes, allTrees)
	topTrees := allTrees
	if len(topTrees) > maxProjectedAgentTrees {
		topTrees = topTrees[:maxProjectedAgentTrees]
	}
	enrichSemanticStates(topTrees, sampler.sessionStateDir)
	phases["trees"] = phase(&mark)
	storage := sampler.sampleStorage(policy, LevelNormal)
	phases["storage"] = phase(&mark)

	snapshot := Snapshot{
		SchemaVersion:              SchemaVersion,
		Timestamp:                  started.UTC(),
		FreePercent:                freePercent,
		PhysicalMemoryMB:           physicalMB,
		SwapUsedMB:                 swapUsedMB,
		HostCPUPercent:             hostCPUPercent,
		HostCPUAvailable:           hostCPUErr == nil,
		HostCPUSource:              "mach-host-statistics",
		HostCPUSampleWindowMS:      float64(hostCPUWindow.Microseconds()) / 1000,
		HostCPULivePercent:         hostCPUPercent,
		HostCPULiveWindowMS:        float64(hostCPUWindow.Microseconds()) / 1000,
		HostCPURollingPercent:      hostCPUPercent,
		HostCPURollingWindowMS:     float64(hostCPUWindow.Microseconds()) / 1000,
		HostCPURollingAvailable:    hostCPUErr == nil,
		AgentCPUAvailable:          len(processes) > 0,
		ProcessCount:               len(processes),
		AgentTreeCount:             len(allTrees),
		MemoryMomentum:             MemoryMomentumUnknown,
		TopHostConsumers:           topHostConsumers,
		TopAgentTrees:              topTrees,
		ProcessInventoryAvailable:  len(processes) > 0,
		ProcessInventoryFresh:      inventoryFresh,
		ProcessInventoryCapturedAt: inventoryAt,
		ProcessInventorySource:     inventorySource,
		GuardPID:                   sampler.pid,
		GuardBinarySHA256:          sampler.binarySHA256,
		GuardRole:                  sampler.role,
		GuardBudgetApplicable:      sampler.role == "resident",
		Storage:                    storage,
		SamplePhaseMS:              phases,
		ThermalState:               powerThermal.ThermalState,
		ThermalAvailable:           powerThermal.ThermalAvailable,
		LowPowerMode:               powerThermal.LowPowerMode,
		LowPowerModeAvailable:      powerThermal.LowPowerModeAvailable,
		PowerThermalSource:         powerThermal.Source,
		PowerThermalError:          powerThermal.Error,
	}
	if hostCPUErr != nil {
		snapshot.HostCPUSource = "process-inventory-fallback"
		snapshot.HostCPUError = hostCPUErr.Error()
		if len(snapshot.HostCPUError) > 512 {
			snapshot.HostCPUError = snapshot.HostCPUError[:512]
		}
	}
	if !inventoryAt.IsZero() {
		snapshot.ProcessInventoryAgeSeconds = max(0, started.Sub(inventoryAt).Seconds())
	}
	if inventoryErr != nil {
		snapshot.ProcessInventoryError = inventoryErr.Error()
		if len(snapshot.ProcessInventoryError) > 512 {
			snapshot.ProcessInventoryError = snapshot.ProcessInventoryError[:512]
		}
	}
	logicalCPUCount := runtime.NumCPU()
	if sampler.logicalCPUCount != nil {
		logicalCPUCount = sampler.logicalCPUCount()
	}
	if logicalCPUCount < 1 {
		logicalCPUCount = 1
	}
	snapshot.LogicalCPUCount = logicalCPUCount
	snapshot.CoordinatedWork = sampler.sampleCoordinatedWork(ctx, processes, inventoryAt, inventoryFresh, logicalCPUCount)
	phases["coordinated_work"] = phase(&mark)
	processCPUPercentSum := 0.0
	processCPUAvailable := len(processes) > 0
	for _, process := range processes {
		snapshot.ProcessRSSSumMB += float64(process.RSSKB) / 1024
		if validCPUEvidence(process.CPUAvailable, process.CPUPercent) {
			processCPUPercentSum += process.CPUPercent
		} else {
			processCPUAvailable = false
		}
		if process.PID == sampler.pid {
			snapshot.GuardRSSMB = float64(process.RSSKB) / 1024
			snapshot.GuardCPUPercent = process.CPUPercent
		}
	}
	for _, tree := range allTrees {
		snapshot.AgentRSSSumMB += tree.RSSSumMB
		snapshot.AgentCPUPercent += tree.CPUPercentSum
		if !validCPUEvidence(tree.CPUAvailable, tree.CPUPercentSum) {
			snapshot.AgentCPUAvailable = false
		}
	}
	if hostCPUErr != nil && processCPUAvailable {
		snapshot.HostCPUPercent = normalizedCPUPercent(processCPUPercentSum, logicalCPUCount)
		snapshot.HostCPUAvailable = true
	}
	snapshot.AgentCPUPercent = normalizedCPUPercent(snapshot.AgentCPUPercent, logicalCPUCount)
	if sampler.peakRSS != nil {
		snapshot.GuardPeakRSSMB = sampler.peakRSS()
	}
	snapshot.SampleDurationMS = float64(sampler.now().Sub(started).Microseconds()) / 1000
	if sampler.cpuNow != nil {
		cpuEnded := sampler.cpuNow()
		snapshot.SampleCPUTimeMS = float64((cpuEnded - cpuStarted).Microseconds()) / 1000
		snapshot.processCPUTotalMS = float64(cpuEnded.Microseconds()) / 1000
	}
	return Evaluate(snapshot, policy), nil
}

func (sampler *Sampler) sampleHostCPU(ctx context.Context, waitForInitialWindow bool) (float64, time.Duration, error) {
	if sampler.hostCPUSource == nil {
		return 0, 0, fmt.Errorf("native host CPU sampler unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	sampler.hostCPUMu.Lock()
	defer sampler.hostCPUMu.Unlock()
	now := time.Now
	if sampler.now != nil {
		now = sampler.now
	}
	current, err := sampler.hostCPUSource()
	if err != nil {
		return 0, 0, err
	}
	currentAt := now()
	currentWallAt := time.Now()
	baseline := sampler.hostCPULast
	baselineAt := sampler.hostCPULastAt
	baselineWallAt := sampler.hostCPULastWallAt
	if baselineWallAt.IsZero() {
		logicalAge := currentAt.Sub(baselineAt)
		if logicalAge > 0 {
			baselineWallAt = currentWallAt.Add(-logicalAge)
		}
	}
	if percent, ok := hostCPUPercentBetween(baseline, current); ok && cpuSampleWindow(baselineAt, currentAt, baselineWallAt, currentWallAt) >= hostCPUMinimumLiveWindow {
		window := cpuSampleWindow(baselineAt, currentAt, baselineWallAt, currentWallAt)
		sampler.hostCPULast = current
		sampler.hostCPULastAt = currentAt
		sampler.hostCPULastWallAt = currentWallAt
		return percent, window, nil
	}
	if !waitForInitialWindow {
		sampler.hostCPULast = current
		sampler.hostCPULastAt = currentAt
		sampler.hostCPULastWallAt = currentWallAt
		return 0, 0, fmt.Errorf("native host CPU baseline initialized")
	}
	if baseline.Total == 0 || baselineAt.IsZero() {
		baseline = current
		baselineAt = currentAt
		baselineWallAt = currentWallAt
	}
	// HOST_CPU_LOAD_INFO refreshes on a kernel cadence that can be coarser than
	// a fixed short sleep. Poll until the cumulative counters actually advance;
	// treating an unchanged pair as 0% would fail open during the exact surge
	// this admission path exists to catch.
	ticker := time.NewTicker(hostCPUInitialPollInterval)
	defer ticker.Stop()
	deadline := time.NewTimer(hostCPUInitialMaxWait)
	defer deadline.Stop()
	for {
		select {
		case <-ctx.Done():
			return 0, 0, ctx.Err()
		case <-deadline.C:
			return 0, 0, fmt.Errorf("native host CPU counters did not advance within %s", hostCPUInitialMaxWait)
		case <-ticker.C:
			second, sampleErr := sampler.hostCPUSource()
			if sampleErr != nil {
				return 0, 0, sampleErr
			}
			secondAt := now()
			secondWallAt := time.Now()
			percent, ok := hostCPUPercentBetween(baseline, second)
			window := cpuSampleWindow(baselineAt, secondAt, baselineWallAt, secondWallAt)
			if !ok || window < hostCPUMinimumLiveWindow {
				continue
			}
			sampler.hostCPULast = second
			sampler.hostCPULastAt = secondAt
			sampler.hostCPULastWallAt = secondWallAt
			return percent, window, nil
		}
	}
}

func cpuSampleWindow(previousAt, currentAt, previousWallAt, currentWallAt time.Time) time.Duration {
	window := currentAt.Sub(previousAt)
	if wallWindow := currentWallAt.Sub(previousWallAt); wallWindow > window {
		window = wallWindow
	}
	return max(time.Duration(0), window)
}

func hostCPUPercentBetween(previous, current hostCPUTicks) (float64, bool) {
	if previous.Total == 0 || current.Total <= previous.Total || current.Busy < previous.Busy {
		return 0, false
	}
	totalDelta := current.Total - previous.Total
	busyDelta := current.Busy - previous.Busy
	if busyDelta > totalDelta {
		return 0, false
	}
	return min(100, float64(busyDelta)*100/float64(totalDelta)), true
}

func (sampler *Sampler) readProcesses(ctx context.Context) ([]Process, string, error) {
	if sampler.processSource != nil {
		processes, source, err := sampler.processSource(ctx)
		if err == nil {
			return processes, source, nil
		}
		// The singleton must fail lightweight: a native API outage disables tree
		// actions but must not resurrect the multi-second, prompt-bearing ps scan
		// that this implementation replaced. Explicit operator snapshots retain
		// the diagnostic fallback below.
		if sampler.role == "resident" {
			return nil, source, err
		}
	}
	processOutput, err := sampler.runner.Run(ctx, "/bin/ps", "-axo", "pid=,ppid=,rss=,%cpu=,etime=,command=")
	if err != nil {
		return nil, "ps", err
	}
	processes, err := parseProcesses(string(processOutput))
	if err != nil {
		return nil, "ps", err
	}
	return processes, "ps", nil
}

func (sampler *Sampler) processInventory(ctx context.Context, policy Policy, force bool) ([]Process, time.Time, bool, string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	for {
		sampler.inventoryMu.Lock()
		now := sampler.now().UTC()
		maxAge := time.Duration(policy.ProcessInventoryIntervalSeconds) * time.Second
		age := now.Sub(sampler.inventoryCapturedAt)
		if !force && len(sampler.inventory) > 0 && age >= 0 && age < maxAge {
			processes, capturedAt, source := sampler.inventory, sampler.inventoryCapturedAt, sampler.inventorySource
			refreshErr := sampler.inventoryRefreshError
			sampler.inventoryMu.Unlock()
			if refreshErr != "" {
				return processes, capturedAt, false, source, fmt.Errorf("resident inventory refresh: %s", refreshErr)
			}
			return processes, capturedAt, false, source, nil
		}

		previous := sampler.inventory
		previousAt := sampler.inventoryCapturedAt
		source := sampler.inventorySource
		// A healthy resident heartbeat must not spend its entire sample window
		// in libproc/sysctl work. Only a scheduled, non-pressure refresh takes
		// this path. The initial inventory and all forced refreshes remain
		// synchronous because they establish or protect safety evidence.
		if sampler.role == "resident" && !force && len(previous) > 0 && age >= maxAge {
			if sampler.inventoryRefreshError != "" {
				refreshErr := sampler.inventoryRefreshError
				sampler.inventoryRefreshError = ""
				sampler.inventoryMu.Unlock()
				return previous, previousAt, false, source, fmt.Errorf("resident inventory refresh: %s", refreshErr)
			}
			if !sampler.inventoryRefreshInFlight {
				done := make(chan struct{})
				sampler.inventoryRefreshInFlight = true
				sampler.inventoryRefreshDone = done
				sampler.inventoryRefreshError = ""
				sampler.inventoryMu.Unlock()
				sampler.scheduleResidentInventoryRefresh(previous, previousAt, done)
				return previous, previousAt, false, source, nil
			}
			sampler.inventoryMu.Unlock()
			// Another normal sample already scheduled the refresh. Reuse the
			// cached projection rather than starting duplicate native scans.
			return previous, previousAt, false, source, nil
		}

		// Pressure-forced work cannot consume a stale inventory while a normal
		// refresh is in flight. Wait for that refresh to settle, then re-check
		// its captured time before deciding whether a synchronous read is needed.
		if force && sampler.inventoryRefreshInFlight {
			done := sampler.inventoryRefreshDone
			source := sampler.inventorySource
			sampler.inventoryMu.Unlock()
			select {
			case <-done:
				continue
			case <-ctx.Done():
				return previous, previousAt, false, source, ctx.Err()
			}
		}
		sampler.inventoryMu.Unlock()

		processes, source, err := sampler.readProcesses(ctx)
		if err != nil {
			sampler.inventoryMu.Lock()
			cached, capturedAt, cachedSource := sampler.inventory, sampler.inventoryCapturedAt, sampler.inventorySource
			sampler.inventoryMu.Unlock()
			return cached, capturedAt, false, cachedSource, err
		}
		capturedAt := sampler.now().UTC()
		if hasCumulativeCPUEvidence(previous) {
			annotateProcessCPUPercent(processes, previous, capturedAt.Sub(previousAt))
		} else if hasCumulativeCPUEvidence(processes) {
			// Native rusage exposes cumulative CPU time. One bounded first-inventory
			// warm-up establishes truthful activity for operator snapshots and the
			// resident's first sample; later refreshes use the cached prior totals.
			timer := time.NewTimer(processCPUWarmupInterval)
			select {
			case <-ctx.Done():
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
			case <-timer.C:
				second := refreshNativeProcessCPUTotals(ctx, processes)
				secondAt := sampler.now().UTC()
				annotateProcessCPUPercent(second, processes, secondAt.Sub(capturedAt))
				processes = second
			}
		}
		sampler.inventoryMu.Lock()
		sampler.inventory = processes
		sampler.inventoryCapturedAt = capturedAt
		sampler.inventorySource = source
		sampler.inventoryRefreshError = ""
		sampler.inventoryMu.Unlock()
		return processes, capturedAt, true, source, nil
	}
}

// scheduleResidentInventoryRefresh keeps the normal heartbeat responsive.
// The small delay lets Sample finish its own CPU-cost measurement before the
// expensive refresh starts; cumulative process CPU remains accounted for in
// the next monitor interval and the refresh has its own bounded context.
func (sampler *Sampler) scheduleResidentInventoryRefresh(previous []Process, previousAt time.Time, done chan struct{}) {
	time.AfterFunc(residentInventoryRefreshDelay, func() {
		ctx, cancel := context.WithTimeout(context.Background(), residentInventoryRefreshTimeout)
		defer cancel()
		processes, source, err := sampler.readProcesses(ctx)
		capturedAt := sampler.now().UTC()
		if err == nil {
			if hasCumulativeCPUEvidence(previous) {
				annotateProcessCPUPercent(processes, previous, capturedAt.Sub(previousAt))
			} else if hasCumulativeCPUEvidence(processes) {
				// A resident refresh normally has a prior inventory. Keep the same
				// first-inventory truthfulness if a test or recovery path races it.
				second := refreshNativeProcessCPUTotals(ctx, processes)
				secondAt := sampler.now().UTC()
				annotateProcessCPUPercent(second, processes, secondAt.Sub(capturedAt))
				processes = second
			}
		}
		sampler.inventoryMu.Lock()
		if err != nil {
			sampler.inventoryRefreshError = err.Error()
		} else {
			sampler.inventory = processes
			sampler.inventoryCapturedAt = capturedAt
			sampler.inventorySource = source
			sampler.inventoryRefreshError = ""
		}
		sampler.inventoryRefreshInFlight = false
		close(done)
		sampler.inventoryRefreshDone = nil
		sampler.inventoryMu.Unlock()
	})
}

const coordinatedWorkStatusTimeout = 500 * time.Millisecond

func (sampler *Sampler) sampleCoordinatedWork(ctx context.Context, processes []Process, capturedAt time.Time, inventoryFresh bool, logicalCPUCount int) CoordinatedWorkSnapshot {
	now := time.Now
	if sampler.now != nil {
		now = sampler.now
	}
	ageSeconds := max(0, now().UTC().Sub(capturedAt).Seconds())
	if sampler.workStatusSource == nil {
		return CoordinatedWorkSnapshot{CapturedAt: capturedAt, Fresh: inventoryFresh, InventoryAgeSeconds: ageSeconds, ByClass: []CoordinatedWorkClassUsage{}}
	}
	statusCtx, cancel := context.WithTimeout(ctx, coordinatedWorkStatusTimeout)
	defer cancel()
	status, err := sampler.workStatusSource(statusCtx)
	if err != nil {
		message := err.Error()
		if len(message) > 512 {
			message = message[:512]
		}
		return CoordinatedWorkSnapshot{CapturedAt: capturedAt, Fresh: inventoryFresh, InventoryAgeSeconds: ageSeconds, ByClass: []CoordinatedWorkClassUsage{}, Error: message}
	}
	result := attributeCoordinatedWork(processes, status.Leases, capturedAt, logicalCPUCount)
	result.Fresh = inventoryFresh
	result.InventoryAgeSeconds = ageSeconds
	return result
}

func cloneCoordinatedWorkSnapshot(snapshot CoordinatedWorkSnapshot) CoordinatedWorkSnapshot {
	snapshot.ByClass = append([]CoordinatedWorkClassUsage(nil), snapshot.ByClass...)
	if snapshot.ByClass == nil {
		snapshot.ByClass = []CoordinatedWorkClassUsage{}
	}
	return snapshot
}

func attributeCoordinatedWork(processes []Process, leases []WorkLeaseStatus, capturedAt time.Time, logicalCPUCount int) CoordinatedWorkSnapshot {
	result := CoordinatedWorkSnapshot{
		Available: true, Fresh: true, CapturedAt: capturedAt, LeaseCount: len(leases),
		CPUAvailable: true, ByClass: []CoordinatedWorkClassUsage{},
	}
	byPID := make(map[int]Process, len(processes))
	for _, process := range processes {
		if process.PID > 0 {
			byPID[process.PID] = process
		}
	}
	rootClass := make(map[int]WorkClass, len(leases))
	usageByClass := make(map[WorkClass]*CoordinatedWorkClassUsage, len(leases))
	for _, lease := range leases {
		rootClass[lease.PID] = lease.Class
		usage := usageByClass[lease.Class]
		if usage == nil {
			usage = &CoordinatedWorkClassUsage{Class: lease.Class, CPUAvailable: true}
			usageByClass[lease.Class] = usage
		}
		usage.LeaseCount++
		if _, found := byPID[lease.PID]; found {
			result.AttributedLeaseCount++
		} else {
			result.UnattributedLeaseCount++
			result.CPUAvailable = false
			usage.CPUAvailable = false
		}
	}
	rawCPUPercent := 0.0
	rawClassCPU := make(map[WorkClass]float64, len(usageByClass))
	for _, process := range processes {
		class, attributed := closestWorkRootClass(process.PID, byPID, rootClass)
		if !attributed {
			continue
		}
		usage := usageByClass[class]
		if usage == nil {
			usage = &CoordinatedWorkClassUsage{Class: class, CPUAvailable: true}
			usageByClass[class] = usage
		}
		result.ProcessCount++
		usage.ProcessCount++
		rssMB := float64(process.RSSKB) / 1024
		result.RSSSumMB += rssMB
		usage.RSSSumMB += rssMB
		if validCPUEvidence(process.CPUAvailable, process.CPUPercent) {
			rawCPUPercent += process.CPUPercent
			rawClassCPU[class] += process.CPUPercent
		} else {
			result.CPUAvailable = false
			usage.CPUAvailable = false
		}
	}
	result.CPUPercent = normalizedCPUPercent(rawCPUPercent, logicalCPUCount)
	classes := AllWorkClasses()
	for _, class := range classes {
		usage := usageByClass[class]
		if usage == nil || usage.LeaseCount == 0 {
			continue
		}
		usage.CPUPercent = normalizedCPUPercent(rawClassCPU[class], logicalCPUCount)
		result.ByClass = append(result.ByClass, *usage)
	}
	return result
}

func closestWorkRootClass(pid int, byPID map[int]Process, rootClass map[int]WorkClass) (WorkClass, bool) {
	for visited := 0; pid > 0 && visited <= len(byPID); visited++ {
		if class, found := rootClass[pid]; found {
			return class, true
		}
		process, found := byPID[pid]
		if !found || process.PPID <= 0 || process.PPID == pid {
			break
		}
		pid = process.PPID
	}
	return "", false
}

func hasCumulativeCPUEvidence(processes []Process) bool {
	for _, process := range processes {
		if process.CPUTotalValid {
			return true
		}
	}
	return false
}

func annotateProcessCPUPercent(current, previous []Process, elapsed time.Duration) {
	if elapsed <= 0 {
		return
	}
	byPID := make(map[int]Process, len(previous))
	for _, process := range previous {
		if process.PID > 0 && process.CPUTotalValid {
			byPID[process.PID] = process
		}
	}
	for index := range current {
		process := &current[index]
		before, ok := byPID[process.PID]
		if !ok || !process.CPUTotalValid || process.StartedAtNS == 0 || process.StartedAtNS != before.StartedAtNS || process.CPUTotalNS < before.CPUTotalNS {
			process.CPUAvailable = false
			continue
		}
		process.CPUPercent = float64(process.CPUTotalNS-before.CPUTotalNS) * 100 / float64(elapsed)
		process.CPUAvailable = true
	}
}

func (sampler *Sampler) inventoryNeedsPressureRefresh(policy Policy) bool {
	sampler.inventoryMu.Lock()
	defer sampler.inventoryMu.Unlock()
	return sampler.inventoryAgentRSSMB >= policy.Thresholds.AgentTotalWarningMB ||
		sampler.inventoryTopRSSMB >= policy.Thresholds.TreeWarningMB
}

func (sampler *Sampler) recordInventoryPressure(trees []AgentTree) {
	agentRSSMB := 0.0
	topRSSMB := 0.0
	for _, tree := range trees {
		agentRSSMB += tree.RSSSumMB
		topRSSMB = max(topRSSMB, tree.RSSSumMB)
	}
	sampler.inventoryMu.Lock()
	sampler.inventoryAgentRSSMB = agentRSSMB
	sampler.inventoryTopRSSMB = topRSSMB
	sampler.inventoryMu.Unlock()
}

// SampleHost combines current cheap memory/swap/Mach-CPU pressure with recent
// resident tree telemetry. It never enumerates all processes; the bounded
// first-use CPU window prevents a stale resident reading from admitting work
// during a new CPU surge.
func (sampler *Sampler) SampleHost(ctx context.Context, policy Policy, latest *Snapshot) (Snapshot, error) {
	started := sampler.now()
	cpuStarted := time.Duration(0)
	if sampler.cpuNow != nil {
		cpuStarted = sampler.cpuNow()
	}
	// Physical RAM is immutable for the life of the host. Canonical admission
	// constructs a short-lived sampler, so seed its cache from resident evidence
	// and avoid a redundant hw.memsize subprocess on every agent/build launch.
	if latest != nil && latest.PhysicalMemoryMB > 0 {
		sampler.physicalMu.Lock()
		if sampler.physicalMB == 0 {
			sampler.physicalMB = latest.PhysicalMemoryMB
		}
		sampler.physicalMu.Unlock()
	}
	physicalMB, err := sampler.PhysicalMemoryMB(ctx)
	if err != nil {
		return Snapshot{}, err
	}
	pressureOutput, err := sampler.runner.Run(ctx, "/usr/bin/memory_pressure", "-Q")
	if err != nil {
		return Snapshot{}, err
	}
	freePercent, err := parseFreePercent(string(pressureOutput))
	if err != nil {
		return Snapshot{}, err
	}
	swapUsedMB, err := sampler.swapUsedMB(ctx)
	if err != nil {
		return Snapshot{}, err
	}
	powerThermal := unknownPowerThermalStatus()
	if latest != nil {
		// Launch admission already has a fresh resident sample. Reuse its typed
		// power/thermal projection instead of spawning pmset in the hot path.
		powerThermal = PowerThermalStatus{
			ThermalState: latest.ThermalState, ThermalAvailable: latest.ThermalAvailable,
			LowPowerMode: latest.LowPowerMode, LowPowerModeAvailable: latest.LowPowerModeAvailable,
			Source: latest.PowerThermalSource, Error: latest.PowerThermalError,
		}
	} else {
		powerThermal = sampler.probePowerThermal(ctx)
	}
	hostCPUPercent, hostCPUWindow, hostCPUErr := sampler.sampleHostCPU(ctx, true)
	previousStorageLevel := LevelNormal
	if latest != nil {
		previousStorageLevel = latest.Storage.Level
	}
	snapshot := Snapshot{
		SchemaVersion: SchemaVersion, Timestamp: started.UTC(), FreePercent: freePercent,
		PhysicalMemoryMB: physicalMB, SwapUsedMB: swapUsedMB, HostCPUPercent: hostCPUPercent,
		HostCPUAvailable: hostCPUErr == nil, HostCPUSource: "mach-host-statistics",
		HostCPUSampleWindowMS: float64(hostCPUWindow.Microseconds()) / 1000,
		HostCPULivePercent:    hostCPUPercent, HostCPULiveWindowMS: float64(hostCPUWindow.Microseconds()) / 1000,
		GuardPID:  sampler.pid,
		GuardRole: "operator", GuardBudgetApplicable: false, MemoryMomentum: MemoryMomentumUnknown,
		TopHostConsumers: []HostConsumer{}, TopAgentTrees: []AgentTree{},
		Storage:      sampler.sampleStorage(policy, previousStorageLevel),
		ThermalState: powerThermal.ThermalState, ThermalAvailable: powerThermal.ThermalAvailable,
		LowPowerMode: powerThermal.LowPowerMode, LowPowerModeAvailable: powerThermal.LowPowerModeAvailable,
		PowerThermalSource: powerThermal.Source, PowerThermalError: powerThermal.Error,
	}
	if latest != nil && latest.HostCPUAvailable {
		snapshot.HostCPURollingAvailable = true
		snapshot.HostCPURollingPercent = latest.HostCPUPercent
		snapshot.HostCPURollingWindowMS = latest.HostCPUSampleWindowMS
		if latest.HostCPURollingAvailable {
			snapshot.HostCPURollingPercent = latest.HostCPURollingPercent
			snapshot.HostCPURollingWindowMS = latest.HostCPURollingWindowMS
		}
	}
	if hostCPUErr != nil {
		snapshot.HostCPUSource = "unavailable"
		snapshot.HostCPUError = hostCPUErr.Error()
		if len(snapshot.HostCPUError) > 512 {
			snapshot.HostCPUError = snapshot.HostCPUError[:512]
		}
	}
	logicalCPUCount := runtime.NumCPU()
	if sampler.logicalCPUCount != nil {
		logicalCPUCount = sampler.logicalCPUCount()
	}
	snapshot.LogicalCPUCount = max(1, logicalCPUCount)
	if latest != nil && latest.ProcessInventoryAvailable {
		capturedAt := latest.ProcessInventoryCapturedAt
		if capturedAt.IsZero() {
			capturedAt = latest.Timestamp
		}
		age := started.Sub(capturedAt)
		maxAge := time.Duration(policy.SampleIntervalSeconds*2+15) * time.Second
		// A stale tree can falsely block a launch after its processes have exited.
		// Admission always trusts live free-memory/swap/Mach-CPU probes, but only
		// projects resident agent CPU/RSS inventory inside this tighter window.
		if age >= -5*time.Second && age <= maxAge {
			snapshot.LogicalCPUCount = max(1, latest.LogicalCPUCount)
			snapshot.AgentCPUPercent = latest.AgentCPUPercent
			snapshot.AgentCPUAvailable = latest.AgentCPUAvailable
			snapshot.CoordinatedWork = cloneCoordinatedWorkSnapshot(latest.CoordinatedWork)
			snapshot.CoordinatedWork.Fresh = false
			snapshot.CoordinatedWork.InventoryAgeSeconds = max(0, started.Sub(snapshot.CoordinatedWork.CapturedAt).Seconds())
			snapshot.ProcessCount = latest.ProcessCount
			snapshot.ProcessRSSSumMB = latest.ProcessRSSSumMB
			snapshot.AgentTreeCount = latest.AgentTreeCount
			snapshot.AgentRSSSumMB = latest.AgentRSSSumMB
			snapshot.MemoryMomentum = latest.MemoryMomentum
			snapshot.FreePercentSlopePerMinute = latest.FreePercentSlopePerMinute
			snapshot.MinutesToMemoryRed = latest.MinutesToMemoryRed
			snapshot.MemoryMomentumSampleCount = latest.MemoryMomentumSampleCount
			snapshot.TopHostConsumers = latest.TopHostConsumers
			snapshot.TopAgentTrees = latest.TopAgentTrees
			snapshot.ProcessInventoryAvailable = true
			snapshot.ProcessInventoryCapturedAt = capturedAt
			snapshot.ProcessInventoryAgeSeconds = max(0, age.Seconds())
			snapshot.ProcessInventorySource = latest.ProcessInventorySource
		}
	}
	snapshot.SampleDurationMS = float64(sampler.now().Sub(started).Microseconds()) / 1000
	if sampler.cpuNow != nil {
		cpuEnded := sampler.cpuNow()
		snapshot.SampleCPUTimeMS = float64((cpuEnded - cpuStarted).Microseconds()) / 1000
		snapshot.processCPUTotalMS = float64(cpuEnded.Microseconds()) / 1000
	}
	return Evaluate(snapshot, policy), nil
}

func normalizedCPUPercent(rawPercent float64, logicalCPUCount int) float64 {
	if logicalCPUCount < 1 || rawPercent <= 0 {
		return 0
	}
	return min(100, rawPercent/float64(logicalCPUCount))
}

func processCPUTime() time.Duration {
	var self, children syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &self); err != nil {
		return 0
	}
	if err := syscall.Getrusage(syscall.RUSAGE_CHILDREN, &children); err != nil {
		return 0
	}
	nanos := syscall.TimevalToNsec(self.Utime) + syscall.TimevalToNsec(self.Stime) +
		syscall.TimevalToNsec(children.Utime) + syscall.TimevalToNsec(children.Stime)
	return time.Duration(nanos)
}

func parseProcesses(body string) ([]Process, error) {
	lines := strings.Split(body, "\n")
	rows := make([]Process, 0, len(lines))
	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) < 6 {
			continue
		}
		pid, errPID := strconv.Atoi(fields[0])
		ppid, errPPID := strconv.Atoi(fields[1])
		rss, errRSS := strconv.ParseInt(fields[2], 10, 64)
		cpu, errCPU := strconv.ParseFloat(strings.ReplaceAll(fields[3], ",", "."), 64)
		if errPID != nil || errPPID != nil || errRSS != nil || errCPU != nil || pid <= 0 {
			continue
		}
		rows = append(rows, Process{
			PID: pid, PPID: ppid, RSSKB: rss, CPUPercent: cpu,
			CPUAvailable: true,
			Elapsed:      fields[4], Command: strings.Join(fields[5:], " "),
			Executable: privacySafeExecutable(fields[5]),
		})
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("process table contained no parseable rows")
	}
	return rows, nil
}

var freePercentPattern = regexp.MustCompile(`(?i)system-wide memory free percentage:\s*([0-9]+)%`)

func parseFreePercent(body string) (int, error) {
	match := freePercentPattern.FindStringSubmatch(body)
	if len(match) != 2 {
		return 0, fmt.Errorf("memory_pressure output missing free percentage")
	}
	value, err := strconv.Atoi(match[1])
	if err != nil || value < 0 || value > 100 {
		return 0, fmt.Errorf("invalid free percentage %q", match[1])
	}
	return value, nil
}

var swapUsedPattern = regexp.MustCompile(`(?i)used\s*=\s*([0-9.]+)([KMGT])`)

func parseSwapUsedMB(body string) (float64, error) {
	match := swapUsedPattern.FindStringSubmatch(body)
	if len(match) != 3 {
		return 0, fmt.Errorf("sysctl vm.swapusage output missing used value")
	}
	value, err := strconv.ParseFloat(match[1], 64)
	if err != nil {
		return 0, fmt.Errorf("invalid swap used value %q", match[1])
	}
	switch strings.ToUpper(match[2]) {
	case "K":
		value /= 1024
	case "G":
		value *= 1024
	case "T":
		value *= 1024 * 1024
	}
	return value, nil
}

var sessionIDPattern = regexp.MustCompile(`(?i)\b[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\b`)

func agentForCommand(command string) (agent, executable string, ok bool) {
	return ActiveAgentIdentityCatalog().MatchCommand(command)
}

// applySamplerSessionHints fills missing Agent/SessionID from hook session
// state without re-scanning the process table.
func applySamplerSessionHints(processes []Process, sessionStateDir string) []Process {
	return applySessionHints(processes, sessionStateDir, false)
}

func applyResidentSessionHints(processes []Process, sessionStateDir string) []Process {
	return applySessionHints(processes, sessionStateDir, true)
}

func applySessionHints(processes []Process, sessionStateDir string, nonblocking bool) []Process {
	if len(processes) == 0 || strings.TrimSpace(sessionStateDir) == "" {
		return processes
	}
	catalog := ActiveAgentIdentityCatalog()
	var hints []SessionOwnershipHint
	if nonblocking {
		hints = peekSessionOwnershipHints(sessionStateDir, catalog)
	} else {
		hints = LoadSessionOwnershipHints(sessionStateDir, catalog)
	}
	return ApplySessionOwnershipHints(processes, hints)
}

func buildAgentTrees(processes []Process) []AgentTree {
	byPID := make(map[int]Process, len(processes))
	children := make(map[int][]int, len(processes))
	candidates := make(map[int]string)
	executables := make(map[int]string)
	for _, process := range processes {
		byPID[process.PID] = process
		children[process.PPID] = append(children[process.PPID], process.PID)
		agent, executable, ok := process.Agent, process.Executable, process.Agent != ""
		if !ok {
			agent, executable, ok = agentForCommand(process.Command)
		}
		if ok {
			candidates[process.PID] = agent
			executables[process.PID] = executable
		}
	}
	rootPIDs := make([]int, 0, len(candidates))
	for pid := range candidates {
		ancestor := byPID[pid].PPID
		hasAgentAncestor := false
		seen := map[int]bool{}
		for ancestor > 0 && !seen[ancestor] {
			seen[ancestor] = true
			if _, ok := candidates[ancestor]; ok {
				hasAgentAncestor = true
				break
			}
			parent, ok := byPID[ancestor]
			if !ok {
				break
			}
			ancestor = parent.PPID
		}
		if !hasAgentAncestor {
			rootPIDs = append(rootPIDs, pid)
		}
	}
	sort.Ints(rootPIDs)
	trees := make([]AgentTree, 0, len(rootPIDs))
	for _, rootPID := range rootPIDs {
		root := byPID[rootPID]
		elapsedSeconds := root.ElapsedSeconds
		if elapsedSeconds == 0 {
			elapsedSeconds = parseElapsedSeconds(root.Elapsed)
		}
		sessionID := root.SessionID
		if sessionID == "" {
			sessionID = sessionIDPattern.FindString(root.Command)
		}
		tree := AgentTree{
			Agent: candidates[rootPID], RootPID: rootPID, Executable: executables[rootPID],
			SessionID: sessionID, ElapsedSeconds: elapsedSeconds, CPUAvailable: true,
		}
		visited := map[int]bool{}
		var walk func(int)
		walk = func(pid int) {
			if visited[pid] {
				return
			}
			visited[pid] = true
			process, ok := byPID[pid]
			if !ok {
				return
			}
			tree.PIDs = append(tree.PIDs, pid)
			tree.ProcessCount++
			tree.RSSSumMB += float64(process.RSSKB) / 1024
			tree.CPUPercentSum += process.CPUPercent
			if !process.CPUAvailable {
				tree.CPUAvailable = false
			}
			for _, child := range children[pid] {
				walk(child)
			}
		}
		walk(rootPID)
		tree.RSSSumMB = math.Round(tree.RSSSumMB*10) / 10
		tree.CPUPercentSum = math.Round(tree.CPUPercentSum*100) / 100
		trees = append(trees, tree)
	}
	sort.Slice(trees, func(i, j int) bool {
		if trees[i].RSSSumMB == trees[j].RSSSumMB {
			return trees[i].RootPID < trees[j].RootPID
		}
		return trees[i].RSSSumMB > trees[j].RSSSumMB
	})
	return trees
}

func parseElapsedSeconds(value string) int64 {
	if value == "" {
		return 0
	}
	var days int64
	clock := value
	if parts := strings.SplitN(value, "-", 2); len(parts) == 2 {
		parsedDays, err := strconv.ParseInt(parts[0], 10, 64)
		if err != nil {
			return 0
		}
		days = parsedDays
		clock = parts[1]
	}
	parts := strings.Split(clock, ":")
	var hours, minutes, seconds int64
	switch len(parts) {
	case 3:
		var err error
		hours, err = strconv.ParseInt(parts[0], 10, 64)
		if err != nil {
			return 0
		}
		minutes, err = strconv.ParseInt(parts[1], 10, 64)
		if err != nil {
			return 0
		}
		seconds, err = strconv.ParseInt(parts[2], 10, 64)
		if err != nil {
			return 0
		}
	case 2:
		var err error
		minutes, err = strconv.ParseInt(parts[0], 10, 64)
		if err != nil {
			return 0
		}
		seconds, err = strconv.ParseInt(parts[1], 10, 64)
		if err != nil {
			return 0
		}
	default:
		return 0
	}
	return days*86400 + hours*3600 + minutes*60 + seconds
}
