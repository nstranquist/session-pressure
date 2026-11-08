//go:build darwin

package sessionpressure

import (
	"bufio"
	"context"
	"encoding/binary"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// This is a copy of the test executable. It sleeps on stdin and never starts a
// browser, opens a profile, or signals another process.
func TestBrowserKernelInventoryHelper(t *testing.T) {
	if os.Getenv("PRESSURE_BROWSER_INVENTORY_HELPER") != "1" {
		return
	}
	if _, err := os.Stdout.WriteString("ready\n"); err != nil {
		t.Fatal(err)
	}
	var one [1]byte
	_, _ = os.Stdin.Read(one[:])
}

func TestNativeBrowserInventoryFindsOwnedSleepingExecutable(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "Fixture.app", "Contents", "MacOS", "Google Chrome")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	input, err := os.Open(self)
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	output, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0700)
	if err != nil {
		t.Fatal(err)
	}
	_, copyErr := io.Copy(output, input)
	closeErr := output.Close()
	if copyErr != nil {
		t.Fatal(copyErr)
	}
	if closeErr != nil {
		t.Fatal(closeErr)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	child := exec.CommandContext(ctx, path, "-test.run=^TestBrowserKernelInventoryHelper$", "--", "--headless=new")
	child.Env = append(os.Environ(), "PRESSURE_BROWSER_INVENTORY_HELPER=1")
	stdin, err := child.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := child.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stdin.Close(); _ = child.Process.Kill(); _ = child.Wait() }()
	if line, err := bufio.NewReader(stdout).ReadString('\n'); err != nil || line != "ready\n" {
		t.Fatalf("sleeping fixture did not start: %q %v", line, err)
	}
	roots, err := automatedBrowserProcesses(ctx)
	if err != nil {
		t.Fatal(err)
	}
	identity, _, err := processGuardIdentity(child.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	for _, root := range roots {
		if root.PID == child.Process.Pid {
			if root.StartIdentity != identity {
				t.Fatalf("birth mismatch: %q != %q", root.StartIdentity, identity)
			}
			return
		}
	}
	t.Fatal("owned sleeping fixture missing from native browser roots")
}

func TestNativeBrowserInventoryReadsKernelLayoutWithoutLaunchingBrowser(t *testing.T) {
	var reader darwinProcessArgsReader
	if _, err := reader.args(os.Getpid()); err != nil {
		t.Fatal(err)
	}
	if got, err := automatedBrowserKernelArgs(reader.buffer); err != nil || got {
		t.Fatalf("test executable misclassified: automated=%v err=%v", got, err)
	}
	roots, err := automatedBrowserProcesses(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, root := range roots {
		if root.PID <= 1 || root.StartedAt.IsZero() || root.StartIdentity == "" {
			t.Fatalf("unbound native root: %+v", root)
		}
	}
}

func TestNativeBrowserInventoryRejectsStaleArgumentTail(t *testing.T) {
	chrome := "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"
	body := browserKernelArgsFixture(chrome, []string{chrome}, nil)
	written := len(body)
	// Simulate a truncated response with argc=2 and an automation flag left
	// beyond the kernel's written length from a prior, longer process.
	binary.LittleEndian.PutUint32(body[:4], 2)
	body = append(body, []byte("--headless=new\x00")...)
	reader := darwinProcessArgsReader{buffer: body}
	if _, err := reader.parseResponse(uintptr(written)); err != nil {
		t.Fatal(err)
	}
	if len(reader.buffer) != written {
		t.Fatalf("retained stale response tail: %d != %d", len(reader.buffer), written)
	}
	if automated, err := automatedBrowserKernelArgs(reader.buffer); automated || err == nil {
		t.Fatalf("stale tail authorized browser: automated=%v err=%v", automated, err)
	}
}

func BenchmarkNativeBrowserInventory(b *testing.B) {
	for range b.N {
		if _, err := automatedBrowserProcesses(context.Background()); err != nil {
			b.Fatal(err)
		}
	}
}
