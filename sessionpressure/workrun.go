package sessionpressure

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// WorkAdmissionRetryInterval matches the resident pressure cadence without
// multiplying live host probes across queued agent sessions.
const WorkAdmissionRetryInterval = 15 * time.Second
const WorkCPUConfirmationInterval = 2 * time.Second
const WorkStorageProgressBytes = 256 << 20

var ErrWorkStorageGraceExceeded = errors.New("storage pressure work-wait grace exceeded")

const (
	// WorkChildMode is an internal mode of the installed pressure helper. The
	// child blocks on fd 3 and cannot exec requested work until its parent has
	// durably rebound the weighted lease to the child's PID.
	WorkChildMode      = "work-exec-gated"
	workGateFD         = uintptr(3)
	workGateToken      = byte(0x47)
	workChildErrorCode = 125
)

type WorkRunOptions struct {
	Class               WorkClass
	RequestedClass      WorkClass
	Exclusive           bool
	Wait                time.Duration
	Progress            WorkProgressMode
	RetentionDays       int
	Command             []string
	BatchStepCount      int
	BatchCompletedSteps int
	CompletedSteps      func() int
	ReuseStatus         string
	ReuseDecision       ExpressReuseDecision
	ReuseRefusalReason  ExpressReuseRefusalReason
	ReuseKeyDigest      string
	ReceiptDigest       string
	SingleflightWaitMS  int64
	NoReuse             bool
	Priority            bool
	reusePreparer       func() ExpressReuseRefusalReason
	reuseFinalizer      func() ExpressReuseRefusalReason
}

type WorkProgressMode string

const (
	WorkProgressHuman WorkProgressMode = "human"
	WorkProgressJSONL WorkProgressMode = "jsonl"
	WorkProgressQuiet WorkProgressMode = "quiet"
)

func ParseWorkProgressMode(value string) (WorkProgressMode, error) {
	mode := WorkProgressMode(strings.ToLower(strings.TrimSpace(value)))
	switch mode {
	case WorkProgressHuman, WorkProgressJSONL, WorkProgressQuiet:
		return mode, nil
	default:
		return "", fmt.Errorf("unknown work progress mode %q; want human, jsonl, or quiet", value)
	}
}

type WorkProgress struct {
	SchemaVersion              int         `json:"schema_version"`
	Timestamp                  time.Time   `json:"timestamp"`
	Stage                      string      `json:"stage"`
	OperationID                string      `json:"operation_id"`
	Class                      WorkClass   `json:"class"`
	Weight                     int         `json:"weight"`
	ElapsedMS                  int64       `json:"elapsed_ms"`
	Blocker                    WorkBlocker `json:"blocker,omitempty"`
	QueuePosition              int         `json:"queue_position,omitempty"`
	QueueDepth                 int         `json:"queue_depth,omitempty"`
	Used                       int         `json:"used,omitempty"`
	Capacity                   int         `json:"capacity,omitempty"`
	Available                  int         `json:"available,omitempty"`
	PressureLevel              Level       `json:"pressure_level,omitempty"`
	PressureDimension          string      `json:"pressure_dimension,omitempty"`
	Reason                     string      `json:"reason,omitempty"`
	NextCheckSeconds           float64     `json:"next_check_seconds,omitempty"`
	SchedulingPolicy           string      `json:"scheduling_policy,omitempty"`
	SelectorSchemaVersion      int         `json:"selector_schema_version,omitempty"`
	SelectedOperationID        string      `json:"selected_operation_id,omitempty"`
	ProtectedOperationID       string      `json:"protected_operation_id,omitempty"`
	DecisionReason             string      `json:"decision_reason,omitempty"`
	BypassedCount              int         `json:"bypassed_count,omitempty"`
	CandidateSchedulingPolicy  string      `json:"candidate_scheduling_policy,omitempty"`
	ShadowSelectedOperationID  string      `json:"shadow_selected_operation_id,omitempty"`
	ShadowProtectedOperationID string      `json:"shadow_protected_operation_id,omitempty"`
	ShadowDecisionReason       string      `json:"shadow_decision_reason,omitempty"`
}

type WorkRunStreams struct {
	Stdin          io.Reader
	Stdout         io.Writer
	Stderr         io.Writer
	Signals        <-chan os.Signal
	CommandFactory WorkCommandFactory
}

// WorkCommandFactory returns the process that will become the requested work
// plus an optional parent gate. Production uses a self-execing helper and a
// pipe; tests may inject a deterministic process while separately proving the
// gate contract.
type WorkCommandFactory func(target string, args []string) (*exec.Cmd, io.WriteCloser, error)

func newGatedWorkCommand(target string, args []string) (*exec.Cmd, io.WriteCloser, error) {
	resolved, err := exec.LookPath(target)
	if err != nil {
		return nil, nil, err
	}
	executable, err := os.Executable()
	if err != nil {
		return nil, nil, fmt.Errorf("resolve pressure work helper: %w", err)
	}
	gateRead, gateWrite, err := os.Pipe()
	if err != nil {
		return nil, nil, fmt.Errorf("create pressure work lease gate: %w", err)
	}
	childArgs := []string{WorkChildMode, resolved}
	childArgs = append(childArgs, args...)
	command := exec.Command(executable, childArgs...)
	command.ExtraFiles = []*os.File{gateRead}
	return command, gateWrite, nil
}

// RunGatedWorkChild waits for proof that its parent durably bound the lease,
// then atomically replaces the helper with the requested command. EOF means
// the parent died before binding, so requested work is never started.
func RunGatedWorkChild(args []string) error {
	gate := os.NewFile(workGateFD, "ndev-pressure-work-gate")
	if gate == nil {
		return errors.New("gated work child did not receive its lease gate")
	}
	return awaitWorkGate(args, gate, syscall.Exec)
}

func awaitWorkGate(args []string, gate io.ReadCloser, replace func(string, []string, []string) error) (resultErr error) {
	gateClosed := false
	if gate != nil {
		defer func() {
			if gateClosed {
				return
			}
			if closeErr := gate.Close(); closeErr != nil {
				resultErr = errors.Join(resultErr, fmt.Errorf("close durable lease gate: %w", closeErr))
			}
		}()
	}
	if len(args) == 0 {
		return errors.New("gated work child requires a command")
	}
	if gate == nil || replace == nil {
		return errors.New("gated work child requires its parent gate and exec boundary")
	}
	var token [1]byte
	if _, err := io.ReadFull(gate, token[:]); err != nil {
		return fmt.Errorf("parent exited before durable lease binding: %w", err)
	}
	if token[0] != workGateToken {
		return errors.New("invalid durable lease gate token")
	}
	// syscall.Exec does not run defers. Close the inherited gate explicitly so
	// requested work cannot retain the helper's private control descriptor.
	if err := gate.Close(); err != nil {
		return fmt.Errorf("close durable lease gate before exec: %w", err)
	}
	gateClosed = true
	target := args[0]
	argv := append([]string{target}, args[1:]...)
	return replace(target, argv, os.Environ())
}

func ParseWorkRunArgs(args []string) (WorkRunOptions, error) {
	options := WorkRunOptions{Wait: 10 * time.Minute, Progress: WorkProgressHuman, RetentionDays: 14}
	classSet := false
	for len(args) > 0 {
		if args[0] == "--" {
			options.Command = append([]string(nil), args[1:]...)
			break
		}
		switch args[0] {
		case "--class":
			if len(args) < 2 {
				return WorkRunOptions{}, errors.New("--class requires test, build, express-test, express-build, install, emulator, browser, heavy, benchmark, benchmark-exclusive, or reclaim")
			}
			class, err := ParseWorkClass(args[1])
			if err != nil {
				return WorkRunOptions{}, err
			}
			options.Class = class
			classSet = true
			args = args[2:]
		case "--exclusive":
			// Opt-in clean-host mode for benchmarks. Capacity-sized exclusive
			// weight freezes residual capacity until the lease drains.
			options.Exclusive = true
			args = args[1:]
		case "--no-reuse":
			options.NoReuse = true
			args = args[1:]
		case "--priority":
			options.Priority = true
			args = args[1:]
		case "--wait":
			if len(args) < 2 {
				return WorkRunOptions{}, errors.New("--wait requires a duration or 0")
			}
			if args[1] == "0" {
				options.Wait = 0
			} else {
				duration, err := time.ParseDuration(args[1])
				if err != nil || duration <= 0 {
					return WorkRunOptions{}, errors.New("--wait must be 0 or a positive duration such as 10m")
				}
				options.Wait = duration
			}
			args = args[2:]
		case "--progress":
			if len(args) < 2 {
				return WorkRunOptions{}, errors.New("--progress requires human, jsonl, or quiet")
			}
			mode, err := ParseWorkProgressMode(args[1])
			if err != nil {
				return WorkRunOptions{}, err
			}
			options.Progress = mode
			args = args[2:]
		default:
			return WorkRunOptions{}, fmt.Errorf("unknown work run argument %s before --", strconv.Quote(args[0]))
		}
	}
	if len(options.Command) == 0 {
		return WorkRunOptions{}, errors.New("work run requires -- COMMAND [ARGS...]")
	}
	if isResidentWorkCommand(options.Command) {
		return WorkRunOptions{}, errors.New("work run accepts finite commands only; resident dev/server commands belong under `ndev dev` so capacity is released after startup health")
	}
	if !classSet {
		// Prefer structural class: package-scoped toolchains become express-*,
		// multi-package / recursive shapes stay full weight. Unrecognized argv
		// still requires an explicit --class (fail closed, no silent under-weight).
		if options.Exclusive {
			return WorkRunOptions{}, errors.New("--exclusive requires --class benchmark or benchmark-exclusive")
		}
		classified, ok := ClassifyWorkArgv(options.Command)
		if !ok {
			return WorkRunOptions{}, errors.New("work run requires --class when the command is not a recognized toolchain shape; prefer --class express-test for package-scoped go/cargo/node/swift test, --class test for multi-package or ./...")
		}
		options.Class = classified
	}
	if options.Exclusive && options.Class != WorkClassBenchmark && options.Class != WorkClassBenchmarkExclusive {
		return WorkRunOptions{}, errors.New("--exclusive is only valid with --class benchmark or benchmark-exclusive")
	}
	if options.Priority && options.Wait == 0 {
		return WorkRunOptions{}, errors.New("--priority requires a positive --wait so the request can enter the audited queue")
	}
	options.RequestedClass = options.Class
	options.Class = ResolveWorkClass(options.Class, options.Exclusive, options.Command)
	return options, nil
}

