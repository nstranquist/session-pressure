# Process-tree bounded subprocesses

`github.com/nstranquist/session-pressure/packages/processtree` is the repository-wide subprocess primitive for
commands that capture output, attach subprocess pipes, or otherwise need a
context cancellation to terminate descendants as well as the direct child.

## Failure it prevents

`exec.CommandContext` kills only the direct child by default. When that child
spawns a descendant which inherits stdout or stderr, `Output`,
`CombinedOutput`, and some `Run` calls can remain blocked after the context
deadline because the descendant still owns the pipe descriptor.

On POSIX, `CommandContext` starts a new process group and makes cancellation
kill the group. On every platform, it sets a two-second `WaitDelay` so inherited
pipe cleanup is bounded even when the direct child exits first.

## Required use

Use `processtree.CommandContext` instead of `exec.CommandContext` when any of
these are true:

- stdout or stderr is captured in memory;
- `Output`, `CombinedOutput`, `StdoutPipe`, or `StderrPipe` is used;
- the command can spawn subprocesses and is controlled by a cancellable
  context;
- a long-running source or agent runner must not leave descendants behind.

Raw `exec.CommandContext` remains allowed only for the implementation itself,
platform-native equivalents with the same cancellation and wait-delay
contract, and commands whose stdio is wired directly to terminal, files, or
discarded output. Interactive commands which inherit terminal stdin should not
be moved into a background process group without explicitly preserving job
control semantics.

The compatibility import `github.com/nstranquist/session-pressure/pkg/processtree` delegates to
this module. Standalone modules should import `github.com/nstranquist/session-pressure/packages/processtree`
directly and keep a local `replace` directive for monorepo builds.

## Catalog boundary

`packages/processtree/catalog.yaml` owns this package as the shared
`context.processtree` cell. Cross-cell consumers declare contract-only access
to `command-context`; the contract resolves to the exported implementation in
`processtree.go`. Add a dependency only when the analyzer proves a real import,
and do not use the boundary baseline to exempt new consumers.

## Verification

From the repository root:

```text
go test ./packages/processtree/...
go test ./nicos-dev/pkg/processtree
GOOS=windows GOARCH=amd64 go test -c -o /tmp/processtree.test.exe ./packages/processtree
```

`TestCommandContextTimeoutDoesNotWaitOnDescendantOutput` reproduces the
original inherited-pipe hang with `sh -c "sleep 30 & wait"` and requires the
deadline path to return in under three seconds.

`TestRepositoryCommandContextPolicy` scans production Go files across the
repository. A new raw `exec.CommandContext` fails unless it is an exact,
reviewed allowlist entry. Each exception also declares its required nearby
process-group or terminal/file/discard stdio wiring, so preserving only the raw
command line is insufficient to pass the policy. The propagation receipt dated
2026-07-13 covered 197 protected production command constructions across 136
files: 190 through the shared module and seven through existing platform-native
equivalents. The July 14 closeout cross-compiled the package for Linux, Windows,
FreeBSD, NetBSD, OpenBSD, DragonFly BSD, Solaris, and AIX.
