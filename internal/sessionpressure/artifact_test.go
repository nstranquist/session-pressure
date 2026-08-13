package sessionpressure

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPromoteArtifactPinsReadOnlyContentAddressedRevision(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "candidate", "ndev-session-pressure")
	if err := os.MkdirAll(filepath.Dir(source), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, []byte("revision-one"), 0o755); err != nil {
		t.Fatal(err)
	}
	installedAt := time.Date(2026, 7, 13, 20, 0, 0, 0, time.UTC)
	first, err := promoteArtifact(source, filepath.Join(dir, "runtime"), installedAt)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(first.Path, "sha256-"+first.SHA256) || !first.InstalledAt.Equal(installedAt) {
		t.Fatalf("unexpected first artifact: %+v", first)
	}
	info, err := os.Stat(first.Path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != artifactFileMode {
		t.Fatalf("artifact mode=%v", info.Mode().Perm())
	}

	if err := os.WriteFile(source, []byte("revision-two"), 0o755); err != nil {
		t.Fatal(err)
	}
	oldBody, err := os.ReadFile(first.Path)
	if err != nil || string(oldBody) != "revision-one" {
		t.Fatalf("candidate rebuild mutated pinned artifact: body=%q err=%v", oldBody, err)
	}
	second, err := promoteArtifact(source, filepath.Join(dir, "runtime"), installedAt.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if second.Path == first.Path || second.SHA256 == first.SHA256 {
		t.Fatalf("new content reused old content address: first=%+v second=%+v", first, second)
	}
	manifest, found, err := LoadInstalledArtifact(filepath.Join(dir, "runtime"))
	if err != nil || !found || manifest != second {
		t.Fatalf("manifest=%+v found=%v err=%v", manifest, found, err)
	}
}

func TestLaunchdStatusRejectsCorruptedArtifact(t *testing.T) {
	home := t.TempDir()
	dataDir := filepath.Join(home, "data")
	source := filepath.Join(home, "candidate")
	if err := os.WriteFile(source, []byte("known-good"), 0o755); err != nil {
		t.Fatal(err)
	}
	artifact, err := promoteArtifact(source, dataDir, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(artifact.Path, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(artifact.Path, []byte("corrupted"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(artifact.Path, artifactFileMode); err != nil {
		t.Fatal(err)
	}
	manager := &LaunchdManager{Home: home, DataDir: dataDir}
	if err := os.MkdirAll(filepath.Dir(manager.PlistPath()), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manager.PlistPath(), []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	manager.Launchctl = func(context.Context, ...string) ([]byte, error) {
		return []byte("state = running\n\tpid = 123\n"), nil
	}
	status := manager.Status(context.Background())
	if status.OK || !status.ArtifactPresent || status.ArtifactVerified || !strings.Contains(status.Detail, "digest") {
		t.Fatalf("corrupted artifact reported healthy: %+v", status)
	}
}

func TestLaunchdStatusRequiresExactArtifactMode(t *testing.T) {
	home := t.TempDir()
	dataDir := filepath.Join(home, "data")
	source := filepath.Join(home, "candidate")
	if err := os.WriteFile(source, []byte("known-good"), 0o755); err != nil {
		t.Fatal(err)
	}
	artifact, err := promoteArtifact(source, dataDir, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(artifact.Path, 0o755); err != nil {
		t.Fatal(err)
	}
	manager := &LaunchdManager{Home: home, DataDir: dataDir}
	if err := os.MkdirAll(filepath.Dir(manager.PlistPath()), 0o700); err != nil {
		t.Fatal(err)
	}
	plist := renderLaunchdPlist(artifact.Path, manager.ControlBinary, strings.Repeat("a", 64), home, dataDir, manager.stdoutPath(), manager.stderrPath())
	if err := os.WriteFile(manager.PlistPath(), []byte(plist), 0o644); err != nil {
		t.Fatal(err)
	}
	manager.Launchctl = func(context.Context, ...string) ([]byte, error) {
		return []byte("state = running\n\tpid = 123\n"), nil
	}
	status := manager.Status(context.Background())
	if status.OK || !status.ArtifactPresent || status.ArtifactVerified || !strings.Contains(status.Detail, "mode is 0755, want 0555") {
		t.Fatalf("permissive artifact mode reported healthy: %+v", status)
	}
}

func TestLaunchdStatusRequiresPlistParityWithManifest(t *testing.T) {
	home := t.TempDir()
	dataDir := filepath.Join(home, "data")
	source := filepath.Join(home, "candidate")
	if err := os.WriteFile(source, []byte("known-good"), 0o755); err != nil {
		t.Fatal(err)
	}
	artifact, err := promoteArtifact(source, dataDir, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	manager := &LaunchdManager{Home: home, DataDir: dataDir, ControlBinary: source}
	if err := os.MkdirAll(filepath.Dir(manager.PlistPath()), 0o700); err != nil {
		t.Fatal(err)
	}
	controlDigest, digestErr := ControlBinarySHA256(source)
	if digestErr != nil {
		t.Fatal(digestErr)
	}
	stalePlist := renderLaunchdPlist(artifact.Path, source, controlDigest, home, dataDir, manager.stdoutPath()+".stale", manager.stderrPath())
	if err := os.WriteFile(manager.PlistPath(), []byte(stalePlist), 0o644); err != nil {
		t.Fatal(err)
	}
	manager.Launchctl = func(context.Context, ...string) ([]byte, error) {
		return []byte("state = running\n\tpid = 123\n"), nil
	}
	status := manager.Status(context.Background())
	if status.OK || !status.ArtifactVerified || !strings.Contains(status.Detail, "plist does not match") {
		t.Fatalf("stale plist reported healthy: %+v", status)
	}
}

func TestLaunchdStatusRejectsCleanupControlBinaryDigestDrift(t *testing.T) {
	home := t.TempDir()
	dataDir := filepath.Join(home, "data")
	resident := filepath.Join(home, "resident")
	control := filepath.Join(home, "ndev")
	if err := os.WriteFile(resident, []byte("resident-fixture"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(control, []byte("control-revision-a"), 0o755); err != nil {
		t.Fatal(err)
	}
	artifact, err := promoteArtifact(resident, dataDir, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	controlDigest, err := ControlBinarySHA256(control)
	if err != nil {
		t.Fatal(err)
	}
	manager := &LaunchdManager{Home: home, DataDir: dataDir, ControlBinary: control}
	if err := os.MkdirAll(filepath.Dir(manager.PlistPath()), 0o700); err != nil {
		t.Fatal(err)
	}
	plist := renderLaunchdPlist(artifact.Path, control, controlDigest, home, dataDir, manager.stdoutPath(), manager.stderrPath())
	if err := os.WriteFile(manager.PlistPath(), []byte(plist), 0o644); err != nil {
		t.Fatal(err)
	}
	manager.Launchctl = func(context.Context, ...string) ([]byte, error) {
		return []byte("state = running\n\tpid = 123\n"), nil
	}
	if status := manager.Status(context.Background()); !status.OK || !status.ControlBinaryVerified {
		t.Fatalf("verified cleanup controller reported unhealthy: %+v", status)
	}
	if err := os.WriteFile(control, []byte("control-revision-b"), 0o755); err != nil {
		t.Fatal(err)
	}
	status := manager.Status(context.Background())
	if status.OK || status.ControlBinaryVerified || !strings.Contains(status.Detail, "digest mismatch") {
		t.Fatalf("changed cleanup controller reported healthy: %+v", status)
	}
}

func TestLoadInstalledArtifactRejectsPathOutsideContentAddress(t *testing.T) {
	dir := t.TempDir()
	artifact := InstalledArtifact{
		SchemaVersion: artifactManifestSchemaVersion,
		SHA256:        strings.Repeat("a", 64),
		Path:          filepath.Join(dir, "unrelated-binary"),
		InstalledAt:   time.Now().UTC(),
	}
	body, err := json.Marshal(artifact)
	if err != nil {
		t.Fatal(err)
	}
	if err := atomicWrite(ArtifactManifestPath(dir), body, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, found, err := LoadInstalledArtifact(dir); !found || err == nil {
		t.Fatalf("tampered manifest found=%v err=%v", found, err)
	}
}

func TestLaunchdStatusRejectsArtifactSymlink(t *testing.T) {
	home := t.TempDir()
	dataDir := filepath.Join(home, "data")
	source := filepath.Join(home, "candidate")
	if err := os.WriteFile(source, []byte("known-good"), 0o755); err != nil {
		t.Fatal(err)
	}
	artifact, err := promoteArtifact(source, dataDir, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(artifact.Path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(source, artifact.Path); err != nil {
		t.Fatal(err)
	}
	manager := &LaunchdManager{Home: home, DataDir: dataDir}
	if err := os.MkdirAll(filepath.Dir(manager.PlistPath()), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manager.PlistPath(), []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	manager.Launchctl = func(context.Context, ...string) ([]byte, error) {
		return []byte("state = running\n\tpid = 123\n"), nil
	}
	status := manager.Status(context.Background())
	if status.OK || status.ArtifactPresent || status.ArtifactVerified || !strings.Contains(status.Detail, "regular file") {
		t.Fatalf("symlinked artifact reported healthy: %+v", status)
	}
}

func TestLoadInstalledArtifactAcceptsSchemaOneManifest(t *testing.T) {
	dir := t.TempDir()
	digest := strings.Repeat("a", 64)
	artifact := InstalledArtifact{
		SchemaVersion: 1,
		SHA256:        digest,
		Path:          filepath.Join(dir, "artifacts", "sha256-"+digest, "ndev-session-pressure"),
		InstalledAt:   time.Now().UTC(),
	}
	body, err := json.Marshal(artifact)
	if err != nil {
		t.Fatal(err)
	}
	if err := atomicWrite(ArtifactManifestPath(dir), body, 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, found, err := LoadInstalledArtifact(dir)
	if err != nil || !found || loaded.SchemaVersion != 1 {
		t.Fatalf("schema-one manifest loaded=%+v found=%v err=%v", loaded, found, err)
	}
}

func TestPruneArtifactsKeepsActiveAndTwoVerifiedRollbacks(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "candidate")
	var artifacts []InstalledArtifact
	base := time.Date(2026, 7, 21, 20, 0, 0, 0, time.UTC)
	for index := 0; index < 5; index++ {
		if err := os.WriteFile(source, []byte(fmt.Sprintf("revision-%d", index)), 0o755); err != nil {
			t.Fatal(err)
		}
		artifact, err := promoteArtifact(source, dir, base.Add(time.Duration(index)*time.Minute))
		if err != nil {
			t.Fatal(err)
		}
		modified := base.Add(time.Duration(index) * time.Minute)
		if err := os.Chtimes(artifact.Path, modified, modified); err != nil {
			t.Fatal(err)
		}
		artifacts = append(artifacts, artifact)
	}

	plan, err := PruneArtifacts(context.Background(), dir, false)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Applied || plan.PruneCount != 2 || plan.PrunedCount != 0 || plan.ReclaimBytes == 0 {
		t.Fatalf("unexpected artifact prune plan: %+v", plan)
	}
	for _, artifact := range artifacts {
		if _, err := os.Stat(artifact.Path); err != nil {
			t.Fatalf("dry-run removed %s: %v", artifact.Path, err)
		}
	}

	applied, err := PruneArtifacts(context.Background(), dir, true)
	if err != nil {
		t.Fatal(err)
	}
	if !applied.Applied || applied.PrunedCount != 2 || applied.ReclaimedBytes != applied.ReclaimBytes {
		t.Fatalf("unexpected applied report: %+v", applied)
	}
	for index, artifact := range artifacts {
		_, err := os.Stat(artifact.Path)
		if index < 2 && !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("old artifact %d was not pruned: %v", index, err)
		}
		if index >= 2 && err != nil {
			t.Fatalf("retained artifact %d missing: %v", index, err)
		}
	}
}

func TestPruneArtifactsSkipsUnexpectedDirectoryContents(t *testing.T) {
	dir := t.TempDir()
	digest := strings.Repeat("b", 64)
	artifactDir := filepath.Join(dir, "artifacts", "sha256-"+digest)
	if err := os.MkdirAll(artifactDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(artifactDir, "unexpected"), []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	report, err := PruneArtifacts(context.Background(), dir, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Entries) != 1 || report.Entries[0].SkipReason == "" || report.PrunedCount != 0 {
		t.Fatalf("unexpected unsafe-directory report: %+v", report)
	}
	if _, err := os.Stat(filepath.Join(artifactDir, "unexpected")); err != nil {
		t.Fatalf("unsafe directory was mutated: %v", err)
	}
}
