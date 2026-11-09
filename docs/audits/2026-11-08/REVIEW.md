# Session Pressure implementation review

Owner: WORK-41, Codex session `01a1bbc7-3cf2-72b0-b843-563bca14e3dd`.
The [implementation ledger](IMPLEMENTATION.md) retains the full evidence history.
This report states the review findings and the remaining acceptance conditions.
Private receipts are under `~/.nicos-dev/session-pressure/acceptance/2026-11-08/`.

## Current installed review (2026-11-09 00:33 UTC)

native product
**0.7.0 / a6cee30a** is installed at both managed prefixes. Independent checks
match all ten installed files to the five-binary Darwin CGO release manifest
(`review-a6-installed-manifests-0028.json`). The signed app is **1.4.4 build 12**,
with full-bundle authority
`23704096c15a1a7e35b98360bf9692efd49b25324af666ecdb0c3560736c6ade`.
All **77 Swift tests in 11 suites** pass, and independent strict signature
verification passes. The fresh installed doctor passes with matching CLI/helper
revisions, resident PID 33936, 16.516 MiB RSS, 0.370% normal CPU duty and sample
CPU p95 15.977 ms. Host CPU pressure is separate from resident health.

Factory **a39854ab** remains independently installed at all three front doors,
SHA-256 `7ec3c96ce83772f63c988f844b9d11835898e6da6a92373904deb280853f9a45`.
Its schema-4 receipt does not cover later queued changes. The sole promoter
retains the final combined factory installation, with explicit product stamp
**a6cee30a**, rather than the moving documentation HEAD.

The immutable observer is
`observation-a6cee30-app144b12-20261109T002627Z.json`. Its monitoring clock starts
at **2026-11-09T00:27:22.376059Z**, after warmup. All 300 reads pass with zero
failures: status p95 **235.409/250 ms**, board **242.713/500 ms**, and rich board
**615.370/1,000 ms**. At 00:29:23 there are three successful health checks,
zero failures/restarts and passing continuity. App CPU is **0.977% over 122.858
seconds**, peak RSS **50.781 MiB**, with zero sample failures. This short sample
is not a resource pass. First eligible checks are **November 9 00:57:22 UTC**
(30 minutes), **November 10 00:27:22 UTC** (24 hours), and **November 12 00:27:22
UTC** (72 hours). No elapsed time or resource pass transfers from earlier
artifacts. App 1.4.2 and 1.4.3 CPU failures remain preserved.

The current full audit **fails** (`review-resume-audit-0025.json`, 00:25 UTC).
The telemetry owner repaired the midnight s20/8008 completion only after
proving its primary event, retaining a backup and reconciliation ledger.
A later independent audit found **another** primary-present completion gap:
s55/7943, started **00:24:49.245288 UTC**, primary `legacy.kb` success at
00:24:49.237096 UTC. Read-only proof is
`review-factory-remaining-gap-0026.json`. Neither incident proves primary loss;
the interruption cause remains unknown. The owner is adding conservative,
bounded recovery for dead-process attempts with exact primary proof. Source
review confirms cursor rotation past unprovable attempts, no guessed latency,
no erased gaps, and one terminal count per proven recovery. This is reviewed
source, not installed prevention proof.

The current audit reports factory/product recent writer p95 **100/57.5 ms**,
above the 25 ms target. Product telemetry has 7,708 attempted/written and zero
failed/gap events; factory recent telemetry has 1,471 attempted and 1,470
written. Native diagnostic benchmarks improve writer p95 **22.287 to 16.058
ms** and short-lock handoff **52.523 to 4.202 ms/op**. These bounded cohorts
are separate from the natural two-hour audit window; installed paired
measurements remain the promoter's responsibility.

Three owned factory deliveries are landed; four remain nonterminal at 00:32:
accepted **9de03888-f6d9-452d-ba3f-47903e2b7166**, **b20237d3-8823-4e62-a034-9dd63b815231**,
**196ac71c-82d8-4e09-83b8-0f890c054fbe**, and held
**2aca8f87-72ad-4d63-95cb-58f5a7248b90**. Replacement 9de supersedes paused
parent 64c635e4 and includes the child's source-digest edit. Do not duplicate
or recapture the child. The retained waiter continues. The service is healthy,
busy and configuration-current; exporter queue position is 54 of 59, reason
`repo_running`, zero runs. Accepted publication is not landed source.

Native export review found that receipts selected host files instead of
export-platform files. The correction runs a host digest helper under the
export's GOOS, GOARCH, CGO and build tags. Tests independently mutate host-only,
target-only, CGO and tagged source. The full builder race suite passes on
Hetzner job **20261109T001559Z-2250586** (14 seconds, exit 0), bound to the exact
four owned paths in `review-export-race-0021.json`. The fix is captured in 9de.

Peer commits **d6d1a1c/a6cee30** reduce short-lock handoff delays and automatic
app refresh layout work. Source review preserves durable terminal writes,
private refresh serialization and manual progress. Installed latency and CPU
acceptance must use the new artifact pair. The final real installed Swift/Unix
API contract is queued behind host CPU admission; no bypass or pass is claimed.

