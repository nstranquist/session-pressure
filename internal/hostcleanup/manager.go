package hostcleanup

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/nstranquist/session-pressure/third_party/pageskein/browser"
	"github.com/nstranquist/session-pressure/internal/devsession"
	"github.com/nstranquist/session-pressure/internal/filelock"
	"github.com/nstranquist/session-pressure/internal/orb"
	"github.com/nstranquist/session-pressure/internal/sessionpressure"
)

type Manager struct {
	Dir string
	Now func() time.Time

	listBrowser    func() ([]browser.Session, error)
	expireBrowser  func(string, int, time.Duration, bool, time.Time) (browser.IdleExpiryResult, error)
	listDev        func() ([]devsession.ScopeEntry, error)
	teardownDev    func(string, string, devsession.IdleTeardownExpectation) (bool, string, error)
	collectOrb     func(context.Context) (orb.Snapshot, error)
	loadOrbPolicy  func() (orb.Policy, string, error)
	planOrb        func(orb.Snapshot, orb.Policy, int) []orb.TrimAction
	applyOrb       func([]orb.TrimAction) []orb.TrimAction
	inspectProcess func(context.Context, sessionpressure.ClaimedProcessExpectation) (sessionpressure.ClaimedProcessResult, error)
	reapProcess    func(context.Context, sessionpressure.ClaimedProcessExpectation) (sessionpressure.ClaimedProcessResult, error)
	memoryLevel    func(sessionpressure.Snapshot) (sessionpressure.Level, error)
}

func NewManager(dir string) *Manager {
	manager := &Manager{
		Dir: dir, Now: time.Now,
		listBrowser: func() ([]browser.Session, error) { return nil, nil }, expireBrowser: stubExpireBrowser,
		listDev: func() ([]devsession.ScopeEntry, error) { return nil, nil }, teardownDev: stubTeardownDev,
		collectOrb: func(context.Context) (orb.Snapshot, error) { return orb.Snapshot{}, nil }, loadOrbPolicy: func() (orb.Policy, string, error) { return orb.Policy{}, "oss", nil },
		planOrb: func(orb.Snapshot, orb.Policy, int) []orb.TrimAction { return nil }, applyOrb: func(actions []orb.TrimAction) []orb.TrimAction { return actions },
		inspectProcess: sessionpressure.InspectClaimedProcessTree,
		reapProcess:    sessionpressure.ReapClaimedProcessTree,
	}
	manager.memoryLevel = manager.evaluateMemoryLevel
	return manager
}

