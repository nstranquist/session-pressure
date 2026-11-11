# Scheduler replay evidence

Owner: codex / 01a1c82a-4cc8-7950-ad12-380eb2d1ecce. Status: in progress, 2026-11-11.
Work: WORK-3. No live admission policy change is included.

## Installed baseline

The installed `ndev session pressure work evaluate --json` returned 22/22
passing deterministic scenarios, with promotion not ready. Its retained
24-hour scheduling replay included 2,617 operations: test p95 queue wait fell
from FIFO 131,250 ms to candidate 89,499 ms, while build p95 rose from
218,346 to 240,644 ms and browser p95 from 71,214 to 101,186 ms.
Both had zero capacity and protected-bypass violations. Short jobs with long
waits fell from 169 to 124. These are modelled policy comparisons on the same
retained arrivals and runtimes, not measured before/after installation effects.
Host-pressure delays are excluded by the replay. Historical versions remain
mixed; the report does not establish cancellation or dead-owner correctness.

Evidence: private session receipt `work3-installed-evaluation.json`, recorded
2026-11-11 05:01 UTC. The seven-day lifecycle classification is retained in
nicos-tools `corpus/research/2026-11-11-pressure-retained-cohorts.json`.

## Decision 1 — expose the required evidence window

The evaluator fixed its input window at 24 hours, which cannot supply the
seven-day replay requested by WORK-3. Add `work evaluate --since DURATION`;
the default remains 24h, and 168h requests seven days. Report the requested
window and start time, plus skipped lifecycle diagnostics from the event store.
Retain promotion refusal and every existing safety/performance comparison.
The regression checks place real lifecycle pairs one and 48 hours back and
verify that only the longer window includes both; invalid windows are refused.

## Open acceptance

- [x] Runner-3 command-package race checks pass for the exact final source.
- [ ] Delivery lands; a clean installed CLI supports the option.
- [ ] Run the installed seven-day replay and investigate class regressions.
- [x] Demonstrate installed release wakeups, cancellation and dead-owner recovery.
- [ ] Use seven-day evidence to accept, revise or reject reserve/burst policy.

## Installed seven-day replay and cancellation repair

2026-11-11: committed 714347b2046c was built as a clean native bundle on
runner 3 and promoted through the expected-current release contract. The
installed CLI and helper matched that revision; the factory front door still
had an older embedded product stamp and requires its separate clean build.
The resident reported healthy after promotion. No live policy changed.

The installed 168h replay retained 7,446 operations and passed 22 deterministic
scenarios. Candidate queue p95 versus FIFO: test 91,986 to 56,231 ms; build
218,346 to 233,909 ms; browser 82,672 to 401 ms. Short jobs with long waits
fell from 300 to 218. Capacity and protected-bypass violations were zero.
Build wait worsened by 7.13%; passing promotion diagnostics does not erase
that tradeoff. This counterfactual uses retained arrivals/runtimes, excludes
host pressure, and mixes historical versions. It is not an elapsed shadow
rollout or sufficient evidence to enable reserve/burst.

The installed private-state lifecycle fixture passed release/wakeup, then
failed explicit cancellation twice. The owner had handled SIGTERM and
released its lease before CancelWork called DropLease. Cancellation had
succeeded, but the command returned the missing-lease error. Preserve initial
live inspection and all other errors; only a missing lease after signaling
is reconciled with current coordinator status. Unknown operations still fail.
No automatic cancellation was added.

The regression uses a separate process that releases its lease on SIGTERM and
exits successfully. Three race-detector repetitions passed on runner 3, job
f09740f6-cc4c-4ed7-bf4a-3f742038b0bc (10.873s). Full sessionpressure and
sessionpressurecmd race suites passed job 65b28df1-2648-45a9-8493-f7b7d25eb810
(98.693s and 3.114s). The installed lifecycle script retains exact helper
provenance, unchanged policy, and private state. It checks observed accounting
through release, owned cancellation, persisted dead-owner recovery, and
concurrent release. Its final installed rerun remains pending the repaired
native bundle; historical failed receipts are retained.

Private receipts: work3-installed-7d-evaluation.json,
work3-lifecycle-acceptance-1/, work3-lifecycle-acceptance-2/,
work3-cancel-race.log and work3-cancel-full-race.log under this session's
session-workspaces directory.

