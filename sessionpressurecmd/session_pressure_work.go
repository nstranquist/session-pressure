package sessionpressurecmd

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/nstranquist/session-pressure/sessionpressure"
)

var sessionPressureWorkHelp = fmt.Sprintf(`Usage: session-pressure [--json] work <subcommand>

Coordinate CPU-heavy work with one weighted capacity shared across classes.
Dead-owner leases are pruned automatically; command lines are never persisted.
bounded-lookahead-v1 gives a capacity-sized queue head an immediate drain
reservation. Other blocked heads admit at most %d younger operations or wait
%s after the first bypass before protection.
Prefer express-test/express-build for package-scoped work (default when --class
is omitted and the command is a recognized toolchain shape). Full test/build is
for multi-package / recursive suites; package-scoped full requests demote, and
express requests for ./... promote. Default benchmark leaves residual capacity
for express work; use --class benchmark-exclusive or --class benchmark
--exclusive for clean-host evidence. Eligible Go express jobs singleflight
identical full-argv digests and reuse a short-lived successful exit only while
source and declared artifacts still verify. Evaluate compares it with FIFO
and an oldest-feasible floor, then applies a separate selector overhead gate.

Subcommands:
  status
  history [--since DURATION] [--limit N] [--class CLASS] [--event EVENT] [--operation-id ID] [--full]
  stats [--since DURATION] [--full]
  report [--since DURATION] [--full]
  evaluate
  override (--operation-id <ID> [--operation-id <ID>...] | --all | --clear) --confirm
  run [--class <express-test|express-build|test|build|install|emulator|browser|heavy|benchmark|benchmark-exclusive|reclaim>] [--exclusive] [--no-reuse] [--priority] [--wait DURATION] [--progress <human|jsonl|quiet>] -- COMMAND [ARGS...]
  batch --file <MANIFEST.json|-> [--wait DURATION] [--progress <human|jsonl|quiet>]

Batch manifests use strict schema_version 1 argv steps and hold one lease across
the whole finite sequence. Optional successful reuse requires declared source,
environment, toolchain, and external-state boundaries; receipts replay only a
successful exit status, never stdout or stderr.

A fast lane admits light, short work at CPU-red instead of latching it: the class
weight must fit the fast-lane ceiling and the free weighted capacity, and measured
class p95 runtime must be under the runtime ceiling. It fails closed without
calibration or coordinated-work CPU attribution, never applies to memory, swap, or
storage pressure, and never raises the weighted ceiling. Disable it with
fast_lane_enabled: false in work_limits.
At CPU or memory warning, the coordinator stays active but new admissions use
warning_capacity (half the normal capacity by default). Existing leases are never
preempted; they drain while excess arrivals remain visible as admission_holds.
Work parked at the host-pressure gate before it reaches the queue appears in
work status as admission_holds with held_for_ms, so a blocked process is never
invisible next to an empty queue.

The default wait is 10m and covers singleflight, red host pressure, and shared capacity.
Use --wait 0 to fail immediately when either gate is closed.
Use --no-reuse to force a real execution and record the disabled reuse decision.
Use --priority for an audited agent self-priority request. It appends behind any
operator-pinned sequence and changes queue order only; it cannot bypass host
pressure, capacity, storage, active leases, or the operator's existing order.
Override is a one-shot queue-priority mutation. It runs the selected live waiters
next, in the order given, but never preempts leases or bypasses host-pressure or
capacity safety. Repeat --operation-id to pin an explicit order, or use --all to
pin every waiter in current queue order; each entry inherits the head
reservation as its predecessor acquires. A later override replaces the whole
pinned sequence rather than adding to it, and --clear releases it so the queue
returns to ordinary policy without disturbing active leases.
`, sessionpressure.WorkMaximumBypasses, sessionpressure.WorkReservationAge)

var pressureWorkAdmissionRetryInterval = sessionpressure.WorkAdmissionRetryInterval
var execPressureWorkHelper = syscall.Exec
var pressureWorkCommandFactory sessionpressure.WorkCommandFactory
var resolvePressureWorkHelper = func() (string, error) {
	dir, err := sessionpressure.DataDir()
	if err != nil {
		return "", err
	}
	artifact, err := sessionpressure.VerifyInstalledArtifact(dir)
	if err != nil {
		return "", err
	}
	return artifact.Path, nil
}

