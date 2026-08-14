package sessionpressurecmd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/nstranquist/session-pressure/sessionpressure"
	"github.com/nstranquist/session-pressure/pkg/processtree"
)

const (
	// A self-test must exercise the installed weighted-work path, but an
	// immediately full queue is not a resident failure. Keep the default probe
	// bounded so routine health checks wait briefly for a legitimate lease to
	// drain instead of turning queue contention into a false red result. Callers
	// that need an immediate admission assertion can still pass --wait 0.
	pressureSelfTestDefaultWait time.Duration = 30 * time.Second
	pressureSelfTestErrorLimit                = 1024
)

var errPressureSelfTestUsage = errors.New("self-test accepts only --wait with 0 or a positive duration such as 10m, plus optional --full")

type pressureSelfTestOptions struct {
	wait time.Duration
	full bool
}

type pressureSelfTestOutput struct {
	buffer bytes.Buffer
	limit  int
}

func (output *pressureSelfTestOutput) Write(body []byte) (int, error) {
	written := len(body)
	remaining := output.limit - output.buffer.Len()
	if remaining > 0 {
		_, _ = output.buffer.Write(body[:min(len(body), remaining)])
	}
	return written, nil
}

func (output *pressureSelfTestOutput) String() string {
	return strings.TrimSpace(output.buffer.String())
}

var runPressureSelfTestProbe = func(ctx context.Context, helper string, wait time.Duration) error {
	waitValue := "0"
	if wait > 0 {
		waitValue = wait.String()
	}
	command := processtree.CommandContext(ctx, helper,
		"work-run", "--class", "browser", "--wait", waitValue,
		"--", "/usr/bin/true",
	)
	command.Stdout = io.Discard
	stderr := &pressureSelfTestOutput{limit: pressureSelfTestErrorLimit}
	command.Stderr = stderr
	if err := command.Run(); err != nil {
		if detail := stderr.String(); detail != "" {
			return fmt.Errorf("immutable helper work probe: %w: %s", err, detail)
		}
		return fmt.Errorf("immutable helper work probe: %w", err)
	}
	return nil
}

type pressureSelfTestState struct {
	Launchd     sessionpressure.LaunchdStatus
	Latest      sessionpressure.Snapshot
	HasLatest   bool
	Recovery    sessionpressure.RecoveryHint
	HasRecovery bool
	RecoveryErr error
	Health      sessionpressure.GuardHealth
	Work        sessionpressure.WorkStatus
}

func parsePressureSelfTestArgs(args []string) (pressureSelfTestOptions, error) {
	options := pressureSelfTestOptions{wait: pressureSelfTestDefaultWait}
	for len(args) > 0 {
		switch args[0] {
		case "--full":
			if options.full {
				return pressureSelfTestOptions{}, errPressureSelfTestUsage
			}
			options.full = true
			args = args[1:]
		case "--wait":
			if len(args) < 2 {
				return pressureSelfTestOptions{}, errPressureSelfTestUsage
			}
			if args[1] == "0" {
				options.wait = 0
			} else {
				wait, err := time.ParseDuration(args[1])
				if err != nil || wait <= 0 {
					return pressureSelfTestOptions{}, errPressureSelfTestUsage
				}
				options.wait = wait
			}
			args = args[2:]
		default:
			return pressureSelfTestOptions{}, errPressureSelfTestUsage
		}
	}
	return options, nil
}

