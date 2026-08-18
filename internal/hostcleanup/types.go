// Package hostcleanup coordinates conservative, pressure-triggered reclaim of
// typed local development resources. It is deliberately separate from the
// pressure monitor's core policy: cleanup starts observe-only, has its own
// cooldown/audit ledger, and treats claim corruption as a global fail-closed
// condition.
package hostcleanup

import (
	"fmt"
	"strings"
	"time"

	"github.com/nstranquist/session-pressure/sessionpressure"
)

const SchemaVersion = 1

const (
	MinimumObservationWindow        = 7 * 24 * time.Hour
	BrowserMinimumObservationWindow = 2 * MinimumObservationWindow
	DevMinimumObservationWindow     = 3 * MinimumObservationWindow
	DockerMinimumObservationWindow  = 4 * MinimumObservationWindow
)

type ResourceKind string

const (
	ResourceBrowser         ResourceKind = "browser"
	ResourceDevSession      ResourceKind = "dev_session"
	ResourceDockerWorkspace ResourceKind = "docker_workspace"
	ResourceProcess         ResourceKind = "process"
	ResourceOther           ResourceKind = "other"
)

func (kind ResourceKind) Valid() bool {
	switch kind {
	case ResourceBrowser, ResourceDevSession, ResourceDockerWorkspace, ResourceProcess, ResourceOther:
		return true
	default:
		return false
	}
}

type Policy struct {
	SchemaVersion            int                   `json:"schema_version"`
	Enabled                  bool                  `json:"enabled"`
	Enforce                  bool                  `json:"enforce"`
	TriggerLevel             sessionpressure.Level `json:"trigger_level"`
	SustainSamples           int                   `json:"sustain_samples"`
	CooldownSeconds          int                   `json:"cooldown_seconds"`
	MaxActionsPerPass        int                   `json:"max_actions_per_pass"`
	DevSessionMinIdleSeconds int                   `json:"dev_session_min_idle_seconds"`
	BrowserGraceSeconds      int                   `json:"browser_grace_seconds"`
	DockerMinIdleSeconds     int                   `json:"docker_min_idle_seconds"`
	ProcessMaxCPUPercent     float64               `json:"process_max_cpu_percent"`
	BrowserEnabled           bool                  `json:"browser_enabled"`
	DevSessionEnabled        bool                  `json:"dev_session_enabled"`
	DockerWorkspaceEnabled   bool                  `json:"docker_workspace_enabled"`
	ProcessEnabled           bool                  `json:"process_enabled"`
	AutoGraduateProcessOnly  bool                  `json:"auto_graduate_process_only"`
	AutoGraduateNative       bool                  `json:"auto_graduate_native_providers"`
	ObservationStartedAt     time.Time             `json:"observation_started_at,omitempty,omitzero"`
}

func DefaultPolicy() Policy {
	return Policy{
		SchemaVersion:            SchemaVersion,
		Enabled:                  true,
		Enforce:                  false,
		TriggerLevel:             sessionpressure.LevelRed,
		SustainSamples:           2,
		CooldownSeconds:          5 * 60,
		MaxActionsPerPass:        1,
		DevSessionMinIdleSeconds: int((4 * time.Hour) / time.Second),
		BrowserGraceSeconds:      int((2 * time.Minute) / time.Second),
		DockerMinIdleSeconds:     int((2 * time.Hour) / time.Second),
		ProcessMaxCPUPercent:     2.5,
		BrowserEnabled:           true,
		DevSessionEnabled:        true,
		DockerWorkspaceEnabled:   true,
		ProcessEnabled:           true,
	}
}

func (policy Policy) ProcessOnly() bool {
	return policy.ProcessEnabled && !policy.BrowserEnabled && !policy.DevSessionEnabled && !policy.DockerWorkspaceEnabled
}

func (policy Policy) ProcessOnlyGraduationAt() time.Time {
	return policy.graduationAt(MinimumObservationWindow)
}

func (policy Policy) BrowserGraduationAt() time.Time {
	return policy.graduationAt(BrowserMinimumObservationWindow)
}

