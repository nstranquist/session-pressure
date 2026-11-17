# Commit-delivery lane enrolment (WORK-1121, 2026-11-17)

## Why

Until this change, session-pressure had no `.ndev/commit-delivery.yaml` and no
pre-commit hook. `ndev-commit-delivery repo commit` refused with
`POLICY_NOT_FOUND`, and the fallback (`ndev-guarded-commit single`) replayed an
empty hook. The gates were whatever each agent chose to run. The repository is
public, and on 2026-11-17 a history scrub had to rewrite private host names
that had already landed and been pushed.

## What is enrolled

`.ndev/commit-delivery.yaml`:

| Stage | Check | Where |
| --- | --- | --- |
| Capture | repo hook, capture profile: `public-names`, `public-secrets`, `conflict-markers`, `go-format` | Mac, inside the 30 s capture budget |
| Capture | `go vet` of the captured packages (module `.`), so Darwin-only files are type-checked | Mac |
| Gate | `go vet ./...` and `go test -count=1 -timeout 10m ./...` (`remote_pass`, linux/amd64) | active Linux gate runner (runner clone `session-pressure`) |
| Gate | `go test` in `internal/helpermain`, scoped to that directory | Mac, darwin/arm64 |
| Gate | `swift test` in `apps/SessionPressure`, scoped to that directory | Mac, darwin/arm64 |
| Merge | append_merge for `CHANGELOG.md`, `corpus/**/*.md`, `docs/**/*.md` | worker |
| Publish | `remote.require_visibility: PUBLIC`; `delivery remote publish` scans the outgoing range with gitleaks | Mac |

Exemptions with reasons: node (no JavaScript), python (a release-time image
helper and a live installed-app check, not candidate checks), docs (no
checker; the hook still scans Markdown for private names and secrets).

Not a lane gate: the Darwin runtime tests of `./sessionpressure`. They
measure the live host (RSS budget, CPU soak, kernel inventory) or use
real-time deadlines. Under the worker's own load on the Mac they failed three
runs in a row with three different timing errors (see Bootstrap findings), at
about 5 min a run, against 30 s for the whole package on the Linux runner.
Capture `go vet` type-checks them, the Linux gate runs the portable tests, and
`make publish-ready` runs them before a release.

Also not covered by a lane gate: `apps/pressure-ui` (Rust, GPUI). The policy has no
Rust adapter. Its check stays `make desktop-gpui` before a release. This is a
known gap, not a decision that it needs no gate.

The Linux suite passed on the runner before enrolment: `go vet ./...` plus the
full `go test` in about 45 s (job 9d093bab, peak 0.8 GB).

## The public-repo check

`.ndev/hooks/pre-commit` is the repo-owned hook. Install it with
`cp .ndev/hooks/pre-commit "$(git rev-parse --git-path hooks/pre-commit)"`.
It scans only the added lines and new file names of the staged diff against
the capture base.

- `public-names` reads private patterns, one Perl regex per line, from
  `~/.nicos-dev/public-repo-check/patterns.txt` (mode 0600) on the host. The
  list is private, so it is not in this repository. It holds the history-scrub
  patterns (runner host names, tailnet addresses, Mac host names, home paths,
  the user name) plus the private workers.dev handle. A failure names the file
  and the rule number, never the matched text.
- `public-secrets` pipes the added lines to `gitleaks stdin --redact`.
- Both fail closed: a missing pattern file, an invalid rule, or a missing
  gitleaks refuses the capture.
- Override the pattern file with `NICOS_PUBLIC_CHECK_PATTERNS` (tests only).

Shell plus Perl, not Go: the hook must start with no build step inside the
capture budget, and Perl gives the same regex dialect as the scrub that
produced the patterns.

To add a new host, copy the pattern file to the new Mac. Without the file,
every capture on that Mac fails closed with a `public-names` repair line.

## Runner clone

`/srv/build/clones/session-pressure` on the active runner, remote
`github-session-pressure:nstranquist/session-pressure` (the deploy-key alias
the neighbourhood clone already uses). `refresh-clones.sh session-pressure`
fetches it, so a push does not strand the clone (unlike the bundle-seeded
clones). The host config maps the Mac checkout to the clone name in
`gate-delegate.json`, and the fleet inventory already lists the repository on
the active runner.

## Proof

Filled in below by the enrolment delivery and the rejection test.

### Bootstrap findings

- A delivery that adds the policy reads it from the worktree. A worktree
  policy cannot delegate (`remote_pass` is honoured only from a committed
  policy), so the first attempt (840f7c88) ran the Linux `go test` on the Mac
  and paused with `VERIFY_CAPABILITY_UNAVAILABLE`. The enrolment therefore
  lands in steps: first the policy without the Linux checks, then a delivery
  that adds them, then the first delivery gated by the full committed policy.
- A policy change runs every check, also the path-scoped ones. The Darwin
  `sessionpressure` tests then failed in the worker:
  `TestRemoteToolchainPreflight` looked for the word "ran" in refusal output
  that quotes the compiler path, and the worker's candidate directory is under
  a home directory whose name contains "ran". The test now uses a sentinel
  that no path contains.
- The next two runs failed on real-time tests, not on the change:
  `work_prestart_refusal_test.go:76` (the 250 ms deadline expired in the
  admission wait, before the reacquire boundary the test expects) and
  `worknested_test.go:135` (`wait for heavy work: context deadline exceeded`).
  Both pass on the quiet Linux runner. The Darwin package run was dropped from
  the gate (above). Making these tests load-independent is a follow-up.
- Capture `tier: admission` skips `go vet` at capture (the worker owns
  compiler checks). The policy leaves the tier unset so that capture vets the
  captured packages on the Mac.
