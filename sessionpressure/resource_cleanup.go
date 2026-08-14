package sessionpressure

import (
	"context"
	"fmt"
	"os"
	"sort"
	"syscall"
	"time"
)

// ResourceCleanupResult is the privacy-safe bridge between the resident
// pressure authority and optional typed resource reclaimers. Provider-specific
// detail belongs in the reclaimer's own audit ledger, not pressure telemetry.
type ResourceCleanupResult struct {
	Attempted         bool    `json:"attempted"`
	Acted             bool    `json:"acted"`
	ResourceKind      string  `json:"resource_kind,omitempty"`
	ResourceID        string  `json:"resource_id,omitempty"`
	Result            string  `json:"result,omitempty"`
	ControlExecuted   bool    `json:"control_executed,omitempty"`
	ControlDurationMS float64 `json:"control_duration_ms,omitempty"`
	ControlMaxRSSMB   float64 `json:"control_max_rss_mb,omitempty"`
}

// ResourceCleaner may reclaim at most one typed, stale resource after the
// monitor has established sustained host pressure. Implementations must load
// their own persisted policy, claims, cooldown history, and final resource
// identity before acting.
type ResourceCleaner interface {
	MaybeRelieve(context.Context, Snapshot) (ResourceCleanupResult, error)
}

// ClaimedProcessExpectation is the complete destructive authority for a
// generic process claim. ProcessIdentity is a kernel start identity captured
// when the claim is acquired; PID liveness by itself is never sufficient.
type ClaimedProcessExpectation struct {
	RootPID         int
	ProcessIdentity string
	MaxCPUPercent   float64
}

// ClaimedProcessResult is a bounded, command-free cleanup result.
type ClaimedProcessResult struct {
	RootPID       int     `json:"root_pid"`
	ProcessCount  int     `json:"process_count"`
	RSSSumMB      float64 `json:"rss_sum_mb"`
	CPUPercentSum float64 `json:"cpu_percent_sum"`
	CPUAvailable  bool    `json:"cpu_available"`
	Signal        string  `json:"signal,omitempty"`
	Result        string  `json:"result"`
}

// CaptureProcessIdentity returns the kernel-backed identity used by resource
// claims to reject PID reuse at every destructive boundary.
func CaptureProcessIdentity(pid int) (string, error) {
	if pid <= 0 {
		return "", fmt.Errorf("PID must be positive")
	}
	return processStartIdentity(pid)
}

type claimedProcessInspection struct {
	Result     ClaimedProcessResult
	PIDs       []int
	Identities map[int]string
}

// InspectClaimedProcessTree performs the same identity, descendant, caller,
// and CPU-idleness checks as the destructive path without sending a signal.
// Planners use it to keep dead or active stale claims from consuming the
// global cleanup cooldown ahead of a genuinely reclaimable resource.
func InspectClaimedProcessTree(ctx context.Context, expected ClaimedProcessExpectation) (ClaimedProcessResult, error) {
	inspection, err := inspectClaimedProcessTree(ctx, expected)
	return inspection.Result, err
}

// ReapClaimedProcessTree re-enumerates the complete descendant tree, verifies
// root identity and current CPU idleness, refuses the caller's own tree, and
// sends one leaf-first SIGTERM. There is deliberately no SIGKILL escalation.
func ReapClaimedProcessTree(ctx context.Context, expected ClaimedProcessExpectation) (ClaimedProcessResult, error) {
	inspection, err := inspectClaimedProcessTree(ctx, expected)
	result := inspection.Result
	if err != nil {
		return result, err
	}
	pids := inspection.PIDs

	// Recheck the root after the inventory and before the first signal. Child
	// lists were sorted before the preorder walk; signalTreePIDs reverses that
	// traversal so every descendant is signaled before its parent.
	identity, err := processStartIdentity(expected.RootPID)
	if err != nil || identity != expected.ProcessIdentity {
		return result, fmt.Errorf("process root %d identity changed before signal", expected.RootPID)
	}
	result.Signal = "SIGTERM"
	result.Result = "signal_sent"
	if err := signalClaimedProcessTree(pids, inspection.Identities, nil, nil); err != nil {
		result.Result = "error"
		return result, err
	}
	if confirmTreeExit(pids, 2*time.Second) {
		result.Result = "tree_exit_confirmed"
	} else {
		result.Result = "signal_sent_unconfirmed"
	}
	return result, nil
}

