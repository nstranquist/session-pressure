package sessionpressure

// Process incidents cover unleased work as well as agent trees. The observer
// uses the resident's existing inventory; it never walks the filesystem or
// reads argv. Detection is advisory. Only an explicit, identity-bound action
// can signal one process.
import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/nstranquist/session-pressure/internal/filelock"
)

const processIncidentLimit = 200

var errProcessGuardExited = errors.New("process exited")

type ProcessIncident struct {
	Source           string                  `json:"source,omitempty"`
	ID               string                  `json:"id"`
	PID              int                     `json:"pid"`
	PPID             int                     `json:"ppid"`
	StartIdentity    string                  `json:"start_identity"`
	Executable       string                  `json:"executable"`
	ParentExecutable string                  `json:"parent_executable,omitempty"`
	SessionID        string                  `json:"session_id,omitempty"`
	UID              int                     `json:"uid"`
	CPUPercent       float64                 `json:"cpu_percent"`
	RSSMB            float64                 `json:"rss_mb"`
	AgeSeconds       int64                   `json:"age_seconds"`
	Reason           string                  `json:"reason"`
	FirstSeen        time.Time               `json:"first_seen"`
	LastSeen         time.Time               `json:"last_seen"`
	SustainedSeconds int64                   `json:"sustained_seconds"`
	State            string                  `json:"state"`
	AlertedAt        *time.Time              `json:"alerted_at,omitempty"`
	Actions          []ProcessIncidentAction `json:"actions,omitempty"`
}

type ProcessIncidentAction struct {
	At     time.Time `json:"at"`
	Signal string    `json:"signal"`
	Result string    `json:"result"`
	Error  string    `json:"error,omitempty"`
}

type ProcessIncidentReport struct {
	SchemaVersion int               `json:"schema_version"`
	UpdatedAt     time.Time         `json:"updated_at"`
	Incidents     []ProcessIncident `json:"incidents"`
}

type processStreak struct {
	first, last time.Time
	incident    ProcessIncident
}
type ProcessGuard struct {
	Dir             string
	Notify          func(ProcessIncident) error
	streaks         map[string]processStreak
	lastInventory   time.Time
	maxInventoryGap time.Duration
	identity        func(int) (string, int, error)
}

func NewProcessGuard(dir string) *ProcessGuard {
	return &ProcessGuard{Dir: dir, streaks: map[string]processStreak{}, identity: processGuardIdentity}
}

func processIncidentPath(dir string) string { return filepath.Join(dir, "process-incidents.v1.json") }

func ReadProcessIncidents(dir string) (ProcessIncidentReport, error) {
	report := ProcessIncidentReport{SchemaVersion: 1, Incidents: []ProcessIncident{}}
	f, err := os.Open(processIncidentPath(dir))
	if errors.Is(err, os.ErrNotExist) {
		return report, nil
	}
	if err != nil {
		return report, err
	}
	defer f.Close()
	// Bounded reads prevent a malformed incident file from becoming a scan.
	body, err := io.ReadAll(io.LimitReader(f, 2<<20+1))
	if err != nil {
		return report, err
	}
	if len(body) > 2<<20 {
		return report, fmt.Errorf("process incident file exceeds 2 MiB")
	}
	if err = json.Unmarshal(body, &report); err != nil {
		return report, fmt.Errorf("read process incidents: %w", err)
	}
	if report.SchemaVersion != 1 || len(report.Incidents) > processIncidentLimit {
		return report, fmt.Errorf("unsupported process incident ledger")
	}
	return report, nil
}

func updateProcessIncidents(ctx context.Context, dir string, fn func(*ProcessIncidentReport) error) error {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	unlock, err := filelock.AcquireContext(ctx, processIncidentPath(dir), 2*time.Second)
	if err != nil {
		return err
	}
	defer unlock()
	report, err := ReadProcessIncidents(dir)
	if err != nil {
		return err
	}
	before, err := json.Marshal(report)
	if err != nil {
		return err
	}
	if err = fn(&report); err != nil {
		return err
	}
	sort.SliceStable(report.Incidents, func(i, j int) bool { return report.Incidents[i].LastSeen.After(report.Incidents[j].LastSeen) })
	if len(report.Incidents) > processIncidentLimit {
		report.Incidents = report.Incidents[:processIncidentLimit]
	}
	after, err := json.Marshal(report)
	if err != nil {
		return err
	}
	// An idle monitor must not fsync an unchanged ledger on every sample.
	if bytes.Equal(before, after) {
		return nil
	}
	report.UpdatedAt = time.Now().UTC()
	body, err := json.Marshal(report)
	if err != nil {
		return err
	}
	return writeProcessIncidentFile(processIncidentPath(dir), body)
}

func incidentKey(pid int, identity string) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%d:%s", pid, identity)))
	return hex.EncodeToString(sum[:12])
}

