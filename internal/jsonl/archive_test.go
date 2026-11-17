package jsonl

import (
	"compress/gzip"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

func gunzip(t *testing.T, path string) string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := io.ReadAll(zr)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestArchiveFileMovesIntoVerifiedGzipAndNeverOverwrites(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "snapshots-20200101.jsonl")
	if err := os.WriteFile(filepath.Join(dir, "snapshots-20200101.jsonl.gz"), []byte("earlier archive"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(src, []byte("{\"a\":1}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	dst, err := ArchiveFile(src)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Fatal("source kept after verified archive")
	}
	if dst == filepath.Join(dir, "snapshots-20200101.jsonl.gz") || !strings.HasSuffix(dst, ".gz") {
		t.Fatalf("archive overwrote or misnamed: %s", dst)
	}
	if got := gunzip(t, dst); got != "{\"a\":1}\n" {
		t.Fatalf("archive content %q", got)
	}
	if raw, _ := os.ReadFile(filepath.Join(dir, "snapshots-20200101.jsonl.gz")); string(raw) != "earlier archive" {
		t.Fatal("existing archive replaced")
	}
	if temps, _ := filepath.Glob(filepath.Join(dir, ".*tmp-*")); len(temps) != 0 {
		t.Fatalf("temp files left: %v", temps)
	}
}

func TestArchiveFailureKeepsSource(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "events.jsonl")
	if err := os.WriteFile(src, []byte("row\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o500); err != nil { // no temp file can be created
		t.Fatal(err)
	}
	defer os.Chmod(dir, 0o700)
	if _, err := ArchiveFile(src); err == nil {
		t.Fatal("archive into a read-only directory must fail")
	}
	if raw, err := os.ReadFile(src); err != nil || string(raw) != "row\n" {
		t.Fatalf("source lost on failed archive: %q %v", raw, err)
	}
}

func TestRotateArchiveKeepsEveryGeneration(t *testing.T) {
	dir := t.TempDir()
	live := filepath.Join(dir, "audit.jsonl")
	now := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	for i, body := range []string{"gen1\n", "gen2\n"} {
		if err := os.WriteFile(live, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := RotateArchive(live, now); err != nil { // same stamp twice
			t.Fatalf("rotate %d: %v", i, err)
		}
	}
	gens, _ := filepath.Glob(filepath.Join(dir, "audit.jsonl.*.gz"))
	if len(gens) != 2 {
		t.Fatalf("generations=%v, want 2", gens)
	}
	seen := map[string]bool{}
	for _, g := range gens {
		seen[gunzip(t, g)] = true
	}
	// nicos-tools jsonl.Family naming: <live>.<20060102T150405Z>[-N].gz
	familyName := regexp.MustCompile(`^audit\.jsonl\.\d{8}T\d{6}Z(-[1-9]\d*)?\.gz$`)
	for _, g := range gens {
		if !familyName.MatchString(filepath.Base(g)) {
			t.Fatalf("generation %s is not in jsonl.Family naming", filepath.Base(g))
		}
	}
	if !seen["gen1\n"] || !seen["gen2\n"] {
		t.Fatalf("lost a generation: %v", seen)
	}
	if _, err := os.Stat(live); !os.IsNotExist(err) {
		t.Fatal("live file not rotated")
	}
}

func TestArchiveCopyKeepsSource(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "actions.jsonl")
	if err := os.WriteFile(src, []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	dst, err := ArchiveCopy(src, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(src); err != nil {
		t.Fatal("source removed by copy")
	}
	if gunzip(t, dst) != "x\n" {
		t.Fatal("copy content")
	}
}

func TestRotateArchiveSweepsLegacyAndUncompressedGenerations(t *testing.T) {
	dir := t.TempDir()
	live := filepath.Join(dir, "audit.jsonl")
	now := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	old := now.Add(-time.Hour)
	legacy := live + ".1"
	stale := live + ".20261115T000000Z"
	fresh := live + ".20261116T115959Z"
	placeholder := live + ".20261115T000001Z"
	for path, body := range map[string]string{legacy: "legacy\n", stale: "stale\n", fresh: "fresh\n", placeholder: "", live: "live\n"} {
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if path != fresh {
			if err := os.Chtimes(path, old, old); err != nil {
				t.Fatal(err)
			}
		} else if err := os.Chtimes(path, now, now); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := RotateArchive(live, now); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]string{legacy: "legacy\n", stale: "stale\n"} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("%s not swept", filepath.Base(path))
		}
		if got := gunzip(t, path+".gz"); got != want {
			t.Fatalf("%s archive=%q", filepath.Base(path), got)
		}
	}
	for _, path := range []string{fresh, placeholder} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("%s should stay: %v", filepath.Base(path), err)
		}
	}
}

func TestRotateArchiveAfterPeerRotationReportsNotExist(t *testing.T) {
	dir := t.TempDir()
	live := filepath.Join(dir, "audit.jsonl")
	_, err := RotateArchive(live, time.Now())
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("err=%v, want not-exist", err)
	}
	if left, _ := filepath.Glob(live + ".*"); len(left) != 0 {
		t.Fatalf("reservation left behind: %v", left)
	}
}