func (policy Policy) DevGraduationAt() time.Time {
	return policy.graduationAt(DevMinimumObservationWindow)
}

func (policy Policy) DockerGraduationAt() time.Time {
	return policy.graduationAt(DockerMinimumObservationWindow)
}

func (policy Policy) graduationAt(window time.Duration) time.Time {
	if policy.ObservationStartedAt.IsZero() {
		return time.Time{}
	}
	return policy.ObservationStartedAt.Add(window)
}

func (policy Policy) ObservationRemaining(now time.Time) time.Duration {
	if now.IsZero() {
		now = time.Now()
	}
	if policy.ObservationStartedAt.IsZero() {
		return MinimumObservationWindow
	}
	remaining := policy.ProcessOnlyGraduationAt().Sub(now)
	if remaining < 0 {
		return 0
	}
	return remaining
}

// AutoGraduationDue reports whether the next explicitly scheduled rollout
// stage may be committed. Native adapters advance one stage per reconciliation
// so a restored old policy cannot jump from observe-only to every provider in
// a single resident pass.
func (policy Policy) AutoGraduationDue(now time.Time) bool {
	if now.IsZero() {
		now = time.Now()
	}
	if policy.AutoGraduateProcessOnly && !policy.Enforce {
		return !policy.ProcessOnlyGraduationAt().IsZero() && !now.Before(policy.ProcessOnlyGraduationAt())
	}
	if !policy.Enforce || !policy.AutoGraduateNative {
		return false
	}
	switch {
	case !policy.BrowserEnabled:
		return !now.Before(policy.BrowserGraduationAt())
	case !policy.DevSessionEnabled:
		return !now.Before(policy.DevGraduationAt())
	case !policy.DockerWorkspaceEnabled:
		return !now.Before(policy.DockerGraduationAt())
	default:
		return false
	}
}

// ValidateEnforcement applies the current destructive rollout boundary at the
// point of use. CLI transition checks are operator UX, not sufficient safety:
// a legacy or manually edited policy must not bypass the observation window or
// enable native providers before their separate graduations are implemented.
func (policy Policy) ValidateEnforcement(now time.Time) error {
	if !policy.Enforce {
		return nil
	}
	if !policy.Enabled {
		return fmt.Errorf("enforcement cannot be enabled while cleanup is disabled")
	}
	if remaining := policy.ObservationRemaining(now); remaining > 0 {
		return fmt.Errorf("enforcement requires seven days of observation; remaining=%s", remaining.Round(time.Second))
	}
	if !policy.ProcessEnabled {
		return fmt.Errorf("enforcement requires the process stage")
	}
	if policy.BrowserEnabled && now.Before(policy.BrowserGraduationAt()) {
		return fmt.Errorf("browser enforcement requires fourteen days of observation")
	}
	if policy.DevSessionEnabled && now.Before(policy.DevGraduationAt()) {
		return fmt.Errorf("dev-session enforcement requires twenty-one days of observation")
	}
	if policy.DockerWorkspaceEnabled && now.Before(policy.DockerGraduationAt()) {
		return fmt.Errorf("Docker workspace enforcement requires twenty-eight days of observation")
	}
	if policy.DevSessionEnabled && !policy.BrowserEnabled {
		return fmt.Errorf("dev-session enforcement requires the browser stage")
	}
	if policy.DockerWorkspaceEnabled && (!policy.BrowserEnabled || !policy.DevSessionEnabled) {
		return fmt.Errorf("Docker workspace enforcement requires browser and dev-session stages")
	}
	return nil
}

