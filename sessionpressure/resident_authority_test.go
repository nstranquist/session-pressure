package sessionpressure

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestResidentAuthorityIsExclusiveAndReleases(t *testing.T) {
	dir := t.TempDir()
	release, err := AcquireResidentAuthority(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := AcquireResidentAuthority(dir); err == nil || !strings.Contains(err.Error(), "already held") {
		t.Fatalf("second resident authority acquisition=%v, want live-owner rejection", err)
	}
	lockBody, err := os.ReadFile(filepath.Join(dir, "resident-authority.lock"))
	if err != nil || strings.TrimSpace(string(lockBody)) == "" {
		t.Fatalf("resident authority diagnostic body=%q err=%v", lockBody, err)
	}
	release()

	secondRelease, err := AcquireResidentAuthority(dir)
	if err != nil {
		t.Fatalf("released resident authority remained locked: %v", err)
	}
	secondRelease()
}

func TestResidentAuthorityReleasesAfterOwnerSIGKILL(t *testing.T) {
	dir := t.TempDir()
	command := exec.Command(os.Args[0], "-test.run=^TestResidentAuthorityCrashHelper$")
	command.Env = append(os.Environ(), "NDEV_TEST_RESIDENT_AUTHORITY_DIR="+dir)
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(stdout)
	line, err := reader.ReadString('\n')
	if err != nil || strings.TrimSpace(line) != "ready" {
		_ = command.Process.Kill()
		_ = command.Wait()
		t.Fatalf("authority helper readiness=%q err=%v", line, err)
	}
	if _, err := AcquireResidentAuthority(dir); err == nil {
		_ = command.Process.Kill()
		_ = command.Wait()
		t.Fatal("parent acquired authority while subprocess owner was live")
	}
	if err := command.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = command.Wait()
	release, err := AcquireResidentAuthority(dir)
	if err != nil {
		t.Fatalf("kernel did not release authority after SIGKILL: %v", err)
	}
	release()
}

func TestResidentAuthorityCrashHelper(t *testing.T) {
	dir := os.Getenv("NDEV_TEST_RESIDENT_AUTHORITY_DIR")
	if dir == "" {
		return
	}
	if _, err := AcquireResidentAuthority(dir); err != nil {
		t.Fatal(err)
	}
	fmt.Fprintln(os.Stdout, "ready")
	select {}
}
