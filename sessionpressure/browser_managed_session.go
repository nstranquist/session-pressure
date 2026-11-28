package sessionpressure

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"time"
)

// managedSessionRecord is the part of a snapref session record
// (snapref browser.Session, <root>/sessions/<name>.json) that bounds its
// lifetime. snapref launches each managed browser with
// --user-data-dir=<root>/profiles/<name>, so the record is found from the
// process alone, for any browser home (ndev uses ~/.nicos-dev/browser).
type managedSessionRecord struct {
	PID              int       `json:"pid"`
	ProcessStartedAt time.Time `json:"process_started_at"`
	StartedAt        time.Time `json:"started_at"`
	LastActivityAt   time.Time `json:"last_activity_at"`
	IdleTimeout      string    `json:"idle_timeout"`
	LifecyclePolicy  string    `json:"lifecycle_policy"`
}

// managedSessionGrace covers the snapref idle watchdog's poll (at most one
// minute for normal leases) between lease expiry and its own close.
const managedSessionGrace = time.Minute

// managedSessionRecordLimit bounds the record read.
const managedSessionRecordLimit = 64 << 10

// managedSessionLeaseActive reports whether root is a snapref-managed browser
// with an unexpired idle lease. Such a session is bounded by its own idle
// watchdog, so the resident's fixed lifetime must not end it while it is in
// use. The record must name this PID and, when it recorded one,
// the same process start; a keep-alive or unmanaged session is not exempt.
// After the lease plus one watchdog poll, the fixed lifetime applies again, so
// a dead watchdog cannot leave a browser running.
func managedSessionLeaseActive(root browserProcess, now time.Time) bool {
	if root.UserDataDir == "" || !filepath.IsAbs(root.UserDataDir) {
		return false
	}
	profileDir := filepath.Clean(root.UserDataDir)
	profiles := filepath.Dir(profileDir)
	if filepath.Base(profiles) != "profiles" {
		return false
	}
	name := filepath.Base(profileDir)
	file, err := os.Open(filepath.Join(filepath.Dir(profiles), "sessions", name+".json"))
	if err != nil {
		return false
	}
	defer file.Close()
	var record managedSessionRecord
	if err := json.NewDecoder(io.LimitReader(file, managedSessionRecordLimit)).Decode(&record); err != nil {
		return false
	}
	if record.PID != root.PID || record.LifecyclePolicy != "idle" {
		return false
	}
	if !record.ProcessStartedAt.IsZero() && !root.StartedAt.IsZero() {
		skew := record.ProcessStartedAt.Sub(root.StartedAt)
		if skew < -2*time.Second || skew > 2*time.Second {
			return false // PID reused by a newer browser
		}
	}
	idle, err := time.ParseDuration(record.IdleTimeout)
	if err != nil || idle <= 0 {
		return false
	}
	last := record.StartedAt
	if record.LastActivityAt.After(last) {
		last = record.LastActivityAt
	}
	if last.IsZero() {
		return false
	}
	return now.Before(last.Add(idle + managedSessionGrace))
}
