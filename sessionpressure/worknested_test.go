package sessionpressure

import (
	"bytes"
	"context"
	"io"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"time"
)

func stubNestedProcess(t *testing.T, group int, alive bool) {
	t.Helper()
	previousGroup, previousAlive := workProcessGroup, workProcessAlive
	workProcessGroup = func() int { return group }
	workProcessAlive = func(int) bool { return alive }
	t.Cleanup(func() { workProcessGroup, workProcessAlive = previousGroup, previousAlive })
}

func TestWithWorkOperationEnvReplacesInheritedOperation(t *testing.T) {
	got := withWorkOperationEnv([]string{"A=1", WorkOperationEnv + "=outer", "B=2"}, "inner")
	want := []string{"A=1", "B=2", WorkOperationEnv + "=inner"}
	if !slices.Equal(got, want) {
		t.Fatalf("env=%v", got)
	}
	if got := workEnvValue(got, WorkOperationEnv); got != "inner" {
		t.Fatalf("value=%q", got)
	}
}

// The per-run operation ID must not reach the express reuse fingerprint.
func TestWorkEnvironmentExcludesOperationEnv(t *testing.T) {
	env, err := WorkEnvironment([]string{"PATH=/bin", WorkOperationEnv + "=abc"}, testWorkLimits(), WorkClassExpressTest)
	if err != nil {
		t.Fatal(err)
	}
	if workEnvValue(env, WorkOperationEnv) != "" {
		t.Fatalf("operation env leaked into the hashed environment: %v", env)
	}
}

func TestNestedWorkParentRequiresLiveLeaseInSameProcessGroup(t *testing.T) {
	coordinator := NewWorkCoordinator(t.TempDir(), defaultWorkLimits(10))
	ctx := context.Background()
	lease, status, err := coordinator.Acquire(ctx, WorkClassBuild)
	if err != nil || len(status.Leases) != 1 {
		t.Fatalf("acquire: leases=%d err=%v", len(status.Leases), err)
	}
	parent := status.Leases[0]
	env := []string{WorkOperationEnv + "=" + parent.OperationID}

	stubNestedProcess(t, parent.PID, true)
	if got, ok := nestedWorkParent(coordinator, env); !ok || got.OperationID != parent.OperationID {
		t.Fatalf("live parent in our group not found: %+v %v", got, ok)
	}
	if _, ok := nestedWorkParent(coordinator, nil); ok {
		t.Fatal("no inherited operation must not nest")
	}
	if _, ok := nestedWorkParent(coordinator, []string{WorkOperationEnv + "=unknown"}); ok {
		t.Fatal("unknown operation nested")
	}
	stubNestedProcess(t, parent.PID+1, true)
	if _, ok := nestedWorkParent(coordinator, env); ok {
		t.Fatal("a process outside the leased group (setsid, daemon) nested")
	}
	stubNestedProcess(t, parent.PID, false)
	if _, ok := nestedWorkParent(coordinator, env); ok {
		t.Fatal("a dead lease leader nested")
	}
	stubNestedProcess(t, parent.PID, true)
	if err := lease.Release(ctx); err != nil {
		t.Fatal(err)
	}
	if _, ok := nestedWorkParent(coordinator, env); ok {
		t.Fatal("a released lease nested")
	}
}

