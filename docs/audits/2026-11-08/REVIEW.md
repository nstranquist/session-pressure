# Session Pressure implementation review

Owner: WORK-41, Codex session `01a1bbc7-3cf2-72b0-b843-563bca14e3dd`.
The [implementation ledger](IMPLEMENTATION.md) retains the full evidence history.
This report states the review findings and the remaining acceptance conditions.
Private receipts are under `~/.nicos-dev/session-pressure/acceptance/2026-11-08/`.


Current checkpoint (**2026-11-09 01:39 UTC**, promoter session `01a1bc3b`):
app **1.4.11 build 19**, committed source **d463ed8** including asynchronous
authorization repair **97d703f**, is installed and running. All **94 tests in
13 suites**, the release build, matching real Team ID **C82D23RNQ9**, and
strict Apple trust-anchor checks pass. Full-bundle authority is
`d577902d147b4d95d0d98b0a90c91ffccbac9b6c46e5004cd14a72be255be59b`;
the atomic receipt is `app1411-install.json`. The trace owner independently
verified that the UI stays responsive during administrator authorization.
Actual trace completion on this build is pending system authentication. The
approved 1.4.9 trace succeeded; 1.4.10 exposed the synchronous authorization
UI freeze now repaired. The stopped 1.4.8 observer remains diagnostic only.
**No final 30-minute resource or 72-hour acceptance pass is claimed.** A new
immutable idle cohort must follow the final live trace and parked-app check.
Earlier observer deadlines below do not apply to the current build.

The live product doctor at **01:28 UTC** passes: matching `a6cee30a` CLI/helper,
fresh resident PID 33936, 18.031 MiB RSS, 0.319% CPU duty, sample CPU p95
16.079 ms, normal host pressure, empty work queue, and no warnings. The natural
two-hour audit (`review-audit-2h-0130.json`, generated 01:28) still **fails**:
CLI writer histogram p95 is 100 ms against 25 ms and standalone writer p95 is
106.077 ms against its 100 ms failure ceiling. Telemetry now passes. Scheduler
status is warning because some classes lack 20 terminal-runtime samples and
two long waits remain; a window losing samples is not proof of improvement.
Storage is warning at 48.7 GiB available. Earlier loaded writes remain in the
latency window. No threshold is widened and no history is deleted.

Factory browser and automatic completion-recovery repairs are **installed at
589996f**, with signed SHA
`0c8f80d28b95d7773d870fac8436bcfd9065c830f0e6f55c0f621d24aba898e4`
verified at all three CLI paths. Native SQLite canaries pass before and after
promotion; product/helper source agreement passes. The fresh installed private
100-call cohort has 100 written, zero failures, writer p95 histogram upper
bound **25 ms**, maximum 27.783 ms, and wall p95 105.531 ms. The separate product
100-call cohort has writer p95 15.608 ms. These private cohorts do not replace
the failing natural production window.

The explicit reconciliation helper delivery **a48c48af** has **landed at
effa7b79**; its installed artifact and native positive/negative tests were
already verified. The independently tested cross-process proof-scan limit
**201ed628** remains accepted, not landed or installed. Full telemetry race,
vet, and native recovery tests pass on its exact integrated bytes. The final
worker source **50d238e** is landed, but its proposed CGO-disabled artifact was
withheld after review found missing SQLite support. The existing healthy
2.34.1 worker remains active. The sole replacement installer is `01a1bdaa`;
CGO-enabled artifact, real writer rows, activation, and normal runtime receipts
remain required. Factory queue state does not block direct app installation.

Installed follow-up (**2026-11-09 01:04 UTC**, promoter session `01a1bc3b`):
app **1.4.8 build 16**, committed source **c966f6e**, is installed and relaunched.
All **89 tests in 13 suites**, the release build, matching real Team ID
**C82D23RNQ9**, and strict Apple trust-anchor checks pass. Full-bundle authority
is `0eaf84270885d5d4142b8103b61f85ddcb1141d0355aa1dabf59612938df0a37`.
The atomic install receipt is `app148-install.json`. The final immutable observer
is `observation-a6cee30-app148b16-20261109T010323Z.json`; its monitoring clock
starts at **01:03:58.385513 UTC**, after warmup. The first eligible resource check
is **November 9 01:33:58 UTC**; the 72-hour check is **November 12 01:03:58 UTC**.
Neither is passed in advance. Actual privileged trace completion on 1.4.8
remains unverified. Earlier trace and resource failures remain preserved.

Product doctor at 01:03 passes: matching `a6cee30a` CLI/helper, fresh resident
PID 33936, 17.797 MiB RSS, 0.359% CPU duty, sample CPU p95 17.270 ms, no warning.
Browser admission is Auto/recovered after two safe samples, with a 120-second
job limit. Factory adapter/recovery installation and delivery-worker activation
are separate follow-through; they do not block this installed product cohort.

Delivery scope correction (**2026-11-09 01:01 UTC**): the four queued
deliveries and delivery-worker upgrade belong to **nicos-tools**, not this
Session Pressure repository. Session Pressure changes use guarded direct
commits and direct product installation. Factory integration is separate
follow-through; its queue must not block the app rebuild, installation, real
trace retry, or product runtime verification. Earlier statements that combine
factory queue completion with Session Pressure readiness are superseded by this
scope correction. Product acceptance still requires its own installed evidence
and actual elapsed resource checks.

