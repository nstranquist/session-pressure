package sessionpressure

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	maxSessionHintFiles     = 64
	maxSessionHintFileBytes = 4 * 1024
	sessionHintCacheTTL     = 15 * time.Second
)

var (
	sessionHintMu        sync.Mutex
	sessionHintCacheDir  string
	sessionHintCacheAt   time.Time
	sessionHintCacheData []SessionOwnershipHint
)

// SessionOwnershipHint is a lightweight session-registry signal: session id →
// tool agent. Used to fill identity when process names are opaque but hooks
// already recorded which agent owns the session.
type SessionOwnershipHint struct {
	SessionID string
	Agent     string
}

// residentSessionHints caches only IDs found in live process evidence. An
// empty result is a valid cache entry; it must not trigger another disk scan.
var residentSessionHints struct {
	sync.Mutex
	key        string
	at         time.Time
	hints      []SessionOwnershipHint
	refreshing bool
}

func liveSessionHintIDs(processes []Process) []string {
	ids := make(map[string]struct{})
	for _, process := range processes {
		id := process.SessionID
		if id == "" {
			id = sessionIDPattern.FindString(process.Command)
		}
		if id != "" && safeSessionStateID.MatchString(id) {
			ids[id] = struct{}{}
		}
	}
	out := make([]string, 0, len(ids))
	for id := range ids {
		out = append(out, id)
	}
	sort.Strings(out)
	if len(out) > maxSessionHintFiles {
		out = out[:maxSessionHintFiles]
	}
	return out
}

// peekSessionOwnershipHints never enumerates the session registry. The
// resident only needs hints for live IDs, and refreshes those bounded files
// asynchronously so hook storage cannot block host-pressure sampling.
func peekSessionOwnershipHints(dir string, catalog *AgentIdentityCatalog, ids []string) []SessionOwnershipHint {
	dir = strings.TrimSpace(dir)
	if dir == "" || catalog == nil || len(ids) == 0 {
		return nil
	}
	key := dir + "\x00" + strings.Join(ids, "\x00")
	now := time.Now()
	residentSessionHints.Lock()
	cached := residentSessionHints.key == key && !residentSessionHints.at.IsZero()
	var out []SessionOwnershipHint
	if cached {
		out = append(out, residentSessionHints.hints...)
	}
	refresh := (!cached || now.Sub(residentSessionHints.at) >= sessionHintCacheTTL) && !residentSessionHints.refreshing
	if refresh {
		residentSessionHints.refreshing = true
	}
	residentSessionHints.Unlock()
	if refresh {
		go func() {
			hints := make([]SessionOwnershipHint, 0, len(ids))
			for _, id := range ids {
				if hint, ok := readSessionOwnershipHint(filepath.Join(dir, id+".json"), id, catalog.agents); ok {
					hints = append(hints, hint)
				}
			}
			residentSessionHints.Lock()
			residentSessionHints.key, residentSessionHints.at, residentSessionHints.hints = key, time.Now(), hints
			residentSessionHints.refreshing = false
			residentSessionHints.Unlock()
		}()
	}
	return out
}

// LoadSessionOwnershipHints reads bounded hook session-state files and returns
// session_id → agent for known catalog agents only. Results are cached briefly
// so the resident sampler does not readdir the session-state tree every sample.
func LoadSessionOwnershipHints(dir string, catalog *AgentIdentityCatalog) []SessionOwnershipHint {
	dir = strings.TrimSpace(dir)
	if dir == "" || catalog == nil {
		return nil
	}
	now := time.Now()
	sessionHintMu.Lock()
	if sessionHintCacheDir == dir && now.Sub(sessionHintCacheAt) < sessionHintCacheTTL {
		out := append([]SessionOwnershipHint(nil), sessionHintCacheData...)
		sessionHintMu.Unlock()
		return out
	}
	sessionHintMu.Unlock()

	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	known := catalog.agents
	hints := make([]SessionOwnershipHint, 0, 16)
	for _, entry := range entries {
		if len(hints) >= maxSessionHintFiles {
			break
		}
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		sessionID := strings.TrimSuffix(entry.Name(), ".json")
		if !safeSessionStateID.MatchString(sessionID) {
			continue
		}
		hint, ok := readSessionOwnershipHint(filepath.Join(dir, entry.Name()), sessionID, known)
		if ok {
			hints = append(hints, hint)
		}
	}

	sessionHintMu.Lock()
	sessionHintCacheDir = dir
	sessionHintCacheAt = now
	sessionHintCacheData = append([]SessionOwnershipHint(nil), hints...)
	sessionHintMu.Unlock()
	return hints
}

