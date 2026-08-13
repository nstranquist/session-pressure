package sessionpressure

import (
	"context"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	pidOwnershipCacheTTL   = 15 * time.Second
	pidOwnershipMaxPIDs    = 48
	pidOwnershipLSOFTimeout = 2 * time.Second
)

// PIDOwnershipHint binds a live agent process to a session id using open
// transcript paths (lsof), without scanning every process or importing
// sessionrecover into the resident sample loop.
type PIDOwnershipHint struct {
	PID       int    `json:"pid"`
	Agent     string `json:"agent"`
	SessionID string `json:"session_id"`
	Evidence  string `json:"evidence,omitempty"`
}

var (
	pidOwnershipMu        sync.Mutex
	pidOwnershipCacheAt   time.Time
	pidOwnershipCacheData []PIDOwnershipHint
	// Test seams.
	pidOwnershipLSOF = runLSOFForPIDs
	pidOwnershipNow  = time.Now
)

// ResetPIDOwnershipCache is for tests.
func ResetPIDOwnershipCache() {
	pidOwnershipMu.Lock()
	pidOwnershipCacheAt = time.Time{}
	pidOwnershipCacheData = nil
	pidOwnershipMu.Unlock()
}

// LoadPIDOwnershipHints returns cached or freshly probed PID→session bindings
// for already-classified agent processes that still lack a session id.
func LoadPIDOwnershipHints(ctx context.Context, processes []Process) []PIDOwnershipHint {
	now := pidOwnershipNow()
	pidOwnershipMu.Lock()
	if now.Sub(pidOwnershipCacheAt) < pidOwnershipCacheTTL && pidOwnershipCacheData != nil {
		out := append([]PIDOwnershipHint(nil), pidOwnershipCacheData...)
		pidOwnershipMu.Unlock()
		return out
	}
	pidOwnershipMu.Unlock()

	candidates := ownershipCandidatePIDs(processes)
	if len(candidates) == 0 {
		pidOwnershipMu.Lock()
		pidOwnershipCacheAt = now
		pidOwnershipCacheData = nil
		pidOwnershipMu.Unlock()
		return nil
	}

	hints := probePIDOwnership(ctx, candidates)
	pidOwnershipMu.Lock()
	pidOwnershipCacheAt = now
	pidOwnershipCacheData = append([]PIDOwnershipHint(nil), hints...)
	pidOwnershipMu.Unlock()
	return hints
}

func ownershipCandidatePIDs(processes []Process) []int {
	seen := map[int]bool{}
	var pids []int
	for _, process := range processes {
		if process.PID <= 0 || process.Agent == "" || process.SessionID != "" {
			continue
		}
		agent := normalizeAgentID(process.Agent)
		if agent != "grok" && agent != "codex" && agent != "claude" && agent != "kimi" {
			continue
		}
		if seen[process.PID] {
			continue
		}
		seen[process.PID] = true
		pids = append(pids, process.PID)
		if len(pids) >= pidOwnershipMaxPIDs {
			break
		}
	}
	sort.Ints(pids)
	return pids
}

func probePIDOwnership(ctx context.Context, pids []int) []PIDOwnershipHint {
	if len(pids) == 0 {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	probeCtx, cancel := context.WithTimeout(ctx, pidOwnershipLSOFTimeout)
	defer cancel()
	body, err := pidOwnershipLSOF(probeCtx, pids)
	if err != nil || len(body) == 0 {
		return nil
	}
	return parseLSOFOwnership(body)
}

func runLSOFForPIDs(ctx context.Context, pids []int) ([]byte, error) {
	if _, err := exec.LookPath("lsof"); err != nil {
		return nil, err
	}
	args := make([]string, 0, 2+len(pids))
	args = append(args, "-Fn")
	// macOS accepts -p p1,p2,p3
	parts := make([]string, 0, len(pids))
	for _, pid := range pids {
		parts = append(parts, strconv.Itoa(pid))
	}
	args = append(args, "-p", strings.Join(parts, ","))
	cmd := exec.CommandContext(ctx, "lsof", args...)
	return cmd.Output()
}

// parseLSOFOwnership maps lsof -Fn output to ownership hints.
func parseLSOFOwnership(body []byte) []PIDOwnershipHint {
	currentPID := 0
	// Prefer first strong match per PID.
	byPID := map[int]PIDOwnershipHint{}
	for _, line := range strings.Split(string(body), "\n") {
		if strings.HasPrefix(line, "p") {
			if pid, err := strconv.Atoi(strings.TrimPrefix(line, "p")); err == nil {
				currentPID = pid
			}
			continue
		}
		if currentPID <= 0 || !strings.HasPrefix(line, "n") {
			continue
		}
		if _, exists := byPID[currentPID]; exists {
			continue
		}
		path := strings.TrimPrefix(line, "n")
		agent, sessionID := sessionIDFromOpenPath(path)
		if agent == "" || sessionID == "" {
			continue
		}
		byPID[currentPID] = PIDOwnershipHint{
			PID: currentPID, Agent: agent, SessionID: sessionID, Evidence: "open-transcript",
		}
	}
	if len(byPID) == 0 {
		return nil
	}
	out := make([]PIDOwnershipHint, 0, len(byPID))
	for _, hint := range byPID {
		out = append(out, hint)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].PID < out[j].PID })
	return out
}

