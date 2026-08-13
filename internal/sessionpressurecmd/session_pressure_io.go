package sessionpressurecmd

import (
	"context"
	"fmt"
	"net/url"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/nstranquist/session-pressure/internal/sessionpressure"
)

const sessionPressureIOHelp = `Usage: ndev [--json] session pressure io <subcommand> [flags]

Observe internal-SSD block writes and correlate best-effort process writes.
Process counters cover all volumes and are not exact internal-SSD attribution.

Subcommands:
  status [--live] [--full]                 Compact summary; live takes a bounded one-second sample
  top [--live] [--limit N]                 Persisted lead writer; --live defaults to 5, maximum 20
  history [--since D] [--limit N]          Hourly bounded history, default 24h/20 and maximum 200
  policy show|observe|enable-alerts|disable Control observation and notification opt-in
  trace --pid PID [--duration D] [--open]  Hand off an interactive 5-30 second path trace to NDev Pressure

Examples:
  ndev --json session pressure io status --live
  ndev --json session pressure io top --live --limit 5
  ndev --json session pressure io history --since 24h --limit 20
  ndev session pressure io policy enable-alerts
  ndev --json session pressure io trace --pid 1234
`

func cmdSessionPressureIO(g *Flags, args []string) int {
	sub := "status"
	if len(args) > 0 {
		sub, args = args[0], args[1:]
	}
	switch sub {
	case "help", "--help", "-h":
		fmt.Print(sessionPressureIOHelp)
		return 0
	case "status":
		return cmdSessionPressureIOStatus(g, args)
	case "top":
		return cmdSessionPressureIOTop(g, args)
	case "history":
		return cmdSessionPressureIOHistory(g, args)
	case "policy":
		return cmdSessionPressureIOPolicy(g, args)
	case "trace":
		return cmdSessionPressureIOTrace(g, args)
	default:
		return sessionPressureError("unknown io subcommand "+strconv.Quote(sub), 2)
	}
}

func loadDiskWriteReport(runtime pressureRuntime, live bool) (sessionpressure.DiskWriteReport, bool, error) {
	if live {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		report, err := sessionpressure.LiveDiskWriteReport(ctx, runtime.dir, runtime.policy.DiskWrite, time.Second)
		if err == nil {
			if latest, ok := runtime.store.ReadLatest(); ok && latest.DiskWrite != nil {
				age := time.Since(latest.DiskWrite.CapturedAt)
				if age >= -5*time.Second && age <= 5*time.Minute {
					report = sessionpressure.MergeLiveDiskWriteReport(report, *latest.DiskWrite)
				}
			}
		}
		return report, true, err
	}
	latest, ok := runtime.store.ReadLatest()
	if !ok || latest.DiskWrite == nil {
		return sessionpressure.DiskWriteReport{}, false, nil
	}
	writers := append([]sessionpressure.DiskWriter(nil), latest.DiskWriteWriters...)
	if len(writers) == 0 && latest.DiskWrite.TopWriter != nil {
		writers = append(writers, sessionpressure.DiskWriter{DiskWriterSummary: *latest.DiskWrite.TopWriter})
	}
	if writers == nil {
		writers = []sessionpressure.DiskWriter{}
	}
	available := max(latest.DiskWrite.WriterAvailableCount, len(writers))
	report := sessionpressure.DiskWriteReport{
		Summary: *latest.DiskWrite, Writers: writers, AvailableCount: available, ReturnedCount: len(writers),
		Truncated: available > len(writers),
	}
	return report, true, nil
}

