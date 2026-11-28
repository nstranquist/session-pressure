package sessionpressure

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func TestRemoteModuleClosureAndSourceBoundaries(t *testing.T) {
	base := tempDirOutsideRepository(t)
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

func TestPlanRemoteBuildArgv(t *testing.T) {
	// A module with one main package, ./cmd/x, and two libraries.
	workdir := writeGoTree(t, map[string]string{
		"go.mod":        "module example.test/plan\ngo 1.26\n",
		"cmd/x/main.go": "package main\n\nfunc main() {}\n",
		"a/a.go":        "package a\n",
		"b/b.go":        "package b\n",
	})
	const out = remoteBuildCandidate
	tests := []struct {
		name       string
		argv       []string
		wantArgv   []string
		wantOutput string
		wantKind   string
	}{
		// No package at the module root: Go fails and writes nothing.
		{name: "bare build", argv: []string{"go", "build"}, wantArgv: []string{"go", "build"}, wantKind: "none"},
		{name: "build dot", argv: []string{"go", "build", "."}, wantArgv: []string{"go", "build", "."}, wantKind: "none"},
		// Go writes the binary of one main package into the working directory.
		{name: "build single main package", argv: []string{"go", "build", "./cmd/x"}, wantArgv: []string{"go", "build", "-o", out, "./cmd/x"}, wantOutput: "x", wantKind: "implicit"},
		{name: "pattern of one main package", argv: []string{"go", "build", "./cmd/..."}, wantArgv: []string{"go", "build", "-o", out, "./cmd/..."}, wantOutput: "x", wantKind: "implicit"},
		{name: "explicit -o separate", argv: []string{"go", "build", "-o", "bin/app", "./cmd/x"}, wantArgv: []string{"go", "build", "-o", out, "./cmd/x"}, wantOutput: "bin/app", wantKind: "explicit"},
		{name: "explicit -o equals", argv: []string{"go", "build", "-o=bin/app", "./cmd/x"}, wantArgv: []string{"go", "build", "-o=" + out, "./cmd/x"}, wantOutput: "bin/app", wantKind: "explicit"},
		{name: "explicit --o separate", argv: []string{"go", "build", "--o", "bin/app", "./cmd/x"}, wantArgv: []string{"go", "build", "--o", out, "./cmd/x"}, wantOutput: "bin/app", wantKind: "explicit"},
		{name: "explicit --o equals directory", argv: []string{"go", "build", "--o=bin/", "./cmd/x"}, wantArgv: []string{"go", "build", "--o=" + out + "/", "./cmd/x"}, wantOutput: "bin/", wantKind: "directory"},
		{name: "-o after a value flag", argv: []string{"go", "build", "-tags", "x", "-o", "bin/app", "./cmd/x"}, wantArgv: []string{"go", "build", "-tags", "x", "-o", out, "./cmd/x"}, wantOutput: "bin/app", wantKind: "explicit"},
		// The value of another flag is never -o; the added -o follows every flag.
		{name: "-o as a flag value", argv: []string{"go", "build", "-ldflags", "-o", "./cmd/x"}, wantArgv: []string{"go", "build", "-ldflags", "-o", "-o", out, "./cmd/x"}, wantOutput: "x", wantKind: "implicit"},
		// After "--" every argument is a package; Go fails on "-o".
		{name: "-o after --", argv: []string{"go", "build", "--", "-o", "bin/app"}, wantArgv: []string{"go", "build", "--", "-o", "bin/app"}, wantKind: "none"},
		// Go stops reading flags at the first package.
		{name: "-o after a package", argv: []string{"go", "build", "./cmd/x", "-o", "bin/app"}, wantArgv: []string{"go", "build", "./cmd/x", "-o", "bin/app"}, wantKind: "none"},
		{name: "multi-package check-only", argv: []string{"go", "build", "./..."}, wantArgv: []string{"go", "build", "./..."}, wantKind: "none"},
		{name: "two libraries", argv: []string{"go", "build", "./a", "./b"}, wantArgv: []string{"go", "build", "./a", "./b"}, wantKind: "none"},
		{name: "a main and a library", argv: []string{"go", "build", "./cmd/x", "./a"}, wantArgv: []string{"go", "build", "./cmd/x", "./a"}, wantKind: "none"},
		{name: "non-build untouched", argv: []string{"go", "test", "./..."}, wantArgv: []string{"go", "test", "./..."}},
		// Go keeps the last -o, and an empty -o is no -o.
		{name: "last -o wins", argv: []string{"go", "build", "-o", "a", "-o=bin/app", "./cmd/x"}, wantArgv: []string{"go", "build", "-o", "a", "-o=" + out, "./cmd/x"}, wantOutput: "bin/app", wantKind: "explicit"},
		{name: "empty -o is the default output", argv: []string{"go", "build", "-o=", "./cmd/x"}, wantArgv: []string{"go", "build", "-o=", "-o", out, "./cmd/x"}, wantOutput: "x", wantKind: "implicit"},
		{name: "last -o empty", argv: []string{"go", "build", "-o", "bin/app", "-o", "", "./cmd/x"}, wantArgv: []string{"go", "build", "-o", "bin/app", "-o", "", "-o", out, "./cmd/x"}, wantOutput: "x", wantKind: "implicit"},
		{name: "empty -o pattern check", argv: []string{"go", "build", "-o", "", "./..."}, wantArgv: []string{"go", "build", "-o", "", "./..."}, wantKind: "none"},
		{name: "/dev/null stays", argv: []string{"go", "build", "-o", "/dev/null", "./cmd/x"}, wantArgv: []string{"go", "build", "-o", "/dev/null", "./cmd/x"}, wantKind: "discard"},
		{name: "pattern in a flag value", argv: []string{"go", "build", "-gcflags=./...=-N", "./cmd/x"}, wantArgv: []string{"go", "build", "-gcflags=./...=-N", "-o", out, "./cmd/x"}, wantOutput: "x", wantKind: "implicit"},
		{name: "pattern after --", argv: []string{"go", "build", "--", "./..."}, wantArgv: []string{"go", "build", "--", "./..."}, wantKind: "none"},
		// -n prints the commands and writes nothing, also with -o.
		{name: "-n with -o", argv: []string{"go", "build", "-n", "-o", "bin/app", "./cmd/x"}, wantArgv: []string{"go", "build", "-n", "-o", "bin/app", "./cmd/x"}, wantKind: "none"},
		{name: "-n without -o", argv: []string{"go", "build", "-n", "./cmd/x"}, wantArgv: []string{"go", "build", "-n", "./cmd/x"}, wantKind: "none"},
		{name: "-n=false", argv: []string{"go", "build", "-n=false", "./cmd/x"}, wantArgv: []string{"go", "build", "-n=false", "-o", out, "./cmd/x"}, wantOutput: "x", wantKind: "implicit"},
		{name: "a value flag without its value", argv: []string{"go", "build", "-tags"}, wantArgv: []string{"go", "build", "-tags"}, wantKind: "none"},
		{name: "-json goes to go build only", argv: []string{"go", "build", "-json", "./cmd/x"}, wantArgv: []string{"go", "build", "-json", "-o", out, "./cmd/x"}, wantOutput: "x", wantKind: "implicit"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			plan, err := planRemoteBuild(workdir, tc.argv)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if plan.output != tc.wantOutput || plan.kind != tc.wantKind {
				t.Errorf("output = %q kind = %q, want %q %q", plan.output, plan.kind, tc.wantOutput, tc.wantKind)
			}
			if !reflect.DeepEqual(plan.argv, tc.wantArgv) {
				t.Fatalf("argv = %q, want %q", plan.argv, tc.wantArgv)
			}
		})
	}
}

