package sessionpressure

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const maxSessionStateBytes = 64 * 1024

var safeSessionStateID = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

type hookSessionState struct {
	SessionID        string        `json:"session_id"`
	Tool             string        `json:"tool"`
	State            SemanticState `json:"state"`
	LastUserPromptAt int64         `json:"last_user_prompt_at"`
	LastStopAt       int64         `json:"last_stop_at"`
}

func defaultSessionStateDir() string {
	if value := strings.TrimSpace(os.Getenv("NDEV_SESSION_STATE_DIR")); value != "" {
		return value
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".nicos-session-state")
}

func enrichSemanticStates(trees []AgentTree, dir string) {
	if dir == "" {
		return
	}
	for index := range trees {
		state, at, ok := readHookSessionState(dir, trees[index].SessionID, trees[index].Agent)
		if !ok {
			continue
		}
		trees[index].SemanticState = state
		trees[index].SemanticStateAt = at
	}
}

func readHookSessionState(dir, sessionID, agent string) (SemanticState, time.Time, bool) {
	if !safeSessionStateID.MatchString(sessionID) {
		return SemanticStateUnknown, time.Time{}, false
	}
	file, err := os.Open(filepath.Join(dir, sessionID+".json"))
	if err != nil {
		return SemanticStateUnknown, time.Time{}, false
	}
	defer file.Close()
	body, err := io.ReadAll(io.LimitReader(file, maxSessionStateBytes+1))
	if err != nil || len(body) > maxSessionStateBytes {
		return SemanticStateUnknown, time.Time{}, false
	}
	var state hookSessionState
	if err := json.Unmarshal(body, &state); err != nil {
		return SemanticStateUnknown, time.Time{}, false
	}
	if state.SessionID != sessionID || !strings.EqualFold(state.Tool, agent) {
		return SemanticStateUnknown, time.Time{}, false
	}
	switch state.State {
	case SemanticStateReady:
		if state.LastStopAt <= 0 || state.LastStopAt < state.LastUserPromptAt {
			return SemanticStateUnknown, time.Time{}, false
		}
		return state.State, time.Unix(state.LastStopAt, 0).UTC(), true
	case SemanticStateBusy:
		if state.LastUserPromptAt <= 0 || state.LastUserPromptAt <= state.LastStopAt {
			return SemanticStateUnknown, time.Time{}, false
		}
		return state.State, time.Unix(state.LastUserPromptAt, 0).UTC(), true
	default:
		return SemanticStateUnknown, time.Time{}, false
	}
}

func validateSemanticRevalidation(previous, current AgentTree) error {
	if current.SemanticState == SemanticStateBusy {
		return fmt.Errorf("agent tree root %d is semantically busy", current.RootPID)
	}
	if previous.SemanticState == SemanticStateReady && current.SemanticState != SemanticStateReady {
		return errors.New("semantic ready evidence is no longer available")
	}
	return nil
}
