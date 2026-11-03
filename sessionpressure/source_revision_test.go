package sessionpressure

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"strings"
	"testing"
)

func TestStampedSourceRevisionParsesLinkerAssignments(t *testing.T) {
	for _, test := range []struct{ ldflags, want string }{
		{"-X " + sourceRevisionSymbol + "=abc123", "abc123"},
		{"-s -w -X=" + sourceRevisionSymbol + "=abc123-dirty", "abc123-dirty"},
		{"-X 'other.pkg.var=1' -X '" + sourceRevisionSymbol + "=def456'", "def456"},
		{"--X=" + sourceRevisionSymbol + "=ghi789", "ghi789"},
		{"-X other/pkg.sourceRevision=nope", ""},
		{"-X", ""},
		{"", ""},
	} {
		if got := stampedSourceRevision(test.ldflags); got != test.want {
			t.Fatalf("ldflags %q got %q want %q", test.ldflags, got, test.want)
		}
	}
}

func TestRevisionFromBuildInfoPrefersStampAndScopesVCSFallback(t *testing.T) {
	info := func(main string, settings ...debug.BuildSetting) *debug.BuildInfo {
		return &debug.BuildInfo{Main: debug.Module{Path: main}, Settings: settings}
	}
	stamp := debug.BuildSetting{Key: "-ldflags", Value: "-X " + sourceRevisionSymbol + "=stamped"}
	vcs := debug.BuildSetting{Key: "vcs.revision", Value: "vcsrev"}
	dirty := debug.BuildSetting{Key: "vcs.modified", Value: "true"}
	if got := revisionFromBuildInfo(info("nicos.tools/nicos-dev", stamp, vcs)); got != "stamped" {
		t.Fatalf("stamp lost to vcs: %q", got)
	}
	// A host module's vcs stamp names the host repository, not this source.
	if got := revisionFromBuildInfo(info("nicos.tools/nicos-dev", vcs)); got != "" {
		t.Fatalf("host vcs revision misread as session-pressure source: %q", got)
	}
	if got := revisionFromBuildInfo(info(sourceRevisionModule, vcs, dirty)); got != "vcsrev-dirty" {
		t.Fatalf("standalone dirty build: %q", got)
	}
	if got := revisionFromBuildInfo(info(sourceRevisionModule, vcs)); got != "vcsrev" {
		t.Fatalf("standalone clean build: %q", got)
	}
	if got := revisionFromBuildInfo(nil); got != "" {
		t.Fatalf("nil build info: %q", got)
	}
}

func TestInspectSourceRevisionsComparesOnlyExecutingBinaries(t *testing.T) {
	if got := inspectSourceRevisions("a", "", "", "b"); got.Checked || got.Mismatch {
		t.Fatalf("one executing revision must not be checked: %+v", got)
	}
	if got := inspectSourceRevisions("a", "a", "a", "b"); !got.Checked || got.Mismatch {
		t.Fatalf("an unpromoted candidate is ArtifactDrift's job, not a mismatch: %+v", got)
	}
	// The 2026-11-02 failure: a current helper under a CLI built hours earlier.
	got := inspectSourceRevisions("old", "", "new", "new")
	if !got.Checked || !got.Mismatch || !strings.Contains(got.Detail, "different source revisions") {
		t.Fatalf("stale CLI over a promoted helper: %+v", got)
	}
	if got := inspectSourceRevisions("a", "b", "", ""); !got.Mismatch {
		t.Fatalf("front door and CLI disagree: %+v", got)
	}
}

func TestShortRevisionAndPromoteCommand(t *testing.T) {
	if got := ShortRevision("0123456789abcdef0123-dirty"); got != "0123456789ab-dirty" {
		t.Fatalf("short dirty: %q", got)
	}
	if got := ShortRevision(""); got != "unknown" {
		t.Fatalf("empty: %q", got)
	}
	t.Setenv(PromoteCommandEnv, "")
	if got := PromoteCommand(); got != "session-pressure monitor install" {
		t.Fatalf("default promote: %q", got)
	}
	t.Setenv(PromoteCommandEnv, "make -C host promote")
	if got := PromoteCommand(); got != "make -C host promote" {
		t.Fatalf("host promote: %q", got)
	}
}

func TestDoctorWarnsOnRevisionMismatchWithoutFailing(t *testing.T) {
	t.Setenv(PromoteCommandEnv, "make promote-all")
	mismatch := inspectSourceRevisions("old", "", "new", "")
	doc := BuildPressureDoctor(PressureDoctorInput{SourceRevisions: &mismatch})
	if doc.SourceRevisions == nil || !doc.SourceRevisions.Mismatch {
		t.Fatalf("doctor dropped revisions: %+v", doc.SourceRevisions)
	}
	if !containsSubstring(doc.Warnings, "disagree on source revision") || !containsSubstring(doc.Fixes, "make promote-all") {
		t.Fatalf("warnings=%v fixes=%v", doc.Warnings, doc.Fixes)
	}
	agree := inspectSourceRevisions("same", "", "same", "")
	quiet := BuildPressureDoctor(PressureDoctorInput{SourceRevisions: &agree})
	if containsSubstring(quiet.Warnings, "source revision") || containsSubstring(quiet.Fixes, "make promote-all") {
		t.Fatalf("agreeing revisions warned: %v %v", quiet.Warnings, quiet.Fixes)
	}
	if doc.OK != quiet.OK {
		t.Fatal("a revision mismatch must not change doctor ok")
	}
}

// The design rests on Go recording -ldflags in build info for every binary,
// so a revision can be read from an installed artifact without running it.
func TestBinarySourceRevisionReadsLinkerStampFromExecutable(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a tiny executable")
	}
	goTool, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go toolchain unavailable")
	}
	dir := t.TempDir()
	for name, body := range map[string]string{
		"go.mod":  "module example.com/stamp\n\ngo 1.22\n",
		"main.go": "package main\n\nfunc main() {}\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	binary := filepath.Join(dir, "stamp")
	build := exec.Command(goTool, "build", "-buildvcs=false", "-ldflags", "-s -X "+sourceRevisionSymbol+"=feedface", "-o", binary, ".")
	build.Dir = dir
	build.Env = append(os.Environ(), "GOWORK=off", "GOFLAGS=")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, output)
	}
	if got := BinarySourceRevision(binary); got != "feedface" {
		t.Fatalf("stamped executable read %q", got)
	}
	if got := BinarySourceRevision(filepath.Join(dir, "missing")); got != "" {
		t.Fatalf("missing executable read %q", got)
	}
}

func containsSubstring(values []string, want string) bool {
	for _, value := range values {
		if strings.Contains(value, want) {
			return true
		}
	}
	return false
}