func isResidentWorkCommand(command []string) bool {
	if len(command) == 0 {
		return false
	}
	name := strings.ToLower(filepath.Base(command[0]))
	args := command[1:]
	if name == "env" {
		for len(args) > 0 && strings.Contains(args[0], "=") {
			args = args[1:]
		}
		if len(args) == 0 {
			return false
		}
		name, args = strings.ToLower(filepath.Base(args[0])), args[1:]
	}
	if name == "vite" || name == "next" || name == "vinext" || name == "webpack-dev-server" {
		return len(args) == 0 || args[0] == "dev" || args[0] == "serve" || name == "webpack-dev-server"
	}
	if name != "npm" && name != "pnpm" && name != "yarn" && name != "bun" {
		return false
	}
	for i, arg := range args {
		if (arg == "run" || arg == "run-script") && i+1 < len(args) {
			return args[i+1] == "dev" || args[i+1] == "start" || args[i+1] == "serve"
		}
		if i == 0 && (arg == "dev" || arg == "start" || arg == "serve") {
			return true
		}
	}
	return false
}

// workShellBookkeepingEnv lists variables a shell rewrites as a side effect of
// how a command was reached rather than of what the command does: the previous
// command's last argument, the directory the caller happened to `cd` out of,
// and the interpreter nesting depth. None can change the output of coordinated
// `go build`/`test`/`vet` work, but every one of them is bound into the
// environment-keyed reuse fingerprint, so leaving them in makes two otherwise
// identical invocations hash differently and never replay a receipt. Keep this
// closed: anything a workload can legitimately read stays in the digest.
var workShellBookkeepingEnv = map[string]bool{
	"_":      true,
	"OLDPWD": true,
	"SHLVL":  true,
}

// WorkEnvironment builds the environment handed to a coordinated child. The
// same slice is what the express reuse fingerprint hashes, so what was hashed
// is exactly what ran — do not let the two diverge.
func WorkEnvironment(base []string, limits WorkLimits, class WorkClass) ([]string, error) {
	environment := make([]string, 0, len(base)+1)
	for _, value := range base {
		if key, _, found := strings.Cut(value, "="); found && workShellBookkeepingEnv[key] {
			continue
		}
		environment = append(environment, value)
	}
	for _, value := range environment {
		if key, _, found := strings.Cut(value, "="); found && key == "GOMAXPROCS" {
			return environment, nil
		}
	}
	weight, err := limits.Weight(class)
	if err != nil {
		return nil, err
	}
	return append(environment, "GOMAXPROCS="+strconv.Itoa(weight)), nil
}

func AdmissionReason(admission Admission) string {
	reason := strings.Join(admission.Reasons, "; ")
	if reason == "" {
		reason = "host pressure policy rejected new work"
	}
	return reason
}

type workAdmissionGate struct {
	limits                              WorkLimits
	cpuRed                              int
	cpuRecovered                        int
	latched                             bool
	deferredLevel                       Level
	deferredDimension                   string
	deferredReason                      string
	coordinatedWorkAttributionAvailable bool
	coordinatedWorkCPUAvailable         bool
	coordinatedWorkCPUPercent           float64
	coordinatedWorkLeaseCount           int
	coordinatedWorkProcessCount         int
	coordinatedWorkInventoryAgeSeconds  float64
	storageGrace                        time.Duration
	storageDeadline                     time.Time
	storageBestAvailableBytes           int64
	storageEvidenceInitialized          bool
	storageReclaimActive                func() bool
	cpuRedPercent                       float64
	now                                 func() time.Time
	// hold/release publish this process's pre-queue admission hold. They are
	// nil in unit tests and whenever coordinator state is unavailable, so the
	// gate keeps working without observability rather than failing the run.
	hold    func(dimension, reason string)
	release func()
	// sharedLatch folds this waiter's observation into one host-wide latch. It is
	// nil in unit tests and whenever coordinator state is unwritable, in which
	// case the gate falls back to its private counters and simply loses sharing.
	sharedLatch func(WorkAdmissionObservation) (WorkAdmissionLatch, error)
	// class and the two accessors below feed the fast-lane predicate. Any of them
	// being unset disables the fast lane for this run, which is the fail-closed
	// direction.
	class                 WorkClass
	suppressHold          bool
	classRuntimeP95MS     func() (int64, bool)
	freeWeightedCapacity  func() (int, bool)
	warningCapacityStatus func() (WorkStatus, bool)
	fastLaneAdmitted      bool
	fastLaneRefusal       fastLaneRefusal
	// Warning derating keeps the coordinator resident while lowering only the
	// ceiling seen by new admissions. These fields make the decision measurable.
	warningCapacityAdmitted bool
	warningCapacityDeferred bool
}

func (gate *workAdmissionGate) syncSharedLatch(observation WorkAdmissionObservation) (WorkAdmissionLatch, bool) {
	if gate == nil || gate.sharedLatch == nil {
		return WorkAdmissionLatch{}, false
	}
	latch, err := gate.sharedLatch(observation)
	if err != nil {
		return WorkAdmissionLatch{}, false
	}
	return latch, true
}

// recordHold publishes this process's pre-queue admission hold and reports
// whether it did. Once the operation is represented in coordinator state — as a
// lease, or as a pressure-reserved waiter after a post-acquire host gate — a hold
// would double-count it: the same operation would appear both in waiters and in
// admission_holds, which is exactly the kind of double vision this record exists
// to remove. An operation is in exactly one place at a time.
func (gate *workAdmissionGate) recordHold(dimension, reason string) bool {
	if gate == nil || gate.hold == nil || gate.suppressHold {
		return false
	}
	gate.hold(dimension, reason)
	return true
}

func (gate *workAdmissionGate) releaseHold() {
	if gate == nil || gate.release == nil {
		return
	}
	gate.release()
}

type workAdmissionDecision struct {
	Allowed       bool
	Dimension     string
	Reason        string
	RetryInterval time.Duration
}

func pressureDimension(admission Admission) string {
	if admission.Allowed {
		return ""
	}
	if admission.Dimension != "" {
		return admission.Dimension
	}
	if len(admission.Reasons) == 0 {
		return "unknown"
	}
	for _, reason := range admission.Reasons {
		if strings.HasPrefix(reason, "storage ") {
			return "storage"
		}
		if !strings.HasPrefix(reason, "host CPU ") {
			return "memory"
		}
	}
	return "cpu"
}

func (gate *workAdmissionGate) uncorroboratedCPURed(admission Admission) bool {
	if gate == nil || !admission.Allowed || admission.Level < LevelRed || admission.Dimension != "cpu" || admission.Snapshot == nil {
		return false
	}
	redPercent := gate.cpuRedPercent
	if redPercent <= 0 {
		redPercent = 95
	}
	blockSamples := max(2, gate.limits.CPUBlockSamples)
	snapshot := admission.Snapshot
	if !snapshot.HostCPUAvailable || snapshot.HostCPUPercent < redPercent {
		return false
	}
	return !(snapshot.HostCPURollingAvailable && snapshot.HostCPURollingPercent >= redPercent) && snapshot.ConsecutiveSamples < blockSamples
}

func coordinatedWorkEvidence(admission Admission) (available, cpuAvailable bool, cpuPercent float64, leaseCount, processCount int, inventoryAgeSeconds float64) {
	if admission.Snapshot == nil || !admission.Snapshot.CoordinatedWork.Available {
		return false, false, 0, 0, 0, 0
	}
	work := admission.Snapshot.CoordinatedWork
	return true, work.CPUAvailable, work.CPUPercent, work.LeaseCount, work.ProcessCount, work.InventoryAgeSeconds
}

