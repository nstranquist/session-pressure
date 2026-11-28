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

## Review fixes (2026-11-28)

An independent review checked the rewrite against `cmd/go` and found
commands whose suggested form would not do what the original does. Each case
below now gives an exact command or the earlier text:

- Implicit output. `go build` of one main package with no `-o` writes the
  binary into the working directory, under the name `cmd/go` gives it: the
  last import path element, or the one before a major version element such
  as `v2`; for a file list, the first file without cgo. The rewrite adds
  `-o ../<name>`, so the binary lands in the same place. It names a file and
  not the directory, because the remote route needs `-o` for a build and
  places a directory output only where nothing exists yet. `go test -c`
  writes `<package>.test` files, and the rewrite adds `-o ../`, which Go
  reads as the same directory. A library package, several packages or a
  pattern only compile, so no `-o` is added. When the default name is an
  existing directory, Go refuses the build, and the hint gives the earlier
  text. The main-package check is the one the express lane uses
  (`goPackageDirIsMain`).
- One output across modules. A profile flag now counts as an output like
  `-o`, and so does a build with no `-o` (a split build could write a binary
  that the one build does not). With packages in several modules, these
  refusals name the modules only.
- Patterns in flags. `-coverpkg` and the `pattern=` part of `-gcflags`,
  `-ldflags`, `-asmflags` and `-gccgoflags` are read from the working
  directory, so relative patterns are rewritten. Keywords such as `all` and
  import paths stay. A relative pattern outside the module gives the
  earlier text.
- The `go test` package list. `go test` passes the positional arguments
  after its package list to the test binary, and it ends the list at a flag
  it does not know. Those arguments now stay as written, and a flag after
  them still moves. `go build` and `go vet` read every argument after the
  first package as a package, so a later flag gives the earlier text.
- Symlinks. `go build -o` is resolved from the physical directory, and
  `go test -o`, `-outputdir` and the profiles from `$PWD`. When a symlink
  between the working directory and the module makes these differ, a command
  with a moved path gives the earlier text.
- Spellings. `goFlagName` reads flags the way `cmd/go` does: one or two
  dashes and the optional `test.` prefix of a test flag. An empty
  `-outputdir` is the working directory, so it moves too.

The shared flag table also exposed a regression in the express lane. Since
`goPackageTargets` read `--o`, `--C` and `-test.coverprofile` as flags, a
command with one of them could count as one package while the output scan,
the directory scan and the full-weight check matched only the single-dash
spelling. A library build with `--o out.a` could then be replayed with no
declared artifact. `goOutputArg`, `expressEffectiveWorkDir`,
`goTestRequiresFullWeight` and `goTestRunsBenchmark` now read flag names
through `goFlagName`, and `goBoolFlags` lists the boolean flags that
`go test` knows.

The remote build had the same gap. `normalizeRemoteBuildArgv` matched `-o`
and `-o=` only, so a build spelled `--o` was refused with "requires -o", and
a `-o` that was the value of another flag (`-ldflags -o`) was taken as the
output. It now reads flag names through `goFlagName`, skips the values of
other flags and stops at `--`.

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
- After the release promotion, the installed command gave the same refusal,
  wrote no binary and logged `module_hint`.
- Review fixes: new cases in `TestRemoteModuleHintNamesTheOwningModule` and
  `TestRemoteModuleHintKeepsTheGenericRefusal`, plus
  `TestRemoteModuleHintNamesTheDefaultBinary`,
  `TestRemoteModuleHintKeepsTheGenericRefusalThroughASymlink` and
  `TestExpressReadsEveryGoFlagSpelling`. `go test ./...` passes.
- The default names were checked with the Go 1.27 toolchain: a package in
  module `example.test/cmd/v2` builds as `cmd`, its `v3` directory as `v2`,
  and a file list whose first file imports `C` and whose second has
  `//go:build ignore` builds as the second file's name.
- A development helper refused `go build ./scripts/<tool>` from a checkout
  with no root `go.mod` and gave `(cd scripts && go build -o ../<tool>
  ./<tool>)`. That command ran on the remote route and returned a darwin
  arm64 binary to the checkout root, where the original build writes it. A
  first version that added `-o ../` was refused by the remote route, which is
  why the rewrite names the file.