type pressureWorkRunOptions struct {
	class    sessionpressure.WorkClass
	wait     time.Duration
	progress sessionpressure.WorkProgressMode
	command  []string
	noReuse  bool
	priority bool
}

func parsePressureWorkRunArgs(args []string) (pressureWorkRunOptions, error) {
	options, err := sessionpressure.ParseWorkRunArgs(args)
	if err != nil {
		return pressureWorkRunOptions{}, err
	}
	return pressureWorkRunOptions{class: options.Class, wait: options.Wait, progress: options.Progress, command: options.Command, noReuse: options.NoReuse, priority: options.Priority}, nil
}

func cmdSessionPressureWork(g *Flags, args []string) int {
	sub := "status"
	if len(args) > 0 {
		sub = args[0]
		args = args[1:]
	}
	if sub == "help" || sub == "--help" || sub == "-h" {
		fmt.Print(sessionPressureWorkHelp)
		return 0
	}
	if sub == "run" {
		if g != nil && g.JSON {
			return sessionPressureError("work run does not accept --json because the child owns stdout; use work status for JSON", 2)
		}
		if _, err := sessionpressure.ParseWorkRunArgs(args); err != nil {
			return sessionPressureError(err.Error(), 2)
		}
		return runPressureWorkHelper(args)
	}
	if sub == "batch" {
		if g != nil && g.JSON {
			return sessionPressureError("work batch does not accept --json because batch steps own stdout; use --progress jsonl for lifecycle progress", 2)
		}
		if _, err := sessionpressure.ParseWorkBatchArgs(args); err != nil {
			return sessionPressureError(err.Error(), 2)
		}
		return runPressureWorkHelperMode("work-batch", args)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	runtime, err := loadPressureRuntime(ctx)
	cancel()
	if err != nil {
		return sessionPressureError(err.Error(), 1)
	}
	coordinator := sessionpressure.NewWorkCoordinator(runtime.dir, runtime.policy.WorkLimits)
	switch sub {
	case "status":
		if len(args) != 0 {
			return sessionPressureError("work status accepts no arguments", 2)
		}
		statusCtx, statusCancel := context.WithTimeout(context.Background(), 5*time.Second)
		status, err := coordinator.Status(statusCtx)
		statusCancel()
		if err != nil {
			return sessionPressureError(err.Error(), 1)
		}
		payload := map[string]any{"ok": true, "action": "work.status", "work": status}
		text := pressureWorkStatusText(status)
		return emitPressure(g, payload, text, 0)
	case "override":
		return cmdSessionPressureWorkOverride(g, coordinator, args)
	case "history":
		return cmdSessionPressureWorkHistory(g, runtime.dir, args)
	case "stats":
		return cmdSessionPressureWorkStats(g, runtime.dir, runtime.policy, args)
	case "report":
		return cmdSessionPressureWorkReport(g, runtime.dir, runtime.policy, args)
	case "evaluate":
		if len(args) != 0 {
			return sessionPressureError("work evaluate accepts no arguments", 2)
		}
		report := sessionpressure.EvaluateWorkSystem(runtime.policy)
		events, replayErr := sessionpressure.NewWorkEventStore(runtime.dir).Read(sessionpressure.WorkEventFilter{Since: time.Now().Add(-24 * time.Hour)})
		if replayErr != nil {
			return sessionPressureError("read work events for replay: "+replayErr.Error(), 1)
		}
		replay := sessionpressure.ReplayWorkEvents(events, runtime.policy.WorkLimits)
		replay.ApplySelectorBenchmark(report.SelectorBenchmark)
		report.Replay = &replay
		if !replay.PromotionReady {
			report.OK = false
			report.ReviewSignals = append(report.ReviewSignals, "live trace replay has not cleared every scheduler promotion gate")
		}
		payload := map[string]any{"ok": report.OK, "action": "work.evaluate", "work_evaluation": report}
		text := fmt.Sprintf("work evaluation ok=%v scenarios=%d passed=%d failed=%d runtime=%s monitor_projection=%dB/day total_projection=%dB/day max_work_event=%dB\n", report.OK, report.ScenarioCount, report.Passed, report.Failed, time.Duration(report.RuntimeMilliseconds)*time.Millisecond, report.ProjectedMonitorBytesDay, report.ProjectedTelemetryBytesDay, report.MaximumWorkEventBytes)
		exit := 0
		if !report.OK {
			exit = 1
		}
		return emitPressure(g, payload, text, exit)
	default:
		return sessionPressureError("unknown work subcommand "+strconv.Quote(sub)+"; try: session-pressure work --help (status|run|batch|override|history|stats|report|evaluate)", 2)
	}
}

func cmdSessionPressureWorkOverride(g *Flags, coordinator *sessionpressure.WorkCoordinator, args []string) int {
	operationIDs := []string{}
	confirmed := false
	all := false
	clear := false
	for index := 0; index < len(args); index++ {
		switch args[index] {
		case "--operation-id":
			index++
			if index >= len(args) || strings.TrimSpace(args[index]) == "" {
				return sessionPressureError("work override --operation-id requires a value", 2)
			}
			operationIDs = append(operationIDs, strings.TrimSpace(args[index]))
		case "--all":
			all = true
		case "--clear":
			clear = true
		case "--confirm":
			confirmed = true
		default:
			return sessionPressureError("unknown work override argument "+strconv.Quote(args[index]), 2)
		}
	}
	if clear && (all || len(operationIDs) > 0) {
		return sessionPressureError("work override --clear releases the whole pinned sequence and takes no --all or --operation-id", 2)
	}
	if all && len(operationIDs) > 0 {
		return sessionPressureError("work override takes either --all or explicit --operation-id values, not both", 2)
	}
	if !clear && !all && len(operationIDs) == 0 {
		return sessionPressureError("work override requires --operation-id from the current work status, --all to pin the whole queue, or --clear to release it", 2)
	}
	if !confirmed {
		return sessionPressureError("work override requires --confirm; it changes queue priority but preserves pressure and capacity safety", 2)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	var results []sessionpressure.WorkOverrideResult
	var status sessionpressure.WorkStatus
	var err error
	switch {
	case clear:
		results, status, err = coordinator.ClearWaiterOverride(ctx)
	case all:
		results, status, err = coordinator.OverrideAllWaiters(ctx)
	default:
		results, status, err = coordinator.OverrideWaiters(ctx, operationIDs)
	}
	cancel()
	if err != nil {
		return sessionPressureError(err.Error(), 1)
	}
	if len(results) == 0 {
		return sessionPressureError("work override matched no queued operation", 1)
	}
	if clear {
		payload := map[string]any{
			"ok": true, "action": "work.override", "cleared": len(results),
			"overrides": results, "pinned": 0, "work": status,
		}
		var text strings.Builder
		fmt.Fprintf(&text, "work override cleared=%d; the queue returns to %s policy and active leases keep running\n", len(results), status.SchedulingPolicy)
		return emitPressure(g, payload, text.String(), 0)
	}
	head := results[0]
	payload := map[string]any{
		"ok": true, "action": "work.override",
		// `override` stays the active head so existing single-override readers
		// keep working; `overrides` carries the whole confirmed sequence.
		"override": head, "overrides": results, "pinned": len(results), "work": status,
	}
	var text strings.Builder
	fmt.Fprintf(&text, "work override pinned=%d head=%s class=%s previous_position=%d already_requested=%v; pinned tasks run in this order as pressure and capacity gates open\n", len(results), head.OperationID, head.Class, head.PreviousPosition, head.AlreadyRequested)
	for _, result := range results[1:] {
		fmt.Fprintf(&text, "  #%d %s class=%s weight=%d previous_position=%d\n", result.OverridePosition, result.OperationID, result.Class, result.Weight, result.PreviousPosition)
	}
	return emitPressure(g, payload, text.String(), 0)
}

func pressureWorkStatusText(status sessionpressure.WorkStatus) string {
	reviewCount := 0
	for _, lease := range status.Leases {
		if lease.Review {
			reviewCount++
		}
	}
	var text strings.Builder
	fmt.Fprintf(&text, "capacity=%d used=%d available=%d leases=%d queue=%d held=%d review_leases=%d pruned_leases=%d pruned_waiters=%d state=%s\n", status.Capacity, status.Used, status.Available, len(status.Leases), status.QueueDepth, status.AdmissionHoldCount, reviewCount, status.Pruned, status.PrunedWaiters, status.StatePath)
	for _, lease := range status.Leases {
		if !lease.Review {
			continue
		}
		fmt.Fprintf(&text, "  review class=%s weight=%d age=%s reason=%s\n", lease.Class, lease.Weight, (time.Duration(lease.AgeMS) * time.Millisecond).Round(time.Second), lease.ReviewReason)
	}
	// Held work is blocked before it ever reaches the queue, so it would
	// otherwise be invisible next to an empty queue and idle capacity.
	for _, hold := range status.AdmissionHolds {
		fmt.Fprintf(&text, "  held class=%s weight=%d for=%s dimension=%s reason=%s\n",
			hold.Class, hold.Weight, (time.Duration(hold.HeldForMS) * time.Millisecond).Round(time.Second), hold.Dimension, hold.Reason)
	}
	if latch := status.AdmissionLatch; latch != nil && latch.Latched {
		fmt.Fprintf(&text, "  latch=engaged dimension=%s recovery=%d/%d reason=%s\n",
			latch.Dimension, latch.RecoverySamples, latch.ReleaseRequired, latch.Reason)
	}
	return text.String()
}

func cmdSessionPressureWorkHistory(g *Flags, dir string, args []string) int {
	// Agent-safe default: 20 decision-grade rows. Operators raise --limit or pass
	// --full for digests, shadow selector fields, and coordinated-work forensics.
	filter := sessionpressure.WorkEventFilter{Since: time.Now().Add(-24 * time.Hour), Limit: 20}
	full := false
	for index := 0; index < len(args); index++ {
		switch args[index] {
		case "--full":
			if full {
				return sessionPressureError("work history accepts --full only once", 2)
			}
			full = true
		case "--since":
			index++
			if index >= len(args) {
				return sessionPressureError("work history --since requires a duration", 2)
			}
			duration, err := time.ParseDuration(args[index])
			if err != nil || duration <= 0 {
				return sessionPressureError("work history --since must be a positive duration such as 24h", 2)
			}
			filter.Since = time.Now().Add(-duration)
		case "--limit":
			index++
			if index >= len(args) {
				return sessionPressureError("work history --limit requires an integer", 2)
			}
			value, err := strconv.Atoi(args[index])
			if err != nil || value < 1 || value > 10000 {
				return sessionPressureError("work history --limit must be between 1 and 10000", 2)
			}
			filter.Limit = value
		case "--class":
			index++
			if index >= len(args) {
				return sessionPressureError("work history --class requires a class", 2)
			}
			class, err := sessionpressure.ParseWorkClass(args[index])
			if err != nil {
				return sessionPressureError(err.Error(), 2)
			}
			filter.Class = class
		case "--event":
			index++
			if index >= len(args) {
				return sessionPressureError("work history --event requires an event", 2)
			}
			eventType, err := sessionpressure.ParseWorkEventType(args[index])
			if err != nil {
				return sessionPressureError(err.Error(), 2)
			}
			filter.Event = eventType
		case "--operation-id":
			index++
			if index >= len(args) {
				return sessionPressureError("work history --operation-id requires a value", 2)
			}
			filter.OperationID = strings.TrimSpace(args[index])
		default:
			return sessionPressureError("unknown work history argument "+strconv.Quote(args[index]), 2)
		}
	}
	events, diagnostics, err := sessionpressure.NewWorkEventStore(dir).ReadWithDiagnostics(filter)
	if err != nil {
		return sessionPressureError(err.Error(), 1)
	}
	payload := map[string]any{
		"ok": true, "action": "work.history", "output_scope": "compact",
		"work_event_count": len(events), "since": filter.Since.UTC(),
		"work_events": sessionpressure.CompactWorkEvents(events),
	}
	if full {
		payload["output_scope"] = "full"
		payload["work_events"] = events
	}
	addWorkEventDiagnostics(payload, diagnostics)
	if g != nil && g.JSON {
		return emitPressure(g, payload, "", 0)
	}
	printWorkEventDiagnostics(diagnostics)
	for _, event := range events {
		fmt.Printf("%s\t%s\t%s\top=%s\tlease=%s\tblocker=%s\tqueue=%d/%d\tcapacity=%d/%d\tpressure=%s/%s\treason=%q\twait=%s\truntime=%s\toutcome=%s\n",
			event.Timestamp.Local().Format(time.RFC3339), event.Event, event.Class, event.OperationID, event.LeaseID, event.Blocker,
			event.QueuePosition, event.QueueDepth, event.Used, event.Capacity, event.PressureLevel, event.PressureDimension,
			event.PressureReason, time.Duration(event.WaitMilliseconds)*time.Millisecond, time.Duration(event.RuntimeMillis)*time.Millisecond, event.Outcome)
	}
	return 0
}

func cmdSessionPressureWorkStats(g *Flags, dir string, policy sessionpressure.Policy, args []string) int {
	options, err := parseWorkSummaryArgs(args, "work stats")
	if err != nil {
		return sessionPressureError(err.Error(), 2)
	}
	events, diagnostics, err := sessionpressure.NewWorkEventStore(dir).ReadWithDiagnostics(sessionpressure.WorkEventFilter{Since: options.since})
	if err != nil {
		return sessionPressureError(err.Error(), 1)
	}
	stats := sessionpressure.SummarizeWorkEvents(events, options.since, time.Now())
	// One source of truth: embed calibration ratios on stats when JSON.
	calibration := sessionpressure.BuildWorkCalibrationReport(events, options.since, time.Now().UTC())
	calibration.SuppressIfAlreadyApplied(policy)
	payload := map[string]any{"ok": true, "action": "work.stats", "output_scope": "compact", "work_stats": compactPressureWorkStats(stats)}
	if options.full {
		payload["output_scope"] = "full"
		payload["work_stats"] = stats
		payload["calibration"] = calibration
	}
	addWorkEventDiagnostics(payload, diagnostics)
	if g != nil && g.JSON {
		return emitPressure(g, payload, "", 0)
	}
	printWorkEventDiagnostics(diagnostics)
	fmt.Printf("operations=%d events=%d open=%d expired=%d cancelled=%d long_wait=%d cache_hits=%d cache_misses=%d singleflight_waits=%d cpu_deferrals=%d memory_deferrals=%d storage_deferrals=%d slowdown_slo=%s slowdown_scope=%s target_p95=%.1f samples_required=%d evaluated=%d deferred=%d\n", stats.OperationCount, stats.EventCount, stats.OpenOperations, stats.ReviewSignals.ExpiredOwnerEvents, stats.ReviewSignals.CancelledOperations, stats.ReviewSignals.LongWaitOperations, stats.ReviewSignals.CacheHits, stats.ReviewSignals.CacheMisses, stats.ReviewSignals.SingleflightWaits, stats.ReviewSignals.CPUOnlyDeferrals, stats.ReviewSignals.MemoryDeferrals, stats.ReviewSignals.StorageDeferrals, stats.ServiceLevel.Status, stats.ServiceLevel.Scope, stats.ServiceLevel.TargetP95BoundedSlowdown, stats.ServiceLevel.SamplesRequired, stats.ServiceLevel.EvaluatedClasses, len(stats.ServiceLevel.DeferredClasses))
	for _, class := range stats.ByClass {
		fmt.Printf("  %-8s operations=%d completed=%d reused=%d failed=%d cancelled=%d expired=%d wait_p95=%s runtime_p95=%s\n", class.Class, class.Operations, class.Completed, class.Reused, class.Failed, class.Cancelled, class.Expired, time.Duration(class.Wait.P95MS)*time.Millisecond, time.Duration(class.Runtime.P95MS)*time.Millisecond)
	}
	fmt.Printf("calibration express_test_share=%.2f express_build_share=%.2f reuse_hits=%d wrapper_interrupts=%d retune=%s\n", calibration.ExpressTestShare, calibration.ExpressBuildShare, calibration.ReuseHits, calibration.WrapperInterruptOperations, calibration.ThresholdRetuneHint)
	return 0
}

func cmdSessionPressureWorkReport(g *Flags, dir string, policy sessionpressure.Policy, args []string) int {
	options, err := parseWorkSummaryArgs(args, "work report")
	if err != nil {
		return sessionPressureError(err.Error(), 2)
	}
	events, diagnostics, err := sessionpressure.NewWorkEventStore(dir).ReadWithDiagnostics(sessionpressure.WorkEventFilter{Since: options.since})
	if err != nil {
		return sessionPressureError(err.Error(), 1)
	}
	calibration := sessionpressure.BuildWorkCalibrationReport(events, options.since, time.Now().UTC())
	calibration.SuppressIfAlreadyApplied(policy)
	payload := map[string]any{"ok": true, "action": "work.report", "output_scope": "compact", "calibration": compactPressureWorkCalibration(calibration)}
	if options.full {
		payload["output_scope"] = "full"
		payload["calibration"] = calibration
	}
	addWorkEventDiagnostics(payload, diagnostics)
	text := fmt.Sprintf(
		"work report operations=%d express_test=%d/%d share=%.2f express_build=%d/%d share=%.2f reuse_hits=%d cache_hits=%d cache_misses=%d wrapper_interrupts=%d\n  retune=%s\n",
		calibration.OperationCount,
		calibration.ExpressTestOps, calibration.ExpressTestOps+calibration.FullTestOps, calibration.ExpressTestShare,
		calibration.ExpressBuildOps, calibration.ExpressBuildOps+calibration.FullBuildOps, calibration.ExpressBuildShare,
		calibration.ReuseHits, calibration.CacheHits, calibration.CacheMisses, calibration.WrapperInterruptOperations, calibration.ThresholdRetuneHint,
	)
	if calibration.SuggestedPolicyProfile != "" {
		text += fmt.Sprintf("  suggested_profile=%s reason=%s  # advisory only; try: session-pressure policy profile apply %s --dry-run\n",
			calibration.SuggestedPolicyProfile, calibration.SuggestedPolicyProfileReason, calibration.SuggestedPolicyProfile)
	}
	text += workEventDiagnosticsText(diagnostics)
	return emitPressure(g, payload, text, 0)
}

// addWorkEventDiagnostics keeps a tolerated skip visible in machine output. A
// silently shrinking population would read as "nothing happened" instead of
// "some history was unreadable".
func addWorkEventDiagnostics(payload map[string]any, diagnostics sessionpressure.WorkEventDiagnostics) {
	if payload == nil || !diagnostics.Degraded() {
		return
	}
	payload["skipped_operations"] = diagnostics.SkippedOperations
	payload["skipped_events"] = diagnostics.SkippedEvents
	if len(diagnostics.Reasons) > 0 {
		payload["skip_reasons"] = diagnostics.Reasons
	}
}

func workEventDiagnosticsText(diagnostics sessionpressure.WorkEventDiagnostics) string {
	if !diagnostics.Degraded() {
		return ""
	}
	text := fmt.Sprintf("  skipped_operations=%d skipped_events=%d  # unreadable lifecycles were excluded\n",
		diagnostics.SkippedOperations, diagnostics.SkippedEvents)
	for _, reason := range diagnostics.Reasons {
		text += fmt.Sprintf("    - %s\n", reason)
	}
	return text
}

func printWorkEventDiagnostics(diagnostics sessionpressure.WorkEventDiagnostics) {
	if text := workEventDiagnosticsText(diagnostics); text != "" {
		fmt.Fprint(os.Stderr, text)
	}
}

type workSummaryOptions struct {
	since time.Time
	full  bool
}

func parseWorkSummaryArgs(args []string, label string) (workSummaryOptions, error) {
	options := workSummaryOptions{since: time.Now().Add(-24 * time.Hour)}
	for len(args) > 0 {
		switch args[0] {
		case "--full":
			if options.full {
				return workSummaryOptions{}, fmt.Errorf("%s accepts --full only once", label)
			}
			options.full = true
			args = args[1:]
		case "--since":
			if len(args) < 2 {
				return workSummaryOptions{}, fmt.Errorf("%s --since requires a duration", label)
			}
			duration, err := time.ParseDuration(args[1])
			if err != nil || duration <= 0 {
				return workSummaryOptions{}, fmt.Errorf("%s --since must be a positive duration such as 24h", label)
			}
			options.since = time.Now().Add(-duration)
			args = args[2:]
		default:
			return workSummaryOptions{}, fmt.Errorf("%s accepts only --since DURATION and --full", label)
		}
	}
	return options, nil
}

func compactPressureWorkStats(stats sessionpressure.WorkStats) sessionpressure.WorkStats {
	active := make([]sessionpressure.WorkClassStats, 0, len(stats.ByClass))
	for _, class := range stats.ByClass {
		if class.Operations > 0 {
			active = append(active, class)
		}
	}
	stats.ByClass = active
	stats.CalibrationCohorts = []sessionpressure.WorkCalibrationCohort{}
	stats.ServiceLevel.EvaluatedSamples = []sessionpressure.WorkClassSLOSample{}
	stats.ServiceLevel.DeferredClasses = []sessionpressure.WorkClassSLOSample{}
	stats.ServiceLevel.Breaches = []sessionpressure.WorkClassSLOBreach{}
	stats.PressureConditionedServiceLevel.EvaluatedSamples = []sessionpressure.WorkClassSLOSample{}
	stats.PressureConditionedServiceLevel.DeferredClasses = []sessionpressure.WorkClassSLOSample{}
	stats.PressureConditionedServiceLevel.Breaches = []sessionpressure.WorkClassSLOBreach{}
	stats.PressureConditionedServiceLevel.ByClass = []sessionpressure.WorkPressureConditionedClass{}
	return stats
}

func compactPressureWorkCalibration(calibration sessionpressure.WorkCalibrationReport) sessionpressure.WorkCalibrationReport {
	calibration.ByClass = []sessionpressure.WorkClassStats{}
	calibration.Outcomes = []sessionpressure.WorkCalibrationOutcome{}
	calibration.InterruptProjection = nil
	return calibration
}

func runPressureWorkHelper(args []string) int {
	return runPressureWorkHelperMode("work-run", args)
}

func runPressureWorkHelperMode(mode string, args []string) int {
	helper, err := resolvePressureWorkHelper()
	if err != nil {
		return sessionPressureError(err.Error(), 1)
	}
	argv := append([]string{helper, mode}, args...)
	if err := execPressureWorkHelper(helper, argv, os.Environ()); err != nil {
		return sessionPressureError("start lightweight work helper: "+err.Error(), 1)
	}
	return 0
}

// The adapter keeps the runner independently testable without retaining a
// full-size ndev process in the production wait path.
func runPressureWorkCommand(coordinator *sessionpressure.WorkCoordinator, options pressureWorkRunOptions) int {
	code, err := sessionpressure.RunWorkCommandWithExpressReuse(coordinator, sessionpressure.WorkRunOptions{
		Class: options.class, Wait: options.wait, Progress: options.progress, Command: options.command, NoReuse: options.noReuse, Priority: options.priority,
	}, WorkHostAdmissionCheck, pressureWorkAdmissionRetryInterval, sessionpressure.WorkRunStreams{
		Stdin: os.Stdin, Stdout: os.Stdout, Stderr: os.Stderr, CommandFactory: pressureWorkCommandFactory,
	})
	if err != nil {
		return sessionPressureError(err.Error(), code)
	}
	return code
}

func pressureAdmissionReason(admission sessionpressure.Admission) string {
	return sessionpressure.AdmissionReason(admission)
}

func pressureWorkEnvironment(base []string, limits sessionpressure.WorkLimits, class sessionpressure.WorkClass) ([]string, error) {
	return sessionpressure.WorkEnvironment(base, limits, class)
}
