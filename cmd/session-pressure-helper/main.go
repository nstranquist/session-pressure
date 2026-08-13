// ndev-session-pressure is the deliberately tiny resident half of
// `ndev session pressure`. The full ndev binary owns policy and lifecycle UX;
// this helper imports only the sampler/monitor packages so idle RSS stays low.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"
	"time"

	"github.com/nstranquist/session-pressure/internal/notifyinbox"
	"github.com/nstranquist/session-pressure/internal/sessionpressure"
)

type cleanupBridgePolicy struct {
	Enabled                 bool                  `json:"enabled"`
	Enforce                 bool                  `json:"enforce"`
	AutoGraduateProcessOnly bool                  `json:"auto_graduate_process_only"`
	AutoGraduateNative      bool                  `json:"auto_graduate_native_providers"`
	BrowserEnabled          bool                  `json:"browser_enabled"`
	DevSessionEnabled       bool                  `json:"dev_session_enabled"`
	DockerWorkspaceEnabled  bool                  `json:"docker_workspace_enabled"`
	ObservationStartedAt    time.Time             `json:"observation_started_at"`
	TriggerLevel            sessionpressure.Level `json:"trigger_level"`
	SustainSamples          int                   `json:"sustain_samples"`
}

type commandResourceCleaner struct {
	dir            string
	controlBinary  string
	controlDigest  string
	pressurePolicy sessionpressure.Policy
	timeout        time.Duration
	runControl     func(context.Context, string, []byte) ([]byte, error)
}

const cleanupBridgeTimeout = 30 * time.Second
const cleanupBridgeObservationWindow = 7 * 24 * time.Hour

func (policy cleanupBridgePolicy) autoGraduationDue(now time.Time) bool {
	if policy.ObservationStartedAt.IsZero() {
		return false
	}
	if !policy.Enforce {
		return policy.AutoGraduateProcessOnly && !now.Before(policy.ObservationStartedAt.Add(cleanupBridgeObservationWindow))
	}
	if !policy.AutoGraduateNative {
		return false
	}
	switch {
	case !policy.BrowserEnabled:
		return !now.Before(policy.ObservationStartedAt.Add(2 * cleanupBridgeObservationWindow))
	case !policy.DevSessionEnabled:
		return !now.Before(policy.ObservationStartedAt.Add(3 * cleanupBridgeObservationWindow))
	case !policy.DockerWorkspaceEnabled:
		return !now.Before(policy.ObservationStartedAt.Add(4 * cleanupBridgeObservationWindow))
	default:
		return false
	}
}

func (cleaner commandResourceCleaner) MaybeRelieve(ctx context.Context, snapshot sessionpressure.Snapshot) (sessionpressure.ResourceCleanupResult, error) {
	var result sessionpressure.ResourceCleanupResult
	body, err := os.ReadFile(filepath.Join(cleaner.dir, "cleanup-policy.json"))
	if os.IsNotExist(err) {
		return result, nil
	}
	if err != nil {
		return result, err
	}
	var policy cleanupBridgePolicy
	if err := json.Unmarshal(body, &policy); err != nil {
		return result, fmt.Errorf("decode cleanup bridge policy: %w", err)
	}
	if !policy.Enabled {
		return result, nil
	}
	if policy.Enforce && policy.AutoGraduateProcessOnly {
		return result, fmt.Errorf("cleanup bridge policy cannot enforce while auto-graduation remains scheduled")
	}
	graduationDue := policy.autoGraduationDue(time.Now().UTC())
	if !policy.Enforce && !graduationDue {
		return result, nil
	}
	if graduationDue && (snapshot.GuardRole != "resident" || !snapshot.GuardBudgetOK || !snapshot.GuardBaselineProven || !snapshot.ProcessInventoryFresh) {
		return result, nil
	}
	if policy.TriggerLevel != sessionpressure.LevelRed && policy.TriggerLevel != sessionpressure.LevelCritical {
		return result, fmt.Errorf("cleanup bridge trigger level %q is invalid", policy.TriggerLevel)
	}
	if policy.Enforce && !graduationDue {
		if policy.SustainSamples < 2 || snapshot.MemoryConsecutiveSamples < policy.SustainSamples {
			return result, nil
		}
		memoryLevel := sessionpressure.EvaluateMemoryPressure(snapshot, cleaner.pressurePolicy).Level
		if !memoryLevel.AtLeast(policy.TriggerLevel) {
			return result, nil
		}
	}
	if cleaner.controlBinary == "" {
		return result, fmt.Errorf("%s is not configured", sessionpressure.ControlBinaryEnv)
	}
	if err := sessionpressure.VerifyControlBinary(cleaner.controlBinary, cleaner.controlDigest); err != nil {
		return result, fmt.Errorf("verify cleanup control binary: %w", err)
	}
	payload, err := json.Marshal(snapshot)
	if err != nil {
		return result, err
	}
	timeout := cleaner.timeout
	if timeout <= 0 {
		timeout = cleanupBridgeTimeout
	}
	commandCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	runControl := cleaner.runControl
	controlMaxRSSMB := float64(0)
	if runControl == nil {
		runControl = func(runCtx context.Context, binary string, input []byte) ([]byte, error) {
			command := exec.CommandContext(runCtx, binary, "--json", "session", "pressure", "cleanup", "enforce")
			command.Stdin = bytes.NewReader(input)
			output, commandErr := command.CombinedOutput()
			controlMaxRSSMB = processStateMaxRSSMB(command.ProcessState)
			return output, commandErr
		}
	}
	controlStarted := time.Now()
	output, err := runControl(commandCtx, cleaner.controlBinary, payload)
	result.ControlExecuted = true
	result.ControlDurationMS = float64(time.Since(controlStarted).Microseconds()) / 1000
	result.ControlMaxRSSMB = controlMaxRSSMB
	if err != nil {
		if errors.Is(commandCtx.Err(), context.DeadlineExceeded) {
			return result, fmt.Errorf("cleanup bridge exceeded %s deadline: %w", timeout, context.DeadlineExceeded)
		}
		return result, fmt.Errorf("cleanup bridge: %w: %s", err, bytes.TrimSpace(output))
	}
	var response struct {
		Result sessionpressure.ResourceCleanupResult `json:"result"`
	}
	if err := json.Unmarshal(output, &response); err != nil {
		return result, fmt.Errorf("decode cleanup bridge result: %w", err)
	}
	response.Result.ControlExecuted = result.ControlExecuted
	response.Result.ControlDurationMS = result.ControlDurationMS
	response.Result.ControlMaxRSSMB = result.ControlMaxRSSMB
	return response.Result, nil
}

