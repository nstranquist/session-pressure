//go:build darwin

package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nstranquist/session-pressure/sessionpressure"
)

func TestRunRequiresInitializedPolicyBeforeStartingLifecycle(t *testing.T) {
	t.Setenv(sessionpressure.DataDirEnv, t.TempDir())
	err := run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "policy is not initialized") {
		t.Fatalf("run error=%v", err)
	}
}

func TestNotifyRecoveryHintQueuesSafeActionableToast(t *testing.T) {
	inbox := filepath.Join(t.TempDir(), "notifications", "inbox.jsonl")
	t.Setenv("NDEV_TOAST_INBOX", inbox)
	hint := sessionpressure.RecoveryHint{
		DetectedAt:      time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC),
		RecoveryCommand: `ndev session recover --around "2026-07-14 11:55:00" --window 30m --include-resume-command`,
	}
	if err := notifyRecoveryHint(hint); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(inbox)
	if err != nil {
		t.Fatal(err)
	}
	var toast struct {
		Source         string `json:"source"`
		ExecuteCommand string `json:"execute_command"`
	}
	if err := json.Unmarshal(body, &toast); err != nil {
		t.Fatalf("toast JSON: %v body=%s", err, body)
	}
	if toast.Source != "session-pressure-helper" || toast.ExecuteCommand != hint.RecoveryCommand {
		t.Fatalf("toast=%+v", toast)
	}
	if strings.Contains(string(body), "prompt") {
		t.Fatalf("toast leaked prompt-shaped content: %s", body)
	}
}

func TestCommandResourceCleanerDoesNotSpawnOutsidePersistedEnforcementTrigger(t *testing.T) {
	dir := t.TempDir()
	policy := sessionpressure.DefaultPolicy(16 * 1024)
	cleaner := commandResourceCleaner{dir: dir, controlBinary: "/does/not/exist", pressurePolicy: policy}
	snapshot := sessionpressure.Snapshot{Level: sessionpressure.LevelNormal, PhysicalMemoryMB: 16 * 1024, FreePercent: 50}
	if _, err := cleaner.MaybeRelieve(context.Background(), snapshot); err != nil {
		t.Fatalf("missing observe-only policy should be a no-op: %v", err)
	}
	body := []byte(`{"enabled":true,"enforce":true,"trigger_level":"red","sustain_samples":2}`)
	if err := os.WriteFile(filepath.Join(dir, "cleanup-policy.json"), body, 0o600); err != nil {
		t.Fatal(err)
	}
	snapshot.MemoryConsecutiveSamples = 2
	if _, err := cleaner.MaybeRelieve(context.Background(), snapshot); err != nil {
		t.Fatalf("normal memory must not spawn the control binary: %v", err)
	}
	snapshot.FreePercent = 10
	if _, err := cleaner.MaybeRelieve(context.Background(), snapshot); err == nil || !strings.Contains(err.Error(), "/does/not/exist") {
		t.Fatalf("sustained memory red did not cross the process bridge: %v", err)
	}
}

func TestCommandResourceCleanerInvokesControlForDueScheduledGraduationAtNormalMemory(t *testing.T) {
	dir := t.TempDir()
	pressurePolicy := sessionpressure.DefaultPolicy(16 * 1024)
	started := time.Now().UTC().Add(-cleanupBridgeObservationWindow - time.Minute)
	body, err := json.Marshal(cleanupBridgePolicy{
		Enabled: true, AutoGraduateProcessOnly: true, ObservationStartedAt: started,
		TriggerLevel: sessionpressure.LevelRed, SustainSamples: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "cleanup-policy.json"), body, 0o600); err != nil {
		t.Fatal(err)
	}
	controlBinary := filepath.Join(dir, "ndev")
	if err := os.WriteFile(controlBinary, []byte("verified control fixture"), 0o755); err != nil {
		t.Fatal(err)
	}
	controlDigest, err := sessionpressure.ControlBinarySHA256(controlBinary)
	if err != nil {
		t.Fatal(err)
	}
	called := false
	cleaner := commandResourceCleaner{
		dir: dir, controlBinary: controlBinary, controlDigest: controlDigest, pressurePolicy: pressurePolicy,
		runControl: func(context.Context, string, []byte) ([]byte, error) {
			called = true
			return []byte(`{"result":{}}`), nil
		},
	}
	result, err := cleaner.MaybeRelieve(context.Background(), sessionpressure.Snapshot{
		Timestamp: time.Now().UTC(), PhysicalMemoryMB: 16 * 1024, FreePercent: 50,
		GuardRole: "resident", GuardBudgetOK: true, GuardBaselineProven: true, ProcessInventoryFresh: true,
	})
	if err != nil || !called || !result.ControlExecuted {
		t.Fatalf("due graduation called=%t result=%+v err=%v", called, result, err)
	}
}

func TestCleanupBridgePolicyStagesNativeGraduations(t *testing.T) {
	now := time.Now().UTC()
	policy := cleanupBridgePolicy{
		Enabled: true, Enforce: true, AutoGraduateNative: true,
		ObservationStartedAt: now.Add(-4*cleanupBridgeObservationWindow - time.Hour),
	}
	if !policy.autoGraduationDue(now) {
		t.Fatal("browser stage was not due")
	}
	policy.BrowserEnabled = true
	if !policy.autoGraduationDue(now) {
		t.Fatal("dev-session stage was not due")
	}
	policy.DevSessionEnabled = true
	if !policy.autoGraduationDue(now) {
		t.Fatal("Docker stage was not due")
	}
	policy.DockerWorkspaceEnabled = true
	if policy.autoGraduationDue(now) {
		t.Fatal("completed native rollout still reported a due stage")
	}
}

