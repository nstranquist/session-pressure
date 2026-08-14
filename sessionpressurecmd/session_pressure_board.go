package sessionpressurecmd

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nstranquist/session-pressure/sessionpressure"
)

// Board is one composite read for UI clients that would otherwise spawn a
// separate `ndev` per contract on every poll tick. The NDev Pressure app was
// measured at 4-5 process launches per board refresh plus one per 2.5s work
// focus poll; each cold start costs 80-230ms of a host the app exists to keep
// unloaded. Every section here reuses the same loaded runtime and the same
// typed structures the individual subcommands emit, so this is a transport
// consolidation and never a second source of truth.
//
// Compact JSON is the default agent/menu-bar projection: admission without the
// nested host snapshot, coverage/launchd summaries, and latest_monitor_summary.
// --full (and selected --include sections that need full shapes) hydrates the
// desktop overview. Sections beyond the always-on trio are opt-in so a
// menu-bar-only client can stay at the cheapest possible read.
const sessionPressureBoardHelp = `Usage: session-pressure --json board [--live] [--full] [--include SECTION[,SECTION...]]

One composite read: host status, weighted work queue, and launch admission in a
single process instead of one child per contract.

  --live            take a bounded live sample instead of the resident one
  --full            hydrate policy, full coverage, full launchd, admission snapshot
  --include LIST    add optional sections, comma separated:
                      doctor       readiness diagnosis and fixes
                      calibration  24h work calibration + interrupt forensics
                      policy       effective policy document and path
                      monitor      LaunchAgent status (full launchd shape)
                      idle         idle-tree cleanup candidates
                      telemetry    recent state transitions and relief actions
                      all          every section above

Default JSON is compact (agent/menu-bar safe). --full restores diagnostic shapes
the desktop overview already requests on rich panes.

Sections that fail are reported as <section>_error and never abort the read: a
partial board is more useful to an operator than none.
`

type sessionPressureBoardSections struct {
	doctor      bool
	calibration bool
	policy      bool
	monitor     bool
	idle        bool
	telemetry   bool
}

func parseSessionPressureBoardSections(value string) (sessionPressureBoardSections, error) {
	sections := sessionPressureBoardSections{}
	for _, raw := range strings.Split(value, ",") {
		switch strings.TrimSpace(raw) {
		case "":
			continue
		case "doctor":
			sections.doctor = true
		case "calibration":
			sections.calibration = true
		case "policy":
			sections.policy = true
		case "monitor":
			sections.monitor = true
		case "idle":
			sections.idle = true
		case "telemetry":
			sections.telemetry = true
		case "all":
			sections = sessionPressureBoardSections{doctor: true, calibration: true, policy: true, monitor: true, idle: true, telemetry: true}
		default:
			return sessionPressureBoardSections{}, fmt.Errorf("unknown board section %s; want doctor, calibration, policy, monitor, idle, telemetry, or all", strconv.Quote(strings.TrimSpace(raw)))
		}
	}
	return sections, nil
}