func applyCoordinatedWorkEvidence(event *WorkEvent, admission Admission, dimension string) {
	if event == nil || dimension != "cpu" {
		return
	}
	event.CoordinatedWorkAttributionAvailable, event.CoordinatedWorkCPUAvailable,
		event.CoordinatedWorkCPUPercent, event.CoordinatedWorkLeaseCount,
		event.CoordinatedWorkProcessCount, event.CoordinatedWorkInventoryAgeSeconds = coordinatedWorkEvidence(admission)
}

func (gate *workAdmissionGate) recordDeferral(admission Admission, dimension, reason string) {
	if gate == nil || gate.deferredDimension != "" {
		return
	}
	gate.deferredLevel = admission.Level
	gate.deferredDimension = dimension
	gate.deferredReason = reason
	if dimension == "cpu" {
		gate.coordinatedWorkAttributionAvailable, gate.coordinatedWorkCPUAvailable,
			gate.coordinatedWorkCPUPercent, gate.coordinatedWorkLeaseCount,
			gate.coordinatedWorkProcessCount, gate.coordinatedWorkInventoryAgeSeconds = coordinatedWorkEvidence(admission)
	}
}

func (gate *workAdmissionGate) storageGraceError(admission Admission, dimension string) error {
	if gate == nil || dimension != "storage" {
		return nil
	}
	now := time.Now
	if gate.now != nil {
		now = gate.now
	}
	current := now().UTC()
	grace := gate.storageGrace
	if grace <= 0 {
		grace = 60 * time.Second
	}
	available := int64(0)
	if admission.Snapshot != nil {
		available = admission.Snapshot.Storage.AvailableBytes
	}
	if !gate.storageEvidenceInitialized {
		gate.storageEvidenceInitialized = true
		gate.storageBestAvailableBytes = available
		gate.storageDeadline = current.Add(grace)
	}
	if available >= gate.storageBestAvailableBytes+WorkStorageProgressBytes {
		gate.storageBestAvailableBytes = available
		gate.storageDeadline = current.Add(grace)
	}
	if gate.storageReclaimActive != nil && gate.storageReclaimActive() {
		gate.storageDeadline = current.Add(grace)
	}
	if !current.Before(gate.storageDeadline) {
		return fmt.Errorf("%w after %s without %d MiB progress or an active reclaim lease", ErrWorkStorageGraceExceeded, grace, WorkStorageProgressBytes>>20)
	}
	return nil
}

func (gate *workAdmissionGate) Observe(admission Admission, immediate bool) workAdmissionDecision {
	return gate.observe(admission, immediate, false)
}

func (gate *workAdmissionGate) ObserveOwned(admission Admission, immediate bool) workAdmissionDecision {
	return gate.observe(admission, immediate, true)
}

func (gate *workAdmissionGate) observe(admission Admission, immediate, ownsLease bool) workAdmissionDecision {
	if decision, ok := gate.warningCapacityDecision(admission, ownsLease); ok {
		return decision
	}
	if gate.uncorroboratedCPURed(admission) {
		return workAdmissionDecision{
			Allowed: true, Dimension: "cpu",
			Reason: "CPU-only live spike was not corroborated by resident rolling CPU or sustained monitor evidence",
		}
	}
	dimension := pressureDimension(admission)
	if immediate {
		if dimension == "cpu" {
			redPercent := gate.cpuRedPercent
			if redPercent <= 0 {
				redPercent = 95
			}
			if admission.Snapshot == nil || !admission.Snapshot.HostCPURollingAvailable || admission.Snapshot.HostCPURollingPercent < redPercent {
				return workAdmissionDecision{Allowed: true, Dimension: "cpu", Reason: "CPU-only live spike was not corroborated by resident rolling CPU"}
			}
		}
		return workAdmissionDecision{Allowed: admission.Allowed, Dimension: dimension, Reason: AdmissionReason(admission)}
	}
	if dimension != "" && dimension != "cpu" {
		gate.cpuRed = 0
		gate.cpuRecovered = 0
		// Deliberately does not touch the shared CPU latch. Folding a memory- or
		// storage-pressure observation into it hits the "neither red nor recovered"
		// branch, which clears the recovery counter — so a single process blocked
		// on memory would repeatedly wipe every other waiter's CPU-recovery
		// progress and the CPU latch could never release. Each dimension answers
		// only for itself.
		return workAdmissionDecision{Allowed: false, Dimension: dimension, Reason: AdmissionReason(admission), RetryInterval: WorkAdmissionRetryInterval}
	}
	// Before latching, ask whether this operation is light enough and short enough
	// that holding it serves no protective purpose. The weighted ceiling still
	// governs; the fast lane only decides who may contend for it.
	//
	// Only consult it when CPU pressure is actually present. An unpressured poll
	// has nothing to admit past, and asking anyway recorded a
	// `fast_lane_refused:non_cpu_dimension` verdict on every healthy run — 261 of
	// them before this was caught, which read as "the fast lane keeps declining"
	// when in truth it had never been asked a real question.
	if dimension == "cpu" {
		if decision, ok := gate.fastLaneDecision(admission, dimension); ok {
			return decision
		}
	}
	// One host, one latch. Fold this observation into shared state so concurrent
	// waiters share a single sustain requirement instead of each proving red and
	// recovery independently against their own private counters.
	recovered := admission.Snapshot != nil && admission.Snapshot.HostCPUAvailable &&
		admission.Snapshot.HostCPUPercent <= gate.limits.CPUReleasePercent && admission.Allowed
	if shared, ok := gate.syncSharedLatch(WorkAdmissionObservation{
		Red: dimension == "cpu", Recovered: recovered, Dimension: dimension, Reason: AdmissionReason(admission),
	}); ok {
		gate.latched = shared.Latched
		gate.cpuRed = shared.RedSamples
		gate.cpuRecovered = shared.RecoverySamples
		if !shared.Latched && dimension != "cpu" {
			return workAdmissionDecision{Allowed: admission.Allowed, Reason: AdmissionReason(admission)}
		}
		if !shared.Latched && recovered {
			if decision, owned := gate.warningCapacityDecision(admission, ownsLease); owned {
				return decision
			}
			return workAdmissionDecision{Allowed: true, Dimension: "cpu", Reason: "CPU-only pressure recovered below release threshold"}
		}
		if !shared.Latched {
			return workAdmissionDecision{
				Allowed: false, Dimension: "cpu",
				Reason:        fmt.Sprintf("confirming CPU-only red pressure (%d/%d): %s", shared.RedSamples, max(1, shared.BlockRequired), AdmissionReason(admission)),
				RetryInterval: WorkCPUConfirmationInterval,
			}
		}
		if recovered {
			return workAdmissionDecision{
				Allowed: false, Dimension: "cpu",
				Reason:        fmt.Sprintf("confirming CPU recovery at %.1f%% <= %.1f%% (%d/%d)", admission.Snapshot.HostCPUPercent, gate.limits.CPUReleasePercent, shared.RecoverySamples, max(1, shared.ReleaseRequired)),
				RetryInterval: WorkCPUConfirmationInterval,
			}
		}
		reason := AdmissionReason(admission)
		if admission.Allowed {
			reason = fmt.Sprintf("CPU admission remains latched until host CPU <= %.1f%%", gate.limits.CPUReleasePercent)
		}
		return workAdmissionDecision{Allowed: false, Dimension: "cpu", Reason: reason, RetryInterval: WorkAdmissionRetryInterval}
	}
	if !gate.latched {
		if dimension == "cpu" {
			gate.cpuRed++
			if gate.cpuRed >= max(1, gate.limits.CPUBlockSamples) {
				gate.latched = true
				gate.cpuRecovered = 0
				return workAdmissionDecision{Allowed: false, Dimension: "cpu", Reason: AdmissionReason(admission), RetryInterval: WorkAdmissionRetryInterval}
			}
			return workAdmissionDecision{
				Allowed: false, Dimension: "cpu",
				Reason:        fmt.Sprintf("confirming CPU-only red pressure (%d/%d): %s", gate.cpuRed, max(1, gate.limits.CPUBlockSamples), AdmissionReason(admission)),
				RetryInterval: WorkCPUConfirmationInterval,
			}
		}
		gate.cpuRed = 0
		return workAdmissionDecision{Allowed: admission.Allowed, Reason: AdmissionReason(admission)}
	}

	// Once CPU-only pressure has latched, release requires sustained evidence
	// below the lower threshold. A warning-band oscillation cannot flap a large
	// compiler wave on and off around the red boundary.
	if admission.Snapshot != nil && admission.Snapshot.HostCPUAvailable && admission.Snapshot.HostCPUPercent <= gate.limits.CPUReleasePercent && admission.Allowed {
		gate.cpuRecovered++
		if gate.cpuRecovered >= max(1, gate.limits.CPUReleaseSamples) {
			gate.latched = false
			gate.cpuRed = 0
			if decision, owned := gate.warningCapacityDecision(admission, ownsLease); owned {
				return decision
			}
			return workAdmissionDecision{Allowed: true, Dimension: "cpu", Reason: "CPU-only pressure recovered below release threshold"}
		}
		return workAdmissionDecision{
			Allowed: false, Dimension: "cpu",
			Reason:        fmt.Sprintf("confirming CPU recovery at %.1f%% <= %.1f%% (%d/%d)", admission.Snapshot.HostCPUPercent, gate.limits.CPUReleasePercent, gate.cpuRecovered, max(1, gate.limits.CPUReleaseSamples)),
			RetryInterval: WorkCPUConfirmationInterval,
		}
	}
	gate.cpuRecovered = 0
	reason := AdmissionReason(admission)
	if admission.Allowed {
		reason = fmt.Sprintf("CPU admission remains latched until host CPU <= %.1f%%", gate.limits.CPUReleasePercent)
	}
	return workAdmissionDecision{Allowed: false, Dimension: "cpu", Reason: reason, RetryInterval: WorkAdmissionRetryInterval}
}