func processStateMaxRSSMB(state *os.ProcessState) float64 {
	if state == nil {
		return 0
	}
	usage, ok := state.SysUsage().(*syscall.Rusage)
	if !ok || usage.Maxrss <= 0 {
		return 0
	}
	maxRSS := float64(usage.Maxrss)
	if runtime.GOOS != "darwin" {
		maxRSS *= 1024
	}
	return maxRSS / (1024 * 1024)
}

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "work-run":
			os.Exit(runWorkCommand(os.Args[2:]))
		case "work-batch":
			os.Exit(runWorkBatchCommand(os.Args[2:]))
		case sessionpressure.WorkChildMode:
			if err := sessionpressure.RunGatedWorkChild(os.Args[2:]); err != nil {
				fmt.Fprintln(os.Stderr, "ndev session pressure: gated child:", err)
				os.Exit(125)
			}
			return
		case sessionpressure.WorkBatchChildMode:
			os.Exit(sessionpressure.RunWorkBatchChild(os.Args[2:], os.Stdout, os.Stderr))
		}
		fatal(fmt.Errorf("unknown helper mode %q", os.Args[1]))
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx); err != nil {
		fatal(err)
	}
}

func runWorkBatchCommand(args []string) int {
	options, err := sessionpressure.ParseWorkBatchArgs(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ndev session pressure:", err)
		return 2
	}
	manifest, err := sessionpressure.ReadWorkBatchManifest(options.File, os.Stdin)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ndev session pressure:", err)
		return 2
	}
	dir, err := sessionpressure.DataDir()
	if err != nil {
		fmt.Fprintln(os.Stderr, "ndev session pressure:", err)
		return 1
	}
	policy, persisted, err := sessionpressure.LoadPolicy(sessionpressure.PolicyPath(dir), 0)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ndev session pressure:", err)
		return 1
	}
	if !persisted {
		fmt.Fprintln(os.Stderr, "ndev session pressure: policy is not initialized; run ndev session pressure policy init")
		return 1
	}
	options.RetentionDays = policy.RetentionDays
	code, err := sessionpressure.RunWorkBatch(
		sessionpressure.NewWorkCoordinator(dir, policy.WorkLimits),
		options,
		manifest,
		func() sessionpressure.Admission {
			return sessionpressure.ConfiguredWorkAdmission(context.Background(), manifest.Class)
		},
		sessionpressure.WorkRunStreams{Stdin: os.Stdin, Stdout: os.Stdout, Stderr: os.Stderr},
	)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ndev session pressure:", err)
	}
	return code
}

func runWorkCommand(args []string) int {
	options, err := sessionpressure.ParseWorkRunArgs(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ndev session pressure:", err)
		return 2
	}
	dir, err := sessionpressure.DataDir()
	if err != nil {
		fmt.Fprintln(os.Stderr, "ndev session pressure:", err)
		return 1
	}
	policy, persisted, err := sessionpressure.LoadPolicy(sessionpressure.PolicyPath(dir), 0)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ndev session pressure:", err)
		return 1
	}
	if !persisted {
		fmt.Fprintln(os.Stderr, "ndev session pressure: policy is not initialized; run ndev session pressure policy init")
		return 1
	}
	options.RetentionDays = policy.RetentionDays
	code, err := sessionpressure.RunWorkCommandWithExpressReuse(
		sessionpressure.NewWorkCoordinator(dir, policy.WorkLimits),
		options,
		func() sessionpressure.Admission {
			return sessionpressure.ConfiguredWorkAdmission(context.Background(), options.Class)
		},
		sessionpressure.WorkAdmissionRetryInterval,
		sessionpressure.WorkRunStreams{Stdin: os.Stdin, Stdout: os.Stdout, Stderr: os.Stderr},
	)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ndev session pressure:", err)
	}
	return code
}