func (manager *Manager) MaybeRelieve(ctx context.Context, snapshot sessionpressure.Snapshot) (sessionpressure.ResourceCleanupResult, error) {
	result := sessionpressure.ResourceCleanupResult{}
	if manager == nil || strings.TrimSpace(manager.Dir) == "" {
		return result, nil
	}
	now := manager.now().UTC()
	policy, persisted, err := LoadPolicy(manager.Dir)
	if err != nil {
		return result, err
	}
	if persisted && policy.AutoGraduationDue(now) {
		// The time gate is necessary but not sufficient. A freshly restarted or
		// unhealthy resident waits until it has current budget and inventory
		// evidence before it is allowed to commit the scheduled transition.
		if snapshot.GuardRole != "resident" || !snapshot.GuardBudgetOK || !snapshot.GuardBaselineProven || !snapshot.ProcessInventoryFresh {
			return result, nil
		}
		var graduated bool
		policy, persisted, graduated, err = ReconcileAutoGraduation(manager.Dir, now)
		if err != nil {
			return result, err
		}
		if graduated {
			return result, nil
		}
	}
	if !persisted || !policy.Enabled || !policy.Enforce {
		return result, nil
	}
	if err := policy.ValidateEnforcement(now); err != nil {
		return result, fmt.Errorf("reject unsafe cleanup policy: %w", err)
	}
	memoryLevel, err := manager.currentMemoryLevel(snapshot)
	if err != nil {
		return result, fmt.Errorf("evaluate memory pressure: %w", err)
	}
	triggered, _ := cleanupTriggered(memoryLevel, snapshot.MemoryConsecutiveSamples, policy)
	if !triggered || snapshot.GuardRole != "resident" || !snapshot.GuardBudgetOK || !snapshot.GuardBaselineProven || !snapshot.ProcessInventoryFresh {
		return result, nil
	}

	if err := os.MkdirAll(manager.Dir, 0o700); err != nil {
		return result, err
	}
	unlock, err := filelock.Acquire(filepath.Join(manager.Dir, "resource-cleanup-run"), time.Second)
	if err != nil {
		// Another monitor generation or operator plan owns the serialized pass.
		// It is not an action attempt and must not produce a retry storm.
		return result, nil
	}
	defer unlock()

	actions, err := ReadActions(manager.Dir, 256)
	if err != nil {
		return result, fmt.Errorf("load cleanup cooldown history: %w", err)
	}
	now = manager.now().UTC()
	if withinCleanupCooldown(actions, now, time.Duration(policy.CooldownSeconds)*time.Second) {
		return result, nil
	}

	report, err := manager.plan(ctx, snapshot, memoryLevel, policy, "enforce")
	if err != nil {
		return result, err
	}
	candidate, found := firstEligible(report.Candidates)
	if !found {
		return result, nil
	}
	unlockPolicy, err := filelock.Acquire(PolicyPath(manager.Dir), 5*time.Second)
	if err != nil {
		return result, nil
	}
	defer unlockPolicy()
	finalPolicy, finalPersisted, err := LoadPolicy(manager.Dir)
	if err != nil {
		return result, fmt.Errorf("reload cleanup policy at final boundary: %w", err)
	}
	if !finalPersisted || !finalPolicy.Enabled || !finalPolicy.Enforce || finalPolicy != policy {
		return result, nil
	}
	if err := finalPolicy.ValidateEnforcement(manager.now().UTC()); err != nil {
		return result, fmt.Errorf("reject unsafe cleanup policy at final boundary: %w", err)
	}
	result.Attempted = true
	result.ResourceKind = string(candidate.ResourceKind)
	result.ResourceID = candidate.ResourceID

	actionID, err := randomID("cleanup")
	if err != nil {
		return result, err
	}
	intent := Action{
		SchemaVersion: SchemaVersion, ID: actionID, Timestamp: now,
		ResourceKind: candidate.ResourceKind, ResourceID: candidate.ResourceID, Provider: candidate.Provider,
		Result: "intent_recorded", Reason: candidate.Reason, EstimatedRAMMB: candidate.EstimatedRAMMB,
		ClaimState: candidate.ClaimState,
	}
	if err := appendAction(manager.Dir, intent); err != nil {
		return result, fmt.Errorf("persist cleanup intent: %w", err)
	}

	final := intent
	final.Timestamp = manager.now().UTC()
	final.Result, final.Error = manager.applyCandidate(ctx, candidate, policy)
	if final.Error != "" {
		final.Reason = "final provider revalidation or graceful cleanup failed"
	}
	if err := appendAction(manager.Dir, final); err != nil {
		return result, fmt.Errorf("persist cleanup result: %w", err)
	}
	result.Result = final.Result
	result.Acted = cleanupResultActed(final.Result)
	return result, nil
}

func (manager *Manager) Plan(ctx context.Context, snapshot sessionpressure.Snapshot) (Report, error) {
	policy, _, err := LoadPolicy(manager.Dir)
	if err != nil {
		return Report{}, err
	}
	memoryLevel, err := manager.currentMemoryLevel(snapshot)
	if err != nil {
		return Report{}, fmt.Errorf("evaluate memory pressure: %w", err)
	}
	return manager.plan(ctx, snapshot, memoryLevel, policy, "plan")
}

