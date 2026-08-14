package hostcleanup

import (
	"context"
	"os"
	"path/filepath"
	"time"

	"github.com/nstranquist/session-pressure/internal/devsession"
	"github.com/nstranquist/session-pressure/internal/orb"
	"github.com/nstranquist/session-pressure/sessionpressure"
	"github.com/nstranquist/session-pressure/third_party/pageskein/browser"
)

type browserCandidate struct {
	Session browser.Session
	Timeout time.Duration
}

func configureBrowserHome() {
	if os.Getenv("PAGESKEIN_HOME") != "" || os.Getenv("NDEV_BROWSER_HOME") != "" {
		return
	}
	home, err := os.UserHomeDir()
	if err == nil {
		_ = os.Setenv("NDEV_BROWSER_HOME", filepath.Join(home, ".nicos-dev", "browser"))
	}
}

func (manager *Manager) browserCandidates(policy Policy) ([]Candidate, error) {
	configureBrowserHome()
	sessions, err := manager.listBrowser()
	if err != nil {
		return nil, err
	}
	now := manager.now().UTC()
	result := make([]Candidate, 0, len(sessions))
	for _, session := range sessions {
		candidate := Candidate{
			ResourceKind: ResourceBrowser, ResourceID: session.Name, Provider: "ndev_browser",
			ClaimState: "native", Eligible: false, Reason: "browser lifecycle is protected",
			private: browserCandidate{Session: session},
		}
		policyKind := session.EffectiveLifecyclePolicy()
		if policyKind != browser.LifecycleIdle {
			if policyKind == browser.LifecycleKeepAlive {
				candidate.Reason = "explicit keep-alive browser claim"
			} else {
				candidate.Reason = "unmanaged browser lifecycle"
			}
			result = append(result, candidate)
			continue
		}
		timeout, parseErr := time.ParseDuration(session.IdleTimeout)
		if parseErr != nil || timeout <= 0 {
			candidate.Reason = "invalid native browser idle lease"
			result = append(result, candidate)
			continue
		}
		last := session.LastActivityAt
		if last.IsZero() {
			last = session.StartedAt
		}
		staleAt := last.Add(timeout).Add(time.Duration(policy.BrowserGraceSeconds) * time.Second)
		candidate.LastActivity = last
		candidate.StaleSince = staleAt
		candidate.private = browserCandidate{Session: session, Timeout: timeout}
		if !last.IsZero() && !now.Before(staleAt) {
			candidate.Eligible = true
			candidate.ClaimState = "native_stale"
			candidate.Reason = "native browser idle lease expired beyond pressure grace"
		} else {
			candidate.Reason = "native browser idle lease is active"
		}
		result = append(result, candidate)
	}
	return result, nil
}

func (manager *Manager) applyBrowser(candidate Candidate) (string, string) {
	value, ok := candidate.private.(browserCandidate)
	if !ok || value.Session.PID <= 0 || value.Timeout <= 0 {
		return "revalidation_rejected", "browser candidate identity is incomplete"
	}
	result, err := manager.expireBrowser(value.Session.Name, value.Session.PID, value.Timeout, value.Session.PurgeOnExpiry, manager.now().UTC())
	if err != nil {
		return "revalidation_rejected", boundedError(err)
	}
	if result.Closed {
		return "browser_closed", ""
	}
	return "revalidation_rejected", string(result.Reason)
}

type devCandidate struct {
	Entry        devsession.ScopeEntry
	LastActivity time.Time
}

func (manager *Manager) devCandidates(policy Policy) ([]Candidate, error) {
	entries, err := manager.listDev()
	if err != nil {
		return nil, err
	}
	now := manager.now().UTC()
	minIdle := time.Duration(policy.DevSessionMinIdleSeconds) * time.Second
	result := make([]Candidate, 0, len(entries))
	for _, entry := range entries {
		started, _ := time.Parse(time.RFC3339, entry.Provenance.StartedAt)
		last := started
		if entry.Provenance.LogPath != "" {
			if info, statErr := os.Stat(entry.Provenance.LogPath); statErr == nil {
				last = maxTime(last, info.ModTime().UTC())
			}
		}
		resourceID := entry.Scope + "/" + entry.Provenance.App
		candidate := Candidate{
			ResourceKind: ResourceDevSession, ResourceID: resourceID, Provider: "ndev_dev",
			LastActivity: last, ClaimState: "unclaimed", Eligible: false,
			Reason:  "dev session has recent log or start activity",
			private: devCandidate{Entry: entry, LastActivity: last},
		}
		if !entry.AttachmentKnown {
			candidate.Reason = "dev session attachment state could not be verified"
		} else if entry.Attached {
			candidate.Reason = "dev session is attached to an active tmux client"
		} else if !last.IsZero() {
			candidate.StaleSince = last.Add(minIdle)
			if !now.Before(candidate.StaleSince) {
				candidate.Eligible = true
				candidate.Reason = "detached dev session exceeded the pressure idle boundary"
			}
		}
		result = append(result, candidate)
	}
	return result, nil
}

