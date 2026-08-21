package sessionpressure

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	workInspectSchemaVersion = 1
	workInspectCommandMax    = 512
	workInspectLogMax        = 2048
	workInspectTermGrace     = 2 * time.Second

	StallRunning    = "running"
	StallWaitingIO  = "waiting_io"
	StallParentDead = "parent_dead"
	StallNoProgress = "no_progress"
	StallDead       = "dead"
	StallUnknown    = "unknown"
)

// WorkInspectReport is an operator-initiated live sample. Argv and cwd are
// returned here only; they are never written to work-events.
type WorkInspectReport struct {
	SchemaVersion    int                    `json:"schema_version"`
	GeneratedAt      time.Time              `json:"generated_at"`
	OperationID      string                 `json:"operation_id"`
	Lease            *WorkLeaseStatus       `json:"lease,omitempty"`
	Waiter           *WorkWaiterStatus      `json:"waiter,omitempty"`
	Process          *WorkProcessSnapshot   `json:"process,omitempty"`
	Origin           WorkInspectOrigin      `json:"origin"`
	Calibration      WorkInspectCalibration `json:"calibration"`
	StallClass       string                 `json:"stall_class"`
	Summary          string                 `json:"summary"`
	SuggestedActions []WorkInspectAction    `json:"suggested_actions"`
}

type WorkProcessSnapshot struct {
	PID         int                   `json:"pid"`
	PPID        int                   `json:"ppid,omitempty"`
	PGID        int                   `json:"pgid,omitempty"`
	State       string                `json:"state,omitempty"`
	CPUPercent  float64               `json:"cpu_percent,omitempty"`
	RSSKB       int64                 `json:"rss_kb,omitempty"`
	Command     string                `json:"command,omitempty"`
	CWD         string                `json:"cwd,omitempty"`
	Truncated   bool                  `json:"command_truncated,omitempty"`
	Alive       bool                  `json:"alive"`
	ParentDead  bool                  `json:"parent_dead,omitempty"`
	Children    []WorkProcessSnapshot `json:"children,omitempty"`
	ParentChain []WorkProcessRef      `json:"parent_chain,omitempty"`
}

type WorkProcessRef struct {
	PID     int    `json:"pid"`
	Command string `json:"command,omitempty"`
}

type WorkInspectOrigin struct {
	Kind            string `json:"kind"`
	ScheduleID      string `json:"schedule_id,omitempty"`
	ScheduleName    string `json:"schedule_name,omitempty"`
	ScheduleRunID   string `json:"schedule_run_id,omitempty"`
	ScheduleSnippet string `json:"schedule_snippet,omitempty"`
	AgentKind       string `json:"agent_kind,omitempty"`
}

type WorkInspectCalibration struct {
	Class             WorkClass `json:"class,omitempty"`
	AgeMS             int64     `json:"age_ms"`
	ClassP95RuntimeMS int64     `json:"class_p95_runtime_ms,omitempty"`
	ClassP95Known     bool      `json:"class_p95_known"`
	AgeToP95Ratio     float64   `json:"age_to_p95_ratio,omitempty"`
	FiniteReviewAgeMS int64     `json:"finite_review_age_ms"`
}

type WorkInspectAction struct {
	ID        string `json:"id"`
	Available bool   `json:"available"`
	Reason    string `json:"reason,omitempty"`
	Command   string `json:"command,omitempty"`
}

type WorkCancelResult struct {
	SchemaVersion int               `json:"schema_version"`
	OperationID   string            `json:"operation_id"`
	SignaledPIDs  []int             `json:"signaled_pids"`
	StallClass    string            `json:"stall_class"`
	Work          WorkStatus        `json:"work"`
	Inspect       WorkInspectReport `json:"inspect"`
}

type WorkHandoffResult struct {
	SchemaVersion string `json:"schema_version,omitempty"`
	OperationID   string `json:"operation_id"`
	Path          string `json:"path"`
	Prompt        string `json:"prompt"`
	Suggested     string `json:"suggested_command,omitempty"`
}