Earlier closeout (**2026-11-09 00:58 UTC**): the operator requested that
queue and elapsed-test waits stop. The active producers remain running; no
queue, worker, approval or test gate is bypassed. The approved real trace found
and caused repairs for registration, signing, trace error handling, authorization
cleanup, background callback isolation and stale helper registration. It has
**not yet completed successfully**.

Installed app **1.4.7 build 15** now independently passes strict app/helper
signature checks with the real Developer ID authority and matching Team ID
**C82D23RNQ9** (`review-app147-signing-0054.json`). Its real trace crashed on
its 25-second deadline: crash `C66B12E4` identifies a MainActor assertion on a
background dispatch queue (`review-trace-crash-0054.txt`). launchd also retained
the old build-14 helper registration and reported `EX_CONFIG`; the private
launch log records the failed executable lookup. Source **a9f9905** fixes both:
explicitly Sendable callbacks and a nonisolated timeout factory, plus supported
asynchronous unregister/re-register once per updated app build on an explicit
trace. Successful registration alone is cached. An unapproved service remains
subject to user approval. **11 targeted tests pass**, including actual background
deadline execution and competing callbacks, in
`review-trace-callback-swift-0058.log`. The sole promoter owns combined validation,
version **1.4.8**, installation and the final immutable observer. No trace result
or resource acceptance is inferred from these source tests.

Earlier trace review (**2026-11-09 00:52 UTC**): the installed app **1.4.6
build 14** and embedded helper have **ad hoc signatures and no Team ID**
(`review-app146-signing-0052.json`). Earlier strict signature checks establish
bundle integrity only; references below to a signed or Developer ID package do
not establish the installed Team ID. The helper correctly rejects clients
without a matching real Team ID. Source `2d14f66` now selects an installed
Developer ID identity and requires matching nonempty app/helper Team IDs after
the recursive build in both installation targets. The sole promoter is
preparing **1.4.7 build 15** with the available Developer ID certificate.
No privileged trace success is claimed yet.

The operator approved the NDev Pressure background item in macOS System
Settings; its switch is visibly on. The preceding real attempt failed with
`SMAppServiceErrorDomain:1`, preserved in the private system log
`review-trace-system-log-0047.json`. The app fixes registration after a framework
lookup failure (`8e72d11`), rejects nonzero trace exits even with diagnostic
output (`4091c6b`), and frees authorization references after denied requests
(`141d6a5`). Six registration tests with eight cases pass; the final changed
client compiles and passes its targeted tests in
`review-trace-final-swift-0044.log`. Full combined Swift evidence is 84 tests in
12 suites. Actual helper authorization and bounded trace completion remain open.

The 1.4.4/1.4.5 observers are stopped and preserved. Their elapsed time cannot
transfer to the final app; no final immutable idle cohort is active while the
operator trace is in progress. A fresh installed product writer cohort reports
**100/100 writes, p95 15.608 ms**, below the 25 ms target. This bounded cohort
does not replace the natural two-hour audit warning or historical 120 gaps.

## Earlier installed review (2026-11-09 00:33 UTC)

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

A later independent audit at **00:33:52 UTC** improves to **WARN / ok true**
(`review-post-s55-repair-audit-0034.json`) after the telemetry owner repairs
s55/7943 from exact primary proof. Recent factory writes are **1,639/1,639**,
with zero failed writes or gaps; product writes are **8,076/8,076**, also with
zero failures or gaps. The **120 historical factory gaps** remain visible.
This repair does not establish installation or effectiveness of the queued
recovery source. Factory/product recent writer p95 is **100/59.035 ms**, so
performance acceptance still fails. At 00:33:29 the new app sample is **1.367%
CPU over 368.713 seconds**, with seven successful health checks, zero failures
and peak RSS 55.266 MiB. This is over target but not yet a 30-minute verdict.
The early lower CPU sample is retained above, rather than substituted for it.

At **00:40 UTC**, the installed a6 CLI and its Unix API passed the real Swift
integration test (`review-a6-installed-swift-contract-0032.log`, one test,
1.970 seconds). The earlier admission wait is retained in that log.

The operator explicitly approved a real privileged trace at 00:35–00:36.
The app stopped before authorization and incorrectly reported a missing
helper. Both embedded files and the strict app signature were present.
`SMAppService.notFound` was being treated as proof of missing bundle files.
Commit **8e72d11** checks the files separately, attempts normal registration
once, preserves framework error domain/code, and still requires macOS
approval. Six registration tests (eight argument cases) pass. Full combined
app validation reports **84 tests in 12 suites**. Review also found that a
nonzero tracer exit with diagnostic output could become an empty success;
**4091c6b** makes every nonzero exit fail. The signed combined release and
real authorization/trace proof are still pending at this checkpoint.
The interactive UI interval starts at 00:35, so the existing observation
must not be described as uninterrupted minimized-app idle evidence.

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
acceptance must use the new artifact pair. The installed Swift/Unix API contract passes as recorded above; no admission
bypass was used.

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