func unavailablePersistedDiskWrite(runtime pressureRuntime) sessionpressure.DiskWriteSummary {
	state := sessionpressure.DiskWriteStateUnavailable
	reason := "no_persisted_sample"
	if !runtime.policy.DiskWrite.Enabled {
		state = sessionpressure.DiskWriteStateDisabled
		reason = "monitoring_disabled"
	}
	return sessionpressure.DiskWriteSummary{
		SchemaVersion: sessionpressure.DiskWriteSummarySchemaVersion,
		ModelVersion:  sessionpressure.DiskWriteProfileQuietAdaptiveV1,
		CapturedAt:    time.Now().UTC(), State: state, Confidence: sessionpressure.DiskWriteConfidenceNone,
		Source: "iokit+libproc", DeviceScope: "internal_ssd", AttributionScope: "all_disk_io_best_effort",
		Context: "uncoordinated", Reasons: []string{reason},
	}
}

func cmdSessionPressureIOStatus(g *Flags, args []string) int {
	live, full := false, false
	for _, arg := range args {
		switch arg {
		case "--live":
			live = true
		case "--full":
			full = true
		default:
			return sessionPressureError("io status accepts only --live and --full", 2)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	runtime, err := loadPressureRuntime(ctx)
	cancel()
	if err != nil {
		return sessionPressureError(err.Error(), 1)
	}
	report, found, err := loadDiskWriteReport(runtime, live)
	if err != nil {
		return sessionPressureError(err.Error(), 1)
	}
	if !found {
		report.Summary = unavailablePersistedDiskWrite(runtime)
		report.Writers = []sessionpressure.DiskWriter{}
	}
	payload := map[string]any{
		"ok": true, "action": "io.status", "output_scope": "compact", "summary": report.Summary,
	}
	if full {
		payload["output_scope"] = "full"
		payload["report"] = report
		payload["disk_write_policy"] = runtime.policy.DiskWrite
	}
	text := fmt.Sprintf("state=%s confidence=%s rate=%s/s window_15m=%s total_24h=%s baseline_ratio=%.2fx scope=%s attribution=%s\n",
		report.Summary.State, report.Summary.Confidence, humanBytes(int64(report.Summary.CurrentBytesPerSecond)),
		humanBytes(int64(report.Summary.Window15mBytes)), humanBytes(int64(report.Summary.Bytes24h)), report.Summary.BaselineRatio,
		report.Summary.DeviceScope, report.Summary.AttributionScope)
	return emitPressure(g, payload, text, 0)
}

func cmdSessionPressureIOTop(g *Flags, args []string) int {
	live, limit := false, 0
	for index := 0; index < len(args); index++ {
		switch args[index] {
		case "--live":
			live = true
		case "--limit":
			index++
			if index >= len(args) {
				return sessionPressureError("io top --limit requires an integer", 2)
			}
			value, err := strconv.Atoi(args[index])
			if err != nil || value < 1 || value > 20 {
				return sessionPressureError("io top --limit must be between 1 and 20", 2)
			}
			limit = value
		default:
			return sessionPressureError("unknown io top argument "+strconv.Quote(args[index]), 2)
		}
	}
	if limit == 0 {
		limit = 1
		if live {
			limit = 5
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	runtime, err := loadPressureRuntime(ctx)
	cancel()
	if err != nil {
		return sessionPressureError(err.Error(), 1)
	}
	report, found, err := loadDiskWriteReport(runtime, live)
	if err != nil {
		return sessionPressureError(err.Error(), 1)
	}
	if !found {
		report.Summary = unavailablePersistedDiskWrite(runtime)
		report.Writers = []sessionpressure.DiskWriter{}
	}
	available := max(report.AvailableCount, len(report.Writers))
	if len(report.Writers) > limit {
		report.Writers = report.Writers[:limit]
	}
	outputScope := "live"
	if !live {
		outputScope = "persisted_lead"
		for index := range report.Writers {
			report.Writers[index].PID = 0
			report.Writers[index].ProcessStartID = 0
		}
	}
	report.AvailableCount = available
	report.ReturnedCount = len(report.Writers)
	report.Truncated = available > report.ReturnedCount
	payload := map[string]any{
		"ok": true, "action": "io.top", "captured_at": report.Summary.CapturedAt,
		"output_scope": outputScope,
		"device_scope": report.Summary.DeviceScope, "attribution_scope": report.Summary.AttributionScope,
		"writers": report.Writers, "available_count": available, "returned_count": len(report.Writers), "truncated": report.Truncated,
	}
	if g != nil && g.JSON {
		return emitPressure(g, payload, "", 0)
	}
	if len(report.Writers) == 0 {
		return emitPressure(g, payload, "no attributed writers in the current bounded window\n", 0)
	}
	var text strings.Builder
	for _, writer := range report.Writers {
		fmt.Fprintf(&text, "%s\t%s/s\t%s/15m\tprocesses=%d\tagent=%d\n", writer.Executable, humanBytes(int64(writer.BytesPerSecond)), humanBytes(int64(writer.WindowBytes)), writer.ProcessCount, writer.AgentProcessCount)
	}
	return emitPressure(g, payload, text.String(), 0)
}

func cmdSessionPressureIOHistory(g *Flags, args []string) int {
	limit := 20
	sinceDuration := 24 * time.Hour
	for index := 0; index < len(args); index++ {
		switch args[index] {
		case "--limit":
			index++
			if index >= len(args) {
				return sessionPressureError("io history --limit requires an integer", 2)
			}
			value, err := strconv.Atoi(args[index])
			if err != nil || value < 1 || value > 200 {
				return sessionPressureError("io history --limit must be between 1 and 200", 2)
			}
			limit = value
		case "--since":
			index++
			if index >= len(args) {
				return sessionPressureError("io history --since requires a duration", 2)
			}
			value, err := time.ParseDuration(args[index])
			if err != nil || value <= 0 || value > 30*24*time.Hour {
				return sessionPressureError("io history --since must be between 1ns and 720h", 2)
			}
			sinceDuration = value
		default:
			return sessionPressureError("unknown io history argument "+strconv.Quote(args[index]), 2)
		}
	}
	dir, err := sessionpressure.DataDir()
	if err != nil {
		return sessionPressureError(err.Error(), 1)
	}
	since := time.Now().UTC().Add(-sinceDuration)
	points, available, err := sessionpressure.NewDiskWriteStore(dir).ReadHistory(limit, since)
	if err != nil {
		return sessionPressureError(err.Error(), 1)
	}
	payload := map[string]any{
		"ok": true, "action": "io.history", "since": since, "history": points,
		"available_count": available, "returned_count": len(points), "truncated": available > len(points),
	}
	if g != nil && g.JSON {
		return emitPressure(g, payload, "", 0)
	}
	if len(points) == 0 {
		return emitPressure(g, payload, "no hourly disk-write history in the requested window\n", 0)
	}
	var text strings.Builder
	for _, point := range points {
		fmt.Fprintf(&text, "%s\tstate=%s\twritten=%s\tunscored_gap=%s\tp99=%s\tsamples=%d\n", point.Hour.Local().Format(time.RFC3339), point.State, humanBytes(int64(point.BytesWritten)), humanBytes(int64(point.UnscoredGapBytes)), humanBytes(int64(point.BaselineP99Bytes)), point.SampleCount)
	}
	return emitPressure(g, payload, text.String(), 0)
}

func cmdSessionPressureIOPolicy(g *Flags, args []string) int {
	action := "show"
	if len(args) > 0 {
		action, args = args[0], args[1:]
	}
	if len(args) != 0 {
		return sessionPressureError("io policy "+action+" accepts no arguments", 2)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	runtime, err := loadPressureRuntime(ctx)
	cancel()
	if err != nil {
		return sessionPressureError(err.Error(), 1)
	}
	if action == "show" {
		payload := map[string]any{"ok": true, "action": "io.policy.show", "disk_write_policy": runtime.policy.DiskWrite, "path": runtime.path, "persisted": runtime.persisted}
		return emitPressure(g, payload, fmt.Sprintf("enabled=%v notifications=%v profile=%s interval=%ds retention=%dd\n", runtime.policy.DiskWrite.Enabled, runtime.policy.DiskWrite.NotificationsEnabled, runtime.policy.DiskWrite.Profile, runtime.policy.DiskWrite.SampleIntervalSeconds, runtime.policy.DiskWrite.BaselineRetentionDays), 0)
	}
	if action != "observe" && action != "enable-alerts" && action != "disable" {
		return sessionPressureError("unknown io policy action "+strconv.Quote(action), 2)
	}
	mutation, err := beginPressurePolicyMutation(runtime.dir)
	if err != nil {
		return sessionPressureError(err.Error(), 1)
	}
	defer mutation.Close()
	runtime = mutation.Runtime
	switch action {
	case "observe":
		runtime.policy.DiskWrite.Enabled = true
		runtime.policy.DiskWrite.NotificationsEnabled = false
	case "enable-alerts":
		runtime.policy.DiskWrite.Enabled = true
		runtime.policy.DiskWrite.NotificationsEnabled = true
	case "disable":
		runtime.policy.DiskWrite.Enabled = false
		runtime.policy.DiskWrite.NotificationsEnabled = false
	}
	if err := sessionpressure.SavePolicy(runtime.path, runtime.policy); err != nil {
		return sessionPressureError(err.Error(), 1)
	}
	if action == "disable" {
		if err := restartPressureMonitor(runtime.dir); err != nil {
			return sessionPressureError("disk-write policy saved but resident reload failed: "+err.Error(), 1)
		}
	} else {
		if _, err := ensurePressureMonitor(runtime.dir); err != nil {
			return sessionPressureError("disk-write policy saved but resident activation failed: "+err.Error(), 1)
		}
	}
	payload := map[string]any{"ok": true, "action": "io.policy." + action, "disk_write_policy": runtime.policy.DiskWrite, "path": runtime.path}
	return emitPressure(g, payload, fmt.Sprintf("disk-write policy %s: enabled=%v notifications=%v\n", action, runtime.policy.DiskWrite.Enabled, runtime.policy.DiskWrite.NotificationsEnabled), 0)
}

func cmdSessionPressureIOTrace(g *Flags, args []string) int {
	pid := 0
	duration := 15 * time.Second
	openApp := false
	for index := 0; index < len(args); index++ {
		switch args[index] {
		case "--pid":
			index++
			if index >= len(args) {
				return sessionPressureError("io trace --pid requires an integer", 2)
			}
			value, err := strconv.Atoi(args[index])
			if err != nil || value <= 0 {
				return sessionPressureError("io trace --pid must be a positive integer", 2)
			}
			pid = value
		case "--duration":
			index++
			if index >= len(args) {
				return sessionPressureError("io trace --duration requires a duration", 2)
			}
			value, err := time.ParseDuration(args[index])
			if err != nil || value < 5*time.Second || value > 30*time.Second {
				return sessionPressureError("io trace --duration must be between 5s and 30s", 2)
			}
			duration = value
		case "--open":
			openApp = true
		default:
			return sessionPressureError("unknown io trace argument "+strconv.Quote(args[index]), 2)
		}
	}
	if pid == 0 {
		return sessionPressureError("io trace requires --pid", 2)
	}
	startIdentity, err := sessionpressure.ProcessStartIdentity(pid)
	if err != nil {
		return sessionPressureError(err.Error(), 1)
	}
	query := url.Values{}
	query.Set("pid", strconv.Itoa(pid))
	query.Set("start", startIdentity)
	query.Set("duration", duration.String())
	deepLink := "ndev-pressure://disk-writes/trace?" + query.Encode()
	if openApp {
		if err := exec.Command("/usr/bin/open", deepLink).Run(); err != nil {
			return sessionPressureError("open NDev Pressure trace: "+err.Error(), 1)
		}
	}
	payload := map[string]any{
		"ok": true, "action": "io.trace", "status": "interactive_required", "pid": pid,
		"process_start_identity": startIdentity, "duration_seconds": int(duration / time.Second),
		"deep_link": deepLink, "opened": openApp,
	}
	text := "interactive trace required: " + deepLink + "\n"
	return emitPressure(g, payload, text, 0)
}
