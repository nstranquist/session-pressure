package sessionpressurecmd

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/nstranquist/session-pressure/internal/hostcleanup"
	"github.com/nstranquist/session-pressure/sessionpressure"
)

const sessionPressureCleanupHelp = `Usage: session-pressure [--json] cleanup <subcommand>

Inspect and configure conservative RAM reclaim for stale local resources.
Automatic action requires persisted enforce policy plus sustained memory-only red pressure.

Subcommands:
  status                         Show policy, graduation gate, claims, and recent actions
  plan                           Live dry-run across browser, dev-session, Docker-workspace, and process providers
  history [--limit N]            Read the bounded cleanup action ledger
  policy show                    Show the effective cleanup policy
  policy init [--force]          Write the observe-only default policy
  policy schedule                Auto-graduate process/browser/dev/Docker one stage per 7d soak
  policy enable                  After 7d observation, enable process-only cleanup immediately
  policy observe                 Keep plans/claims but disable automatic actions
  policy disable                 Disable cleanup evaluation
  claim list                     List active and stale resource claims
  claim acquire [flags]          Protect a resource with a renewable TTL claim
  claim heartbeat --claim-id ID  Renew one exact claim using its original TTL
  claim release --claim-id ID    Release one exact claim

Acquire flags:
  --kind browser|dev_session|docker_workspace|process|other
  --resource ID --owner OWNER --ttl DURATION [--note TEXT]
  --pid PID [--cleanup-on-stale]  process claims only; captures kernel start identity

Examples:
  session-pressure --json cleanup plan
  session-pressure cleanup policy enable
  session-pressure cleanup claim acquire --kind dev_session --resource ad-hoc/api --owner terminal-1 --ttl 2h
  session-pressure cleanup claim heartbeat --claim-id claim-...
`

func cmdSessionPressureCleanup(g *Flags, args []string) int {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		fmt.Print(sessionPressureCleanupHelp)
		return 0
	}
	dir, err := sessionpressure.DataDir()
	if err != nil {
		return sessionPressureError(err.Error(), 1)
	}
	switch args[0] {
	case "status":
		return cmdSessionPressureCleanupStatus(g, dir, args[1:])
	case "plan":
		return cmdSessionPressureCleanupPlan(g, dir, args[1:])
	case "history":
		return cmdSessionPressureCleanupHistory(g, dir, args[1:])
	case "policy":
		return cmdSessionPressureCleanupPolicy(g, dir, args[1:])
	case "claim":
		return cmdSessionPressureCleanupClaim(g, dir, args[1:])
	case "enforce":
		return cmdSessionPressureCleanupEnforce(g, dir, args[1:])
	default:
		return sessionPressureError("unknown cleanup subcommand "+strconv.Quote(args[0]), 2)
	}
}

// cmdSessionPressureCleanupEnforce is the narrow resident-to-control-plane
// bridge. It is intentionally omitted from help: operators use plan/policy;
// only the tiny resident sends a bounded, already-sampled snapshot on stdin.
func cmdSessionPressureCleanupEnforce(g *Flags, dir string, args []string) int {
	return cmdSessionPressureCleanupEnforceFromReader(g, dir, args, os.Stdin)
}

const cleanupBridgeSnapshotMaxAge = 30 * time.Second