func (manager *Manager) plan(ctx context.Context, snapshot sessionpressure.Snapshot, memoryLevel sessionpressure.Level, policy Policy, mode string) (Report, error) {
	now := manager.now().UTC()
	claims, err := NewClaimStore(manager.Dir).List()
	if err != nil {
		return Report{}, fmt.Errorf("load resource claims: %w", err)
	}
	triggered, reason := cleanupTriggered(memoryLevel, snapshot.MemoryConsecutiveSamples, policy)
	report := Report{
		SchemaVersion: SchemaVersion, Timestamp: now, Mode: mode, Policy: policy,
		PolicyPath: PolicyPath(manager.Dir), MemoryLevel: memoryLevel, Triggered: triggered, TriggerReason: reason,
		Claims: claims, Candidates: []Candidate{}, ProviderErrors: map[string]string{},
	}

	if policy.DockerWorkspaceEnabled {
		candidates, providerErr := manager.dockerCandidates(ctx, policy)
		if providerErr != nil {
			report.ProviderErrors["docker_workspace"] = providerErr.Error()
		} else {
			report.Candidates = append(report.Candidates, candidates...)
		}
	}
	if policy.DevSessionEnabled {
		candidates, providerErr := manager.devCandidates(policy)
		if providerErr != nil {
			report.ProviderErrors["dev_session"] = providerErr.Error()
		} else {
			report.Candidates = append(report.Candidates, candidates...)
		}
	}
	if policy.BrowserEnabled {
		candidates, providerErr := manager.browserCandidates(policy)
		if providerErr != nil {
			report.ProviderErrors["browser"] = providerErr.Error()
		} else {
			report.Candidates = append(report.Candidates, candidates...)
		}
	}
	if policy.ProcessEnabled {
		report.Candidates = append(report.Candidates, manager.processCandidates(ctx, claims, policy)...)
	}

	for index := range report.Candidates {
		decorateClaims(&report.Candidates[index], claims)
	}
	sort.SliceStable(report.Candidates, func(i, j int) bool {
		if report.Candidates[i].Eligible != report.Candidates[j].Eligible {
			return report.Candidates[i].Eligible
		}
		if report.Candidates[i].EstimatedRAMMB != report.Candidates[j].EstimatedRAMMB {
			return report.Candidates[i].EstimatedRAMMB > report.Candidates[j].EstimatedRAMMB
		}
		if !report.Candidates[i].LastActivity.Equal(report.Candidates[j].LastActivity) {
			return report.Candidates[i].LastActivity.Before(report.Candidates[j].LastActivity)
		}
		if report.Candidates[i].ResourceKind != report.Candidates[j].ResourceKind {
			return report.Candidates[i].ResourceKind < report.Candidates[j].ResourceKind
		}
		return report.Candidates[i].ResourceID < report.Candidates[j].ResourceID
	})
	if len(report.ProviderErrors) == 0 {
		report.ProviderErrors = nil
	}
	return report, nil
}

func cleanupTriggered(memoryLevel sessionpressure.Level, consecutiveSamples int, policy Policy) (bool, string) {
	if !policy.Enabled {
		return false, "cleanup policy is disabled"
	}
	if !memoryLevel.AtLeast(policy.TriggerLevel) {
		return false, fmt.Sprintf("memory level %s is below %s", memoryLevel, policy.TriggerLevel)
	}
	if consecutiveSamples < policy.SustainSamples {
		return false, fmt.Sprintf("pressure has %d/%d sustained samples", consecutiveSamples, policy.SustainSamples)
	}
	return true, fmt.Sprintf("memory level %s sustained for %d samples", memoryLevel, consecutiveSamples)
}

func (manager *Manager) currentMemoryLevel(snapshot sessionpressure.Snapshot) (sessionpressure.Level, error) {
	if manager.memoryLevel != nil {
		return manager.memoryLevel(snapshot)
	}
	return manager.evaluateMemoryLevel(snapshot)
}

func (manager *Manager) evaluateMemoryLevel(snapshot sessionpressure.Snapshot) (sessionpressure.Level, error) {
	policy, _, err := sessionpressure.LoadPolicy(sessionpressure.PolicyPath(manager.Dir), snapshot.PhysicalMemoryMB)
	if err != nil {
		return sessionpressure.LevelNormal, err
	}
	return sessionpressure.EvaluateMemoryPressure(snapshot, policy).Level, nil
}