WORK-41 remains in progress until writer completion/latency, factory delivery,
app resources, sufficient scheduler evidence, a real administrator-authorized
trace and actual elapsed acceptance are proved. Historical checkpoints below
retain their original identities and times.

## Findings and corrections

| Finding | Correction | Evidence and limit |
| --- | --- | --- |
| The acceptance observer started its elapsed clock before read warmup and did not bind the complete installed artifact pair. | Bind CLI SHA-256, full app bundle authority and the running executable path. Start the monitoring clock after warmup. Preserve artifact changes, failures and restarts. Require all read, health, continuity and app gates before 24/72-hour acceptance. | `b36ac47`; observer race tests pass. The current immutable a6/app1.4.4 observer is active; old observations do not gain the new semantics. |
| The uncommitted Hetzner pane read worker files and ran SSH directly from Swift. | Adopt and complete the pane through typed `remote inspect` and `remote job-log` Go contracts. Swift retains view state only. | CLI reads are bounded by file type, byte count, rows, process lifetime and response size. No new database or policy authority. |
| Equal local/remote booleans could be presented as agreement when the local run failed because of its environment. | Use the worker's recorded `agreement` as the only comparison authority. Keep unknown comparisons unknown. Show environment text as a hint. | Go and Swift tests preserve an unknown agreement despite opposite booleans and an environment failure. |
| A failed refresh could look like fresh empty history. | Retain the last successful data and its original age. Show local, box and job-log failures. Show missing ledgers without inventing a cause. | Store tests cover failed reads and retained ages. |
| Polling could continue when the pane was hidden, minimized or inactive. Late results could update a stopped pane. | Poll only while visible and active. Cancel tasks and invalidate their generation on stop. Coalesce concurrent refreshes. | Lifecycle tests cover coalescing and stopped generations. |
| Every refresh could decode and recompute the same ledger. | Use a SHA-256 signature of the complete bounded JSONL suffix. An unchanged ledger returns a cursor and skips row decoding; configuration and box health still refresh. | Tests cover incomplete appends, complete appends and unchanged history. |
| History and receipt inputs had no complete resource contract. | Reject links, FIFOs and special files; close the open/stat race; read at most 4 MiB and 1,000 complete ledger rows. Bound emitted history and jobs to 512 KiB each, configuration to 256 KiB, SSH output to 1 MiB and job logs to 100,000 bytes. | Tests cover oversized input, response encoding expansion, malformed rows, invalid job IDs, non-finite health and bounded capture. |
| Capabilities omitted existing commands, and some read leaves had incomplete telemetry vocabulary. | Cover dispatcher operations and categorize remote inspection/job logs, I/O health and identity reads without retaining private flag values. | A dispatcher-based test detects future capability omissions. |
| The pane displayed only the first 100 runs and 120 jobs without access to later matching rows. Unknown decisions looked like shadow decisions. | Add bounded incremental display with lazy rows. Preserve unknown decision text. Counts explicitly describe the loaded window. | Source review; final native verification and live review are recorded below when available. |
| Standalone command events omitted the shared outcome class and reason. All 373 nonzero calls projected into one unknown group. | Add the factory's closed result vocabulary. Mark a completed audit finding only after successful report output; preserve loading, encoding and output failures as command failures. Keep the stable operation ID, bounded action and legacy outcome fields. | `921c267`: guarded publication complete. Exact-source targeted race and installed outcome/privacy dogfood pass. Historical events are unchanged. |

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

A bounded read-only primary-stream review at 22:06 UTC reconciles all 373
nonzero product events among 6,238 command events, with zero malformed rows:
303 status exits, 16 audit exits, 13 old I/O unknown leaves, 3 process-stop exits,
3 self-test exits, 2 job-log exits, 3 process-run timeouts, 1 other process-run
exit and 29 usage exits. These are call outcomes, not 373 independently proved
product defects. Historical status rows do not distinguish an unhealthy report
from a report-loading error, so their cause remains unknown. The new audit
marker is set by the report path, never inferred from an exit-1 action alone.
The stable operation ID remains `session.pressure`; the existing categorical
`action` continues to identify the leaf. The factory failure query now confirms
that the source-specific cluster contains 373 unknown outcomes; a default
`cli-invocations` query does not measure this product stream.

At 21:52 UTC, 30 diagnostic installed status calls all succeeded. First-byte
latency was 24.029 ms p95 and full process completion was 47.682 ms p95
(maximum 56.653 ms). This is a 30-call diagnostic of the old installed CLI,
not a replacement for its failed 100-call acceptance cohort or a causal claim
about writer performance. The private receipt binds the installed binary hash.

The native `8c042d24` packaging job passed its full race tests, then failed on
malformed `GOFLAGS` quoting (`unknown flag -linkmode`). The whole flag token
must be quoted, including `-ldflags=`. Correct parsing was checked on the Mac
and runner without compilation. Peer `c8afd904` corrects the release guidance;
its clean named-ref bundle and parser preflight passed. The promoter retains
one canonical native build for that cohort. The later telemetry commit is a
separate follow-up and must not be described as part of the sealed c8 build.

