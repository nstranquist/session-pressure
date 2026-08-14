package sessionpressurecmd

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/nstranquist/session-pressure/internal/hostcleanup"
	"github.com/nstranquist/session-pressure/sessionpressure"
)

func TestSessionPressureCleanupPolicyLifecycle(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(sessionpressure.DataDirEnv, dir)
	var code int
	out := captureStdout(t, func() {
		code = cmdSessionPressure(&Flags{JSON: true}, []string{"cleanup", "policy", "init"})
	})
	if code != 0 {
		t.Fatalf("policy init code=%d out=%s", code, out)
	}
	policy, persisted, err := hostcleanup.LoadPolicy(dir)
	if err != nil || !persisted || policy.Enforce {
		t.Fatalf("init policy=%+v persisted=%t err=%v", policy, persisted, err)
	}
	out = captureStdout(t, func() {
		code = cmdSessionPressure(&Flags{JSON: true}, []string{"cleanup", "policy", "schedule"})
	})
	policy, _, err = hostcleanup.LoadPolicy(dir)
	if code != 0 || err != nil || policy.Enforce || !policy.AutoGraduateProcessOnly || !policy.AutoGraduateNative {
		t.Fatalf("policy schedule code=%d out=%s policy=%+v err=%v", code, out, policy, err)
	}
	out = captureStdout(t, func() {
		code = cmdSessionPressure(&Flags{JSON: true}, []string{"cleanup", "policy", "enable"})
	})
	if code != 11 {
		t.Fatalf("early policy enable code=%d out=%s", code, out)
	}
	policy.ObservationStartedAt = time.Now().Add(-hostcleanup.MinimumObservationWindow - time.Hour)
	if err := hostcleanup.SavePolicy(dir, policy); err != nil {
		t.Fatal(err)
	}
	out = captureStdout(t, func() {
		code = cmdSessionPressure(&Flags{JSON: true}, []string{"cleanup", "policy", "enable"})
	})
	if code != 0 {
		t.Fatalf("policy enable code=%d out=%s", code, out)
	}
	policy, _, err = hostcleanup.LoadPolicy(dir)
	if err != nil || !policy.Enabled || !policy.Enforce || !policy.ProcessOnly() || policy.AutoGraduateProcessOnly {
		t.Fatalf("enabled policy=%+v err=%v", policy, err)
	}
	out = captureStdout(t, func() {
		code = cmdSessionPressure(&Flags{JSON: true}, []string{"cleanup", "status"})
	})
	var statusPayload struct {
		PolicyPersisted           bool      `json:"policy_persisted"`
		ActiveClaims              int       `json:"active_claims"`
		ProcessCleanupOptInClaims int       `json:"process_cleanup_opt_in_claims"`
		ObservationRemaining      int64     `json:"observation_remaining_seconds"`
		ProcessOnlyGraduationAt   time.Time `json:"process_only_graduation_at"`
	}
	decodeErr := json.Unmarshal([]byte(out), &statusPayload)
	if code != 0 || decodeErr != nil || !statusPayload.PolicyPersisted || statusPayload.ActiveClaims != 0 || statusPayload.ProcessCleanupOptInClaims != 0 || statusPayload.ObservationRemaining != 0 || statusPayload.ProcessOnlyGraduationAt.IsZero() {
		t.Fatalf("status code=%d out=%s", code, out)
	}
}

func TestSummarizeCleanupClaimsSeparatesProcessOptIn(t *testing.T) {
	claims := []hostcleanup.ClaimView{
		{Claim: hostcleanup.Claim{ResourceKind: hostcleanup.ResourceDevSession}, State: hostcleanup.ClaimActive},
		{Claim: hostcleanup.Claim{ResourceKind: hostcleanup.ResourceProcess, CleanupOnStale: true}, State: hostcleanup.ClaimActive},
		{Claim: hostcleanup.Claim{ResourceKind: hostcleanup.ResourceProcess, CleanupOnStale: true}, State: hostcleanup.ClaimStale},
		{Claim: hostcleanup.Claim{ResourceKind: hostcleanup.ResourceProcess}, State: hostcleanup.ClaimStale},
	}
	summary := summarizeCleanupClaims(claims)
	if summary.Active != 2 || summary.Stale != 2 || summary.ProcessOptIn != 2 || summary.ActiveProcessOptIn != 1 || summary.StaleProcessOptIn != 1 {
		t.Fatalf("summary=%+v", summary)
	}
	if got := cleanupDurationSeconds(1500 * time.Millisecond); got != 2 {
		t.Fatalf("rounded observation seconds=%d", got)
	}
}

