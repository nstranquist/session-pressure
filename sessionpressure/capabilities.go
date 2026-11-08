package sessionpressure

import (
	"os"
	"runtime"
	"time"
)

// Capabilities is a cheap, read-only description of this executing authority.
// Provider actionability is deliberately not inferred here: each provider's
// inventory/plan is the authority for whether a particular reclaim is safe.
type Capabilities struct {
	SchemaVersion         int       `json:"schema_version"`
	Version               string    `json:"version"`
	SourceRevision        string    `json:"source_revision,omitempty"`
	Executable            string    `json:"executable"`
	OS                    string    `json:"os"`
	Arch                  string    `json:"arch"`
	CapturedAt            time.Time `json:"captured_at"`
	Operations            []string  `json:"operations"`
	NativeSampling        bool      `json:"native_sampling"`
	ProviderActionability string    `json:"provider_actionability"`
	PolicyAuthority       string    `json:"policy_authority"`
}

func CurrentCapabilities() Capabilities {
	executable, _ := os.Executable()
	return Capabilities{SchemaVersion: 1, Version: Version, SourceRevision: SourceRevision(),
		Executable: executable, OS: runtime.GOOS, Arch: runtime.GOARCH, CapturedAt: time.Now().UTC(),
		Operations:     []string{"capabilities", "snapshot", "check", "self-test", "recovery", "idle", "browser", "remote", "processes", "doctor", "audit", "board", "status", "identity", "work", "storage", "io", "policy", "monitor", "telemetry", "cleanup", "artifact", "api"},
		NativeSampling: runtime.GOOS == "darwin", ProviderActionability: "provider-inventory-and-plan", PolicyAuthority: "persisted-policy"}
}