type workProgressReporter struct {
	mode         WorkProgressMode
	stderr       io.Writer
	operationID  string
	class        WorkClass
	weight       int
	startedAt    time.Time
	lastKey      string
	lastReported time.Time
	now          func() time.Time
}

func newWorkProgressReporter(mode WorkProgressMode, stderr io.Writer, operationID string, class WorkClass, weight int, startedAt time.Time) *workProgressReporter {
	return &workProgressReporter{mode: mode, stderr: stderr, operationID: operationID, class: class, weight: weight, startedAt: startedAt, now: time.Now}
}

func (reporter *workProgressReporter) emit(progress WorkProgress) {
	if reporter == nil || reporter.mode == WorkProgressQuiet || reporter.stderr == nil {
		return
	}
	now := time.Now
	if reporter.now != nil {
		now = reporter.now
	}
	current := now().UTC()
	progress.SchemaVersion = WorkEventSchemaVersion
	progress.Timestamp = current
	progress.OperationID = reporter.operationID
	progress.Class = reporter.class
	progress.Weight = reporter.weight
	progress.ElapsedMS = max(int64(0), current.Sub(reporter.startedAt).Milliseconds())
	key := fmt.Sprintf("%s/%s/%d/%d/%s/%s", progress.Stage, progress.Blocker, progress.QueuePosition, progress.Used, progress.PressureLevel, progress.Reason)
	if key == reporter.lastKey && current.Sub(reporter.lastReported) < 30*time.Second {
		return
	}
	reporter.lastKey = key
	reporter.lastReported = current
	if reporter.mode == WorkProgressJSONL {
		body, err := json.Marshal(progress)
		if err != nil {
			fmt.Fprintln(reporter.stderr, "ndev session pressure: encode work progress:", err)
			return
		}
		fmt.Fprintln(reporter.stderr, string(body))
		return
	}
	if progress.Stage == "waiting" {
		fmt.Fprintf(reporter.stderr, "ndev session pressure: waiting elapsed=%s class=%s weight=%d blocker=%s", time.Duration(progress.ElapsedMS)*time.Millisecond, progress.Class, progress.Weight, progress.Blocker)
		if progress.QueueDepth > 0 {
			fmt.Fprintf(reporter.stderr, " queue=%d/%d", progress.QueuePosition, progress.QueueDepth)
		}
		if progress.Capacity > 0 {
			fmt.Fprintf(reporter.stderr, " capacity=%d/%d available=%d", progress.Used, progress.Capacity, progress.Available)
		}
		if progress.PressureLevel != "" {
			fmt.Fprintf(reporter.stderr, " pressure=%s/%s", progress.PressureLevel, progress.PressureDimension)
		}
		if progress.Reason != "" {
			fmt.Fprintf(reporter.stderr, " reason=%s", progress.Reason)
		}
		if progress.NextCheckSeconds > 0 {
			fmt.Fprintf(reporter.stderr, " next=%.0fs", progress.NextCheckSeconds)
		}
		fmt.Fprintln(reporter.stderr)
		return
	}
	fmt.Fprintf(reporter.stderr, "ndev session pressure: %s operation=%s class=%s elapsed=%s\n", progress.Stage, progress.OperationID, progress.Class, time.Duration(progress.ElapsedMS)*time.Millisecond)
}

func waiterPosition(status WorkStatus, operationID string) int {
	for _, waiter := range status.Waiters {
		if waiter.OperationID == operationID {
			return waiter.Position
		}
	}
	return 0
}

func leaseRecordID(lease *WorkLease) string {
	if lease == nil {
		return ""
	}
	return lease.Record().ID
}

func leaseRecordPID(lease *WorkLease) int {
	if lease == nil {
		return 0
	}
	return lease.Record().PID
}