func decorateClaims(candidate *Candidate, claims []ClaimView) {
	if candidate == nil {
		return
	}
	active := false
	stale := false
	for _, claim := range claims {
		if claim.ResourceKind != candidate.ResourceKind || claim.ResourceID != candidate.ResourceID {
			continue
		}
		if !containsString(candidate.ClaimIDs, claim.ID) {
			candidate.ClaimIDs = append(candidate.ClaimIDs, claim.ID)
		}
		if claim.State == ClaimActive {
			active = true
		} else {
			stale = true
		}
	}
	sort.Strings(candidate.ClaimIDs)
	switch {
	case active:
		candidate.ClaimState = string(ClaimActive)
		candidate.Eligible = false
		candidate.Reason = "protected by an active resource claim"
	case stale:
		candidate.ClaimState = string(ClaimStale)
	default:
		if candidate.ClaimState == "" {
			candidate.ClaimState = "unclaimed"
		}
	}
}

func containsString(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}

func firstEligible(candidates []Candidate) (Candidate, bool) {
	for _, candidate := range candidates {
		if candidate.Eligible {
			return candidate, true
		}
	}
	return Candidate{}, false
}

func withinCleanupCooldown(actions []Action, now time.Time, cooldown time.Duration) bool {
	for index := len(actions) - 1; index >= 0; index-- {
		action := actions[index]
		if now.Sub(action.Timestamp) < -5*time.Second {
			return true
		}
		if now.Sub(action.Timestamp) <= cooldown {
			// A durable intent consumes cooldown even if the resident crashed
			// before it could append the matching result.
			return true
		}
		break
	}
	return false
}

func cleanupResultActed(result string) bool {
	switch result {
	case "browser_closed", "dev_session_stopped", "docker_workspace_stopped", "tree_exit_confirmed", "signal_sent_unconfirmed":
		return true
	default:
		return false
	}
}

func (manager *Manager) applyCandidate(ctx context.Context, candidate Candidate, policy Policy) (string, string) {
	// Claims are reloaded at the final boundary. A heartbeat that lands after
	// planning wins; the state lock stays held through the provider mutation,
	// so heartbeat/acquire/release and cleanup have one linearized boundary.
	// Corrupt state fails closed.
	store := NewClaimStore(manager.Dir)
	unlock, err := store.lock()
	if err != nil {
		return "revalidation_rejected", err.Error()
	}
	defer unlock()
	claims, err := store.listUnlocked()
	if err != nil {
		return "revalidation_rejected", err.Error()
	}
	revalidated := candidate
	decorateClaims(&revalidated, claims)
	if !revalidated.Eligible {
		return "revalidation_rejected", "resource acquired an active claim"
	}
	if candidate.ResourceKind == ResourceProcess && !matchingStaleProcessClaim(candidate, claims) {
		return "revalidation_rejected", "process claim was renewed, released, or replaced"
	}

	switch candidate.ResourceKind {
	case ResourceBrowser:
		return manager.applyBrowser(candidate)
	case ResourceDevSession:
		return manager.applyDev(candidate, policy)
	case ResourceDockerWorkspace:
		return manager.applyDocker(ctx, candidate, policy)
	case ResourceProcess:
		return manager.applyProcess(ctx, candidate, policy)
	default:
		return "revalidation_rejected", "resource kind has no cleanup adapter"
	}
}

func matchingStaleProcessClaim(candidate Candidate, claims []ClaimView) bool {
	value, ok := candidate.private.(processCandidate)
	if !ok {
		return false
	}
	for _, current := range claims {
		if current.ID != value.Claim.ID {
			continue
		}
		return current.State == ClaimStale && current.CleanupOnStale &&
			current.ProcessIdentity == value.Claim.ProcessIdentity && current.RootPID == value.Claim.RootPID
	}
	return false
}

func (manager *Manager) now() time.Time {
	if manager.Now == nil {
		return time.Now()
	}
	return manager.Now()
}

func maxTime(values ...time.Time) time.Time {
	var result time.Time
	for _, value := range values {
		if value.After(result) {
			result = value
		}
	}
	return result
}

func boundedError(err error) string {
	if err == nil {
		return ""
	}
	value := err.Error()
	if len(value) > 512 {
		value = value[:512]
	}
	return value
}

var _ sessionpressure.ResourceCleaner = (*Manager)(nil)
