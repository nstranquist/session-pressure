package browser

import (
	"errors"
	"time"
)

// ErrNotAvailable is returned by the extract stub. Pageskein reclaim stays in nicos-tools.
var ErrNotAvailable = errors.New("pageskein reclaim is not available in the open extract")

type GCOptions struct {
	OlderThan time.Duration
	Apply     bool
	Now       func() time.Time
}

type GCEntry struct {
	Kind       string    `json:"kind"`
	Name       string    `json:"name"`
	Path       string    `json:"path"`
	AgeSeconds int64     `json:"age_seconds"`
	SizeBytes  int64     `json:"size_bytes,omitempty"`
	StartedAt  time.Time `json:"started_at,omitempty"`
	ModTime    time.Time `json:"mod_time,omitempty"`
	Removed    bool      `json:"removed,omitempty"`
	Error      string    `json:"error,omitempty"`
}

type GCReport struct {
	DryRun           bool      `json:"dry_run"`
	OlderThan        string    `json:"older_than"`
	Cutoff           time.Time `json:"cutoff"`
	StaleKeepAlive   []GCEntry `json:"stale_keep_alive"`
	DeadSessions     []GCEntry `json:"dead_sessions"`
	DeadProfiles     []GCEntry `json:"dead_profiles"`
	RemovedSessions  int       `json:"removed_sessions"`
	RemovedProfiles  int       `json:"removed_profiles"`
	ReclaimableBytes int64     `json:"reclaimable_bytes"`
	RemovedBytes     int64     `json:"removed_bytes"`
}

// GC is closed in the extract. A successful empty report would let storage
// apply report mutation completed for a no-op.
func GC(opts GCOptions) (*GCReport, error) {
	return nil, ErrNotAvailable
}
