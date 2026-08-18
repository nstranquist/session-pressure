package browser

import "time"

type Session struct {
	Name            string
	PID             int
	StartedAt       time.Time
	LastActivityAt  time.Time
	IdleTimeout     string
	LifecyclePolicy string
	PurgeOnExpiry   bool
}

func (s Session) EffectiveLifecyclePolicy() string {
	if s.LifecyclePolicy != "" {
		return s.LifecyclePolicy
	}
	return LifecycleIdle
}

const (
	LifecycleIdle      = "idle"
	LifecycleKeepAlive = "keep-alive"
)

type IdleExpiryResult struct {
	Done   bool
	Closed bool
	Reason string
}

const IdleExpiryLeaseExpired = "lease_expired"
