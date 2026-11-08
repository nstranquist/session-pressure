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
