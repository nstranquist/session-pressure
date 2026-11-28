package sessionpressure

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

// moduleHintTree is a checkout with no go.mod at its root and two nested
// modules, scripts/go.mod and native/go.mod.
func moduleHintTree(t *testing.T) string {
	t.Helper()
	root := tempDirOutsideRepository(t)
	if module := enclosingGoModule(root); module != "" {
		t.Skipf("temporary directory %s is inside the Go module %s", root, module)
	}
	for path, body := range map[string]string{
		"scripts/go.mod":        "module example.test/scripts\ngo 1.26\n",
		"scripts/tool/main.go":  "package main\n",
		"scripts/tool/check.go": "package main\n",
		"native/go.mod":         "module example.test/native\ngo 1.26\n",
		"native/app/app.go":     "package app\n",
		"tools/loose.go":        "package main\n",
		"other/README":          "no module here\n",
	} {
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

func TestRemoteModuleHintNamesTheOwningModule(t *testing.T) {
	root := moduleHintTree(t)
	for _, tc := range []struct {
		name    string
		cwd     string
		workdir string // the directory of a leading -C; cwd when empty
		argv    []string
		hint    string
		modules []string
	}{{
		name:    "files of a nested module",
		argv:    []string{"go", "build", "-o", "tmp/bin/tool", "scripts/tool/main.go"},
		hint:    "the named files are in scripts/go.mod, so run the command from that module: (cd scripts && go build -o ../tmp/bin/tool tool/main.go)",
		modules: []string{"scripts/go.mod"},
	}, {
		name:    "package with an inline directory output",
		argv:    []string{"go", "build", "-o=tmp/bin/", "./scripts/tool"},
		hint:    "the named packages are in scripts/go.mod, so run the command from that module: (cd scripts && go build -o=../tmp/bin/ ./tool)",
		modules: []string{"scripts/go.mod"},
	}, {
		name: "pattern, test flags and test binary arguments",
		argv: []string{"go", "test", "-count=1", "-run", "TestX", "-pkgdir", "tmp/pkg", "./scripts/...", "-args", "-update", "x/y"},
		hint: "the named packages are in scripts/go.mod, so run the command from that module: (cd scripts && go test -count=1 -run TestX -pkgdir ../tmp/pkg ./... -args -update x/y)",
	}, {
		name: "module root, an empty value and a quoted value",
		argv: []string{"go", "test", "-run", "", "-ldflags", "-s -w", "./scripts"},
		hint: "the named packages are in scripts/go.mod, so run the command from that module: (cd scripts && go test -run '' -ldflags '-s -w' .)",
	}, {
		// The route follows a leading -C, so argv no longer has it and paths
		// are read from workdir; the command is spelled for the caller in cwd.
		name:    "paths read from the -C directory",
		workdir: "other",
		argv:    []string{"go", "build", "-o", "bin/lc", "../scripts/tool/main.go"},
		hint:    "the named files are in scripts/go.mod, so run the command from that module: (cd scripts && go build -o ../other/bin/lc tool/main.go)",
		modules: []string{"scripts/go.mod"},
	}, {
		name:    "packages in two modules",
		argv:    []string{"go", "vet", "./scripts/...", "./native/app"},
		hint:    "the named packages are in 2 modules (scripts/go.mod, native/go.mod); run one command in each: (cd scripts && go vet ./...) and (cd native && go vet ./app)",
		modules: []string{"scripts/go.mod", "native/go.mod"},
	}, {
		name: "one output cannot be split across two modules",
		argv: []string{"go", "build", "-o", "tmp/bin/", "./scripts/tool", "./native/app"},
		hint: "the named packages are in 2 modules (scripts/go.mod, native/go.mod); run a separate command in each module directory",
	}, {
		name:    "a module beside the working directory",
		cwd:     "other",
		argv:    []string{"go", "build", "-o", "bin/lc", "../scripts/tool/main.go"},
		hint:    "the named files are in ../scripts/go.mod, so run the command from that module: (cd ../scripts && go build -o ../other/bin/lc tool/main.go)",
		modules: []string{"../scripts/go.mod"},
	}, {
		// Go writes the binary of one main package into the working directory.
		name: "one main package keeps its binary where Go wrote it",
		argv: []string{"go", "build", "./scripts/tool"},
		hint: "the named packages are in scripts/go.mod, so run the command from that module: (cd scripts && go build -o ../tool ./tool)",
	}, {
		name: "main files keep their binary where Go wrote it",
		argv: []string{"go", "build", "scripts/tool/main.go"},
		hint: "the named files are in scripts/go.mod, so run the command from that module: (cd scripts && go build -o ../main tool/main.go)",
	}, {
		name: "a library package writes nothing",
		argv: []string{"go", "build", "./native/app"},
		hint: "the named packages are in native/go.mod, so run the command from that module: (cd native && go build ./app)",
	}, {
		// The remote route runs every pattern build as a check and returns no
		// binary, so the suggested command adds no -o.
		name: "a pattern build stays a check",
		argv: []string{"go", "build", "./scripts/..."},
		hint: "the named packages are in scripts/go.mod, so run the command from that module: (cd scripts && go build ./...)",
	}, {

		name: "coverage and per-package patterns move, keywords and import paths stay",
		argv: []string{"go", "test", "-coverpkg=./scripts/...,fmt", "-gcflags=./scripts/tool=-N -l", "-ldflags=all=-s", "./scripts/..."},
		hint: "the named packages are in scripts/go.mod, so run the command from that module: (cd scripts && go test -coverpkg=./...,fmt '-gcflags=./tool=-N -l' -ldflags=all=-s ./...)",
	}, {
		// go test passes the arguments after its package list to the test binary.
		name:    "go test arguments after the package list stay as written",
		argv:    []string{"go", "test", "./scripts/tool", "-v", "./native/app"},
		hint:    "the named packages are in scripts/go.mod, so run the command from that module: (cd scripts && go test ./tool -v ./native/app)",
		modules: []string{"scripts/go.mod"},
	}, {
		// The value of an unknown flag stays; go test reads its own flags after it.
		name: "go test flags after a test binary flag value still move",
		argv: []string{"go", "test", "./scripts/tool", "-update", "golden", "-pkgdir", "tmp/pkg"},
		hint: "the named packages are in scripts/go.mod, so run the command from that module: (cd scripts && go test ./tool -update golden -pkgdir ../tmp/pkg)",
	}, {
		// Only the flags go test passes to the test binary take a test. prefix.
		name: "test. and double-dash spellings",
		argv: []string{"go", "test", "-test.run=TestX", "--pkgdir", "tmp/pkg", "./scripts/tool"},
		hint: "the named packages are in scripts/go.mod, so run the command from that module: (cd scripts && go test -test.run=TestX --pkgdir ../tmp/pkg ./tool)",
	}, {
		// zsh expands a word that starts with "=" to a command path.
		name: "a value that starts with = is quoted",
		argv: []string{"go", "test", "-run", "=X", "./scripts"},
		hint: "the named packages are in scripts/go.mod, so run the command from that module: (cd scripts && go test -run '=X' .)",
	}, {
		name: "a split build could write a binary the one build does not",
		argv: []string{"go", "build", "./scripts/tool", "./native/app"},
		hint: "the named packages are in 2 modules (scripts/go.mod, native/go.mod); run a separate command in each module directory",
	}} {
		t.Run(tc.name, func(t *testing.T) {
			cwd, workdir := filepath.Join(root, tc.cwd), filepath.Join(root, tc.cwd)
			if tc.workdir != "" {
				workdir = filepath.Join(root, tc.workdir)
			}
			hint, modules := remoteModuleHint(cwd, workdir, tc.argv)
			if hint != tc.hint {
				t.Fatalf("hint\n got %q\nwant %q", hint, tc.hint)
			}
			if tc.modules != nil && !reflect.DeepEqual(modules, tc.modules) {
				t.Fatalf("modules = %q, want %q", modules, tc.modules)
			}
		})
	}
}

// Without one owning module for every argument the generic refusal stays.
func TestRemoteModuleHintKeepsTheGenericRefusal(t *testing.T) {
	root := moduleHintTree(t)
	for _, argv := range [][]string{
		{"go", "test", "./..."},                      // the module-less root itself
		{"go", "vet", "fmt"},                         // an import path
		{"go", "build", "-o", "x"},                   // the module-less directory
		{"go", "build", "-o", "x", "tools/loose.go"}, // a file outside any module
		{"go", "build", "-o", "x", "scripts/tool/missing.go"},
		{"go", "build", "-o", "x", "./scripts/missing"},
		{"go", "build", "-o", "x", "./scripts/tool", "./tools"},
		{"go", "build", "-o", "x", "--", "./scripts/tool"},
		// A -C reaches the planner only when the route could not follow it.
		{"go", "build", "-o", "x", "./scripts/tool", "-C", "scripts"}, // Go rejects a later -C
		{"go", "build", "-C", "missing", "-o", "x", "."},
		{"go", "build", "-C="},
		{"go", "build", "-o"},
		{"go", "test", "./scripts/...x"},
		{"go", "build", "./scripts"},                                // a directory with no Go files
		{"go", "build", "./scripts/tool", "-o", "x"},                // go build reads -o as a package
		{"go", "vet", "./scripts/...", "-v"},                        // so does go vet
		{"go", "test", "-update", "./scripts/tool"},                 // the value of a test binary flag
		{"go", "test", "-coverpkg=./native/...", "./scripts/..."},   // a pattern outside the module
		{"go", "test", "-gcflags=./native/...=-N", "./scripts/..."}, // the same in a per-package flag
		// The remote route refuses the go test outputs before the module
		// check, so the planner never spells them.
		{"go", "test", "-coverprofile", "c.out", "./scripts/tool"},
		{"go", "test", "-c", "./scripts/tool"},
		{"go", "test", "-o", "tool.test", "./scripts/tool"},
		{"go", "build", "-buildmode=plugin", "./scripts/tool"},  // another default name
		{"go", "build", "-o", "a", "-o", "b", "./scripts/tool"}, // Go keeps the last -o
		{"go", "build", "-o=", "./scripts/tool"},                // an empty -o is no -o
		{"go", "build", "-race=maybe", "./scripts/tool"},        // Go rejects the boolean
		{"go", "build", "-tags", "a b", "./scripts/tool"},       // the deprecated tag list
	} {
		if hint, modules := remoteModuleHint(root, root, argv); hint != "" || modules != nil {
			t.Errorf("%q: hint %q, modules %q; want the generic refusal", argv, hint, modules)
		}
	}
}

func TestRemoteModuleRefusalKeepsTheRefusalContract(t *testing.T) {
	root := moduleHintTree(t)
	dir := t.TempDir()
	handled, code, err := rejectRemoteModule(dir, root, root, []string{"go", "build", "-o", "tmp/bin/tool", "scripts/tool/main.go"})
	if !handled || code != workExitPolicyDenied || !IsWorkRefusal(err) {
		t.Fatalf("handled=%v code=%d err=%v", handled, code, err)
	}
	text := err.Error()
	for _, want := range []string{
		WorkRefusalMarker + " kind=" + WorkRefusalRemoteProfile + ": " + WorkRefusalNotRun,
		"remote Go work blocked: remote Go work requires a module; the named files are in scripts/go.mod",
		"(cd scripts && go build -o ../tmp/bin/tool tool/main.go); local fallback is disabled",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("refusal %q lacks %q", text, want)
		}
	}
	if strings.Contains(text, remoteRunnerAdvice) {
		t.Fatalf("refusal %q still sends the caller to a runner profile", text)
	}
	events, err := os.ReadFile(filepath.Join(dir, "work-control-events.jsonl"))
	if err != nil || !strings.Contains(string(events), `"module_hint":"scripts/go.mod"`) || !strings.Contains(string(events), `"reason":"remote Go work requires a module"`) {
		t.Fatalf("control event = %s (%v)", events, err)
	}

	_, code, err = rejectRemoteModule(t.TempDir(), root, root, []string{"go", "test", "./..."})
	if code != workExitPolicyDenied || err == nil || !strings.Contains(err.Error(), "remote Go work blocked: remote Go work requires a module; "+remoteRunnerAdvice+"; local fallback is disabled") {
		t.Fatalf("generic refusal changed: code=%d err=%v", code, err)
	}
}

// The real entry point gives the hint, before any upload or placement.
func TestTryRemoteWorkNamesTheNestedModule(t *testing.T) {
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
	t.Chdir(root)
	for _, tc := range []struct {
		argv []string
		want string
	}{
		{[]string{"go", "build", "-o", "tmp/bin/tool", "scripts/tool/main.go"}, "(cd scripts && go build -o ../tmp/bin/tool tool/main.go)"},
		// The route follows a leading -C into the module-less directory,
		// as Go does, and names the module from there.
		{[]string{"go", "build", "-C", "other", "-o", "bin/lc", "../scripts/tool/main.go"}, "(cd scripts && go build -o ../other/bin/lc tool/main.go)"},
	} {
		options := WorkRunOptions{Class: WorkClassBuild, Command: tc.argv}
		handled, code, err := TryRemoteWork(dir, options, WorkRunStreams{Stderr: &strings.Builder{}})
		if !handled || code != workExitPolicyDenied || err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%q: handled=%v code=%d err=%v", tc.argv, handled, code, err)
		}
	}
}