// ResetSessionOwnershipHintCache is for tests.
func ResetSessionOwnershipHintCache() {
	for {
		residentSessionHints.Lock()
		if !residentSessionHints.refreshing {
			break
		}
		residentSessionHints.Unlock()
		time.Sleep(time.Millisecond)
	}
	residentSessionHints.key = ""
	residentSessionHints.at = time.Time{}
	residentSessionHints.hints = nil
	residentSessionHints.Unlock()
	sessionHintMu.Lock()
	sessionHintCacheDir = ""
	sessionHintCacheAt = time.Time{}
	sessionHintCacheData = nil
	sessionHintMu.Unlock()
}

func readSessionOwnershipHint(path, sessionID string, known map[string]struct{}) (SessionOwnershipHint, bool) {
	file, err := os.Open(path)
	if err != nil {
		return SessionOwnershipHint{}, false
	}
	defer file.Close()
	body, err := io.ReadAll(io.LimitReader(file, maxSessionHintFileBytes+1))
	if err != nil || len(body) > maxSessionHintFileBytes {
		return SessionOwnershipHint{}, false
	}
	var state hookSessionState
	if err := json.Unmarshal(body, &state); err != nil {
		return SessionOwnershipHint{}, false
	}
	if state.SessionID != "" && state.SessionID != sessionID {
		return SessionOwnershipHint{}, false
	}
	agent := normalizeAgentID(state.Tool)
	if agent == "" {
		return SessionOwnershipHint{}, false
	}
	if _, ok := known[agent]; !ok {
		return SessionOwnershipHint{}, false
	}
	return SessionOwnershipHint{SessionID: sessionID, Agent: agent}, true
}

// ApplySessionOwnershipHints fills missing process Agent/SessionID from the
// session registry. Never overrides a conflicting process-derived agent
// (relief must not re-label a codex tree as grok from a stale hook file).
func ApplySessionOwnershipHints(processes []Process, hints []SessionOwnershipHint) []Process {
	if len(processes) == 0 || len(hints) == 0 {
		return processes
	}
	bySession := make(map[string]string, len(hints))
	for _, hint := range hints {
		if hint.SessionID == "" || hint.Agent == "" {
			continue
		}
		bySession[hint.SessionID] = hint.Agent
	}
	if len(bySession) == 0 {
		return processes
	}
	out := processes
	// Copy on write only when we mutate.
	copied := false
	for index := range out {
		sessionID := out[index].SessionID
		if sessionID == "" {
			sessionID = sessionIDPattern.FindString(out[index].Command)
		}
		if sessionID == "" {
			continue
		}
		agent, ok := bySession[sessionID]
		if !ok {
			continue
		}
		current := out[index].Agent
		if current != "" && !strings.EqualFold(current, agent) {
			// Conflict: keep process identity.
			if out[index].SessionID == "" {
				if !copied {
					out = append([]Process(nil), processes...)
					copied = true
				}
				out[index].SessionID = sessionID
			}
			continue
		}
		if current == agent && out[index].SessionID == sessionID {
			continue
		}
		if !copied {
			out = append([]Process(nil), processes...)
			copied = true
		}
		if out[index].Agent == "" {
			out[index].Agent = agent
			if out[index].Executable == "" || out[index].Executable == "unknown" {
				out[index].Executable = agent
			}
		}
		if out[index].SessionID == "" {
			out[index].SessionID = sessionID
		}
	}
	return out
}
