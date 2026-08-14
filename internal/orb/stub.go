package orb

type Snapshot struct{}

type Policy struct {
	MinIdleMinutes int
}

type TrimAction struct {
	WorkspaceID    string
	Workspace      string
	Action         string
	Reason         string
	ReclaimedRAMMB int64
	IdleMinutes    int
	LastUsedAt     string
	Error          string
}

func DefaultPolicy() Policy { return Policy{} }
