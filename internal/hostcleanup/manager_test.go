package hostcleanup

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nstranquist/session-pressure/third_party/pageskein/browser"
	"github.com/nstranquist/session-pressure/internal/devsession"
	"github.com/nstranquist/session-pressure/internal/orb"
	"github.com/nstranquist/session-pressure/internal/sessionpressure"
)

func TestClaimStoreActiveHeartbeatStaleAndRelease(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC().Truncate(time.Second)
	store := NewClaimStore(dir)
	store.Now = func() time.Time { return now }
	claim, err := store.Acquire(ResourceDevSession, "ad-hoc/api", "test-owner", time.Hour, false, 0, "test")
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	views, err := store.List()
	if err != nil {
		t.Fatalf("List active: %v", err)
	}
	if len(views) != 1 || views[0].State != ClaimActive {
		t.Fatalf("active views = %#v", views)
	}

	now = now.Add(2 * time.Hour)
	views, err = store.List()
	if err != nil {
		t.Fatalf("List stale: %v", err)
	}
	if len(views) != 1 || views[0].State != ClaimStale {
		t.Fatalf("stale views = %#v", views)
	}
	renewed, err := store.Heartbeat(claim.ID)
	if err != nil {
		t.Fatalf("Heartbeat: %v", err)
	}
	if !renewed.ExpiresAt.Equal(now.Add(time.Hour)) {
		t.Fatalf("renewed expiry = %s", renewed.ExpiresAt)
	}
	if err := store.Release(claim.ID); err != nil {
		t.Fatalf("Release: %v", err)
	}
	views, err = store.List()
	if err != nil || len(views) != 0 {
		t.Fatalf("List released = %#v, %v", views, err)
	}
}

func TestClaimStoreCorruptionFailsClosed(t *testing.T) {
	dir := t.TempDir()
	claimsDir := ClaimsDir(dir)
	if err := os.MkdirAll(claimsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(claimsDir, "broken.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewClaimStore(dir).List(); err == nil {
		t.Fatal("List accepted corrupt protection state")
	}
}

func TestLoadPolicyDoesNotInferObservationEvidenceFromFileMTime(t *testing.T) {
	dir := t.TempDir()
	policy := DefaultPolicy()
	body, err := json.Marshal(policy)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := PolicyPath(dir)
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * MinimumObservationWindow)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	loaded, persisted, err := LoadPolicy(dir)
	if err != nil || !persisted {
		t.Fatalf("loaded=%+v persisted=%t err=%v", loaded, persisted, err)
	}
	if !loaded.ObservationStartedAt.IsZero() || loaded.ObservationRemaining(time.Now()) != MinimumObservationWindow {
		t.Fatalf("file mtime became observation evidence: %+v", loaded)
	}
}

func TestManagerAutoGraduatesScheduledProcessOnlyPolicyAtDueBoundary(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC().Truncate(time.Second)
	policy := DefaultPolicy()
	policy.AutoGraduateProcessOnly = true
	policy.ObservationStartedAt = now.Add(-MinimumObservationWindow)
	if err := SavePolicy(dir, policy); err != nil {
		t.Fatal(err)
	}
	manager := testManager(dir, now)
	if result, err := manager.MaybeRelieve(context.Background(), sessionpressure.Snapshot{}); err != nil || result.Attempted {
		t.Fatalf("unproven graduation result=%+v err=%v", result, err)
	}
	stillScheduled, _, err := LoadPolicy(dir)
	if err != nil || stillScheduled.Enforce || !stillScheduled.AutoGraduateProcessOnly {
		t.Fatalf("unproven resident graduated policy=%+v err=%v", stillScheduled, err)
	}
	result, err := manager.MaybeRelieve(context.Background(), eligibleSnapshot(policy))
	if err != nil || result.Attempted || result.Acted {
		t.Fatalf("graduation result=%+v err=%v", result, err)
	}
	graduated, persisted, err := LoadPolicy(dir)
	if err != nil || !persisted || !graduated.Enforce || !graduated.ProcessOnly() || graduated.AutoGraduateProcessOnly {
		t.Fatalf("graduated policy=%+v persisted=%t err=%v", graduated, persisted, err)
	}
}

