package sessionpressure

import (
	"encoding/json"
	"errors"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

// writeGoTree writes files (slash-separated paths) into a new directory
// outside any repository and returns it.
func writeGoTree(t *testing.T, files map[string]string) string {
	t.Helper()
	root := tempDirOutsideRepository(t)
	for path, body := range files {
		full := filepath.Join(root, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// goBuildOutputFiles is a module with three main packages (the root, cmd/a
// and cmd/b), a library and two packages selected by a build tag.
var goBuildOutputFiles = map[string]string{
	"go.mod":         "module example.test/m/v2\ngo 1.26\n",
	"main.go":        "package main\n\nfunc main() {}\n",
	"cmd/a/main.go":  "package main\n\nfunc main() {}\n",
	"cmd/b/main.go":  "package main\n\nfunc main() {}\n",
	"lib/lib.go":     "package lib\n",
	"tags/x/main.go": "//go:build special\n\npackage main\n\nfunc main() {}\n",
	"tags/y/y.go":    "//go:build !special\n\npackage y\n",
}

func TestGoBuildImplicitOutput(t *testing.T) {
	dir := writeGoTree(t, goBuildOutputFiles)
	for _, tc := range []struct {
		args  []string
		name  string
		fails bool
		probe string
	}{
		{[]string{"./cmd/a/..."}, "a", false, "go_list"}, // a pattern of one main package
		{[]string{"./cmd/..."}, "", false, "walk"},       // two main packages
		{[]string{"./..."}, "", false, "walk"},
		{[]string{"./lib/..."}, "", false, "go_list"},  // a library
		{[]string{"./tags/..."}, "", false, "go_list"}, // without the tag only the library y
		{[]string{"-tags", "special", "./tags/..."}, "x", false, "go_list"},
		{[]string{"--tags=special", "./tags/..."}, "x", false, "go_list"},
		{[]string{"."}, "m", false, "go_list"}, // v2 is a major version element
		{[]string{"./cmd/a"}, "a", false, "go_list"},
		{[]string{"cmd/a/main.go"}, "main", false, "go_list"},
		{[]string{"./cmd/a/...", "./cmd/a"}, "a", false, "go_list"}, // one package named twice
		{[]string{"./cmd/a", "./lib"}, "", false, "go_list"},
		{[]string{"-buildmode=plugin", "./cmd/a"}, "a.so", false, "go_list"},
		{[]string{"-buildmode=archive", "./cmd/a"}, "", false, ""}, // Go leaves out main packages
		{[]string{"-n", "./cmd/a"}, "", false, ""},
		{[]string{"./missing"}, "", true, "go_list"},
		{[]string{"./tags/x"}, "", true, "go_list"}, // its constraint excludes every file
	} {
		got, err := goBuildImplicitOutput(dir, "go", tc.args)
		if err != nil || got.name != tc.name || got.fails != tc.fails || got.probe != tc.probe {
			t.Errorf("%q: %+v, %v; want name %q fails %v probe %q", tc.args, got, err, tc.name, tc.fails, tc.probe)
		}
	}
	// An overlay that deletes cmd/b/main.go leaves one main package, so the
	// walk of the files on disk does not answer.
	overlay := filepath.Join(dir, "overlay.json")
	body, err := json.Marshal(map[string]map[string]string{"Replace": {filepath.Join(dir, "cmd", "b", "main.go"): ""}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(overlay, body, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"-overlay", overlay, "./cmd/..."}, {"--overlay=" + overlay, "./cmd/..."}} {
		if got, err := goBuildImplicitOutput(dir, "go", args); err != nil || got.name != "a" || got.probe != "go_list" {
			t.Errorf("%q: %+v, %v; want a from go list", args, got, err)
		}
	}
	if _, err := goBuildImplicitOutput(dir, "go", []string{"-buildmode=c-shared", "./cmd/a"}); err == nil {
		t.Error("a build mode the probe does not read gave an answer")
	}
	// go list does not run when the walk finds two packages.
	missingGo := filepath.Join(dir, "missing-go")
	if got, err := goBuildImplicitOutput(dir, missingGo, []string{"./..."}); err != nil || got.probe != "walk" {
		t.Errorf("walk without go list: %+v, %v", got, err)
	}
	if _, err := goBuildImplicitOutput(dir, missingGo, []string{"./cmd/a/..."}); err == nil {
		t.Error("one package needs go list, and go list could not run")
	}
}

// The probe agrees with go build itself: it names the file go build writes
// into the working directory, or none.
func TestGoBuildImplicitOutputMatchesGoBuild(t *testing.T) {
	goCmd, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go is not on PATH")
	}
	for _, args := range [][]string{
		{"./cmd/a/..."}, {"./cmd/..."}, {"./lib/..."}, {"./tags/..."}, {"-tags", "special", "./tags/..."},
		{"."}, {"cmd/a/main.go"}, {"./cmd/a/...", "./cmd/a"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			t.Parallel()
			dir := writeGoTree(t, goBuildOutputFiles)
			probe, err := goBuildImplicitOutput(dir, goCmd, args)
			if err != nil {
				t.Fatal(err)
			}
			before := goTreeEntries(t, dir)
			build := exec.Command(goCmd, append([]string{"build"}, args...)...)
			build.Dir = dir
			build.Env = append(os.Environ(), "GOWORK=off", "GOFLAGS=", "CGO_ENABLED=0")
			if out, err := build.CombinedOutput(); err != nil {
				t.Fatalf("go build: %v\n%s", err, out)
			}
			var written []string
			for _, name := range goTreeEntries(t, dir) {
				if !slices.Contains(before, name) {
					written = append(written, name)
				}
			}
			want := []string(nil)
			if probe.name != "" {
				want = []string{probe.name}
			}
			if !slices.Equal(written, want) {
				t.Fatalf("go build wrote %q, the probe said %q", written, want)
			}
		})
	}
}

func goTreeEntries(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, len(entries))
	for i, entry := range entries {
		names[i] = entry.Name()
	}
	return names
}

// The walk counts a directory only when Go is sure to match it as a
// package, so a wrong count can only send the question to go list.
func TestGoPatternsMatchTwoPackagesCountsOnlySurePackages(t *testing.T) {
	base := map[string]string{"go.mod": "module example.test/walk\ngo 1.26\n", "p/one/one.go": "package one\n"}
	otherOS := "windows"
	if runtime.GOOS == "windows" {
		otherOS = "linux"
	}
	for name, extra := range map[string]map[string]string{
		"testdata":           {"p/testdata/t.go": "package t\n"},
		"underscore dir":     {"p/_x/x.go": "package x\n"},
		"dot dir":            {"p/.x/x.go": "package x\n"},
		"vendor":             {"p/vendor/v/v.go": "package v\n"},
		"nested module":      {"p/nested/go.mod": "module example.test/nested\ngo 1.26\n", "p/nested/n.go": "package n\n"},
		"build constraint":   {"p/c/c.go": "//go:build " + runtime.GOOS + "\n\npackage c\n"},
		"plus build line":    {"p/c/c.go": "// +build " + runtime.GOOS + "\n\npackage c\n"},
		"cgo":                {"p/c/c.go": "package c\n\nimport \"C\"\n"},
		"another GOOS":       {"p/c/c_" + otherOS + ".go": "package c\n"},
		"test file":          {"p/c/c_test.go": "package c\n"},
		"underscore file":    {"p/c/_c.go": "package c\n"},
		"documentation file": {"p/c/doc.go": "package documentation\n"},
		"parse error":        {"p/c/c.go": "package\n"},
	} {
		files := maps.Clone(base)
		maps.Copy(files, extra)
		if goPatternsMatchTwoPackages(writeGoTree(t, files), []string{"./p/..."}) {
			t.Errorf("%s: counted as a sure package", name)
		}
	}
	linked := writeGoTree(t, base)
	if err := os.Symlink(filepath.Join(linked, "p", "one"), filepath.Join(linked, "p", "link")); err != nil {
		t.Fatal(err)
	}
	if goPatternsMatchTwoPackages(linked, []string{"./p/..."}) {
		t.Error("followed a symlinked directory, which Go does not")
	}

	files := maps.Clone(base)
	files["p/two/two.go"] = "package two\n"
	dir := writeGoTree(t, files)
	for _, patterns := range [][]string{{"./p/..."}, {"--", "./p/..."}, {"./p/one/...", "./p/two/..."}, {"./..."}} {
		if !goPatternsMatchTwoPackages(dir, patterns) {
			t.Errorf("%q: two packages not found", patterns)
		}
	}
	for _, patterns := range [][]string{
		{"./p"}, {"./p/one/...", "./p/one/..."}, {"./p/o..."}, {"example.test/walk/..."}, {"./p/one", "./p/two/..."}, {"../..."},
	} {
		if goPatternsMatchTwoPackages(dir, patterns) {
			t.Errorf("%q: sure of two packages", patterns)
		}
	}
	if !goPatternsMatchTwoPackages(filepath.Join(dir, "p"), []string{"../..."}) {
		t.Error("../... from a subdirectory of the module")
	}
	// Go reads the first directory from the pattern: "." is walked, "_p" is not.
	hidden := writeGoTree(t, map[string]string{"go.mod": base["go.mod"], "_p/one/one.go": "package one\n", "_p/two/two.go": "package two\n"})
	for _, patterns := range [][]string{{"./_p/..."}, {"./..."}} {
		if goPatternsMatchTwoPackages(hidden, patterns) {
			t.Errorf("%q: walked an underscore directory", patterns)
		}
	}
	// An ignore directive leaves directories out of every pattern.
	files["go.mod"] = "module example.test/walk\ngo 1.26\nignore ./p/two\n"
	if goPatternsMatchTwoPackages(writeGoTree(t, files), []string{"./p/..."}) {
		t.Error("walked a module with an ignore directive")
	}
}

// Go refuses to replace a directory or a file that is not an object file;
// the route fails such a build before upload, and refuses an -o directory
// that it could not place.
func TestPlanRemoteBuildChecksTheOutputPath(t *testing.T) {
	machO := string([]byte{0xCF, 0xFA, 0xED, 0xFE, 0x0C, 0x00})
	for _, tc := range []struct {
		name             string
		files            map[string]string
		dirs             []string
		argv             []string
		failure, refusal string
	}{
		{name: "default output is a directory", dirs: []string{"a"}, argv: []string{"go", "build", "./cmd/a/..."}, failure: `go: build output "a" already exists and is a directory`},
		{name: "default output is a text file", files: map[string]string{"a": "notes\n"}, argv: []string{"go", "build", "./cmd/a/..."}, failure: `go: build output "a" already exists and is not an object file`},
		{name: "default output is an empty file", files: map[string]string{"a": ""}, argv: []string{"go", "build", "./cmd/a/..."}},
		{name: "default output is a binary", files: map[string]string{"a": machO}, argv: []string{"go", "build", "./cmd/a"}},
		{name: "-o names a text file", files: map[string]string{"README": "x\n"}, argv: []string{"go", "build", "-o", "README", "./cmd/a"}, failure: `go: build output "README" already exists and is not an object file`},
		{name: "-o names a binary", files: map[string]string{"bin/a": machO}, argv: []string{"go", "build", "-o", "bin/a", "./cmd/a"}},
		{name: "-o names a directory", dirs: []string{"bin"}, argv: []string{"go", "build", "-o", "bin", "./cmd/..."}, refusal: "-o bin is an existing directory"},
		{name: "-o directory exists", dirs: []string{"bin"}, argv: []string{"go", "build", "-o", "bin/", "./cmd/..."}, refusal: "-o bin/ already exists"},
		{name: "-o directory is a file", files: map[string]string{"bin": machO}, argv: []string{"go", "build", "-o", "bin/", "./cmd/..."}, refusal: "-o bin/ already exists"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			files := maps.Clone(goBuildOutputFiles)
			maps.Copy(files, tc.files)
			dir := writeGoTree(t, files)
			for _, sub := range tc.dirs {
				if err := os.MkdirAll(filepath.Join(dir, sub), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			plan, err := planRemoteBuild(dir, tc.argv)
			var failure *goBuildOutputFailure
			var refusal *remoteBuildOutputRefusal
			switch {
			case tc.failure != "":
				if !errors.As(err, &failure) || err.Error() != tc.failure {
					t.Fatalf("err = %v, want the failure %q", err, tc.failure)
				}
			case tc.refusal != "":
				if !errors.As(err, &refusal) || !strings.HasPrefix(refusal.reason, tc.refusal) || refusal.advice != remoteLocalOutputAdvice {
					t.Fatalf("err = %v, want a refusal starting %q", err, tc.refusal)
				}
			case err != nil || plan.output == "":
				t.Fatalf("plan %+v, %v", plan, err)
			}
		})
	}
	plugin, err := planRemoteBuild(writeGoTree(t, goBuildOutputFiles), []string{"go", "build", "-buildmode=plugin", "./cmd/a"})
	if err != nil || plugin.output != "a.so" || plugin.mode != 0o644 {
		t.Fatalf("plugin plan %+v, %v", plugin, err)
	}
	if _, err := planRemoteBuild(t.TempDir(), []string{filepath.Join(t.TempDir(), "missing-go"), "build", "./cmd/a"}); !errors.As(err, new(*remoteBuildOutputRefusal)) {
		t.Fatalf("a failed go list gave %v, want a refusal", err)
	}
}

// The route fails or refuses, before upload, a build whose output Go cannot
// write or the route cannot place.
func TestTryRemoteWorkChecksTheImplicitBinary(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("remote Go routing runs only on darwin")
	}
	root := moduleHintTree(t)
	dir := t.TempDir()
	if err := ConfigureRemoteWork(dir, true, "runner.example.test", "builder"); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"GOFLAGS", "GOEXPERIMENT", "CC", "CXX", "CGO_CFLAGS", "CGO_CPPFLAGS", "CGO_CXXFLAGS", "CGO_LDFLAGS", "CGO_ENABLED", "GOOS", "GOARCH"} {
		t.Setenv(key, "")
	}
	// In scripts/, the package directory ./tool is where Go would write the
	// binary of the tool package, so Go itself fails these builds.
	t.Chdir(filepath.Join(root, "scripts"))
	for _, argv := range [][]string{{"go", "build", "./tool"}, {"go", "build", "./..."}} {
		handled, code, err := TryRemoteWork(dir, WorkRunOptions{Class: WorkClassBuild, Command: argv}, WorkRunStreams{Stderr: &strings.Builder{}})
		if !handled || code != 1 || IsWorkRefusal(err) || err == nil || err.Error() != `go: build output "tool" already exists and is a directory` {
			t.Fatalf("%q: handled=%v code=%d err=%v", argv, handled, code, err)
		}
	}
	handled, code, err := TryRemoteWork(dir, WorkRunOptions{Class: WorkClassBuild, Command: []string{"go", "build", "-o", "tool", "./tool"}}, WorkRunStreams{Stderr: &strings.Builder{}})
	if !handled || code != workExitPolicyDenied || !IsWorkRefusal(err) || !strings.Contains(err.Error(), "-o tool is an existing directory") || !strings.Contains(err.Error(), remoteLocalOutputAdvice) {
		t.Fatalf("-o directory: handled=%v code=%d err=%v", handled, code, err)
	}
	// The failures name the probe that found the binary; the refusal of an
	// explicit -o ran no probe and records none.
	body, err := os.ReadFile(filepath.Join(dir, "work-control-events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(body)), "\n")
	if len(lines) != 3 {
		t.Fatalf("control events:\n%s", body)
	}
	for i, line := range lines {
		failed := strings.Contains(line, `"event":"remote_work_failed"`) && strings.Contains(line, `"build_output_probe":"go_list"`)
		declined := strings.Contains(line, `"event":"remote_work_declined"`) && !strings.Contains(line, "build_output_probe")
		if i < 2 && !failed || i == 2 && !declined {
			t.Fatalf("control event %d: %s", i, line)
		}
	}
}
