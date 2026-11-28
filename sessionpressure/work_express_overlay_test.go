package sessionpressure

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// writeOverlayFixture writes files under root, creating directories.
func writeOverlayFixture(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for name, body := range files {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func writeOverlayJSON(t *testing.T, path string, replace map[string]string) {
	t.Helper()
	body, err := json.Marshal(map[string]any{"Replace": replace})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
}

// isolateGoEnv points the go command at a private go env file and clears
// GOFLAGS, so the host's go settings cannot change the digests under test.
func isolateGoEnv(t *testing.T) string {
	t.Helper()
	goEnv := filepath.Join(t.TempDir(), "go.env")
	t.Setenv("GOENV", goEnv)
	t.Setenv("GOFLAGS", "")
	t.Setenv("GOTOOLCHAIN", "local")
	t.Setenv("GOWORK", "off")
	previous := expressSourceListTimeout
	expressSourceListTimeout = testWatchdog
	t.Cleanup(func() { expressSourceListTimeout = previous })
	return goEnv
}

func TestExpressSourceDigestBindsGoOverlay(t *testing.T) {
	goEnv := isolateGoEnv(t)
	// t.TempDir is under /var on macOS, a symlink to /private/var. go list
	// reports the real directory while the overlay keys below are joined to
	// the -C directory, so this also proves the symlink-safe lookup.
	parent := t.TempDir()
	root, repl := filepath.Join(parent, "mod"), filepath.Join(parent, "repl")
	writeOverlayFixture(t, root, map[string]string{
		"go.mod":    "module fixture.invalid/overlay\n\ngo 1.25\n",
		"a.go":      "package overlay\n\nconst V = 1\n",
		"a_test.go": "package overlay\n\nimport \"testing\"\n\nfunc TestV(t *testing.T) { _ = V }\n",
	})
	writeOverlayFixture(t, repl, map[string]string{
		"a.go": "package overlay\n\nconst V = 2\n",
		// b.go exists only in the overlay: the snapshot used to fail on it
		// because it hashed the path go list printed.
		"b.go": "package overlay\n\nconst W = 3\n",
	})
	overlay := filepath.Join(repl, "overlay.json")
	writeOverlayJSON(t, overlay, map[string]string{"a.go": filepath.Join(repl, "a.go"), "b.go": "../repl/b.go"})

	snap := func(argv ...string) string {
		t.Helper()
		snapshot, err := snapshotExpressSource(argv, "")
		if err != nil {
			t.Fatalf("snapshot %v: %v", argv, err)
		}
		return snapshot.Digest
	}
	plain := snap("go", "-C", root, "test", "-count=1", ".")
	withOverlay := snap("go", "-C", root, "test", "-count=1", "-overlay="+overlay, ".")
	if withOverlay == plain {
		t.Fatal("the overlay did not change the source digest")
	}
	if again := snap("go", "-C", root, "test", "-count=1", "-overlay="+overlay, "."); again != withOverlay {
		t.Fatal("an unchanged overlay changed the source digest, so it can never reuse")
	}
	for name, argv := range map[string][]string{
		"double dash":    {"go", "-C", root, "test", "--overlay=" + overlay, "-count=1", "."},
		"separate value": {"go", "-C", root, "test", "-overlay", overlay, "-count=1", "."},
		"relative path":  {"go", "-C", root, "test", "-count=1", "-overlay=../repl/overlay.json", "."},
	} {
		if got := snap(argv...); got != withOverlay {
			t.Errorf("%s: digest differs from the -overlay=path spelling", name)
		}
	}
	if got := snap("go", "-C", root, "test", "-count=1", ".", "-args", "-overlay="+overlay); got != plain {
		t.Error("-overlay after -args belongs to the test binary but changed the digest")
	}

	// New replacement contents behind the same overlay path must change the
	// digest: this is the stale pass the reuse replayed.
	writeOverlayFixture(t, repl, map[string]string{"a.go": "package overlay\n\nconst V = 4\n"})
	changed := snap("go", "-C", root, "test", "-count=1", "-overlay="+overlay, ".")
	if changed == withOverlay {
		t.Fatal("a changed replacement file left the source digest unchanged")
	}
	writeOverlayFixture(t, repl, map[string]string{"b.go": "package overlay\n\nconst W = 5\n"})
	changedAdded := snap("go", "-C", root, "test", "-count=1", "-overlay="+overlay, ".")
	if changedAdded == changed {
		t.Fatal("a changed overlay-only file left the source digest unchanged")
	}

	// The JSON itself is an input: a deletion removes a file from the build.
	writeOverlayJSON(t, overlay, map[string]string{"a.go": "", "b.go": filepath.Join(repl, "b.go")})
	deleted := snap("go", "-C", root, "test", "-count=1", "-overlay="+overlay, ".")
	if deleted == changedAdded {
		t.Fatal("deleting a file in the overlay left the source digest unchanged")
	}
	writeOverlayJSON(t, overlay, map[string]string{"a.go": filepath.Join(repl, "a.go"), "b.go": "../repl/b.go"})
	restored := snap("go", "-C", root, "test", "-count=1", "-overlay="+overlay, ".")
	if restored != changedAdded {
		t.Fatal("the same overlay contents gave a different digest")
	}

	// GOFLAGS from the environment names the same overlay.
	t.Setenv("GOFLAGS", "-overlay="+overlay)
	fromEnvironment := snap("go", "-C", root, "test", "-count=1", ".")
	if fromEnvironment != restored {
		t.Error("GOFLAGS=-overlay gave a different digest than the -overlay flag")
	}
	writeOverlayFixture(t, repl, map[string]string{"a.go": "package overlay\n\nconst V = 6\n"})
	if snap("go", "-C", root, "test", "-count=1", ".") == fromEnvironment {
		t.Fatal("a changed replacement behind GOFLAGS=-overlay left the digest unchanged")
	}

	// GOFLAGS from the go env file (go env -w GOFLAGS=...) when the
	// environment leaves it empty.
	t.Setenv("GOFLAGS", "")
	plainNow := snap("go", "-C", root, "test", "-count=1", ".")
	if err := os.WriteFile(goEnv, []byte("GOFLAGS=-overlay="+overlay+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fromEnvFile := snap("go", "-C", root, "test", "-count=1", ".")
	if fromEnvFile == plainNow {
		t.Fatal("GOFLAGS=-overlay in the go env file did not change the digest")
	}
	writeOverlayFixture(t, repl, map[string]string{"a.go": "package overlay\n\nconst V = 7\n"})
	if snap("go", "-C", root, "test", "-count=1", ".") == fromEnvFile {
		t.Fatal("a changed replacement behind the go env file left the digest unchanged")
	}
}

func TestExpressReuseRerunsWhenGoOverlayContentChanges(t *testing.T) {
	isolateGoEnv(t)
	counter := filepath.Join(t.TempDir(), "executions")
	t.Setenv("SP_REUSE_COUNTER", counter)
	t.Setenv("NDEV_PRESSURE_NO_GREEN_EXPRESS", "1")
	parent := t.TempDir()
	root, repl := filepath.Join(parent, "mod"), filepath.Join(parent, "repl")
	writeOverlayFixture(t, root, map[string]string{
		"go.mod": "module fixture.invalid/overlayreuse\n\ngo 1.25\n",
		"a.go":   "package overlayreuse\n\nconst V = 1\n",
		"fixture_test.go": "package overlayreuse\nimport (\"os\"; \"testing\")\n" +
			"func TestExecution(t *testing.T) { f, e := os.OpenFile(os.Getenv(\"SP_REUSE_COUNTER\"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600); if e != nil { t.Fatal(e) }; f.WriteString(\"run\\n\"); f.Close(); _ = V }\n",
	})
	writeOverlayFixture(t, repl, map[string]string{"a.go": "package overlayreuse\n\nconst V = 2\n"})
	overlay := filepath.Join(repl, "overlay.json")
	writeOverlayJSON(t, overlay, map[string]string{"a.go": filepath.Join(repl, "a.go")})

	c := NewWorkCoordinator(t.TempDir(), testWorkLimits())
	run := func() {
		t.Helper()
		argv := []string{"go", "-C", root, "test", "-count=1", "-overlay=" + overlay, "."}
		code, err := RunWorkCommandWithExpressReuse(c, WorkRunOptions{Class: WorkClassExpressTest, Wait: testWaitNotUnderTest, Command: argv},
			func() Admission { return Admission{Allowed: true, Level: LevelNormal} }, time.Millisecond,
			WorkRunStreams{Stdout: io.Discard, Stderr: io.Discard, CommandFactory: func(target string, args []string) (*exec.Cmd, io.WriteCloser, error) {
				cmd := exec.Command(target, args...)
				cmd.Dir = root
				return cmd, nil, nil
			}})
		if err == nil && code != 0 {
			err = fmt.Errorf("child exit %d", code)
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	check := func(want int, why string) {
		t.Helper()
		data, err := os.ReadFile(counter)
		if err != nil || bytes.Count(data, []byte("run\n")) != want {
			var decisions []string
			if events, readErr := NewWorkEventStore(c.Dir).Read(WorkEventFilter{}); readErr == nil {
				for _, event := range events {
					if event.ReuseDecision != "" || event.ReuseStatus != "" {
						decisions = append(decisions, fmt.Sprintf("%s/%s/%s/%s", event.Event, event.ReuseStatus, event.ReuseDecision, event.ReuseRefusalReason))
					}
				}
			}
			t.Fatalf("%s: executions=%q want=%d err=%v reuse=%v", why, data, want, err, decisions)
		}
	}
	run()
	check(1, "first run")
	run()
	check(1, "unchanged overlay must reuse")
	writeOverlayFixture(t, repl, map[string]string{"a.go": "package overlayreuse\n\nconst V = 3\n"})
	run()
	check(2, "changed replacement content must run again")
	run()
	check(2, "unchanged overlay after the rerun must reuse")
}
