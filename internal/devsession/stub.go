package devsession

type Provenance struct {
	Workspace string
	App       string
}
type ScopeEntry struct {
	Scope            string
	Alive            bool
	Attached         bool
	AttachmentKnown  bool
	Provenance       Provenance
}
type IdleTeardownExpectation struct{}