// The deadlock this prevents: a 5-unit build inside a 5-unit build on a
// capacity of 8 used to wait for capacity only its own parent could free.
func TestRunWorkCommandRunsNestedInlineUnderParentLease(t *testing.T) {
	coordinator := NewWorkCoordinator(t.TempDir(), defaultWorkLimits(10))
	ctx := context.Background()
	lease, status, err := coordinator.Acquire(ctx, WorkClassBuild)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lease.Release(ctx) }()
	parent := status.Leases[0]
	stubNestedProcess(t, parent.PID, true)
	t.Setenv(WorkOperationEnv, parent.OperationID)
	var execPath string
	var execArgs []string
	previousExec := workNestedExec
	workNestedExec = func(path string, args []string, _ []string) error {
		execPath, execArgs = path, args
		return nil
	}
	t.Cleanup(func() { workNestedExec = previousExec })
	var stderr bytes.Buffer
	code, err := RunWorkCommandWithExpressReuse(coordinator,
		WorkRunOptions{Class: WorkClassBuild, Wait: 50 * time.Millisecond, Command: []string{"true"}},
		func() Admission { return Admission{Allowed: true, Level: LevelNormal} },
		time.Millisecond, WorkRunStreams{Stdout: io.Discard, Stderr: &stderr})
	if code != 0 || err != nil {
		t.Fatalf("nested run code=%d err=%v stderr=%q", code, err, stderr.String())
	}
	if !strings.HasSuffix(execPath, "/true") || !slices.Equal(execArgs, []string{"true"}) {
		t.Fatalf("exec path=%q args=%v", execPath, execArgs)
	}
	if !strings.Contains(stderr.String(), "nested class=build runs under parent operation="+parent.OperationID) {
		t.Fatalf("stderr=%q", stderr.String())
	}
	after, err := coordinator.Status(ctx)
	if err != nil || len(after.Leases) != 1 || after.Used != 5 {
		t.Fatalf("nested run took a second lease: used=%d leases=%d err=%v", after.Used, len(after.Leases), err)
	}
}

// A top-level child learns its own operation, so its nested runs can find it.
func TestRunWorkCommandExportsOperationToChild(t *testing.T) {
	coordinator := NewWorkCoordinator(t.TempDir(), testWorkLimits())
	t.Setenv(WorkOperationEnv, "stale-outer-operation")
	var stdout, stderr bytes.Buffer
	code, err := RunWorkCommand(coordinator,
		WorkRunOptions{Class: WorkClassExpressTest, Wait: time.Second, Command: []string{"/bin/sh", "-c", `printf %s "$` + WorkOperationEnv + `"`}},
		func() Admission { return Admission{Allowed: true, Level: LevelNormal} },
		time.Millisecond,
		WorkRunStreams{Stdout: &stdout, Stderr: &stderr, CommandFactory: func(target string, args []string) (*exec.Cmd, io.WriteCloser, error) {
			return exec.Command(target, args...), nil, nil
		}})
	if code != 0 || err != nil {
		t.Fatalf("code=%d err=%v stderr=%q", code, err, stderr.String())
	}
	operation := stdout.String()
	if operation == "" || operation == "stale-outer-operation" || !strings.Contains(stderr.String(), "operation="+operation) {
		t.Fatalf("child saw operation %q; stderr=%q", operation, stderr.String())
	}
}

func TestWithoutGoTestGitBindingDropsOnlyForGoTest(t *testing.T) {
	env := []string{"GIT_INDEX_FILE=/repo/.git/index", "GIT_DIR=/repo/.git", "GIT_AUTHOR_NAME=a", "HOME=/h"}
	for _, argv := range [][]string{{"go", "test", "./..."}, {"/usr/local/go/bin/go", "test", "-run", "X"}, {"env", "-u", "X", "GOFLAGS=-count=1", "go", "test", "."}, {"sh", "-c", "go build ./... && go test ./x"}} {
		got := strings.Join(withoutGoTestGitBinding(env, argv), " ")
		if strings.Contains(got, "GIT_INDEX_FILE") || strings.Contains(got, "GIT_DIR") || !strings.Contains(got, "GIT_AUTHOR_NAME=a") || !strings.Contains(got, "HOME=/h") {
			t.Fatalf("%v: %s", argv, got)
		}
	}
	for _, argv := range [][]string{{"go", "build", "./..."}, {"make", "test"}, {"sh", "-c", "go vet ./..."}} {
		if got := withoutGoTestGitBinding(env, argv); len(got) != len(env) {
			t.Fatalf("%v changed: %v", argv, got)
		}
	}
}
