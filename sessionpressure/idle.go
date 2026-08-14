package sessionpressure

import (
	"context"
	"fmt"
	"math"
	"os"
	"sort"
	"time"
)

const (
	DefaultIdleMinAge         = 12 * time.Hour
	DefaultIdleMaxCPUPercent  = 0.25
	DefaultIdleCandidateLimit = 10
)

// IdleCriteria defines the deliberately conservative operator-facing idle
// lifecycle boundary. It is independent of critical-pressure auto-shedding:
// merely being old never grants action authority.
type IdleCriteria struct {
	MinAge        time.Duration
	MaxCPUPercent float64
	Limit         int
}

func DefaultIdleCriteria() IdleCriteria {
	return IdleCriteria{
		MinAge:        DefaultIdleMinAge,
		MaxCPUPercent: DefaultIdleMaxCPUPercent,
		Limit:         DefaultIdleCandidateLimit,
	}
}

func (criteria IdleCriteria) Validate() error {
	if criteria.MinAge < time.Hour {
		return fmt.Errorf("idle minimum age must be at least 1h")
	}
	if math.IsNaN(criteria.MaxCPUPercent) || math.IsInf(criteria.MaxCPUPercent, 0) || criteria.MaxCPUPercent < 0 || criteria.MaxCPUPercent > 10 {
		return fmt.Errorf("idle CPU ceiling must be between 0 and 10 percent")
	}
	if criteria.Limit < 1 || criteria.Limit > 50 {
		return fmt.Errorf("idle candidate limit must be between 1 and 50")
	}
	return nil
}

// IdleCandidate is a bounded, prompt-free projection. The private tree is
// retained only long enough to perform same-command final revalidation.
type IdleCandidate struct {
	Agent          string  `json:"agent"`
	RootPID        int     `json:"root_pid"`
	SessionID      string  `json:"session_id"`
	Executable     string  `json:"executable"`
	ProcessCount   int     `json:"process_count"`
	RSSSumMB       float64 `json:"rss_sum_mb"`
	CPUPercentSum  float64 `json:"cpu_percent_sum"`
	CPUAvailable   bool    `json:"cpu_available"`
	ElapsedSeconds int64   `json:"elapsed_seconds"`

	tree AgentTree
}

type IdleInventory struct {
	Candidates     []IdleCandidate `json:"candidates"`
	CandidateCount int             `json:"candidate_count"`
	ReturnedCount  int             `json:"returned_count"`
	Truncated      bool            `json:"truncated"`
}

// InspectIdleTrees requires a fresh process inventory and excludes the tree
// containing selfPID. That prevents an ndev process launched from an agent
// shell from ever nominating or terminating its own parent session.
func InspectIdleTrees(snapshot Snapshot, criteria IdleCriteria, selfPID int) (IdleInventory, error) {
	if err := criteria.Validate(); err != nil {
		return IdleInventory{}, err
	}
	if !snapshot.ProcessInventoryAvailable || !snapshot.ProcessInventoryFresh {
		return IdleInventory{}, fmt.Errorf("fresh process inventory is required")
	}
	eligible := make([]IdleCandidate, 0, len(snapshot.TopAgentTrees))
	for _, tree := range snapshot.TopAgentTrees {
		if tree.SessionID == "" || tree.ElapsedSeconds < int64(criteria.MinAge/time.Second) || !validCPUEvidence(tree.CPUAvailable, tree.CPUPercentSum) ||
			tree.CPUPercentSum > criteria.MaxCPUPercent || containsPID(tree.PIDs, selfPID) {
			continue
		}
		eligible = append(eligible, idleCandidate(tree))
	}
	sort.Slice(eligible, func(i, j int) bool {
		if eligible[i].ElapsedSeconds != eligible[j].ElapsedSeconds {
			return eligible[i].ElapsedSeconds > eligible[j].ElapsedSeconds
		}
		if eligible[i].RSSSumMB != eligible[j].RSSSumMB {
			return eligible[i].RSSSumMB > eligible[j].RSSSumMB
		}
		return eligible[i].RootPID < eligible[j].RootPID
	})
	total := len(eligible)
	if len(eligible) > criteria.Limit {
		eligible = eligible[:criteria.Limit]
	}
	return IdleInventory{
		Candidates: eligible, CandidateCount: total, ReturnedCount: len(eligible), Truncated: total > len(eligible),
	}, nil
}

func idleCandidate(tree AgentTree) IdleCandidate {
	return IdleCandidate{
		Agent: tree.Agent, RootPID: tree.RootPID, SessionID: tree.SessionID,
		Executable: tree.Executable, ProcessCount: tree.ProcessCount,
		RSSSumMB: tree.RSSSumMB, CPUPercentSum: tree.CPUPercentSum, CPUAvailable: tree.CPUAvailable,
		ElapsedSeconds: tree.ElapsedSeconds, tree: tree,
	}
}

// NewIdleReapIntent returns the durable audit row that must be persisted before
// manual cleanup can enter final revalidation or signal a process. The result
// row is appended separately after the attempt, leaving an explicit intent
// behind even if result persistence fails after SIGTERM.
func NewIdleReapIntent(candidate IdleCandidate, level Level, now time.Time) Action {
	return Action{
		SchemaVersion: SchemaVersion, Timestamp: now.UTC(), Kind: "manual_idle_tree_reap_intent", Level: level,
		RootPID: candidate.RootPID, Agent: candidate.Agent, SessionID: candidate.SessionID,
		RSSSumMB: candidate.RSSSumMB, Result: "intent_recorded",
		Reason: "operator-confirmed idle lifecycle cleanup",
	}
}