// A file the route cannot return is refused with local-placement advice,
// not the runner advice, before any upload.
func TestTryRemoteWorkRefusesOutputsWithLocalAdvice(t *testing.T) {
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
	t.Chdir(filepath.Join(root, "scripts"))
	for _, tc := range []struct {
		class WorkClass
		argv  []string
		want  string
	}{
		{WorkClassTest, []string{"go", "test", "-coverprofile", "c.out", "./..."}, "argument -coverprofile makes go test write a file that the remote route does not return"},
		{WorkClassBuild, []string{"go", "build", "-buildmode=c-shared", "-o", "lib.so", "./tool"}, "argument -buildmode=c-shared also writes a C header that the remote route does not return"},
	} {
		handled, code, err := TryRemoteWork(dir, WorkRunOptions{Class: tc.class, Command: tc.argv}, WorkRunStreams{Stderr: &strings.Builder{}})
		if !handled || code != workExitPolicyDenied || !IsWorkRefusal(err) {
			t.Fatalf("%q: handled=%v code=%d err=%v", tc.argv, handled, code, err)
		}
		if text := err.Error(); !strings.Contains(text, tc.want+"; "+remoteLocalOutputAdvice+"; local fallback is disabled") || strings.Contains(text, remoteRunnerAdvice) {
			t.Fatalf("%q: refusal %q", tc.argv, text)
		}
	}
}

