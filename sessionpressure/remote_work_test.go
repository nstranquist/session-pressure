package sessionpressure

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
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
	write(filepath.Join(a, ".tmp", "ignored.go"), "generated build tree")
	modules, root, err := remoteModuleClosure(a)
	if err != nil || root != base || len(modules) != 2 {
		t.Fatal(modules, root, err)
	}
	var out bytes.Buffer
	if err := writeRemoteSources(&out, modules, root, remoteScopeRepository); err != nil {
		t.Fatal(err)
	}
	found := remoteUploadNames(t, &out)
	if !found["a/main.go"] || !found["b/go.mod"] || found["a/.env"] || found["a/node_modules/ignored.js"] || found["a/.tmp/ignored.go"] {
		t.Fatal(found)
	}
}

// remoteUploadNames decodes the gzip tar stream exactly as the runner's
// `tar -xzf -` does and returns the member names.
func remoteUploadNames(t *testing.T, stream io.Reader) map[string]bool {
	t.Helper()
	zr, err := gzip.NewReader(stream)
	if err != nil {
		t.Fatalf("upload is not gzip: %v", err)
	}
	tr := tar.NewReader(zr)
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
	return found
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

func TestNormalizeRemoteBuildArgv(t *testing.T) {
	tests := []struct {
		name       string
		argv       []string
		wantArgv   []string
		wantOutput string
		wantErr    bool
	}{
		{name: "bare build", argv: []string{"go", "build"}, wantErr: true},
		{name: "build dot", argv: []string{"go", "build", "."}, wantErr: true},
		{name: "build single package", argv: []string{"go", "build", "./cmd/x"}, wantErr: true},
		{name: "explicit -o separate", argv: []string{"go", "build", "-o", "bin/app", "./cmd/x"}, wantArgv: []string{"go", "build", "-o", "/srv/build/candidates/OUTPUT", "./cmd/x"}, wantOutput: "bin/app"},
		{name: "explicit -o equals", argv: []string{"go", "build", "-o=bin/app", "./cmd/x"}, wantArgv: []string{"go", "build", "-o=/srv/build/candidates/OUTPUT", "./cmd/x"}, wantOutput: "bin/app"},
		{name: "multi-package check-only", argv: []string{"go", "build", "./..."}, wantArgv: []string{"go", "build", "./..."}, wantOutput: ""},
		{name: "two packages without -o", argv: []string{"go", "build", "./a", "./b"}, wantArgv: []string{"go", "build", "./a", "./b"}, wantErr: true},
		{name: "non-build untouched", argv: []string{"go", "test", "./..."}, wantArgv: []string{"go", "test", "./..."}, wantOutput: ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gotArgv, gotOutput, err := normalizeRemoteBuildArgv(tc.argv)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got argv=%v output=%q", gotArgv, gotOutput)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if gotOutput != tc.wantOutput {
				t.Errorf("output = %q, want %q", gotOutput, tc.wantOutput)
			}
			if len(gotArgv) != len(tc.wantArgv) {
				t.Fatalf("argv = %v, want %v", gotArgv, tc.wantArgv)
			}
			for i := range tc.wantArgv {
				if gotArgv[i] != tc.wantArgv[i] {
					t.Fatalf("argv = %v, want %v", gotArgv, tc.wantArgv)
				}
			}
		})
	}
}