var (
	errWorkInspectNotFound = errors.New("work inspect: operation is not a live lease or waiter")
	lookupScheduleName     = lookupScheduleNameFromRegistry
	readScheduleSnippet    = readScheduleOutputSnippet
	runScheduleNowFn       = runScheduleNow
	readProcessEnv         = processEnvKeys
)

// InspectWork samples the live process tree for one coordinator operation.
func InspectWork(ctx context.Context, coordinator *WorkCoordinator, operationID string) (WorkInspectReport, error) {
	if coordinator == nil {
		return WorkInspectReport{}, errors.New("work inspect requires a coordinator")
	}
	operationID = strings.TrimSpace(operationID)
	if !validPrivateID(operationID) {
		return WorkInspectReport{}, errors.New("work inspect --operation-id must be a 32-character lowercase hex identity")
	}
	status, err := coordinator.Status(ctx)
	if err != nil {
		return WorkInspectReport{}, err
	}
	report := WorkInspectReport{
		SchemaVersion: workInspectSchemaVersion,
		GeneratedAt:   coordinator.now(),
		OperationID:   operationID,
	}
	var pid int
	var class WorkClass
	var ageMS int64
	for i := range status.Leases {
		if status.Leases[i].OperationID == operationID {
			lease := status.Leases[i]
			report.Lease = &lease
			pid = lease.PID
			class = lease.Class
			ageMS = lease.AgeMS
			break
		}
	}
	if report.Lease == nil {
		for i := range status.Waiters {
			if status.Waiters[i].OperationID == operationID {
				waiter := status.Waiters[i]
				report.Waiter = &waiter
				pid = waiter.PID
				class = waiter.Class
				ageMS = waiter.WaitMS
				break
			}
		}
	}
	if report.Lease == nil && report.Waiter == nil {
		return WorkInspectReport{}, fmt.Errorf("%w: %s", errWorkInspectNotFound, operationID)
	}

	proc := snapshotProcessTree(pid)
	if proc != nil && report.Lease != nil {
		if supervisor := leaseSupervisorPID(coordinator, operationID); supervisor > 1 {
			if err := syscall.Kill(supervisor, 0); err != nil {
				proc.ParentDead = true
			}
		}
	}
	report.Process = proc
	env := readProcessEnv(pid, []string{
		"NDEV_SCHEDULE_ID", "NDEV_SCHEDULE_RUN_ID", "NDEV_SCHEDULE_TRIGGER",
		"CODEX_THREAD_ID", "CLAUDE_SESSION_ID", "GROK_SESSION_ID", "KIMI_SESSION_ID",
	})
	report.Origin = classifyInspectOrigin(env)
	report.Calibration = inspectCalibration(coordinator.Dir, class, ageMS)
	report.StallClass = classifyStall(proc, report.Calibration)
	report.Summary = inspectSummary(report)
	report.SuggestedActions = inspectActions(report)
	return report, nil
}

func leaseSupervisorPID(coordinator *WorkCoordinator, operationID string) int {
	if coordinator == nil {
		return 0
	}
	pid := 0
	_, _ = coordinator.withState(context.Background(), func(state *workState) error {
		for _, lease := range state.Leases {
			if lease.OperationID == operationID {
				pid = lease.SupervisorPID
				return nil
			}
		}
		return nil
	})
	return pid
}

func inspectCalibration(dir string, class WorkClass, ageMS int64) WorkInspectCalibration {
	cal := WorkInspectCalibration{
		Class: class, AgeMS: ageMS,
		FiniteReviewAgeMS: WorkFiniteLeaseReviewAge.Milliseconds(),
	}
	if p95, ok := calibratedClassRuntimeP95MS(dir, class, time.Now); ok {
		cal.ClassP95RuntimeMS = p95
		cal.ClassP95Known = true
		if p95 > 0 {
			cal.AgeToP95Ratio = float64(ageMS) / float64(p95)
		}
	}
	return cal
}

