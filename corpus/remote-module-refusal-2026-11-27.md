# The "requires a module" refusal names the module to run from (2026-11-27)

## Problem

Remote Go routing sends a direct `go build`, `go test` or `go vet` to the
runner. It first looks for a `go.mod` at or above the working directory.
When there is none, it refuses the command:

```
remote Go work blocked: remote Go work requires a module; use the Hetzner
gate-runner with an explicit compiler profile; local fallback is disabled
```

A checkout often keeps its Go code in a nested module, for example
`scripts/go.mod` with no `go.mod` at the root. A build of
`scripts/tool/main.go` from the root then got this refusal. The advice was
wrong for this case: a compiler profile does not help. The fix is to run the
same command from the module directory, and the text did not say so.

## Change

`sessionpressure/remote_work_module_hint.go`:

- `remoteModuleClosure` returns the sentinel `errRemoteNoModule`.
  `TryRemoteWork` sends that case to `rejectRemoteModule`.
- `remoteModuleHint` resolves each named `.go` file and each relative package
  path (with an optional `/...` pattern) to the nearest `go.mod` above it.
  When every argument has an owner, the refusal names the `go.mod` and gives
  the same command run from that directory in a subshell. Arguments, `-o`,
  `-pkgdir` and `-outputdir` are rewritten relative to the module. The
  profile outputs (`-coverprofile`, `-cpuprofile` and the rest) are rewritten
  too, except when `-outputdir` is set, because Go then reads them relative to
  that directory. Other flags and the test binary arguments after `-args`
  stay as written. Values that need it are shell-quoted.
- `go build -C dir`: the refusal says that the remote route does not follow
  `-C`, names the `go.mod` that owns `dir` and gives
  `(cd dir && go build ...)` with the other arguments unchanged.
- Packages in more than one module: one command for each module. With `-o`,
  the refusal names the modules only, because one output cannot be split
  across several builds.
- No hint, and the earlier text unchanged: an import path, a pattern over
  the directory with no module (`go test ./...` at the root), a missing file
  or directory, `--`, a `-C` that is not the first flag (Go rejects it), a
  value flag with no value, or a path with no `go.mod` above it.

Example, from a checkout whose only module is `scripts/go.mod`:

```
go build -o tmp/bin/tool scripts/tool/main.go
remote Go work blocked: remote Go work requires a module; the named files are
in scripts/go.mod, so run the command from that module:
(cd scripts && go build -o ../tmp/bin/tool tool/main.go); local fallback is
disabled
```

What stays the same: the exit code (`workExitPolicyDenied`), the
`ndev-work-refused/v1 kind=remote_profile` marker, the "NOT RUN" statement,
the `remote Go work blocked: remote Go work requires a module` prefix and
the `local fallback is disabled` ending. The command still does not run. The
`remote_work_declined` control event keeps the reason
`remote Go work requires a module` and adds `module_hint` (the `go.mod`
paths that the refusal names).

`rejectRemoteWork` now calls `refuseRemoteGo(dir, operation, reason, advice,
attrs)`. The other refusals pass `remoteRunnerAdvice`, which is the earlier
text.

### One Go flag table

`goFlagsWithValue` (`remote_work_eligibility.go`) is now the one table of Go
flags that take a value. `goPackageTargets` and `goTestRunsBenchmark`
(`work_express.go`) had their own shorter lists. They now read this table,
and the table adds `-vet`, `-blockprofilerate`, `-memprofilerate` and
`-mutexprofilefraction`. Before, the express lane counted the value of
`-shuffle on`, `-buildmode pie`, `-list X` or `--tags x` as a package, and
`-list -bench ./pkg` as a benchmark run.

## Decisions

- Keep the refusal. Following `-C` or the file paths automatically was
  considered and rejected for this change: it would change which directory
  the remote route uploads and runs in, and a refusal with an exact command
  is enough to recover.
- The suggested command uses a subshell, `(cd dir && ...)`, so that a
  pasted command leaves the caller's working directory as it was.
- File arguments stay files, relative to the module. A file list is not
  turned into a package path, because that changes which files Go builds.

## Verification

- `go test ./sessionpressure/` passes. New tests in
  `remote_work_module_hint_test.go`: the owning module for files, packages,
  patterns, `-C`, two modules, a module beside the working directory, flag
  rewriting and quoting; the cases that keep the earlier text; the refusal
  contract (marker, kind, prefix, exit code, control event); the
  `TryRemoteWork` entry point on darwin; and the shared flag table.
- A development build of the work helper, run from a checkout with no root
  `go.mod`, refused `go build -o tmp/bin/x scripts/<tool>/main.go` with exit
  11 and the new text. The suggested `(cd scripts && go build ...)` built the
  binary.