// RunWorkCommand owns a weighted lease for the entire child lifetime. It is
// shared by the tiny installed helper and thin CLI adapters used by tests.
// Callers that want verified singleflight/reuse should use
// RunWorkCommandWithExpressReuse, which demotes package-scoped Go work and
// collapses identical express jobs and full Go test/vet jobs.
func RunWorkCommand(coordinator *WorkCoordinator, options WorkRunOptions, admissionCheck func() Admission, retryInterval time.Duration, streams WorkRunStreams) (int, error) {
	if coordinator == nil {
		return 1, errors.New("work coordinator is required")
	}
	if len(options.Command) == 0 {
		return 1, errors.New("work command is required")
	}
	if options.RequestedClass == "" {
		options.RequestedClass = options.Class
	}
	options.Class = ResolveWorkClass(options.Class, options.Exclusive, options.Command)
	if options.Progress == "" {
		options.Progress = WorkProgressHuman
	}
	if admissionCheck == nil {
		admissionCheck = func() Admission { return ConfiguredWorkHostAdmission(context.Background()) }
	}
	if retryInterval <= 0 {
		retryInterval = WorkAdmissionRetryInterval
	}
	if streams.Stdin == nil {
		streams.Stdin = os.Stdin
	}
	if streams.Stdout == nil {
		streams.Stdout = os.Stdout
	}
	if streams.Stderr == nil {
		streams.Stderr = os.Stderr
	}
	operationID, err := NewWorkOperationID()
	if err != nil {
		return 1, err
	}
	resolvedExecutable, err := exec.LookPath(options.Command[0])
	if err != nil {
		return 1, fmt.Errorf("resolve leased command: %w", err)
	}
	weight, err := coordinator.Limits.Weight(options.Class)
	if err != nil {
		return 1, err
	}
	startedAt := time.Now().UTC()
	reporter := newWorkProgressReporter(options.Progress, streams.Stderr, operationID, options.Class, weight, startedAt)
	eventStore := NewWorkEventStore(coordinator.Dir)
	retentionDays := options.RetentionDays
	if retentionDays < 1 {
		retentionDays = 14
	}
	if err := eventStore.Prune(retentionDays); err != nil {
		return 1, fmt.Errorf("prune work event telemetry: %w", err)
	}
	commandDigest := CommandShapeDigest(resolvedExecutable, len(options.Command)-1)
	sessionDigest := DetectedAgentSessionDigest(os.Environ())
	// Declared ahead of appendEvent so every persisted row can carry the gate's
	// admission decision; it is constructed once the storage/reclaim probes below
	// are available.
	var admissionGate *workAdmissionGate
	appendEvent := func(event WorkEvent) error {
		event.OperationID = operationID
		event.Class = options.Class
		if event.Event == WorkEventQueued && options.RequestedClass != options.Class {
			event.RequestedClass = options.RequestedClass
		}
		event.Weight = weight
		event.SessionDigest = sessionDigest
		event.CommandDigest = commandDigest
		if event.BatchStepCount == 0 {
			event.BatchStepCount = options.BatchStepCount
		}
		if event.BatchCompletedSteps == 0 {
			event.BatchCompletedSteps = options.BatchCompletedSteps
		}
		event.ReuseStatus = options.ReuseStatus
		event.ReuseDecision = options.ReuseDecision
		event.ReuseRefusalReason = options.ReuseRefusalReason
		event.ReuseKeyDigest = options.ReuseKeyDigest
		event.ReceiptDigest = options.ReceiptDigest
		event.SingleflightWaitMS = options.SingleflightWaitMS
		if event.AdmissionDecision == "" {
			event.AdmissionDecision = admissionGate.decisionLabel()
		}
		event.SchedulingPolicy = coordinator.schedulingPolicy()
		event.SelectorSchemaVersion = workSelectorSchemaVersion
		return eventStore.AppendDurable(event)
	}
	initialStatus, err := coordinator.Status(context.Background())
	if err != nil {
		return 1, fmt.Errorf("read heavy-work capacity before queueing: %w", err)
	}
	initialAdmission := admissionCheck()
	initialPressureReason := ""
	initialBlocker := WorkBlockerNone
	if !initialAdmission.Allowed {
		initialPressureReason = AdmissionReason(initialAdmission)
		initialBlocker = WorkBlockerPressure
	}
	queuedEvent := WorkEvent{
		Event: WorkEventQueued, PID: coordinator.PID, Blocker: initialBlocker,
		Capacity: initialStatus.Capacity, Used: initialStatus.Used, Available: initialStatus.Available,
		PressureLevel: initialAdmission.Level, PressureDimension: pressureDimension(initialAdmission), PressureReason: initialPressureReason,
	}
	applyCoordinatedWorkEvidence(&queuedEvent, initialAdmission, queuedEvent.PressureDimension)
	if err := appendEvent(queuedEvent); err != nil {
		return 1, fmt.Errorf("persist queued work event: %w", err)
	}

	var waitCtx context.Context = context.Background()
	cancel := func() {}
	if options.Wait > 0 {
		waitCtx, cancel = context.WithTimeout(context.Background(), options.Wait)
	}
	waitCtx, stopWaiting := signal.NotifyContext(waitCtx, os.Interrupt, syscall.SIGTERM)
	var lease *WorkLease
	var acquiredAt time.Time
	var childStartedAt time.Time
	var admissionReadyAt time.Time
	var admissionWaitMS int64
	var queueWaitMS int64
	var pressureWaitMS int64
	terminalRecorded := false
	recordTerminal := func(eventType WorkEventType, outcome string, exitCode *int) error {
		if terminalRecorded {
			return nil
		}
		now := time.Now().UTC()
		waitMS := now.Sub(startedAt).Milliseconds()
		if !childStartedAt.IsZero() {
			waitMS = childStartedAt.Sub(startedAt).Milliseconds()
		}
		runtimeMS := int64(0)
		if !childStartedAt.IsZero() {
			runtimeMS = now.Sub(childStartedAt).Milliseconds()
		}
		completedSteps := options.BatchCompletedSteps
		if options.CompletedSteps != nil {
			completedSteps = max(0, min(options.BatchStepCount, options.CompletedSteps()))
		}
		// An operation that never acquired left its time after the admission gate
		// unattributed, because queueWaitMS is only assigned on acquire. That is
		// exactly the population worth measuring — work that waited and then gave
		// up — so close the residual here and keep the buckets disjoint.
		terminalAdmissionMS, terminalQueueMS := admissionWaitMS, queueWaitMS
		if admissionReadyAt.IsZero() {
			terminalAdmissionMS = max(int64(0), waitMS)
			terminalQueueMS = 0
		} else if acquiredAt.IsZero() {
			queueEnd := now
			if !childStartedAt.IsZero() {
				queueEnd = childStartedAt
			}
			terminalQueueMS = max(int64(0), queueEnd.Sub(admissionReadyAt).Milliseconds())
		}
		err := appendEvent(WorkEvent{
			Event: eventType, LeaseID: leaseRecordID(lease), PID: leaseRecordPID(lease),
			WaitMilliseconds: max(int64(0), waitMS), RuntimeMillis: max(int64(0), runtimeMS), ExitCode: exitCode, Outcome: outcome,
			AdmissionWaitMilliseconds: terminalAdmissionMS, QueueWaitMilliseconds: terminalQueueMS,
			PressureWaitMilliseconds: pressureWaitMS, PrestartMilliseconds: max(int64(0), waitMS),
			BatchCompletedSteps: completedSteps,
		})
		if err == nil {
			terminalRecorded = true
		}
		return err
	}
	release := func() error {
		if lease == nil {
			return nil
		}
		releaseCtx, releaseCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer releaseCancel()
		return lease.Release(releaseCtx)
	}
	admissionGate = &workAdmissionGate{
		limits:        coordinator.Limits,
		storageGrace:  configuredWorkStorageGrace(coordinator.Dir),
		cpuRedPercent: configuredWorkCPURedPercent(coordinator.Dir),
		storageReclaimActive: func() bool {
			statusCtx, statusCancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
			defer statusCancel()
			status, statusErr := coordinator.Status(statusCtx)
			if statusErr != nil {
				return false
			}
			for _, active := range status.Leases {
				if active.Class == WorkClassReclaim {
					return true
				}
			}
			return false
		},
		hold: func(dimension, reason string) {
			holdCtx, holdCancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer holdCancel()
			// Observability must never be able to fail the run it is observing.
			_ = coordinator.HoldAdmission(holdCtx, operationID, options.Class, WorkAdmissionObservation{
				Dimension: dimension, Reason: reason,
			})
		},
		release: func() {
			releaseCtx, releaseCancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer releaseCancel()
			_ = coordinator.ReleaseAdmission(releaseCtx, operationID)
		},
		sharedLatch: func(observation WorkAdmissionObservation) (WorkAdmissionLatch, error) {
			latchCtx, latchCancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer latchCancel()
			return coordinator.ObserveAdmissionLatch(latchCtx, observation)
		},
		class:                options.Class,
		classRuntimeP95MS:    newCalibratedClassRuntimeP95(coordinator.Dir, options.Class, coordinator.Now),
		freeWeightedCapacity: func() (int, bool) { return coordinatorFreeCapacity(coordinator) },
		warningCapacityStatus: func() (WorkStatus, bool) {
			statusCtx, statusCancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
			defer statusCancel()
			status, statusErr := coordinator.Status(statusCtx)
			return status, statusErr == nil
		},
	}
	admissionAfterWait, admissionErr := waitForWorkAdmission(waitCtx, options.Wait > 0, admissionCheck, retryInterval, admissionGate, &initialAdmission, reporter)
	if admissionErr != nil {
		stopWaiting()
		cancel()
		eventType := WorkEventFailed
		outcome := "admission_failed"
		if waitCtx.Err() != nil {
			eventType = WorkEventCancelled
			outcome = workCancelOutcome(waitCtx.Err(), 0)
		}
		return coordinatorFailureExit(admissionErr, workExitPolicyDenied), errors.Join(admissionErr, recordTerminal(eventType, outcome, nil))
	}
	initialAdmission = admissionAfterWait
	admissionReadyAt = time.Now().UTC()
	admissionWaitMS = max(int64(0), admissionReadyAt.Sub(startedAt).Milliseconds())
	var status WorkStatus
	var firstQueueBlocker WorkBlocker
	var firstQueueStatus WorkStatus
	emitQueueProgress := func(status WorkStatus, blocker WorkBlocker) {
		if firstQueueBlocker == "" {
			firstQueueBlocker = blocker
			firstQueueStatus = status
		}
		reporter.emit(WorkProgress{
			Stage: "waiting", Blocker: blocker, QueuePosition: waiterPosition(status, operationID), QueueDepth: status.QueueDepth,
			Used: status.Used, Capacity: status.Capacity, Available: status.Available,
			Reason: map[WorkBlocker]string{
				WorkBlockerCapacity:    workCapacityReason(status),
				WorkBlockerFairness:    "another waiter is the oldest feasible operation",
				WorkBlockerReservation: "a protected waiter owns the next reservation",
				WorkBlockerUpgrade:     "a legacy coordinator lease must finish before the state upgrade",
			}[blocker],
			SchedulingPolicy: status.SchedulingPolicy, SelectorSchemaVersion: status.SelectorSchemaVersion,
			SelectedOperationID: status.SelectedOperationID, ProtectedOperationID: status.ProtectedOperationID,
			DecisionReason: status.DecisionReason, BypassedCount: status.BypassedCount,
			CandidateSchedulingPolicy: status.CandidateSchedulingPolicy, ShadowSelectedOperationID: status.ShadowSelectedOperationID,
			ShadowProtectedOperationID: status.ShadowProtectedOperationID, ShadowDecisionReason: status.ShadowDecisionReason,
			NextCheckSeconds: workCapacityRetryInterval.Seconds(),
		})
	}
	effectiveCapacity := warningEffectiveCapacity(admissionAfterWait, coordinator.Limits)
	if options.Wait == 0 {
		lease, status, err = coordinator.AcquireOperationWithCapacity(waitCtx, options.Class, operationID, effectiveCapacity)
	} else if options.Priority {
		lease, status, err = coordinator.WaitAcquirePriorityOperationWithCapacity(waitCtx, options.Class, operationID, effectiveCapacity, emitQueueProgress)
	} else {
		lease, status, err = coordinator.WaitAcquireOperationWithCapacity(waitCtx, options.Class, operationID, effectiveCapacity, emitQueueProgress)
	}
	if err != nil {
		stopWaiting()
		cancel()
		eventType := WorkEventFailed
		outcome := "acquisition_failed"
		if waitCtx.Err() != nil {
			eventType = WorkEventCancelled
			outcome = workCancelOutcome(waitCtx.Err(), 0)
		}
		return coordinatorFailureExit(err, 1), errors.Join(fmt.Errorf("acquire %s work lease: %w (used=%d capacity=%d queue=%d)", options.Class, err, status.Used, status.Capacity, status.QueueDepth), recordTerminal(eventType, outcome, nil))
	}
	postLeaseAdmission := admissionCheck()
	postLeaseDecision := admissionGate.ObserveOwned(postLeaseAdmission, false)
	if !postLeaseDecision.Allowed && admissionGate.deferredDimension == "" {
		admissionGate.recordDeferral(postLeaseAdmission, postLeaseDecision.Dimension, postLeaseDecision.Reason)
	}
	experiencedBlocker := firstQueueBlocker
	if experiencedBlocker == "" && admissionGate.deferredDimension != "" {
		experiencedBlocker = WorkBlockerPressure
	}
	acquiredAt = time.Now().UTC()
	queueWaitMS = max(int64(0), acquiredAt.Sub(admissionReadyAt).Milliseconds())
	if eventErr := appendEvent(WorkEvent{
		Event: WorkEventAcquired, LeaseID: lease.Record().ID, PID: lease.Record().PID,
		Capacity: status.Capacity, Used: status.Used, Available: status.Available,
		Blocker: experiencedBlocker, QueuePosition: waiterPosition(firstQueueStatus, operationID), QueueDepth: firstQueueStatus.QueueDepth,
		PressureLevel: admissionGate.deferredLevel, PressureDimension: admissionGate.deferredDimension, PressureReason: admissionGate.deferredReason,
		CoordinatedWorkAttributionAvailable: admissionGate.coordinatedWorkAttributionAvailable,
		CoordinatedWorkCPUAvailable:         admissionGate.coordinatedWorkCPUAvailable,
		CoordinatedWorkCPUPercent:           admissionGate.coordinatedWorkCPUPercent,
		CoordinatedWorkLeaseCount:           admissionGate.coordinatedWorkLeaseCount,
		CoordinatedWorkProcessCount:         admissionGate.coordinatedWorkProcessCount,
		CoordinatedWorkInventoryAgeSeconds:  admissionGate.coordinatedWorkInventoryAgeSeconds,
		WaitMilliseconds:                    max(int64(0), acquiredAt.Sub(startedAt).Milliseconds()), Outcome: "lease_acquired",
		AdmissionWaitMilliseconds: admissionWaitMS, QueueWaitMilliseconds: queueWaitMS,
		PrestartMilliseconds: max(int64(0), acquiredAt.Sub(startedAt).Milliseconds()),
		SelectedOperationID:  status.SelectedOperationID, ProtectedOperationID: status.ProtectedOperationID, DecisionReason: status.DecisionReason,
		BypassedCount:             status.BypassedCount,
		CandidateSchedulingPolicy: status.CandidateSchedulingPolicy, ShadowSelectedOperationID: status.ShadowSelectedOperationID,
		ShadowProtectedOperationID: status.ShadowProtectedOperationID, ShadowDecisionReason: status.ShadowDecisionReason,
	}); eventErr != nil {
		releaseErr := release()
		stopWaiting()
		cancel()
		return 1, errors.Join(fmt.Errorf("persist acquired work event: %w", eventErr), releaseErr, recordTerminal(WorkEventFailed, "acquired_event_failed", nil))
	}
	reporter.emit(WorkProgress{Stage: "acquired", Used: status.Used, Capacity: status.Capacity, Available: status.Available})

	// If pressure changed while this operation queued, convert the lease into a
	// protected, non-consuming reservation before waiting. Schema-five owners
	// cannot represent that state, so mixed-version operation conservatively
	// retains the lease until the old ledger drains.
	var pressureWaiter *WorkWaiter
	cancelPressureWaiter := func() error {
		if pressureWaiter == nil {
			return nil
		}
		cancelCtx, cancelReservation := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancelReservation()
		err := pressureWaiter.Cancel(cancelCtx)
		if err == nil {
			pressureWaiter = nil
		}
		return err
	}
	// Admission and weighted capacity are independent moving gates. A pressure
	// reservation may wait a long time to regain capacity after pressure clears;
	// recheck pressure after every reacquisition and cycle the same durable queue
	// identity back into a non-consuming reservation when the host regressed.
	for {
		if postLeaseDecision.Allowed {
			break
		}
		pressureWaitStarted := time.Now().UTC()
		if options.Wait > 0 {
			reservedLeaseID := lease.Record().ID
			reserved, reservedStatus, reserveErr := lease.ReserveForPressure(waitCtx)
			if reserveErr == nil {
				pressureWaiter = reserved
				lease = nil
				status = reservedStatus
				if eventErr := appendEvent(WorkEvent{
					Event: WorkEventReserved, LeaseID: reservedLeaseID, PID: coordinator.PID,
					Blocker: WorkBlockerPressure, Capacity: status.Capacity, Used: status.Used, Available: status.Available,
					PressureLevel: postLeaseAdmission.Level, PressureDimension: pressureDimension(postLeaseAdmission), PressureReason: AdmissionReason(postLeaseAdmission),
					WaitMilliseconds:          max(int64(0), time.Since(startedAt).Milliseconds()),
					AdmissionWaitMilliseconds: admissionWaitMS, QueueWaitMilliseconds: queueWaitMS,
					PrestartMilliseconds: max(int64(0), time.Since(startedAt).Milliseconds()), Outcome: "pressure_reserved",
				}); eventErr != nil {
					cancelErr := cancelPressureWaiter()
					stopWaiting()
					cancel()
					return 1, errors.Join(fmt.Errorf("persist pressure reservation event: %w", eventErr), cancelErr, recordTerminal(WorkEventFailed, "reservation_event_failed", nil))
				}
				reporter.emit(WorkProgress{
					Stage: "reserved", Blocker: WorkBlockerPressure, Used: status.Used, Capacity: status.Capacity, Available: status.Available,
					PressureLevel: postLeaseAdmission.Level, PressureDimension: pressureDimension(postLeaseAdmission), Reason: AdmissionReason(postLeaseAdmission),
					NextCheckSeconds: retryInterval.Seconds(),
				})
			} else if !errors.Is(reserveErr, ErrWorkUpgradePending) {
				releaseErr := release()
				stopWaiting()
				cancel()
				return coordinatorFailureExit(reserveErr, 1), errors.Join(fmt.Errorf("reserve pressure-blocked work: %w", reserveErr), releaseErr, recordTerminal(WorkEventFailed, "reservation_failed", nil))
			}
		}
		// The operation now owns a lease, or a pressure reservation that already
		// shows up as a waiter. Either way it is visible without a hold record.
		admissionGate.suppressHold = true
		admissionAfterLease, admissionErr := waitForWorkAdmission(waitCtx, options.Wait > 0, admissionCheck, retryInterval, admissionGate, &postLeaseAdmission, reporter)
		pressureWaitMS += max(int64(0), time.Since(pressureWaitStarted).Milliseconds())
		if admissionErr != nil {
			releaseErr := errors.Join(release(), cancelPressureWaiter())
			stopWaiting()
			cancel()
			eventType := WorkEventFailed
			outcome := "post_acquire_pressure"
			if waitCtx.Err() != nil {
				eventType = WorkEventCancelled
				outcome = workCancelOutcome(waitCtx.Err(), 0)
			}
			return coordinatorFailureExit(admissionErr, workExitPolicyDenied), errors.Join(admissionErr, releaseErr, recordTerminal(eventType, outcome, nil))
		}
		postLeaseAdmission = admissionAfterLease
		if pressureWaiter == nil {
			break
		}

		reacquireCapacity := warningEffectiveCapacity(postLeaseAdmission, coordinator.Limits)
		reacquired, reacquiredStatus, reacquireErr := pressureWaiter.WaitAcquireWithCapacity(waitCtx, reacquireCapacity, emitQueueProgress)
		pressureWaiter = nil
		status = reacquiredStatus
		if reacquireErr != nil {
			stopWaiting()
			cancel()
			eventType := WorkEventFailed
			outcome := "pressure_reacquire_failed"
			if waitCtx.Err() != nil {
				eventType = WorkEventCancelled
				outcome = workCancelOutcome(waitCtx.Err(), 0)
			}
			return coordinatorFailureExit(reacquireErr, 1), errors.Join(fmt.Errorf("reacquire pressure reservation: %w", reacquireErr), recordTerminal(eventType, outcome, nil))
		}
		lease = reacquired
		if eventErr := appendEvent(WorkEvent{
			Event: WorkEventReacquired, LeaseID: lease.Record().ID, PID: lease.Record().PID,
			Capacity: status.Capacity, Used: status.Used, Available: status.Available,
			WaitMilliseconds:          max(int64(0), time.Since(startedAt).Milliseconds()),
			AdmissionWaitMilliseconds: admissionWaitMS, QueueWaitMilliseconds: queueWaitMS,
			PressureWaitMilliseconds: pressureWaitMS, PrestartMilliseconds: max(int64(0), time.Since(startedAt).Milliseconds()),
			PressureLevel: postLeaseAdmission.Level, PressureDimension: admissionGate.deferredDimension, PressureReason: admissionGate.deferredReason,
			SelectedOperationID: status.SelectedOperationID, ProtectedOperationID: status.ProtectedOperationID, DecisionReason: status.DecisionReason,
			Outcome: "pressure_reservation_reacquired",
		}); eventErr != nil {
			releaseErr := release()
			stopWaiting()
			cancel()
			return 1, errors.Join(fmt.Errorf("persist pressure reacquisition event: %w", eventErr), releaseErr, recordTerminal(WorkEventFailed, "reacquired_event_failed", nil))
		}
		reporter.emit(WorkProgress{Stage: "reacquired", Used: status.Used, Capacity: status.Capacity, Available: status.Available})

		// WaitAcquire only governs capacity and fairness. Do not let time spent in
		// that queue make an earlier green sample authoritative at child start.
		postLeaseAdmission = admissionCheck()
		// The raw probe denying is no longer the same question as this operation
		// being unable to proceed. A fast-lane admission holds while the host is
		// still red, so testing only the probe here spun reserve → reacquire →
		// reserve without end: every iteration re-reserved on a red sample that
		// the gate had already decided to admit past, at roughly a hundred
		// persisted events per second until the wait budget expired. Ask the gate
		// the question it exists to answer; it re-checks free capacity each time,
		// so a ceiling that fills mid-wait still parks the operation.
		postLeaseDecision = admissionGate.ObserveOwned(postLeaseAdmission, false)
		if admissionGate.deferredDimension == "" {
			admissionGate.recordDeferral(postLeaseAdmission, postLeaseDecision.Dimension, postLeaseDecision.Reason)
		}
	}

	signals := streams.Signals
	var ownedSignals chan os.Signal
	if signals == nil {
		ownedSignals = make(chan os.Signal, 2)
		signal.Notify(ownedSignals, os.Interrupt, syscall.SIGTERM)
		signals = ownedSignals
		defer signal.Stop(ownedSignals)
	}
	if waitCtx.Err() != nil {
		preStartOutcome := workCancelOutcome(waitCtx.Err(), 0)
		if releaseErr := release(); releaseErr != nil {
			stopWaiting()
			cancel()
			return coordinatorFailureExit(waitCtx.Err(), 1), errors.Join(fmt.Errorf("release canceled work lease: %w", releaseErr), recordTerminal(WorkEventCancelled, preStartOutcome, nil))
		}
		stopWaiting()
		cancel()
		return coordinatorFailureExit(waitCtx.Err(), 1), errors.Join(fmt.Errorf("wait for heavy work: %w", waitCtx.Err()), recordTerminal(WorkEventCancelled, preStartOutcome, nil))
	}
	stopWaiting()
	cancel()
	if options.reusePreparer != nil {
		if prepareReason := options.reusePreparer(); prepareReason != "" {
			options.ReuseRefusalReason = prepareReason
			options.reuseFinalizer = nil
		}
	}

	commandFactory := streams.CommandFactory
	if commandFactory == nil {
		commandFactory = newGatedWorkCommand
	}
	command, gate, commandErr := commandFactory(options.Command[0], options.Command[1:])
	if commandErr != nil {
		if releaseErr := release(); releaseErr != nil {
			commandErr = errors.Join(commandErr, fmt.Errorf("release work lease: %w", releaseErr))
		}
		return 1, errors.Join(fmt.Errorf("prepare gated leased command: %w", commandErr), recordTerminal(WorkEventFailed, "prepare_failed", nil))
	}
	if command == nil {
		commandErr = errors.New("work command factory returned a nil command")
		if gate != nil {
			commandErr = errors.Join(commandErr, gate.Close())
		}
		if releaseErr := release(); releaseErr != nil {
			commandErr = errors.Join(commandErr, fmt.Errorf("release work lease: %w", releaseErr))
		}
		return 1, errors.Join(fmt.Errorf("prepare gated leased command: %w", commandErr), recordTerminal(WorkEventFailed, "prepare_failed", nil))
	}
	command.Stdin = streams.Stdin
	command.Stdout = streams.Stdout
	command.Stderr = streams.Stderr
	environment, environmentErr := WorkEnvironment(os.Environ(), coordinator.Limits, options.Class)
	if environmentErr != nil {
		if gate != nil {
			environmentErr = errors.Join(environmentErr, gate.Close())
		}
		environmentErr = errors.Join(environmentErr, closeWorkCommandExtraFiles(command))
		if releaseErr := release(); releaseErr != nil {
			environmentErr = errors.Join(environmentErr, fmt.Errorf("release work lease: %w", releaseErr))
		}
		return 1, errors.Join(fmt.Errorf("configure leased command: %w", environmentErr), recordTerminal(WorkEventFailed, "configure_failed", nil))
	}
	command.Env = environment
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if startErr := command.Start(); startErr != nil {
		if gate != nil {
			startErr = errors.Join(startErr, gate.Close())
		}
		startErr = errors.Join(startErr, closeWorkCommandExtraFiles(command))
		if releaseErr := release(); releaseErr != nil {
			startErr = errors.Join(startErr, fmt.Errorf("release work lease: %w", releaseErr))
		}
		return 1, errors.Join(fmt.Errorf("start leased command: %w", startErr), recordTerminal(WorkEventFailed, "start_failed", nil))
	}
	if closeErr := closeWorkCommandExtraFiles(command); closeErr != nil {
		fmt.Fprintln(streams.Stderr, "ndev session pressure: close parent lease gate copy:", closeErr)
	}
	if priorityErr := syscall.Setpriority(syscall.PRIO_PROCESS, command.Process.Pid, 5); priorityErr != nil && !errors.Is(priorityErr, syscall.ESRCH) {
		fmt.Fprintln(streams.Stderr, "ndev session pressure: lower leased command priority:", priorityErr)
	}
	bindCtx, bindCancel := context.WithTimeout(context.Background(), 5*time.Second)
	bindErr := lease.BindPID(bindCtx, command.Process.Pid)
	bindCancel()
	if bindErr != nil {
		if gate != nil {
			bindErr = errors.Join(bindErr, gate.Close())
		}
		bindErr = abortStartedWorkCommand(command, bindErr)
		finalizeErr := finalizeWorkLease(
			func() error { return recordTerminal(WorkEventFailed, "bind_failed", nil) },
			release,
		)
		return 1, errors.Join(fmt.Errorf("bind work lease to child: %w", bindErr), finalizeErr)
	}
	childStartedAt = time.Now().UTC()
	if eventErr := appendEvent(WorkEvent{
		Event: WorkEventStarted, LeaseID: lease.Record().ID, PID: command.Process.Pid,
		WaitMilliseconds: max(int64(0), childStartedAt.Sub(startedAt).Milliseconds()), Outcome: "child_bound",
		AdmissionWaitMilliseconds: admissionWaitMS, QueueWaitMilliseconds: queueWaitMS,
		PressureWaitMilliseconds: pressureWaitMS, PrestartMilliseconds: max(int64(0), childStartedAt.Sub(startedAt).Milliseconds()),
	}); eventErr != nil {
		if gate != nil {
			eventErr = errors.Join(eventErr, gate.Close())
		}
		eventErr = abortStartedWorkCommand(command, eventErr)
		eventErr = errors.Join(eventErr, finalizeWorkLease(
			func() error { return recordTerminal(WorkEventFailed, "started_event_failed", nil) },
			release,
		))
		return 1, fmt.Errorf("persist started work event: %w", eventErr)
	}
	if gate != nil {
		written, gateErr := gate.Write([]byte{workGateToken})
		closeErr := gate.Close()
		gate = nil
		if gateErr != nil || written != 1 {
			gateErr = errors.Join(gateErr, closeErr)
			if gateErr == nil {
				gateErr = io.ErrShortWrite
			}
			gateErr = abortStartedWorkCommand(command, fmt.Errorf("release durably bound work child: %w", gateErr))
			finalizeErr := finalizeWorkLease(
				func() error { return recordTerminal(WorkEventFailed, "gate_release_failed", nil) },
				release,
			)
			return 1, errors.Join(gateErr, finalizeErr)
		}
		if closeErr != nil {
			fmt.Fprintln(streams.Stderr, "ndev session pressure: close released lease gate:", closeErr)
		}
	}
	reporter.emit(WorkProgress{Stage: "started"})

	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	var cancellationSignal syscall.Signal
	for {
		select {
		case received, open := <-signals:
			if !open {
				signals = nil
				continue
			}
			if unixSignal, ok := received.(syscall.Signal); ok {
				if cancellationSignal == 0 {
					cancellationSignal = unixSignal
				}
				if signalErr := syscall.Kill(-command.Process.Pid, unixSignal); signalErr != nil && !errors.Is(signalErr, syscall.ESRCH) {
					fmt.Fprintln(streams.Stderr, "ndev session pressure: forward signal to leased command:", signalErr)
				}
			}
		case waitErr := <-done:
			if waitErr == nil && cancellationSignal == 0 && options.reuseFinalizer != nil {
				if finalReason := options.reuseFinalizer(); finalReason != "" {
					options.ReuseRefusalReason = finalReason
				}
			}
			code := 0
			eventType := WorkEventCompleted
			outcome := "completed"
			if cancellationSignal != 0 {
				code = 128 + int(cancellationSignal)
				eventType = WorkEventCancelled
				// Closed forensics enum (M2/P5): wrapper forwarded agent/host signal.
				outcome = workCancelOutcome(nil, cancellationSignal)
			} else if waitErr != nil {
				var exitErr *exec.ExitError
				if errors.As(waitErr, &exitErr) {
					code = exitErr.ExitCode()
				} else {
					code = 1
				}
				eventType = WorkEventFailed
				outcome = "child_failed"
			}
			finalizeErr := finalizeWorkLease(
				func() error { return recordTerminal(eventType, outcome, &code) },
				release,
			)
			reporter.emit(WorkProgress{Stage: string(eventType)})
			if finalizeErr != nil {
				return 1, errors.Join(fmt.Errorf("finalize leased command"), finalizeErr)
			}
			if waitErr == nil {
				return code, nil
			}
			var exitErr *exec.ExitError
			if errors.As(waitErr, &exitErr) {
				return code, nil
			}
			return 1, fmt.Errorf("wait for leased command: %w", waitErr)
		}
	}
}