func classifyStall(proc *WorkProcessSnapshot, cal WorkInspectCalibration) string {
	if proc == nil || !proc.Alive {
		return StallDead
	}
	if proc.ParentDead {
		return StallParentDead
	}
	state := strings.ToUpper(proc.State)
	if strings.Contains(state, "D") || strings.Contains(state, "U") {
		return StallWaitingIO
	}
	if cal.ClassP95Known && cal.AgeToP95Ratio >= 3 && proc.CPUPercent < 1 {
		return StallNoProgress
	}
	if proc.CPUPercent >= 1 {
		return StallRunning
	}
	if cal.ClassP95Known && cal.AgeToP95Ratio >= 3 {
		return StallNoProgress
	}
	return StallUnknown
}

func inspectSummary(report WorkInspectReport) string {
	role := "queued"
	age := time.Duration(report.Calibration.AgeMS) * time.Millisecond
	if report.Lease != nil {
		role = "lease"
	}
	cmd := ""
	pid := 0
	if report.Process != nil {
		cmd = report.Process.Command
		pid = report.Process.PID
	}
	if len(cmd) > 80 {
		cmd = cmd[:77] + "..."
	}
	origin := report.Origin.Kind
	if report.Origin.ScheduleID != "" {
		origin = "schedule " + report.Origin.ScheduleID
		if report.Origin.ScheduleName != "" {
			origin = report.Origin.ScheduleName
		}
	}
	p95 := "unknown class p95"
	if report.Calibration.ClassP95Known {
		p95 = "class p95 " + (time.Duration(report.Calibration.ClassP95RuntimeMS) * time.Millisecond).Round(time.Second).String()
	}
	return fmt.Sprintf("%s %s pid=%d age=%s vs %s. stall=%s. origin=%s. cmd=%s",
		role, report.Calibration.Class, pid, age.Round(time.Second), p95, report.StallClass, origin, cmd)
}

func inspectActions(report WorkInspectReport) []WorkInspectAction {
	live := report.Lease != nil && report.Process != nil && report.Process.Alive
	cancel := WorkInspectAction{
		ID: "cancel", Available: live,
		Command: "ndev --json session pressure work cancel --operation-id " + report.OperationID + " --confirm",
	}
	if !live {
		cancel.Reason = "no live lease process to signal"
	}
	reschedule := WorkInspectAction{
		ID: "reschedule", Available: report.Origin.ScheduleID != "",
		Command: "ndev --json session pressure work reschedule --operation-id " + report.OperationID + " --confirm",
	}
	if report.Origin.ScheduleID == "" {
		reschedule.Reason = "not a scheduled job"
	}
	handoff := WorkInspectAction{
		ID: "handoff", Available: true,
		Command: "ndev --json session pressure work handoff --operation-id " + report.OperationID,
	}
	return []WorkInspectAction{cancel, reschedule, handoff}
}

func classifyInspectOrigin(env map[string]string) WorkInspectOrigin {
	origin := WorkInspectOrigin{Kind: "ad_hoc"}
	if id := strings.TrimSpace(env["NDEV_SCHEDULE_ID"]); id != "" {
		origin.Kind = "schedule"
		origin.ScheduleID = id
		origin.ScheduleRunID = strings.TrimSpace(env["NDEV_SCHEDULE_RUN_ID"])
		origin.ScheduleName, origin.ScheduleSnippet = lookupScheduleName(id, origin.ScheduleRunID)
	}
	switch {
	case strings.TrimSpace(env["GROK_SESSION_ID"]) != "":
		origin.AgentKind = "grok"
	case strings.TrimSpace(env["CODEX_THREAD_ID"]) != "":
		origin.AgentKind = "codex"
	case strings.TrimSpace(env["CLAUDE_SESSION_ID"]) != "":
		origin.AgentKind = "claude"
	case strings.TrimSpace(env["KIMI_SESSION_ID"]) != "":
		origin.AgentKind = "kimi"
	}
	if origin.Kind == "ad_hoc" && origin.AgentKind != "" {
		origin.Kind = "agent"
	}
	return origin
}

