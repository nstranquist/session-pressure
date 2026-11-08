# Session Pressure implementation review

Owner: WORK-41, Codex session `01a1bbc7-3cf2-72b0-b843-563bca14e3dd`.
The [implementation ledger](IMPLEMENTATION.md) retains the full evidence history.
This report states the review findings and the remaining acceptance conditions.
Private receipts are under `~/.nicos-dev/session-pressure/acceptance/2026-11-08/`.

## Findings and corrections

| Finding | Correction | Evidence and limit |
| --- | --- | --- |
| The acceptance observer started its elapsed clock before read warmup and did not bind the complete installed artifact pair. | Bind CLI SHA-256, full app bundle authority and the running executable path. Start the monitoring clock after warmup. Preserve artifact changes, failures and restarts. Require all read, health, continuity and app gates before 24/72-hour acceptance. | `b36ac47`; observer race tests pass. A new installed observer generation is required; old observations do not gain the new semantics. |
| The uncommitted Hetzner pane read worker files and ran SSH directly from Swift. | Adopt and complete the pane through typed `remote inspect` and `remote job-log` Go contracts. Swift retains view state only. | CLI reads are bounded by file type, byte count, rows, process lifetime and response size. No new database or policy authority. |
| Equal local/remote booleans could be presented as agreement when the local run failed because of its environment. | Use the worker's recorded `agreement` as the only comparison authority. Keep unknown comparisons unknown. Show environment text as a hint. | Go and Swift tests preserve an unknown agreement despite opposite booleans and an environment failure. |
| A failed refresh could look like fresh empty history. | Retain the last successful data and its original age. Show local, box and job-log failures. Show missing ledgers without inventing a cause. | Store tests cover failed reads and retained ages. |
| Polling could continue when the pane was hidden, minimized or inactive. Late results could update a stopped pane. | Poll only while visible and active. Cancel tasks and invalidate their generation on stop. Coalesce concurrent refreshes. | Lifecycle tests cover coalescing and stopped generations. |
| Every refresh could decode and recompute the same ledger. | Use a SHA-256 signature of the complete bounded JSONL suffix. An unchanged ledger returns a cursor and skips row decoding; configuration and box health still refresh. | Tests cover incomplete appends, complete appends and unchanged history. |
| History and receipt inputs had no complete resource contract. | Reject links, FIFOs and special files; close the open/stat race; read at most 4 MiB and 1,000 complete ledger rows. Bound emitted history and jobs to 512 KiB each, configuration to 256 KiB, SSH output to 1 MiB and job logs to 100,000 bytes. | Tests cover oversized input, response encoding expansion, malformed rows, invalid job IDs, non-finite health and bounded capture. |
| Capabilities omitted existing commands, and some read leaves had incomplete telemetry vocabulary. | Cover dispatcher operations and categorize remote inspection/job logs, I/O health and identity reads without retaining private flag values. | A dispatcher-based test detects future capability omissions. |
| The pane displayed only the first 100 runs and 120 jobs without access to later matching rows. Unknown decisions looked like shadow decisions. | Add bounded incremental display with lazy rows. Preserve unknown decision text. Counts explicitly describe the loaded window. | Source review; final native verification and live review are recorded below when available. |

The Hetzner UI originated in a concurrent, uncommitted six-path change. Its owner
was inactive after board coordination. WORK-41 explicitly adopted integration at
21:15 UTC (`d-06grq0vph0tohhk3pc`). The original contribution and other native CGO
routing changes were preserved. Separate owners retain runtime/factory promotion
and delivery-worker installation authority.

## Runtime and telemetry findings

Real CLI capture at 21:38:55 returned 93 valid delegate records, 668 bounded job
receipts and live box health in 2.270 seconds (514,202 JSON bytes). Local-only
inspection took 0.268 seconds (95,028 bytes); neither read reported an error or
malformed ledger rows. The captured output supplies the scrubbed Swift fixture.
A completed job's captured log returned in 1.726 seconds. The initial integration
test incorrectly assumed the newest receipt always had a log: a concurrent
`SETUP` failure has a receipt before log creation. The UI correctly showed the
read failure. The test now binds its log read to an explicitly selected completed
receipt, and preserves the original failed verification log.

At 21:36 UTC, the installed 1.4.1 build 9 app had 2,176 seconds of valid observed
time, CPU 0.3005%, peak RSS 114.219 MiB and no sample failures. Its 30-minute app
resource gate passed. Resident PID 90605 had no restarts, 37 successful checks and
passing continuity. These observations belong to CLI `0535927f` and full app
bundle authority `c827a2d6`; they cannot carry forward to a replacement pair.