func finalizeWorkLease(recordTerminal, release func() error) error {
	// Persist the child outcome while the lease is still authoritative.
	// Releasing first leaves a narrow child-exit window in which a concurrent
	// status reconciliation can emit a false owner expiry. Always release even
	// if the durable receipt fails so a telemetry fault cannot leak capacity.
	terminalErr := recordTerminal()
	releaseErr := release()
	return errors.Join(terminalErr, releaseErr)
}

func workCapacityReason(status WorkStatus) string {
	reason := "weighted capacity unavailable"
	for _, lease := range status.Leases {
		if lease.Review {
			return fmt.Sprintf("%s; finite %s lease has run %s and may be a resident service (inspect work status; use ndev dev for future launches)", reason, lease.Class, time.Duration(lease.AgeMS)*time.Millisecond)
		}
	}
	if status.DecisionReason == "no_waiter_fits_capacity" || status.DecisionReason == "fifo_head_does_not_fit" {
		return reason + "; no queued operation currently fits the remaining weighted capacity"
	}
	return reason
}

func closeWorkCommandExtraFiles(command *exec.Cmd) error {
	if command == nil {
		return nil
	}
	var failures []error
	for _, file := range command.ExtraFiles {
		if file != nil {
			if err := file.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
				failures = append(failures, err)
			}
		}
	}
	command.ExtraFiles = nil
	return errors.Join(failures...)
}