func containsPID(pids []int, want int) bool {
	if want <= 0 {
		return false
	}
	for _, pid := range pids {
		if pid == want {
			return true
		}
	}
	return false
}

// ReapIdleTree performs a second complete sample and sends one graceful signal
// only when identity, descendants, age, and CPU activity still match. The
// caller must have selected an exact candidate and is responsible for
// persisting the returned action audit row.
func ReapIdleTree(ctx context.Context, sampler *Sampler, policy Policy, expected IdleCandidate, criteria IdleCriteria) (Action, error) {
	now := time.Now().UTC()
	action := Action{
		SchemaVersion: SchemaVersion, Timestamp: now, Kind: "manual_idle_tree_reap", Level: LevelNormal,
		RootPID: expected.RootPID, Agent: expected.Agent, SessionID: expected.SessionID,
		RSSSumMB: expected.RSSSumMB, Result: "revalidation_rejected",
		Reason: "operator-confirmed idle lifecycle cleanup",
	}
	if sampler == nil {
		sampler = NewSampler()
	}
	if err := criteria.Validate(); err != nil {
		action.Reason = err.Error()
		return action, err
	}
	if expected.RootPID <= 0 || expected.SessionID == "" || expected.tree.RootPID == 0 {
		err := fmt.Errorf("exact root PID and session ID from the current idle inventory are required")
		action.Reason = err.Error()
		return action, err
	}
	snapshot, err := sampler.Sample(ctx, policy)
	if snapshot.Level != "" {
		action.Level = snapshot.Level
		action.RevalidatedLevel = snapshot.Level
	}
	action.RevalidationDurationMS = snapshot.SampleDurationMS
	action.RevalidationCPUTimeMS = snapshot.SampleCPUTimeMS
	action.RevalidationGuardRSSMB = snapshot.GuardRSSMB
	action.RevalidationPeakRSSMB = snapshot.GuardPeakRSSMB
	if err != nil {
		action.Result = "error"
		action.Reason = "final sample failed: " + err.Error()
		return action, fmt.Errorf("final idle revalidation: %w", err)
	}
	if !snapshot.ProcessInventoryAvailable || !snapshot.ProcessInventoryFresh {
		err = fmt.Errorf("final process inventory is not fresh")
		action.Reason = err.Error()
		return action, err
	}
	var current AgentTree
	found := false
	for _, tree := range snapshot.TopAgentTrees {
		if tree.RootPID == expected.RootPID {
			current, found = tree, true
			break
		}
	}
	if !found || current.Agent != expected.Agent || current.Executable != expected.Executable || current.SessionID != expected.SessionID {
		err = fmt.Errorf("agent tree root %d identity changed", expected.RootPID)
		action.Reason = err.Error()
		return action, err
	}
	action.RevalidatedCPUPercent = current.CPUPercentSum
	action.RevalidatedRSSSumMB = current.RSSSumMB
	if !current.CPUAvailable {
		err = fmt.Errorf("agent tree root %d has no current CPU activity evidence", expected.RootPID)
		action.Reason = err.Error()
		return action, err
	}
	if containsPID(current.PIDs, os.Getpid()) {
		err = fmt.Errorf("refusing to terminate the caller's own agent tree")
		action.Reason = err.Error()
		return action, err
	}
	if current.ElapsedSeconds+5 < expected.ElapsedSeconds || current.ElapsedSeconds < int64(criteria.MinAge/time.Second) {
		err = fmt.Errorf("agent tree root %d age no longer meets the idle boundary", expected.RootPID)
		action.Reason = err.Error()
		return action, err
	}
	if !validCPUEvidence(current.CPUAvailable, current.CPUPercentSum) {
		err = fmt.Errorf("agent tree root %d has invalid CPU evidence", expected.RootPID)
		action.Reason = err.Error()
		return action, err
	}
	if current.CPUPercentSum > criteria.MaxCPUPercent {
		err = fmt.Errorf("agent tree root %d became active at %.2f%% CPU above the %.2f%% ceiling", expected.RootPID, current.CPUPercentSum, criteria.MaxCPUPercent)
		action.Reason = err.Error()
		return action, err
	}
	previousPIDs := make(map[int]struct{}, len(expected.tree.PIDs))
	for _, pid := range expected.tree.PIDs {
		previousPIDs[pid] = struct{}{}
	}
	for _, pid := range current.PIDs {
		if _, existed := previousPIDs[pid]; !existed {
			err = fmt.Errorf("agent tree root %d added descendant %d after inventory", expected.RootPID, pid)
			action.Reason = err.Error()
			return action, err
		}
	}
	if len(current.PIDs) == 0 {
		err = fmt.Errorf("agent tree root %d has no signalable PID projection", expected.RootPID)
		action.Reason = err.Error()
		return action, err
	}
	action.Signal = "SIGTERM"
	action.Result = "signal_sent"
	if err = signalTreePIDs(current.PIDs, nil); err != nil {
		action.Result = "error"
		action.Reason = err.Error()
		return action, err
	}
	if confirmTreeExit(current.PIDs, 2*time.Second) {
		action.Result = "tree_exit_confirmed"
	} else {
		action.Result = "signal_sent_unconfirmed"
	}
	return action, nil
}