func TestReconcileAutoGraduationPreservesScheduledObservationBeforeDue(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC().Truncate(time.Second)
	policy := DefaultPolicy()
	policy.AutoGraduateProcessOnly = true
	policy.ObservationStartedAt = now.Add(-MinimumObservationWindow + time.Minute)
	if err := SavePolicy(dir, policy); err != nil {
		t.Fatal(err)
	}
	got, persisted, graduated, err := ReconcileAutoGraduation(dir, now)
	if err != nil || !persisted || graduated || got.Enforce || !got.AutoGraduateProcessOnly || !got.ObservationStartedAt.Equal(policy.ObservationStartedAt) {
		t.Fatalf("policy=%+v persisted=%t graduated=%t err=%v", got, persisted, graduated, err)
	}
}

func TestReconcileAutoGraduationAdvancesOneNativeStagePerBoundary(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC().Truncate(time.Second)
	policy := DefaultPolicy()
	policy.AutoGraduateProcessOnly = true
	policy.AutoGraduateNative = true
	policy.ObservationStartedAt = now.Add(-DockerMinimumObservationWindow - time.Hour)
	if err := SavePolicy(dir, policy); err != nil {
		t.Fatal(err)
	}
	want := []struct {
		process, browser, dev, docker, nativeScheduled bool
	}{
		{true, false, false, false, true},
		{true, true, false, false, true},
		{true, true, true, false, true},
		{true, true, true, true, false},
	}
	for index, stage := range want {
		boundary := now.Add(time.Duration(index) * time.Second)
		got, persisted, graduated, err := ReconcileAutoGraduation(dir, boundary)
		if err != nil || !persisted || !graduated || !got.Enforce ||
			got.ProcessEnabled != stage.process || got.BrowserEnabled != stage.browser ||
			got.DevSessionEnabled != stage.dev || got.DockerWorkspaceEnabled != stage.docker ||
			got.AutoGraduateNative != stage.nativeScheduled {
			t.Fatalf("stage %d policy=%+v persisted=%t graduated=%t err=%v", index, got, persisted, graduated, err)
		}
		if err := got.ValidateEnforcement(boundary); err != nil {
			t.Fatalf("stage %d enforcement invalid: %v", index, err)
		}
	}
}

func TestClaimHeartbeatSerializesWithFinalCleanupBoundary(t *testing.T) {
	dir := t.TempDir()
	store := NewClaimStore(dir)
	claim, err := store.Acquire(ResourceOther, "cache-worker", "test-owner", time.Hour, false, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	unlock, err := store.lock()
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, heartbeatErr := store.Heartbeat(claim.ID)
		done <- heartbeatErr
	}()
	select {
	case heartbeatErr := <-done:
		unlock()
		t.Fatalf("heartbeat crossed locked cleanup boundary: %v", heartbeatErr)
	case <-time.After(100 * time.Millisecond):
	}
	unlock()
	select {
	case heartbeatErr := <-done:
		if heartbeatErr != nil {
			t.Fatalf("heartbeat after cleanup boundary: %v", heartbeatErr)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("heartbeat did not resume after cleanup boundary")
	}
}

func TestManagerRejectsUnsafeEnforcementAtActionBoundary(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	tests := []struct {
		name      string
		configure func(*Policy)
		want      string
	}{
		{
			name: "observation window incomplete",
			configure: func(policy *Policy) {
				policy.ObservationStartedAt = now
			},
			want: "requires seven days of observation",
		},
		{
			name: "native provider scope not graduated",
			configure: func(policy *Policy) {
				policy.DevSessionEnabled = true
			},
			want: "twenty-one days of observation",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			policy := processOnlyEnforcementPolicy(now)
			test.configure(&policy)
			if err := SavePolicy(dir, policy); err != nil {
				t.Fatal(err)
			}
			manager := testManager(dir, now)
			manager.reapProcess = func(context.Context, sessionpressure.ClaimedProcessExpectation) (sessionpressure.ClaimedProcessResult, error) {
				t.Fatal("unsafe policy crossed the destructive boundary")
				return sessionpressure.ClaimedProcessResult{}, nil
			}
			result, err := manager.MaybeRelieve(context.Background(), eligibleSnapshot(policy))
			if err == nil || !strings.Contains(err.Error(), test.want) || result.Attempted || result.Acted {
				t.Fatalf("result=%+v err=%v want=%q", result, err, test.want)
			}
			actions, actionsErr := ReadActions(dir, 10)
			if actionsErr != nil || len(actions) != 0 {
				t.Fatalf("unsafe policy wrote actions=%+v err=%v", actions, actionsErr)
			}
		})
	}
}