func abortStartedWorkCommand(command *exec.Cmd, cause error) error {
	if command == nil || command.Process == nil {
		return cause
	}
	killErr := syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
	if killErr != nil && !errors.Is(killErr, syscall.ESRCH) {
		if fallbackErr := command.Process.Kill(); fallbackErr != nil && !errors.Is(fallbackErr, os.ErrProcessDone) {
			cause = errors.Join(cause, fmt.Errorf("kill gated command: group=%v process=%w", killErr, fallbackErr))
		}
	}
	waitErr := command.Wait()
	var exitErr *exec.ExitError
	if waitErr != nil && !errors.As(waitErr, &exitErr) {
		cause = errors.Join(cause, fmt.Errorf("reap gated command: %w", waitErr))
	}
	return cause
}

// Coordinator-origin failure exits, aligned with the invocation-outcome
// exit-code contract (internal/telemetry/outcome.go): 124 → timeout /
// deadline_exceeded, 130 → cancelled/interrupted, 11 → policy_block /
// policy_denied. Child exit codes still propagate untouched once the child
// starts; these apply only before the child runs, so red-pressure waits and
// denials stop projecting as unclassifiable exit-1 failures.
const (
	workExitPolicyDenied = 11
	workExitWaitTimeout  = 124
	workExitInterrupted  = 130
)