## Session journal

- 2026-11-11: Installed baseline exposed a performance tradeoff; WORK-3 stays
  open. The first remote test archive was empty after local Python rejected
  an unsupported extraction argument; the remote command failed before tests
  because go.mod was absent. Recreated the sealed archive from committed
  cee893a0ac5cabf67ce6b9cd4d057b2a1c927b76 plus the two owned command files
  and reran on the same runner 3. This is a setup failure, not a source result.

- 2026-11-11: The first populated test run failed because the test fixture's
  completed event omitted its required lease ID. Corrected the fixture; the
  focused race run passed (job 51929cc1-9555-4a9c-9588-2bd3f76f2800, 1.348s).
  The full package then passed (da1cbbb1-799a-49f2-8aae-08380aa4382b, 3.331s).
  Added skipped-lifecycle diagnostics to text output as well as JSON, then
  repeated the full race suite for the exact final source on runner 3:
  c63f6aed-e1c0-46ba-a434-fdad80e4728a, exit 0, 3.177s. No live policy was changed.

## Installed cancellation repair acceptance

2026-11-11: clean native revision 1596f7a80d01ffe06bd4c11026d772e0b745e250
was built on runner 3 (job 191bb1b0-2b6f-4593-bc5b-6ae70c4e99e3) and
promoted with expected-current 714347b2046cfdf05a304fbb0e4a07c0d8ab0406.
The resident reported healthy. Installed helper SHA-256:
a2578ee64c3928f2d66be3a0f8c2fea2dd97e8e83cccb1e4648e714810705a69.

The installed CLI/helper private-state fixture passed all four cases: release
woke its follower in 65 ms; explicit cancellation recovered capacity; restoring
a persisted lease for a dead fixture owner was pruned; four concurrent test
owners released capacity and woke the heavy follower in 71 ms. Every sampled
accounting check stayed within capacity, final usage and queue were zero, and
the copied live policy remained byte-identical. These are fixture observations,
not production latency guarantees or an observed operating-system crash.
Receipt: work3-lifecycle-acceptance-4/result.json in this session workspace.

Correction: acceptance-3 incorrectly used four browser jobs to fill capacity.
Browser jobs also require a serial execution lock; three correctly refused.
The final fixture uses test jobs for weighted concurrency. Earlier cancellation
failures remain retained. Two native setup attempts failed because bundle verify
ran outside a Git repository; cloning before verifying fixed setup, and neither
failed attempt reached product compilation.

The factory front door remains on its older embedded Pressure stamp. Its clean
runner-3 build passed SDK identity validation but failed readonly module
validation (go.mod requires updates). This shared producer failure is under
repair. WORK-3 remains open for front-door alignment, replay tradeoff analysis,
and the real seven-day shadow decision; no reserve/burst policy was enabled.

## Aligned installed bundle acceptance (2026-11-11 06:41 UTC)

Runner-3 job `0e078b99-c4be-4bbc-9678-a7a9a278ddf7` built the clean
Darwin/arm64 CGO release from `84b378cdbe3009fa88a10288d9a3b0e856d2c326`.
Canonical promotion fenced the prior installed revision `1596f7a80d01`.
Resident PID 98048 reported healthy; the CLI, front door and helper agree,
and doctor reports `source_revisions.mismatch=false`. Helper SHA-256 is
`e1d91ccc8dd810913a6ea4f975a82eb6149eb72e8737600e8d452adcb18a8968`.
The installed lifecycle rerun passed all four cases: release wakeup 70 ms,
owned cancellation, persisted dead-owner recovery, and four-owner concurrent
release 101 ms. Final private usage was zero; live policy was unchanged.
Receipts: this session's `work3-aligned-lifecycle-acceptance/result.json`,
`work3-aligned-native-release/release.json`, and
`work3-aligned-installed-doctor.json`.

The first resident sample is insufficient to establish a new daily-driver
baseline. The 24-hour swap-derate observation remains 53 minutes/day; its
30-minute target is diagnostic, not a reason to weaken admission blindly.
WORK-3 remains open for the real seven-day shadow, reserve/burst decision,
and the documented build-latency tradeoff. Counterfactual replay does not
establish that the shadow window elapsed.
