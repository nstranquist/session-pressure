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
	if light != "env GOWORK=off GOMAXPROCS=1 GOMEMLIMIT=768MiB GOFLAGS='-p=1' 'go' 'test' './x'" {
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