func TestSessionPressureCleanupClaimLifecycle(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(sessionpressure.DataDirEnv, dir)
	var code int
	out := captureStdout(t, func() {
		code = cmdSessionPressure(&Flags{JSON: true}, []string{
			"cleanup", "claim", "acquire", "--kind", "dev_session", "--resource", "ad-hoc/api",
			"--owner", "test-owner", "--ttl", "2h",
		})
	})
	if code != 0 {
		t.Fatalf("acquire code=%d out=%s", code, out)
	}
	var payload struct {
		Claim hostcleanup.Claim `json:"claim"`
	}
	if err := json.Unmarshal([]byte(out), &payload); err != nil || payload.Claim.ID == "" {
		t.Fatalf("decode acquire: claim=%+v err=%v out=%s", payload.Claim, err, out)
	}
	out = captureStdout(t, func() {
		code = cmdSessionPressure(&Flags{JSON: true}, []string{"cleanup", "claim", "heartbeat", "--claim-id", payload.Claim.ID})
	})
	if code != 0 || !strings.Contains(out, payload.Claim.ID) {
		t.Fatalf("heartbeat code=%d out=%s", code, out)
	}
	out = captureStdout(t, func() {
		code = cmdSessionPressure(&Flags{JSON: true}, []string{"cleanup", "claim", "release", "--claim-id", payload.Claim.ID})
	})
	if code != 0 || !strings.Contains(out, payload.Claim.ID) {
		t.Fatalf("release code=%d out=%s", code, out)
	}
	claims, err := hostcleanup.NewClaimStore(dir).List()
	if err != nil || len(claims) != 0 {
		t.Fatalf("claims=%+v err=%v", claims, err)
	}
}

func TestSessionPressureCleanupEnforceRequiresVerifiedResidentParent(t *testing.T) {
	digest := strings.Repeat("a", 64)
	parentPID := os.Getppid()
	fake := &fakePressureLaunchdController{status: sessionpressure.LaunchdStatus{
		OK: true, Loaded: true, PID: parentPID, ArtifactPresent: true,
		ArtifactVerified: true, ArtifactSHA256: digest,
	}}
	installFakePressureLaunchd(t, fake)
	snapshot := sessionpressure.Snapshot{
		Timestamp: time.Now().UTC(), GuardRole: "resident", GuardPID: parentPID,
		GuardBinarySHA256: digest, GuardBudgetApplicable: true,
	}
	run := func(value sessionpressure.Snapshot) int {
		body, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		code, _, _ := captureMainOutput(t, func() int {
			return cmdSessionPressureCleanupEnforceFromReader(&Flags{JSON: true}, t.TempDir(), nil, strings.NewReader(string(body)))
		})
		return code
	}
	if code := run(snapshot); code != 0 {
		t.Fatalf("verified resident bridge code=%d", code)
	}
	forged := snapshot
	forged.GuardPID = os.Getpid()
	if code := run(forged); code != 2 {
		t.Fatalf("forged parent bridge code=%d", code)
	}
	stale := snapshot
	stale.Timestamp = time.Now().Add(-cleanupBridgeSnapshotMaxAge - time.Second)
	if code := run(stale); code != 2 {
		t.Fatalf("stale snapshot bridge code=%d", code)
	}
	mismatched := snapshot
	mismatched.GuardBinarySHA256 = strings.Repeat("b", 64)
	if code := run(mismatched); code != 2 {
		t.Fatalf("mismatched artifact bridge code=%d", code)
	}
}

func TestSessionPressureHelpAdvertisesCleanup(t *testing.T) {
	if !strings.Contains(sessionPressureHelp, "cleanup <subcommand>") || !strings.Contains(sessionPressureCleanupHelp, "active and stale") {
		t.Fatal("session pressure help does not advertise cleanup claims")
	}
}
