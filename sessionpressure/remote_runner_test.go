package sessionpressure

import (
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
