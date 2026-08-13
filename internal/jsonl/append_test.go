package jsonl

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nstranquist/session-pressure/internal/lockfile"
)

func TestAppendLineWritesOneLockedJSONLRow(t *testing.T) {
	path := filepath.Join(t.TempDir(), "queries.jsonl")

	if err := AppendLine(path, []byte(`{"q":"workspace"}`), 0o644); err != nil {
		t.Fatalf("AppendLine: %v", err)
	}

	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	if string(body) != "{\"q\":\"workspace\"}\n" {
		t.Fatalf("body = %q", body)
	}
	if _, err := os.Stat(path + ".lock"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("lock sidecar should be cleaned up, stat err=%v", err)
	}
}

func TestAppendLineTightensPrivateLogPermissions(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "private")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "events.jsonl")
	if err := os.WriteFile(path, []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := AppendLine(path, []byte(`{"ok":true}`), 0o600); err != nil {
		t.Fatalf("AppendLine: %v", err)
	}
	if info, err := os.Stat(dir); err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("dir mode = %v err=%v, want 0700", info.Mode().Perm(), err)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("file mode = %v err=%v, want 0600", info.Mode().Perm(), err)
	}
}

func TestAppendLineWaitsForFileLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "queries.jsonl")
	unlock, err := lockfile.Lock(path)
	if err != nil {
		t.Fatalf("pre-lock file: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		done <- AppendLine(path, []byte(`{"q":"workspace"}`), 0o644)
	}()

	select {
	case err := <-done:
		t.Fatalf("AppendLine finished while lock was held: %v", err)
	case <-time.After(100 * time.Millisecond):
	}

	unlock()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("AppendLine after unlock: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("AppendLine did not finish after lock release")
	}

	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	if got := strings.Count(string(body), "\n"); got != 1 {
		t.Fatalf("newline count = %d, want 1; body=%q", got, body)
	}
}

func TestAppendLineWithinBoundsHeldLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "queries.jsonl")
	lockBody := fmt.Sprintf(`{"pid":%d,"timestamp":%d,"token":"other-process"}`+"\n", os.Getpid(), time.Now().UnixMilli())
	if err := os.WriteFile(path+".lock", []byte(lockBody), 0o644); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	err := AppendLineWithin(path, []byte(`{"q":"workspace"}`), 0o644, 25*time.Millisecond)
	if !errors.Is(err, lockfile.ErrLocked) {
		t.Fatalf("AppendLineWithin error = %v, want ErrLocked", err)
	}
	if elapsed := time.Since(started); elapsed > 150*time.Millisecond {
		t.Fatalf("AppendLineWithin elapsed = %s, want bounded contention", elapsed)
	}
}