func lookupScheduleNameFromRegistry(id, runID string) (string, string) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", readScheduleOutputSnippet(runID)
	}
	raw, err := os.ReadFile(filepath.Join(home, ".nicos-dev", "scheduler", "schedules.json"))
	if err != nil {
		return "", readScheduleOutputSnippet(runID)
	}
	name := ""
	// Tiny scan: avoid a JSON import cycle of operator registry schema.
	needle := `"id": "` + id + `"`
	idx := strings.Index(string(raw), needle)
	if idx >= 0 {
		window := string(raw[idx:])
		if n := strings.Index(window, `"name": "`); n >= 0 {
			rest := window[n+len(`"name": "`):]
			if end := strings.Index(rest, `"`); end >= 0 {
				name = rest[:end]
			}
		}
	}
	return name, readScheduleOutputSnippet(runID)
}

func readScheduleOutputSnippet(runID string) string {
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return ""
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	base := filepath.Join(home, ".nicos-dev", "scheduler", "outputs")
	var parts []string
	for _, name := range []string{runID + ".err.log", runID + ".out.log"} {
		raw, err := os.ReadFile(filepath.Join(base, name))
		if err != nil || len(raw) == 0 {
			continue
		}
		text := strings.TrimSpace(string(raw))
		if len(text) > workInspectLogMax {
			text = text[len(text)-workInspectLogMax:]
		}
		parts = append(parts, text)
	}
	return strings.TrimSpace(strings.Join(parts, "\n"))
}

// CancelWork signals the live lease process tree and lets the coordinator prune
// dead owners. It never writes argv to the work-event ledger.
func CancelWork(ctx context.Context, coordinator *WorkCoordinator, operationID string) (WorkCancelResult, error) {
	report, err := InspectWork(ctx, coordinator, operationID)
	if err != nil {
		return WorkCancelResult{}, err
	}
	if report.Lease == nil {
		return WorkCancelResult{}, errors.New("work cancel applies to a live lease, not a queued waiter")
	}
	pids := inspectTreePIDs(report)
	signalProcessPIDs(pids, syscall.SIGTERM)
	deadline := time.Now().Add(workInspectTermGrace)
	for time.Now().Before(deadline) {
		if !anyPIDAlive(pids) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	signalProcessPIDs(pids, syscall.SIGKILL)
	status, err := coordinator.DropLease(ctx, operationID)
	if err != nil {
		return WorkCancelResult{}, err
	}
	return WorkCancelResult{
		SchemaVersion: workInspectSchemaVersion,
		OperationID:   operationID,
		SignaledPIDs:  pids,
		StallClass:    report.StallClass,
		Work:          status,
		Inspect:       report,
	}, nil
}

func inspectTreePIDs(report WorkInspectReport) []int {
	seen := map[int]struct{}{}
	var pids []int
	add := func(pid int) {
		if pid <= 1 {
			return
		}
		if _, ok := seen[pid]; ok {
			return
		}
		seen[pid] = struct{}{}
		pids = append(pids, pid)
	}
	if report.Lease != nil {
		add(report.Lease.PID)
	}
	if report.Process != nil {
		add(report.Process.PID)
		for _, child := range report.Process.Children {
			add(child.PID)
		}
		for _, ancestor := range descendantPIDs(report.Process.PID) {
			add(ancestor)
		}
	}
	return pids
}

func signalProcessPIDs(pids []int, sig syscall.Signal) {
	for i := len(pids) - 1; i >= 0; i-- {
		pid := pids[i]
		_ = syscall.Kill(-pid, sig)
		_ = syscall.Kill(pid, sig)
	}
}

func anyPIDAlive(pids []int) bool {
	for _, pid := range pids {
		if err := syscall.Kill(pid, 0); err == nil {
			return true
		}
	}
	return false
}

// HandoffWork writes an operator-local inspect brief. This is not telemetry.
func HandoffWork(ctx context.Context, coordinator *WorkCoordinator, operationID string) (WorkHandoffResult, error) {
	report, err := InspectWork(ctx, coordinator, operationID)
	if err != nil {
		return WorkHandoffResult{}, err
	}
	dir, err := DataDir()
	if err != nil {
		dir = coordinator.Dir
	}
	outDir := filepath.Join(dir, "inspect")
	if err := os.MkdirAll(outDir, 0o700); err != nil {
		return WorkHandoffResult{}, err
	}
	path := filepath.Join(outDir, operationID+".md")
	body := formatInspectBrief(report)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		return WorkHandoffResult{}, err
	}
	return WorkHandoffResult{
		OperationID: operationID,
		Path:        path,
		Prompt:      body,
		Suggested:   "Paste this brief into an interactive agent and ask it to stop, reschedule, or fix the stall.",
	}, nil
}