// sessionIDFromOpenPath extracts agent + session id from a trusted open path.
// Pure and unit-tested; mirrors the fail-closed shapes used by sessionrecover
// without importing that package into the resident.
func sessionIDFromOpenPath(path string) (agent, sessionID string) {
	path = filepath.ToSlash(path)
	lower := strings.ToLower(path)
	base := strings.ToLower(filepath.Base(path))

	// Grok: ~/.grok/sessions/<cwd-encoded>/<uuid>/{summary,updates,chat_history,events}.jsonl
	if strings.Contains(lower, "/.grok/sessions/") {
		switch base {
		case "chat_history.jsonl", "events.jsonl", "summary.json", "updates.jsonl":
			id := strings.ToLower(filepath.Base(filepath.Dir(path)))
			if sessionIDPattern.MatchString(id) {
				return "grok", sessionIDPattern.FindString(id)
			}
		}
	}

	// Codex: ~/.codex/sessions/.../rollout-...-<uuid>.jsonl
	if strings.Contains(lower, "/.codex/sessions/") && strings.HasSuffix(lower, ".jsonl") {
		if id := sessionIDPattern.FindString(base); id != "" {
			return "codex", id
		}
		// Sometimes the uuid is the parent directory name.
		if id := sessionIDPattern.FindString(filepath.Base(filepath.Dir(path))); id != "" {
			return "codex", id
		}
	}

	// Claude project transcripts under ~/.claude/projects often embed session
	// id in the filename.
	if strings.Contains(lower, "/.claude/") && strings.HasSuffix(lower, ".jsonl") {
		if id := sessionIDPattern.FindString(base); id != "" {
			return "claude", id
		}
	}

	// Kimi sessions under ~/.kimi/sessions/
	if strings.Contains(lower, "/.kimi/sessions/") {
		if id := sessionIDPattern.FindString(base); id != "" {
			return "kimi", id
		}
		if id := sessionIDPattern.FindString(filepath.Base(filepath.Dir(path))); id != "" {
			return "kimi", id
		}
	}
	return "", ""
}

// ApplyPIDOwnershipHints attaches session ids from open-transcript evidence.
// Never overrides a conflicting process-derived agent. Does not invent agent
// labels for unlabeled processes (ownership is session attachment only).
func ApplyPIDOwnershipHints(processes []Process, hints []PIDOwnershipHint) []Process {
	if len(processes) == 0 || len(hints) == 0 {
		return processes
	}
	byPID := make(map[int]PIDOwnershipHint, len(hints))
	for _, hint := range hints {
		if hint.PID <= 0 || hint.SessionID == "" || hint.Agent == "" {
			continue
		}
		byPID[hint.PID] = hint
	}
	if len(byPID) == 0 {
		return processes
	}
	out := processes
	copied := false
	for index := range out {
		hint, ok := byPID[out[index].PID]
		if !ok {
			continue
		}
		currentAgent := normalizeAgentID(out[index].Agent)
		hintAgent := normalizeAgentID(hint.Agent)
		if currentAgent != "" && currentAgent != hintAgent {
			// Conflict: keep process identity; still allow session fill only when empty? No —
			// a mismatched agent must not receive that session id.
			continue
		}
		if out[index].SessionID == hint.SessionID && currentAgent == hintAgent {
			continue
		}
		if !copied {
			out = append([]Process(nil), processes...)
			copied = true
		}
		if out[index].Agent == "" {
			// Attachment-only mode: do not create agent ownership from lsof alone
			// (relief eligibility must still come from the identity catalog).
			// Skip unlabeled processes.
			continue
		}
		if out[index].SessionID == "" {
			out[index].SessionID = hint.SessionID
		}
	}
	return out
}

// EnrichProcessesForIdentity runs session-state + PID ownership enrichment.
func EnrichProcessesForIdentity(ctx context.Context, processes []Process, sessionStateDir string) []Process {
	processes = applySamplerSessionHints(processes, sessionStateDir)
	hints := LoadPIDOwnershipHints(ctx, processes)
	return ApplyPIDOwnershipHints(processes, hints)
}

// enrichSampleProcesses is the resident-safe inventory enrichment.
// The resident never execs lsof: a 15-PID probe is 600ms–2s wall and the
// 15s ownership cache expires before the 120s sample cadence. Session ids
// still attach from the session-state store. Operator / identity --live
// keep the full lsof path.
func enrichSampleProcesses(ctx context.Context, processes []Process, sessionStateDir string, inventoryFresh bool, role string) []Process {
	if role != "resident" {
		return EnrichProcessesForIdentity(ctx, processes, sessionStateDir)
	}
	return applyResidentSessionHints(processes, sessionStateDir)
}

// CollectIdentityInventory is the operator/diagnostic inventory path used by
// `identity show --live`. It reuses the same enrichment as the sampler without
// sampling host memory/CPU. The ownership cache is cleared so --live is not
// a 15-second stale hit from the resident.
func CollectIdentityInventory(ctx context.Context, sessionStateDir string) (processes []Process, trees []AgentTree, source string, err error) {
	if ctx == nil {
		ctx = context.Background()
	}
	ResetPIDOwnershipCache()
	processes, source, err = nativeProcesses(ctx)
	if err != nil {
		return nil, nil, source, err
	}
	processes = EnrichProcessesForIdentity(ctx, processes, sessionStateDir)
	trees = buildAgentTrees(processes)
	return processes, trees, source, nil
}