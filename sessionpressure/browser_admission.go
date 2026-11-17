package sessionpressure

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/nstranquist/session-pressure/internal/filelock"
)

// Browser admission (WORK-1114, under WORK-1065).
//
// Until 2026-11-17 every browser work run took one host-wide file lock
// (browser-execution), so one long CSP sweep blocked console screenshots and
// smoke runs in every other agent session for more than 15 minutes. Nico's
// rule is that limits come from live resources and measured cost, not from a
// fixed human cap. Browser concurrency is therefore derived, at each
// admission, from memory headroom and the per-session cost of the requested
// runtime:
//
//	slots = active + floor((headroom - charged) / session cost)
//
// headroom is the shared memory-headroom view (work_headroom.go): available
// memory minus the reserve and the in-flight predictions of running leases.
// charged is the cost of browser admissions made after the last resident
// sample was taken, because their memory is not in that sample yet. A
// detached session (ndev browser open) outlives its admission command, so
// its charge also outlives the command until a newer sample sees it.
//
// CPU pressure stays with the browser control (BROWSER_PAUSED). Both are
// waited on, until the caller's deadline, by waitForBrowserAdmission.

// BrowserRuntimeEnv names the runtime a browser work command will launch.
// ndev browser sets it from the resolved snapref runtime. Unknown or unset
// means the conservative Chrome for Testing cost.
const BrowserRuntimeEnv = "SESSION_PRESSURE_BROWSER_RUNTIME"

const (
	BrowserRuntimeHeadlessShell    = "headless-shell"
	BrowserRuntimeChromeForTesting = "chrome-for-testing"
	// Per-session resident memory, measured by the snapref runtime-cost
	// benchmark (packages/snapref/cmd/snapref-runtime-cost, receipt
	// benchmark/runtime-cost.json, nicos-tools delivery 47c5778b, M4
	// 2026-11-17): headless-shell 290 MB, Chrome for Testing 928 MB.
	browserHeadlessShellSessionMB    = 290
	browserChromeForTestingSessionMB = 928

	browserAdmissionLedgerName = "browser-admissions.json"
	browserAdmissionLockName   = "browser-admission"
	// A sample taken this long before an admission may already include it.
	browserAdmissionSampleSkew = 2 * time.Second
	// Ledger rows older than the in-flight ramp are dropped; by then several
	// resident samples include the session's real memory.
	browserAdmissionRetention = headroomInflightRamp
	browserAdmissionRetry     = 2 * time.Second
	browserAdmissionMaxRows   = 512
)

// NormalizeBrowserRuntime maps runtime spellings to the two cost classes.
func NormalizeBrowserRuntime(runtime string) string {
	switch strings.ToLower(strings.TrimSpace(runtime)) {
	case "headless-shell", "chrome-headless-shell", "chromium-headless-shell", "headless_shell":
		return BrowserRuntimeHeadlessShell
	default:
		return BrowserRuntimeChromeForTesting
	}
}

// BrowserRuntimeSessionMB is the measured per-session cost of a runtime.
func BrowserRuntimeSessionMB(runtime string) int64 {
	if NormalizeBrowserRuntime(runtime) == BrowserRuntimeHeadlessShell {
		return browserHeadlessShellSessionMB
	}
	return browserChromeForTestingSessionMB
}

// browserSessionCostMB is the larger of the runtime's measured session cost
// and the command's own measured peak. A script that drives its own browser
// (a CSP sweep) records its whole tree, browser included; ndev browser open
// records only itself, so the runtime cost dominates.
func browserSessionCostMB(runtime string, predictedMB int64) int64 {
	return max(BrowserRuntimeSessionMB(runtime), predictedMB)
}

// BrowserAdmissionEntry is one admitted browser command.
type BrowserAdmissionEntry struct {
	PID     int       `json:"pid"`
	Runtime string    `json:"runtime"`
	CostMB  int64     `json:"cost_mb"`
	At      time.Time `json:"at"`
	// StartedAt is when the admitted command started, after any lease wait.
	// Zero while it still waits in the work queue.
	StartedAt time.Time `json:"started_at,omitzero"`
	Released  bool      `json:"released,omitempty"`
}