func (policy Policy) Validate() error {
	if policy.SchemaVersion != SchemaVersion {
		return fmt.Errorf("unsupported cleanup schema_version %d", policy.SchemaVersion)
	}
	if policy.TriggerLevel != sessionpressure.LevelRed && policy.TriggerLevel != sessionpressure.LevelCritical {
		return fmt.Errorf("trigger_level must be red or critical")
	}
	if policy.SustainSamples < 2 || policy.SustainSamples > 20 {
		return fmt.Errorf("sustain_samples must be between 2 and 20")
	}
	if policy.CooldownSeconds < 60 || policy.CooldownSeconds > 24*60*60 {
		return fmt.Errorf("cooldown_seconds must be between 60 and 86400")
	}
	if policy.MaxActionsPerPass != 1 {
		return fmt.Errorf("max_actions_per_pass must be 1")
	}
	if policy.DevSessionMinIdleSeconds < int(time.Hour/time.Second) {
		return fmt.Errorf("dev_session_min_idle_seconds must be at least 3600")
	}
	if policy.BrowserGraceSeconds < 0 || policy.BrowserGraceSeconds > int(time.Hour/time.Second) {
		return fmt.Errorf("browser_grace_seconds must be between 0 and 3600")
	}
	if policy.DockerMinIdleSeconds < int((30*time.Minute)/time.Second) {
		return fmt.Errorf("docker_min_idle_seconds must be at least 1800")
	}
	if policy.ProcessMaxCPUPercent < 0 || policy.ProcessMaxCPUPercent > 10 {
		return fmt.Errorf("process_max_cpu_percent must be between 0 and 10")
	}
	if policy.AutoGraduateProcessOnly {
		if !policy.Enabled {
			return fmt.Errorf("auto_graduate_process_only requires cleanup to be enabled")
		}
		if policy.Enforce {
			return fmt.Errorf("auto_graduate_process_only must be cleared once enforcement is enabled")
		}
		if policy.ObservationStartedAt.IsZero() {
			return fmt.Errorf("auto_graduate_process_only requires an explicit observation_started_at")
		}
	}
	if policy.AutoGraduateNative {
		if !policy.Enabled {
			return fmt.Errorf("auto_graduate_native_providers requires cleanup to be enabled")
		}
		if policy.ObservationStartedAt.IsZero() {
			return fmt.Errorf("auto_graduate_native_providers requires an explicit observation_started_at")
		}
	}
	return nil
}

type Claim struct {
	SchemaVersion   int          `json:"schema_version"`
	ID              string       `json:"id"`
	ResourceKind    ResourceKind `json:"resource_kind"`
	ResourceID      string       `json:"resource_id"`
	Owner           string       `json:"owner"`
	AcquiredAt      time.Time    `json:"acquired_at"`
	HeartbeatAt     time.Time    `json:"heartbeat_at"`
	ExpiresAt       time.Time    `json:"expires_at"`
	TTLSeconds      int          `json:"ttl_seconds"`
	CleanupOnStale  bool         `json:"cleanup_on_stale,omitempty"`
	RootPID         int          `json:"root_pid,omitempty"`
	ProcessIdentity string       `json:"process_identity,omitempty"`
	Note            string       `json:"note,omitempty"`
}

type ClaimState string

const (
	ClaimActive ClaimState = "active"
	ClaimStale  ClaimState = "stale"
)

type ClaimView struct {
	Claim
	State      ClaimState `json:"state"`
	AgeSeconds int64      `json:"age_seconds"`
}