func (manager *Manager) applyDev(candidate Candidate, policy Policy) (string, string) {
	value, ok := candidate.private.(devCandidate)
	if !ok {
		return "revalidation_rejected", "dev candidate identity is incomplete"
	}
	entries, err := manager.listDev()
	if err != nil {
		return "revalidation_rejected", boundedError(err)
	}
	for _, current := range entries {
		if current.Scope != value.Entry.Scope || current.Provenance.App != value.Entry.Provenance.App {
			continue
		}
		if current.Provenance.StartedAt != value.Entry.Provenance.StartedAt || current.Provenance.TmuxSession != value.Entry.Provenance.TmuxSession {
			return "revalidation_rejected", "dev session generation changed"
		}
		if !current.AttachmentKnown {
			return "revalidation_rejected", "dev session attachment state could not be verified"
		}
		if current.Attached {
			return "revalidation_rejected", "dev session became attached"
		}
		last := time.Time{}
		if current.Provenance.LogPath != "" {
			if info, statErr := os.Stat(current.Provenance.LogPath); statErr == nil {
				last = info.ModTime().UTC()
			}
		}
		if last.After(value.LastActivity) || manager.now().UTC().Sub(maxTime(last, value.LastActivity)) < time.Duration(policy.DevSessionMinIdleSeconds)*time.Second {
			return "revalidation_rejected", "dev session became active"
		}
		stopped, reason, err := manager.teardownDev(current.Provenance.Workspace, current.Provenance.App, devsession.IdleTeardownExpectation{
			StartedAt: current.Provenance.StartedAt, TmuxSession: current.Provenance.TmuxSession,
			MinimumIdle: time.Duration(policy.DevSessionMinIdleSeconds) * time.Second,
			Now:         manager.now().UTC(),
		})
		if err != nil {
			return "error", boundedError(err)
		}
		if !stopped {
			if reason == "" {
				reason = "dev session is no longer reclaimable"
			}
			return "revalidation_rejected", reason
		}
		return "dev_session_stopped", ""
	}
	return "revalidation_rejected", "dev session disappeared"
}

type dockerCandidate struct {
	Action orb.TrimAction
}

func (manager *Manager) dockerCandidates(ctx context.Context, policy Policy) ([]Candidate, error) {
	snapshot, err := manager.collectOrb(ctx)
	if err != nil {
		return nil, err
	}
	orbPolicy, _, err := manager.loadOrbPolicy()
	if err != nil {
		return nil, err
	}
	orbPolicy.MinIdleMinutes = max(orbPolicy.MinIdleMinutes, policy.DockerMinIdleSeconds/60)
	actions, err := manager.planOrb(ctx, snapshot, orbPolicy, policy.MaxActionsPerPass)
	if err != nil {
		return nil, err
	}
	result := make([]Candidate, 0, len(actions))
	for _, action := range actions {
		id := action.Workspace
		if id == "" {
			id = action.WorkspaceID
		}
		candidate := Candidate{
			ResourceKind: ResourceDockerWorkspace, ResourceID: id, Provider: "ndev_orb_workspace",
			EstimatedRAMMB: action.ReclaimedRAMMB, ClaimState: "unclaimed",
			Eligible: action.Action == "would_stop", Reason: action.Reason,
			private: dockerCandidate{Action: action},
		}
		if action.IdleMinutes > 0 {
			candidate.LastActivity = manager.now().UTC().Add(-time.Duration(action.IdleMinutes) * time.Minute)
			candidate.StaleSince = candidate.LastActivity.Add(time.Duration(policy.DockerMinIdleSeconds) * time.Second)
		}
		result = append(result, candidate)
	}
	return result, nil
}