// A followed -C gives a physical directory; through a symlinked $PWD the
// hint still shows a path below the caller's directory.
func TestShownDirReadsThroughASymlinkedWorkingDirectory(t *testing.T) {
	physical := tempDirOutsideRepository(t)
	link := filepath.Join(tempDirOutsideRepository(t), "link")
	if err := os.Symlink(physical, link); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ dir, want string }{
		{filepath.Join(physical, "scripts"), "scripts"},
		{physical, "."},
		{filepath.Join(link, "scripts"), "scripts"},
		// Above the physical directory, a shell's cd .. could go elsewhere.
		{filepath.Join(filepath.Dir(physical), "other"), filepath.Join(filepath.Dir(physical), "other")},
	} {
		if got := shownDir(link, tc.dir); got != tc.want {
			t.Errorf("shownDir(%s, %s) = %q, want %q", link, tc.dir, got, tc.want)
		}
	}
}

// remoteWorkDirectory follows a -C the way cmd/go does: only as the first
// flag, to the physical directory, and with the flag taken out of argv.
func TestRemoteWorkDirectoryFollowsALeadingChdir(t *testing.T) {
	root := moduleHintTree(t)
	if err := os.Symlink(filepath.Join(root, "scripts"), filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	scripts := filepath.Join(root, "scripts")
	for _, tc := range []struct {
		argv    []string
		workdir string
		rest    []string
	}{
		{[]string{"go", "build", "-C", "scripts", "-o", "x", "./tool"}, scripts, []string{"go", "build", "-o", "x", "./tool"}},
		{[]string{"go", "build", "--C=scripts", "./tool"}, scripts, []string{"go", "build", "./tool"}},
		{[]string{"go", "test", "-C", scripts, "./...", "-args", "-C", "x"}, scripts, []string{"go", "test", "./...", "-args", "-C", "x"}},
		{[]string{"go", "vet", "-C", "link", "./..."}, scripts, []string{"go", "vet", "./..."}},
		// Go rejects these or fails to change directory; argv stays.
		{[]string{"go", "build", "-o", "x", "-C", "scripts", "."}, root, nil},
		{[]string{"go", "build", "-C", "scripts", "-C", "native", "."}, root, nil},
		{[]string{"go", "build", "-C="}, root, nil},
		{[]string{"go", "build", "-C", "missing", "."}, root, nil},
		{[]string{"go", "build", "-C", "other/README", "."}, root, nil},
		{[]string{"go", "build"}, root, nil},
	} {
		workdir, rest := remoteWorkDirectory(root, tc.argv)
		want := tc.rest
		if want == nil {
			want = tc.argv
		}
		if workdir != tc.workdir || !reflect.DeepEqual(rest, want) {
			t.Errorf("%q: workdir %q argv %q, want %q %q", tc.argv, workdir, rest, tc.workdir, want)
		}
	}
}

// The added -o names the file cmd/go would write (DefaultExecName).
func TestRemoteModuleHintNamesTheDefaultBinary(t *testing.T) {
	root := tempDirOutsideRepository(t)
	for path, body := range map[string]string{
		"mod/go.mod":             "module example.test/cmd/v2\ngo 1.26\n",
		"mod/main.go":            "package main\n",
		"mod/tool/main.go":       "package main\n",
		"mod/v3/main.go":         "package main\n",
		"mod/files/a_cgo.go":     "package main\n\nimport \"C\"\n",
		"mod/files/b_ignored.go": "//go:build ignore\n\npackage main\n",
		"mod/files/c_plain.go":   "package main\n",
		"mod/files/d_last.go":    "package main\n",
		// go build leaves out a file its constraints exclude and a file whose
		// name starts with "_", so this package is a library.
		"mod/lib/lib.go":      "package lib\n",
		"mod/lib/gen.go":      "//go:build ignore\n\npackage main\n",
		"mod/lib/_old.go":     "package main\n",
		"mod/tagged/main.go":  "//go:build special\n\npackage main\n",
		"mod/named/_skip.go":  "package main\n",
		"mod/named/a_test.go": "package main\n",
		"mod/named/doc.go":    "package documentation\n",
		"mod/named/real.go":   "package main\n",
		"-mod/go.mod":         "module example.test/dash\ngo 1.26\n",
		"-mod/app/app.go":     "package app\n",
	} {
		full := filepath.Join(root, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		argv []string
		want string
	}{
		{[]string{"go", "build", "./mod"}, "(cd mod && go build -o ../cmd .)"},            // the v2 element is skipped
		{[]string{"go", "build", "./mod/tool"}, "(cd mod && go build -o ../tool ./tool)"}, // the last element
		{[]string{"go", "build", "./mod/v3"}, "(cd mod && go build -o ../v2 ./v3)"},       // cmd/go skips v3 too
		{[]string{"go", "build", "mod/files/a_cgo.go", "mod/files/b_ignored.go", "mod/files/c_plain.go", "mod/files/d_last.go"},
			"(cd mod && go build -o ../b_ignored files/a_cgo.go files/b_ignored.go files/c_plain.go files/d_last.go)"},
		{[]string{"go", "build", "./mod/lib"}, "(cd mod && go build ./lib)"},
		{[]string{"go", "build", "-tags", "special", "./mod/tagged"}, "(cd mod && go build -o ../tagged -tags special ./tagged)"},
		{[]string{"go", "build", "--tags=x,special", "./mod/tagged"}, "(cd mod && go build -o ../tagged --tags=x,special ./tagged)"},
		// Named files skip the _, test and documentation files, as cmd/go does.
		{[]string{"go", "build", "mod/named/_skip.go", "mod/named/a_test.go", "mod/named/doc.go", "mod/named/real.go"},
			"(cd mod && go build -o ../real named/_skip.go named/a_test.go named/doc.go named/real.go)"},
		{[]string{"go", "vet", "./-mod/..."}, "(cd ./-mod && go vet ./...)"},
	} {
		hint, _ := remoteModuleHint(root, root, tc.argv)
		if !strings.HasSuffix(hint, tc.want) {
			t.Errorf("%q: hint %q, want it to end with %q", tc.argv, hint, tc.want)
		}
	}
	// Go refuses a default output that is an existing directory.
	if err := os.Mkdir(filepath.Join(root, "tool"), 0o700); err != nil {
		t.Fatal(err)
	}
	if hint, _ := remoteModuleHint(root, root, []string{"go", "build", "./mod/tool"}); hint != "" {
		t.Errorf("hint %q over an existing directory, want the generic refusal", hint)
	}
	// Without its tag the package has no files, and Go fails.
	if hint, _ := remoteModuleHint(root, root, []string{"go", "build", "./mod/tagged"}); hint != "" {
		t.Errorf("hint %q for a package with no selected files, want the generic refusal", hint)
	}
	// GOFLAGS selects files too; the command line overrides it.
	t.Setenv("GOFLAGS", "-tags=special")
	if hint, _ := remoteModuleHint(root, root, []string{"go", "build", "./mod/tagged"}); !strings.HasSuffix(hint, "(cd mod && go build -o ../tagged ./tagged)") {
		t.Errorf("hint %q with GOFLAGS tags, want the tagged binary", hint)
	}
	if hint, _ := remoteModuleHint(root, root, []string{"go", "build", "-tags=", "./mod/tagged"}); hint != "" {
		t.Errorf("hint %q with the GOFLAGS tags cleared, want the generic refusal", hint)
	}
}

// go build resolves -o from the physical directory and go test resolves its
// paths from $PWD, so through a symlink a moved path has no exact rewrite.
func TestRemoteModuleHintKeepsTheGenericRefusalThroughASymlink(t *testing.T) {
	root := moduleHintTree(t)
	elsewhere := tempDirOutsideRepository(t)
	for path, body := range map[string]string{
		"mod/go.mod":       "module example.test/mod\ngo 1.26\n",
		"mod/tool/main.go": "package main\n",
	} {
		full := filepath.Join(elsewhere, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(filepath.Join(elsewhere, "mod"), filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	for _, argv := range [][]string{
		{"go", "build", "-o", "tmp/bin/tool", "./link/tool"},
		{"go", "build", "./link/tool"},
		{"go", "test", "-pkgdir", "tmp/pkg", "./link/tool"},
	} {
		if hint, modules := remoteModuleHint(root, root, argv); hint != "" || modules != nil {
			t.Errorf("%q: hint %q, modules %q; want the generic refusal", argv, hint, modules)
		}
	}
	hint, _ := remoteModuleHint(root, root, []string{"go", "vet", "./link/..."})
	if want := "the named packages are in link/go.mod, so run the command from that module: (cd link && go vet ./...)"; hint != want {
		t.Fatalf("hint\n got %q\nwant %q", hint, want)
	}
}

// The express lane reads the spellings that goPackageTargets reads, so a
// flag value it skips as a package is never missed as an output, a directory
// or a full-weight mode.
func TestExpressReadsEveryGoFlagSpelling(t *testing.T) {
	dir := tempDirOutsideRepository(t)
	for path, body := range map[string]string{"go.mod": "module example.test/x\ngo 1.26\n", "app/app.go": "package app\n"} {
		full := filepath.Join(dir, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, argv := range [][]string{
		{"go", "build", "--o", "out.a", "./app"},
		{"go", "build", "--o=out.a", "./app"},
		{"go", "build", "-tags", "x", "-o", "out.a", "./app"},
	} {
		paths, ok, err := expressDeclaredArtifactPaths(argv, dir)
		if err != nil || !ok || !reflect.DeepEqual(paths, []string{filepath.Join(dir, "out.a")}) {
			t.Errorf("%q: artifacts %q ok=%v err=%v; want the -o output", argv, paths, ok, err)
		}
	}
	if expressWritesUndeclaredBinary([]string{"go", "build", "--o", "out.a", "./app"}, dir) {
		t.Error("--o was not read as an output")
	}
	for _, args := range [][]string{{"--C", "app", "."}, {"--C=app", "."}, {"-C", "app", "."}} {
		if got, ok := expressEffectiveWorkDir(args, dir); !ok || got != filepath.Join(dir, "app") {
			t.Errorf("%q: workdir %q ok=%v, want %s", args, got, ok, filepath.Join(dir, "app"))
		}
	}
	for _, args := range [][]string{{"--coverprofile", "c.out", "./app"}, {"-test.cpuprofile=c.out", "./app"}, {"--race", "./app"}} {
		if !goTestRequiresFullWeight(args) {
			t.Errorf("%q: not full weight", args)
		}
	}
	for _, args := range [][]string{{"-test.bench", "."}, {"--bench=X"}} {
		if !goTestRunsBenchmark(args) {
			t.Errorf("%q: benchmark not seen", args)
		}
	}
}

// Every Go argument parser in the package reads goFlagsWithValue, so the value
// of a flag is never taken for a package, a path or a benchmark selector.
func TestGoArgumentParsersShareOneFlagTable(t *testing.T) {
	args := []string{"-shuffle", "on", "-vet", "off", "-buildmode", "pie", "--tags", "x", "./pkg"}
	if got := goPackageTargets(args); !reflect.DeepEqual(got, []string{"./pkg"}) {
		t.Fatalf("packages = %q, want [./pkg]", got)
	}
	var positionals []string
	for _, arg := range splitGoArgs(args) {
		if arg.flag == "" {
			positionals = append(positionals, arg.value)
		}
	}
	if !reflect.DeepEqual(positionals, []string{"./pkg"}) {
		t.Fatalf("splitGoArgs positionals = %q, want [./pkg]", positionals)
	}
	if goTestRunsBenchmark([]string{"-list", "-bench", "./pkg"}) {
		t.Fatal("the value of -list was read as a -bench selector")
	}
}