func (claim Claim) Validate(now time.Time) error {
	if claim.SchemaVersion != SchemaVersion {
		return fmt.Errorf("claim %q has unsupported schema_version %d", claim.ID, claim.SchemaVersion)
	}
	if !safeToken(claim.ID, 128) {
		return fmt.Errorf("claim id is invalid")
	}
	if !claim.ResourceKind.Valid() {
		return fmt.Errorf("claim %q has invalid resource_kind %q", claim.ID, claim.ResourceKind)
	}
	if !safeResourceID(claim.ResourceID) {
		return fmt.Errorf("claim %q has invalid resource_id", claim.ID)
	}
	if strings.TrimSpace(claim.Owner) == "" || len(claim.Owner) > 128 {
		return fmt.Errorf("claim %q owner is required and must be at most 128 bytes", claim.ID)
	}
	if claim.TTLSeconds < 60 || claim.TTLSeconds > 7*24*60*60 {
		return fmt.Errorf("claim %q ttl_seconds must be between 60 and 604800", claim.ID)
	}
	if claim.AcquiredAt.IsZero() || claim.HeartbeatAt.IsZero() || claim.ExpiresAt.IsZero() {
		return fmt.Errorf("claim %q has incomplete timestamps", claim.ID)
	}
	if claim.HeartbeatAt.Before(claim.AcquiredAt.Add(-time.Second)) || claim.ExpiresAt.Before(claim.HeartbeatAt) {
		return fmt.Errorf("claim %q timestamp ordering is invalid", claim.ID)
	}
	if claim.HeartbeatAt.After(now.Add(5*time.Minute)) || claim.ExpiresAt.After(now.Add(8*24*time.Hour)) {
		return fmt.Errorf("claim %q has an implausible future timestamp", claim.ID)
	}
	if claim.ResourceKind == ResourceProcess {
		if claim.RootPID <= 0 || claim.ProcessIdentity == "" {
			return fmt.Errorf("process claim %q requires root_pid and process_identity", claim.ID)
		}
	} else if claim.RootPID != 0 || claim.ProcessIdentity != "" || claim.CleanupOnStale {
		return fmt.Errorf("claim %q may use process cleanup fields only for resource_kind=process", claim.ID)
	}
	if len(claim.Note) > 256 {
		return fmt.Errorf("claim %q note must be at most 256 bytes", claim.ID)
	}
	return nil
}

func (claim Claim) View(now time.Time) ClaimView {
	state := ClaimActive
	if !now.Before(claim.ExpiresAt) {
		state = ClaimStale
	}
	age := now.Sub(claim.HeartbeatAt)
	if age < 0 {
		age = 0
	}
	return ClaimView{Claim: claim, State: state, AgeSeconds: int64(age.Seconds())}
}

type Candidate struct {
	ResourceKind   ResourceKind `json:"resource_kind"`
	ResourceID     string       `json:"resource_id"`
	Provider       string       `json:"provider"`
	LastActivity   time.Time    `json:"last_activity,omitempty"`
	StaleSince     time.Time    `json:"stale_since,omitempty"`
	EstimatedRAMMB int64        `json:"estimated_ram_mb,omitempty"`
	ClaimState     string       `json:"claim_state"`
	ClaimIDs       []string     `json:"claim_ids,omitempty"`
	Eligible       bool         `json:"eligible"`
	Reason         string       `json:"reason"`
	private        any
}

type Action struct {
	SchemaVersion  int          `json:"schema_version"`
	ID             string       `json:"id"`
	Timestamp      time.Time    `json:"timestamp"`
	ResourceKind   ResourceKind `json:"resource_kind"`
	ResourceID     string       `json:"resource_id"`
	Provider       string       `json:"provider"`
	Result         string       `json:"result"`
	Reason         string       `json:"reason"`
	EstimatedRAMMB int64        `json:"estimated_ram_mb,omitempty"`
	ClaimState     string       `json:"claim_state"`
	Error          string       `json:"error,omitempty"`
}

type Report struct {
	SchemaVersion  int                   `json:"schema_version"`
	Timestamp      time.Time             `json:"timestamp"`
	Mode           string                `json:"mode"`
	Policy         Policy                `json:"policy"`
	PolicyPath     string                `json:"policy_path"`
	MemoryLevel    sessionpressure.Level `json:"memory_level"`
	Triggered      bool                  `json:"triggered"`
	TriggerReason  string                `json:"trigger_reason"`
	Claims         []ClaimView           `json:"claims"`
	Candidates     []Candidate           `json:"candidates"`
	ProviderErrors map[string]string     `json:"provider_errors,omitempty"`
	Action         *Action               `json:"action,omitempty"`
}

func safeToken(value string, limit int) bool {
	if value == "" || len(value) > limit {
		return false
	}
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || strings.ContainsRune("._-", r) {
			continue
		}
		return false
	}
	return true
}

func safeResourceID(value string) bool {
	if value == "" || len(value) > 256 || strings.ContainsRune(value, '\x00') {
		return false
	}
	clean := strings.TrimSpace(value)
	return clean == value && clean != "" && !strings.Contains(value, "..")
}
