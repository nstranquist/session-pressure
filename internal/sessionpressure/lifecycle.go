package sessionpressure

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

type RuntimeState struct {
	SchemaVersion int       `json:"schema_version"`
	PID           int       `json:"pid"`
	StartedAt     time.Time `json:"started_at"`
	CleanShutdown bool      `json:"clean_shutdown"`
	EndedAt       time.Time `json:"ended_at,omitempty,omitzero"`
}

type RecoveryHint struct {
	SchemaVersion     int       `json:"schema_version"`
	DetectedAt        time.Time `json:"detected_at"`
	PreviousStartedAt time.Time `json:"previous_started_at"`
	LastSampleAt      time.Time `json:"last_sample_at,omitempty,omitzero"`
	LastLevel         Level     `json:"last_level,omitempty"`
	Reason            string    `json:"reason"`
	RecoveryCommand   string    `json:"recovery_command"`
}

type Lifecycle struct {
	dir   string
	state RuntimeState
	now   func() time.Time
}

func RuntimePath(dir string) string      { return filepath.Join(dir, "runtime.json") }
func RecoveryHintPath(dir string) string { return filepath.Join(dir, "recovery-hint.json") }

func StartLifecycle(dir string, pid int, now func() time.Time) (*Lifecycle, *RecoveryHint, error) {
	if now == nil {
		now = time.Now
	}
	previous, previousFound, err := loadRuntimeState(RuntimePath(dir))
	if err != nil {
		return nil, nil, err
	}
	var hint *RecoveryHint
	if previousFound && !previous.CleanShutdown {
		at := previous.StartedAt
		last := Snapshot{}
		if snapshot, ok := NewTelemetryStore(dir).ReadLatest(); ok {
			last = snapshot
			if !snapshot.Timestamp.IsZero() {
				at = snapshot.Timestamp
			}
		}
		if at.IsZero() {
			at = now().UTC()
		}
		value := RecoveryHint{
			SchemaVersion: SchemaVersion, DetectedAt: now().UTC(), PreviousStartedAt: previous.StartedAt,
			LastSampleAt: last.Timestamp, LastLevel: last.Level,
			Reason:          "previous pressure monitor did not record a clean shutdown",
			RecoveryCommand: fmt.Sprintf("ndev session recover --around %q --window 30m --include-resume-command", at.Local().Format("2006-01-02 15:04:05")),
		}
		if err := saveJSON(RecoveryHintPath(dir), value); err != nil {
			return nil, nil, err
		}
		hint = &value
	}
	state := RuntimeState{SchemaVersion: SchemaVersion, PID: pid, StartedAt: now().UTC(), CleanShutdown: false}
	if err := saveJSON(RuntimePath(dir), state); err != nil {
		return nil, hint, err
	}
	return &Lifecycle{dir: dir, state: state, now: now}, hint, nil
}

func (lifecycle *Lifecycle) MarkClean() error {
	if lifecycle == nil {
		return nil
	}
	lifecycle.state.CleanShutdown = true
	lifecycle.state.EndedAt = lifecycle.now().UTC()
	return saveJSON(RuntimePath(lifecycle.dir), lifecycle.state)
}

func LoadRecoveryHint(dir string) (RecoveryHint, bool, error) {
	body, err := os.ReadFile(RecoveryHintPath(dir))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return RecoveryHint{}, false, nil
		}
		return RecoveryHint{}, false, err
	}
	var hint RecoveryHint
	if err := json.Unmarshal(body, &hint); err != nil {
		return RecoveryHint{}, true, err
	}
	return hint, true, nil
}

func ClearRecoveryHint(dir string) error {
	err := os.Remove(RecoveryHintPath(dir))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func loadRuntimeState(path string) (RuntimeState, bool, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return RuntimeState{}, false, nil
		}
		return RuntimeState{}, false, err
	}
	var state RuntimeState
	if err := json.Unmarshal(body, &state); err != nil {
		return RuntimeState{}, true, err
	}
	return state, true, nil
}

func saveJSON(path string, value any) error {
	body, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	body = append(body, '\n')
	return atomicWrite(path, body, 0o600)
}