// Observe requires distinct inventory timestamps and continuous evidence.
// A gap beyond the configured inventory tolerance resets the streak. PPID 1 alone is common for
// daemons: require ten minutes of age and two minutes above 60% of one core.
// Other long-running processes require thirty minutes of age and five hot minutes.
func (guard *ProcessGuard) Observe(ctx context.Context, at time.Time, processes []Process) error {
	if at.IsZero() || !at.After(guard.lastInventory) {
		return nil
	}
	guard.lastInventory = at
	byPID := make(map[int]int, len(processes))
	for i := range processes {
		byPID[processes[i].PID] = i
	}
	next := map[string]processStreak{}
	var candidates []ProcessIncident
	for _, p := range processes {
		if p.PID <= 1 || p.PID == os.Getpid() || !validCPUEvidence(p.CPUAvailable, p.CPUPercent) || p.CPUPercent < 60 {
			continue
		}
		parentIndex, hasParent := byPID[p.PPID]
		parent := Process{}
		if hasParent {
			parent = processes[parentIndex]
		}
		orphan := p.PPID == 1 || (hasParent && parent.PPID == 1 && isProcessShell(parent.Executable))
		age, window, reason := int64(1800), 5*time.Minute, "sustained_cpu"
		if orphan {
			age, window, reason = 600, 2*time.Minute, "orphaned_sustained_cpu"
		}
		elapsed := p.ElapsedSeconds
		if elapsed == 0 {
			elapsed = parseElapsedSeconds(p.Elapsed)
		}
		if elapsed < age {
			continue
		}
		identity, uid, err := guard.identity(p.PID)
		if err != nil || uid != os.Getuid() {
			continue
		}
		// Native CPU deltas belong to this exact birth, not a recycled PID.
		if p.StartedAtNS != 0 && strings.HasPrefix(identity, "darwin:") {
			expected := fmt.Sprintf("darwin:%d:%d", p.StartedAtNS/int64(time.Second), (p.StartedAtNS%int64(time.Second))/int64(time.Microsecond))
			if identity != expected {
				continue
			}
		}
		key := incidentKey(p.PID, identity)
		streak, ok := guard.streaks[key]
		gap := guard.maxInventoryGap
		if gap < 3*time.Minute {
			gap = 3 * time.Minute
		}
		if !ok || at.Sub(streak.last) > gap || streak.incident.Reason != reason {
			streak.first = at
		}
		streak.last = at
		streak.incident = ProcessIncident{ID: key, PID: p.PID, PPID: p.PPID, StartIdentity: identity, UID: uid, Executable: privacySafeExecutable(p.Executable), ParentExecutable: privacySafeExecutable(parent.Executable), SessionID: p.SessionID, CPUPercent: p.CPUPercent, RSSMB: float64(p.RSSKB) / 1024, AgeSeconds: elapsed, Reason: reason, FirstSeen: streak.first, LastSeen: at, SustainedSeconds: int64(at.Sub(streak.first).Seconds()), State: "active"}
		next[key] = streak
		if at.Sub(streak.first) >= window {
			candidates = append(candidates, streak.incident)
		}
	}
	guard.streaks = next
	// No active detection means no mutation or fsynced lock-owner record.
	// A bounded read suffices to decide whether an old active row needs recovery.
	if len(candidates) == 0 {
		report, err := ReadProcessIncidents(guard.Dir)
		if err != nil {
			return err
		}
		needsRecovery := false
		for _, incident := range report.Incidents {
			if incident.State == "active" {
				needsRecovery = true
				break
			}
		}
		if !needsRecovery {
			return nil
		}
	}
	var pending []ProcessIncident
	err := updateProcessIncidents(ctx, guard.Dir, func(report *ProcessIncidentReport) error {
		active := map[string]bool{}
		for _, candidate := range candidates {
			active[candidate.ID] = true
			index := -1
			for i := range report.Incidents {
				if report.Incidents[i].ID == candidate.ID {
					index = i
					break
				}
			}
			if index >= 0 {
				old := report.Incidents[index]
				candidate.FirstSeen = old.FirstSeen
				candidate.Actions = old.Actions
				candidate.AlertedAt = old.AlertedAt
				if old.State == "stop_requested" {
					candidate.State = old.State
				}
			} else {
				index = len(report.Incidents)
				report.Incidents = append(report.Incidents, candidate)
			}
			if candidate.AlertedAt == nil && guard.Notify != nil {
				pending = append(pending, candidate)
			}
			report.Incidents[index] = candidate
		}
		for i := range report.Incidents {
			if !active[report.Incidents[i].ID] && report.Incidents[i].State == "active" {
				report.Incidents[i].State = "recovered"
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	// Notify only after the incident is durable and the ledger lock released.
	// Delivery is at least once across a crash between send and acknowledgement.
	for _, incident := range pending {
		if err := guard.Notify(incident); err != nil {
			return fmt.Errorf("notify process incident: %w", err)
		}
		if err := updateProcessIncidents(ctx, guard.Dir, func(report *ProcessIncidentReport) error {
			for i := range report.Incidents {
				if report.Incidents[i].ID == incident.ID {
					now := at
					report.Incidents[i].AlertedAt = &now
					break
				}
			}
			return nil
		}); err != nil {
			return err
		}
	}
	return nil
}

func isProcessShell(executable string) bool {
	switch filepath.Base(executable) {
	case "sh", "bash", "zsh", "fish":
		return true
	}
	return false
}

// StopProcessIncident signals only the selected PID. It never expands to a
// process group or agent tree. A durable intent must succeed before the signal.
// Force is a separate explicit action, available only after a graceful request.
func StopProcessIncident(ctx context.Context, dir, id string, force bool) (ProcessIncident, error) {
	return stopProcessIncident(ctx, dir, id, force, processGuardIdentity, func(pid int, s syscall.Signal) error { return syscall.Kill(pid, s) })
}

func stopProcessIncident(ctx context.Context, dir, id string, force bool, identity func(int) (string, int, error), signal func(int, syscall.Signal) error) (ProcessIncident, error) {
	var selected ProcessIncident
	var actionErr error
	err := updateProcessIncidents(ctx, dir, func(report *ProcessIncidentReport) error {
		index := -1
		for i := range report.Incidents {
			if report.Incidents[i].ID == id {
				index = i
				break
			}
		}
		if index < 0 {
			return fmt.Errorf("incident not found; refresh process incidents")
		}
		p := &report.Incidents[index]
		selected = *p
		if p.PID <= 1 || p.PID == os.Getpid() || p.UID != os.Getuid() {
			return fmt.Errorf("process is protected or owned by another user")
		}
		if p.State != "active" && p.State != "stop_requested" {
			return fmt.Errorf("incident is no longer active; refresh process incidents")
		}
		if force && p.State != "stop_requested" {
			return fmt.Errorf("request Stop first; Force Stop is a separate escalation")
		}
		if len(p.Actions) >= 20 {
			return fmt.Errorf("incident action limit reached")
		}
		start, uid, err := identity(p.PID)
		if err != nil || start != p.StartIdentity || uid != p.UID {
			actionErr = fmt.Errorf("process identity changed or unavailable; no signal sent")
			p.Actions = append(p.Actions, ProcessIncidentAction{At: time.Now().UTC(), Result: "revalidation_rejected", Error: actionErr.Error()})
			selected = *p
			return nil
		}
		sig, label := syscall.SIGTERM, "TERM"
		if force {
			sig, label = syscall.SIGKILL, "KILL"
		}
		p.Actions = append(p.Actions, ProcessIncidentAction{At: time.Now().UTC(), Signal: label, Result: "requested"})
		p.State = "stop_requested"
		// Persist the request while holding the same mutation lock. A crash
		// after this boundary leaves an honest requested, unconfirmed action.
		body, err := json.Marshal(report)
		if err != nil {
			return err
		}
		if err = writeProcessIncidentFile(processIncidentPath(dir), body); err != nil {
			return err
		}
		// Check again after disk I/O immediately before the signal boundary.
		start, uid, err = identity(p.PID)
		if err != nil || start != p.StartIdentity || uid != p.UID {
			actionErr = fmt.Errorf("process identity changed or exited; no signal sent")
		} else if ctx.Err() != nil {
			actionErr = ctx.Err()
		} else {
			actionErr = signal(p.PID, sig)
		}
		a := &p.Actions[len(p.Actions)-1]
		if actionErr != nil {
			a.Result = "rejected"
			a.Error = actionErr.Error()
		} else {
			a.Result = "signal_sent_unconfirmed"
			deadline := time.NewTimer(2 * time.Second)
			defer deadline.Stop()
			tick := time.NewTicker(50 * time.Millisecond)
			defer tick.Stop()
		wait:
			for {
				select {
				case <-ctx.Done():
					break wait
				case <-deadline.C:
					break wait
				case <-tick.C:
					current, _, e := identity(p.PID)
					if errors.Is(e, errProcessGuardExited) || errors.Is(e, os.ErrNotExist) || errors.Is(e, syscall.ESRCH) || (e == nil && current != p.StartIdentity) {
						a.Result = "exit_confirmed"
						p.State = "stopped"
						break wait
					}
				}
			}
		}
		selected = *p
		return nil
	})
	return selected, errors.Join(err, actionErr)
}

// A stop receipt must survive a crash, not just a concurrent reader.
func writeProcessIncidentFile(path string, body []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".process-incidents-*")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if _, err = f.Write(body); err != nil {
		_ = f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Rename(name, path); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
