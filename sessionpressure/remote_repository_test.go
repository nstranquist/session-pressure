package sessionpressure

import (
	"archive/tar"
	"bytes"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestRemoteNestedModuleRetainsRepositoryFixtures(t *testing.T) {
	root := t.TempDir()
	if out, err := exec.Command("git", "init", "-q", root).CombinedOutput(); err != nil {
		t.Fatalf("fixture git init: %s: %v", out, err)
	}
	write := func(name, body string) {
		t.Helper()
		p := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("nested/go.mod", "module example.test/nested\ngo 1.26\n")
	write("nested/main.go", "package main")
	write(".claude/hooks/toolguard.sh", "fixture")
	write("cmd/fixture.go", "fixture")
	write(".env", "secret")
	write(".tmp/generated", "cache")
	write(".gitignore", "ignored\nnested/generated-binary\nnested/.venv/\n")
	write("ignored", "ignored")
	write("nested/generated-binary", "not source")
	write("nested/.venv/cache", "not source")
	modules, common, err := remoteModuleClosure(filepath.Join(root, "nested"))
	if err != nil || common != root {
		t.Fatalf("common=%q err=%v", common, err)
	}
	var out bytes.Buffer
	if err := writeRemoteSources(&out, modules, common); err != nil {
		t.Fatal(err)
	}
	found := map[string]bool{}
	tr := tar.NewReader(&out)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		found[h.Name] = true
	}
	for _, name := range []string{"nested/go.mod", ".claude/hooks/toolguard.sh", "cmd/fixture.go"} {
		if !found[name] {
			t.Errorf("missing repository input %s", name)
		}
	}
	for _, name := range []string{".env", ".tmp/generated", "ignored", ".git/HEAD", "nested/generated-binary", "nested/.venv/cache"} {
		if found[name] {
			t.Errorf("excluded input %s uploaded", name)
		}
	}
}
