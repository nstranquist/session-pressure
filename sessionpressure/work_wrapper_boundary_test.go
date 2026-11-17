package sessionpressure

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// Reproduce the delivery chain: wrapper -> admitted generator -> candidate
// ndev -> native provider. The candidate has a restricted PATH and the same
// recursion refusal as the real wrapper. Exercise actual child processes,
// including the inline nested-exec boundary, rather than inspecting argv.
func TestAdmittedGeneratorStartsFreshWrapperChain(t *testing.T) {
	for _, nested := range []bool{false, true} {
		name := "normal"
		if nested {
			name = "nested"
		}
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			provider := filepath.Join(dir, "provider")
			candidate := filepath.Join(dir, "candidate-ndev")
			generator := filepath.Join(dir, "generator")
			files := map[string]string{
				provider:  "#!/bin/sh\n[ \"$SESSION_PRESSURE_WORK_OPERATION\" != \"\" ] || exit 91\n[ \"$GOMAXPROCS\" = 2 ] || exit 92\n[ \"$NDEV_PRESSURE_BIN\" = /sealed/product ] || exit 93\n[ \"$NICOS_PREFLIGHT_NDEV_BIN\" = \"$CANDIDATE\" ] || exit 94\nprintf provider-ok\n",
				candidate: "#!/bin/sh\n[ \"${NDEV_PRESSURE_WRAPPER-}\" != 1 ] || exit 127\nexport NDEV_PRESSURE_WRAPPER=1\nexec \"$PROVIDER\"\n",
				generator: "#!/bin/sh\nexec \"$CANDIDATE\" session pressure work run --placement remote -- provider\n",
			}
			for path, body := range files {
				if err := os.WriteFile(path, []byte(body), 0700); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("PATH", "/usr/bin:/bin")
			t.Setenv("NDEV_PRESSURE_WRAPPER", "1")
			t.Setenv("GOMAXPROCS", "2")
			t.Setenv("NDEV_PRESSURE_BIN", "/sealed/product")
			t.Setenv("NICOS_PREFLIGHT_NDEV_BIN", candidate)
			t.Setenv("CANDIDATE", candidate)
			t.Setenv("PROVIDER", provider)
			t.Setenv(WorkOperationEnv, "")
			coordinator := NewWorkCoordinator(t.TempDir(), testWorkLimits())
			var stdout, stderr bytes.Buffer
			if nested {
				lease, status, err := coordinator.Acquire(context.Background(), WorkClassBuild)
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = lease.Release(context.Background()) }()
				parent := status.Leases[0]
				stubNestedProcess(t, parent.PID, true)
				t.Setenv(WorkOperationEnv, parent.OperationID)
				previous := workNestedExec
				workNestedExec = func(path string, args, env []string) error {
					cmd := exec.Command(path, args[1:]...)
					cmd.Env, cmd.Stdout, cmd.Stderr = env, &stdout, &stderr
					return cmd.Run()
				}
				t.Cleanup(func() { workNestedExec = previous })
			}
			code, err := RunWorkCommand(coordinator,
				WorkRunOptions{Class: WorkClassExpressTest, Wait: testWaitNotUnderTest, Command: []string{generator}},
				func() Admission { return Admission{Allowed: true, Level: LevelNormal} }, time.Millisecond,
				WorkRunStreams{Stdout: &stdout, Stderr: &stderr, CommandFactory: func(target string, args []string) (*exec.Cmd, io.WriteCloser, error) {
					return exec.Command(target, args...), nil, nil
				}})
			if code != 0 || err != nil || stdout.String() != "provider-ok" {
				t.Fatalf("code=%d err=%v stdout=%q stderr=%q", code, err, stdout.String(), stderr.String())
			}
		})
	}
}

func TestWorkChildDropsOnlyCompatibilityWrapperMarker(t *testing.T) {
	base := []string{"NDEV_PRESSURE_WRAPPER=1", "NDEV_PRESSURE_WRAPPER=other", WorkOperationEnv + "=parent", "GOMAXPROCS=2", "NCD_GO_TEST_MAX_SHARDS=2", "NDEV_PRESSURE_BIN=/sealed/product", "NICOS_PREFLIGHT_NDEV_BIN=/candidate", "GATE_BOX_HOST=runner-3"}
	got := withoutPressureWrapperMarker(base)
	if len(got) != len(base)-2 || workEnvValue(got, "NDEV_PRESSURE_WRAPPER") != "" {
		t.Fatalf("wrapper marker retained: %v", got)
	}
	for _, key := range []string{WorkOperationEnv, "GOMAXPROCS", "NCD_GO_TEST_MAX_SHARDS", "NDEV_PRESSURE_BIN", "NICOS_PREFLIGHT_NDEV_BIN", "GATE_BOX_HOST"} {
		if workEnvValue(got, key) != workEnvValue(base, key) {
			t.Fatalf("changed %s", key)
		}
	}
}