func TestManagerReapsOneOptedInStaleProcessWithDurableIntent(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC().Truncate(time.Second)
	policy := processOnlyEnforcementPolicy(now)
	if err := SavePolicy(dir, policy); err != nil {
		t.Fatal(err)
	}
	claim := acquireStaleProcessClaim(t, dir, now)

	manager := testManager(dir, now)
	manager.inspectProcess = func(_ context.Context, expectation sessionpressure.ClaimedProcessExpectation) (sessionpressure.ClaimedProcessResult, error) {
		if expectation.RootPID != claim.RootPID || expectation.ProcessIdentity != claim.ProcessIdentity {
			t.Fatalf("inspection expectation=%+v claim=%+v", expectation, claim)
		}
		return sessionpressure.ClaimedProcessResult{RootPID: claim.RootPID, RSSSumMB: 128}, nil
	}
	reaps := 0
	manager.reapProcess = func(_ context.Context, expectation sessionpressure.ClaimedProcessExpectation) (sessionpressure.ClaimedProcessResult, error) {
		reaps++
		if expectation.RootPID != claim.RootPID || expectation.ProcessIdentity != claim.ProcessIdentity {
			t.Fatalf("reap expectation=%+v claim=%+v", expectation, claim)
		}
		return sessionpressure.ClaimedProcessResult{RootPID: claim.RootPID, Result: "tree_exit_confirmed"}, nil
	}
	result, err := manager.MaybeRelieve(context.Background(), eligibleSnapshot(policy))
	if err != nil {
		t.Fatalf("MaybeRelieve: %v", err)
	}
	if !result.Attempted || !result.Acted || result.Result != "tree_exit_confirmed" || reaps != 1 {
		t.Fatalf("result=%#v reaps=%d", result, reaps)
	}
	actions, err := ReadActions(dir, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(actions) != 2 || actions[0].Result != "intent_recorded" || actions[1].Result != "tree_exit_confirmed" || actions[0].ID != actions[1].ID {
		t.Fatalf("actions = %#v", actions)
	}
}

func TestManagerCooldownPreventsSecondProcessAction(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC().Truncate(time.Second)
	policy := processOnlyEnforcementPolicy(now)
	if err := SavePolicy(dir, policy); err != nil {
		t.Fatal(err)
	}
	acquireStaleProcessClaim(t, dir, now)
	manager := testManager(dir, now)
	manager.inspectProcess = func(context.Context, sessionpressure.ClaimedProcessExpectation) (sessionpressure.ClaimedProcessResult, error) {
		return sessionpressure.ClaimedProcessResult{RSSSumMB: 64}, nil
	}
	reaps := 0
	manager.reapProcess = func(context.Context, sessionpressure.ClaimedProcessExpectation) (sessionpressure.ClaimedProcessResult, error) {
		reaps++
		return sessionpressure.ClaimedProcessResult{Result: "tree_exit_confirmed"}, nil
	}
	first, err := manager.MaybeRelieve(context.Background(), eligibleSnapshot(policy))
	if err != nil || !first.Acted {
		t.Fatalf("first result=%+v err=%v", first, err)
	}
	second, err := manager.MaybeRelieve(context.Background(), eligibleSnapshot(policy))
	if err != nil || second.Attempted || reaps != 1 {
		t.Fatalf("second result=%+v reaps=%d err=%v", second, reaps, err)
	}
	actions, err := ReadActions(dir, 10)
	if err != nil || len(actions) != 2 {
		t.Fatalf("actions=%+v err=%v", actions, err)
	}
}

func TestManagerActiveClaimProtectsStaleDevSession(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC().Truncate(time.Second)
	old := now.Add(-6 * time.Hour)
	logPath := filepath.Join(t.TempDir(), "api.log")
	if err := os.WriteFile(logPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(logPath, old, old); err != nil {
		t.Fatal(err)
	}
	entry := devsession.ScopeEntry{Scope: "ad-hoc", Alive: true, AttachmentKnown: true, Provenance: devsession.Provenance{
		Version: 1, Workspace: "ad-hoc", App: "api", TmuxSession: "ndev-api",
		StartedAt: old.Format(time.RFC3339), LogPath: logPath,
	}}
	policy := DefaultPolicy()
	store := NewClaimStore(dir)
	if _, err := store.Acquire(ResourceDevSession, "ad-hoc/api", "test-owner", time.Hour, false, 0, ""); err != nil {
		t.Fatal(err)
	}

	manager := testManager(dir, now)
	manager.listDev = func() ([]devsession.ScopeEntry, error) { return []devsession.ScopeEntry{entry}, nil }
	report, err := manager.Plan(context.Background(), eligibleSnapshot(policy))
	if err != nil {
		t.Fatal(err)
	}
	for _, candidate := range report.Candidates {
		if candidate.ResourceKind == ResourceDevSession && candidate.ResourceID == "ad-hoc/api" {
			if candidate.Eligible || candidate.ClaimState != string(ClaimActive) {
				t.Fatalf("active claim did not protect candidate: %+v", candidate)
			}
			return
		}
	}
	t.Fatalf("protected dev candidate missing: %+v", report.Candidates)
}

func TestBrowserCandidatesHonorNativeLeaseAndKeepAlive(t *testing.T) {
	now := time.Now().UTC()
	manager := testManager(t.TempDir(), now)
	manager.listBrowser = func() ([]browser.Session, error) {
		return []browser.Session{
			{Name: "idle", PID: 10, StartedAt: now.Add(-2 * time.Hour), LastActivityAt: now.Add(-2 * time.Hour), IdleTimeout: time.Hour.String(), LifecyclePolicy: browser.LifecycleIdle},
			{Name: "keep", PID: 11, StartedAt: now.Add(-24 * time.Hour), LifecyclePolicy: browser.LifecycleKeepAlive},
		}, nil
	}
	policy := DefaultPolicy()
	policy.BrowserGraceSeconds = 0
	candidates, err := manager.browserCandidates(policy)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 2 || !candidates[0].Eligible || candidates[1].Eligible || candidates[1].Reason != "explicit keep-alive browser claim" {
		t.Fatalf("candidates = %#v", candidates)
	}
}

func TestApplyBrowserPassesExactNativeGeneration(t *testing.T) {
	now := time.Now().UTC()
	manager := testManager(t.TempDir(), now)
	manager.expireBrowser = func(name string, pid int, timeout time.Duration, purge bool, gotNow time.Time) (browser.IdleExpiryResult, error) {
		if name != "idle" || pid != 4242 || timeout != time.Hour || !purge || !gotNow.Equal(now) {
			t.Fatalf("expire args name=%q pid=%d timeout=%s purge=%t now=%s", name, pid, timeout, purge, gotNow)
		}
		return browser.IdleExpiryResult{Done: true, Closed: true, Reason: browser.IdleExpiryLeaseExpired}, nil
	}
	candidate := Candidate{ResourceKind: ResourceBrowser, ResourceID: "idle", private: browserCandidate{
		Session: browser.Session{Name: "idle", PID: 4242, PurgeOnExpiry: true}, Timeout: time.Hour,
	}}
	result, detail := manager.applyBrowser(candidate)
	if result != "browser_closed" || detail != "" {
		t.Fatalf("result=%q detail=%q", result, detail)
	}
}

func TestDevCandidatesProtectAttachedTmuxSession(t *testing.T) {
	now := time.Now().UTC()
	manager := testManager(t.TempDir(), now)
	manager.listDev = func() ([]devsession.ScopeEntry, error) {
		return []devsession.ScopeEntry{{Scope: "ad-hoc", Alive: true, Attached: true, AttachmentKnown: true, Provenance: devsession.Provenance{
			Workspace: "ad-hoc", App: "api", TmuxSession: "ndev-api", StartedAt: now.Add(-24 * time.Hour).Format(time.RFC3339),
		}}}, nil
	}
	candidates, err := manager.devCandidates(DefaultPolicy())
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 || candidates[0].Eligible || candidates[0].Reason != "dev session is attached to an active tmux client" {
		t.Fatalf("candidates=%#v", candidates)
	}
}

func TestDevCandidatesProtectUnknownAttachmentState(t *testing.T) {
	now := time.Now().UTC()
	manager := testManager(t.TempDir(), now)
	manager.listDev = func() ([]devsession.ScopeEntry, error) {
		return []devsession.ScopeEntry{{Scope: "ad-hoc", Alive: true, AttachmentKnown: false, Provenance: devsession.Provenance{
			Workspace: "ad-hoc", App: "api", TmuxSession: "ndev-api", StartedAt: now.Add(-24 * time.Hour).Format(time.RFC3339),
		}}}, nil
	}
	candidates, err := manager.devCandidates(DefaultPolicy())
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 || candidates[0].Eligible || candidates[0].Reason != "dev session attachment state could not be verified" {
		t.Fatalf("candidates=%#v", candidates)
	}
}

func TestApplyDevRejectsChangedGeneration(t *testing.T) {
	now := time.Now().UTC()
	original := devsession.ScopeEntry{Scope: "ad-hoc", Alive: true, AttachmentKnown: true, Provenance: devsession.Provenance{
		Workspace: "ad-hoc", App: "api", TmuxSession: "ndev-api-old", StartedAt: now.Add(-6 * time.Hour).Format(time.RFC3339),
	}}
	manager := testManager(t.TempDir(), now)
	changed := original
	changed.Provenance.TmuxSession = "ndev-api-new"
	manager.listDev = func() ([]devsession.ScopeEntry, error) { return []devsession.ScopeEntry{changed}, nil }
	manager.teardownDev = func(string, string, devsession.IdleTeardownExpectation) (bool, string, error) {
		t.Fatal("changed dev generation crossed teardown boundary")
		return false, "", nil
	}
	result, detail := manager.applyDev(Candidate{ResourceKind: ResourceDevSession, private: devCandidate{Entry: original, LastActivity: now.Add(-6 * time.Hour)}}, DefaultPolicy())
	if result != "revalidation_rejected" || detail != "dev session generation changed" {
		t.Fatalf("result=%q detail=%q", result, detail)
	}
}

func TestApplyDevRejectsUnknownAttachmentAtFinalBoundary(t *testing.T) {
	now := time.Now().UTC()
	original := devsession.ScopeEntry{Scope: "ad-hoc", Alive: true, AttachmentKnown: true, Provenance: devsession.Provenance{
		Workspace: "ad-hoc", App: "api", TmuxSession: "ndev-api", StartedAt: now.Add(-6 * time.Hour).Format(time.RFC3339),
	}}
	current := original
	current.AttachmentKnown = false
	manager := testManager(t.TempDir(), now)
	manager.listDev = func() ([]devsession.ScopeEntry, error) { return []devsession.ScopeEntry{current}, nil }
	manager.teardownDev = func(string, string, devsession.IdleTeardownExpectation) (bool, string, error) {
		t.Fatal("unknown attachment state crossed teardown boundary")
		return false, "", nil
	}
	result, detail := manager.applyDev(Candidate{ResourceKind: ResourceDevSession, private: devCandidate{Entry: original, LastActivity: now.Add(-6 * time.Hour)}}, DefaultPolicy())
	if result != "revalidation_rejected" || detail != "dev session attachment state could not be verified" {
		t.Fatalf("result=%q detail=%q", result, detail)
	}
}

func TestApplyDevDelegatesFinalGenerationAndIdleCheckAtomically(t *testing.T) {
	now := time.Now().UTC()
	original := devsession.ScopeEntry{Scope: "ad-hoc", Alive: true, AttachmentKnown: true, Provenance: devsession.Provenance{
		Workspace: "ad-hoc", App: "api", TmuxSession: "ndev-api", StartedAt: now.Add(-6 * time.Hour).Format(time.RFC3339),
	}}
	manager := testManager(t.TempDir(), now)
	manager.listDev = func() ([]devsession.ScopeEntry, error) { return []devsession.ScopeEntry{original}, nil }
	manager.teardownDev = func(workspace, app string, expected devsession.IdleTeardownExpectation) (bool, string, error) {
		if workspace != "ad-hoc" || app != "api" || expected.StartedAt != original.Provenance.StartedAt || expected.TmuxSession != original.Provenance.TmuxSession || expected.MinimumIdle != 4*time.Hour || !expected.Now.Equal(now) {
			t.Fatalf("workspace=%q app=%q expected=%+v", workspace, app, expected)
		}
		return true, "", nil
	}
	result, detail := manager.applyDev(Candidate{ResourceKind: ResourceDevSession, private: devCandidate{Entry: original, LastActivity: now.Add(-6 * time.Hour)}}, DefaultPolicy())
	if result != "dev_session_stopped" || detail != "" {
		t.Fatalf("result=%q detail=%q", result, detail)
	}
}

func TestStaleProcessInspectionFailureDoesNotStarveDevCandidate(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC().Truncate(time.Second)
	old := now.Add(-6 * time.Hour)
	store := NewClaimStore(dir)
	store.Now = func() time.Time { return now.Add(-2 * time.Hour) }
	if _, err := store.Acquire(ResourceProcess, "worker", "test-owner", time.Minute, true, os.Getpid(), ""); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(t.TempDir(), "api.log")
	if err := os.WriteFile(logPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(logPath, old, old); err != nil {
		t.Fatal(err)
	}
	entry := devsession.ScopeEntry{Scope: "ad-hoc", Alive: true, AttachmentKnown: true, Provenance: devsession.Provenance{
		Workspace: "ad-hoc", App: "api", TmuxSession: "ndev-api", StartedAt: old.Format(time.RFC3339), LogPath: logPath,
	}}
	policy := DefaultPolicy()
	manager := testManager(dir, now)
	manager.inspectProcess = func(context.Context, sessionpressure.ClaimedProcessExpectation) (sessionpressure.ClaimedProcessResult, error) {
		return sessionpressure.ClaimedProcessResult{}, errors.New("process disappeared")
	}
	manager.listDev = func() ([]devsession.ScopeEntry, error) { return []devsession.ScopeEntry{entry}, nil }
	report, err := manager.Plan(context.Background(), eligibleSnapshot(policy))
	if err != nil {
		t.Fatal(err)
	}
	candidate, found := firstEligible(report.Candidates)
	if !found || candidate.ResourceKind != ResourceDevSession {
		t.Fatalf("stale process inspection starved dev candidate: candidate=%+v found=%t report=%+v", candidate, found, report)
	}
}

func TestProcessCandidatesBoundWholeProcessInventoryCaptures(t *testing.T) {
	manager := testManager(t.TempDir(), time.Now().UTC())
	inspections := 0
	manager.inspectProcess = func(context.Context, sessionpressure.ClaimedProcessExpectation) (sessionpressure.ClaimedProcessResult, error) {
		inspections++
		return sessionpressure.ClaimedProcessResult{RSSSumMB: 64}, nil
	}
	claims := make([]ClaimView, maxProcessClaimsInspectedPerPlan+2)
	for index := range claims {
		claims[index] = ClaimView{
			Claim: Claim{
				ID: "claim-" + strconv.Itoa(index), ResourceKind: ResourceProcess,
				ResourceID: "worker-" + strconv.Itoa(index), CleanupOnStale: true,
				RootPID: 1000 + index, ProcessIdentity: "identity-" + strconv.Itoa(index),
			},
			State: ClaimStale,
		}
	}

	candidates := manager.processCandidates(context.Background(), claims, DefaultPolicy())
	if len(candidates) != len(claims) || inspections != maxProcessClaimsInspectedPerPlan {
		t.Fatalf("candidates=%d inspections=%d", len(candidates), inspections)
	}
	eligible := 0
	deferred := 0
	for _, candidate := range candidates {
		if candidate.Eligible && candidate.EstimatedRAMMB == 64 {
			eligible++
		}
		if !candidate.Eligible && candidate.Reason == "stale process claim inspection deferred by per-pass safety limit" {
			deferred++
		}
	}
	if eligible != maxProcessClaimsInspectedPerPlan || deferred != len(claims)-maxProcessClaimsInspectedPerPlan {
		t.Fatalf("eligible=%d deferred=%d candidates=%+v", eligible, deferred, candidates)
	}
}

func TestProcessCandidateInspectionWindowRotates(t *testing.T) {
	now := time.Unix(0, 0).UTC()
	manager := testManager(t.TempDir(), now)
	seen := map[int]bool{}
	manager.inspectProcess = func(_ context.Context, expectation sessionpressure.ClaimedProcessExpectation) (sessionpressure.ClaimedProcessResult, error) {
		seen[expectation.RootPID] = true
		return sessionpressure.ClaimedProcessResult{}, nil
	}
	claims := make([]ClaimView, maxProcessClaimsInspectedPerPlan+2)
	for index := range claims {
		claims[index] = ClaimView{
			Claim: Claim{
				ID: "claim-" + strconv.Itoa(index), ResourceKind: ResourceProcess,
				ResourceID: "worker-" + strconv.Itoa(index), CleanupOnStale: true,
				RootPID: 1000 + index, ProcessIdentity: "identity-" + strconv.Itoa(index),
			},
			State: ClaimStale,
		}
	}

	_ = manager.processCandidates(context.Background(), claims, DefaultPolicy())
	if seen[1008] {
		t.Fatalf("first bounded window unexpectedly inspected pid 1008: %+v", seen)
	}
	seen = map[int]bool{}
	manager.Now = func() time.Time { return now.Add(processClaimInspectionRotationInterval) }
	_ = manager.processCandidates(context.Background(), claims, DefaultPolicy())
	if !seen[1008] {
		t.Fatalf("rotated bounded window did not inspect pid 1008: %+v", seen)
	}
}

func testManager(dir string, now time.Time) *Manager {
	return &Manager{
		Dir: dir, Now: func() time.Time { return now },
		listBrowser:   func() ([]browser.Session, error) { return nil, nil },
		expireBrowser: browser.ExpireIdleSession,
		listDev:       func() ([]devsession.ScopeEntry, error) { return nil, nil },
		teardownDev: func(string, string, devsession.IdleTeardownExpectation) (bool, string, error) {
			return true, "", nil
		},
		collectOrb:     func(context.Context) (orb.Snapshot, error) { return orb.Snapshot{}, nil },
		loadOrbPolicy:  func() (orb.Policy, string, error) { return orb.DefaultPolicy(), "default", nil },
		planOrb:        func(orb.Snapshot, orb.Policy, int) []orb.TrimAction { return nil },
		applyOrb:       func(actions []orb.TrimAction) []orb.TrimAction { return actions },
		inspectProcess: sessionpressure.InspectClaimedProcessTree,
		reapProcess:    sessionpressure.ReapClaimedProcessTree,
		memoryLevel:    func(sessionpressure.Snapshot) (sessionpressure.Level, error) { return sessionpressure.LevelRed, nil },
	}
}

func TestCleanupTriggerIgnoresCPUOnlyRed(t *testing.T) {
	policy := DefaultPolicy()
	triggered, reason := cleanupTriggered(sessionpressure.LevelNormal, policy.SustainSamples, policy)
	if triggered || reason != "memory level normal is below red" {
		t.Fatalf("triggered=%v reason=%q", triggered, reason)
	}
}

func TestDecorateClaimsPreservesNativeProviderClaim(t *testing.T) {
	candidate := Candidate{ResourceKind: ResourceBrowser, ResourceID: "keep", ClaimState: "native"}
	decorateClaims(&candidate, nil)
	if candidate.ClaimState != "native" {
		t.Fatalf("claim_state=%q", candidate.ClaimState)
	}
}

func TestApplyDockerReplansAllCandidatesBeforeExactMatch(t *testing.T) {
	now := time.Now().UTC()
	manager := testManager(t.TempDir(), now)
	manager.collectOrb = func(context.Context) (orb.Snapshot, error) { return orb.Snapshot{}, nil }
	gotLimit := 0
	manager.planOrb = func(_ orb.Snapshot, _ orb.Policy, intLimit int) []orb.TrimAction {
		gotLimit = intLimit
		return []orb.TrimAction{{WorkspaceID: "nicos-api", Workspace: "feat/api", Action: "would_stop"}}
	}
	manager.applyOrb = func(actions []orb.TrimAction) []orb.TrimAction {
		actions[0].Action = "stopped"
		return actions
	}
	candidate := Candidate{
		ResourceKind: ResourceDockerWorkspace, ResourceID: "feat/api", Eligible: true,
		private: dockerCandidate{Action: orb.TrimAction{WorkspaceID: "nicos-api", Workspace: "feat/api", Action: "would_stop"}},
	}
	result, detail := manager.applyDocker(context.Background(), candidate, DefaultPolicy())
	if result != "docker_workspace_stopped" || detail != "" || gotLimit != -1 {
		t.Fatalf("result=%q detail=%q planner_limit=%d", result, detail, gotLimit)
	}
}

func TestApplyCandidateRejectsReleasedStaleProcessClaim(t *testing.T) {
	manager := testManager(t.TempDir(), time.Now().UTC())
	manager.reapProcess = func(context.Context, sessionpressure.ClaimedProcessExpectation) (sessionpressure.ClaimedProcessResult, error) {
		t.Fatal("released process claim crossed the signal boundary")
		return sessionpressure.ClaimedProcessResult{}, nil
	}
	claim := Claim{ID: "claim-released", ResourceKind: ResourceProcess, RootPID: 123, ProcessIdentity: "identity", CleanupOnStale: true}
	candidate := Candidate{
		ResourceKind: ResourceProcess, ResourceID: "worker", Eligible: true,
		private: processCandidate{Claim: claim},
	}
	result, detail := manager.applyCandidate(context.Background(), candidate, DefaultPolicy())
	if result != "revalidation_rejected" || detail != "process claim was renewed, released, or replaced" {
		t.Fatalf("result=%q detail=%q", result, detail)
	}
}

func eligibleSnapshot(policy Policy) sessionpressure.Snapshot {
	return sessionpressure.Snapshot{
		Level: sessionpressure.LevelRed, ConsecutiveSamples: policy.SustainSamples,
		MemoryConsecutiveSamples: policy.SustainSamples,
		GuardRole:                "resident", GuardBudgetOK: true, GuardBaselineProven: true,
		ProcessInventoryFresh: true,
	}
}

func processOnlyEnforcementPolicy(now time.Time) Policy {
	policy := DefaultPolicy()
	policy.Enabled = true
	policy.Enforce = true
	policy.BrowserEnabled = false
	policy.DevSessionEnabled = false
	policy.DockerWorkspaceEnabled = false
	policy.ProcessEnabled = true
	policy.ObservationStartedAt = now.Add(-MinimumObservationWindow - time.Hour)
	return policy
}

func acquireStaleProcessClaim(t *testing.T, dir string, now time.Time) Claim {
	t.Helper()
	store := NewClaimStore(dir)
	store.Now = func() time.Time { return now.Add(-2 * time.Hour) }
	claim, err := store.Acquire(ResourceProcess, "test-worker", "test-owner", time.Minute, true, os.Getpid(), "")
	if err != nil {
		t.Fatal(err)
	}
	return claim
}
