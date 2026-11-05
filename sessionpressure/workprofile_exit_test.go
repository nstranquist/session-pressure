package sessionpressure

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestGoProfileSurvivesFailureTimeoutAndSignal(t *testing.T) {
	for _, mode := range []string{"failure", "timeout", "signal"} {
		t.Run(mode, func(t *testing.T) {
			root, state := t.TempDir(), t.TempDir()
			marker := filepath.Join(root, "started")
			t.Setenv("SP_PROFILE_EXIT_MODE", mode)
			t.Setenv("SP_PROFILE_EXIT_MARKER", marker)
			files := map[string]string{
				"go.mod": "module fixture.invalid/profile\n\ngo 1.25\n",
				"fixture_test.go": `package profile
import ("os"; "testing"; "time")
func TestA(t *testing.T) { t.Log("private-output-must-not-persist") }
func TestB(t *testing.T) {
 if os.Getenv("SP_PROFILE_EXIT_MODE") == "failure" { t.Fatal("private-output-must-not-persist") }
 if err := os.WriteFile(os.Getenv("SP_PROFILE_EXIT_MARKER"), []byte("ready"), 0600); err != nil { t.Fatal(err) }
 time.Sleep(time.Minute)
}
`,
			}
			for name, body := range files {
				if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0600); err != nil {
					t.Fatal(err)
				}
			}
			signals := make(chan os.Signal, 1)
			type result struct {
				code int
				err  error
			}
			finished := make(chan result, 1)
			go func() {
				timeout := "10s"
				if mode == "timeout" {
					timeout = "200ms"
				}
				code, err := RunWorkCommand(NewWorkCoordinator(state, testWorkLimits()),
					WorkRunOptions{Class: WorkClassTest, Wait: 20 * time.Second, ProfileGoTest: true, Command: []string{"go", "test", "-json", "-count=1", "-timeout=" + timeout, "."}},
					func() Admission { return Admission{Allowed: true, Level: LevelNormal} }, time.Millisecond,
					WorkRunStreams{Stdout: io.Discard, Stderr: io.Discard, Signals: signals, CommandFactory: func(target string, args []string) (*exec.Cmd, io.WriteCloser, error) {
						cmd := exec.Command(target, args...)
						cmd.Dir = root
						return cmd, nil, nil
					}})
				finished <- result{code, err}
			}()
			if mode == "signal" {
				waitForTestFile(t, marker, 15*time.Second)
				signals <- syscall.SIGTERM
			}
			select {
			case got := <-finished:
				want := 1
				if mode == "signal" {
					want = 128 + int(syscall.SIGTERM)
				}
				if got.err != nil || got.code != want {
					t.Fatalf("exit=%d want=%d err=%v", got.code, want, got.err)
				}
			case <-time.After(20 * time.Second):
				t.Fatal("profiled command did not finish")
			}
			paths, err := filepath.Glob(filepath.Join(state, "work-profiles", "*.json"))
			if err != nil || len(paths) != 1 {
				t.Fatalf("profiles=%v err=%v", paths, err)
			}
			body, err := os.ReadFile(paths[0])
			if err != nil {
				t.Fatal(err)
			}
			var profile goTestProfile
			if err := json.Unmarshal(body, &profile); err != nil {
				t.Fatal(err)
			}
			if len(profile.Records) == 0 || bytes.Contains(body, []byte("private-output")) || bytes.Contains(body, []byte(root)) {
				t.Fatalf("missing timing or private output in profile: %s", body)
			}
		})
	}
}
