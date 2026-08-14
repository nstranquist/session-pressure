package sessionpressure

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"syscall"
	"time"
)

type processSignaler interface {
	Terminate(tree AgentTree, policy Policy) (terminationResult, error)
}

type osProcessSignaler struct {
	sampler     *Sampler
	policyPath  string
	signalPID   func(int) error
	confirmExit func([]int, time.Duration) bool
}

type terminationResult struct {
	Snapshot        Snapshot
	Tree            AgentTree
	SignalAttempted bool
	ExitChecked     bool
	TreeExited      bool
}

type reliefRevalidationError struct{ reason string }

func (failure reliefRevalidationError) Error() string {
	return "relief rejected by final revalidation: " + failure.reason
}

func (signaler osProcessSignaler) Terminate(tree AgentTree, policy Policy) (terminationResult, error) {
	sampler := signaler.sampler
	if sampler == nil {
		sampler = NewResidentSampler()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	snapshot, err := sampler.Sample(ctx, policy)
	result := terminationResult{Snapshot: snapshot}
	if err != nil {
		return result, fmt.Errorf("revalidate host and agent tree: %w", err)
	}
	var current AgentTree
	found := false
	for _, candidate := range snapshot.TopAgentTrees {
		if candidate.RootPID == tree.RootPID {
			current = candidate
			found = true
			break
		}
	}
	result.Tree = current
	if !found || current.Agent != tree.Agent || current.Executable != tree.Executable {
		return result, reliefRevalidationError{reason: fmt.Sprintf("agent tree root %d changed", tree.RootPID)}
	}
	if current.ElapsedSeconds+5 < tree.ElapsedSeconds {
		return result, reliefRevalidationError{reason: fmt.Sprintf("agent tree root %d appears to have been reused", tree.RootPID)}
	}
	if current.SessionID != tree.SessionID {
		return result, reliefRevalidationError{reason: fmt.Sprintf("agent tree root %d session identity changed", tree.RootPID)}
	}
	if err := validateSemanticRevalidation(tree, current); err != nil {
		return result, reliefRevalidationError{reason: err.Error()}
	}
	previousPIDs := make(map[int]struct{}, len(tree.PIDs))
	// The root identity was already established even if an older or synthetic
	// candidate omitted the convenience PIDs projection.
	previousPIDs[tree.RootPID] = struct{}{}
	for _, pid := range tree.PIDs {
		previousPIDs[pid] = struct{}{}
	}
	for _, pid := range current.PIDs {
		if _, existed := previousPIDs[pid]; !existed {
			return result, reliefRevalidationError{reason: fmt.Sprintf("agent tree root %d added descendant %d after quiescence", tree.RootPID, pid)}
		}
	}
	if snapshot.Level != LevelCritical {
		return result, reliefRevalidationError{reason: fmt.Sprintf("host pressure recovered to %s", snapshot.Level)}
	}
	if current.ElapsedSeconds < int64(policy.CandidateMinAgeSeconds) {
		return result, reliefRevalidationError{reason: fmt.Sprintf("agent tree root %d is now younger than the %d second minimum", tree.RootPID, policy.CandidateMinAgeSeconds)}
	}
	if !validCPUEvidence(current.CPUAvailable, current.CPUPercentSum) {
		return result, reliefRevalidationError{reason: fmt.Sprintf("agent tree root %d has no valid current CPU activity evidence", tree.RootPID)}
	}
	if current.CPUPercentSum > policy.CandidateMaxCPUPercent {
		return result, reliefRevalidationError{reason: fmt.Sprintf("agent tree root %d became active at %.2f%% CPU above the %.2f%% ceiling", tree.RootPID, current.CPUPercentSum, policy.CandidateMaxCPUPercent)}
	}
	// Policy can change while the complete final sample is running. Check disk
	// again at the last possible boundary so an operator downgrade that lands
	// during revalidation wins before the first signal is sent.
	if signaler.policyPath == "" {
		return result, reliefRevalidationError{reason: "persisted action policy is unavailable"}
	}
	persistedPolicy, persisted, policyErr := LoadPolicy(signaler.policyPath, snapshot.PhysicalMemoryMB)
	if policyErr != nil {
		return result, reliefRevalidationError{reason: "persisted action policy could not be read"}
	}
	if !persisted || persistedPolicy != policy {
		return result, reliefRevalidationError{reason: "persisted action policy changed during final revalidation"}
	}
	if len(current.PIDs) == 0 {
		return result, reliefRevalidationError{reason: fmt.Sprintf("agent tree root %d has no signalable PID projection", tree.RootPID)}
	}
	result.SignalAttempted = true
	if err := signalTreePIDs(current.PIDs, signaler.signalPID); err != nil {
		return result, err
	}
	confirmExit := signaler.confirmExit
	if confirmExit == nil {
		confirmExit = confirmTreeExit
	}
	result.ExitChecked = true
	result.TreeExited = confirmExit(current.PIDs, 2*time.Second)
	return result, nil
}

func confirmTreeExit(pids []int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		alive := false
		for _, pid := range pids {
			if processAlive(pid) {
				alive = true
				break
			}
		}
		if !alive {
			return true
		}
		if !time.Now().Before(deadline) {
			return false
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func signalTreePIDs(pids []int, signalPID func(int) error) error {
	if signalPID == nil {
		signalPID = func(pid int) error {
			process, findErr := os.FindProcess(pid)
			if findErr != nil {
				return findErr
			}
			return process.Signal(syscall.SIGTERM)
		}
	}
	var failures []error
	// A fresh tree records root-first DFS, so reverse order asks remaining
	// children and MCP helpers to exit before their owning CLI wrapper.
	for i := len(pids) - 1; i >= 0; i-- {
		if err := signalPID(pids[i]); err != nil && !errors.Is(err, os.ErrProcessDone) {
			failures = append(failures, fmt.Errorf("pid %d: %w", pids[i], err))
		}
	}
	return errors.Join(failures...)
}

type Monitor struct {
	Sampler           *Sampler
	Store             *TelemetryStore
	Policy            Policy
	Signaler          processSignaler
	Cleaner           ResourceCleaner
	ResidentStarts24h int
	Now               func() time.Time
	DiskObserver      *DiskWriteObserver
	DiskNotifier      func(DiskWriteAlert) error
}

type monitorStats struct {
	durations               []float64
	cpuTimes                []float64
	dutyCPUTimes            []float64
	intervals               []float64
	levels                  []Level
	rss                     []float64
	cpu                     []float64
	next                    int
	count                   int
	recurringEventMaxBytes  int64
	transitionEventMaxBytes int64
	actionBytesTotal        int64
	actionCount             int64
	normalSamplesTotal      int
	sampleErrors            int
	resourceCleanupFailures int
}

type resourceCleanupTelemetryState struct {
	lastError            string
	lastEvent            time.Time
	pendingErrorEvent    bool
	pendingRecoveryEvent bool
	controlExecutedAt    time.Time
	controlDurationMS    float64
	controlMaxRSSMB      float64
}

const resourceCleanupTelemetryMinInterval = 5 * time.Minute

const monitorStatsWindow = 64

// Retention is day-granular. Re-globbing shards (and migrating a legacy action
// ledger) on every 5-second critical sample adds work precisely when the host
// has the least headroom, so amortize housekeeping independently of sampling.
const telemetryPruneInterval = 6 * time.Hour

// Full transition rows retain bounded attribution, so persist at most the 24
// rows/day reserved by the telemetry projection. Compact heartbeats continue
// to capture the current level at the configured heartbeat cadence.
const telemetryTransitionMinInterval = time.Hour

func (stats *monitorStats) add(snapshot Snapshot, sampleIntervalSeconds float64) {
	dutyCPUTimeMS := snapshot.intervalCPUTimeMS
	if dutyCPUTimeMS <= 0 {
		dutyCPUTimeMS = snapshot.SampleCPUTimeMS
	}
	if snapshot.Level == LevelNormal {
		stats.normalSamplesTotal++
	}
	if len(stats.durations) < monitorStatsWindow {
		stats.durations = append(stats.durations, snapshot.SampleDurationMS)
		stats.cpuTimes = append(stats.cpuTimes, snapshot.SampleCPUTimeMS)
		stats.dutyCPUTimes = append(stats.dutyCPUTimes, dutyCPUTimeMS)
		stats.intervals = append(stats.intervals, sampleIntervalSeconds)
		stats.levels = append(stats.levels, snapshot.Level)
		stats.rss = append(stats.rss, snapshot.GuardRSSMB)
		stats.cpu = append(stats.cpu, snapshot.GuardCPUPercent)
	} else {
		stats.durations[stats.next] = snapshot.SampleDurationMS
		stats.cpuTimes[stats.next] = snapshot.SampleCPUTimeMS
		stats.dutyCPUTimes[stats.next] = dutyCPUTimeMS
		stats.intervals[stats.next] = sampleIntervalSeconds
		stats.levels[stats.next] = snapshot.Level
		stats.rss[stats.next] = snapshot.GuardRSSMB
		stats.cpu[stats.next] = snapshot.GuardCPUPercent
		stats.next = (stats.next + 1) % monitorStatsWindow
	}
	stats.count++
}

func (stats *monitorStats) recordEventBytes(event string, bytes int64) {
	if bytes <= 0 {
		return
	}
	if event == "state_transition" || event == "sample_recovered" || event == "resource_cleanup_error" || event == "resource_cleanup_recovered" {
		stats.transitionEventMaxBytes = max(stats.transitionEventMaxBytes, bytes)
		return
	}
	stats.recurringEventMaxBytes = max(stats.recurringEventMaxBytes, bytes)
}

func (stats *monitorStats) recordActionBytes(bytes int64) {
	if bytes <= 0 {
		return
	}
	stats.actionBytesTotal += bytes
	stats.actionCount++
}

func (stats *monitorStats) apply(snapshot *Snapshot, policy Policy) {
	if len(stats.durations) == 0 {
		return
	}
	snapshot.MonitorSamples = stats.count
	snapshot.GuardBaselineProven = stats.normalSamplesTotal >= policy.SustainSamples
	snapshot.NormalMonitorSamples = 0
	ordered := append([]float64(nil), stats.durations...)
	sort.Float64s(ordered)
	orderedCPU := append([]float64(nil), stats.cpuTimes...)
	sort.Float64s(orderedCPU)
	var durationTotal, sampleCPUTimeTotal, dutyCPUTimeTotal, intervalTotal, idleCPUTimeTotal, idleIntervalTotal, rssMax, cpuTotal float64
	for index := range stats.durations {
		durationTotal += stats.durations[index]
		sampleCPUTimeTotal += stats.cpuTimes[index]
		dutyCPUTimeTotal += stats.dutyCPUTimes[index]
		if stats.intervals[index] > 0 {
			intervalTotal += stats.intervals[index]
			if stats.levels[index] == LevelNormal {
				idleCPUTimeTotal += stats.dutyCPUTimes[index]
				idleIntervalTotal += stats.intervals[index]
				snapshot.NormalMonitorSamples++
			}
		}
		cpuTotal += stats.cpu[index]
		if stats.rss[index] > rssMax {
			rssMax = stats.rss[index]
		}
	}
	snapshot.SampleDurationAvgMS = durationTotal / float64(len(stats.durations))
	p95Index := int(float64(len(ordered))*0.95+0.999999) - 1
	if p95Index < 0 {
		p95Index = 0
	}
	snapshot.SampleDurationP95MS = ordered[p95Index]
	snapshot.SampleDurationMaxMS = ordered[len(ordered)-1]
	snapshot.SampleCPUTimeAvgMS = sampleCPUTimeTotal / float64(len(stats.cpuTimes))
	snapshot.SampleCPUTimeP95MS = orderedCPU[p95Index]
	snapshot.SampleCPUTimeMaxMS = orderedCPU[len(orderedCPU)-1]
	if intervalTotal > 0 {
		snapshot.GuardCPUDutyPercent = dutyCPUTimeTotal / (intervalTotal * 10)
	}
	if idleIntervalTotal > 0 {
		snapshot.GuardIdleCPUDutyPercent = idleCPUTimeTotal / (idleIntervalTotal * 10)
	}
	snapshot.GuardRSSMaxMB = rssMax
	snapshot.GuardCPUAvgPercent = cpuTotal / float64(len(stats.cpu))
	projectedActionBytes := int64(0)
	if stats.actionCount > 0 {
		projectedActionBytes = stats.actionBytesTotal / stats.actionCount
	}
	projection := ProjectTelemetryBytesPerDay(policy, stats.recurringEventMaxBytes, stats.transitionEventMaxBytes, projectedActionBytes)
	snapshot.TelemetryProjectedBytesDay = projection.TotalBytes
	snapshot.GuardSampleErrors = stats.sampleErrors
	snapshot.ResourceCleanupFailures = stats.resourceCleanupFailures
}

func (monitor *Monitor) annotateOperationalTelemetry(snapshot *Snapshot, cleanup resourceCleanupTelemetryState, stats monitorStats) {
	if snapshot == nil {
		return
	}
	snapshot.GuardSampleErrors = stats.sampleErrors
	snapshot.ResourceCleanupFailures = stats.resourceCleanupFailures
	snapshot.ResidentStarts24h = monitor.ResidentStarts24h
	snapshot.ResourceCleanupExecutedAt = cleanup.controlExecutedAt
	snapshot.ResourceCleanupDurationMS = cleanup.controlDurationMS
	snapshot.ResourceCleanupMaxRSSMB = cleanup.controlMaxRSSMB
	switch {
	case monitor.Cleaner == nil:
		snapshot.ResourceCleanupStatus = "disabled"
	case cleanup.lastError != "":
		snapshot.ResourceCleanupStatus = "failing"
	default:
		snapshot.ResourceCleanupStatus = "healthy"
	}
}

func NewMonitor(sampler *Sampler, store *TelemetryStore, policy Policy) *Monitor {
	policyPath := ""
	if store != nil {
		policyPath = PolicyPath(store.Dir)
	}
	return &Monitor{Sampler: sampler, Store: store, Policy: policy, Signaler: osProcessSignaler{sampler: sampler, policyPath: policyPath}, Now: time.Now}
}

func (monitor *Monitor) RunOnce(ctx context.Context, event string) (Snapshot, error) {
	snapshot, err := monitor.Sampler.Sample(ctx, monitor.Policy)
	if err != nil {
		if appendErr := monitor.Store.AppendEvent(TelemetryEvent{Event: "sample_error", Error: err.Error()}); appendErr != nil {
			err = errors.Join(err, fmt.Errorf("persist sample error telemetry: %w", appendErr))
		}
		return Snapshot{}, err
	}
	if latest, ok := monitor.Store.ReadLatest(); ok {
		snapshot.Storage = EvaluateStorage(snapshot.Storage, monitor.Policy.Storage, latest.Storage.Level)
	}
	monitor.attachDiskWrite(&snapshot)
	snapshot.TelemetryBytesToday = monitor.Store.BytesForDay(snapshot.Timestamp)
	snapshot = Evaluate(snapshot, monitor.Policy)
	if event != "" {
		if err := monitor.Store.AppendEvent(TelemetryEvent{Timestamp: snapshot.Timestamp, Event: event, Snapshot: &snapshot}); err != nil {
			return Snapshot{}, err
		}
		snapshot.TelemetryBytesToday = monitor.Store.BytesForDay(snapshot.Timestamp)
		snapshot = Evaluate(snapshot, monitor.Policy)
	}
	// Manual operator probes are durable history, not resident health. Only a
	// resident sampler may replace latest.json and its rolling budget evidence.
	if snapshot.GuardRole == "resident" {
		if err := monitor.Store.WriteLatest(snapshot); err != nil {
			return Snapshot{}, err
		}
	}
	return snapshot, nil
}

func (monitor *Monitor) Run(ctx context.Context) error {
	if err := monitor.Policy.Validate(); err != nil {
		return err
	}
	if !monitor.Policy.Enabled {
		return fmt.Errorf("session pressure policy is disabled")
	}
	if monitor.DiskObserver != nil {
		diskContext, cancelDiskObserver := context.WithCancel(ctx)
		diskObserverDone := make(chan struct{})
		go func() {
			defer close(diskObserverDone)
			_ = monitor.DiskObserver.Run(diskContext, func(transition DiskWriteTransition) {
				if monitor.Store != nil {
					_ = monitor.Store.AppendEvent(TelemetryEvent{Timestamp: transition.Timestamp, Event: "disk_write_state_transition", DiskWrite: &transition})
				}
			}, func(alert DiskWriteAlert) {
				if monitor.DiskNotifier != nil {
					_ = monitor.DiskNotifier(alert)
				}
			})
		}()
		defer func() {
			cancelDiskObserver()
			timer := time.NewTimer(2 * time.Second)
			defer timer.Stop()
			select {
			case <-diskObserverDone:
			case <-timer.C:
			}
		}()
	}
	lastLevel := Level("")
	consecutive := 0
	lastMemoryLevel := Level("")
	memoryConsecutive := 0
	lastPersist := time.Time{}
	lastTransitionPersist := time.Time{}
	lastAttempt := time.Time{}
	lastRevalidationAttempt := time.Time{}
	rejectedCandidates := map[string]bool{}
	stats := monitorStats{}
	quiescentStreak := map[int]int{}
	lastSampleStarted := time.Time{}
	lastSampleError := ""
	lastErrorPersist := time.Time{}
	lastProcessCPUTotalMS := 0.0
	lastPrune := time.Time{}
	lastStorageLevel := LevelNormal
	cleanupTelemetry := resourceCleanupTelemetryState{}
	memoryHistory := make([]memoryObservation, 0, memoryMomentumWindow)
	if latest, ok := monitor.Store.ReadLatest(); ok {
		lastStorageLevel = latest.Storage.Level
		if age := monitor.Now().UTC().Sub(latest.Timestamp); age >= -5*time.Second && age <= 10*time.Minute {
			memoryHistory = append(memoryHistory, memoryObservation{Timestamp: latest.Timestamp, FreePercent: float64(latest.FreePercent)})
		}
	}
	historyWindow := max(time.Duration(monitor.Policy.ActionCooldownSeconds)*time.Second, reliefRevalidationRetryInterval)
	actions, err := monitor.Store.ReadActions(1024, monitor.Now().UTC().Add(-historyWindow))
	if err != nil {
		// Cooldown history is part of the destructive-action authority. If it
		// cannot be proved, fail closed before sampling instead of treating an
		// unreadable or partial ledger as permission to signal another tree.
		return fmt.Errorf("load automatic-relief cooldown state: %w", err)
	}
	for _, action := range actions {
		if action.Result == "revalidation_rejected" {
			rejectedCandidates[reliefActionKey(action)] = true
			if action.Timestamp.After(lastRevalidationAttempt) {
				lastRevalidationAttempt = action.Timestamp
			}
		} else if action.Timestamp.After(lastAttempt) {
			lastAttempt = action.Timestamp
		}
	}
	for {
		sampleCtx, sampleCancel := context.WithTimeout(ctx, 15*time.Second)
		snapshot, err := monitor.Sampler.Sample(sampleCtx, monitor.Policy)
		sampleCancel()
		if err != nil {
			stats.sampleErrors++
			now := monitor.Now().UTC()
			message := err.Error()
			if message != lastSampleError || lastErrorPersist.IsZero() || now.Sub(lastErrorPersist) >= time.Duration(monitor.Policy.HeartbeatSeconds)*time.Second {
				beforeBytes := monitor.Store.BytesForDay(now)
				if appendErr := monitor.Store.AppendEvent(TelemetryEvent{Event: "sample_error", Error: message}); appendErr == nil {
					stats.recordEventBytes("sample_error", monitor.Store.BytesForDay(now)-beforeBytes)
				}
				lastErrorPersist = now
			}
			lastSampleError = message
		} else {
			memoryLevel := EvaluateMemoryPressure(snapshot, monitor.Policy).Level
			if memoryLevel == lastMemoryLevel {
				memoryConsecutive++
			} else {
				lastMemoryLevel = memoryLevel
				memoryConsecutive = 1
			}
			snapshot.Storage = EvaluateStorage(snapshot.Storage, monitor.Policy.Storage, lastStorageLevel)
			lastStorageLevel = snapshot.Storage.Level
			monitor.attachDiskWrite(&snapshot)
			recovered := lastSampleError != ""
			lastSampleError = ""
			annotateQuiescence(snapshot.TopAgentTrees, monitor.Policy, quiescentStreak)
			observedInterval := float64(monitor.Policy.IntervalSeconds(snapshot.Level))
			if !lastSampleStarted.IsZero() {
				if elapsed := snapshot.Timestamp.Sub(lastSampleStarted).Seconds(); elapsed > 0 {
					observedInterval = elapsed
				}
			}
			snapshot.ObservedIntervalSeconds = observedInterval
			lastSampleStarted = snapshot.Timestamp
			annotateMemoryMomentum(&snapshot, &memoryHistory, monitor.Policy.Thresholds.FreeRedPercent)
			snapshot.intervalCPUTimeMS = snapshot.SampleCPUTimeMS
			if snapshot.processCPUTotalMS > 0 {
				if lastProcessCPUTotalMS > 0 && snapshot.processCPUTotalMS >= lastProcessCPUTotalMS {
					snapshot.intervalCPUTimeMS = snapshot.processCPUTotalMS - lastProcessCPUTotalMS
				}
				lastProcessCPUTotalMS = snapshot.processCPUTotalMS
			}
			stats.add(snapshot, observedInterval)
			monitor.annotateOperationalTelemetry(&snapshot, cleanupTelemetry, stats)
			if snapshot.Level == lastLevel {
				consecutive++
			} else {
				lastLevel = snapshot.Level
				consecutive = 1
			}
			if snapshot.Level != LevelCritical {
				clear(rejectedCandidates)
				lastRevalidationAttempt = time.Time{}
			}
			snapshot.ConsecutiveSamples = consecutive
			snapshot.MemoryConsecutiveSamples = memoryConsecutive
			snapshot.TelemetryBytesToday = monitor.Store.BytesForDay(snapshot.Timestamp)
			stats.apply(&snapshot, monitor.Policy)
			snapshot = Evaluate(snapshot, monitor.Policy)
			now := monitor.Now()
			event, persist := selectResidentTelemetryEvent(now, lastPersist, lastTransitionPersist, recovered, consecutive, monitor.Policy)
			if persist {
				beforeBytes := monitor.Store.BytesForDay(snapshot.Timestamp)
				if appendErr := monitor.Store.AppendEvent(TelemetryEvent{Timestamp: snapshot.Timestamp, Event: event, Snapshot: &snapshot}); appendErr != nil {
					return appendErr
				}
				stats.recordEventBytes(event, monitor.Store.BytesForDay(snapshot.Timestamp)-beforeBytes)
				lastPersist = now
				if event == "state_transition" || event == "sample_recovered" {
					lastTransitionPersist = now
				}
			}
			resourceReliefAllowed := true
			if action, acted := monitor.maybeRelieveExcluding(snapshot, lastAttempt, lastRevalidationAttempt, rejectedCandidates); acted {
				resourceReliefAllowed = false
				beforeBytes := monitor.Store.BytesForDay(action.Timestamp)
				if err := monitor.Store.AppendAction(action); err != nil {
					return err
				}
				stats.recordActionBytes(monitor.Store.BytesForDay(action.Timestamp) - beforeBytes)
				if action.Result == "revalidation_rejected" {
					rejectedCandidates[reliefActionKey(action)] = true
					lastRevalidationAttempt = action.Timestamp
				} else {
					lastAttempt = action.Timestamp
				}
			}
			snapshot = monitor.runResourceCleanup(ctx, snapshot, resourceReliefAllowed, &cleanupTelemetry, &stats)
			monitor.annotateOperationalTelemetry(&snapshot, cleanupTelemetry, stats)
			snapshot.TelemetryBytesToday = monitor.Store.BytesForDay(snapshot.Timestamp)
			stats.apply(&snapshot, monitor.Policy)
			snapshot = Evaluate(snapshot, monitor.Policy)
			if snapshot.GuardRole == "resident" {
				if err := monitor.Store.WriteLatest(snapshot); err != nil {
					return err
				}
			}
			pruneAt := monitor.Now()
			if lastPrune.IsZero() || pruneAt.Sub(lastPrune) >= telemetryPruneInterval || pruneAt.Before(lastPrune) {
				// Treat an attempted prune as scheduled even if it fails. Retrying an
				// I/O failure every five seconds would itself amplify pressure; the
				// next bounded housekeeping window can try again.
				_ = monitor.Store.Prune(monitor.Policy.RetentionDays)
				lastPrune = pruneAt
			}
		}
		delay := time.Duration(monitor.Policy.IntervalSeconds(lastLevel)) * time.Second
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return nil
		case <-timer.C:
		}
	}
}

func (monitor *Monitor) attachDiskWrite(snapshot *Snapshot) {
	if monitor == nil || snapshot == nil || monitor.DiskObserver == nil {
		return
	}
	monitor.DiskObserver.SetProcessOwnership(monitor.Sampler.currentProcessInventory())
	monitor.DiskObserver.SetContext(diskWriteContext(snapshot.CoordinatedWork))
	// Persist only the compact summary. It already carries the top writer and
	// true available count; repeating up to 20 writer rows in every existing
	// latest.json rewrite adds avoidable SSD traffic. Explicit live/full reads
	// retain the complete bounded writer report.
	report := monitor.DiskObserver.Latest(1)
	summary := report.Summary
	snapshot.DiskWrite = &summary
	snapshot.DiskWriteWriters = nil
}

func (monitor *Monitor) runResourceCleanup(ctx context.Context, snapshot Snapshot, allowed bool, state *resourceCleanupTelemetryState, stats *monitorStats) Snapshot {
	if monitor == nil || monitor.Cleaner == nil || state == nil {
		return snapshot
	}
	// If core agent-tree relief owned this sample, preserve an earlier cleanup
	// failure until the extension itself completes a successful check.
	if !allowed {
		snapshot.ResourceCleanupError = state.lastError
		return snapshot
	}
	result, err := monitor.Cleaner.MaybeRelieve(ctx, snapshot)
	if result.ControlExecuted {
		state.controlExecutedAt = snapshot.Timestamp
		state.controlDurationMS = result.ControlDurationMS
		state.controlMaxRSSMB = result.ControlMaxRSSMB
		snapshot.ResourceCleanupExecutedAt = state.controlExecutedAt
		snapshot.ResourceCleanupDurationMS = state.controlDurationMS
		snapshot.ResourceCleanupMaxRSSMB = state.controlMaxRSSMB
	}
	if err != nil {
		if stats != nil {
			stats.resourceCleanupFailures++
		}
		message := boundedText(err.Error(), durableTextLimit)
		snapshot.ResourceCleanupError = message
		if message != state.lastError {
			state.pendingErrorEvent = true
			state.pendingRecoveryEvent = false
		}
		state.lastError = message
		if state.pendingErrorEvent && (state.lastEvent.IsZero() || snapshot.Timestamp.Sub(state.lastEvent) >= resourceCleanupTelemetryMinInterval) {
			beforeBytes := int64(0)
			if monitor.Store != nil {
				beforeBytes = monitor.Store.BytesForDay(snapshot.Timestamp)
				if appendErr := monitor.Store.AppendEvent(TelemetryEvent{Timestamp: snapshot.Timestamp, Event: "resource_cleanup_error", Error: message}); appendErr == nil {
					state.lastEvent = snapshot.Timestamp
					state.pendingErrorEvent = false
					if stats != nil {
						stats.recordEventBytes("resource_cleanup_error", monitor.Store.BytesForDay(snapshot.Timestamp)-beforeBytes)
					}
				}
			}
		}
		return snapshot
	}
	if state.lastError != "" {
		state.pendingRecoveryEvent = !state.pendingErrorEvent
		state.pendingErrorEvent = false
	}
	state.lastError = ""
	if state.pendingRecoveryEvent && monitor.Store != nil && (state.lastEvent.IsZero() || snapshot.Timestamp.Sub(state.lastEvent) >= resourceCleanupTelemetryMinInterval) {
		beforeBytes := monitor.Store.BytesForDay(snapshot.Timestamp)
		if appendErr := monitor.Store.AppendEvent(TelemetryEvent{Timestamp: snapshot.Timestamp, Event: "resource_cleanup_recovered"}); appendErr == nil {
			state.lastEvent = snapshot.Timestamp
			state.pendingRecoveryEvent = false
			if stats != nil {
				stats.recordEventBytes("resource_cleanup_recovered", monitor.Store.BytesForDay(snapshot.Timestamp)-beforeBytes)
			}
		}
	}
	return snapshot
}

func selectResidentTelemetryEvent(now, lastPersist, lastTransitionPersist time.Time, recovered bool, consecutive int, policy Policy) (string, bool) {
	heartbeatDue := lastPersist.IsZero() || now.Sub(lastPersist) >= time.Duration(policy.HeartbeatSeconds)*time.Second
	transitionDue := lastTransitionPersist.IsZero() || now.Sub(lastTransitionPersist) >= telemetryTransitionMinInterval
	if transitionDue {
		if recovered {
			return "sample_recovered", true
		}
		if consecutive == 1 {
			return "state_transition", true
		}
	}
	if heartbeatDue {
		return "heartbeat", true
	}
	return "", false
}

func (monitor *Monitor) maybeRelieve(snapshot Snapshot, lastAction time.Time) (Action, bool) {
	return monitor.maybeRelieveExcluding(snapshot, lastAction, time.Time{}, nil)
}

func (monitor *Monitor) maybeRelieveExcluding(snapshot Snapshot, lastAction, lastRevalidation time.Time, rejected map[string]bool) (Action, bool) {
	// Only the low-RSS singleton helper is an action authority. Foreground
	// operator loops may collect diagnostic history but must never race launchd
	// or terminate a session tree.
	if snapshot.GuardRole != "resident" || !monitor.Policy.AutoShedCritical || snapshot.Level != LevelCritical {
		return Action{}, false
	}
	if !snapshot.GuardBudgetOK || !snapshot.GuardBaselineProven || !snapshot.ProcessInventoryFresh {
		return Action{}, false
	}
	wantSamples := monitor.Policy.CriticalSustainSamples
	if snapshot.ConsecutiveSamples < wantSamples {
		return Action{}, false
	}
	now := monitor.Now().UTC()
	if !lastAction.IsZero() && now.Sub(lastAction) < time.Duration(monitor.Policy.ActionCooldownSeconds)*time.Second {
		return Action{}, false
	}
	if !lastRevalidation.IsZero() && now.Sub(lastRevalidation) < reliefRevalidationRetryInterval {
		return Action{}, false
	}
	// Re-read the persisted policy at the destructive boundary. A failed
	// launchd restart after `policy observe` or uninstall must not leave an old
	// resident acting on its stale in-memory auto-shed setting. Any read error,
	// missing policy, or policy drift fails closed until a replacement resident
	// loads the exact persisted configuration.
	if monitor.Store == nil {
		return Action{}, false
	}
	persistedPolicy, persisted, err := LoadPolicy(PolicyPath(monitor.Store.Dir), snapshot.PhysicalMemoryMB)
	if err != nil || !persisted || persistedPolicy != monitor.Policy {
		return Action{}, false
	}
	tree, ok := selectReliefCandidateExcluding(snapshot.TopAgentTrees, monitor.Policy, rejected)
	if !ok {
		// This is not an action attempt. Do not consume the five-minute signal
		// cooldown or emit an action row: a currently active tree can become
		// safely quiescent on a later critical sample, and repeated 5-second
		// no-candidate rows would violate the telemetry budget.
		return Action{}, false
	}
	action := Action{
		Timestamp: now, Kind: "graceful_tree_shed", Level: snapshot.Level,
		RootPID: tree.RootPID, Agent: tree.Agent, SessionID: tree.SessionID, RSSSumMB: tree.RSSSumMB, SemanticState: tree.SemanticState,
		ReliefScope: "agent_trees_only", Result: "signal_sent", Reason: "sustained critical host pressure",
	}
	if len(snapshot.TopHostConsumers) > 0 {
		action.PrimaryHostExecutable = snapshot.TopHostConsumers[0].Executable
		action.PrimaryHostRSSSumMB = snapshot.TopHostConsumers[0].RSSSumMB
	}
	if snapshot.ProcessRSSSumMB > 0 {
		action.AgentRSSSharePercent = snapshot.AgentRSSSumMB * 100 / snapshot.ProcessRSSSumMB
	}
	result, err := monitor.Signaler.Terminate(tree, monitor.Policy)
	if result.SignalAttempted {
		action.Signal = "SIGTERM"
	}
	if !result.Snapshot.Timestamp.IsZero() {
		action.RevalidationDurationMS = result.Snapshot.SampleDurationMS
		action.RevalidationCPUTimeMS = result.Snapshot.SampleCPUTimeMS
		action.RevalidationGuardRSSMB = result.Snapshot.GuardRSSMB
		action.RevalidationPeakRSSMB = result.Snapshot.GuardPeakRSSMB
	}
	if result.Tree.RootPID != 0 {
		action.RevalidatedLevel = result.Snapshot.Level
		action.RevalidatedCPUPercent = result.Tree.CPUPercentSum
		action.RevalidatedRSSSumMB = result.Tree.RSSSumMB
		action.RevalidatedSemanticState = result.Tree.SemanticState
	}
	if err != nil {
		var rejected reliefRevalidationError
		if errors.As(err, &rejected) {
			action.Result = "revalidation_rejected"
		} else {
			action.Result = "error"
		}
		action.Reason = err.Error()
	} else if result.ExitChecked && result.TreeExited {
		action.Result = "tree_exit_confirmed"
	} else if result.ExitChecked {
		action.Result = "signal_sent_unconfirmed"
	}
	return action, true
}

func selectReliefCandidate(trees []AgentTree, policy Policy) (AgentTree, bool) {
	return selectReliefCandidateExcluding(trees, policy, nil)
}

func selectReliefCandidateExcluding(trees []AgentTree, policy Policy, rejected map[string]bool) (AgentTree, bool) {
	eligible := make([]AgentTree, 0, len(trees))
	for _, tree := range trees {
		if tree.ElapsedSeconds < int64(policy.CandidateMinAgeSeconds) || !validCPUEvidence(tree.CPUAvailable, tree.CPUPercentSum) || tree.CPUPercentSum > policy.CandidateMaxCPUPercent || tree.QuiescentSamples < policy.CriticalSustainSamples {
			continue
		}
		if tree.SemanticState == SemanticStateBusy {
			continue
		}
		if rejected[reliefTreeKey(tree)] {
			continue
		}
		eligible = append(eligible, tree)
	}
	if len(eligible) == 0 {
		return AgentTree{}, false
	}
	sort.Slice(eligible, func(i, j int) bool {
		readyI := eligible[i].SemanticState == SemanticStateReady
		readyJ := eligible[j].SemanticState == SemanticStateReady
		if readyI != readyJ {
			return readyI
		}
		if eligible[i].RSSSumMB != eligible[j].RSSSumMB {
			return eligible[i].RSSSumMB > eligible[j].RSSSumMB
		}
		if eligible[i].CPUPercentSum != eligible[j].CPUPercentSum {
			return eligible[i].CPUPercentSum < eligible[j].CPUPercentSum
		}
		return eligible[i].RootPID < eligible[j].RootPID
	})
	return eligible[0], true
}

func reliefTreeKey(tree AgentTree) string {
	return fmt.Sprintf("%d/%s/%s", tree.RootPID, boundedText(tree.Agent, 32), boundedText(tree.SessionID, actionIdentityLimit))
}

func reliefActionKey(action Action) string {
	return fmt.Sprintf("%d/%s/%s", action.RootPID, boundedText(action.Agent, 32), boundedText(action.SessionID, actionIdentityLimit))
}

func annotateQuiescence(trees []AgentTree, policy Policy, streak map[int]int) {
	seen := make(map[int]bool, len(trees))
	for index := range trees {
		tree := &trees[index]
		seen[tree.RootPID] = true
		if tree.ElapsedSeconds >= int64(policy.CandidateMinAgeSeconds) && validCPUEvidence(tree.CPUAvailable, tree.CPUPercentSum) && tree.CPUPercentSum <= policy.CandidateMaxCPUPercent {
			streak[tree.RootPID]++
		} else {
			streak[tree.RootPID] = 0
		}
		tree.QuiescentSamples = streak[tree.RootPID]
	}
	for pid := range streak {
		if !seen[pid] {
			delete(streak, pid)
		}
	}
}

func CheckAdmission(ctx context.Context, sampler *Sampler, policy Policy) Admission {
	snapshot, err := sampler.SampleHost(ctx, policy, nil)
	if err != nil {
		return Admission{Allowed: true, Level: LevelNormal, Source: "fail-open", Warning: "pressure sampling unavailable; admission failed open: " + err.Error()}
	}
	return AdmissionForSnapshot(snapshot, policy, "live-sample")
}