func cmdSessionPressureSelfTest(g *Flags, args []string) int {
	options, err := parsePressureSelfTestArgs(args)
	if err != nil {
		return sessionPressureError(err.Error(), 2)
	}
	started := time.Now()
	runtime, err := loadPressureSelfTestRuntime()
	if err != nil {
		return sessionPressureError(err.Error(), 1)
	}
	manager, err := NewLaunchdController(runtime.dir)
	if err != nil {
		return sessionPressureError(err.Error(), 1)
	}
	coordinator := sessionpressure.NewWorkCoordinator(runtime.dir, runtime.policy.WorkLimits)
	state, stateErr := readPressureSelfTestState(runtime, manager, coordinator)
	payload := pressureSelfTestPayload(runtime, state, options.full)
	fail := func(message string) int {
		message = boundedPressureSelfTestError(message)
		payload["ok"] = false
		payload["sample_error"] = message
		return emitPressure(g, payload, "session pressure self-test: FAIL: "+message+"\n", 1)
	}
	if stateErr != nil {
		return fail(stateErr.Error())
	}
	if state.RecoveryErr != nil {
		return fail("read recovery state: " + state.RecoveryErr.Error())
	}
	if state.HasRecovery {
		return fail("an unclean-shutdown recovery hint is pending")
	}
	if !state.Health.OperatorReady {
		return fail("resident is not operator ready: " + strings.Join(state.Health.OperatorReasons, "; "))
	}

	helper, err := resolvePressureWorkHelper()
	if err != nil {
		return fail("verify installed helper: " + err.Error())
	}
	probeTimeout := 30 * time.Second
	if options.wait > 0 {
		probeTimeout += options.wait
	}
	probeCtx, probeCancel := context.WithTimeout(context.Background(), probeTimeout)
	err = runPressureSelfTestProbe(probeCtx, helper, options.wait)
	probeCancel()
	if err != nil {
		return fail(err.Error())
	}

	afterRuntime, err := loadPressureSelfTestRuntime()
	if err != nil {
		return fail("reload policy after work probe: " + err.Error())
	}
	if afterRuntime.persisted != runtime.persisted || afterRuntime.policy != runtime.policy {
		return fail("pressure policy changed during the work probe")
	}
	afterCoordinator := sessionpressure.NewWorkCoordinator(afterRuntime.dir, afterRuntime.policy.WorkLimits)
	after, afterErr := readPressureSelfTestState(afterRuntime, manager, afterCoordinator)
	payload = pressureSelfTestPayload(afterRuntime, after, options.full)
	if afterErr != nil {
		return fail(afterErr.Error())
	}
	if after.RecoveryErr != nil {
		return fail("read recovery state after work probe: " + after.RecoveryErr.Error())
	}
	if after.HasRecovery {
		return fail("work probe created an unclean-shutdown recovery hint")
	}
	if after.Launchd.PID != state.Launchd.PID || after.Launchd.ArtifactSHA256 != state.Launchd.ArtifactSHA256 {
		return fail("resident PID or artifact changed during the work probe")
	}
	if !after.Health.OperatorReady {
		return fail("resident lost operator readiness during the work probe: " + strings.Join(after.Health.OperatorReasons, "; "))
	}

	payload["ok"] = true
	elapsed := time.Since(started).Round(time.Millisecond)
	text := fmt.Sprintf(
		"session pressure self-test: PASS pid=%d artifact=%s work=browser:true elapsed=%s capacity=%d/%d\n",
		after.Launchd.PID, shortPressureDigest(after.Launchd.ArtifactSHA256), elapsed,
		after.Work.Available, after.Work.Capacity,
	)
	return emitPressure(g, payload, text, 0)
}

func loadPressureSelfTestRuntime() (pressureRuntime, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return loadPressureRuntime(ctx)
}

func readPressureSelfTestState(runtime pressureRuntime, manager LaunchdController, coordinator *sessionpressure.WorkCoordinator) (pressureSelfTestState, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	launchd := manager.Status(ctx)
	cancel()
	latest, hasLatest := runtime.store.ReadLatest()
	recovery, hasRecovery, recoveryErr := sessionpressure.LoadRecoveryHint(runtime.dir)
	health := sessionpressure.AssessGuardHealth(time.Now().UTC(), runtime.policy, runtime.persisted, launchd, latest, hasLatest).WithOperatorState(hasRecovery, recoveryErr)
	workCtx, workCancel := context.WithTimeout(context.Background(), 5*time.Second)
	work, workErr := coordinator.Status(workCtx)
	workCancel()
	state := pressureSelfTestState{
		Launchd: launchd, Latest: latest, HasLatest: hasLatest,
		Recovery: recovery, HasRecovery: hasRecovery, RecoveryErr: recoveryErr,
		Health: health, Work: work,
	}
	if workErr != nil {
		return state, fmt.Errorf("read weighted work state: %w", workErr)
	}
	return state, nil
}

func pressureSelfTestPayload(runtime pressureRuntime, state pressureSelfTestState, full bool) map[string]any {
	payload := map[string]any{
		"ok": false, "action": "self-test", "output_scope": "compact",
		"policy_persisted": runtime.persisted,
		"launchd_summary":  compactPressureLaunchdStatus(state.Launchd), "health": state.Health,
		"has_latest_monitor": state.HasLatest, "has_recovery_hint": state.HasRecovery,
		"work": state.Work,
	}
	if state.HasLatest {
		payload["latest_monitor_summary"] = compactPressureStatusSnapshot(state.Latest)
	}
	if full {
		payload["output_scope"] = "full"
		payload["policy"] = runtime.policy
		payload["policy_path"] = runtime.path
		payload["launchd"] = state.Launchd
		delete(payload, "launchd_summary")
		if state.HasLatest {
			payload["latest_monitor"] = state.Latest
			delete(payload, "latest_monitor_summary")
		}
	}
	if state.HasRecovery {
		payload["recovery_hint"] = state.Recovery
	}
	if state.RecoveryErr != nil {
		payload["recovery_hint_error"] = state.RecoveryErr.Error()
	}
	return payload
}

func boundedPressureSelfTestError(message string) string {
	message = strings.TrimSpace(message)
	if len(message) <= pressureSelfTestErrorLimit {
		return message
	}
	return message[:pressureSelfTestErrorLimit]
}

func shortPressureDigest(digest string) string {
	if len(digest) <= 12 {
		return digest
	}
	return digest[:12]
}
