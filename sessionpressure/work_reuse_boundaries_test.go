package sessionpressure

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestGoReuseSingleflightAndExplicitExecution(t *testing.T) {
	root, counter := t.TempDir(), filepath.Join(t.TempDir(), "executions")
	t.Setenv("SP_REUSE_COUNTER", counter)
	t.Setenv("NDEV_PRESSURE_NO_GREEN_EXPRESS", "1")
	for path, body := range map[string]string{
		"go.mod":          "module fixture.invalid/reuse\n\ngo 1.25\n",
		"fixture_test.go": "package reuse\nimport (\"os\"; \"testing\"; \"time\")\nfunc TestExecution(t *testing.T) { f,e:=os.OpenFile(os.Getenv(\"SP_REUSE_COUNTER\"),os.O_CREATE|os.O_APPEND|os.O_WRONLY,0600); if e!=nil {t.Fatal(e)}; f.WriteString(\"run\\n\"); f.Close(); time.Sleep(100*time.Millisecond) }\n",
	} {
		if err := os.WriteFile(filepath.Join(root, path), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	c := NewWorkCoordinator(t.TempDir(), testWorkLimits())
	run := func(noReuse, profile bool) error {
		argv := []string{"go", "-C", root, "test", "-count=1", "."}
		// The profiler requires the direct Go JSON shape; the child factory
		// below selects the fixture directory without changing process cwd.
		if profile {
			argv = []string{"go", "test", "-json", "-count=1", "."}
		}
		code, err := RunWorkCommandWithExpressReuse(c, WorkRunOptions{Class: WorkClassExpressTest, Wait: 20 * time.Second, NoReuse: noReuse, ProfileGoTest: profile, Command: argv},
			func() Admission { return Admission{Allowed: true, Level: LevelNormal} }, time.Millisecond,
			WorkRunStreams{Stdout: io.Discard, Stderr: io.Discard, CommandFactory: func(target string, args []string) (*exec.Cmd, io.WriteCloser, error) {
				cmd := exec.Command(target, args...)
				cmd.Dir = root
				return cmd, nil, nil
			}})
		if err == nil && code != 0 {
			err = fmt.Errorf("child exit %d", code)
		}
		return err
	}
	start, results := make(chan struct{}), make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() { <-start; results <- run(false, false) }()
	}
	close(start)
	for i := 0; i < 2; i++ {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	check := func(want int) {
		t.Helper()
		data, err := os.ReadFile(counter)
		if err != nil || bytes.Count(data, []byte("run\n")) != want {
			t.Fatalf("executions=%q want=%d err=%v", data, want, err)
		}
	}
	check(1)
	if err := run(true, false); err != nil {
		t.Fatal(err)
	}
	check(2)
	if err := run(false, true); err != nil {
		t.Fatal(err)
	}
	check(3)
}

func TestSuccessfulReuseRequiresUnchangedInputsAndArtifact(t *testing.T) {
	for _, change := range []string{"none", "source", "artifact", "missing", "expired", "failure"} {
		t.Run(change, func(t *testing.T) {
			dir := t.TempDir()
			artifact := filepath.Join(dir, "binary")
			if err := os.WriteFile(artifact, []byte("original"), 0600); err != nil {
				t.Fatal(err)
			}
			key := bytes.Repeat([]byte{1}, 32)
			fp := ExpressCommandFingerprint{KeyDigest: "sha256:" + strings.Repeat("a", 64), Class: WorkClassExpressBuild}
			source := "sha256:" + strings.Repeat("b", 64)
			artifacts, reason, err := snapshotExpressArtifacts([]string{artifact}, key)
			if err != nil || reason != "" {
				t.Fatal(reason, err)
			}
			now := time.Now().UTC()
			code := 0
			if change == "failure" {
				code = 7
			}
			if err := writeExpressReuseReceipt(dir, fp, source, artifacts, code, now); err != nil {
				t.Fatal(err)
			}
			switch change {
			case "source":
				source = "sha256:" + strings.Repeat("c", 64)
			case "artifact":
				if err := os.WriteFile(artifact, []byte("modified"), 0600); err != nil {
					t.Fatal(err)
				}
			case "missing":
				if err := os.Remove(artifact); err != nil {
					t.Fatal(err)
				}
			case "expired":
				now = now.Add(3 * time.Minute)
			}
			_, hit, _, err := readExpressReuseReceipt(dir, fp, source, []string{artifact}, key, now)
			if err != nil {
				t.Fatal(err)
			}
			if hit != (change == "none") {
				t.Fatalf("change=%s reused=%v", change, hit)
			}
		})
	}
}

func TestBatchPreservesDependenciesAndFailureStatus(t *testing.T) {
	root := t.TempDir()
	input, later := filepath.Join(root, "input"), filepath.Join(root, "later")
	t.Setenv("SP_BATCH_BOUNDARY_FIXTURE", "1")
	step := func(id, mode, path string) WorkBatchStep {
		return WorkBatchStep{ID: id, CWD: ".", Argv: []string{os.Args[0], "-test.run=^TestBatchBoundaryHelper$", "--", mode, path}}
	}
	m := WorkBatchManifest{Steps: []WorkBatchStep{step("produce", "write", input), step("consume-fail", "read-fail", input), step("must-not-run", "write", later)}}
	result := filepath.Join(root, "result.json")
	var stderr bytes.Buffer
	if code := executeWorkBatch(m, root, result, WorkProgressJSONL, &bytes.Buffer{}, &stderr); code != 7 {
		t.Fatalf("exit=%d: %s", code, &stderr)
	}
	progress, err := readWorkBatchResult(result)
	if err != nil || progress.CompletedSteps != 1 {
		t.Fatal(progress, err)
	}
	if _, err := os.Stat(later); !os.IsNotExist(err) {
		t.Fatalf("dependent step ran: %v", err)
	}
	if !strings.Contains(stderr.String(), `"stage":"batch_step_finished"`) || !strings.Contains(stderr.String(), `"outcome":"failed"`) {
		t.Fatal(stderr.String())
	}
}

func TestBatchBoundaryHelper(t *testing.T) {
	if os.Getenv("SP_BATCH_BOUNDARY_FIXTURE") != "1" {
		return
	}
	args := os.Args[len(os.Args)-2:]
	switch args[0] {
	case "write":
		if err := os.WriteFile(args[1], []byte("ready"), 0600); err != nil {
			os.Exit(9)
		}
	case "read-fail":
		data, err := os.ReadFile(args[1])
		if err != nil || string(data) != "ready" {
			os.Exit(8)
		}
		os.Exit(7)
	default:
		os.Exit(10)
	}
	os.Exit(0)
}
