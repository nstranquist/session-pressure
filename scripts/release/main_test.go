package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTamperedBundlePreservesInstalledAuthority(t *testing.T) {
	dir := t.TempDir()
	prefix := t.TempDir()
	old := filepath.Join(prefix, "session-pressure")
	if e := os.WriteFile(old, []byte("previous installation"), 0755); e != nil {
		t.Fatal(e)
	}
	m := manifest{Revision: strings.Repeat("a", 40), Files: map[string]string{}}
	for _, n := range names {
		if e := os.WriteFile(filepath.Join(dir, n), []byte("tampered"), 0755); e != nil {
			t.Fatal(e)
		}
		m.Files[n] = strings.Repeat("0", 64)
	}
	if e := writeJSON(filepath.Join(dir, "release.json"), m); e != nil {
		t.Fatal(e)
	}
	if e := install(dir, prefix); e == nil {
		t.Fatal("tampered bundle accepted")
	}
	raw, e := os.ReadFile(old)
	if e != nil || string(raw) != "previous installation" {
		t.Fatalf("prior authority changed: %q %v", raw, e)
	}
	if _, e = os.Lstat(filepath.Join(prefix, ".session-pressure-releases", "current")); !os.IsNotExist(e) {
		t.Fatal("failed candidate promoted")
	}
}

func TestObserveRefusesShortSampleAsAcceptance(t *testing.T) {
	if e := observe([]string{"--out", filepath.Join(t.TempDir(), "evidence.json"), "--binary", "/bin/echo", "--samples", "3"}); e == nil {
		t.Fatal("underpowered sample accepted")
	}
}

func TestObservationRestartPreservesPriorEvidence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "evidence.json")
	if e := startObservation(path, observation{Failures: 7}); e != nil {
		t.Fatal(e)
	}
	before, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	if e = observe([]string{"--out", path, "--binary", "/bin/echo"}); e == nil || !strings.Contains(e.Error(), "unused --out") {
		t.Fatalf("restart did not preserve the original run: %v", e)
	}
	after, e := os.ReadFile(path)
	if e != nil || string(after) != string(before) {
		t.Fatalf("prior evidence changed: %q %v", after, e)
	}
}

func TestDarwinReleaseRejectsDisabledNativeReader(t *testing.T) {
	t.Setenv("CGO_ENABLED", "0")
	if _, e := releaseEnvironment("darwin", "arm64"); e == nil {
		t.Fatal("incomplete Darwin release accepted")
	}
	dir := t.TempDir()
	m := manifest{Revision: strings.Repeat("a", 40), OS: "darwin", Arch: "arm64", Files: map[string]string{}}
	for _, name := range names {
		m.Files[name] = strings.Repeat("0", 64)
	}
	if e := writeJSON(filepath.Join(dir, "release.json"), m); e != nil {
		t.Fatal(e)
	}
	if _, e := verify(dir); e == nil || !strings.Contains(e.Error(), "native SMART") {
		t.Fatalf("disabled native feature was not rejected: %v", e)
	}
}
