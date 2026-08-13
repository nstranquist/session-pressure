// Package processtree preserves the original nicos-dev import path for the
// standalone github.com/nstranquist/session-pressure/packages/processtree implementation.
package processtree

import (
	"context"
	"os/exec"

	base "github.com/nstranquist/session-pressure/packages/processtree"
)

// DefaultWaitDelay bounds pipe cleanup after cancellation or after the direct
// child exits while a descendant still owns an inherited output descriptor.
const DefaultWaitDelay = base.DefaultWaitDelay

// CommandContext is exec.CommandContext with process-tree cancellation and
// bounded output-pipe cleanup preconfigured.
func CommandContext(ctx context.Context, name string, args ...string) *exec.Cmd {
	return base.CommandContext(ctx, name, args...)
}

// Configure makes an existing command safe for context-bounded Run, Output,
// and CombinedOutput calls. It must be called before the command starts.
func Configure(cmd *exec.Cmd) {
	base.Configure(cmd)
}