func cmdSessionPressureBoard(g *Flags, args []string) int {
	live := false
	full := false
	sections := sessionPressureBoardSections{}
	for index := 0; index < len(args); index++ {
		switch args[index] {
		case "help", "--help", "-h":
			fmt.Print(sessionPressureBoardHelp)
			return 0
		case "--live":
			live = true
		case "--full":
			full = true
		case "--include":
			index++
			if index >= len(args) {
				return sessionPressureError("board --include requires a comma-separated section list", 2)
			}
			parsed, err := parseSessionPressureBoardSections(args[index])
			if err != nil {
				return sessionPressureError(err.Error(), 2)
			}
			sections = parsed
		default:
			return sessionPressureError("unknown board argument "+strconv.Quote(args[index]), 2)
		}
	}

	runtimeCtx, runtimeCancel := context.WithTimeout(context.Background(), 5*time.Second)
	runtime, err := loadPressureRuntime(runtimeCtx)
	runtimeCancel()
	if err != nil {
		return sessionPressureError(err.Error(), 1)
	}

	payload := map[string]any{"ok": true, "action": "board", "output_scope": "compact"}
	if full {
		payload["output_scope"] = "full"
	}

	latest, hasLatest := runtime.store.ReadLatest()
	recovery, hasRecovery, recoveryErr := sessionpressure.LoadRecoveryHint(runtime.dir)

	// The independent sections run concurrently. Collapsing five processes into
	// one is only a win if the work inside also overlaps: measured sequentially
	// this verb cost 757ms wall against 370ms for the concurrent fan-out it
	// replaced, trading UI latency for the process saving. Goroutines keep the
	// saving (one Go runtime start instead of five) and give back the latency.
	// Results land in locals; the payload map is assembled on this goroutine
	// only, so there is no shared-map race.
	var wide sync.WaitGroup

	// LaunchAgent status is needed for health assessment whether or not the
	// caller asked for the monitor section itself.
	manager, managerErr := NewLaunchdController(runtime.dir)
	launchd := sessionpressure.LaunchdStatus{}
	if managerErr == nil {
		wide.Add(1)
		go func() {
			defer wide.Done()
			statusCtx, statusCancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer statusCancel()
			launchd = manager.Status(statusCtx)
		}()
	} else {
		payload["monitor_error"] = managerErr.Error()
	}

	var (
		work     sessionpressure.WorkStatus
		workErr  error
		snapshot sessionpressure.Snapshot
		sampleOK bool
		sampleErr,
		diskErr error
		admission              sessionpressure.Admission
		doctorDoc              *sessionpressure.PressureDoctor
		doctorErr              error
		calibration            *sessionpressure.WorkCalibrationReport
		calibrationErr         error
		calibrationDiagnostics sessionpressure.WorkEventDiagnostics
		telemetryEvents        []sessionpressure.TelemetryEvent
		telemetryActions       []sessionpressure.Action
		telemetryErr           error
	)

	wide.Add(1)
	go func() {
		defer wide.Done()
		coordinator := sessionpressure.NewWorkCoordinator(runtime.dir, runtime.policy.WorkLimits)
		workCtx, workCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer workCancel()
		work, workErr = coordinator.Status(workCtx)
	}()
	// Only --live pays for a fresh admission probe. `ConfiguredAgentLaunchAdmission`
	// samples the host on every call and is the single largest cost in this verb
	// (~300ms, more than every other section combined); a resident board has a
	// snapshot already and reusing it also keeps the whole read to one instant
	// instead of showing resident numbers beside a live-probed admission.
	// This is a display projection — a real launch gate must still call
	// ConfiguredAgentLaunchAdmission.
	if live {
		wide.Add(1)
		go func() {
			defer wide.Done()
			admission = AgentLaunchAdmissionCheck(sessionpressure.AgentLaunchNew)
		}()
	}

	if live {
		wide.Add(1)
		go func() {
			defer wide.Done()
			sampleCtx, sampleCancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer sampleCancel()
			snapshot, sampleErr = SampleSnapshot(sampleCtx, runtime.sampler, runtime.policy)
			if sampleErr != nil {
				return
			}
			sampleOK = true
			if !runtime.policy.DiskWrite.Enabled {
				return
			}
			diskCtx, diskCancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer diskCancel()
			var diskReport sessionpressure.DiskWriteReport
			diskReport, diskErr = sessionpressure.LiveDiskWriteReport(diskCtx, runtime.dir, runtime.policy.DiskWrite, time.Second)
			if diskErr != nil {
				return
			}
			if hasLatest && latest.DiskWrite != nil {
				age := time.Since(latest.DiskWrite.CapturedAt)
				if age >= -5*time.Second && age <= 5*time.Minute {
					diskReport = sessionpressure.MergeLiveDiskWriteReport(diskReport, *latest.DiskWrite)
				}
			}
			diskSummary := diskReport.Summary
			snapshot.DiskWrite = &diskSummary
			if full {
				snapshot.DiskWriteWriters = diskReport.Writers
			}
		}()
	}
	if sections.doctor {
		wide.Add(1)
		go func() {
			defer wide.Done()
			doctorCtx, doctorCancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer doctorCancel()
			repoRoot, _ := nicosToolsRepoRoot()
			doc, err := sessionpressure.LoadPressureDoctorFromDir(doctorCtx, runtime.dir, repoRoot)
			if err != nil {
				doctorErr = err
				return
			}
			doctorDoc = &doc
		}()
	}
	if sections.calibration {
		wide.Add(1)
		go func() {
			defer wide.Done()
			since := time.Now().Add(-24 * time.Hour)
			events, diagnostics, err := sessionpressure.NewWorkEventStore(runtime.dir).ReadWithDiagnostics(sessionpressure.WorkEventFilter{Since: since})
			if err != nil {
				calibrationErr = err
				return
			}
			report := sessionpressure.BuildWorkCalibrationReport(events, since, time.Now().UTC())
			calibration = &report
			calibrationDiagnostics = diagnostics
		}()
	}
	if sections.telemetry {
		wide.Add(1)
		go func() {
			defer wide.Done()
			since := time.Now().Add(-24 * time.Hour)
			events, eventsErr := runtime.store.ReadEvents(40, since)
			actions, actionsErr := runtime.store.ReadActions(40, since)
			switch {
			case eventsErr != nil:
				telemetryErr = eventsErr
			case actionsErr != nil:
				telemetryErr = actionsErr
			default:
				telemetryEvents, telemetryActions = events, actions
			}
		}()
	}
	wide.Wait()

	health := sessionpressure.AssessGuardHealth(time.Now().UTC(), runtime.policy, runtime.persisted, launchd, latest, hasLatest).WithOperatorState(hasRecovery, recoveryErr)
	payload["health"] = health
	payload["policy_persisted"] = runtime.persisted
	payload["has_latest_monitor"] = hasLatest
	payload["has_recovery_hint"] = hasRecovery
	if hasRecovery {
		payload["recovery_hint"] = recovery
	}
	if recoveryErr != nil {
		payload["recovery_hint_error"] = recoveryErr.Error()
	}

	repoRoot, repoRootErr := nicosToolsRepoRoot()
	if repoRootErr != nil {
		repoRoot = ""
		payload["coverage_error"] = repoRootErr.Error()
	}
	var identityTrees []sessionpressure.AgentTree
	if hasLatest {
		identityTrees = latest.TopAgentTrees
	}
	coverage := sessionpressure.AssessCoverageDetailed(sessionpressure.CoverageAssessment{
		RepoRoot: repoRoot,
		Policy:   runtime.policy,
		Health:   health,
		Trees:    identityTrees,
		Catalog:  sessionpressure.ActiveAgentIdentityCatalog(),
	})
	// Compact is the agent/menu-bar default. Full coverage lists and launchd
	// paths are diagnostic detail: desktop overview requests --full, and
	// --include monitor still hydrates full launchd for the monitor pane.
	if full {
		payload["coverage"] = coverage
		payload["launchd"] = launchd
	} else {
		payload["coverage_summary"] = compactPressureCoverage(coverage)
		if sections.monitor {
			payload["launchd"] = launchd
		} else {
			payload["launchd_summary"] = compactPressureLaunchdStatus(launchd)
		}
	}

	snapshotText := ""
	// Whichever snapshot the board ends up holding is reused by the idle
	// section instead of paying for a second host sample.
	boardSnapshot := latest
	haveBoardSnapshot := hasLatest
	snapshotSource := "resident"
	if live {
		if sampleOK {
			boardSnapshot = snapshot
			haveBoardSnapshot = true
			snapshotSource = "live"
			if diskErr != nil {
				payload["disk_write_sample_error"] = diskErr.Error()
			}
			if full {
				payload["snapshot"] = snapshot
			} else {
				payload["snapshot_summary"] = compactPressureStatusSnapshot(snapshot)
			}
			snapshotText = snapshot.Summary()
		} else {
			payload["sample_error"] = sampleErr.Error()
			payload["ok"] = false
			snapshotText = "live_sample_error=" + strconv.Quote(sampleErr.Error())
		}
	} else if hasLatest {
		snapshotText = latest.Summary()
	}
	if hasLatest {
		if full {
			payload["latest_monitor"] = latest
		} else {
			payload["latest_monitor_summary"] = compactPressureStatusSnapshot(latest)
		}
	}

	if workErr != nil {
		payload["work_error"] = workErr.Error()
		payload["ok"] = false
	} else {
		payload["work"] = work
	}
	if !live {
		// Derived from the same snapshot the rest of this read reports, and
		// only meaningful once that snapshot and the queue both loaded.
		if haveBoardSnapshot && workErr == nil {
			admission = sessionpressure.AgentLaunchAdmissionFromSnapshot(
				boardSnapshot, runtime.policy, runtime.persisted, work,
				sessionpressure.AgentLaunchNew, snapshotSource,
			)
		} else {
			admission = sessionpressure.Admission{
				Allowed: true, Level: sessionpressure.LevelNormal, Source: "unavailable",
				Warning: "no resident snapshot or queue status; admission failed open for display",
			}
		}
	}
	// Admission decisions are tiny; the nested host snapshot is the bulk.
	// Agents and the menu bar need allowed/level/work_queue, not a second copy
	// of latest_monitor. Desktop overview requests --full for the full shape.
	if full {
		payload["admission"] = admission
	} else {
		payload["admission"] = compactPressureAdmission(admission)
	}

	if sections.policy || full {
		payload["policy"] = runtime.policy
		payload["policy_path"] = runtime.path
	}
	if sections.doctor {
		if doctorErr != nil {
			payload["doctor_error"] = doctorErr.Error()
		} else if doctorDoc != nil {
			payload["doctor"] = *doctorDoc
		}
	}
	if sections.calibration {
		if calibrationErr != nil {
			payload["calibration_error"] = calibrationErr.Error()
		} else if calibration != nil {
			if full {
				payload["calibration"] = *calibration
			} else {
				payload["calibration"] = compactPressureWorkCalibration(*calibration)
			}
			addWorkEventDiagnostics(payload, calibrationDiagnostics)
		}
	}
	if sections.idle {
		if !haveBoardSnapshot {
			payload["idle_error"] = "no host snapshot is available; pass --live or wait for a resident sample"
		} else {
			inventory, idleErr := sessionpressure.InspectIdleTrees(boardSnapshot, sessionpressure.DefaultIdleCriteria(), os.Getpid())
			if idleErr != nil {
				payload["idle_error"] = idleErr.Error()
			} else {
				payload["inventory"] = inventory
				// Say which snapshot the candidates came from. `idle --apply`
				// re-samples and revalidates; a board read never authorizes a
				// signal, and must not read as though it had.
				payload["idle_source"] = snapshotSource
			}
		}
	}
	if sections.telemetry {
		if telemetryErr != nil {
			payload["telemetry_error"] = telemetryErr.Error()
		} else {
			payload["events"] = telemetryEvents
			payload["actions"] = telemetryActions
		}
	}

	text := snapshotText
	if workErr == nil {
		text += fmt.Sprintf(" work=%d/%d queue=%d holds=%d", work.Used, work.Capacity, work.QueueDepth, work.AdmissionHoldCount)
		if work.OverrideQueueDepth > 0 {
			text += fmt.Sprintf(" pinned=%d", work.OverrideQueueDepth)
		}
	}
	text += fmt.Sprintf(" admission=%v monitor_healthy=%v protection=%s\n", admission.Allowed, health.MonitorHealthy, health.ProtectionMode)
	exit := 0
	if ok, _ := payload["ok"].(bool); !ok {
		exit = 1
	}
	return emitPressure(g, payload, text, exit)
}