func inspectClaimedProcessTree(ctx context.Context, expected ClaimedProcessExpectation) (claimedProcessInspection, error) {
	result := ClaimedProcessResult{RootPID: expected.RootPID, Result: "revalidation_rejected"}
	if expected.RootPID <= 0 || expected.ProcessIdentity == "" {
		return claimedProcessInspection{Result: result}, fmt.Errorf("exact root PID and process identity are required")
	}
	if expected.MaxCPUPercent < 0 || expected.MaxCPUPercent > 10 {
		return claimedProcessInspection{Result: result}, fmt.Errorf("max CPU percent must be between 0 and 10")
	}
	identity, err := processStartIdentity(expected.RootPID)
	if err != nil || identity != expected.ProcessIdentity {
		return claimedProcessInspection{Result: result}, fmt.Errorf("process root %d identity changed", expected.RootPID)
	}

	sampler := NewSampler()
	policy := DefaultPolicy(16 << 10)
	processes, _, _, _, err := sampler.processInventory(ctx, policy, true)
	if err != nil {
		return claimedProcessInspection{Result: result}, fmt.Errorf("sample claimed process tree: %w", err)
	}
	byPID := make(map[int]Process, len(processes))
	children := make(map[int][]int, len(processes))
	for _, process := range processes {
		byPID[process.PID] = process
		children[process.PPID] = append(children[process.PPID], process.PID)
	}
	for parent := range children {
		sort.Ints(children[parent])
	}
	_, found := byPID[expected.RootPID]
	if !found {
		return claimedProcessInspection{Result: result}, fmt.Errorf("process root %d disappeared", expected.RootPID)
	}

	pids := make([]int, 0, 8)
	identities := make(map[int]string, 8)
	visited := make(map[int]bool)
	var identityErr error
	var walk func(int)
	walk = func(pid int) {
		if visited[pid] || identityErr != nil {
			return
		}
		visited[pid] = true
		process, ok := byPID[pid]
		if !ok {
			return
		}
		identity, err := processStartIdentity(pid)
		if err != nil {
			identityErr = fmt.Errorf("capture process %d identity during inventory: %w", pid, err)
			return
		}
		if pid == expected.RootPID && identity != expected.ProcessIdentity {
			identityErr = fmt.Errorf("process root %d identity changed during inventory", expected.RootPID)
			return
		}
		pids = append(pids, pid)
		identities[pid] = identity
		result.ProcessCount++
		result.RSSSumMB += float64(process.RSSKB) / 1024
		result.CPUPercentSum += process.CPUPercent
		if result.ProcessCount == 1 {
			result.CPUAvailable = true
		}
		if !validCPUEvidence(process.CPUAvailable, process.CPUPercent) {
			result.CPUAvailable = false
		}
		for _, child := range children[pid] {
			walk(child)
		}
	}
	walk(expected.RootPID)
	if identityErr != nil {
		return claimedProcessInspection{Result: result}, identityErr
	}
	if len(pids) == 0 {
		return claimedProcessInspection{Result: result}, fmt.Errorf("process root %d has no signalable projection", expected.RootPID)
	}
	if containsPID(pids, os.Getpid()) {
		return claimedProcessInspection{Result: result}, fmt.Errorf("refusing to terminate the caller's own process tree")
	}
	if !result.CPUAvailable {
		return claimedProcessInspection{Result: result}, fmt.Errorf("process root %d has no valid current CPU activity evidence", expected.RootPID)
	}
	if result.CPUPercentSum > expected.MaxCPUPercent {
		return claimedProcessInspection{Result: result}, fmt.Errorf("process root %d is active at %.2f%% CPU above the %.2f%% ceiling", expected.RootPID, result.CPUPercentSum, expected.MaxCPUPercent)
	}
	result.Result = "eligible"
	return claimedProcessInspection{Result: result, PIDs: pids, Identities: identities}, nil
}

// signalClaimedProcessTree binds every descendant PID to the kernel start
// identity captured during inventory. The first pass rejects reuse before any
// signal; the second repeats the check immediately before each leaf-first
// SIGTERM so a disappearing child is skipped rather than targeting its reuse.
func signalClaimedProcessTree(pids []int, identities map[int]string, readIdentity func(int) (string, error), signalPID func(int) error) error {
	if readIdentity == nil {
		readIdentity = processStartIdentity
	}
	if signalPID == nil {
		signalPID = func(pid int) error {
			process, err := os.FindProcess(pid)
			if err != nil {
				return err
			}
			return process.Signal(syscall.SIGTERM)
		}
	}
	live := make(map[int]bool, len(pids))
	for _, pid := range pids {
		want, ok := identities[pid]
		if !ok || want == "" {
			return fmt.Errorf("process %d has no captured start identity", pid)
		}
		got, err := readIdentity(pid)
		if err != nil {
			if !processAlive(pid) {
				continue
			}
			return fmt.Errorf("revalidate process %d identity: %w", pid, err)
		}
		if got != want {
			return fmt.Errorf("process %d identity changed before signal", pid)
		}
		live[pid] = true
	}
	return signalTreePIDs(pids, func(pid int) error {
		if !live[pid] {
			return nil
		}
		got, err := readIdentity(pid)
		if err != nil {
			if !processAlive(pid) {
				return nil
			}
			return fmt.Errorf("revalidate process %d identity at signal: %w", pid, err)
		}
		if got != identities[pid] {
			return fmt.Errorf("process %d identity changed at signal boundary", pid)
		}
		return signalPID(pid)
	})
}
