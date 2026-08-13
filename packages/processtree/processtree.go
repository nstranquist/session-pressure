// Package processtree configures cancellable subprocesses so a timeout applies
// to the whole spawned process tree and cannot remain blocked on inherited
// stdout or stderr pipes.
package processtree

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"time"
)

// DefaultWaitDelay bounds pipe cleanup after cancellation or after the direct
// child exits while a descendant still owns an inherited output descriptor.
const DefaultWaitDelay = 2 * time.Second

// CommandContext is exec.CommandContext with process-tree cancellation and
// bounded output-pipe cleanup preconfigured.
func CommandContext(ctx context.Context, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, args...)
	Configure(cmd)
	return cmd
}

// Configure makes an existing command safe for context-bounded Run, Output,
// and CombinedOutput calls. It must be called before the command starts.
func Configure(cmd *exec.Cmd) {
	if cmd == nil {
		return
	}
	configurePlatform(cmd)
	if cmd.WaitDelay <= 0 {
		cmd.WaitDelay = DefaultWaitDelay
	}
}

// CredentialFreeEnvironment returns a fresh environment with credential-like
// variables removed by name. Credential-bearing parents should assign this to
// helper processes such as media tools and local CLIs that do not need provider
// authority. Names only are inspected; values are never copied into diagnostics.
func CredentialFreeEnvironment(ambient []string) []string {
	result := make([]string, 0, len(ambient))
	for _, pair := range ambient {
		name, _, ok := strings.Cut(pair, "=")
		if !ok || name == "" || isCredentialEnvironmentName(name) {
			continue
		}
		result = append(result, pair)
	}
	return result
}

// CurrentCredentialFreeEnvironment is the process-local convenience form.
func CurrentCredentialFreeEnvironment() []string {
	return CredentialFreeEnvironment(os.Environ())
}

func isCredentialEnvironmentName(name string) bool {
	upper := strings.ToUpper(strings.TrimSpace(name))
	for _, fragment := range []string{"_TOKEN", "_SECRET", "_PASSWORD", "_CREDENTIAL", "_API_KEY", "_PRIVATE_KEY", "_COOKIE"} {
		if strings.Contains(upper, fragment) {
			return true
		}
	}
	return false
}