func run(ctx context.Context) error {
	dir, err := sessionpressure.DataDir()
	if err != nil {
		return err
	}
	releaseAuthority, err := sessionpressure.AcquireResidentAuthority(dir)
	if err != nil {
		return err
	}
	defer releaseAuthority()
	sampler := sessionpressure.NewResidentSampler()
	probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	physicalMB, err := sampler.PhysicalMemoryMB(probeCtx)
	cancel()
	if err != nil {
		return err
	}
	policy, persisted, err := sessionpressure.LoadPolicy(sessionpressure.PolicyPath(dir), physicalMB)
	if err != nil {
		return err
	}
	if !persisted {
		return fmt.Errorf("policy is not initialized; run ndev session pressure policy init")
	}
	sampler.WithWorkCoordinator(sessionpressure.NewWorkCoordinator(dir, policy.WorkLimits))
	lifecycle, recoveryHint, err := sessionpressure.StartLifecycle(dir, os.Getpid(), time.Now)
	if err != nil {
		return err
	}
	if recoveryHint != nil {
		if err := notifyRecoveryHint(*recoveryHint); err != nil {
			// Recovery evidence remains durable in recovery-hint.json. Notification
			// delivery is best-effort and must not prevent the pressure authority
			// from protecting a host during login or a crash-loop restart.
			fmt.Fprintln(os.Stderr, "ndev-session-pressure: queue recovery notification:", err)
		}
	}
	defer func() {
		if err := lifecycle.MarkClean(); err != nil {
			fmt.Fprintln(os.Stderr, "ndev-session-pressure: mark clean shutdown:", err)
		}
	}()
	store := sessionpressure.NewTelemetryStore(dir)
	startedAt := time.Now().UTC()
	if err := store.AppendEvent(sessionpressure.TelemetryEvent{Timestamp: startedAt, Event: "resident_started"}); err != nil {
		fmt.Fprintln(os.Stderr, "ndev-session-pressure: persist resident start:", err)
	}
	starts24h := 0
	if events, err := store.ReadEvents(10_000, startedAt.Add(-24*time.Hour)); err == nil {
		for _, event := range events {
			if event.Event == "resident_started" {
				starts24h++
			}
		}
	}
	defer func() {
		if err := store.AppendEvent(sessionpressure.TelemetryEvent{Timestamp: time.Now().UTC(), Event: "resident_stopped"}); err != nil {
			fmt.Fprintln(os.Stderr, "ndev-session-pressure: persist resident stop:", err)
		}
	}()
	monitor := sessionpressure.NewMonitor(sampler, store, policy)
	monitor.DiskObserver = sessionpressure.NewDiskWriteObserver(dir, policy.DiskWrite)
	monitor.DiskNotifier = notifyDiskWriteAlert
	monitor.ResidentStarts24h = starts24h
	monitor.Cleaner = commandResourceCleaner{
		dir: dir, controlBinary: os.Getenv(sessionpressure.ControlBinaryEnv),
		controlDigest: os.Getenv(sessionpressure.ControlBinaryDigestEnv), pressurePolicy: policy,
	}
	return monitor.Run(ctx)
}

func notifyRecoveryHint(hint sessionpressure.RecoveryHint) error {
	return notifyinbox.Append(notifyinbox.Path(os.Getenv("HOME"), ""), notifyinbox.Toast{
		Title:          "Session recovery may be available",
		Body:           "The previous pressure monitor did not shut down cleanly. Open the recovery hint to inspect resumable sessions.",
		Severity:       "warning",
		Source:         "ndev-session-pressure",
		Seconds:        "12",
		ExecuteCommand: hint.RecoveryCommand,
	}, time.Now)
}

func notifyDiskWriteAlert(alert sessionpressure.DiskWriteAlert) error {
	body := fmt.Sprintf("Sustained internal SSD writes are %.1fx the learned baseline over 15 minutes.", alert.Ratio)
	if alert.TopWriter != "" {
		body += " Likely contributor: " + alert.TopWriter + "."
	}
	return notifyinbox.Append(notifyinbox.Path(os.Getenv("HOME"), ""), notifyinbox.Toast{
		Title:          "Unusual disk writes detected",
		Body:           body,
		Severity:       "warning",
		Source:         "ndev-session-pressure",
		Seconds:        "12",
		ExecuteCommand: "/usr/bin/open ndev-pressure://disk-writes",
	}, time.Now)
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "ndev-session-pressure:", err)
	os.Exit(1)
}
