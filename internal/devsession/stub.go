package devsession

import "time"

type Provenance struct {
	Workspace   string
	App         string
	StartedAt   string
	LogPath     string
	TmuxSession string
}

type ScopeEntry struct {
	Scope           string
	Alive           bool
	Attached        bool
	AttachmentKnown bool
	Provenance      Provenance
}

type IdleTeardownExpectation struct {
	StartedAt   string
	TmuxSession string
	MinimumIdle time.Duration
	Now         time.Time
}
