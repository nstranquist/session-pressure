package sessionpressurecmd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func retentionTouch(t *testing.T, path string, modTime time.Time, dir bool) {
	t.Helper()
	if dir {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(path, "payload"), []byte("payload"), 0o600); err != nil {
			t.Fatal(err)
		}
	} else if err := os.WriteFile(path, []byte("binary"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, modTime, modTime); err != nil {
		t.Fatal(err)
	}
}

func retentionSHA(n int) string {
	return fmt.Sprintf("sha256-%064x", n+1)
}

func withRetentionFakes(t *testing.T, now time.Time, args string) {
	t.Helper()
	originalArgs, originalNow, originalClean, originalAlive, originalEnv := storageProcessArgs, storageRetentionNow, storageCleanVCSBuild, storageProcessAlive, storageGoEnv
	storageProcessArgs = func() ([]byte, error) { return []byte(args), nil }
	storageRetentionNow = func() time.Time { return now }
	storageCleanVCSBuild = func(string) bool { return false }
	storageProcessAlive = func(int) bool { return false }
	storageGoEnv = func() (string, string, error) { return "", "", nil }
	t.Cleanup(func() {
		storageProcessArgs, storageRetentionNow, storageCleanVCSBuild, storageProcessAlive, storageGoEnv = originalArgs, originalNow, originalClean, originalAlive, originalEnv
	})
}

func candidatePaths(evidence storageRetentionEvidence) map[string]bool {
	out := map[string]bool{}
	for _, candidate := range evidence.candidates() {
		out[filepath.Base(candidate.Path)] = true
	}
	return out
}