func TestRemoteDeliveryLineageForwardsOnlyValidIDs(t *testing.T) {
	env := map[string]string{
		"NDEV_COMMIT_DELIVERY_ID":     "35513d6a-fb2a-45cf-a3dd-e87fbbc389cd",
		"NDEV_COMMIT_DELIVERY_RUN_ID": "2e500cbf-02d5-442f-a7a3-740659369d57",
	}
	got := remoteDeliveryLineage(func(k string) string { return env[k] })
	want := " NCD_DELIVERY_ID=35513d6a-fb2a-45cf-a3dd-e87fbbc389cd NCD_RUN_ID=2e500cbf-02d5-442f-a7a3-740659369d57"
	if got != want {
		t.Fatalf("lineage = %q, want %q", got, want)
	}
	env["NDEV_COMMIT_DELIVERY_ID"] = "x; rm -rf /"
	env["NDEV_COMMIT_DELIVERY_RUN_ID"] = ""
	if got := remoteDeliveryLineage(func(k string) string { return env[k] }); got != "" {
		t.Fatalf("invalid IDs forwarded: %q", got)
	}
}

func TestRemoteModuleClosureNamesMissingReplaceTarget(t *testing.T) {
	write := func(t *testing.T, path, body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("direct missing directory", func(t *testing.T) {
		base := tempDirOutsideRepository(t)
		a := filepath.Join(base, "a")
		missing := filepath.Join(base, "b")
		write(t, filepath.Join(a, "go.mod"), "module example.test/a\ngo 1.26\nreplace example.test/b => ../b\n")

		_, _, err := remoteModuleClosure(a)
		var replaceErr *remoteReplaceTargetError
		if !errors.As(err, &replaceErr) {
			t.Fatalf("err = %T (%v), want *remoteReplaceTargetError", err, err)
		}
		want := "go.mod: replace example.test/b => ../b: replacement directory " + missing + " does not exist"
		if err.Error() != want {
			t.Fatalf("err\n got %q\nwant %q", err.Error(), want)
		}
	})

	t.Run("versioned replace missing directory", func(t *testing.T) {
		base := tempDirOutsideRepository(t)
		a := filepath.Join(base, "a")
		missing := filepath.Join(base, "b")
		write(t, filepath.Join(a, "go.mod"), "module example.test/a\ngo 1.26\nreplace example.test/b v1.2.3 => ../b\n")

		_, _, err := remoteModuleClosure(a)
		var replaceErr *remoteReplaceTargetError
		if !errors.As(err, &replaceErr) {
			t.Fatalf("err = %T (%v), want *remoteReplaceTargetError", err, err)
		}
		want := "go.mod: replace example.test/b v1.2.3 => ../b: replacement directory " + missing + " does not exist"
		if err.Error() != want {
			t.Fatalf("err\n got %q\nwant %q", err.Error(), want)
		}
	})

	t.Run("directory exists without go.mod", func(t *testing.T) {
		base := tempDirOutsideRepository(t)
		a := filepath.Join(base, "a")
		b := filepath.Join(base, "b")
		write(t, filepath.Join(a, "go.mod"), "module example.test/a\ngo 1.26\nreplace example.test/b => ../b\n")
		if err := os.MkdirAll(b, 0o700); err != nil {
			t.Fatal(err)
		}

		_, _, err := remoteModuleClosure(a)
		var replaceErr *remoteReplaceTargetError
		if !errors.As(err, &replaceErr) {
			t.Fatalf("err = %T (%v), want *remoteReplaceTargetError", err, err)
		}
		want := "go.mod: replace example.test/b => ../b: replacement directory " + b + " has no go.mod"
		if err.Error() != want {
			t.Fatalf("err\n got %q\nwant %q", err.Error(), want)
		}
	})

	t.Run("target path is a regular file", func(t *testing.T) {
		base := tempDirOutsideRepository(t)
		a := filepath.Join(base, "a")
		fileTarget := filepath.Join(base, "b")
		write(t, filepath.Join(a, "go.mod"), "module example.test/a\ngo 1.26\nreplace example.test/b => ../b\n")
		write(t, fileTarget, "not a directory\n")

		_, _, err := remoteModuleClosure(a)
		var replaceErr *remoteReplaceTargetError
		if !errors.As(err, &replaceErr) {
			t.Fatalf("err = %T (%v), want *remoteReplaceTargetError", err, err)
		}
		want := "go.mod: replace example.test/b => ../b: replacement directory " + fileTarget + " is not a directory"
		if err.Error() != want {
			t.Fatalf("err\n got %q\nwant %q", err.Error(), want)
		}
	})

	t.Run("transitive missing replace names the declaring go.mod", func(t *testing.T) {
		base := tempDirOutsideRepository(t)
		a := filepath.Join(base, "a")
		b := filepath.Join(base, "b")
		missing := filepath.Join(base, "c")
		write(t, filepath.Join(a, "go.mod"), "module example.test/a\ngo 1.26\nreplace example.test/b => ../b\n")
		write(t, filepath.Join(b, "go.mod"), "module example.test/b\ngo 1.26\nreplace example.test/c => ../c\n")

		_, _, err := remoteModuleClosure(a)
		var replaceErr *remoteReplaceTargetError
		if !errors.As(err, &replaceErr) {
			t.Fatalf("err = %T (%v), want *remoteReplaceTargetError", err, err)
		}
		want := "../b/go.mod: replace example.test/c => ../c: replacement directory " + missing + " does not exist"
		if err.Error() != want {
			t.Fatalf("err\n got %q\nwant %q", err.Error(), want)
		}
	})

	t.Run("absolute missing replace target", func(t *testing.T) {
		base := tempDirOutsideRepository(t)
		a := filepath.Join(base, "a")
		missing := filepath.Join(base, "abs-missing")
		write(t, filepath.Join(a, "go.mod"), "module example.test/a\ngo 1.26\nreplace example.test/abs => "+missing+"\n")

		_, _, err := remoteModuleClosure(a)
		var replaceErr *remoteReplaceTargetError
		if !errors.As(err, &replaceErr) {
			t.Fatalf("err = %T (%v), want *remoteReplaceTargetError", err, err)
		}
		want := "go.mod: replace example.test/abs => " + missing + ": replacement directory " + missing + " does not exist"
		if err.Error() != want {
			t.Fatalf("err\n got %q\nwant %q", err.Error(), want)
		}
	})

	t.Run("non-local versioned replace is ignored", func(t *testing.T) {
		base := tempDirOutsideRepository(t)
		a := filepath.Join(base, "a")
		write(t, filepath.Join(a, "go.mod"), "module example.test/a\ngo 1.26\nreplace example.test/old => example.test/new v1.0.0\n")

		modules, root, err := remoteModuleClosure(a)
		if err != nil || len(modules) != 1 || root != a {
			t.Fatalf("modules=%v root=%q err=%v", modules, root, err)
		}
	})
}

func TestRemoteReplaceTargetFailsAsCommandNotRefusal(t *testing.T) {
	base := tempDirOutsideRepository(t)
	a := filepath.Join(base, "a")
	missing := filepath.Join(base, "b")
	if err := os.MkdirAll(a, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(a, "go.mod"), []byte("module example.test/a\ngo 1.26\nreplace example.test/b => ../b\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, _, closureErr := remoteModuleClosure(a)
	var replaceErr *remoteReplaceTargetError
	if !errors.As(closureErr, &replaceErr) {
		t.Fatalf("closureErr = %T (%v), want *remoteReplaceTargetError", closureErr, closureErr)
	}

	checkResult := func(t *testing.T, dir string, handled bool, code int, err error) {
		t.Helper()
		if !handled || code != 1 || err == nil {
			t.Fatalf("handled=%v code=%d err=%v, want handled=true code=1 non-nil err", handled, code, err)
		}
		if IsWorkRefusal(err) {
			t.Fatalf("missing replace target must not carry WorkRefusalMarker: %v", err)
		}
		text := err.Error()
		for _, forbidden := range []string{
			WorkRefusalMarker,
			"kind=" + WorkRefusalRemoteProfile,
			"remote Go work blocked:",
			remoteRunnerAdvice,
		} {
			if strings.Contains(text, forbidden) {
				t.Fatalf("error %q must not contain %q", text, forbidden)
			}
		}
		for _, want := range []string{
			"go.mod",
			"replace example.test/b => ../b",
			missing,
			"does not exist",
		} {
			if !strings.Contains(text, want) {
				t.Fatalf("error %q lacks %q", text, want)
			}
		}
		events, readErr := os.ReadFile(filepath.Join(dir, "work-control-events.jsonl"))
		if readErr != nil {
			t.Fatalf("read control events: %v", readErr)
		}
		body := string(events)
		if !strings.Contains(body, `"event":"remote_work_failed"`) || strings.Contains(body, `"event":"remote_work_declined"`) {
			t.Fatalf("unexpected control event: %s", body)
		}
		if !strings.Contains(body, `"go_mod":"go.mod"`) || !strings.Contains(body, `"replace_old":"example.test/b"`) || !strings.Contains(body, `"replace_new":"../b"`) {
			t.Fatalf("control event missing replace attrs: %s", body)
		}
	}

	dir := t.TempDir()
	handled, code, err := failRemoteReplaceTarget(dir, "build", replaceErr)
	checkResult(t, dir, handled, code, err)

	if runtime.GOOS == "darwin" {
		dir2 := t.TempDir()
		if err := ConfigureRemoteWork(dir2, true, "runner.example.test", "builder"); err != nil {
			t.Fatal(err)
		}
		for _, key := range []string{"GOFLAGS", "GOEXPERIMENT", "CC", "CXX", "CGO_CFLAGS", "CGO_CPPFLAGS", "CGO_CXXFLAGS", "CGO_LDFLAGS", "CGO_ENABLED", "GOOS", "GOARCH"} {
			t.Setenv(key, "")
		}
		t.Chdir(a)
		options := WorkRunOptions{Class: WorkClassBuild, Command: []string{"go", "build", "-o", "tmp/bin/app", "."}}
		handled2, code2, err2 := TryRemoteWork(dir2, options, WorkRunStreams{Stderr: &strings.Builder{}})
		checkResult(t, dir2, handled2, code2, err2)
	}
}
