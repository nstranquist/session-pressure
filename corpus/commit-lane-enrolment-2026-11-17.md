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
| Gate | `go test -short` in `sessionpressure`, scoped to that directory (WORK-1157) | Mac, darwin/arm64 |
| Gate | `go test` in `internal/helpermain`, scoped to that directory | Mac, darwin/arm64 |
| Gate | `swift test` in `apps/SessionPressure`, scoped to that directory | Mac, darwin/arm64 |
| Merge | append_merge for `CHANGELOG.md`, `corpus/**/*.md`, `docs/**/*.md` | worker |
| Publish | `remote.require_visibility: PUBLIC`; `delivery remote publish` scans the outgoing range with gitleaks | Mac |

Exemptions with reasons: node (no JavaScript), python (a release-time image
helper and a live installed-app check, not candidate checks), docs (no
checker; the hook still scans Markdown for private names and secrets).

The Darwin runtime tests of `./sessionpressure` were first left out of the
lane: under the worker's own load on the Mac they failed three runs in a row
with three different timing errors (see Bootstrap findings). WORK-1157 made
them load-independent and restored them as `darwin-sessionpressure-tests`
with `-short`. See "Darwin sessionpressure gate (WORK-1157)" below.

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

- Hetzner gate: delivery 0e2de841 landed as 1af316d, the first delivery
  gated by the full committed policy. `go test -count=1 -timeout 10m ./...`
  (65 s) and `go vet ./...` (24 s) ran remotely on linux/amd64 on the active
  runner (jobs 43cc660a, d8764b0a). The path-scoped Mac checks reported
  `skipped_out_of_scope`.
- Rejection: a capture of a Markdown file that names a runner host failed
  before a delivery existed: `CAPTURE_CHECK_FAILED`, gate `public-names`,
  path `corpus/public-check-probe.md (rule 1)`, capture d100c9d6, "no
  delivery was created". The first probe attempt was malformed (the shell
  dropped the host number), so it held no private name, was accepted
  (138ac4e6) and was superseded before it could land. Build such probes
  from parts and check them with the pattern file before the capture.
- Publication: `delivery verify --full` passed for 1af316d and 86049a8 (about
  140 s each, darwin, Linux commands authorized by `remote_pass`).
  `delivery remote publish` pushed e336428..86049a8 at 14:23:39Z (receipt
  20261117T142339.363380000Z-86049a8a35a1, gitleaks 8.30.1, 0 findings).
  A scan of the same range with the private pattern file found no match in
  added lines, file names or commit messages.

## Darwin sessionpressure gate (WORK-1157)

The tests that failed under the worker on the Mac did not have wrong logic.
They raced wall-clock budgets against host load. WORK-1157 removed the races
with these rules. A new test in `sessionpressure` follows them
(`sessionpressure/timing_seams_test.go`):

- A test that asserts a wait-budget outcome ends the budget itself.
  `WorkRunStreams.WaitDeadline` is an optional seam (nil keeps
  `context.WithTimeout`). `manualDeadline` reports
  `context.DeadlineExceeded` at the stage that the test picks. The prestart
  refusal stages (capacity, reacquire, before start) use it. A seam context
  must cancel its children synchronously. The runner wraps the wait context
  in `signal.NotifyContext`. A custom parent without `AfterFunc` is
  cancelled through a goroutine, so the next stage can run before it sees the
  spent budget. `manualDeadline` implements `AfterFunc` and a real
  `Deadline`, and `TestManualDeadlineCancelsDerivedContextsSynchronously`
  guards it.
- A test that asserts success does not race its wait budget. It uses
  `testWaitNotUnderTest`.
- A wait for an event that the test expects (a child PID, a queue entry, a
  goroutine result) polls the condition and uses `testWatchdog` (2 min) only
  as a hang guard. To prove "does not block", the test makes the blocking
  path impossible to finish and checks that the call returns (heartbeat
  refresh). It does not time the call. Where a fallback poll could hide a
  missed wake, the test turns off the poll (`workCapacityRetryInterval` is a
  var for that).
- Two callers that must overlap meet at a condition, not inside a sleep.
  `expressSingleflightWaitHook` (nil in production) marks when a caller has
  fixed its start time and reached the singleflight lock. The reuse fixture
  blocks on a release file until both callers are there.
- A test that needs a directory outside every repository uses
  `tempDirOutsideRepository`. A commit-delivery candidate puts TMPDIR inside
  its own git checkout, so `t.TempDir()` is not outside a repository there.
  To reproduce, run the package with TMPDIR inside a `git init` directory.