func TestBinaryRevisionsNeverSelectsCurrentOrPins(t *testing.T) {
	home := t.TempDir()
	now := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	store := filepath.Join(home, ".local", "bin", ".ndev.revisions")
	if err := os.MkdirAll(store, 0o700); err != nil {
		t.Fatal(err)
	}
	// Eight revisions, one day apart; index 0 is the newest.
	for i := 0; i < 8; i++ {
		retentionTouch(t, filepath.Join(store, retentionSHA(i)), now.Add(-time.Duration(i+1)*24*time.Hour), false)
	}
	// current points at the oldest revision; a launchd agent pins index 6;
	// a running process names index 5.
	if err := os.Symlink(filepath.Join(store, retentionSHA(7)), filepath.Join(store, "current")); err != nil {
		t.Fatal(err)
	}
	agents := filepath.Join(home, "Library", "LaunchAgents")
	if err := os.MkdirAll(agents, 0o700); err != nil {
		t.Fatal(err)
	}
	plist := "<string>" + filepath.Join(store, retentionSHA(6)) + "</string>"
	if err := os.WriteFile(filepath.Join(agents, "com.example.plist.bak"), []byte(plist), 0o600); err != nil {
		t.Fatal(err)
	}
	withRetentionFakes(t, now, filepath.Join(store, retentionSHA(5))+" --serve\n")
	// Ignore junk names and a non-revision file.
	retentionTouch(t, filepath.Join(store, "notes.txt"), now.Add(-90*24*time.Hour), false)

	evidence, err := planBinaryRevisions(home, 2*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	got := candidatePaths(evidence)
	want := map[string]bool{retentionSHA(3): true, retentionSHA(4): true}
	if len(got) != len(want) {
		t.Fatalf("candidates = %v, want %v", got, want)
	}
	for name := range want {
		if !got[name] {
			t.Fatalf("candidates = %v, want %v", got, want)
		}
	}
	if evidence.CandidateBytes <= 0 {
		t.Fatalf("candidate bytes not measured: %+v", evidence)
	}
	if reason := evidence.Stores[0].Protected[filepath.Join(store, retentionSHA(7))]; reason != "link:current" {
		t.Fatalf("current protection = %q", reason)
	}
}

func TestBinaryRevisionsKeepsYoungAndNewestCleanBuild(t *testing.T) {
	home := t.TempDir()
	now := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	store := filepath.Join(home, ".local", "bin", ".wip.revisions")
	if err := os.MkdirAll(store, 0o700); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 6; i++ {
		retentionTouch(t, filepath.Join(store, retentionSHA(i)), now.Add(-time.Duration(i+1)*time.Minute), false)
	}
	withRetentionFakes(t, now, "")
	// Everything is younger than the min age: nothing is a candidate.
	evidence, err := planBinaryRevisions(home, 2*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if evidence.CandidateCount != 0 {
		t.Fatalf("young revisions selected: %v", candidatePaths(evidence))
	}
	storageCleanVCSBuild = func(path string) bool {
		return strings.HasSuffix(path, retentionSHA(4)) || strings.HasSuffix(path, retentionSHA(5))
	}
	evidence, err = planBinaryRevisions(home, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	got := candidatePaths(evidence)
	if len(got) != 2 || !got[retentionSHA(3)] || !got[retentionSHA(5)] {
		t.Fatalf("candidates = %v, want sha 3 and the older clean sha 5", got)
	}
}

func TestSessionPressureReleasesKeepLinkedDirectories(t *testing.T) {
	home := t.TempDir()
	now := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	store := filepath.Join(home, ".local", "bin", ".session-pressure-releases")
	names := make([]string, 6)
	for i := range names {
		names[i] = strings.Repeat(string(rune('a'+i)), 40)
		retentionTouch(t, filepath.Join(store, names[i]), now.Add(-time.Duration(i+1)*24*time.Hour), true)
	}
	if err := os.Symlink(names[5], filepath.Join(store, "current")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(store, names[4]), filepath.Join(store, "previous-20261111T000000Z")); err != nil {
		t.Fatal(err)
	}
	withRetentionFakes(t, now, "")
	evidence, err := planBinaryRevisions(home, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	got := candidatePaths(evidence)
	if len(got) != 1 || !got[names[3]] {
		t.Fatalf("candidates = %v, want only %s", got, names[3])
	}
}

func TestTursoBackupsKeepNewestTwoAndPinned(t *testing.T) {
	root := t.TempDir()
	now := time.Now()
	for _, day := range []string{"2026-11-08", "2026-11-09", "2026-11-10", "2026-11-07"} {
		retentionTouch(t, filepath.Join(root, day), now, true)
	}
	retentionTouch(t, filepath.Join(root, "pinned"), now.Add(-365*24*time.Hour), true)
	evidence := planTursoBackups(root, 2)
	got := candidatePaths(evidence)
	if len(got) != 2 || !got["2026-11-07"] || !got["2026-11-08"] {
		t.Fatalf("candidates = %v", got)
	}
}

func TestGoBuildOrphansSkipsLiveActiveAndUnsuffixed(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	old := now.Add(-48 * time.Hour)
	for _, name := range []string{"go-build", "go-build-capped", "go-build-cli-111", "go-build-serve-222", "go-build-timeline-clean", "go-build-fresh-333"} {
		retentionTouch(t, filepath.Join(root, name), old, true)
	}
	retentionTouch(t, filepath.Join(root, "go-build-fresh-333"), now.Add(-time.Minute), true)
	withRetentionFakes(t, now, "")
	storageProcessAlive = func(pid int) bool { return pid == 222 }
	evidence := planGoBuildOrphans(root, []string{filepath.Join(root, "go-build"), filepath.Join(root, "go-build-capped")}, now, time.Hour)
	got := candidatePaths(evidence)
	if len(got) != 1 || !got["go-build-cli-111"] {
		t.Fatalf("candidates = %v", got)
	}
}

func TestResolveGoCachesReadsGocacheLRUFlags(t *testing.T) {
	withRetentionFakes(t, time.Now(), "")
	storageGoEnv = func() (string, string, error) {
		return "/x/go-build", "/home/bin/gocache-lru -max 8GiB -dir=/x/capped", nil
	}
	gocache, dir, max, err := resolveGoCaches("/home")
	if err != nil || gocache != "/x/go-build" || dir != "/x/capped" || max != "8GiB" {
		t.Fatalf("got %q %q %q %v", gocache, dir, max, err)
	}
	storageGoEnv = func() (string, string, error) { return "", "/home/bin/gocache-lru", nil }
	gocache, dir, max, _ = resolveGoCaches("/home")
	if gocache != "/home/Library/Caches/go-build" || dir != "/home/Library/Caches/go-build-capped" || max != "16GiB" {
		t.Fatalf("defaults: %q %q %q", gocache, dir, max)
	}
}

func TestExecuteRetentionRemovesOnlyFreshCandidates(t *testing.T) {
	home := t.TempDir()
	now := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	root := filepath.Join(home, ".local", "state", "turso-backups")
	for _, day := range []string{"2026-11-08", "2026-11-09", "2026-11-10"} {
		retentionTouch(t, filepath.Join(root, day), now, true)
	}
	retentionTouch(t, filepath.Join(root, "pinned"), now, true)
	withRetentionFakes(t, now, "")
	if err := executeRetentionProvider(context.Background(), "turso-backups", home, 7200); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"2026-11-09", "2026-11-10", "pinned"} {
		if _, err := os.Stat(filepath.Join(root, name)); err != nil {
			t.Fatalf("%s removed: %v", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "2026-11-08")); !os.IsNotExist(err) {
		t.Fatalf("2026-11-08 kept: %v", err)
	}
	if err := executeRetentionProvider(context.Background(), "user-trash", home, 7200); err == nil {
		t.Fatal("non-retention provider accepted")
	}
}

func TestDeliveryGoCacheTrimReceipt(t *testing.T) {
	dir := t.TempDir()
	receipt := filepath.Join(dir, "trim.json")
	if err := os.WriteFile(receipt, []byte(`{"dir":"/a/go-build-cache","after_bytes":42}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, ok := readDeliveryGoCacheTrimBytes(receipt, "/a/go-build-cache"); !ok || got != 42 {
		t.Fatalf("got %d %v", got, ok)
	}
	if _, ok := readDeliveryGoCacheTrimBytes(receipt, "/b/go-build-cache"); ok {
		t.Fatal("receipt for another dir accepted")
	}
}