// coordinatorFailureExit classifies a pre-child coordinator failure by its
// cause: wait deadline → timeout band, signal cancel → interrupt band,
// otherwise the provided fallback (policy band for admission denials, 1 for
// internal coordinator errors). It keys on the error chain, not ambient
// context state, so an unrelated cancellation cannot misclassify a denial.
func coordinatorFailureExit(cause error, fallback int) int {
	switch {
	case errors.Is(cause, context.DeadlineExceeded):
		return workExitWaitTimeout
	case errors.Is(cause, context.Canceled):
		return workExitInterrupted
	}
	return fallback
}

// workCancelOutcome maps cancellation causes to closed privacy-safe outcome
// enums counted as wrapper_interrupt_operations (P5 / M2 forensics).
// Never free text; never argv.
func workCancelOutcome(err error, signal syscall.Signal) string {
	if signal != 0 {
		switch signal {
		case syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP:
			return "wrapper_interrupt"
		default:
			return "signal_interrupt"
		}
	}
	if err != nil && errors.Is(err, context.Canceled) {
		return "wrapper_interrupt"
	}
	if err != nil && errors.Is(err, context.DeadlineExceeded) {
		// Wait deadline is not an agent kill — keep distinct closed token so
		// forensics can separate timeouts from interrupts without counting as
		// wrapper-kill pressure unless explicitly cancelled.
		return "interrupted"
	}
	return "interrupted"
}

// storageDeadlockAdviceAfter is how long a storage-red waiter must spin before
// the coordinator emits a single actionable deadlock line (KEP P1).
const storageDeadlockAdviceAfter = 45 * time.Second

// shouldEmitStorageDeadlockAdvice is the pure emit gate for storage-red
// deadlock advice (unit-proven; no wall-clock wait required in tests).
func shouldEmitStorageDeadlockAdvice(elapsed time.Duration, alreadyEmitted bool, dimension string) bool {
	if alreadyEmitted || dimension != "storage" {
		return false
	}
	return elapsed >= storageDeadlockAdviceAfter
}

func waitForWorkAdmission(ctx context.Context, wait bool, admissionCheck func() Admission, retryInterval time.Duration, gate *workAdmissionGate, initial *Admission, reporter *workProgressReporter) (Admission, error) {
	waitStarted := time.Now()
	deadlockAdviceEmitted := false
	// A process parked here holds no weighted capacity, so nothing else in the
	// coordinator can see it. Publish a hold record for the duration of the wait
	// and always withdraw it, including on the error and cancellation paths.
	held := false
	defer func() {
		if held {
			gate.releaseHold()
		}
	}()
	for {
		admission := Admission{}
		if initial != nil {
			admission = *initial
			initial = nil
		} else {
			admission = admissionCheck()
		}
		if admission.Warning != "" {
			fmt.Fprintln(reporter.stderr, "ndev session pressure: heavy-work admission warning:", admission.Warning)
		}
		decision := gate.Observe(admission, !wait)
		if decision.Allowed {
			return admission, nil
		}
		if gate.deferredDimension == "" {
			gate.recordDeferral(admission, decision.Dimension, decision.Reason)
		}
		if wait {
			if gate.recordHold(decision.Dimension, decision.Reason) {
				held = true
			}
		}
		if graceErr := gate.storageGraceError(admission, decision.Dimension); graceErr != nil {
			return admission, graceErr
		}
		// P1: after sustained storage pressure wait, emit one deadlock line.
		if shouldEmitStorageDeadlockAdvice(time.Since(waitStarted), deadlockAdviceEmitted, decision.Dimension) {
			advice := storageDeadlockAdviceFromAdmission(admission)
			if advice != "" && reporter != nil && reporter.stderr != nil {
				fmt.Fprintln(reporter.stderr, "ndev session pressure:", advice)
			}
			deadlockAdviceEmitted = true
		}
		if !wait {
			return admission, fmt.Errorf("heavy work blocked at %s/%s: %s", admission.Level, decision.Dimension, decision.Reason)
		}
		interval := retryInterval
		if decision.RetryInterval > 0 && (interval <= 0 || decision.RetryInterval < interval) {
			interval = decision.RetryInterval
		}
		reason := decision.Reason
		if deadlockAdviceEmitted && decision.Dimension == "storage" {
			if advice := storageDeadlockAdviceFromAdmission(admission); advice != "" {
				reason = advice
			}
		}
		reporter.emit(WorkProgress{
			Stage: "waiting", Blocker: WorkBlockerPressure, PressureLevel: admission.Level,
			PressureDimension: decision.Dimension, Reason: reason, NextCheckSeconds: interval.Seconds(),
		})
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return admission, fmt.Errorf("wait for host pressure admission: %w", ctx.Err())
		case <-timer.C:
		}
	}
}

func storageDeadlockAdviceFromAdmission(admission Admission) string {
	// The advice line must quote the PERSISTED release threshold: quoting the
	// compiled default here once printed "release=30 GiB need≈0 B" while the
	// live policy released at 35 GiB — an actively misleading diagnostic that
	// made a working hysteresis band look like a deadlock bug.
	policy := DefaultPolicy(16 << 10).Storage
	if dir, err := DataDir(); err == nil {
		if persistedPolicy, persisted, loadErr := LoadPolicy(PolicyPath(dir), 0); loadErr == nil && persisted {
			policy = persistedPolicy.Storage
		}
	}
	if admission.Snapshot == nil {
		return FormatStorageDeadlockAdvice(StorageSnapshot{}, policy)
	}
	return FormatStorageDeadlockAdvice(admission.Snapshot.Storage, policy)
}

func configuredWorkStorageGrace(dir string) time.Duration {
	grace := 60 * time.Second
	policy, persisted, err := LoadPolicy(PolicyPath(dir), 0)
	if err == nil && persisted && policy.Storage.WorkWaitGraceSeconds > 0 {
		grace = time.Duration(policy.Storage.WorkWaitGraceSeconds) * time.Second
	}
	return grace
}

func configuredWorkCPURedPercent(dir string) float64 {
	red := 95.0
	policy, persisted, err := LoadPolicy(PolicyPath(dir), 0)
	if err == nil && persisted && policy.Thresholds.HostCPURedPercent > 0 {
		red = policy.Thresholds.HostCPURedPercent
	}
	return red
}
