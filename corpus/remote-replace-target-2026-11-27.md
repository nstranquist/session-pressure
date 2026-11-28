# Missing `go.mod` `replace` targets fail as command errors (2026-11-27)

## Problem

Before uploading sources to a remote Go runner, `remoteModuleClosure` walks the
current module's `go.mod` and every local `replace` directive (those with no
target version) so replaced sibling modules travel alongside it.

When a local `replace` target was missing on disk (for example a temporary copy
of a repository without its replaced sibling, or a typo in `go.mod`),
`os.ReadFile` returned a bare path error:

```
remote Go work blocked: open /tmp/sibling/go.mod: no such file or directory;
use the Hetzner gate-runner with an explicit compiler profile; local fallback
is disabled
```

This had two faults:

1. **Opaque diagnosis.** The error named neither the `go.mod` that declared the
   replacement nor the `replace` directive itself (especially confusing for a
   transitive replacement), and ended with unrelated compiler-profile advice.
2. **Wrong failure class.** Wrapping the error in `workRefusal`
   (`ndev-work-refused/v1 kind=remote_profile`) and the `remote Go work
   blocked:` prefix told downstream commit-delivery workers that the host
   routing environment had failed before the command could run. Those workers
   match both `ndev-work-refused/v1` and `remote Go work blocked:` as
   environment faults: they hold the delivery on backoff retries (2 m, 10 m,
   30 m) and can trip shared producer circuit breakers.

In practice, 95 historical `remote_work_declined` control events hit this exact
`open <dir>/go.mod: no such file or directory` path (91 builds, 4 tests).

## Decision: command failure, not a coordinator refusal

A missing local `replace` target is a broken module tree, not a host routing or
runner-profile fault:

- Running `go build`, `go test` or `go vet` locally without remote routing
  fails immediately with exit code 1 (`replacement directory ... does not
  exist`).
- No compiler profile, runner change or environment-hold backoff retry can make
  a missing local directory appear.
- Treating it as a plain command failure (exit code 1, without
  `ndev-work-refused/v1` or `remote Go work blocked:`) lets a commit-delivery
  worker apply normal gate attribution: if the candidate change broke `go.mod`,
  the check passes on the target branch and pauses immediately with clear
  attribution; if the target branch itself is broken, the delivery holds on a
  red-target hold rather than tripping host environment retries and shared
  producer circuit breakers.
- `TryRemoteWork` still returns `handled = true`, so a missing `replace` target
  never falls back into local Mac admission.

## Change

`sessionpressure/remote_work.go`:

- `remoteModuleClosure` tracks the immediate parent `go.mod` and
  `*modfile.Replace` edge while walking local replacements. When reading a
  replacement's `go.mod` fails because the target directory does not exist, is
  not a directory, or exists without a `go.mod`, it returns a structured
  `*remoteReplaceTargetError`. Transitive errors bubble unchanged so the error
  always names the `go.mod` that actually wrote the broken directive.
- The error formats the owning `go.mod` (relative to the working directory when
  nearby via `shownGoMod`), the `replace` directive (including any old version),
  and the resolved missing directory path:
  - `go.mod: replace example.test/b => ../b: replacement directory /path/to/b does not exist`
  - `go.mod: replace example.test/b => ../b: replacement directory /path/to/b has no go.mod`
  - `go.mod: replace example.test/b => ../b: replacement directory /path/to/b is not a directory`
- `TryRemoteWork` routes `*remoteReplaceTargetError` to
  `failRemoteReplaceTarget`, which records a `remote_work_failed` control event
  (`operation`, `reason`, `go_mod`, `replace_old`, `replace_new`,
  `missing_target`, `local_blocked: true`) and returns `(true, 1, err)` without
  `WorkRefusalMarker` or `remote Go work blocked:`.

## Verification

- `go test -count=1 ./sessionpressure/...` passes, including new tests in
  `sessionpressure/remote_work_test.go` covering direct, versioned, transitive,
  absolute, missing-`go.mod`, and non-directory replace targets, plus the
  non-refusal exit-1 contract and `TryRemoteWork` entry point on Darwin.
- `go vet ./...` and the repository pre-commit capture checks (`public-names`,
  `public-secrets`, `conflict-markers`, `go-format`) pass.
