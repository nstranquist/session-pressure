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

// browserControlEvidence is the resident's in-memory sample and policy. The
// resident evaluates browser control after every sample; re-reading
// latest.json and re-validating policy.json from disk there cost more CPU than
// the sample itself. CLI callers pass nil and read from disk.
type browserControlEvidence struct {
	snapshot Snapshot
	policy   Policy
}

func BrowserControlStatus(ctx context.Context, dir string, mode string) (BrowserControl, error) {
	return browserControlStatus(ctx, dir, mode, nil)
}

func browserControlStatus(ctx context.Context, dir string, mode string, evidence *browserControlEvidence) (BrowserControl, error) {
	if mode != "" && mode != "auto" && mode != "paused" {
		return BrowserControl{}, errors.New("browser mode must be auto or paused")
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return BrowserControl{}, err
	}
	path := filepath.Join(dir, "browser-control.json")
	// The resident evaluates control after every sample. Writers replace the
	// record by atomic rename, so an unlocked read is never torn. Take the
	// lock only when the record must change.
	if evidence != nil && mode == "" {
		if state, changed, err := evaluateBrowserControl(dir, path, mode, evidence); err != nil || !changed {
			return state, err
		}
	}
	var state BrowserControl
	err := filelock.WithFileLockContext(ctx, filepath.Join(dir, "browser-control"), 2*time.Second, func() error {
		var changed bool
		var err error
		if state, changed, err = evaluateBrowserControl(dir, path, mode, evidence); err != nil || !changed {
			return err
		}
		state.UpdatedAt = time.Now().UTC()
		body, err := json.MarshalIndent(state, "", "  ")
		if err != nil {
			return err
		}
		return atomicWrite(path, append(body, '\n'), 0600)
	})
	return state, err
}

// evaluateBrowserControl reads the persisted record and applies one
// observation. changed reports whether the record must be written. Cached
// reads must not flush an identical record: paused status can be polled by
// many clients without new evidence.
func evaluateBrowserControl(dir, path, mode string, evidence *browserControlEvidence) (state BrowserControl, changed bool, err error) {
	body, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) && mode == "" {
		return BrowserControl{}, false, nil // opt in per host
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return BrowserControl{}, false, err
	}
	if len(body) > 0 {
		if err := json.Unmarshal(body, &state); err != nil {
			return BrowserControl{}, false, fmt.Errorf("invalid browser control: %w", err)
		}
	}
	previous := state
	if state.Mode == "" {
		state.Mode = "auto"
		state.Blocked = true
	}
	if state.Mode != "auto" && state.Mode != "paused" {
		return BrowserControl{}, false, errors.New("invalid persisted browser mode")
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
		if evidence != nil {
			snapshot, available = evidence.snapshot, true
		} else {
			snapshot, available = NewTelemetryStore(dir).ReadLatest()
		}
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
		var policy Policy
		if evidence != nil {
			policy = evidence.policy
		} else if policy, _, err = LoadPolicy(PolicyPath(dir), 0); err != nil {
			return BrowserControl{}, false, err
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
		// LastSample only spaces recovery samples while blocked. Advancing it
		// in steady unblocked state made every resident sample rewrite an
		// otherwise identical record.
		if state.Blocked && (state.LastSample.IsZero() || snapshot.Timestamp.After(state.LastSample.Add(5*time.Second))) {
			state.LastSample = snapshot.Timestamp
		}
	}
	return state, state != previous, nil
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

// Browser concurrency is derived from memory headroom per admission
// (browser_admission.go); there is no fixed browser slot.
