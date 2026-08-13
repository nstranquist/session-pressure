package sessionpressurecmd

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nstranquist/session-pressure/third_party/pageskein/browser"
	"github.com/nstranquist/session-pressure/internal/sessionpressure"
)

func withStorageProviderFakes(t *testing.T) {
	t.Helper()
	originalSize := storageDirectoryBytes
	originalActive := storagePNPMActive
	originalGradleActive := storageGradleActive
	originalCacheOwnerActive := storageCacheOwnerActive
	originalPrune := storageRunPNPMPrune
	originalGoClean := storageRunGoBuildCacheClean
	originalRemoveAll := storageRemoveAll
	originalGC := storageBrowserGC
	originalAdmission := storageHostAdmission
	originalMobileSyncEvidence := storageMobileSyncEvidence
	originalMobileSyncState := storageReadMobileSyncState
	originalEmptyTrash := storageEmptyTrash
	originalCleanUpdaterCaches := storageCleanUpdaterCaches
	originalCleanBrowserMediaCaches := storageCleanBrowserMediaCaches
	originalDockerAvailable := storageDockerAvailable
	originalDockerReclaimable := storageDockerReclaimable
	originalRunDockerPrune := storageRunDockerPrune
	originalMeasureHotness := storageMeasureHotness
	storageGradleActive = func() (bool, error) { return false, nil }
	storageCacheOwnerActive = func(string) (bool, error) { return false, nil }
	storageMobileSyncEvidence = func(string, time.Time) storageProviderDecisionEvidence {
		return storageProviderDecisionEvidence{SchemaVersion: 1, Scope: "direct_backup_roots", Available: true, IdentityRedacted: true}
	}
	storageDockerAvailable = func() (string, bool, error) { return "", false, nil }
	// Most command tests use the real HOME and fake directory sizes. Keep the
	// recursive Go age walk out of those tests; the synthetic plan test below
	// opts into the real bounded walker explicitly.
	storageMeasureHotness = func(string, time.Time) (*storageProviderHotnessEvidence, error) { return nil, nil }
	t.Cleanup(func() {
		storageDirectoryBytes = originalSize
		storagePNPMActive = originalActive
		storageGradleActive = originalGradleActive
		storageCacheOwnerActive = originalCacheOwnerActive
		storageRunPNPMPrune = originalPrune
		storageRunGoBuildCacheClean = originalGoClean
		storageRemoveAll = originalRemoveAll
		storageBrowserGC = originalGC
		storageHostAdmission = originalAdmission
		storageMobileSyncEvidence = originalMobileSyncEvidence
		storageReadMobileSyncState = originalMobileSyncState
		storageEmptyTrash = originalEmptyTrash
		storageCleanUpdaterCaches = originalCleanUpdaterCaches
		storageCleanBrowserMediaCaches = originalCleanBrowserMediaCaches
		storageDockerAvailable = originalDockerAvailable
		storageDockerReclaimable = originalDockerReclaimable
		storageRunDockerPrune = originalRunDockerPrune
		storageMeasureHotness = originalMeasureHotness
	})
}

func TestExecutePNPMProviderCompletesWithIntentAndResult(t *testing.T) {
	withStorageProviderFakes(t)
	dir := t.TempDir()
	t.Setenv(sessionpressure.StorageVolumePathEnv, t.TempDir())
	storageDirectoryBytes = func(string) (int64, bool, error) { return 4096, true, nil }
	storagePNPMActive = func() (bool, error) { return false, nil }
	storageGradleActive = func() (bool, error) { return false, nil }
	pruneCalls := 0
	storageRunPNPMPrune = func(context.Context) error { pruneCalls++; return nil }
	storageHostAdmission = func(context.Context) sessionpressure.Admission {
		return sessionpressure.Admission{Allowed: true, Level: sessionpressure.LevelNormal}
	}
	runtime := pressureRuntime{dir: dir, policy: sessionpressure.DefaultPolicy(16 << 10)}
	provider := storageProviderReport{ID: "pnpm-store", Path: filepath.Join(t.TempDir(), "store"), Classification: storageProviderAutoSafe, MutationSupported: true}
	receipts, err := executeStorageProvider(t.Context(), runtime, provider)
	if err != nil || pruneCalls != 1 || len(receipts) != 2 || receipts[0].Mode != "intent" || receipts[1].Mode != "result" || receipts[1].Outcome != "completed" || receipts[1].ProviderBytesMeasured || receipts[1].AfterProviderBytes != 0 {
		t.Fatalf("receipts=%+v pruneCalls=%d err=%v", receipts, pruneCalls, err)
	}
}