- A fixed production bound that a test does not check (a lock or listing
  timeout) is a package var, and the test raises it to `testWatchdog`:
  `processIncidentLockTimeout` (2 s) and `expressSourceListTimeout` (15 s).
  The production value does not change.
- A CPU window starts after the process has settled (its CPU stops
  changing), not at `Start`, so shell startup is not counted.
- CPU and unit checks compare two counters of the same quantity (libproc
  against getrusage), or wait for an amount of CPU, not for a time window.
- Live-host budgets (native inventory and disk-scan latency, sampler
  duration, resident RSS, the 64-sample soak, live phase timing) are
  measurements, not logic. `liveHostBudgets()` is false under `-short`, so
  the gate keeps the correctness checks of those tests and drops only the
  budget. `make test` and `make publish-ready` run without `-short` and keep
  the budgets.

Evidence on the Mac, 2026-11-17, with the package test binary run directly:

- Before (old code, load about 45 to 58): 3 package runs, 3 failed. There
  were 6 distinct failures: prestart reacquire, profile `failure` (20 s
  watchdog), FIFO reservation (1 s watchdog), reacquisition recheck (1 s
  wait, code 124), operator sample 1728 ms > 1500 ms, and the CPU soak.
- After: 5 `-short` runs passed at 1-minute load 34, 60, 83, 130 and 134
  (90 to 148 s each). 1 full run with budgets passed at load 24 (124 s).
  The changed tests also passed under `-race`.
- The first full verify of cc83db6 failed. The prestart reacquire stage
  raced the async seam cancel. Two tests assumed a temp dir outside every
  repository. With TMPDIR inside a `git init` directory, a local package run
  showed two more: the remote source size test (same cause) and the reuse
  singleflight test (100 ms overlap window, failed at load 65). Delivery
  4932c07a fixed all of them. Then the prestart test passed 30 runs, the
  changed tests passed 5 `-race` runs in that layout, and the `-short`
  package passed in that layout in 109 s. It landed as d0e5bc5. Its worker
  gate was the first to run `darwin-sessionpressure-tests`: passed in 57 s on
  the Mac. The Linux `go test ./...` passed in 139 s on the Linux gate runner.
  `delivery verify --full --ref d0e5bc5` passed all 5 policy checks in
  268 s.
- The next three Darwin gate runs each found one more load race: the 60 ms
  browser admission deadline, the idle group CPU window, and the 2 s
  incident lock. A local stress found the last one before the gate did:
  three `-short` package runs at once, with TMPDIR inside a `git init`
  directory. The reuse singleflight test failed 2 of 6 there, from the 15 s
  source listing bound. 3c4211d fixed all four. Its gate and
  `delivery verify --full` passed (143 s), and it was published to origin.
- Publish found a home-path finding in an earlier commit (6809ddc) after the
  tip had removed it, because publish scans every outgoing commit. The value
  was a one-letter placeholder user, so it was accepted with a recorded
  reason. Use `/Users/example` in fixtures from the start.

## Follow-ups

- Done (WORK-1157): make the real-time `sessionpressure` tests independent
  of host load, then restore a Darwin runtime check for `./sessionpressure`.
  See "Darwin sessionpressure gate (WORK-1157)".
- WORK-1225: the Linux full verify once showed two holders inside
  `internal/filelock` at the same time (TestExclusiveCriticalSectionAcrossProcesses).
  That is a real mutual-exclusion bug, not a timing test.
- Before a delivery that touches `sessionpressure` tests, run the stress
  above. One gate run finds only one load race at a time.
- The Darwin `-short` package run takes several minutes on a loaded Mac.
  Most of it is real `go test` children in the reuse and profile tests,
  which the Linux gate also runs. Watch the gate time.
- Add a Rust adapter or a check for `apps/pressure-ui`.
- Capture `go vet` of `sessionpressure` took 18.7 s of the 30 s capture
  budget on a loaded Mac. Watch it, because a capture over budget gives no
  verdict.

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

### Enrolment deliveries

- 7a31940 (delivery 7f7dc973): policy without the Linux checks, the hook,
  this record and the test sentinel fix. Gates: capture hook and `go vet`
  (Mac), `go test` in `internal/helpermain` (Mac, 49 s) and `swift test`
  (Mac). A policy change runs every check, also the path-scoped ones.
- 5b3daae (delivery 340c7abc): adds the Linux `go-tests` and `go-vet`
  required checks. Gated by the committed policy of 7a31940.
