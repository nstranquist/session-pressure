package sessionpressure

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A rebuilt-but-unpromoted helper silently served stale behavior on 2026-07-24
// while artifact status reported ok. Drift must be visible.
func TestInspectArtifactDriftDetectsUnpromotedBuild(t *testing.T) {
	dir := t.TempDir()
	installedSource := filepath.Join(dir, "installed-source")
	if err := os.WriteFile(installedSource, []byte("revision-one"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := promoteArtifact(installedSource, dir, time.Now()); err != nil {
		t.Fatal(err)
	}

	same := filepath.Join(dir, "same-build")
	if err := os.WriteFile(same, []byte("revision-one"), 0o755); err != nil {
		t.Fatal(err)
	}
	if drift := InspectArtifactDrift(same, dir); !drift.Checked || drift.Drifted {
		t.Fatalf("identical build reported drift: %+v", drift)
	}

	newer := filepath.Join(dir, "newer-build")
	if err := os.WriteFile(newer, []byte("revision-two"), 0o755); err != nil {
		t.Fatal(err)
	}
	drift := InspectArtifactDrift(newer, dir)
	if !drift.Checked || !drift.Drifted {
		t.Fatalf("unpromoted build not reported: %+v", drift)
	}
	if drift.Detail == "" || drift.CandidateSHA256 == drift.InstalledSHA256 {
		t.Fatalf("drift lacks actionable detail: %+v", drift)
	}
}

// Report-only: an unreadable candidate must not fail artifact inspection.
func TestInspectArtifactDriftStaysReportOnly(t *testing.T) {
	dir := t.TempDir()
	if drift := InspectArtifactDrift(filepath.Join(dir, "absent"), dir); drift.Checked || drift.Drifted {
		t.Fatalf("missing artifact should leave drift unchecked: %+v", drift)
	}
}
