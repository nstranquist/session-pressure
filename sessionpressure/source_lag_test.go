package sessionpressure

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func initTestSessionPressureRepo(t *testing.T) (string, []string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	dir := t.TempDir()
	run := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
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
	run("init", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module "+sourceRevisionModule+"\n\ngo 1.22\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	run("add", "go.mod")
	run("commit", "-m", "initial commit")
	c1 := run("rev-parse", "HEAD")

	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("two\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	run("add", "a.txt")
	run("commit", "-m", "second commit")
	c2 := run("rev-parse", "HEAD")

	if err := os.WriteFile(filepath.Join(dir, "b.txt"), []byte("three\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	run("add", "b.txt")
	run("commit", "-m", "third commit")
	c3 := run("rev-parse", "HEAD")

	return dir, []string{c1, c2, c3}
}

func TestInspectSourceLagCurrentBehindAheadAndUnknown(t *testing.T) {
	repo, commits := initTestSessionPressureRepo(t)
	ctx := context.Background()

	current := InspectSourceLagAtRepo(ctx, commits[2], repo)
	if !current.Checked || current.Status != "current" || current.CommitsBehind != 0 || current.CommitsAhead != 0 || current.Remediation != "" {
		t.Fatalf("current=%+v", current)
	}

	dirty := InspectSourceLagAtRepo(ctx, commits[2]+"-dirty", repo)
	if !dirty.Checked || dirty.Status != "dirty" || !dirty.InstalledDirty || dirty.Remediation == "" {
		t.Fatalf("dirty=%+v", dirty)
	}

	behind := InspectSourceLagAtRepo(ctx, commits[0], repo)
	if !behind.Checked || behind.Status != "behind" || behind.CommitsBehind != 2 || behind.CommitsAhead != 0 || !strings.Contains(behind.Detail, "2 commits behind") || behind.Remediation == "" {
		t.Fatalf("behind=%+v", behind)
	}

	unknown := InspectSourceLagAtRepo(ctx, "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef", repo)
	if unknown.Checked || unknown.Status != "unknown" || unknown.Remediation != "" {
		t.Fatalf("unknown=%+v", unknown)
	}

	empty := InspectSourceLagAtRepo(ctx, "", repo)
	if empty.Checked || empty.Status != "unknown" {
		t.Fatalf("empty=%+v", empty)
	}

	// Checkout c1 as HEAD so c3 is ahead of local HEAD (no false alarm).
	cmd := exec.Command("git", "-C", repo, "checkout", "--quiet", "--detach", commits[0])
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("checkout detach: %v\n%s", err, out)
	}
	ahead := InspectSourceLagAtRepo(ctx, commits[2], repo)
	if !ahead.Checked || ahead.Status != "ahead" || ahead.CommitsAhead != 2 || ahead.CommitsBehind != 0 || ahead.Remediation != "" {
		t.Fatalf("ahead=%+v", ahead)
	}
}

func TestInspectSourceLagRejectsForeignRepoAndDisableEnv(t *testing.T) {
	other := t.TempDir()
	if err := os.MkdirAll(filepath.Join(other, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(other, "go.mod"), []byte("module example.com/other-repo\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	lag := InspectSourceLagAtRepo(context.Background(), "abc123", other)
	if lag.Checked || lag.Status != "unavailable" {
		t.Fatalf("foreign repo must not trigger lag check: %+v", lag)
	}

	t.Setenv(SourceRepoEnv, "off")
	if got := DiscoverSourceRepo(other); got != "" {
		t.Fatalf("disabled env discovered %q", got)
	}
}

func TestDoctorWarnsOnSourceLagWithoutFalseAlarmsOrFailingOK(t *testing.T) {
	behind := SourceLag{
		Checked:           true,
		Status:            "behind",
		SourceRevision:    "1111111111111111111111111111111111111111",
		InstalledRevision: "0000000000000000000000000000000000000000",
		CommitsBehind:     3,
		Detail:            "installed session-pressure (000000000000) is 3 commits behind source HEAD (111111111111)",
		Remediation:       "make -C /repo release-promote",
	}
	docBehind := BuildPressureDoctor(PressureDoctorInput{SourceLag: &behind})
	if docBehind.SourceLag == nil || docBehind.SourceLag.CommitsBehind != 3 {
		t.Fatalf("doctor dropped source_lag: %+v", docBehind.SourceLag)
	}
	if !containsSubstring(docBehind.Warnings, "3 commits behind") || !containsSubstring(docBehind.Fixes, "release-promote") {
		t.Fatalf("warnings=%v fixes=%v", docBehind.Warnings, docBehind.Fixes)
	}

	// Current, ahead, and unavailable must NEVER emit warnings or fixes.
	for _, calm := range []SourceLag{
		{Checked: true, Status: "current"},
		{Checked: true, Status: "ahead", CommitsAhead: 2},
		{Checked: false, Status: "unavailable"},
		{Checked: false, Status: "unknown"},
	} {
		copyLag := calm
		doc := BuildPressureDoctor(PressureDoctorInput{SourceLag: &copyLag})
		if containsSubstring(doc.Warnings, "behind") || containsSubstring(doc.Warnings, "diverged") || containsSubstring(doc.Fixes, "release-promote") {
			t.Fatalf("false alarm for %+v: warnings=%v fixes=%v", calm, doc.Warnings, doc.Fixes)
		}
		if doc.OK != docBehind.OK {
			t.Fatalf("source lag must be a warning, not change doctor ok")
		}
	}
}