// chargedAgainst reports whether a sample taken at sampledAt cannot yet
// include this admission's memory. A queued command has not started its
// browser, so no sample includes it.
func (entry BrowserAdmissionEntry) chargedAgainst(sampledAt time.Time) bool {
	if entry.StartedAt.IsZero() {
		return !entry.Released || sampledAt.IsZero() || entry.At.After(sampledAt.Add(-browserAdmissionSampleSkew))
	}
	return sampledAt.IsZero() || entry.StartedAt.After(sampledAt.Add(-browserAdmissionSampleSkew))
}

// BrowserCapacity is one browser admission decision and the slot count it
// was derived from.
type BrowserCapacity struct {
	Runtime     string    `json:"runtime"`
	CostMB      int64     `json:"cost_mb"`
	SampleFresh bool      `json:"sample_fresh"`
	SampledAt   time.Time `json:"sampled_at,omitzero"`
	HeadroomMB  int64     `json:"headroom_mb"`
	ChargedMB   int64     `json:"charged_mb"`
	AvailableMB int64     `json:"available_mb"`
	Active      int       `json:"active"`
	Slots       int       `json:"slots"`
	Admit       bool      `json:"admit"`
	Reason      string    `json:"reason"`
}

// decideBrowserCapacity is pure. entries must already exclude dead holders
// from Active by marking them Released.
func decideBrowserCapacity(headroom MemoryHeadroom, entries []BrowserAdmissionEntry, runtime string, cost int64) BrowserCapacity {
	decision := BrowserCapacity{Runtime: NormalizeBrowserRuntime(runtime), CostMB: max(cost, 1), SampleFresh: headroom.SampleFresh, SampledAt: headroom.SampledAt, HeadroomMB: headroom.HeadroomMB}
	for _, entry := range entries {
		if !entry.Released {
			decision.Active++
		}
		if entry.chargedAgainst(headroom.SampledAt) {
			decision.ChargedMB += entry.CostMB
		}
	}
	if !headroom.SampleFresh {
		// Without fresh memory evidence the safe slot count is one.
		decision.Slots = 1
		decision.Admit = decision.Active == 0
		decision.Reason = "no_sample: one browser at a time until the resident publishes a fresh memory sample"
		return decision
	}
	decision.AvailableMB = headroom.HeadroomMB - decision.ChargedMB
	free := 0
	if decision.AvailableMB > 0 {
		free = int(decision.AvailableMB / decision.CostMB)
	}
	decision.Slots = decision.Active + free
	decision.Admit = free >= 1
	if decision.Admit {
		decision.Reason = fmt.Sprintf("fits: %d MB free fits %d more %s session(s) of %d MB", decision.AvailableMB, free, decision.Runtime, decision.CostMB)
	} else {
		decision.Reason = fmt.Sprintf("no_headroom: need %d MB for a %s session, %d MB free after %d active", decision.CostMB, decision.Runtime, max(decision.AvailableMB, 0), decision.Active)
	}
	return decision
}

func browserProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// pruneBrowserAdmissions releases dead holders and drops rows that every
// later sample already covers.
func pruneBrowserAdmissions(entries []BrowserAdmissionEntry, now time.Time, alive func(int) bool) []BrowserAdmissionEntry {
	kept := entries[:0]
	for _, entry := range entries {
		if !entry.Released && !alive(entry.PID) {
			entry.Released = true
		}
		if entry.Released && now.Sub(entry.At) > browserAdmissionRetention {
			continue
		}
		kept = append(kept, entry)
	}
	if len(kept) > browserAdmissionMaxRows {
		kept = kept[len(kept)-browserAdmissionMaxRows:]
	}
	return kept
}