That generation's 100-sample status read p95 was 266.788 ms against 250 ms and
failed. Board p95 187.389 ms against 500 ms and rich read p95 302.615 ms against
1,000 ms passed. All 300 reads succeeded. The loaded-host failure is retained
alongside the earlier passing cohort. Neither cohort establishes the other's
performance. A final promoted cohort needs its own read gate.

The 21:10 live audit passed policy, resident, readings, cleanup and artifact
checks. It failed factory writer completeness and warned about writer latency
and scheduler calibration. Product telemetry retained 5,820/5,820 successful
writes with no gaps. The factory's recent two-hour window contained 117 gaps;
the current activation hour still contained one persistent missing write.

Read-only inspection at 21:12 located the current gap in
`.writer-health-v4-s26.sqlite`: sequence 8006 began at
`2026-11-08T20:53:22.472613Z` and had no primary JSONL receipt. Subsequent sequences
8007 and 8008 completed. This is not a transient in-flight attempt. The pending
schema does not identify its process or operation, so the exact origin remains
unknown. Do not delete the pending row, reset counters or infer a successful
write. The factory owner was notified with the exact receipt boundary.

Product recent writer p95 was 66.057 ms and factory recent writer p95 was 50 ms;
both exceed the 25 ms target. The previous paired writer improvement remains
directional evidence, separate from these current windows. Scheduler replay
and low terminal sample counts still do not justify increasing capacity.

## Acceptance still required

Source and candidate verification at 21:44 UTC:

- `b36ac47`: observer race tests pass (1.143 seconds).
- `a20e450a`: final API snapshot matches all nine committed owned paths.
  Targeted API/CLI/observer race tests pass (1.266/2.478/1.142 seconds), canonical
  job `20261108T212723Z-1593804`. The later full race snapshot also includes
  stable row IDs and the final I/O/identity telemetry leaves; its receipt is
  still pending.
- `36f2a69e`: all 75 Swift tests in 11 suites pass, with real CLI/SSH/log
  integration enabled. Developer ID release build and strict signature
  verification pass (`review-desktop-final-verify-2.log`). Installed full-bundle
  authority is still `c827a2d6`, version 1.4.1 build 9; the verified 1.4.2 build
  10 candidate waits for its matching CLI API.
- Peer `3132b75` adds monitor help before resident access. It is a later source
  change and is not covered by the earlier full-race candidate.

At 21:45, installed `doctor` reports ready admission-only protection, no warning,
fresh resident PID 90605, matching CLI/helper `0535927f`, 19.438 MiB RSS, 0.324%
normal CPU duty, sample CPU p95 15.279 ms and projected telemetry 549,408 bytes/day.
That passing operational check is separate from the audit, which still reports
117 recent factory gaps and writer p95 50 ms (factory) / 71.516 ms (product).
The delivery service is healthy and busy, with three owned deliveries landed,
two accepted, one held and one paused. No queued work is described as installed.

| Owner | Required result | Completion proof |
| --- | --- | --- |
| WORK-41 | Final bounded API and app verification, guarded publication and installation | Full exact-source Go race suite, real captured CLI contract, all Swift tests, signed release, app full-bundle compare-and-swap, installed endpoint and UI checks. |
| Runtime/factory promoter | Activate the reviewed native CGO profile and fail-closed wrapper routing, then include the new read API | Clean committed source, pinned Darwin toolchain, matching installed CLI/factory receipts and live artifact identity. Portable fixture binaries are not release candidates. |
| Delivery owner | Finish the paused builder parent, held child and two accepted factory deliveries | Exact delivery IDs are landed, installed source agrees, owned doctor is clean and the service remains healthy. Do not stop a busy worker. |
| Factory telemetry owner | Diagnose the persistent missing receipt and writer latency | Preserve historical gaps; prove newly completed writes and fresh bounded latency windows. Missing history cannot be repaired by resetting evidence. |
| WORK-41 | Final same-artifact 30-minute, 24-hour and 72-hour acceptance | One immutable observer helper and one new cohort; zero sample errors, restarts and health failures; continuity and all three 100-sample read budgets pass. Actual elapsed time is required. |
| Scheduler owner | Calibrate from sufficient terminal evidence | Replay promotion meets the existing gap-closure gate. Keep admission weights unchanged until then. |
| Operator and WORK-41 | Exercise administrator-approved privileged trace when authorization is available | Real helper authorization, bounded completion and independently measured trace duration. A signed launch is not this proof. |

## Self-review

The review improved artifact identity, evidence semantics, bounded reads,
lifecycle cancellation and agent-facing capability coverage. It also found two
process weaknesses: repeated oversized board subjects and audit flag misuse.
Use the source/CLI usage contract before a diagnostic call, keep board subjects
under 240 characters, retain one remote wait handle and distinguish packaging
failures from product test failures. No test or historical metric is rewritten
to improve the score. WORK-41 remains in progress until its acceptance conditions
are proved.
