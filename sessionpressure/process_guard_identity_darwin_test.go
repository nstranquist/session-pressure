//go:build darwin

package sessionpressure

import (
	"os"
	"os/exec"
	"testing"
)

func TestProcessGuardDarwinIdentityDistinguishesExit(t *testing.T) {
	child := exec.Command("/bin/sleep", "60")
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = child.Process.Kill(); _ = child.Wait() }()
	pid := child.Process.Pid
	birth, uid, err := processGuardIdentity(pid)
	if err != nil || birth == "" || uid != os.Getuid() {
		t.Fatalf("live identity: birth=%q uid=%d err=%v", birth, uid, err)
	}
	if err := child.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = child.Wait()
	if _, _, err := processGuardIdentity(pid); !processGuardGone(err) {
		t.Fatalf("exited child must have a typed gone result: %v", err)
	}
}