func cmdSessionPressureCleanupEnforceFromReader(g *Flags, dir string, args []string, input io.Reader) int {
	if len(args) != 0 {
		return sessionPressureError("cleanup enforce accepts no arguments", 2)
	}
	body, err := io.ReadAll(io.LimitReader(input, 256<<10))
	if err != nil {
		return sessionPressureError(err.Error(), 1)
	}
	var snapshot sessionpressure.Snapshot
	if err := json.Unmarshal(body, &snapshot); err != nil {
		return sessionPressureError("decode cleanup snapshot: "+err.Error(), 2)
	}
	if snapshot.GuardRole != "resident" || !snapshot.GuardBudgetApplicable {
		return sessionPressureError("cleanup enforce requires resident budget evidence", 2)
	}
	parentPID := os.Getppid()
	age := time.Since(snapshot.Timestamp)
	if parentPID <= 1 || snapshot.GuardPID != parentPID || snapshot.Timestamp.IsZero() || age < -5*time.Second || age > cleanupBridgeSnapshotMaxAge {
		return sessionPressureError("cleanup enforce requires a fresh snapshot from its direct resident parent", 2)
	}
	controller, err := NewLaunchdController(dir)
	if err != nil {
		return sessionPressureError("verify cleanup bridge resident: "+err.Error(), 1)
	}
	statusCtx, statusCancel := context.WithTimeout(context.Background(), 5*time.Second)
	status := controller.Status(statusCtx)
	statusCancel()
	if !status.OK || status.PID != parentPID || !status.ArtifactVerified || status.ArtifactSHA256 == "" || status.ArtifactSHA256 != snapshot.GuardBinarySHA256 {
		return sessionPressureError("cleanup enforce parent is not the verified installed resident artifact", 2)
	}
	result, err := hostcleanup.NewManager(dir).MaybeRelieve(context.Background(), snapshot)
	if err != nil {
		return sessionPressureError(err.Error(), 1)
	}
	payload := map[string]any{"ok": true, "action": "cleanup.enforce", "result": result}
	return emitPressure(g, payload, fmt.Sprintf("cleanup enforce: attempted=%t acted=%t result=%s\n", result.Attempted, result.Acted, result.Result), 0)
}

func cmdSessionPressureCleanupStatus(g *Flags, dir string, args []string) int {
	if len(args) != 0 {
		return sessionPressureError("cleanup status accepts no arguments", 2)
	}
	policy, persisted, err := hostcleanup.LoadPolicy(dir)
	if err != nil {
		return sessionPressureError(err.Error(), 1)
	}
	claims, err := hostcleanup.NewClaimStore(dir).List()
	if err != nil {
		return sessionPressureError(err.Error(), 1)
	}
	actions, err := hostcleanup.ReadActions(dir, 20)
	if err != nil {
		return sessionPressureError(err.Error(), 1)
	}
	claimSummary := summarizeCleanupClaims(claims)
	now := time.Now().UTC()
	remaining := policy.ObservationRemaining(now)
	payload := map[string]any{
		"ok": true, "action": "cleanup.status", "policy": policy,
		"policy_path": hostcleanup.PolicyPath(dir), "policy_persisted": persisted,
		"claims": claims, "active_claims": claimSummary.Active, "stale_claims": claimSummary.Stale,
		"process_cleanup_opt_in_claims": claimSummary.ProcessOptIn,
		"active_process_cleanup_claims": claimSummary.ActiveProcessOptIn,
		"stale_process_cleanup_claims":  claimSummary.StaleProcessOptIn,
		"observation_remaining_seconds": cleanupDurationSeconds(remaining),
		"recent_actions":                actions,
	}
	graduation := "not-scheduled"
	if !policy.ObservationStartedAt.IsZero() {
		graduationAt := policy.ProcessOnlyGraduationAt()
		payload["process_only_graduation_at"] = graduationAt
		graduation = graduationAt.Local().Format(time.RFC3339)
	}
	payload["auto_process_only_graduation_scheduled"] = policy.AutoGraduateProcessOnly
	payload["auto_native_provider_graduation_scheduled"] = policy.AutoGraduateNative
	payload["browser_graduation_at"] = policy.BrowserGraduationAt()
	payload["dev_session_graduation_at"] = policy.DevGraduationAt()
	payload["docker_workspace_graduation_at"] = policy.DockerGraduationAt()
	text := fmt.Sprintf("cleanup mode=%s policy=%s claims=%d active=%d stale=%d process_opt_in=%d recent_actions=%d process_only_graduation=%s\n",
		cleanupPolicyMode(policy, persisted), hostcleanup.PolicyPath(dir), len(claims), claimSummary.Active, claimSummary.Stale,
		claimSummary.ProcessOptIn, len(actions), graduation)
	return emitPressure(g, payload, text, 0)
}

