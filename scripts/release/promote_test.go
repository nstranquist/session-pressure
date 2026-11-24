package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestPromoteDryRunCapturesAuthoritiesEvenWithDirtyWorkingTree(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	repo := t.TempDir()
	runGit := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=Test",
			"GIT_AUTHOR_EMAIL=test@example.com",
			"GIT_COMMITTER_NAME=Test",
			"GIT_COMMITTER_EMAIL=test@example.com",
		)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	runGit("init", "-b", "main")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("hello\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit("add", "README.md")
	runGit("commit", "-m", "initial")
	head := runGit("rev-parse", "HEAD")

	// Simulate an unrelated peer editing a file in the checkout.
	if err := os.WriteFile(filepath.Join(repo, "dirty-peer.txt"), []byte("wip\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	prefix := filepath.Join(t.TempDir(), "bin")
	receipt, err := runPromote(promoteOptions{
		Repo:        repo,
		Ref:         "HEAD",
		Prefix:      prefix,
		Destination: filepath.Join(t.TempDir(), "NDev Pressure.app"),
		SkipApp:     true,
		DryRun:      true,
	})
	if err != nil {
		t.Fatalf("dry-run promote failed on dirty checkout: %v", err)
	}
	if !receipt.OK || !receipt.DryRun || !receipt.Headless {
		t.Fatalf("unexpected receipt flags: %+v", receipt)
	}
	if receipt.TargetRevision != head || receipt.ExpectedCurrent != "none" {
		t.Fatalf("unexpected target/authority: %+v", receipt)
	}

	// Verify cloneCleanRevision isolates committed HEAD cleanly without dirty-peer.txt.
	cleanDir := filepath.Join(t.TempDir(), "clean")
	if err := cloneCleanRevision(repo, head, cleanDir); err != nil {
		t.Fatalf("cloneCleanRevision failed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(cleanDir, "dirty-peer.txt")); !os.IsNotExist(err) {
		t.Fatalf("dirty working tree file leaked into clean clone")
	}
}

func TestReadCLIAuthorityReadsActiveLinkOrNone(t *testing.T) {
	prefix := t.TempDir()
	got, err := readCLIAuthority(prefix)
	if err != nil || got != "none" {
		t.Fatalf("first install authority got=%q err=%v, want none", got, err)
	}
	rev := strings.Repeat("a", 40)
	store := filepath.Join(prefix, ".session-pressure-releases")
	if err := os.MkdirAll(store, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(rev, filepath.Join(store, "current")); err != nil {
		t.Fatal(err)
	}
	got, err = readCLIAuthority(prefix)
	if err != nil || got != rev {
		t.Fatalf("active authority got=%q err=%v, want %q", got, err, rev)
	}
}