func (manager *Manager) applyDocker(ctx context.Context, candidate Candidate, policy Policy) (string, string) {
	value, ok := candidate.private.(dockerCandidate)
	if !ok {
		return "revalidation_rejected", "docker candidate identity is incomplete"
	}
	snapshot, err := manager.collectOrb(ctx)
	if err != nil {
		return "revalidation_rejected", boundedError(err)
	}
	orbPolicy, _, err := manager.loadOrbPolicy()
	if err != nil {
		return "revalidation_rejected", boundedError(err)
	}
	orbPolicy.MinIdleMinutes = max(orbPolicy.MinIdleMinutes, policy.DockerMinIdleSeconds/60)
	// A negative limit asks the pure planner for every still-safe candidate so
	// the originally selected workspace can be matched by exact identity even
	// if another workspace's ranking changed between plan and apply.
	plan, err := manager.planOrb(ctx, snapshot, orbPolicy, -1)
	if err != nil {
		return "revalidation_rejected", boundedError(err)
	}
	var selected *orb.TrimAction
	for index := range plan {
		action := &plan[index]
		if action.WorkspaceID == value.Action.WorkspaceID && action.Workspace == value.Action.Workspace && action.Action == "would_stop" {
			selected = action
			break
		}
	}
	if selected == nil {
		return "revalidation_rejected", "docker workspace is no longer reclaimable"
	}
	applied, err := manager.applyOrb(ctx, []orb.TrimAction{*selected})
	if err != nil {
		return "error", boundedError(err)
	}
	if len(applied) != 1 {
		return "error", "docker provider returned no result"
	}
	if applied[0].Action == "stopped" {
		return "docker_workspace_stopped", ""
	}
	if applied[0].Error != "" {
		return "error", applied[0].Error
	}
	return "revalidation_rejected", applied[0].Reason
}

type processCandidate struct {
	Claim Claim
}

// Process inspection captures the whole process table. Bound those captures per
// cleanup plan so a corrupted or adversarial claim set cannot add unbounded
// work while the host is already under memory pressure. Deferred claims remain
// visible and ineligible; native providers later in the plan are unaffected.
const maxProcessClaimsInspectedPerPlan = 8
const processClaimInspectionRotationInterval = 15 * time.Second

func (manager *Manager) processCandidates(ctx context.Context, claims []ClaimView, policy Policy) []Candidate {
	processClaims := make([]ClaimView, 0, len(claims))
	for _, view := range claims {
		if view.ResourceKind == ResourceProcess && view.State == ClaimStale && view.CleanupOnStale {
			processClaims = append(processClaims, view)
		}
	}
	result := make([]Candidate, 0, len(processClaims))
	if len(processClaims) == 0 {
		return result
	}
	rotation := int(manager.now().Unix()/int64(processClaimInspectionRotationInterval/time.Second)) % len(processClaims)
	if rotation < 0 {
		rotation += len(processClaims)
	}
	inspected := 0
	for index := range processClaims {
		view := processClaims[(rotation+index)%len(processClaims)]
		candidate := Candidate{
			ResourceKind: ResourceProcess, ResourceID: view.ResourceID, Provider: "claimed_process",
			LastActivity: view.HeartbeatAt, StaleSince: view.ExpiresAt,
			ClaimState: string(ClaimStale), ClaimIDs: []string{view.ID}, Eligible: false,
			Reason:  "stale process claim has not passed current identity and activity inspection",
			private: processCandidate{Claim: view.Claim},
		}
		if inspected >= maxProcessClaimsInspectedPerPlan {
			candidate.Reason = "stale process claim inspection deferred by per-pass safety limit"
			result = append(result, candidate)
			continue
		}
		inspected++
		inspect := manager.inspectProcess
		if inspect == nil {
			inspect = sessionpressure.InspectClaimedProcessTree
		}
		inspection, err := inspect(ctx, sessionpressure.ClaimedProcessExpectation{
			RootPID: view.RootPID, ProcessIdentity: view.ProcessIdentity,
			MaxCPUPercent: policy.ProcessMaxCPUPercent,
		})
		if err != nil {
			candidate.Reason = "stale process claim is not currently reclaimable: " + boundedError(err)
			result = append(result, candidate)
			continue
		}
		candidate.Eligible = true
		candidate.EstimatedRAMMB = int64(inspection.RSSSumMB)
		candidate.Reason = "opted-in stale process claim passed current identity and activity inspection"
		result = append(result, candidate)
	}
	return result
}

func (manager *Manager) applyProcess(ctx context.Context, candidate Candidate, policy Policy) (string, string) {
	value, ok := candidate.private.(processCandidate)
	if !ok {
		return "revalidation_rejected", "process candidate identity is incomplete"
	}
	result, err := manager.reapProcess(ctx, sessionpressure.ClaimedProcessExpectation{
		RootPID: value.Claim.RootPID, ProcessIdentity: value.Claim.ProcessIdentity,
		MaxCPUPercent: policy.ProcessMaxCPUPercent,
	})
	if err != nil {
		return "revalidation_rejected", boundedError(err)
	}
	return result.Result, ""
}