func formatInspectBrief(report WorkInspectReport) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Work inspect %s\n\n", report.OperationID)
	fmt.Fprintf(&b, "%s\n\n", report.Summary)
	fmt.Fprintf(&b, "- stall: %s\n", report.StallClass)
	fmt.Fprintf(&b, "- origin: %s\n", report.Origin.Kind)
	if report.Origin.ScheduleID != "" {
		fmt.Fprintf(&b, "- schedule: %s (%s) run=%s\n", report.Origin.ScheduleID, report.Origin.ScheduleName, report.Origin.ScheduleRunID)
	}
	if report.Origin.AgentKind != "" {
		fmt.Fprintf(&b, "- agent: %s\n", report.Origin.AgentKind)
	}
	if report.Process != nil {
		fmt.Fprintf(&b, "- pid: %d ppid=%d state=%s cpu=%.1f rss_kb=%d\n", report.Process.PID, report.Process.PPID, report.Process.State, report.Process.CPUPercent, report.Process.RSSKB)
		fmt.Fprintf(&b, "- command: %s\n", report.Process.Command)
		if report.Process.CWD != "" {
			fmt.Fprintf(&b, "- cwd: %s\n", report.Process.CWD)
		}
	}
	if report.Origin.ScheduleSnippet != "" {
		fmt.Fprintf(&b, "\n## Last schedule output\n\n```\n%s\n```\n", report.Origin.ScheduleSnippet)
	}
	fmt.Fprintf(&b, "\n## Suggested actions\n")
	for _, action := range report.SuggestedActions {
		fmt.Fprintf(&b, "- %s available=%v %s\n", action.ID, action.Available, action.Command)
	}
	return b.String()
}

// RescheduleWork cancels a scheduled lease then asks ndev to run the job now.
func RescheduleWork(ctx context.Context, coordinator *WorkCoordinator, operationID string) (map[string]any, error) {
	report, err := InspectWork(ctx, coordinator, operationID)
	if err != nil {
		return nil, err
	}
	if report.Origin.ScheduleID == "" {
		return nil, errors.New("work reschedule requires a scheduled origin (NDEV_SCHEDULE_ID)")
	}
	cancel, err := CancelWork(ctx, coordinator, operationID)
	if err != nil {
		return nil, err
	}
	runErr := runScheduleNowFn(report.Origin.ScheduleID)
	result := map[string]any{
		"ok":            runErr == nil,
		"action":        "work.reschedule",
		"operation_id":  operationID,
		"schedule_id":   report.Origin.ScheduleID,
		"cancelled":     cancel,
		"run_now_error": nil,
	}
	if runErr != nil {
		result["run_now_error"] = runErr.Error()
		result["suggested_command"] = "ndev schedule run-now " + report.Origin.ScheduleID
		return result, runErr
	}
	return result, nil
}

func runScheduleNow(id string) error {
	ndev, err := exec.LookPath("ndev")
	if err != nil {
		return fmt.Errorf("ndev not on PATH: %w", err)
	}
	cmd := exec.Command(ndev, "schedule", "run-now", id)
	cmd.Env = os.Environ()
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("ndev schedule run-now %s: %w\n%s", id, err, strings.TrimSpace(string(out)))
	}
	return nil
}

func truncateCommand(raw string) (string, bool) {
	clean := strings.Join(strings.Fields(raw), " ")
	if len(clean) <= workInspectCommandMax {
		return clean, false
	}
	return strings.TrimSpace(clean[:workInspectCommandMax-3]) + "...", true
}

func parsePSInt(raw string) int {
	n, _ := strconv.Atoi(strings.TrimSpace(raw))
	return n
}