func readBrowserAdmissions(path string) ([]BrowserAdmissionEntry, error) {
	body, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var entries []BrowserAdmissionEntry
	if len(body) > 0 {
		if err := json.Unmarshal(body, &entries); err != nil {
			// A torn or foreign ledger must not wedge browser work. The
			// resident sample still bounds memory on the next decision.
			return nil, nil
		}
	}
	return entries, nil
}

func writeBrowserAdmissions(path string, entries []BrowserAdmissionEntry) error {
	body, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(path, append(body, '\n'), 0600)
}

// browserAdmitter evaluates and records browser admissions for one command.
type browserAdmitter struct {
	dir         string
	coordinator *WorkCoordinator
	runtime     string
	cost        int64
	pid         int
	now         func() time.Time
	alive       func(int) bool
	headroom    func(ctx context.Context, now time.Time) (MemoryHeadroom, error)
}

func newBrowserAdmitter(coordinator *WorkCoordinator, runtime string, predictedMB int64) *browserAdmitter {
	history := readPeakRSSHistory(coordinator.Dir, time.Now())
	return &browserAdmitter{
		dir: coordinator.Dir, coordinator: coordinator, runtime: NormalizeBrowserRuntime(runtime),
		cost: browserSessionCostMB(runtime, predictedMB), pid: os.Getpid(), now: time.Now, alive: browserProcessAlive,
		headroom: func(ctx context.Context, now time.Time) (MemoryHeadroom, error) {
			status, err := coordinator.StatusSnapshot(ctx)
			if err != nil {
				return MemoryHeadroom{}, err
			}
			snapshot, ok := NewTelemetryStore(coordinator.Dir).ReadLatest()
			return computeHeadroom(snapshot, ok, status.Leases, history, now), nil
		},
	}
}

// browserAdmission is one admitted command's ledger row.
type browserAdmission struct {
	admitter *browserAdmitter
	at       time.Time
	Decision BrowserCapacity
}

func (admission *browserAdmission) update(change func(*BrowserAdmissionEntry)) {
	if admission == nil {
		return
	}
	admitter := admission.admitter
	path := filepath.Join(admitter.dir, browserAdmissionLedgerName)
	_ = filelock.WithFileLockContext(context.Background(), filepath.Join(admitter.dir, browserAdmissionLockName), 2*time.Second, func() error {
		entries, err := readBrowserAdmissions(path)
		if err != nil {
			return err
		}
		for i := range entries {
			if entries[i].PID == admitter.pid && entries[i].At.Equal(admission.at) {
				change(&entries[i])
			}
		}
		return writeBrowserAdmissions(path, pruneBrowserAdmissions(entries, admitter.now().UTC(), admitter.alive))
	})
}

// Started records that the command left the work queue and started.
func (admission *browserAdmission) Started() {
	admission.update(func(entry *BrowserAdmissionEntry) { entry.StartedAt = admission.admitter.now().UTC() })
}

// Release marks the row released. It keeps its charge until a newer sample
// covers the session, because a detached browser outlives its command.
func (admission *browserAdmission) Release() {
	admission.update(func(entry *BrowserAdmissionEntry) { entry.Released = true })
}

// tryAcquire makes one admission decision under the ledger lock. It returns
// a nil admission when the decision refuses.
func (admitter *browserAdmitter) tryAcquire(ctx context.Context) (BrowserCapacity, *browserAdmission, error) {
	var decision BrowserCapacity
	var admittedAt time.Time
	path := filepath.Join(admitter.dir, browserAdmissionLedgerName)
	lock := filepath.Join(admitter.dir, browserAdmissionLockName)
	err := filelock.WithFileLockContext(ctx, lock, 2*time.Second, func() error {
		now := admitter.now().UTC()
		entries, err := readBrowserAdmissions(path)
		if err != nil {
			return err
		}
		entries = pruneBrowserAdmissions(entries, now, admitter.alive)
		headroom, err := admitter.headroom(ctx, now)
		if err != nil {
			return err
		}
		decision = decideBrowserCapacity(headroom, entries, admitter.runtime, admitter.cost)
		if decision.Admit {
			admittedAt = now
			entries = append(entries, BrowserAdmissionEntry{PID: admitter.pid, Runtime: decision.Runtime, CostMB: decision.CostMB, At: now})
		}
		return writeBrowserAdmissions(path, entries)
	})
	if err != nil || !decision.Admit {
		return decision, nil, err
	}
	return decision, &browserAdmission{admitter: admitter, at: admittedAt, Decision: decision}, nil
}

