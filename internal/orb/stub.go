package orb

type Snapshot struct{}
type Policy struct{}
type TrimAction struct {
	WorkspaceID string
	Workspace   string
	Action      string
}
func DefaultPolicy() Policy { return Policy{} }
