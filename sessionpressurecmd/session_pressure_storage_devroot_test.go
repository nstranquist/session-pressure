package sessionpressurecmd

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func devRootTestGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.invalid", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.invalid", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func devRootWrite(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// devRootOwnerLayout is a repository with enough top-level names for the
// layout match (at least five shared names).
var devRootOwnerLayout = map[string]string{
	"README.md": "readme\n", "go.mod": "module x\n", "main.go": "package main\n",
	"docs/guide.md": "guide\n", "cmd/tool/main.go": "package main\n", "Makefile": "all:\n",
	".gitignore": "cache/\n",
}

func devRootTestOwner(t *testing.T, root string) string {
	t.Helper()
	owner := filepath.Join(root, "owner")
	for rel, body := range devRootOwnerLayout {
		devRootWrite(t, filepath.Join(owner, rel), body)
	}
	devRootTestGit(t, root, "init", "-q", "-b", "main", owner)
	devRootTestGit(t, owner, "add", "-A")
	devRootTestGit(t, owner, "commit", "-q", "-m", "base")
	return owner
}

func devRootCopy(t *testing.T, dst string) {
	t.Helper()
	for rel, body := range devRootOwnerLayout {
		devRootWrite(t, filepath.Join(dst, rel), body)
	}
}

func devRootEntryByName(evidence storageDevRootEvidence, name string) (storageDevRootEntry, bool) {
	for _, entry := range evidence.Entries {
		if entry.Name == name {
			return entry, true
		}
	}
	return storageDevRootEntry{}, false
}

func TestDevRootLeftoversProveNeverCommittedContent(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	withStorageProviderFakes(t)
	storageDirectoryBytes = func(path string) (int64, bool, error) { return 100, true, nil }
	root := t.TempDir()
	owner := devRootTestOwner(t, root)

	// A clean mktemp copy: only committed content plus an ignored cache file.
	clean := filepath.Join(root, ".catalog-proof.sCiWK1")
	devRootCopy(t, clean)
	devRootWrite(t, filepath.Join(clean, "cache", "derived.bin"), "derived\n")

	// A copy with one file that was never committed anywhere.
	dirty := filepath.Join(root, "owner-gocache.Je4Soy")
	devRootCopy(t, dirty)
	devRootWrite(t, filepath.Join(dirty, "docs", "new-finding.md"), "unique work\n")

	// An orphaned worktree: its admin dir is gone.
	orphan := filepath.Join(root, "owner-final-91473c")
	devRootCopy(t, orphan)
	devRootWrite(t, filepath.Join(orphan, ".git"), "gitdir: "+filepath.Join(root, "deleted-repo", ".git", "worktrees", "owner-final-91473c")+"\n")

	// A local clone with one commit that the owner does not have.
	clone := filepath.Join(root, "owner-fix-20260824")
	devRootTestGit(t, root, "clone", "-q", owner, clone)
	devRootWrite(t, filepath.Join(clone, "fix.go"), "package fix\n")
	devRootTestGit(t, clone, "add", "fix.go")
	devRootTestGit(t, clone, "commit", "-q", "-m", "fix")

	// Real projects and house directories are never listed.
	devRootTestGit(t, root, "init", "-q", filepath.Join(root, "real-project"))
	if err := os.MkdirAll(filepath.Join(root, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	devRootWrite(t, filepath.Join(root, "notes.backup"), "not random\n")

	evidence, err := inspectDevRootLeftovers(context.Background(), root, true)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, entry := range evidence.Entries {
		names = append(names, entry.Name)
	}
	want := []string{".catalog-proof.sCiWK1", "owner-final-91473c", "owner-fix-20260824", "owner-gocache.Je4Soy"}
	if !slices.Equal(names, want) {
		t.Fatalf("entries = %v, want %v", names, want)
	}

	cleanEntry, _ := devRootEntryByName(evidence, ".catalog-proof.sCiWK1")
	if cleanEntry.Reason != devRootReasonShellMktemp || cleanEntry.Owner != owner || cleanEntry.OwnerBasis != "layout-match" ||
		cleanEntry.Verdict != devRootVerdictNoUniqueWork || cleanEntry.NeverCommittedFiles != 0 || cleanEntry.IgnoredFiles != 1 {
		t.Fatalf("clean copy = %+v", cleanEntry)
	}

	dirtyEntry, _ := devRootEntryByName(evidence, "owner-gocache.Je4Soy")
	if dirtyEntry.Verdict != devRootVerdictReview || dirtyEntry.NeverCommittedFiles != 1 || !slices.Equal(dirtyEntry.NeverCommittedSample, []string{"docs/new-finding.md"}) {
		t.Fatalf("dirty copy = %+v", dirtyEntry)
	}

	orphanEntry, _ := devRootEntryByName(evidence, "owner-final-91473c")
	if orphanEntry.Reason != devRootReasonOrphanedWorktree || orphanEntry.Kind != "worktree" || orphanEntry.Verdict != devRootVerdictNoUniqueWork {
		t.Fatalf("orphaned worktree = %+v", orphanEntry)
	}

	cloneEntry, _ := devRootEntryByName(evidence, "owner-fix-20260824")
	if cloneEntry.Reason != devRootReasonLocalClone || cloneEntry.Owner != owner || cloneEntry.MissingCommits != 1 ||
		cloneEntry.NeverCommittedFiles != 1 || cloneEntry.Verdict != devRootVerdictReview {
		t.Fatalf("local clone = %+v", cloneEntry)
	}

	if evidence.NoUniqueWorkCount != 2 || evidence.ReviewCount != 2 || evidence.UnknownCount != 0 || evidence.EntryCount != 4 {
		t.Fatalf("counts = %+v", evidence)
	}
}

func TestDevRootLeftoversUnmeasuredSkipsProof(t *testing.T) {
	root := t.TempDir()
	devRootWrite(t, filepath.Join(root, "x-proof.Ab12Cd", "file.txt"), "x\n")
	evidence, err := inspectDevRootLeftovers(context.Background(), root, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(evidence.Entries) != 1 || evidence.Entries[0].Verdict != devRootVerdictUnmeasured || evidence.Entries[0].NeverCommittedFiles != -1 || evidence.Measured {
		t.Fatalf("unmeasured evidence = %+v", evidence)
	}
}

func TestDevRootNameReason(t *testing.T) {
	for name, want := range map[string]string{
		"nicos-terminal-catalog-validate.XXXXXX.json":     devRootReasonUnfilledTemplate,
		"nicos-tools-precommit-projection.head.120229477": devRootReasonGoMkdirTemp,
		"nicos-docgraph-changed-paths.4062258806.paths0":  devRootReasonGoMkdirTemp,
		".catalog-exact.UlLPjp":                           devRootReasonShellMktemp,
		".catalog-exact.UlLPjp.index":                     devRootReasonShellMktemp,
		"nicos-terminal-bin.HdHH0W":                       devRootReasonShellMktemp,
		".kep-history-recovery-20260814T1115Z":            devRootReasonTimestamped,
		"nicos-tools":                                     "",
		"notes.backup":                                    "",
		"app.config":                                      "",
		"release-2026.11":                                 "",
		"kep-history-20260814T1115Z":                      "", // not hidden
	} {
		if got := devRootNameReason(name); got != want {
			t.Errorf("devRootNameReason(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestDevRootLayoutOwnerRefusesWeakOrTiedMatches(t *testing.T) {
	set := func(names ...string) map[string]bool {
		out := map[string]bool{}
		for _, name := range names {
			out[name] = true
		}
		return out
	}
	layout := set("a", "b", "c", "d", "e", "f")
	if got := devRootLayoutOwner(layout, map[string]map[string]bool{"/r1": set("a", "b", "c", "d", "e", "f"), "/r2": set("a", "b")}); got != "/r1" {
		t.Fatalf("strong match = %q", got)
	}
	if got := devRootLayoutOwner(layout, map[string]map[string]bool{"/r1": set("a", "b", "c", "d", "e", "f"), "/r2": set("a", "b", "c", "d", "e", "f")}); got != "" {
		t.Fatalf("tie should be unknown, got %q", got)
	}
	if got := devRootLayoutOwner(layout, map[string]map[string]bool{"/r1": set("a", "b", "c", "x", "y", "z", "w", "v")}); got != "" {
		t.Fatalf("weak match should be unknown, got %q", got)
	}
}
