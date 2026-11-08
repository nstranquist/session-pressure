package sessionpressure

import (
	"io"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestEnvWrappedGoNeverEntersLocalAdmission(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("remote routing is a Mac admission policy")
	}
	dir := t.TempDir()
	if err := SetRemoteWork(dir, true); err != nil {
		t.Fatal(err)
	}
	for _, argv := range [][]string{
		{"env", "NDEV_LEGACY_TEST_WORKER=/private/Mac-worker", "go", "test", "./internal/commitdeliverylegacy"},
		{"go", "-C", "/repo", "test", "./..."},
		{"env", "-S", `go\_test ./...`},
		{"env", "-P", "/opt/homebrew/bin", "go", "test", "."},
	} {
		admitted, started := false, false
		code, err := RunWorkCommand(NewWorkCoordinator(dir, testWorkLimits()), WorkRunOptions{
			Class:   WorkClassTest,
			Command: argv,
		}, func() Admission {
			admitted = true
			return Admission{Allowed: true, Level: LevelNormal}
		}, time.Millisecond, WorkRunStreams{Stdout: io.Discard, Stderr: io.Discard,
			CommandFactory: func(string, []string) (*exec.Cmd, io.WriteCloser, error) {
				started = true
				return exec.Command("false"), nil, nil
			},
		})
		if code != workExitPolicyDenied || err == nil || !strings.Contains(err.Error(), WorkRefusalMarker) || !strings.Contains(err.Error(), WorkRefusalRemoteProfile) || admitted || started {
			t.Fatalf("code=%d err=%v admitted=%t started=%t", code, err, admitted, started)
		}
	}
}

func TestEnvWrappedGoOperation(t *testing.T) {
	for _, tc := range []struct {
		name string
		argv []string
		want string
	}{
		{"bound Mac worker", []string{"env", "NDEV_LEGACY_TEST_WORKER=/private/worker", "go", "test", "./..."}, "test"},
		{"absolute binaries", []string{"/usr/bin/env", "CGO_ENABLED=1", "/opt/homebrew/bin/go", "build", "."}, "build"},
		{"unset and delimiter", []string{"env", "-u", "GOFLAGS", "--", "go", "vet", "."}, "vet"},
		{"split string", []string{"env", "-S", "CGO_ENABLED=1 go build ."}, "wrapped"},
		{"quoted split command", []string{"env", "-S", "\"go\" test ."}, "wrapped"},
		{"split and trailing argv", []string{"env", "-S", "'go'", "vet", "."}, "wrapped"},
		{"attached split string", []string{"env", "-Sgo test ."}, "wrapped"},
		{"long split string", []string{"env", "--split-string=go test ."}, "wrapped"},
		{"env special escape", []string{"env", "-S", `go\_test .`}, "wrapped"},
		{"ambiguous split", []string{"env", "-S", "${COMPILER} test ."}, "wrapped"},
		{"alternate path", []string{"env", "-P", "/opt/homebrew/bin", "go", "test", "."}, "test"},
		{"unknown combined options", []string{"env", "-iv", "go", "test", "."}, "wrapped"},
		{"nested wrapper", []string{"env", "X=1", "env", "Y=2", "go", "test", "."}, "test"},
		{"global directory flag", []string{"go", "-C", "/repo", "test", "."}, "test"},
		{"global directory equals", []string{"go", "-C=/repo", "build", "."}, "build"},
		{"wrapped directory flag", []string{"env", "X=1", "go", "-C", "/repo", "vet", "."}, "vet"},
		{"plain go", []string{"go", "test", "."}, ""},
		{"other command", []string{"env", "X=1", "echo", "go", "test"}, ""},
		{"go only a value", []string{"env", "X=go", "echo", "test"}, ""},
		{"unset name", []string{"env", "-u", "go", "echo", "test"}, ""},
		{"metadata", []string{"env", "go", "version"}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := unsupportedWrappedGoOperation(tc.argv); got != tc.want {
				t.Fatalf("operation = %q, want %q", got, tc.want)
			}
		})
	}
}
