package sessionpressure

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// Reproduce admission starvation through the real work runner. A later warm
// baseline must not turn this pre-child timeout into a candidate failure.
func TestRunWorkPrestartRefusals(t *testing.T) {
	for _, stage := range []string{"capacity", "reacquire", "before_start", "no_wait"} {
		t.Run(stage, func(t *testing.T) {
			coordinator := NewWorkCoordinator(t.TempDir(), testWorkLimits())
			occupy := func() {
				t.Helper()
				for i := range 2 {
					lease, _, err := coordinator.AcquireOperation(context.Background(), WorkClassBuild, fmt.Sprintf("%032x", i+1))
					if err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() { _ = lease.Release(context.Background()) })
				}
			}
			wait := 250 * time.Millisecond
			if stage == "capacity" || stage == "no_wait" {
				occupy()
			}
			if stage == "no_wait" {
				wait = 0
			}
			calls := 0
			admission := func() Admission {
				calls++
				if stage == "reacquire" {
					if calls == 2 {
						return Admission{Allowed: false, Level: LevelRed, Reasons: []string{"host free memory 10% <= red 15%"}}
					}
					if calls == 3 {
						// Pressure clears after the admission observation consumes
						// the remaining deadline, before reservation reacquisition.
						<-time.After(2 * wait)
					}
				}
				if stage == "before_start" && calls == 2 {
					<-time.After(2 * wait)
				}
				return Admission{Allowed: true, Level: LevelNormal}
			}
			constructed := false
			code, err := RunWorkCommand(coordinator,
				WorkRunOptions{Class: WorkClassTest, Wait: wait, Progress: WorkProgressQuiet, Command: []string{"/bin/true"}},
				admission, time.Millisecond,
				WorkRunStreams{Stdout: io.Discard, Stderr: io.Discard, CommandFactory: func(string, []string) (*exec.Cmd, io.WriteCloser, error) {
					constructed = true
					return nil, nil, errors.New("child must not be constructed")
				}})
			kind, wantCode := WorkRefusalWaitTimeout, workExitWaitTimeout
			if stage == "no_wait" {
				kind, wantCode = WorkRefusalAdmission, 1
			}
			if code != wantCode || err == nil || !strings.Contains(err.Error(), WorkRefusalMarker+" kind="+kind+":") || constructed {
				t.Fatalf("code=%d err=%v child constructed=%v", code, err, constructed)
			}
			if wait > 0 && !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("lost deadline cause: %v", err)
			}
			boundary := map[string]string{"capacity": "acquire test work lease:", "no_wait": "acquire test work lease:", "reacquire": "reacquire pressure reservation:", "before_start": "wait for heavy work:"}[stage]
			if !strings.Contains(err.Error(), boundary) {
				t.Fatalf("did not reach %s boundary: %v", stage, err)
			}
			events, readErr := NewWorkEventStore(coordinator.Dir).Read(WorkEventFilter{})
			if readErr != nil {
				t.Fatal(readErr)
			}
			for _, event := range events {
				if event.Event == WorkEventStarted {
					t.Fatal("pre-start refusal recorded child execution")
				}
			}
		})
	}
}

func TestRunWorkChildTimeoutExitIsNotAdmissionRefusal(t *testing.T) {
	coordinator := NewWorkCoordinator(t.TempDir(), testWorkLimits())
	t.Setenv("NICOS_WORK_GATE_TEST_MARKER", coordinator.Dir+"/child-started")
	code, err := RunWorkCommand(coordinator,
		WorkRunOptions{Class: WorkClassTest, Wait: time.Second, Progress: WorkProgressQuiet, Command: []string{"/bin/sh", "-c", "exit 124"}},
		func() Admission { return Admission{Allowed: true, Level: LevelNormal} }, time.Millisecond,
		WorkRunStreams{Stdout: io.Discard, Stderr: io.Discard, CommandFactory: testGatedWorkCommand})
	if code != 124 || IsWorkRefusal(err) {
		t.Fatalf("child exit misclassified: code=%d err=%v", code, err)
	}
	if _, err := os.Stat(coordinator.Dir + "/child-started"); err != nil {
		t.Fatalf("child did not execute: %v", err)
	}
}