// ReadBrowserCapacity reports the current derived browser slot count without
// admitting anything.
func ReadBrowserCapacity(ctx context.Context, coordinator *WorkCoordinator, runtime string) (BrowserCapacity, error) {
	if coordinator == nil {
		return BrowserCapacity{}, errors.New("work coordinator is required")
	}
	admitter := newBrowserAdmitter(coordinator, runtime, 0)
	now := admitter.now().UTC()
	entries, err := readBrowserAdmissions(filepath.Join(coordinator.Dir, browserAdmissionLedgerName))
	if err != nil {
		return BrowserCapacity{}, err
	}
	entries = pruneBrowserAdmissions(entries, now, admitter.alive)
	headroom, err := admitter.headroom(ctx, now)
	if err != nil {
		return BrowserCapacity{}, err
	}
	return decideBrowserCapacity(headroom, entries, runtime, admitter.cost), nil
}

// browserGate is one blocking reason the browser waiter reports.
type browserGate struct {
	blocker WorkBlocker
	reason  string
}

// waitForBrowserAdmission waits, until ctx ends, for the browser control to
// allow work and for memory headroom to fit one more session. With wait
// false it decides once, as before. Each blocked check emits a progress line
// on stderr (deduplicated by the reporter). The returned release must run
// when the command ends.
func waitForBrowserAdmission(ctx context.Context, wait bool, control func(context.Context) error, admitter *browserAdmitter, reporter *workProgressReporter, retry time.Duration) (*browserAdmission, error) {
	if retry <= 0 {
		retry = browserAdmissionRetry
	}
	started := time.Now()
	var last browserGate
	for {
		gate, admission, err := browserAdmissionOnce(ctx, control, admitter)
		if err != nil {
			return nil, err
		}
		if admission != nil {
			return admission, nil
		}
		last = gate
		if !wait {
			return nil, errors.New(gate.reason)
		}
		reporter.emit(WorkProgress{Stage: "waiting", Blocker: gate.blocker, Reason: gate.reason, NextCheckSeconds: retry.Seconds()})
		timer := time.NewTimer(retry)
		select {
		case <-ctx.Done():
			timer.Stop()
			if errors.Is(ctx.Err(), context.Canceled) {
				return nil, fmt.Errorf("browser admission interrupted after %s: %s", time.Since(started).Round(time.Second), last.reason)
			}
			return nil, fmt.Errorf("browser admission wait deadline reached after %s: %s", time.Since(started).Round(time.Second), last.reason)
		case <-timer.C:
		}
	}
}

func browserAdmissionOnce(ctx context.Context, control func(context.Context) error, admitter *browserAdmitter) (browserGate, *browserAdmission, error) {
	if err := control(ctx); err != nil {
		if strings.HasPrefix(err.Error(), "BROWSER_PAUSED") {
			return browserGate{blocker: WorkBlockerBrowserPaused, reason: err.Error()}, nil, nil
		}
		return browserGate{}, nil, err
	}
	decision, admission, err := admitter.tryAcquire(ctx)
	if err != nil {
		return browserGate{}, nil, fmt.Errorf("browser admission: %w", err)
	}
	if admission != nil {
		return browserGate{}, admission, nil
	}
	return browserGate{blocker: WorkBlockerBrowserMemory, reason: "BROWSER_NO_HEADROOM: " + decision.Reason + "; inspect: ndev session pressure work headroom"}, nil, nil
}
