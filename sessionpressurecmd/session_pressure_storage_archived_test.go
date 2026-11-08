package sessionpressurecmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestArchivedProjectsProviderReadsProjection(t *testing.T) {
	home := t.TempDir()
	now := time.Date(2026, 11, 8, 17, 0, 0, 0, time.UTC)

	missing := storageArchivedProjectsProvider(home, now)
	if missing.Present || missing.EstimatedBytes != 0 || missing.ArchiveEvidence != nil || missing.Classification != storageProviderReportOnly || missing.MutationSupported {
		t.Fatalf("missing projection should be quiet and report-only: %+v", missing)
	}

	path := storageArchiveProjectionPath(home)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	doc := `{"schema":"nicos.catalog.archive-candidates.v1","generated_at":"2026-11-08T16:00:00Z","producer":"test","archives":[
 {"id":"small","path":"/a/small","format":"git","tier":"local","online":true,"bytes":10,"external_drive_candidate":true,"offload_command":"ndev catalog external archive offload --id small --volume /Volumes/<drive> --apply"},
 {"id":"big","path":"/a/big","format":"git","tier":"local","online":true,"bytes":900,"external_drive_candidate":true},
 {"id":"gone","path":"/a/gone","format":"git","tier":"local","online":false,"bytes":0,"external_drive_candidate":true},
 {"id":"keep","path":"/a/keep","format":"git","tier":"local","online":true,"bytes":50,"external_drive_candidate":false},
 {"id":"moved","path":"/Volumes/D/nicos-archive/moved","format":"git","tier":"external-drive","online":false,"bytes":0,"external_drive_candidate":true}
]}`
	if err := os.WriteFile(path, []byte(doc), 0o644); err != nil {
		t.Fatal(err)
	}
	report := storageArchivedProjectsProvider(home, now)
	ev := report.ArchiveEvidence
	if !report.Present || report.EstimatedBytes != 910 || report.EstimateKind != "inventory" || ev == nil {
		t.Fatalf("report = %+v", report)
	}
	if ev.CandidateCount != 2 || ev.OffloadedCount != 1 || ev.Stale || ev.Candidates[0].ID != "big" {
		t.Fatalf("evidence = %+v", ev)
	}
	if !strings.Contains(report.BlockedReason, "offload") {
		t.Fatalf("blocked reason = %q", report.BlockedReason)
	}

	stale := storageArchivedProjectsProvider(home, now.Add(8*24*time.Hour))
	if !stale.ArchiveEvidence.Stale {
		t.Fatal("8-day-old projection must be stale")
	}

	if err := os.WriteFile(path, []byte(`{"schema":"other"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if bad := storageArchivedProjectsProvider(home, now); bad.Present || !strings.Contains(bad.BlockedReason, "unknown schema") {
		t.Fatalf("bad schema = %+v", bad)
	}
}
