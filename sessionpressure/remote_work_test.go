package sessionpressure

import (
	"archive/tar"
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestRemoteModuleClosureAndSourceBoundaries(t *testing.T) {
	base := t.TempDir()
	a := filepath.Join(base, "a")
	b := filepath.Join(base, "b")
	write := func(path, body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(a, "go.mod"), "module example.test/a\ngo 1.26\nreplace example.test/b => ../b\n")
	write(filepath.Join(b, "go.mod"), "module example.test/b\ngo 1.26\n")
	write(filepath.Join(a, "main.go"), "package main")
	write(filepath.Join(a, ".env"), "sensitive")
	write(filepath.Join(a, "node_modules", "ignored.js"), "cache")
	modules, root, err := remoteModuleClosure(a)
	if err != nil || root != base || len(modules) != 2 {
		t.Fatal(modules, root, err)
	}
	var out bytes.Buffer
	if err := writeRemoteSources(&out, modules, root); err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(&out)
	found := map[string]bool{}
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
	if !found["a/main.go"] || !found["b/go.mod"] || found["a/.env"] || found["a/node_modules/ignored.js"] {
		t.Fatal(found)
	}
}

func TestAutomatedBrowserRootExcludesPersonalAndChildren(t *testing.T) {
	for _, tc := range []struct {
		command string
		want    bool
	}{
		{"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome", false},
		{"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome --headless=new --screenshot=/tmp/a", true},
		{"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome --headless=new --type=gpu-process", false},
		{"python3 script.py --headless", false},
	} {
		if got := automatedBrowserRoot(tc.command); got != tc.want {
			t.Errorf("%q: %v", tc.command, got)
		}
	}
}
