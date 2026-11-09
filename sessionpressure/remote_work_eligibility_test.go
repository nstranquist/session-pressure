package sessionpressure

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func envMap(values map[string]string) func(string) string {
	return func(key string) string { return values[key] }
}

func TestRemoteWorkIneligibleReason(t *testing.T) {
	pure := t.TempDir()
	write := func(dir, name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(pure, "go.mod", "module example.test/pure\n\ngo 1.22\n")
	write(pure, "main.go", "package main\n\nfunc main() {}\n")
	cgo := t.TempDir()
	write(cgo, "go.mod", "module example.test/cgo\n\ngo 1.22\n")
	write(cgo, "main.go", "package main\n\n// int one(void) { return 1; }\nimport \"C\"\n\nfunc main() { _ = C.one() }\n")

	cases := []struct {
		name string
		dir  string
		argv []string
		env  map[string]string
		want string // substring; "" means eligible
	}{
		{"pure test", pure, []string{"go", "test", "./..."}, nil, ""},
		{"pure build with output", pure, []string{"go", "build", "-o", "/Users/x/bin/app", "."}, nil, ""},
		{"producer modfile in GOFLAGS", pure, []string{"go", "build", "-p=1", "-o", "/tmp/out", "./cmd/ndev-go"},
			map[string]string{"GOFLAGS": "-mod=mod -modfile=/var/folders/T/ndev-producer-mod-1/producer.mod"}, "GOFLAGS -modfile names a local file"},
		{"modfile argument", pure, []string{"go", "test", "-modfile", "alt.mod", "./..."}, nil, "-modfile names a local file"},
		{"overlay", pure, []string{"go", "vet", "-overlay=o.json", "./..."}, nil, "-overlay names a local file"},
		{"pgo profile", pure, []string{"go", "build", "-pgo=default.pgo", "-o", "x", "."}, nil, "-pgo names a local profile"},
		{"pgo auto", pure, []string{"go", "build", "-pgo=auto", "-o", "x", "."}, nil, ""},
		{"absolute package", pure, []string{"go", "vet", "/Users/x/pkg"}, nil, "absolute local path /Users/x/pkg"},
		{"absolute coverprofile", pure, []string{"go", "test", "-coverprofile", "/tmp/c.out", "./..."}, nil, "-coverprofile names the absolute local path"},
		{"goexperiment", pure, []string{"go", "test", "./..."}, map[string]string{"GOEXPERIMENT": "rangefunc"}, "GOEXPERIMENT"},
		{"test GOOS set", pure, []string{"go", "test", "./..."}, map[string]string{"GOOS": "darwin"}, "remote test runs natively on the linux/arm64 runner"},
		{"build linux target", pure, []string{"go", "build", "-o", "x", "."}, map[string]string{"GOOS": "linux"}, "remote builds produce darwin/arm64"},
		// The runner builds darwin/arm64 with cgo through its pinned compiler,
		// so cgo builds are eligible; a local CC is refused by the profile.
		{"build explicit cgo", pure, []string{"go", "build", "-o", "x", "."}, map[string]string{"CGO_ENABLED": "1"}, ""},
		{"build cgo graph", cgo, []string{"go", "build", "-o", "x", "."}, nil, ""},
		{"build cgo graph, local cgo off", cgo, []string{"go", "build", "-o", "x", "."}, map[string]string{"CGO_ENABLED": "0"}, ""},
		{"test cgo graph runs natively", cgo, []string{"go", "test", "./..."}, nil, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := remoteWorkIneligibleReason(tc.argv, envMap(tc.env))
			if tc.want == "" && got != "" {
				t.Fatalf("want eligible, got %q", got)
			}
			if tc.want != "" && !strings.Contains(got, tc.want) {
				t.Fatalf("want reason containing %q, got %q", tc.want, got)
			}
		})
	}
}

func TestRemoteSourceSizeUsesUploadFilter(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.test/size\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "a.go"), make([]byte, 600), 0o600); err != nil {
		t.Fatal(err)
	}
	// Excluded trees must not count toward the limit.
	if err := os.MkdirAll(filepath.Join(root, ".tmp"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".tmp", "big"), make([]byte, 4096), 0o600); err != nil {
		t.Fatal(err)
	}
	size, err := remoteSourceSize([]string{root}, root, remoteScopeRepository, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if want := int64(600 + len("module example.test/size\n")); size != want {
		t.Fatalf("size = %d, want %d", size, want)
	}
	if _, err := remoteSourceSize([]string{root}, root, remoteScopeRepository, 100); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("want limit error, got %v", err)
	}
}
