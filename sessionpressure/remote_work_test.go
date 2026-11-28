package sessionpressure

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"os"
	"path/filepath"
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
		{name: "explicit --o separate", argv: []string{"go", "build", "--o", "bin/app", "./cmd/x"}, wantArgv: []string{"go", "build", "--o", "/srv/build/candidates/OUTPUT", "./cmd/x"}, wantOutput: "bin/app"},
		{name: "explicit --o equals directory", argv: []string{"go", "build", "--o=bin/", "./cmd/x"}, wantArgv: []string{"go", "build", "--o=/srv/build/candidates/OUTPUT/", "./cmd/x"}, wantOutput: "bin/"},
		{name: "-o after a value flag", argv: []string{"go", "build", "-tags", "x", "-o", "bin/app", "./cmd/x"}, wantArgv: []string{"go", "build", "-tags", "x", "-o", "/srv/build/candidates/OUTPUT", "./cmd/x"}, wantOutput: "bin/app"},
		{name: "-o as a flag value", argv: []string{"go", "build", "-ldflags", "-o", "./cmd/x"}, wantErr: true},
		{name: "-o after --", argv: []string{"go", "build", "--", "-o", "bin/app"}, wantErr: true},
		{name: "multi-package check-only", argv: []string{"go", "build", "./..."}, wantArgv: []string{"go", "build", "./..."}, wantOutput: ""},
		{name: "two packages without -o", argv: []string{"go", "build", "./a", "./b"}, wantArgv: []string{"go", "build", "./a", "./b"}, wantErr: true},
		{name: "non-build untouched", argv: []string{"go", "test", "./..."}, wantArgv: []string{"go", "test", "./..."}, wantOutput: ""},
		// Go keeps the last -o, and an empty -o is no -o.
		{name: "last -o wins", argv: []string{"go", "build", "-o", "a", "-o=bin/app", "./cmd/x"}, wantArgv: []string{"go", "build", "-o", "a", "-o=/srv/build/candidates/OUTPUT", "./cmd/x"}, wantOutput: "bin/app"},
		{name: "empty -o needs an output", argv: []string{"go", "build", "-o=", "./cmd/x"}, wantErr: true},
		{name: "last -o empty", argv: []string{"go", "build", "-o", "bin/app", "-o", "", "./cmd/x"}, wantErr: true},
		{name: "empty -o pattern check", argv: []string{"go", "build", "-o", "", "./..."}, wantArgv: []string{"go", "build", "-o", "", "./..."}, wantOutput: ""},
		{name: "/dev/null stays", argv: []string{"go", "build", "-o", "/dev/null", "./cmd/x"}, wantArgv: []string{"go", "build", "-o", "/dev/null", "./cmd/x"}, wantOutput: "/dev/null"},
		// Only a package argument makes a pattern build.
		{name: "pattern in a flag value", argv: []string{"go", "build", "-gcflags=./...=-N", "./cmd/x"}, wantErr: true},
		{name: "pattern after --", argv: []string{"go", "build", "--", "./..."}, wantArgv: []string{"go", "build", "--", "./..."}, wantOutput: ""},
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
