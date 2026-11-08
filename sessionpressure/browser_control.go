package sessionpressure

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/nstranquist/session-pressure/internal/filelock"
)

// BrowserControl is host-wide. Automatic recovery counts distinct resident
// samples, never the number of callers reading a cached sample.
type BrowserControl struct {
	Mode              string    `json:"mode"`
	Blocked           bool      `json:"blocked"`
	Reason            string    `json:"reason"`
	RecoverySamples   int       `json:"recovery_samples"`
	LastSample        time.Time `json:"last_sample"`
	UpdatedAt         time.Time `json:"updated_at"`
	MaxRuntimeSeconds int       `json:"max_runtime_seconds"`
}

func BrowserControlStatus(ctx context.Context, dir string, mode string) (BrowserControl, error) {
	var state BrowserControl
	if mode != "" && mode != "auto" && mode != "paused" {
		return state, errors.New("browser mode must be auto or paused")
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return state, err
	}
	err := filelock.WithFileLockContext(ctx, filepath.Join(dir, "browser-control"), 2*time.Second, func() error {
		path := filepath.Join(dir, "browser-control.json")
		body, err := os.ReadFile(path)
		if errors.Is(err, os.ErrNotExist) && mode == "" {
			return nil
		} // opt in per host
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if len(body) > 0 {
			if err := json.Unmarshal(body, &state); err != nil {
				return fmt.Errorf("invalid browser control: %w", err)
			}
		}
		previous := state
		if state.Mode == "" {
			state.Mode = "auto"
			state.Blocked = true
		}
		if state.Mode != "auto" && state.Mode != "paused" {
			return errors.New("invalid persisted browser mode")
		}
		if mode != "" && mode != state.Mode {
			state.Mode = mode
			state.Blocked = true
			state.RecoverySamples = 0
			state.LastSample = time.Time{}
		}
		state.MaxRuntimeSeconds = 120
		now := time.Now().UTC()
		var snapshot Snapshot
		available := false
		if state.Mode == "auto" {
			snapshot, available = NewTelemetryStore(dir).ReadLatest()
		}
		if state.Mode == "paused" {
			state.Blocked = true
			state.RecoverySamples = 0
			state.Reason = "Browser work is paused by the operator"
		} else if !available || now.Sub(snapshot.Timestamp) > 30*time.Second || snapshot.Timestamp.After(now.Add(time.Second)) || !snapshot.HostCPUAvailable {
			state.Blocked = true
			state.RecoverySamples = 0
			state.Reason = "Waiting for fresh resident CPU evidence"
		} else {
			policy, _, err := LoadPolicy(PolicyPath(dir), 0)
			if err != nil {
				return err
			}
			admission := AdmissionForWorkSnapshot(snapshot, policy, "resident")
			if !admission.Allowed || snapshot.HostCPUPercent >= policy.Thresholds.HostCPURedPercent {
				state.Blocked = true
				state.RecoverySamples = 0
				state.Reason = "Host pressure blocks browser work"
			} else if state.Blocked && snapshot.Timestamp.After(state.LastSample.Add(5*time.Second)) {
				if snapshot.HostCPUPercent <= policy.WorkLimits.CPUReleasePercent {
					state.RecoverySamples++
				} else {
					state.RecoverySamples = 0
				}
				if state.RecoverySamples >= 2 {
					state.Blocked = false
					state.Reason = "Host load recovered"
				} else {
					state.Reason = "Waiting for two fresh recovery samples"
				}
			}
			if state.LastSample.IsZero() || snapshot.Timestamp.After(state.LastSample.Add(5*time.Second)) {
				state.LastSample = snapshot.Timestamp
			}
		}
		// Cached reads must not flush an identical control record. In particular,
		// paused status can be polled by multiple clients without new evidence.
		if state == previous {
			return nil
		}
		state.UpdatedAt = now
		body, err = json.MarshalIndent(state, "", "  ")
		if err != nil {
			return err
		}
		return atomicWrite(path, append(body, '\n'), 0600)
	})
	return state, err
}

func CheckBrowserWork(ctx context.Context) error {
	dir, err := DataDir()
	if err != nil {
		return err
	}
	state, err := BrowserControlStatus(ctx, dir, "")
	if err != nil {
		return err
	}
	if state.Blocked {
		return fmt.Errorf("BROWSER_PAUSED: %s; inspect: ndev session pressure browser status", state.Reason)
	}
	return nil
}

// AcquireBrowserSlot serializes finite browser commands across all agents.
func AcquireBrowserSlot(ctx context.Context, dir string) (func(), error) {
	return filelock.AcquireContext(ctx, filepath.Join(dir, "browser-execution"), 2*time.Second)
}
