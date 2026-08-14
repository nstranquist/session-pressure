package sessionpressurecleanup

import (
	"context"
	"sync"
	"time"
)

// Providers are the nicos-only reclaim hooks. The extract CLI stays
// OSS-stubbed until a factory process calls Install.
type Providers struct {
	ListBrowser   func() ([]BrowserSession, error)
	ExpireBrowser func(name string, pid int, timeout time.Duration, purge bool, now time.Time) (BrowserExpireResult, error)
	ListDev       func() ([]DevScope, error)
	TeardownDev   func(workspace, app, startedAt, tmux string, minIdle time.Duration, now time.Time) (stopped bool, reason string, err error)
	PlanDocker    func(ctx context.Context, minIdleMinutes, maxActions int) ([]DockerAction, error)
	ApplyDocker   func(ctx context.Context, action DockerAction) (DockerAction, error)
}

type BrowserSession struct {
	Name           string
	PID            int
	IdleTimeout    string
	Lifecycle      string
	PurgeOnExpiry  bool
	LastActivityAt time.Time
	StartedAt      time.Time
}

type BrowserExpireResult struct {
	Closed bool
	Reason string
}

type DevScope struct {
	Scope           string
	Alive           bool
	Attached        bool
	AttachmentKnown bool
	Workspace       string
	App             string
	StartedAt       string
	LogPath         string
	TmuxSession     string
}

type DockerAction struct {
	WorkspaceID    string
	Workspace      string
	Action         string
	Reason         string
	ReclaimedRAMMB int64
	IdleMinutes    int
	LastUsedAt     string
	Error          string
}

var (
	mu      sync.RWMutex
	current Providers
)

func Install(providers Providers) {
	mu.Lock()
	current = providers
	mu.Unlock()
}

func Reset() {
	mu.Lock()
	current = Providers{}
	mu.Unlock()
}

func Current() Providers {
	mu.RLock()
	defer mu.RUnlock()
	return current
}