type cleanupClaimSummary struct {
	Active             int
	Stale              int
	ProcessOptIn       int
	ActiveProcessOptIn int
	StaleProcessOptIn  int
}

func summarizeCleanupClaims(claims []hostcleanup.ClaimView) cleanupClaimSummary {
	var summary cleanupClaimSummary
	for _, claim := range claims {
		if claim.State == hostcleanup.ClaimActive {
			summary.Active++
		} else {
			summary.Stale++
		}
		if claim.ResourceKind != hostcleanup.ResourceProcess || !claim.CleanupOnStale {
			continue
		}
		summary.ProcessOptIn++
		if claim.State == hostcleanup.ClaimActive {
			summary.ActiveProcessOptIn++
		} else {
			summary.StaleProcessOptIn++
		}
	}
	return summary
}

func cleanupDurationSeconds(value time.Duration) int64 {
	if value <= 0 {
		return 0
	}
	return int64((value + time.Second - 1) / time.Second)
}

func cmdSessionPressureCleanupPlan(g *Flags, dir string, args []string) int {
	if len(args) != 0 {
		return sessionPressureError("cleanup plan accepts no arguments", 2)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	runtime, err := loadPressureRuntime(ctx)
	if err != nil {
		return sessionPressureError(err.Error(), 1)
	}
	snapshot, err := SampleSnapshot(ctx, runtime.sampler, runtime.policy)
	if err != nil {
		return sessionPressureError(err.Error(), 1)
	}
	report, err := hostcleanup.NewManager(dir).Plan(ctx, snapshot)
	if err != nil {
		return sessionPressureError(err.Error(), 1)
	}
	eligible := 0
	for _, candidate := range report.Candidates {
		if candidate.Eligible {
			eligible++
		}
	}
	payload := map[string]any{"ok": true, "action": "cleanup.plan", "snapshot": snapshot, "report": report, "eligible_count": eligible}
	text := fmt.Sprintf("cleanup dry-run: triggered=%t eligible=%d candidates=%d (%s)\n", report.Triggered, eligible, len(report.Candidates), report.TriggerReason)
	return emitPressure(g, payload, text, 0)
}

func cmdSessionPressureCleanupHistory(g *Flags, dir string, args []string) int {
	set := flag.NewFlagSet("session pressure cleanup history", flag.ContinueOnError)
	set.SetOutput(os.Stderr)
	limit := set.Int("limit", 50, "maximum action rows")
	if err := set.Parse(args); err != nil {
		return 2
	}
	if set.NArg() != 0 || *limit < 1 || *limit > 1000 {
		return sessionPressureError("cleanup history requires --limit between 1 and 1000", 2)
	}
	actions, err := hostcleanup.ReadActions(dir, *limit)
	if err != nil {
		return sessionPressureError(err.Error(), 1)
	}
	payload := map[string]any{"ok": true, "action": "cleanup.history", "actions": actions, "count": len(actions)}
	return emitPressure(g, payload, fmt.Sprintf("%d cleanup action row(s)\n", len(actions)), 0)
}

func cmdSessionPressureCleanupPolicy(g *Flags, dir string, args []string) int {
	verb := "show"
	if len(args) > 0 {
		verb = args[0]
		args = args[1:]
	}
	policy, persisted, err := hostcleanup.LoadPolicy(dir)
	if err != nil {
		return sessionPressureError(err.Error(), 1)
	}
	switch verb {
	case "show":
		if len(args) != 0 {
			return sessionPressureError("cleanup policy show accepts no arguments", 2)
		}
	case "init":
		force := false
		for _, arg := range args {
			if arg == "--force" {
				force = true
				continue
			}
			return sessionPressureError("cleanup policy init accepts only --force", 2)
		}
		if persisted && !force {
			return sessionPressureError("cleanup policy already exists; pass --force to replace it", 1)
		}
		policy = hostcleanup.DefaultPolicy()
		policy.ObservationStartedAt = time.Now().UTC()
		if err := hostcleanup.SavePolicy(dir, policy); err != nil {
			return sessionPressureError(err.Error(), 1)
		}
		persisted = true
	case "enable":
		if len(args) != 0 {
			return sessionPressureError("cleanup policy enable accepts no arguments", 2)
		}
		if !persisted {
			return sessionPressureError("cleanup policy enable requires a persisted observation policy", 11)
		}
		if remaining := policy.ObservationRemaining(time.Now()); remaining > 0 {
			return sessionPressureError(fmt.Sprintf("cleanup policy requires seven days of observation before process-only graduation; remaining=%s", remaining.Round(time.Second)), 11)
		}
		policy.Enabled, policy.Enforce = true, true
		policy.AutoGraduateProcessOnly = false
		policy.AutoGraduateNative = false
		policy.ProcessEnabled = true
		policy.BrowserEnabled = false
		policy.DevSessionEnabled = false
		policy.DockerWorkspaceEnabled = false
		if err := hostcleanup.SavePolicy(dir, policy); err != nil {
			return sessionPressureError(err.Error(), 1)
		}
		persisted = true
	case "schedule":
		if len(args) != 0 {
			return sessionPressureError("cleanup policy schedule accepts no arguments", 2)
		}
		if !persisted {
			return sessionPressureError("cleanup policy schedule requires a persisted observation policy", 11)
		}
		if policy.Enforce {
			return sessionPressureError("cleanup policy is already enforced; use policy observe to restart observation", 1)
		}
		policy.Enabled = true
		policy.AutoGraduateProcessOnly = true
		policy.AutoGraduateNative = true
		policy.ProcessEnabled = true
		policy.BrowserEnabled = true
		policy.DevSessionEnabled = true
		policy.DockerWorkspaceEnabled = true
		if policy.ObservationStartedAt.IsZero() {
			policy.ObservationStartedAt = time.Now().UTC()
		}
		if err := hostcleanup.SavePolicy(dir, policy); err != nil {
			return sessionPressureError(err.Error(), 1)
		}
		persisted = true
	case "observe":
		if len(args) != 0 {
			return sessionPressureError("cleanup policy observe accepts no arguments", 2)
		}
		policy.Enabled, policy.Enforce = true, false
		policy.AutoGraduateProcessOnly = false
		policy.AutoGraduateNative = false
		policy.ProcessEnabled = true
		policy.BrowserEnabled = true
		policy.DevSessionEnabled = true
		policy.DockerWorkspaceEnabled = true
		policy.ObservationStartedAt = time.Now().UTC()
		if err := hostcleanup.SavePolicy(dir, policy); err != nil {
			return sessionPressureError(err.Error(), 1)
		}
		persisted = true
	case "disable":
		if len(args) != 0 {
			return sessionPressureError("cleanup policy disable accepts no arguments", 2)
		}
		policy.Enabled, policy.Enforce = false, false
		policy.AutoGraduateProcessOnly = false
		policy.AutoGraduateNative = false
		policy.ObservationStartedAt = time.Time{}
		if err := hostcleanup.SavePolicy(dir, policy); err != nil {
			return sessionPressureError(err.Error(), 1)
		}
		persisted = true
	default:
		return sessionPressureError("unknown cleanup policy action "+strconv.Quote(verb), 2)
	}
	payload := map[string]any{"ok": true, "action": "cleanup.policy." + verb, "policy": policy, "path": hostcleanup.PolicyPath(dir), "persisted": persisted}
	text := fmt.Sprintf("cleanup policy %s: mode=%s path=%s\n", verb, cleanupPolicyMode(policy, persisted), hostcleanup.PolicyPath(dir))
	return emitPressure(g, payload, text, 0)
}

func cmdSessionPressureCleanupClaim(g *Flags, dir string, args []string) int {
	if len(args) == 0 {
		return sessionPressureError("cleanup claim requires list, acquire, heartbeat, or release", 2)
	}
	store := hostcleanup.NewClaimStore(dir)
	switch args[0] {
	case "list":
		if len(args) != 1 {
			return sessionPressureError("cleanup claim list accepts no arguments", 2)
		}
		claims, err := store.List()
		if err != nil {
			return sessionPressureError(err.Error(), 1)
		}
		return emitPressure(g, map[string]any{"ok": true, "action": "cleanup.claim.list", "claims": claims, "count": len(claims)}, fmt.Sprintf("%d resource claim(s)\n", len(claims)), 0)
	case "acquire":
		return cmdSessionPressureCleanupClaimAcquire(g, store, args[1:])
	case "heartbeat", "release":
		verb := args[0]
		set := flag.NewFlagSet("session pressure cleanup claim "+verb, flag.ContinueOnError)
		set.SetOutput(os.Stderr)
		claimID := set.String("claim-id", "", "exact claim id")
		if err := set.Parse(args[1:]); err != nil {
			return 2
		}
		if set.NArg() != 0 || strings.TrimSpace(*claimID) == "" {
			return sessionPressureError("cleanup claim "+verb+" requires --claim-id", 2)
		}
		if verb == "heartbeat" {
			claim, err := store.Heartbeat(*claimID)
			if err != nil {
				return sessionPressureError(err.Error(), 1)
			}
			return emitPressure(g, map[string]any{"ok": true, "action": "cleanup.claim.heartbeat", "claim": claim}, "renewed resource claim "+claim.ID+"\n", 0)
		}
		if err := store.Release(*claimID); err != nil {
			return sessionPressureError(err.Error(), 1)
		}
		return emitPressure(g, map[string]any{"ok": true, "action": "cleanup.claim.release", "claim_id": *claimID}, "released resource claim "+*claimID+"\n", 0)
	default:
		return sessionPressureError("unknown cleanup claim action "+strconv.Quote(args[0]), 2)
	}
}

func cmdSessionPressureCleanupClaimAcquire(g *Flags, store *hostcleanup.ClaimStore, args []string) int {
	set := flag.NewFlagSet("session pressure cleanup claim acquire", flag.ContinueOnError)
	set.SetOutput(os.Stderr)
	kindValue := set.String("kind", "", "resource kind")
	resourceID := set.String("resource", "", "resource id")
	owner := set.String("owner", "", "claim owner")
	ttlValue := set.String("ttl", "", "claim TTL")
	note := set.String("note", "", "bounded non-secret note")
	pid := set.Int("pid", 0, "root PID for a process claim")
	cleanupOnStale := set.Bool("cleanup-on-stale", false, "allow exact stale process claim cleanup")
	if err := set.Parse(args); err != nil {
		return 2
	}
	if set.NArg() != 0 || *kindValue == "" || *resourceID == "" || *owner == "" || *ttlValue == "" {
		return sessionPressureError("cleanup claim acquire requires --kind, --resource, --owner, and --ttl", 2)
	}
	kind := hostcleanup.ResourceKind(*kindValue)
	if !kind.Valid() {
		return sessionPressureError("invalid cleanup claim kind "+strconv.Quote(*kindValue), 2)
	}
	ttl, err := time.ParseDuration(*ttlValue)
	if err != nil {
		return sessionPressureError("invalid claim TTL: "+err.Error(), 2)
	}
	claim, err := store.Acquire(kind, *resourceID, *owner, ttl, *cleanupOnStale, *pid, *note)
	if err != nil {
		return sessionPressureError(err.Error(), 1)
	}
	payload := map[string]any{"ok": true, "action": "cleanup.claim.acquire", "claim": claim}
	text := fmt.Sprintf("acquired resource claim %s for %s/%s until %s\n", claim.ID, claim.ResourceKind, claim.ResourceID, claim.ExpiresAt.Local().Format(time.RFC3339))
	return emitPressure(g, payload, text, 0)
}

func cleanupPolicyMode(policy hostcleanup.Policy, persisted bool) string {
	if !persisted {
		return "uninitialized-observe"
	}
	if !policy.Enabled {
		return "disabled"
	}
	if policy.Enforce {
		return "enforce"
	}
	return "observe"
}
