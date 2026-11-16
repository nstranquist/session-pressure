package sessionpressure

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestRemoteGoInvocationPinsOnlyFixedHeavyAndLight(t *testing.T) {
	light := remoteGoInvocation("light", "1", "768MiB", "1", "", "", []string{"go", "test", "./x"})
	if !strings.HasPrefix(light, `if [ "${NCD_RUNNER_LANE:-light}" = light ]; then env GOWORK=off GOMAXPROCS=1 GOMEMLIMIT=768MiB GOFLAGS='-p=1' 'go' 'test' './x'; else if [ "${NCD_CPU_MODE:-fixed}" = elastic ]; then `) {
		t.Fatalf("light = %s", light)
	}
	heavy := remoteGoInvocation("heavy", "2", "3GiB", "2", "-tags=x", " GOOS=darwin", []string{"go", "build", "./cmd"})
	for _, want := range []string{
		`if [ "${NCD_CPU_MODE:-fixed}" = elastic ]; then `,
		`env -u GOMAXPROCS GOWORK=off GOMEMLIMIT=3GiB GOFLAGS='-tags=x' GOOS=darwin 'go' 'build' './cmd'; else `,
		`env GOWORK=off GOMAXPROCS=2 GOMEMLIMIT=3GiB GOFLAGS='-tags=x -p=2' GOOS=darwin 'go' 'build' './cmd'; fi`,
	} {
		if !strings.Contains(heavy, want) {
			t.Fatalf("heavy = %s\nmissing %s", heavy, want)
		}
	}
}

// The compound must keep the command's exit status and environment in both
// modes, because the artifact seal is chained after it with &&.
func TestRemoteGoInvocationShellSemantics(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell")
	}
	if _, err := exec.Command("env", "-u", "X", "true").CombinedOutput(); err != nil {
		t.Skip("env -u unavailable")
	}
	dir := t.TempDir()
	probe := filepath.Join(dir, "probe.sh")
	if err := os.WriteFile(probe, []byte("#!/bin/sh\nprintf '%s|%s' \"${GOMAXPROCS:-unset}\" \"$GOFLAGS\"\nexit \"$1\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	run := func(mode, code string) (string, error) {
		script := remoteGoInvocation("heavy", "2", "3GiB", "2", "", "", []string{probe, code}) + " && printf ' sealed'"
		cmd := exec.Command("sh", "-c", script)
		cmd.Env = append(os.Environ(), "GOMAXPROCS=9", "NCD_CPU_MODE="+mode)
		out, err := cmd.Output()
		return string(out), err
	}
	if out, err := run("elastic", "0"); err != nil || out != "unset| sealed" {
		t.Fatalf("elastic = %q, %v", out, err)
	}
	if out, err := run("fixed", "0"); err != nil || out != "2|-p=2 sealed" {
		t.Fatalf("fixed = %q, %v", out, err)
	}
	if out, err := run("elastic", "3"); err == nil || out != "unset|" {
		t.Fatalf("a failing command must stop the seal: %q, %v", out, err)
	}
}

// A light request that the runner admits heavy (a rerun after the light
// caps, or a runner without a light lane) must run with heavy limits: the
// light pins made two cmd/ndev-go reruns crawl until they were killed as
// stalled (2026-11-15). Without NCD_RUNNER_LANE (an older runner) the
// requested light pins still apply.
func TestRemoteGoInvocationLightFollowsTheAdmittedLane(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell")
	}
	if _, err := exec.Command("env", "-u", "X", "true").CombinedOutput(); err != nil {
		t.Skip("env -u unavailable")
	}
	dir := t.TempDir()
	probe := filepath.Join(dir, "probe.sh")
	if err := os.WriteFile(probe, []byte("#!/bin/sh\nprintf '%s|%s|%s' \"${GOMAXPROCS:-unset}\" \"$GOMEMLIMIT\" \"$GOFLAGS\"\nexit \"$1\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	lane, procs, memlimit, parallel := remoteLane(WorkRunOptions{Class: WorkClassExpressTest, Command: []string{"go", "test", "./cmd/ndev-go/"}})
	run := func(code string, env ...string) (string, error) {
		script := remoteGoInvocation(lane, procs, memlimit, parallel, "", "", []string{probe, code}) + " && printf ' sealed'"
		cmd := exec.Command("sh", "-c", script)
		cmd.Env = append(append(os.Environ(), "GOMAXPROCS=9"), env...)
		out, err := cmd.Output()
		return string(out), err
	}
	for _, c := range []struct {
		name string
		env  []string
		want string
	}{
		{"older runner", []string{"NCD_CPU_MODE=elastic"}, "1|768MiB|-p=1 sealed"},
		{"light attempt", []string{"NCD_RUNNER_LANE=light", "NCD_CPU_MODE=fixed"}, "1|768MiB|-p=1 sealed"},
		{"heavy rerun, fixed", []string{"NCD_RUNNER_LANE=heavy", "NCD_CPU_MODE=fixed"}, "2|3GiB|-p=2 sealed"},
		{"heavy rerun, elastic", []string{"NCD_RUNNER_LANE=heavy", "NCD_CPU_MODE=elastic"}, "unset|3GiB| sealed"},
	} {
		if out, err := run("0", c.env...); err != nil || out != c.want {
			t.Fatalf("%s = %q, %v; want %q", c.name, out, err, c.want)
		}
	}
	if out, err := run("3", "NCD_RUNNER_LANE=heavy", "NCD_CPU_MODE=elastic"); err == nil || out != "unset|3GiB|" {
		t.Fatalf("a failing heavy rerun must stop the seal: %q, %v", out, err)
	}
}