func TestParseStorageBytes(t *testing.T) {
	for raw, want := range map[string]int64{"50GiB": 50 << 30, "1.5GB": 1_500_000_000, "512MiB": 512 << 20, "4096": 4096, "0B": 0} {
		got, err := parseStorageBytes(raw)
		if err != nil || got != want {
			t.Fatalf("parseStorageBytes(%q) = %d, %v; want %d", raw, got, err, want)
		}
	}
	for _, raw := range []string{"", "0", "-1GiB", "wat"} {
		if _, err := parseStorageBytes(raw); err == nil {
			t.Fatalf("parseStorageBytes(%q) unexpectedly passed", raw)
		}
	}
}

func TestStorageProviderClassificationPreservesPersonalStateAndRequiresExplicitGoCacheSelection(t *testing.T) {
	withStorageProviderFakes(t)
	storageDirectoryBytes = func(path string) (int64, bool, error) { return int64(len(path)) * 1024, true, nil }
	storagePNPMActive = func() (bool, error) { return false, nil }
	storageBrowserGC = func(options browser.GCOptions) (*browser.GCReport, error) {
		return &browser.GCReport{ReclaimableBytes: 1234}, nil
	}
	reports, err := inspectStorageProviders(sessionpressure.DefaultPolicy(16<<10), true)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"mobile-sync", "downloads", "go-module-cache"} {
		report, ok := storageProviderByID(reports, id)
		if !ok || report.Classification != storageProviderReportOnly || report.MutationSupported {
			t.Fatalf("provider %s = %+v", id, report)
		}
	}
	for _, id := range []string{"go-build-cache", "yarn-cache", "npm-cache", "brew-cache", "swiftpm-cache", "playwright-cache", "gradle-caches"} {
		report, ok := storageProviderByID(reports, id)
		if !ok || report.Classification != storageProviderOperator || !report.MutationSupported {
			t.Fatalf("provider %s = %+v", id, report)
		}
	}
	for _, id := range []string{"user-trash", "app-updater-caches", "browser-media-caches"} {
		report, ok := storageProviderByID(reports, id)
		if !ok || report.Classification != storageProviderOperator {
			t.Fatalf("provider %s = %+v", id, report)
		}
	}
	for _, id := range []string{"browser-dead-profiles", "pnpm-store"} {
		report, ok := storageProviderByID(reports, id)
		if !ok || report.Classification != storageProviderAutoSafe || !report.MutationSupported {
			t.Fatalf("provider %s = %+v", id, report)
		}
	}
}

