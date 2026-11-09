package sessionpressure

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestRemoteRunnerPreludeUsesCanonicalToolchain(t *testing.T) {
	if _, err := os.Stat("/srv/build/bin/git"); err != nil {
		t.Skip("requires the canonical Hetzner runner toolchain")
	}
	dir := t.TempDir()
	for _, name := range []string{"current", "base", "other"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("same\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	// Start with a hostile login environment. The production prelude must
	// select pinned Git, restore the shared caches, and set the test umask.
	command := remoteRunnerPrelude() + `test "$(command -v git)" = /srv/build/bin/git &&
test "$GOCACHE" = /srv/build/cache/go-build &&
test "$GOMODCACHE" = /srv/build/cache/go-mod &&
test "$GOPATH" = /srv/build/go &&
test "$(umask)" = 0022 &&
git merge-file --diff-algorithm=histogram current base other`
	cmd := exec.Command("/bin/bash", "-c", command)
	cmd.Dir = dir
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "PATH=") && !strings.HasPrefix(entry, "GOCACHE=") {
			cmd.Env = append(cmd.Env, entry)
		}
	}
	cmd.Env = append(cmd.Env, "PATH=/usr/bin:/bin", "GOCACHE=/private/invalid-cache")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("runner environment did not support the canonical Git operation: %v: %s", err, out)
	}
}

func TestRunnerSubscriberReconnectPreservesExactlyOneTranscript(t *testing.T) {
	calls := 0
	var out bytes.Buffer
	safe, err := runRunnerSubscriber(context.Background(), func() *exec.Cmd {
		calls++
		data, code := "aGVsbG8=", "255"
		if calls > 1 {
			data, code = "aGVsbG8gd29ybGQ=", "0"
		}
		return exec.Command("sh", "-c", `printf '%s\n' '@@gaterunner {"state":"ready","protocol":1}' >&2; printf '%s\n' '@@gaterunner-output {"job_id":"job-1","offset":0,"data":"`+data+`"}'; exit `+code)
	}, &out, io.Discard)
	if err != nil || !safe || calls != 2 || out.String() != "hello world" {
		t.Fatal(safe, err, calls, out.String())
	}
}

func TestRunnerSubscriberAmbiguousFailurePreservesInputs(t *testing.T) {
	calls := 0
	safe, err := runRunnerSubscriber(context.Background(), func() *exec.Cmd { calls++; return exec.Command("sh", "-c", "exit 255") }, io.Discard, io.Discard)
	if safe || err == nil || calls != 1 {
		t.Fatal(safe, err, calls)
	}
	// An acknowledged service error is not a guest verdict.
	safe, err = runRunnerSubscriber(context.Background(), func() *exec.Cmd {
		return exec.Command("sh", "-c", `printf '%s\n' '@@gaterunner {"state":"ready","protocol":1}' >&2; exit 2`)
	}, io.Discard, io.Discard)
	if safe || err == nil {
		t.Fatal(safe, err)
	}
}

func TestRunnerSubscriberGuest255IsTerminal(t *testing.T) {
	calls := 0
	safe, err := runRunnerSubscriber(context.Background(), func() *exec.Cmd {
		calls++
		return exec.Command("sh", "-c", `printf '%s\n' '@@gaterunner {"state":"ready","protocol":1}' '@@gaterunner {"state":"failed","job_id":"job-1","exit":255}' >&2; exit 255`)
	}, io.Discard, io.Discard)
	if !safe || err == nil || calls != 1 {
		t.Fatal(safe, err, calls)
	}
}

func TestRunnerSubscriberRejectsOutputGaps(t *testing.T) {
	s := &runnerSubscriber{}
	w := &runnerStream{s: s, output: io.Discard}
	w.line([]byte(`@@gaterunner-output {"job_id":"job-1","offset":1,"data":"YQ=="}`))
	if s.err == nil {
		t.Fatal("missing output accepted")
	}
}