## Acceptance still required

Historical source and candidate verification at 21:51 UTC (installation superseded by the current checkpoint above):

- `b36ac47`: observer race tests pass (1.143 seconds).
- `a20e450a`: final API snapshot matches all nine committed owned paths.
  Targeted API/CLI/observer race tests pass (1.266/2.478/1.142 seconds), canonical
  job `20261108T212723Z-1593804`. The later full race snapshot also includes
  stable row IDs and the final I/O/identity telemetry leaves. The full
  `go test -race ./...` suite passes: canonical job
  `20261108T213140Z-1666344`, exit 0 in 78 seconds at 21:50:39 UTC.
  `review-go-full-race-receipt.json` binds the log and source manifest hashes.
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
| WORK-41 | Completed API/app review, native installation and installed outcome dogfood | Full Go race passes; all 77 Swift tests for app 1.4.4 pass. The earlier twelve-pane review and outcome checks retain their identities; current a6 manifests and doctor pass. Current writer, app resource and elapsed gates remain open. |
| Runtime/factory promoter | Activate the reviewed native CGO profile and fail-closed wrapper routing, then include the new read API | Factory a39854ab receipt and all three installed hashes match. Later queued helper/browser/export changes still require their own landed and installed proof. Portable fixture binaries are not release candidates. |
| Delivery owner | Land accepted replacement 9de, resolve its included held child, and land the two other accepted factory deliveries | Exact delivery IDs are landed, installed source agrees, owned doctor is clean and the service remains healthy. Do not stop a busy worker. |
| Factory telemetry owner | Resolve s55/7943 from primary proof, publish bounded completion recovery, and prove installed writer latency | Preserve historical gaps; prove newly completed writes and fresh bounded latency windows. Missing history cannot be repaired by resetting evidence. |
| WORK-41 | Final same-artifact 30-minute, 24-hour and 72-hour acceptance | One immutable observer helper and one new cohort; zero sample errors, restarts and health failures; continuity and all three 100-sample read budgets pass. Actual elapsed time is required. |
| Scheduler owner | Calibrate from sufficient terminal evidence | Replay promotion meets the existing gap-closure gate. Keep admission weights unchanged until then. |
| Operator and WORK-41 | Exercise administrator-approved privileged trace when authorization is available | Real helper authorization, bounded completion and independently measured trace duration. A signed launch is not this proof. |

## Closeout evidence (22:39 UTC)

The full telemetry baseline comparison records 2,550 additional product events,
with mixed-command p95 299.413 → 270.861 ms (9.5% lower). This is directional
usage evidence across changing artifacts and workloads, not causal proof or a
replacement for the artifact-bound read gates. No source is lost. The global
diff is not wholly green: the separate delivery-queue-3d source crosses its
20% latency threshold (14.874 → 17.934 ms). That independent application is
outside this product review. The private full diff is
`review-telemetry-diff-2238.json`.

The finish gate refuses completion because parent `64c635e4` is still paused.
`review-closeout-refusal-2239.json` preserves that result and the verified exact
factory paths. The existing recovery is queued; do not duplicate it. WORK-41
remains in progress. The final outcome and timed re-check instructions are in
Scratchpad Agents note `01M6YN14GE6XF01K51JDR6SPMA`, read back successfully.

## Self-review

The export regression test found a receipt-authority defect that ordinary
Linux-only tests could miss. Build provenance must select the target source
closure even when the helper runs on another host. Target-file mutation tests
now check that boundary. The review also caused avoidable invalid command
calls by guessing positional arguments, managed prefixes, telemetry field paths
and time formats. The guarded helper must resolve its installed symlink to the
canonical repository path before using runtime-relative shared helpers. Read command help
and resolve paths before invoking diagnostics; retain those failures in the
telemetry rather than deleting them.


The final gate review also found that root Make targets and CI used fixed
package lists. Those lists omitted the release observer, compiler adapter,
product telemetry and gate-runner packages. `make vet` and `make test` now
cover `./...`; `make race` runs the same full module with the race detector.
Both CI operating systems use these shared targets. The local `publish-ready`
aggregate also requires the full race target. README build output and telemetry
ownership now match the source. `make build` also builds
the release installer/observer and compiler adapter. A dry run verifies the
expanded commands; the earlier full-module Linux race receipt supplies the
source test evidence. No new local heavy Go build was run for this gate-only
change, and a GitHub CI execution is not claimed.

The review improved artifact identity, evidence semantics, bounded reads,
lifecycle cancellation and agent-facing capability coverage. It also found two
process weaknesses: repeated oversized board subjects and audit flag misuse.
Use the source/CLI usage contract before a diagnostic call, keep board subjects
under 240 characters, retain one remote wait handle and distinguish packaging
failures from product test failures. Resolve paths with `rg --files` before
reading them or putting them into a handoff; guessed paths caused avoidable
failed reads during this review. No test or historical metric is rewritten
to improve the score. WORK-41 remains in progress until its acceptance conditions
are proved.