func TestMobileSyncDecisionEvidenceIsIdentityRedactedAndReportOnly(t *testing.T) {
	withStorageProviderFakes(t)
	root := t.TempDir()
	backupRoot := filepath.Join(root, "Backup")
	for _, name := range []string{"private-device-a", "private-device-b", "private-device-c"} {
		if err := os.MkdirAll(filepath.Join(backupRoot, name), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(backupRoot, name, "Status.plist"), []byte("fixture"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Date(2026, 7, 23, 12, 0, 0, 0, time.UTC)
	oldest := now.Add(-400 * 24 * time.Hour)
	newest := now.Add(-30 * 24 * time.Hour)
	if err := os.Chtimes(filepath.Join(backupRoot, "private-device-a", "Status.plist"), oldest, oldest); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(filepath.Join(backupRoot, "private-device-b", "Status.plist"), newest, newest); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(filepath.Join(backupRoot, "private-device-c", "Status.plist"), now.Add(-time.Hour), now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	storageReadMobileSyncState = func(path string) (string, error) {
		switch filepath.Base(filepath.Dir(path)) {
		case "private-device-a":
			return "finished", nil
		case "private-device-b":
			return "in-progress", nil
		default:
			return "", errors.New("unreadable fixture")
		}
	}
	evidence := inspectMobileSyncDecisionEvidence(root, now)
	if !evidence.Available || !evidence.IdentityRedacted || evidence.MutationAuthorized || evidence.ItemCount != 3 ||
		evidence.CompletedItems != 1 || evidence.IncompleteItems != 1 || evidence.UnknownStateItems != 1 ||
		evidence.OldestAgeDays != 400 || evidence.NewestAgeDays != 0 || evidence.OldestModifiedAt != oldest.Format(time.RFC3339) {
		t.Fatalf("mobile sync evidence=%+v", evidence)
	}
	body, err := json.Marshal(evidence)
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{"private-device-a", "private-device-b", "private-device-c", backupRoot} {
		if strings.Contains(string(body), private) {
			t.Fatalf("private identity leaked in evidence: %s", body)
		}
	}
}

func TestInspectStorageProvidersRejectsMissingHome(t *testing.T) {
	t.Setenv("HOME", "")
	if _, err := inspectStorageProviders(sessionpressure.DefaultPolicy(16<<10), false); err == nil {
		t.Fatal("missing HOME produced relative storage-provider paths")
	}
}

func TestInspectStorageProvidersBlocksActiveCacheOwner(t *testing.T) {
	withStorageProviderFakes(t)
	storageDirectoryBytes = func(string) (int64, bool, error) { return 4096, true, nil }
	storagePNPMActive = func() (bool, error) { return false, nil }
	storageCacheOwnerActive = func(id string) (bool, error) { return id == "go-build-cache", nil }
	storageBrowserGC = func(browser.GCOptions) (*browser.GCReport, error) { return &browser.GCReport{}, nil }
	reports, err := inspectStorageProviders(sessionpressure.DefaultPolicy(16<<10), true)
	if err != nil {
		t.Fatal(err)
	}
	report, ok := storageProviderByID(reports, "go-build-cache")
	if !ok || !report.ActiveOwner || report.MutationSupported || !strings.Contains(report.BlockedReason, "active") {
		t.Fatalf("go-build-cache provider = %+v", report)
	}
}

func TestInspectNamedStorageProvidersBlocksActiveOwners(t *testing.T) {
	withStorageProviderFakes(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, "Library", "Caches", "ShipIt-fixture"), 0o700); err != nil {
		t.Fatal(err)
	}
	mediaPath := browserMediaCachePaths(home)[0]
	if err := os.MkdirAll(mediaPath, 0o700); err != nil {
		t.Fatal(err)
	}
	storageDirectoryBytes = func(string) (int64, bool, error) { return 4096, true, nil }
	storageBrowserGC = func(browser.GCOptions) (*browser.GCReport, error) { return &browser.GCReport{}, nil }
	storagePNPMActive = func() (bool, error) { return false, nil }
	storageCacheOwnerActive = func(id string) (bool, error) {
		return id == "app-updater-caches" || id == "browser-media-caches", nil
	}
	reports, err := inspectStorageProviders(sessionpressure.DefaultPolicy(16<<10), true)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"app-updater-caches", "browser-media-caches"} {
		report, ok := storageProviderByID(reports, id)
		if !ok || !report.ActiveOwner || report.MutationSupported || !strings.Contains(report.BlockedReason, "active") {
			t.Fatalf("provider %s = %+v", id, report)
		}
	}
}

func TestExecuteGoBuildCacheProviderWritesAuditedReceipts(t *testing.T) {
	withStorageProviderFakes(t)
	dir := t.TempDir()
	t.Setenv(sessionpressure.StorageVolumePathEnv, t.TempDir())
	cleanCalls := 0
	storageRunGoBuildCacheClean = func(context.Context, string) error {
		cleanCalls++
		return nil
	}
	storageGradleActive = func() (bool, error) { return false, nil }
	runtime := pressureRuntime{dir: dir, policy: sessionpressure.DefaultPolicy(16 << 10)}
	// All rebuildable-cache providers share the quarantine-rename executor.
	for _, id := range []string{"go-build-cache", "yarn-cache", "npm-cache", "brew-cache", "swiftpm-cache", "playwright-cache", "gradle-caches"} {
		provider := storageProviderReport{ID: id, Classification: storageProviderOperator, MutationSupported: true}
		receipts, err := executeStorageProvider(t.Context(), runtime, provider)
		if err != nil || len(receipts) != 2 || receipts[0].Mode != "intent" || receipts[1].Mode != "result" || receipts[1].Outcome != "completed" {
			t.Fatalf("provider %s receipts=%+v err=%v", id, receipts, err)
		}
	}
	if cleanCalls != 7 {
		t.Fatalf("cleanCalls=%d, want 7", cleanCalls)
	}
}

func TestExecuteRebuildableCacheProviderRevalidatesActiveOwner(t *testing.T) {
	withStorageProviderFakes(t)
	dir := t.TempDir()
	t.Setenv(sessionpressure.StorageVolumePathEnv, t.TempDir())
	storageCacheOwnerActive = func(id string) (bool, error) { return id == "go-build-cache", nil }
	cleanCalled := false
	storageRunGoBuildCacheClean = func(context.Context, string) error {
		cleanCalled = true
		return nil
	}
	runtime := pressureRuntime{dir: dir, policy: sessionpressure.DefaultPolicy(16 << 10)}
	provider := storageProviderReport{ID: "go-build-cache", Path: filepath.Join(t.TempDir(), "cache"), Classification: storageProviderOperator, MutationSupported: true}
	receipts, err := executeStorageProvider(t.Context(), runtime, provider)
	if err == nil || !strings.Contains(err.Error(), "active") || cleanCalled || len(receipts) != 2 || receipts[1].Outcome != "failed" {
		t.Fatalf("receipts=%+v cleanCalled=%v err=%v", receipts, cleanCalled, err)
	}
}

func TestExecuteGradleProviderRevalidatesActiveOwner(t *testing.T) {
	withStorageProviderFakes(t)
	dir := t.TempDir()
	t.Setenv(sessionpressure.StorageVolumePathEnv, t.TempDir())
	storageGradleActive = func() (bool, error) { return true, nil }
	cleanCalled := false
	storageRunGoBuildCacheClean = func(context.Context, string) error {
		cleanCalled = true
		return nil
	}
	runtime := pressureRuntime{dir: dir, policy: sessionpressure.DefaultPolicy(16 << 10)}
	provider := storageProviderReport{ID: "gradle-caches", Path: filepath.Join(t.TempDir(), "caches"), Classification: storageProviderOperator, MutationSupported: true}
	receipts, err := executeStorageProvider(t.Context(), runtime, provider)
	if err == nil || !strings.Contains(err.Error(), "active") || cleanCalled || len(receipts) != 2 || receipts[1].Outcome != "failed" {
		t.Fatalf("receipts=%+v cleanCalled=%v err=%v", receipts, cleanCalled, err)
	}
}

func TestRunGoBuildCacheCleanAtomicallyReplacesCacheDirectory(t *testing.T) {
	cache := filepath.Join(t.TempDir(), "go-build")
	if err := os.MkdirAll(filepath.Join(cache, "00"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cache, "00", "artifact"), []byte("cache"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := runGoBuildCacheClean(t.Context(), cache); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(cache)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("replacement cache entries=%v want empty", entries)
	}
	quarantines, err := filepath.Glob(cache + ".reclaim-*")
	if err != nil || len(quarantines) != 0 {
		t.Fatalf("quarantines=%v err=%v", quarantines, err)
	}
}

func TestRunGoBuildCacheCleanRestoresCacheWhenQuarantineRemovalFails(t *testing.T) {
	cache := filepath.Join(t.TempDir(), "go-build")
	artifact := filepath.Join(cache, "00", "artifact")
	if err := os.MkdirAll(filepath.Dir(artifact), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(artifact, []byte("cache"), 0o644); err != nil {
		t.Fatal(err)
	}
	originalRemoveAll := storageRemoveAll
	storageRemoveAll = func(path string) error {
		if strings.Contains(path, ".reclaim-") {
			return errors.New("simulated quarantine removal failure")
		}
		return os.RemoveAll(path)
	}
	t.Cleanup(func() { storageRemoveAll = originalRemoveAll })
	if err := runGoBuildCacheClean(t.Context(), cache); err == nil || !strings.Contains(err.Error(), "cache restored") {
		t.Fatalf("runGoBuildCacheClean error=%v, want restored-cache error", err)
	}
	if body, err := os.ReadFile(artifact); err != nil || string(body) != "cache" {
		t.Fatalf("restored artifact=%q err=%v", body, err)
	}
	quarantines, err := filepath.Glob(cache + ".reclaim-*")
	if err != nil || len(quarantines) != 0 {
		t.Fatalf("quarantines=%v err=%v", quarantines, err)
	}
}

func TestExecuteBrowserStorageProviderWritesIntentAndResult(t *testing.T) {
	withStorageProviderFakes(t)
	dir := t.TempDir()
	t.Setenv(sessionpressure.StorageVolumePathEnv, t.TempDir())
	storageDirectoryBytes = func(string) (int64, bool, error) { return 0, false, nil }
	applyCalls := 0
	storageBrowserGC = func(options browser.GCOptions) (*browser.GCReport, error) {
		if options.Apply {
			applyCalls++
			return &browser.GCReport{RemovedBytes: 2048}, nil
		}
		return &browser.GCReport{ReclaimableBytes: 2048}, nil
	}
	runtime := pressureRuntime{dir: dir, policy: sessionpressure.DefaultPolicy(16 << 10)}
	provider := storageProviderReport{ID: "browser-dead-profiles", Classification: storageProviderAutoSafe, MutationSupported: true, EstimatedBytes: 2048}
	receipts, err := executeStorageProvider(t.Context(), runtime, provider)
	if err != nil || applyCalls != 1 || len(receipts) != 2 || receipts[0].Mode != "intent" || receipts[1].Mode != "result" || receipts[1].Outcome != "completed" {
		t.Fatalf("receipts=%+v applyCalls=%d err=%v", receipts, applyCalls, err)
	}
	stored, err := sessionpressure.NewStorageReceiptStore(dir).Read(time.Time{}, 10)
	if err != nil || len(stored) != 2 {
		t.Fatalf("stored=%+v err=%v", stored, err)
	}
}

func TestExecutePNPMProviderRevalidatesOwnerAndRecordsFailure(t *testing.T) {
	withStorageProviderFakes(t)
	dir := t.TempDir()
	t.Setenv(sessionpressure.StorageVolumePathEnv, t.TempDir())
	storageDirectoryBytes = func(string) (int64, bool, error) { return 1024, true, nil }
	storagePNPMActive = func() (bool, error) { return true, nil }
	pruneCalled := false
	storageRunPNPMPrune = func(context.Context) error { pruneCalled = true; return nil }
	runtime := pressureRuntime{dir: dir, policy: sessionpressure.DefaultPolicy(16 << 10)}
	provider := storageProviderReport{ID: "pnpm-store", Path: filepath.Join(t.TempDir(), "store"), Classification: storageProviderAutoSafe, MutationSupported: true}
	receipts, err := executeStorageProvider(t.Context(), runtime, provider)
	if err == nil || !strings.Contains(err.Error(), "active") || pruneCalled || len(receipts) != 2 || receipts[1].Outcome != "failed" {
		t.Fatalf("receipts=%+v pruneCalled=%v err=%v", receipts, pruneCalled, err)
	}
}

func TestStorageApplyIsDryRunByDefault(t *testing.T) {
	withStorageProviderFakes(t)
	dir := t.TempDir()
	t.Setenv(sessionpressure.DataDirEnv, dir)
	t.Setenv(sessionpressure.StorageVolumePathEnv, t.TempDir())
	policy := sessionpressure.DefaultPolicy(16 << 10)
	if err := sessionpressure.SavePolicy(sessionpressure.PolicyPath(dir), policy); err != nil {
		t.Fatal(err)
	}
	storageDirectoryBytes = func(string) (int64, bool, error) { return 1024, true, nil }
	storagePNPMActive = func() (bool, error) { return false, nil }
	storageBrowserGC = func(browser.GCOptions) (*browser.GCReport, error) { return &browser.GCReport{}, nil }
	storageRunPNPMPrune = func(context.Context) error { return errors.New("must not run") }
	var exit int
	out := captureStdout(t, func() {
		exit = cmdSessionPressureStorage(&Flags{JSON: true}, []string{"apply", "--provider", "pnpm-store"})
	})
	if exit != 0 {
		t.Fatalf("exit=%d output=%s", exit, out)
	}
	var payload struct {
		Apply bool `json:"apply"`
		OK    bool `json:"ok"`
	}
	if err := json.Unmarshal([]byte(out), &payload); err != nil || !payload.OK || payload.Apply {
		t.Fatalf("payload=%+v err=%v output=%s", payload, err, out)
	}
}

func TestStorageApplyRequiresGoBuildHotnessEvidence(t *testing.T) {
	withStorageProviderFakes(t)
	dir := t.TempDir()
	home := t.TempDir()
	t.Setenv(sessionpressure.DataDirEnv, dir)
	t.Setenv(sessionpressure.StorageVolumePathEnv, t.TempDir())
	t.Setenv("HOME", home)
	if err := sessionpressure.SavePolicy(sessionpressure.PolicyPath(dir), sessionpressure.DefaultPolicy(16<<10)); err != nil {
		t.Fatal(err)
	}
	cache := filepath.Join(home, "Library", "Caches", "go-build")
	if err := os.MkdirAll(cache, 0o700); err != nil {
		t.Fatal(err)
	}
	storageDirectoryBytes = func(string) (int64, bool, error) { return 1024, true, nil }
	storagePNPMActive = func() (bool, error) { return false, nil }
	storageBrowserGC = func(browser.GCOptions) (*browser.GCReport, error) { return &browser.GCReport{}, nil }
	storageMeasureHotness = func(string, time.Time) (*storageProviderHotnessEvidence, error) { return nil, nil }
	cleanCalled := false
	storageRunGoBuildCacheClean = func(context.Context, string) error { cleanCalled = true; return nil }
	exit, stdout, stderr := captureMainOutput(t, func() int {
		return cmdSessionPressureStorage(&Flags{JSON: true}, []string{"apply", "--provider", "go-build-cache", "--target-free", "900GiB", "--apply"})
	})
	out := stdout + stderr
	if exit == 0 || cleanCalled || !strings.Contains(out, "hotness") {
		t.Fatalf("exit=%d cleanCalled=%v output=%s", exit, cleanCalled, out)
	}
	receipts, err := sessionpressure.NewStorageReceiptStore(dir).Read(time.Time{}, 10)
	if err != nil || len(receipts) != 0 {
		t.Fatalf("hotness rejection wrote receipts: %+v, %v", receipts, err)
	}
}

func TestStorageApplyUsesCurrentGoBuildHotnessEvidence(t *testing.T) {
	withStorageProviderFakes(t)
	dir := t.TempDir()
	home := t.TempDir()
	t.Setenv(sessionpressure.DataDirEnv, dir)
	t.Setenv(sessionpressure.StorageVolumePathEnv, t.TempDir())
	t.Setenv("HOME", home)
	if err := sessionpressure.SavePolicy(sessionpressure.PolicyPath(dir), sessionpressure.DefaultPolicy(16<<10)); err != nil {
		t.Fatal(err)
	}
	cache := filepath.Join(home, "Library", "Caches", "go-build")
	if err := os.MkdirAll(cache, 0o700); err != nil {
		t.Fatal(err)
	}
	storageBrowserGC = func(browser.GCOptions) (*browser.GCReport, error) { return &browser.GCReport{}, nil }
	measureCalls := 0
	storageMeasureHotness = func(path string, measuredAt time.Time) (*storageProviderHotnessEvidence, error) {
		measureCalls++
		if path != cache || measuredAt.IsZero() {
			t.Fatalf("hotness measurement path=%q time=%v", path, measuredAt)
		}
		return &storageProviderHotnessEvidence{Available: true, Complete: true}, nil
	}
	cleanCalls := 0
	storageRunGoBuildCacheClean = func(context.Context, string) error {
		cleanCalls++
		return nil
	}
	var exit int
	out := captureStdout(t, func() {
		exit = cmdSessionPressureStorage(&Flags{JSON: true}, []string{"apply", "--provider", "go-build-cache", "--target-free", "900GiB", "--apply"})
	})
	if exit != 0 || measureCalls != 1 || cleanCalls != 1 {
		t.Fatalf("exit=%d measureCalls=%d cleanCalls=%d output=%s", exit, measureCalls, cleanCalls, out)
	}
	receipts, err := sessionpressure.NewStorageReceiptStore(dir).Read(time.Time{}, 10)
	if err != nil || len(receipts) != 2 {
		t.Fatalf("successful hotness apply receipts=%+v err=%v", receipts, err)
	}
	seenIntent, seenCompleted := false, false
	for _, receipt := range receipts {
		seenIntent = seenIntent || receipt.Mode == "intent" && receipt.Outcome == "intent_persisted"
		seenCompleted = seenCompleted || receipt.Mode == "result" && receipt.Outcome == "completed"
	}
	if !seenIntent || !seenCompleted {
		t.Fatalf("successful hotness apply missing receipt modes: %+v", receipts)
	}
}

func TestStorageAutoSafeApplyRequiresEnabledPolicy(t *testing.T) {
	withStorageProviderFakes(t)
	dir := t.TempDir()
	t.Setenv(sessionpressure.DataDirEnv, dir)
	t.Setenv(sessionpressure.StorageVolumePathEnv, t.TempDir())
	if err := sessionpressure.SavePolicy(sessionpressure.PolicyPath(dir), sessionpressure.DefaultPolicy(16<<10)); err != nil {
		t.Fatal(err)
	}
	storageBrowserGC = func(browser.GCOptions) (*browser.GCReport, error) { return &browser.GCReport{}, nil }
	exit, stdout, stderr := captureMainOutput(t, func() int {
		return cmdSessionPressureStorage(&Flags{JSON: true}, []string{"apply", "--auto-safe", "--target-free", "900GiB", "--apply"})
	})
	out := stdout + stderr
	if exit == 0 || !strings.Contains(out, "explicit storage policy enable") {
		t.Fatalf("exit=%d output=%s", exit, out)
	}
}

func TestStorageApplyRejectsReportOnlyProviderEvenWhenTargetIsMet(t *testing.T) {
	withStorageProviderFakes(t)
	dir := t.TempDir()
	t.Setenv(sessionpressure.DataDirEnv, dir)
	t.Setenv(sessionpressure.StorageVolumePathEnv, t.TempDir())
	policy := sessionpressure.DefaultPolicy(16 << 10)
	if err := sessionpressure.SavePolicy(sessionpressure.PolicyPath(dir), policy); err != nil {
		t.Fatal(err)
	}
	storageDirectoryBytes = func(string) (int64, bool, error) { return 1024, true, nil }
	storagePNPMActive = func() (bool, error) { return false, nil }
	storageBrowserGC = func(browser.GCOptions) (*browser.GCReport, error) { return &browser.GCReport{}, nil }
	var exit int
	_ = captureStdout(t, func() {
		exit = cmdSessionPressureStorage(&Flags{JSON: true}, []string{"apply", "--provider", "mobile-sync", "--target-free", "1B", "--apply"})
	})
	if exit == 0 {
		t.Fatal("report-only provider was accepted")
	}
	receipts, err := sessionpressure.NewStorageReceiptStore(dir).Read(time.Time{}, 10)
	if err != nil || len(receipts) != 0 {
		t.Fatalf("report-only rejection wrote receipts: %+v, %v", receipts, err)
	}
}

func TestStorageApplyStopsAtSatisfiedTargetWithoutMutating(t *testing.T) {
	withStorageProviderFakes(t)
	dir := t.TempDir()
	t.Setenv(sessionpressure.DataDirEnv, dir)
	t.Setenv(sessionpressure.StorageVolumePathEnv, t.TempDir())
	policy := sessionpressure.DefaultPolicy(16 << 10)
	if err := sessionpressure.SavePolicy(sessionpressure.PolicyPath(dir), policy); err != nil {
		t.Fatal(err)
	}
	storageDirectoryBytes = func(string) (int64, bool, error) { return 1024, true, nil }
	storagePNPMActive = func() (bool, error) { return false, nil }
	applyCalls := 0
	storageBrowserGC = func(options browser.GCOptions) (*browser.GCReport, error) {
		if options.Apply {
			applyCalls++
		}
		return &browser.GCReport{ReclaimableBytes: 1024}, nil
	}
	var exit int
	out := captureStdout(t, func() {
		exit = cmdSessionPressureStorage(&Flags{JSON: true}, []string{"apply", "--provider", "browser-dead-profiles", "--target-free", "1B", "--apply"})
	})
	if exit != 0 || applyCalls != 0 {
		t.Fatalf("exit=%d applyCalls=%d output=%s", exit, applyCalls, out)
	}
	var payload struct {
		Receipts []sessionpressure.StorageReceipt `json:"receipts"`
	}
	if err := json.Unmarshal([]byte(out), &payload); err != nil || len(payload.Receipts) != 0 {
		t.Fatalf("payload=%+v err=%v output=%s", payload, err, out)
	}
}

func TestStorageProviderCooldownOnlyMatchesCompletedResultForProvider(t *testing.T) {
	receipts := []sessionpressure.StorageReceipt{
		{ProviderID: "browser-dead-profiles", Mode: "result", Outcome: "completed"},
		{ProviderID: "pnpm-store", Mode: "intent", Outcome: "intent_persisted"},
		{ProviderID: "pnpm-store", Mode: "result", Outcome: "failed"},
	}
	if !storageProviderInsideCooldown(receipts, "browser-dead-profiles") {
		t.Fatal("completed browser result did not activate cooldown")
	}
	if storageProviderInsideCooldown(receipts, "pnpm-store") {
		t.Fatal("intent or failed result incorrectly activated pnpm cooldown")
	}
}

func TestStoragePlanIncludesTypedOperatorProvidersAndGoHotness(t *testing.T) {
	withStorageProviderFakes(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	for _, path := range []string{
		filepath.Join(home, ".Trash"),
		filepath.Join(home, "Library", "Caches", "ShipIt-fixture"),
		filepath.Join(home, "Library", "Caches", "com.spotify.client"),
		filepath.Join(home, "Library", "Caches", "Homebrew"),
		filepath.Join(home, "Library", "Caches", "go-build"),
	} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	write := func(path string, size int, modified time.Time) {
		t.Helper()
		if err := os.WriteFile(path, []byte(strings.Repeat("x", size)), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, modified, modified); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now().UTC()
	write(filepath.Join(home, ".Trash", "old-item"), 11, now.Add(-10*24*time.Hour))
	write(filepath.Join(home, "Library", "Caches", "ShipIt-fixture", "payload"), 13, now.Add(-2*24*time.Hour))
	write(filepath.Join(home, "Library", "Caches", "com.spotify.client", "media"), 17, now.Add(-3*24*time.Hour))
	write(filepath.Join(home, "Library", "Caches", "Homebrew", "bottle"), 19, now.Add(-40*24*time.Hour))
	write(filepath.Join(home, "Library", "Caches", "go-build", "hot-artifact"), 23, now.Add(-24*time.Hour))
	write(filepath.Join(home, "Library", "Caches", "go-build", "cold-artifact"), 29, now.Add(-10*24*time.Hour))

	storageBrowserGC = func(browser.GCOptions) (*browser.GCReport, error) { return &browser.GCReport{}, nil }
	storagePNPMActive = func() (bool, error) { return false, nil }
	storageCacheOwnerActive = func(string) (bool, error) { return false, nil }
	storageGradleActive = func() (bool, error) { return false, nil }
	storageMeasureHotness = measureStorageHotness
	plan, err := buildStoragePlan(sessionpressure.DefaultPolicy(16<<10), sessionpressure.StorageSnapshot{Available: true, AvailableBytes: 1, TotalBytes: 100 << 30, Level: sessionpressure.LevelRed}, 1<<30)
	if err != nil {
		t.Fatal(err)
	}
	if plan.EstimatedBytes <= 0 {
		t.Fatalf("plan estimated=%d, expected typed operator reclaim", plan.EstimatedBytes)
	}
	for _, id := range []string{"user-trash", "app-updater-caches", "browser-media-caches", "brew-cache"} {
		provider, ok := storageProviderByID(plan.Providers, id)
		if !ok || !provider.Present || !provider.MutationSupported || provider.EstimatedBytes <= 0 {
			t.Fatalf("provider %s = %+v", id, provider)
		}
	}
	goProvider, ok := storageProviderByID(plan.Providers, "go-build-cache")
	if !ok || goProvider.HotnessEvidence == nil || !goProvider.HotnessEvidence.Available || !goProvider.HotnessEvidence.Complete {
		t.Fatalf("go hotness evidence missing: %+v", goProvider)
	}
	if goProvider.HotnessEvidence.HotBytes == 0 || goProvider.HotnessEvidence.ColdEligibleBytes == 0 || goProvider.HotnessEvidence.HotFraction <= 0 || goProvider.HotnessEvidence.HotFraction >= 1 {
		t.Fatalf("unexpected go hotness evidence: %+v", goProvider.HotnessEvidence)
	}
	if len(goProvider.HotnessEvidence.AgeBuckets) != 4 || plan.ColdEligibleBytes == 0 || plan.ColdMeetsShortfall {
		t.Fatalf("unexpected cold evidence plan=%+v provider=%+v", plan, goProvider.HotnessEvidence)
	}
}

func TestStorageProviderMutatorsStayWithinAllowlistedRoots(t *testing.T) {
	withStorageProviderFakes(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	trash := filepath.Join(home, ".Trash")
	outside := filepath.Join(home, "outside-trash-sibling")
	if err := os.MkdirAll(trash, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(trash, "discard-me"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outside, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := emptyUserTrash(t.Context(), trash); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(trash, "discard-me")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Trash item remains or returned unexpected error: %v", err)
	}
	if _, err := os.Stat(outside); err != nil {
		t.Fatalf("sibling outside Trash was touched: %v", err)
	}

	cacheRoot := filepath.Join(home, "Library", "Caches")
	if err := os.MkdirAll(filepath.Join(cacheRoot, "ShipIt-fixture"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(cacheRoot, "keep-me"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := cleanAppUpdaterCaches(t.Context(), cacheRoot); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(cacheRoot, "ShipIt-fixture")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("updater cache remains or returned unexpected error: %v", err)
	}
	if _, err := os.Stat(filepath.Join(cacheRoot, "keep-me")); err != nil {
		t.Fatalf("unmatched cache was touched: %v", err)
	}

	mediaPaths := browserMediaCachePaths(home)
	if err := os.MkdirAll(mediaPaths[0], 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mediaPaths[0], "media"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := cleanBrowserMediaCaches(t.Context(), mediaPaths[:1]); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(mediaPaths[0]); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("media cache remains or returned unexpected error: %v", err)
	}
	if err := cleanBrowserMediaCaches(t.Context(), []string{filepath.Join(home, "Library", "Application Support", "BraveSoftware", "Brave-Browser", "Default", "Login Data")}); err == nil {
		t.Fatal("browser password-store path passed media-cache allowlist")
	}
}

func TestStorageProviderExecutorsWriteReceiptsForNewProviders(t *testing.T) {
	withStorageProviderFakes(t)
	dir := t.TempDir()
	t.Setenv(sessionpressure.StorageVolumePathEnv, t.TempDir())
	runCalls := map[string]int{}
	storageEmptyTrash = func(context.Context, string) error { runCalls["user-trash"]++; return nil }
	storageCleanUpdaterCaches = func(context.Context, string) error { runCalls["app-updater-caches"]++; return nil }
	storageCleanBrowserMediaCaches = func(context.Context, []string) error { runCalls["browser-media-caches"]++; return nil }
	storageRunDockerPrune = func(context.Context) error { runCalls["docker-reclaim"]++; return nil }
	runtime := pressureRuntime{dir: dir, policy: sessionpressure.DefaultPolicy(16 << 10)}
	providers := []storageProviderReport{
		{ID: "user-trash", Path: filepath.Join(t.TempDir(), ".Trash"), Classification: storageProviderOperator, MutationSupported: true},
		{ID: "app-updater-caches", Path: filepath.Join(t.TempDir(), "Caches"), Classification: storageProviderOperator, MutationSupported: true},
		{ID: "browser-media-caches", Paths: []string{filepath.Join(t.TempDir(), "media")}, Classification: storageProviderOperator, MutationSupported: true},
		{ID: "docker-reclaim", Classification: storageProviderOperator, MutationSupported: true},
	}
	for _, provider := range providers {
		receipts, err := executeStorageProvider(t.Context(), runtime, provider)
		if err != nil || len(receipts) != 2 || receipts[0].Mode != "intent" || receipts[1].Mode != "result" || receipts[1].Outcome != "completed" {
			t.Fatalf("provider %s receipts=%+v err=%v", provider.ID, receipts, err)
		}
	}
	for _, provider := range providers {
		if runCalls[provider.ID] != 1 {
			t.Fatalf("provider %s calls=%d", provider.ID, runCalls[provider.ID])
		}
	}
}

func TestDockerReclaimEstimateExcludesVolumes(t *testing.T) {
	got, err := parseDockerSystemDFOutput([]byte("{\"Type\":\"Images\",\"Reclaimable\":\"1.5GB (50%)\"}\n{\"Type\":\"Build Cache\",\"Reclaimable\":\"512MiB\"}\n{\"Type\":\"Containers\",\"Reclaimable\":\"0B\"}\n{\"Type\":\"Volumes\",\"Reclaimable\":\"20GB\"}\n"))
	if err != nil {
		t.Fatal(err)
	}
	want := int64(1_500_000_000) + 512<<20
	if got != want {
		t.Fatalf("docker reclaim estimate=%d, want %d", got, want)
	}
}