func TestCommandResourceCleanerBoundsControlPlaneExecution(t *testing.T) {
	dir := t.TempDir()
	policy := sessionpressure.DefaultPolicy(16 * 1024)
	if err := os.WriteFile(filepath.Join(dir, "cleanup-policy.json"), []byte(`{"enabled":true,"enforce":true,"trigger_level":"red","sustain_samples":2}`), 0o600); err != nil {
		t.Fatal(err)
	}
	controlBinary := filepath.Join(dir, "ndev")
	if err := os.WriteFile(controlBinary, []byte("verified control fixture"), 0o755); err != nil {
		t.Fatal(err)
	}
	controlDigest, err := sessionpressure.ControlBinarySHA256(controlBinary)
	if err != nil {
		t.Fatal(err)
	}
	cleaner := commandResourceCleaner{
		dir: dir, controlBinary: controlBinary, controlDigest: controlDigest, pressurePolicy: policy,
		timeout: 10 * time.Millisecond,
		runControl: func(ctx context.Context, _ string, _ []byte) ([]byte, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		},
	}
	snapshot := sessionpressure.Snapshot{
		Timestamp: time.Now().UTC(), PhysicalMemoryMB: 16 * 1024, FreePercent: 10,
		MemoryConsecutiveSamples: 2,
	}
	started := time.Now()
	result, err := cleaner.MaybeRelieve(context.Background(), snapshot)
	if err == nil || !strings.Contains(err.Error(), "deadline") || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("bounded bridge err=%v", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("bounded bridge took %s", elapsed)
	}
	if !result.ControlExecuted || result.ControlDurationMS < 1 || result.ControlDurationMS > 1000 {
		t.Fatalf("bounded bridge performance result=%+v", result)
	}
}

func TestCommandResourceCleanerReportsControlPerformance(t *testing.T) {
	dir := t.TempDir()
	policy := sessionpressure.DefaultPolicy(16 * 1024)
	if err := os.WriteFile(filepath.Join(dir, "cleanup-policy.json"), []byte(`{"enabled":true,"enforce":true,"trigger_level":"red","sustain_samples":2}`), 0o600); err != nil {
		t.Fatal(err)
	}
	controlBinary := filepath.Join(dir, "ndev")
	if err := os.WriteFile(controlBinary, []byte("verified control fixture"), 0o755); err != nil {
		t.Fatal(err)
	}
	controlDigest, err := sessionpressure.ControlBinarySHA256(controlBinary)
	if err != nil {
		t.Fatal(err)
	}
	cleaner := commandResourceCleaner{
		dir: dir, controlBinary: controlBinary, controlDigest: controlDigest, pressurePolicy: policy,
		runControl: func(context.Context, string, []byte) ([]byte, error) {
			time.Sleep(5 * time.Millisecond)
			return []byte(`{"result":{"attempted":true,"result":"no_candidate"}}`), nil
		},
	}
	result, err := cleaner.MaybeRelieve(context.Background(), sessionpressure.Snapshot{
		Timestamp: time.Now().UTC(), PhysicalMemoryMB: 16 * 1024, FreePercent: 10, MemoryConsecutiveSamples: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.ControlExecuted || result.ControlDurationMS < 5 || result.ControlMaxRSSMB != 0 {
		t.Fatalf("control performance result=%+v", result)
	}
}

func TestCommandResourceCleanerRejectsChangedControlBinaryBeforeExecution(t *testing.T) {
	dir := t.TempDir()
	policy := sessionpressure.DefaultPolicy(16 * 1024)
	if err := os.WriteFile(filepath.Join(dir, "cleanup-policy.json"), []byte(`{"enabled":true,"enforce":true,"trigger_level":"red","sustain_samples":2}`), 0o600); err != nil {
		t.Fatal(err)
	}
	controlBinary := filepath.Join(dir, "ndev")
	if err := os.WriteFile(controlBinary, []byte("control-revision-a"), 0o755); err != nil {
		t.Fatal(err)
	}
	controlDigest, err := sessionpressure.ControlBinarySHA256(controlBinary)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(controlBinary, []byte("control-revision-b"), 0o755); err != nil {
		t.Fatal(err)
	}
	called := false
	cleaner := commandResourceCleaner{
		dir: dir, controlBinary: controlBinary, controlDigest: controlDigest, pressurePolicy: policy,
		runControl: func(context.Context, string, []byte) ([]byte, error) {
			called = true
			return nil, nil
		},
	}
	snapshot := sessionpressure.Snapshot{
		Timestamp: time.Now().UTC(), PhysicalMemoryMB: 16 * 1024, FreePercent: 10,
		MemoryConsecutiveSamples: 2,
	}
	if _, err := cleaner.MaybeRelieve(context.Background(), snapshot); err == nil || !strings.Contains(err.Error(), "digest mismatch") || called {
		t.Fatalf("changed controller crossed execution boundary: called=%t err=%v", called, err)
	}
}
