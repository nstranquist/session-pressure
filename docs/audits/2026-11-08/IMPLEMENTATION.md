# Session Pressure 0.7.0 implementation and acceptance ledger

Owner: Codex, session `01a1bbc7-3cf2-72b0-b843-563bca14e3dd`.
Central work item: WORK-41 (`e6011b2df33955484962f546b6767e02`).
Baseline source: `c136a8f415595b6e9b8f4ee01c85315aef75de80`.


Current review checkpoint (22:07 UTC): the review corrections are committed:
artifact-bound acceptance observer `b36ac47`, bounded gate-runner API
`a20e450a`, and native app 1.4.2 build 10 `36f2a69e`. All 75 Swift tests pass,
including actual candidate CLI, SSH health, a completed job log and installed
CLI/Unix API contracts. The signed release build passes strict verification.
The exact final Go race suite passes on the canonical runner: job
`20261108T213140Z-1666344`, exit 0 in 78 seconds, completed 21:50:39 UTC.
Its base-plus-overlay manifest binds all nine final API paths. The later peer
monitor-help commit has separate verification. These source and candidate
results are not installation receipts. The app awaits the matching
native API promotion; installed authority remains `0535927f` / app `c827a2d6`.
The later telemetry correction `921c267` adds typed standalone outcomes and
report-based audit findings; its exact targeted race test is queued separately.
Peer `c8afd904` fixes release flag quoting and has one retained native build.
Neither new cohort is installed yet.

The existing app passed its separate 30-minute resource gate at 0.30% CPU and
114.219 MiB peak RSS. Its status read p95 still fails at 266.788/250 ms. Four
factory deliveries remain nonterminal (one paused, one held, two accepted), and
factory writer completeness and latency remain open. The detailed review,
ownership and acceptance conditions are in [REVIEW.md](REVIEW.md). Historical
checkpoints below retain their own times and artifact identities.

Historical checkpoint (20:59 UTC): product release `0535927f` is installed at
both CLI prefixes. Its exact committed full race suite and Darwin native build
passed on Hetzner (job `20261108T195328Z-205269`, 88 seconds). The canonical
resident is PID 90605. Its 20:21 monitor-scope self-test passed: normal CPU
0.387% against 0.5%, RSS 16.672 MiB against 30 MiB, sample CPU p95 16.970 ms
against 150 ms, and projected telemetry 528,384 bytes/day against 1 MiB/day.
These are that verified resource cohort, separate from earlier failures; the
current observer independently checks live health and continuity.

The signed canonical desktop is now 1.4.1 build 9, with all twelve panes from the
reviewed combined snapshot. Day Gate's bounded advisor contract and UI are
committed in `37383cd` and `28fa1ce`; the concurrent Hetzner source remains
separately owned and is preserved with file-hash provenance. All 68 exact-owned
Swift tests pass; the combined snapshot also passes its full verification,
real installed CLI/Unix-API integration and strict Developer ID signature check.
The live UI confirms Day Gate's correct successful-read age. Both stale-intent
and real downgrade attempts fail without changing the installed bundle.

Three factory deliveries are landed, including guarded index isolation
`598a33c1` and legacy app installer forwarding `ae8f228a`. Two remain accepted;
parent `64c635e4` is paused by a remote compiler-profile refusal, and child
`2aca8f87` is held behind it. The worker is healthy and busy. All three factory front-door targets independently hash to
`fe8275d0e428282baadc6d27cc93650cd312f3bf64dbe0355dbf1c641cc6c9c4`,
with clean factory revision `483b1def`, native CGO/external linking and product
stamp `0535927f`. Live artifact identity agrees. The publisher reports a passing
private SQLite native canary. Its schema-v4 receipt at
`nicos-tools/nicos-dev/bin/ndev-install-receipt.json` binds that hash to clean
factory source digest `4d70882e53036a002a676d6d8ab6d3e9ff4061a16cde95e4c039934a28354e8a`,
published at 20:19:30 UTC; a private copy is retained. Do not stop the busy worker.

The earlier observation records are preserved, including the 1.4.0 app's
2.566% CPU over 482 seconds. A recorded process sample led to a 15-second
age-label cadence fix (`a9b3480`, clock correction `22595af`). The final signed
1.4.1 build 9 passes all 68 tests, the real CLI/Unix API integration, a clean
release build, strict signing and live Day Gate/freshness review. Its bundle
authority is `c827a2d6`; installation used full-bundle compare-and-swap.

One immutable diagnostic observer began a new app/CLI generation at
20:59:19 UTC. Its private output is
`observation-0535927f-app141b9-20261108T205919Z.json`. No elapsed window carries
forward. The 30-minute, 24-hour and 72-hour gates remain pending. The first
status warm p95 is 266.788 ms/250 under the current loaded host; preserve that
failed read budget alongside the earlier passing 53 ms cohort. This run does
not establish factory alignment and will need a new generation after the
separate native-profile owner completes the forward runtime promotion.

At 20:58 factory status still reports three landed, two accepted, one held and
one paused. Parent recovery was queued once at 20:38; the owned 15-minute wait
timed out at 20:56. The worker remains healthy, busy and configuration-current;
do not resubmit, duplicate the child, or stop it. Writer latency, scheduler
cohorts, peer desktop source publication, privileged trace with normal human
macOS authorization, queued factory work and elapsed acceptance remain open.
WORK-41 stays in progress.

## Product contract

The SwiftUI app is the primary desktop interface. The GPUI app is a tested preview.
Admission protection is the default operating mode. Automatic agent termination stays off.
Storage admission applies to growing work: warn at 50 GiB, red at 25 GiB, critical at 10 GiB;
release thresholds are 60/30/20 GiB. Cleanup requires explicit confirmation.
Remote work uses existing Hetzner jobs and receipts. This program does not add a remote scheduler.

## Acceptance checklist

| Area | Implemented result | Acceptance state |
| --- | --- | --- |
| Installation identity | Capabilities expose product version, revision, executable and platform. Five-binary promotion verifies hashes, clean source, Darwin CGO and identity before switching one authority link; previous authority and first-migration files are preserved. | Installed; all five release hashes verified. Factory front door `483b1def` is landed and installed. CLI and full-bundle app authority guards installed; stale-intent and downgrade negative checks pass. Factory 0535927f stamp independently verified at all three targets; schema-v4 publisher source-closure receipt is retained. |
| Readiness | Admission-only is a valid ready mode; persisted policy, storage, monitor freshness and resident baseline remain explicit gates. | Installed. Current 0535927f normal-state CPU and all resource budgets pass. Earlier CPU failures are preserved; elapsed observation remains pending. |
| Telemetry | Real product writer and reader; privacy validation, closed fields, failure classes, attempted/written/failed/gap counters and freshness; bounded sharding and full lock-to-append timing. Factory correlation and product-owned registry fragment implemented. | Product and factory integration installed. Historical gaps and factory latency failures remain visible. |
| Retention | Owned JSONL, writer health, disk maintenance and monitor streams have bounded retention; pruning and append failures are reported. Helper artifacts retain active plus two rollback revisions. | Implemented and tested. Unique legacy install backups are preserved. |
| Work coordinator | Private host identity and OS/architecture/CPU/revision cohorts, explicit legacy counts and reuse miss reasons. Mixed-cohort retunes fail closed. Current CPU/fairness/capacity gates remain in force. | Installed. Replay promotion and live slowdown SLO still fail; no unsupported weight increase applied. |
| Native desktop | Twelve sections, including Day Gate and concurrent Process Incidents and Hetzner, refresh on entry; requests coalesce and have time/output bounds; per-section age/errors retain prior results. Settings show executing identity. SMART age and native write-window units are correct. | 68 exact-owned tests and combined signed verification pass. Twelve-pane 1.4.1 build 9 installed and live reviewed; peer Hetzner source is preserved separately. New idle/elapsed observation started. |
| GPUI preview | Executable validation, bounded stdout/stderr, nonblocking reads, process-group cleanup, timeouts and explicit failed envelopes; supported-operation matrix documented. | Ten core tests and full Rust build pass; remains a preview. |
| Policy | Exact legacy defaults migrate idempotently; custom settings stay intact. Admission and storage enforcement enabled, automatic shedding disabled. Cleanup remains confirmed and manual. | Installed and live policy audit passes. |
| Go cache helper | Immutable content-addressed objects, cross-process pinning until close, crash recovery, strict sizes and bounded streaming/journal/maintenance. Pinned objects can temporarily exceed the soft limit. | Race tests and real Go cache integration pass on runner. Live reuse history still reports zero hits and concrete miss reasons. |
| Local API | Product owns CLI/Unix API contracts. Unix socket permissions/peer UID, request deadlines and response bounds; native client has CLI fallback and loopback redirect limits. | Real installed Swift/Unix-API contract passes, including capabilities, SMART and live query. |
| Privileged trace | Signed helper verifies Team ID; administrator authorization, PID identity, XPC completion and independent trace wall time are bounded. Restricted entitlement without provisioning profile removed after a real launch failure. | Signed app launches. Administrator-approved trace is not exercised by this audit. |
| Validation | Entire Go race suite, targeted boundary/race tests, fresh Swift tests, GPUI tests/build, real installed API contract, native SMART read and UI review. | Immediate checks pass. Warm latency, idle and 24/72-hour evidence are recorded independently below. |
| Delivery | Product changes published with guarded exact-path commits. Three factory deliveries landed, two are accepted, one is held and one is paused. Clean native bundle built on Hetzner and promoted with receipts. | Four factory deliveries, installed builder/filter/promotion state, native producer recovery and long observation remain open. |

## Baseline evidence (2026-11-08)

- CLI/helper revision `2a44637a36ee3932a41f6c543520e075366c17b1` differed from the front door product revision `c136a8f415595b6e9b8f4ee01c85315aef75de80`.
- Monitor PID 96405 had run since November 5. Peak RSS 21–23.4 MiB, CPU duty 0.144–0.20%, sample CPU p95 25–30.6 ms; these passed the installed budgets. No duplicate resident was established.
- Admission was enabled, automatic shedding disabled, storage admission disabled. Readiness rejected admission-only mode solely because automatic shedding was off.
- Scheduler 72-hour sample: 1,453 operations, 5,760 events; total wait 9.72 hours. Wait shares: capacity 37.37%, reservations 27.05%, other queued work 20.97%, CPU 12.48%. Current test/build p95 slowdown 15.93/27.16. Replay promotion failed its existing 25% gap-closure gate. Capacity must not be raised without evidence.
- CPU used/reserved: test 0.62/3.75, build 0.11/6.25, express-test 0.53/1.25, express-build 0.71/2.5. Singleflight reported 0 hits/955 misses. These figures require host-aware calibration, not blind weight changes.
- Central telemetry contained 4,556 pressure command events, including 264 in 24 hours and 50 historical nonzero results. Legacy fields made the normal usage query miss those events. Twelve historical parse errors existed; none was recent. Preserve these records.
- Product writer health and privacy functions were stubs. Local pressure JSONL had 1,379 rows across four files in 24 hours with no parse errors. Disk-full stderr dated August 30 and was not a current incident.
- Native sections were inspected: Overview, Trees, Disk Writes, Storage, Work, Policy, Monitor, Telemetry, Settings. Several did not refresh on entry. Telemetry could show empty before polling. Disk writer bytes used a fixed 15-minute label despite a one-second window.
- SMART sample was from November 6; vendor-used 10%, host writes 247.8 TB, media errors/critical warnings zero. Host writes are not NAND writes or proof of remaining lifetime.
- Three-sample timings were exploratory, not SLO evidence: product status 46–61 ms, board 48–168 ms, rich reads 169–316 ms; factory status 376–524 ms, board 391–489 ms, rich reads 754–1,299 ms.
- Portable Go subset passed on Hetzner at baseline; receipt `20261108T153953Z-1943759`. Linux full-suite failures included macOS-dependent probes. Cached Swift tests passed; a fresh admitted run was denied under host CPU/swap pressure.

## Release gates

Keep status/board/rich warm p95 targets at 250/500/1,000 ms over at least 100 samples.
Native idle acceptance requires 30 minutes, at most 1% CPU and 128 MiB RSS.
Never treat accepted delivery as landed source, source tests as installed behavior,
or a short sample as a 24/72-hour soak. Record candidate identity, test command,
receipt, installed identity, observation start and end, and remaining blockers.

## Verification and delivery log

Product source includes `55ab6a763e7207f11da6ada01dda5c6245710cbe`, the standalone writer audit correction, in the full-race-tested dependency closure `cdcd003e601da242c7e18f432f4ae2730c7ae3cb`. Later authorized concurrent installations are recorded separately below.
The implementation is installed in the standalone product. The complete epic is **not accepted**:
factory delivery/install, performance failures, manual privileged trace, and elapsed gates remain open.

### Owned source changes

- `52fb125`: product capabilities, 0.7.0 contract and admission-only readiness.
- `d38fd91`: immutable and pinned Go cache objects.
- `5bae95ef`: real telemetry writer, denominators, privacy, retention and host-aware calibration.
- `5a0d45bd`: bounded native clients, refresh/freshness behavior and tenable failure states.
- `38e84889`: verified release/promotion, rollback and elapsed observation tooling.
- `c9028637`: conservative policy migration, WAL-aware health reads and readiness correction.
- `1bb2ac92`, `07ab1cbe`, `b05b896f`: native SMART requirements and clean cross-compiler provenance.
- `e7a90151`: privacy validation cache migration without deleting historical records.
- `c60bdba8`: live API envelope/query corrections, writer contention/failure evidence, reuse fixture and manual-cleanup audit corrections.
- `d4a08a7`: remove unsupported restricted entitlement; preserve helper identity and administrator authorization.
- `8610eee9`, `7162b4a4`, `90ab4e0c`: interrupted-run evidence preservation, actual resident health checks and bounded observation process trees.
- `55ab6a76`: standalone writer failures, unresolved attempts, retention failures and latency now control audit findings.
- `c2caba94`: parked leases do not consume charged capacity; actual overcommit, reservation and malformed-weight checks remain enforced. Seven targeted invariant cases pass under race testing.

Concurrent WORK-82 process detection and confirmed control landed in `2aa0284` and
`acff0fb`. Their work is integrated; it is not attributed to this audit. Browser pause/recovery, remote routing and outcome telemetry from the concurrent
owner landed in `b839ac8`, `2021be6`, `135d517` and `89f1b30`. The last two commits
fix bare `go build` validation and exclude generated temporary trees from uploads.
Hetzner navigation and further process-review edits remain separately owned work.

### Test evidence

Heavy Go work ran on `gaterunner-1` through the canonical `remote-verify.sh` wrapper.
Receipts are in `/srv/build/logs/jobs.log`; stdout is in `/srv/build/logs/jobs/<job>.log`.
Private local acceptance copies are under
`~/.nicos-dev/session-pressure/acceptance/2026-11-08/`.

| Candidate / check | Result and receipt |
| --- | --- |
| Committed `cdcd003`, `GOWORK=off go test -race ./...` | PASS, all packages, 74 s; job `20261108T183500Z-3622634`. Core 27.217 s, cache command 35.737 s, command 2.834 s, control 3.975 s, release 1.106 s. |
| Committed `c2caba94`, `GOWORK=off go test -race ./...` | PASS, all packages, 60 s; job `20261108T181008Z-3422475`. Core 29.808 s, cache 41.620 s, control 7.737 s, command 4.133 s. |
| Committed `c60bdba8`, `GOWORK=off go test -race ./...` | PASS, all packages, 145 s; job `20261108T174159Z-2970659`. Cache command took 121.584 s, core 33.536 s, control 11.955 s. |
| Writer contention and real Go singleflight/reuse repeated race checks | PASS; cold fixture compilation moved outside the 20 s coordination window instead of extending that gate. |
| Release rollback / tamper and CGO validation | PASS; corrupt bundle cannot replace installed authority; incomplete Darwin bundles rejected. |
| Factory builder race tests with native CGO and source digest | PASS, 6.776 s; job `20261108T174333Z-2990133`. Accepted source is not an installed factory binary. |
| Factory telemetry registry, trace and wrapper tests | PASS on source candidates; registry fragment is bounded and validates duplicates/schema; generic/leaf trace pair is tested exactly once. |
| Swift fresh tests + Developer ID signed verification | PASS, 59 tests in eight suites. `native-signing-verify.log` retained. |
| Real installed `PRESSURE_CONTRACT_BIN=~/.local/bin/session-pressure swift test --filter InstalledContractTests` | PASS on installed `cdcd003`, 1 Swift Testing test, 2.180 s at 18:38 UTC. Real CLI identity/SMART decoded; private temporary Unix API live read and identity matched. |
| GPUI core tests and complete Rust build | PASS, ten core tests and full build; documented preview capabilities retained. |
| Restart evidence preservation, `go test -race ./scripts/release` | PASS, 1.032 s; job `20261108T175254Z-3114872`. |
| Resident health observation, `go test -race ./scripts/release` | PASS, 1.035 s; job `20261108T175620Z-3188711`. |
| Observation output bounds and process-tree cancellation | PASS, release 1.136 s and process-tree package 1.131 s. |

Darwin bundles use the checked-in `scripts/release-cc` adapter, the existing
runner Darwin SDK/compiler, `CGO_ENABLED=1`, and internal Go linking. Each executable
has clean Git build information and a manifest SHA-256. Compiler probes run outside
the source directory. Source-only snapshots are used for tests; clean Git bundles
are used for release provenance.

### Installed behavior and live audit

At 17:43 UTC, clean native bundle `c60bdba8` was promoted and the monitor installed.
A concurrent WORK-82 owner promoted the newer committed `d4a08a7` bundle at 17:51 UTC;
live capabilities and the verified launchd artifact confirmed that newer authority.
The signed app launch fix was installed at 17:47 UTC. All ten sections were inspected:
Overview, Agent Trees, Process Incidents, Disk Writes, Storage, Work Queue, Policy,
Monitor, Telemetry and Settings. The app was parked on Overview and minimized for
idle collection. Settings showed the canonical product CLI; disk data showed its
actual 1 s measurement window and a fresh native SMART sample.

Native SMART measured vendor estimated life used 11%, approximately 250.1 TB host
writes, zero media errors and zero critical warnings. Host writes do not measure
NAND writes or predict remaining lifetime. Raw device identifiers are not copied here.

The 17:53 UTC full audit is retained privately as `audit-1753.json`:

- Policy, readings, cleanup and artifact retention PASS. Automatic shedding remains
  false; admission true; storage admission true. Storage floors are 50/25/10 GiB,
  with 60/30/20 GiB release hysteresis. Available data volume space was about 101 GiB.
- Product writer: 528 attempted, 528 written, zero failures/gaps; p95 81.229 ms,
  max 173.808 ms. Timing includes lock queue through primary append. Product JSONL:
  four files, 3,411 rows, zero parse/privacy/mode violations.
- Factory JSONL: 192 files, 30,414 rows, zero parse/privacy/mode violations.
  Privacy cache migration is warm with `load_error=false`, `write_error=false`.
- Factory writer history: 8,204 attempted / 8,201 written, zero terminal failures,
  three preserved gaps. Recent two-hour cohort: 3,335 / 3,333, two gaps, p95 upper
  bound 250 ms against 100 ms ceiling, max 2.018 s. This is a real FAIL.
- Resident is verified, loaded, healthy and fresh. Its new PID still needs four normal
  samples. Guard peak RSS 15.48 MiB, duty 0.265%, sample CPU p95 75.731 ms and
  projected telemetry 528,384 bytes/day remain within their budgets.
- Scheduling remains FAIL. Recent end-to-end p95 bounded slowdown:
  express-test 40.55 (36 samples), test 55.07 (37), express-build 14.39 (75),
  build 31.92 (21), against target 5. After subtracting measured host-pressure wait,
  respective p95 values are 15.95, 7.30, 7.83, 22.84. Seventy operations crossed
  the long-wait threshold. Four records have incomplete pressure decomposition;
  they remain explicitly excluded from that informational projection.
- Calibration at 17:54 UTC: one known host cohort with 319 events, 2,417 legacy
  unattributed host events, zero reuse hits; miss reasons include unavailable receipt
  788, explicit disabled 44, changed source 25 and expired TTL 4. No threshold retune
  is recommended. Promotion must meet the existing replay gate and live SLO.

The full audit remains FAIL. Historical failures are preserved, and source tests do
not override the live result. Performance repair requires a measured cause and a
verified improvement, rather than budget increases or history deletion.

### Later installed evidence and observation boundaries

At 18:14 UTC, clean native `c2caba94` was promoted. The concurrent owner then
promoted `2021be6` at about 18:16 UTC. Its verified resident was healthy and fresh:
RSS 15.91 MiB (peak 16.20), CPU duty 0.0651%, sample CPU 78.056 ms and projected
telemetry 528,384 bytes/day. These resource budgets passed. This does not clear
older latency or scheduling failures.

The c2 full audit at 18:15 UTC retained these results:

- Policy, readings, cleanup and artifact checks PASS. Charged capacity and parked
  lease accounting are consistent. The just-restarted resident briefly exceeded
  the CPU-duty budget; the later resident measurement above passed.
- Factory JSONL: 192 files and 32,680 rows; zero parse/privacy/mode violations.
  Recent two-hour writer cohort: 2,697 attempts, 2,695 written, zero terminal
  failures, two preserved gaps; p95 histogram upper bound 250 ms, max 2.018 s.
- Product writer: 1,152 attempts / 1,151 written, zero terminal failures, one
  unresolved attempt (a live writer can still be in progress); p95 167.899 ms,
  max 2.529 s. Product writer latency therefore also exceeds the 100 ms ceiling.
- Recent scheduler p95 slowdown: express-test 40.55 (39 samples), test 55.07 (38),
  express-build 14.39 (79), build 23.91 (28), versus target 5. Current-controller
  work evidence is insufficient, rather than a passing new cohort.

A bounded alternating diagnostic of 20 status reads found production median
52.371 ms (max 75.640), versus diagnostic-only telemetry-disabled median
26.334 ms (max 39.440). This small sample suggests writer cost and host load
need matched profiling; it is not a p95 acceptance result. Telemetry remains on.

The standalone product writer was displayed in the audit without controlling its
verdict. The correction now applies the same latency sample count and thresholds
as the factory writer, fails on terminal write/retention errors and warns on
unknown coverage or unresolved attempts. In-flight attempts are not labelled
proven data loss. Boundary race tests cover clean/unknown/failed/in-flight/retention
states and the shared latency ceiling. They passed on Hetzner in 1.038 s;
job `20261108T182251Z-3494362`.

Private observation outputs are never overwritten:

| Run | Result / cause |
| --- | --- |
| `observation-90ab4e0c.json` | Interrupted after launchd background priority distorted latency; the failed output and interruption record are retained. |
| `observation-90ab4e0c-standard.json` | Source identity changed during another authorized install. Status p95 602 ms FAIL; board 264 ms and rich 383 ms passed their budgets, but the run is not acceptance. |
| `observation-c2caba94.json` | Source changed to `2021be6`. Status p95 474 ms with 65 failed reads; board 322 ms and rich 514 ms had no failed reads. Mixed-identity results are not acceptance. |

The failed c2 collector was stopped at about 18:34 UTC with a separate
interruption record before the next promotion. The next 72-hour observation must
bind a stable installed revision. Each later
promotion or resident/app PID change must remain visible. No 24-hour, 72-hour or
30-minute idle acceptance is inferred from these short or interrupted runs.

### Factory delivery and installation boundary

| Delivery | Owned paths / result | Current state |
| --- | --- | --- |
| `aa7b8fa9-4821-45c1-874b-85265d691db0` | Canonical standalone binary priority, trace context, exact-once tests, product registry fragment and owner | Landed 19:05 UTC as `483b1def`; native factory installation independently verified 19:15 UTC |
| `64c635e4-3fae-4254-bcd6-24e913bbaba0` | Committed export explicit target / CGO native compiler support and receipt | Accepted 17:21 UTC, not landed |
| `2aca8f87-72ad-4d63-95cb-58f5a7248b90` | Export receipt canonical source digest required by atomic binary publisher | Accepted 17:43 UTC, stacked after prior builder delivery, not landed |
| `be3a3593-6e93-482c-8901-1b9c4289e446` | Exact owned staging isolation for fallback commit helper, regression tests and workflow reference | Accepted 18:30 UTC, capture gates PASS, not landed |

At 18:16 UTC the existing install-handoff flock protocol admitted the clean
commit-delivery upgrade. The worker now reports version 2.33.0, healthy and busy,
matching the installed client. This used a natural idle handoff and did not stop
an active worker. The private `factory-guarded-install.json` receipt is retained.
`SERVICE_PREFLIGHT_SOURCE_LAG` remains a warning about the separately pinned
factory preflight revision. The doctor says to retain the wait and not repeat
accepted captures. No queue bypass or unrequested priority change was used. The factory registry and wrapper are not
called installed before landing and committed native export/promotion.

After landing, build the committed factory `ndev` with native Darwin CGO on the
runner. Use the `source_digest` and `revision` in `export.json` with the existing
`ndev-binary-publish` command; publish its three managed binary targets atomically
and verify the receipt. Never replace the installed executable with a dirty tree
build or a manual copy. Re-run installed wrapper, trace, registry coverage and
telemetry queries after promotion.

### Remaining gates and ownership (earlier checkpoint)

| Gate | Owner / next action | Acceptance |
| --- | --- | --- |
| Factory delivery + install | Agent: retain delivery wait, inspect terminal receipts, export committed native candidate and use atomic publisher | All six deliveries landed; installed wrapper/registry/trace/filter contract and source digest receipt match |
| Factory and product writer latency / preserved gaps | Agent: profile authority registration and primary append under a matched current cohort; retain gap evidence | p95 <=100 ms with no new unexplained loss, without weakening durability/privacy |
| Work coordinator performance | Agent: collect enough revision/host-attributed samples, replay calibrated reservation candidates and dogfood the passing candidate | Existing 25% replay gap-closure gate and live p95 slowdown <=5, without violating host floors |
| Resident recovery CPU | Agent: reduce measured loop overhead at the 5/15-second recovery cadence, preserve policy thresholds | Normal-state duty <=0.5%, pressure-state and other resource budgets pass |
| Desktop idle | Collector: one parked app PID for >=30 min | CPU <=1%, peak RSS <=128 MiB; restarted PID gets a new window |
| 24/72-hour continuity | Collector: monitor health, identity and resource budgets each minute, preserve interrupted output | Actual elapsed windows, no excessive sample gap, zero health failures; inspect every budget result |
| Privileged trace | Human: approve the app's ordinary macOS administrator prompt when a trace is requested | Helper registration, authorized finite trace and resulting path evidence; no prompt approved by the audit |

Use `ndev-commit-delivery --json delivery wait --owned` from the factory with the
recorded agent/session environment. Read the observation JSON and run
`session-pressure --json audit --full` for fresh truth. Keep WORK-41 in progress
until the remaining gates are proven.

### Self-evaluation and process improvements

1. Fixture decoding missed a real API `api_version` CodingKey and query parameter.
   The opt-in installed Swift contract test now exercises the real CLI and Unix API.
2. Portable Darwin builds ran but omitted native SMART. Release and install now reject
   that incomplete capability, and clean build metadata is checked for every binary.
3. Static code-sign verification passed an app that AMFI would not launch because
   of an unsupported restricted entitlement without provisioning. The entitlement was
   removed; actual app launch and pane review are release evidence. Team ID checks,
   administrator authorization and bounded helper time remain in place.
4. Race testing exposed writer contention and a cold compiler fixture inside a
   singleflight wait window. Writer contention/failure evidence was fixed, and the
   fixture is precompiled instead of relaxing its production timeout.
5. Observation restarts originally could erase failures, and an `ok=true` read did
   not prove resident health. New-output exclusivity, resident validation, PID-change
   counters, context deadlines and process-tree cleanup now cover those boundaries.
6. Factory capture still compiles substantial Go producer code on the Mac. This is
   a factory offload/admission gap, not evidence that heavy local builds are preferred.
7. Agent misuse was visible: an outdated delivery command, a wrong cwd and an
   oversized board decision failed. Correct direct delivery commands and <=240-character
   linked decisions are used here. Guarded HEAD comparison also rejected a stale
   expectation when a peer committed; a fresh snapshot succeeded without disturbing it.

Reference for the native helper registration model:
[Apple SMAppService guide](https://developer.apple.com/forums/thread/802443).


8. A background-priority launchd collector distorted latency under host load. It
   was stopped with an interruption record; the normal-priority replacement got
   a new output. Repeated source promotions correctly invalidated later runs.
9. Product writer counters did not affect audit status. The new verdict boundary
   tests prevent metrics-only terminal failures and enforce the existing latency
   ceiling for both writer authorities.
10. Strict central telemetry coverage still passes: 131 covered plus 13 exempt
    entities out of 144, with 53 registered sources. This measures the installed
    factory and does not prove the queued product registry fragment is installed.
    The available baseline does not support a full summary diff; raw failure
    samples include concurrent sessions and must not be attributed to this owner.

11. Two sessions staged disjoint files at once and both fallback commit helpers
    refused the mixed index. The guarded helper now has explicit owned-entry
    isolation: one private commit index, unchanged full shared-tree fence, hook
    doctor and HEAD compare-and-swap. Tests cover foreign staged and unstaged
    preservation, owned unstaged changes, additions/deletions, literal pathspec
    characters, unborn HEAD and concurrent shared-index mutation. The first
    minimal remote test fixture omitted a helper; that setup failure is retained.
    The complete fixture passed all tests under the race detector in 3.452 s;
    job `20261108T182942Z-3513379`.
    The repair is captured as `be3a3593`; it is used from the tested source while
    central delivery remains queued. Board decisions must be length-checked
    before submission; another 241-character misuse was observed and corrected.

12. A clean bundle at `55ab6a76` failed compilation because a concurrent source
    commit called `remoteWorkIneligibleReason` before its defining file was
    committed. Both the full-suite failure and native build refusal were retained;
    no bundle was installed. `cdcd003` commits the missing definition. The complete `cdcd003` bundle
    then passed the full Go race suite and native Darwin build. Splitting commits
    must preserve a buildable dependency closure, even when staged paths have
    separate owners.

### Later source and operational findings (18:38–18:46 UTC)

The clean `cdcd003` five-binary Darwin build passed in 12 s;
job `20261108T183500Z-3622722`. A concurrent owner promoted that revision first.
The installer refused our independently built, different-byte bundle for the
same revision. The existing authority was retained. Independent checks verified
all five manifest hashes, exact VCS revisions, clean source and `CGO_ENABLED=1`.
The resident installed at 18:37 UTC was PID 47481 with verified helper SHA
`9424db1c9c59bfddaef14d340082b07b916e1651ad1bf7e75d1bb35cd47e08a1`.

The 18:38 audit found product writer 2,179 attempted / 2,179 written,
zero failures/gaps, p95 174.857 ms. Factory recent p95 was 250 ms.
The resident later proved its four normal baseline samples, but budget health
still depended on the current sampling cadence and host state.

A normal-priority launchd observation started at 18:41:54.583615 UTC, with deadline
November 11 at 18:41:54.583615 UTC, in `observation-cdcd003e.json`.
All three endpoints had 100 reads. Status p95 154.035 ms was within its latency
budget but had eight failed reads. Board p95 178.284 ms and rich p95 774.972 ms
had zero failed reads and passed. An authorized concurrent promotion to
`670d488` invalidated source continuity before the first health check. This
failed run is preserved and does not establish 24/72-hour acceptance.

At 18:44 UTC, a separate 200-read status diagnostic found valid JSON with
nonzero health results. Current resident PID 82962 had a normal-state CPU duty
of 0.956% against the unchanged 0.5% budget. Fast browser/admission recovery
sampling is active at 5/15-second intervals. The loop needs measured overhead
reduction; raising the budget does not resolve the defect. The shared board
records this boundary for the recovery owner. Two `NDevPressure` processes
were present, including a peer's isolated UI review app. The collector refuses
to attribute idle CPU to an ambiguous process; no foreign process was stopped.

At 18:45 UTC, all five installed `670d488` binaries independently matched their
manifest, clean VCS revision and native CGO setting. `audit-latest.json` reports:
policy/readings/cleanup/artifacts PASS; resident/scheduler/performance FAIL.
Product writer 2,989 attempted/written, zero failures/gaps, p95 157.371 ms.
Factory current activation-hour authority 723/723 with zero gaps, p95 250 ms;
the preceding two-hour history retains two unresolved attempts.
Scheduler p95 slowdown: express-test 40.55 (37 samples), test 55.07 (31),
express-build 10.09 (89), build 22.84 (31), target 5. Pressure-subtracted
informational p95 remains above 5 in all four classes; it cannot replace
end-to-end acceptance. Long waits 77 and stale-owner reconciliations 9.

At 18:46 UTC, the peer promoted `8273bb1`, which adds repository-aware remote
fixture closure and truthful decline/finish events. Peer test results are
coordination evidence until independently checked. No complete race result
for that newer source is inferred from the `cdcd003` race receipt.

Factory doctor at 18:42 UTC: four intents/pointers/source refs remain intact,
worker healthy/busy/current, all four owned deliveries still accepted. The three
older captures are overdue with active worker progress; do not repeat them.
The separately pinned preflight source warning remains. A retained delivery
wait is active; no automatic factory build/install watcher is claimed.

### Measured writer improvement

`179a3193ab835fd2518c7924d44741c582c43ade` removes the disk sync from
ephemeral lock ownership records. Complete write/close before atomic hard-link
publication still prevents partial ownership visibility. A lock coordinates live
processes; the existing dead-owner recovery handles a stopped owner. The actual
telemetry attempt, primary event and completion durability syncs are unchanged.

A new checked-in `BenchmarkDurableCommandWriter` validates the attempt/written
counts and measures p95 on the target filesystem. Both before/after test binaries
were compiled on the runner for Darwin arm64, then executed on the Mac through
pressure admission. Three alternating pairs each wrote 100 events. Before p95:
34.737/31.483/42.777 ms; after: 28.592/25.550/37.804 ms. Median p95 improved
17.7%. Every run had 100 written, zero failed and zero gaps. This controlled
benchmark does not clear the historical live authority or factory writer SLO.

Lock and telemetry race tests passed in 3.049/1.620 s, including cross-process
critical-section, complete publication, contention, cancellation and stale-owner
coverage; job `20261108T184639Z-3642477`, wall 6 s. The complete committed
race suite and native release build passed in runner job
`20261108T184858Z-3678495`, wall 82 s. Core race tests took 24.738 s; telemetry
1.618 s, CLI 2.549 s, Unix control 3.880 s. The five native binaries and manifest
were downloaded to `~/.nicos-dev/session-pressure/releases/179a3193/`. Promotion
was held because the concurrent browser owner is testing combined `cc23fb2`,
which includes this change and newer coverage/remote-source fixes. A tested
ancestor release is not substituted for the combined source.


### Resident control overhead follow-up

The monitor's recovery delay treated a manual browser pause like automatic
recovery: it kept sampling every five seconds forever, including when no work
was waiting. Paused mode cannot recover from CPU readings. The correction uses
the policy interval while paused and idle; explicit work admission holds and
latched work still request five-second checks, and shorter critical intervals
remain unchanged. No live browser mode or remote-routing preference is changed.

Unchanged browser control reads also rewrote and synced the complete durable
record. The correction skips that replacement unless a control field changes,
and avoids reading the full resident snapshot in paused mode. Real mode changes
still use the same lock and durable atomic write. A regression check verifies
that twenty unchanged reads preserve the file identity and timestamp, while a
mode change replaces the record. Further checks cover paused idle/waiting work,
critical cadence and the existing distinct-sample automatic recovery contract.

The first focused run correctly pruned the test fixture's synthetic dead PID;
its waiting-work assertion failed. The fixture now uses the real process and
clock because the function opens a fresh coordinator. That failed receipt is
preserved in `paused-cadence-race.log`; it is not called a passing check.

At 18:55 UTC, the current installed resident again passed health with normal
CPU duty 0.314%, sample CPU p95 57.102 ms, RSS 15.656 MiB and projected telemetry
528,384 bytes/day. This short later result does not erase the 18:44 failure or
prove a stable 72-hour cohort. The invalidated `cdcd003` acceptance collector
was stopped with a separate interruption record at 18:54 UTC. Its failed output
is preserved; a new observation waits for the combined stable release.

Factory status at 18:53 UTC, run from **nicos-tools**, still reports all four
owned deliveries accepted, not landed. A status/doctor run from the standalone
product sees no factory intents; that repository-scoped empty result must never
be interpreted as factory delivery completion. Keep the retained factory wait.


The corrected focused control race tests passed in 1.071 s, runner job
`20261108T185816Z-3866297` (26 s including Darwin benchmark compilation).
The first three 100-read timing pairs were noisy and did not support an
improvement claim. Six longer alternating-order pairs of 1,000 reads on the Mac
then measured median cost 1.209 ms before and 0.880 ms after (27.2% lower).
All 12,000 reads preserved paused state. Raw results, including the initial
noisy run, are retained as `control-profile-mac*.json`. This measures control
read cost; it does not prove the resident's whole-process duty budget.

To preserve prompt recovery after reducing idle sampling, the resident rechecks
control and admission demand every five seconds without a full host sample.
A bounded 500 ms demand probe can wake the full sampler when Auto is enabled or
work starts waiting during the normal sleep. Cancellation stays immediate.
Integration checks exercise both live demand transitions with real process
identity. The complete source race run includes these checks.

At 19:01 UTC, the concurrent combined `cc23fb2` release was independently checked:
all five installed binaries matched one manifest, its exact VCS revision,
`vcs.modified=false` and `CGO_ENABLED=1`. The real installed Swift/Unix contract
passed (one test, 2.904 s). This release includes `179a319` and the concurrent
remote-source, telemetry outcome and standalone coverage fixes. The newer
control optimization is source-only until its own final test and promotion.


Control optimization source is committed as
`3e5da8f46b9cc54003e29278f4c0abdedca85644`. The full candidate Go race suite
passed, runner job `20261108T190116Z-3868757`, wall 84 s. Core 30.286 s,
telemetry 1.627 s, CLI 2.555 s, control 4.095 s. This includes real-process
wake-on-demand checks and the existing monitor/automatic recovery tests. The
commit contains the exact four tested files; its parent is the archived
`51fa78d` source. Publication used the guarded owned-entry helper; foreign
staged and worktree changes remained present. Native build/promotion is recorded
separately and respects the concurrent owner's live remote-proof hold.

The fresh `cc23fb2` audit at 19:01 UTC passes resident, policy, readings, cleanup
and artifacts. Resident PID 74540; normal duty 0.277%, sample CPU p95 14.998 ms,
RSS 14.281 MiB. Historical scheduler and writer performance still fail:
product writer p95 155.479 ms, factory activation-hour p95 upper bound 250 ms.
The current-controller scheduler cohort contains only four operations, one
terminal-runtime sample, and is explicitly insufficient data. It cannot replace
or clear the historical slowdown failures. `audit-cc23fb2.json` is the full
private receipt.

Factory doctor at 18:59 UTC is `ok=true` with four intents, pointers and source
refs intact; worker healthy/busy/current version 2.33.0. Worker and preflight
source-lag warnings now reflect newer concurrent commits. It explicitly reports
active progress for the three overdue captures and says to keep waiting.
Foreign uncommitted policy remains excluded from delivery. No active worker was
stopped or queue priority changed.


### Additional self-evaluation

13. The writer improvement was measured with remotely compiled native executables
    on the target filesystem. Removing a lock-record flush retained atomic
    publication and all data durability syncs; three 100-event pairs reduced
    median p95 by 17.7%. A microbenchmark is evidence for that cost, not live SLO
    acceptance.
14. Status polling itself generated durable control writes, and paused mode
    demanded full host samples forever. The code now separates unchanged reads,
    paused idle cadence and new recovery demand. Initial noisy timings were
    retained, then a longer alternating-order comparison supported the 27.2%
    control-read improvement. A synthetic test PID was correctly rejected by a
    fresh coordinator; the fixture now uses the real process identity.
15. Empty standalone delivery status was almost mistaken for factory landing.
    The fresh factory-scoped status/doctor proves all four captures remain
    queued. Commands and receipts must always bind the intended repository.
    Repeated length assertions prevented overlong board submissions; keep
    coordination summaries below 240 characters before invoking the CLI.


### Final combined runtime handoff

The control optimization's native release build passed on Hetzner, job
`20261108T190403Z-3876991`, 12 s execution, exit 0. Its ancestor bundle was not
promoted. The concurrent release owner instead promoted combined
`828b1ea7bb400423557648cfb84d033dcae07db7`, including `3e5da8f` and fail-closed
remote routing. The owner reports an unsupported GOFLAGS profile now exits 1
without local fallback; that command receipt is the owner's evidence.

At 19:10 UTC, this audit independently verified all five installed executables:
SHA-256 manifest parity, one authority directory, exact clean VCS revision and
native CGO enabled. An ancestry check confirms `3e5da8f` is included. The real
installed Swift/Unix API contract passed again: one test, 2.639 s. Private
receipts are `release-828b1ea-verification.json` and
`828b1ea-installed-contract.log`.

Resident PID 8526, helper SHA
`b883b94978b54b6023ff08ecb13ee6d5575ce98b966bc485611b80d2e38f2859`, passed
monitor health, freshness and admission-only operator readiness with five normal
samples. Baseline duty 0.255%, sample CPU p95 50.385 ms, RSS 15.828 MiB and
projected telemetry 528,384 bytes/day pass unchanged limits. No browser or remote
routing setting was changed by this audit; the concurrent owner set automatic
recovery under their user instructions.

The 19:10 full audit still returns FAIL. Policy/resident/readings/cleanup/artifacts
pass. Product writer p95 is 152.833 ms across 3,261 attempts/written, zero failures
or gaps. Factory two-hour writer p95 upper bound is 250 ms, one preserved gap;
its new activation-hour cohort is 209/209, zero gaps, p95 upper bound 50 ms.
The latter is a short cohort, not a replacement for historical acceptance.
Factory 24-hour authority retains three gaps. JSONL scans: product 4,162 rows and
factory 38,476 rows, zero parse/privacy/mode violations. Current scheduler cohort
contains two terminal operations and is insufficient data. Historical end-to-end
slowdown breaches remain; the pressure-excluded view is informational only.
`audit-828b1ea.json` retains all findings and denominators.

Factory telemetry integration `aa7b8fa9-4821-45c1-874b-85265d691db0` landed at
19:05:22 UTC as `483b1def839b3dd1650bf52747e7bebf32a59e62`; the typed terminal
receipt is `factory-telemetry-landed.json`. This does not establish installed
factory integration. Builder deliveries `64c635e4` and `2aca8f87`, and guarded
commit delivery `be3a3593`, remain accepted. One retained 20-minute factory wait
is active. No priority change, duplicate capture or unauthorized push was used.

After baseline readiness passed, the normal-priority acceptance LaunchAgent was
started once with unique output `observation-828b1ea.json`, 72-hour duration,
one-minute health sampling and 100 warm reads per endpoint. Previous failed runs
remain intact. Actual start/deadline/read gates are recorded separately below;
a running collector does not prove elapsed acceptance.


### Release regression found by acceptance

The first `828b1ea` observation started at 19:10:37.477107 UTC, with planned
end 2026-11-11 at the same time. All three warm-read gates passed over 100 reads
with zero failures: status p95 96.184 ms, board 81.545 ms, rich 268.065 ms.
At 19:11:04, another promotion changed the authority to ancestor `3e5da8f`.
The collector correctly recorded `installed revision changed`. It was stopped;
`observation-828b1ea.json` and its separate interruption receipt remain intact.
That run is failed acceptance despite its passing read gates.

The verified combined `828b1ea` authority was restored, including the remote
fail-closed fix. This actual regression justified a further release correction:
`d2e57df433200daa800b1b67204f2e37b14033a6` requires an explicit
`--expected-current` revision (or `none` for first install), checks it under the
same install lock, and refuses older build timestamps without `--allow-rollback`.
The receipt records both intent fields. A stale compare-and-swap cannot be
bypassed by the rollback flag. Build time is only a downgrade fence, not a Git
ancestry proof. Old retained installer executables predate this contract; the
runbook requires the current installer for promotion and rollback.

The complete candidate race suite passed in runner job
`20261108T191348Z-4026154`, wall 81 s, core 29.345 s, release 1.115 s. Tests
cover a delayed A-to-B job after C won, explicit rollback with the correct
current revision, rejection of an unpinned or missing authority, idempotent
same-revision install, and unknown build timestamp. Native build and installed
negative checks are recorded below.

A concurrent factory owner published the landed telemetry integration as a
clean native `ndev` at 19:11:31 UTC. This audit independently verified SHA-256
`1789e1f726b67ec8dbf882310380319d69e182018bad594c2b48b18e30f5cfe1` across
all three installed targets against the atomic publisher receipt. It records
clean VCS `483b1def`, native CGO and source digest
`1ddc3abda6c2d18c0ff7ee2bbca6ddbd8227d0109c4e2b830660d2e1d3e14d99`.
Its embedded product stamp is `828b1ea`. This is independent verification of
that installed receipt, not authorship of the peer's build. Strict telemetry
coverage passes: 131 covered entities, 13 exemptions, 144 total, 57 sources.
The three builder/guard helper deliveries remain separate queued work.

16. A kernel install lock prevented overlapping writes but did not protect a
    newer release from a late older build. Actual acceptance caught this. The
    new explicit authority check binds the user's observed release to promotion;
    the older-build fence adds an explicit rollback boundary. Durable observation
    failures were preserved rather than rewritten as a clean cohort.


### Promotion guard validation and longer resident baseline

The final clean native `d2e57df` build passed in job
`20261108T191556Z-4031329` (12 s, exit 0). The five-executable bundle is retained
without promotion. Using its actual Darwin installer against installed `828b1ea`,
a stale expected-current revision failed before changing authority. An older
`3e5da8f` candidate with the correct expected-current also failed before changing
authority. The two negative checks and unchanged authority are recorded in
`promotion-guard-installed-negative.json`; native output is in
`d2e57df4-native-build.log`.

The initial green resident sample did not establish sustained acceptance. At
19:18 UTC PID 18210 had 25 samples, including 23 normal samples, and normal-state
CPU duty 0.539% against 0.5%; it later rose to about 0.66%. Sample CPU p95
15.532 ms, RSS 15.859 MiB and projected telemetry 528,384 bytes/day still passed.
A ten-second native sampling profile is retained in
`resident-828b1ea.sample.txt`. Normal-state idle acceptance cannot be inferred
from a later warning-pressure sample whose applicable guard budget passes.

The concurrent WORK-87 owner committed `8e6cb978` to use read-only coordinator
status for recovery-demand probes, with a held-lock/nonmutation regression test.
The owner reports remote full three-package tests, focused race tests and vet
passing, and is building the forward native release. This is the owner's test
evidence until independent installed verification below. Browser Automatic mode,
remote routing, CPU thresholds and resource budgets were preserved. This audit
will not install its older bundle while that forward promotion is in flight.


### Telemetry triage contract correction

At 19:25 UTC the current projection's operation-scoped 24-hour usage report
(`--operation-id session.pressure --full --no-refresh`) contains 933 typed
parent invocations, 202 nonzero results: 731 success, 142 command failure,
33 user input, 15 timeout, seven expected negative and five policy block.
These counts are all-revision observations, not a clean final-release cohort;
latency includes admitted work execution. The source projection's latest event
was 19:24:31 UTC. A concurrent projection writer held its normal lease, so
explicit no-refresh reads were used; no writer was stopped and no history rebuilt.

Command-name filters `ndev session pressure` and `session pressure` both returned
zero usage because the canonical parent is `ndev session` with operation identity
`session.pressure`. That zero is a filter mismatch, not evidence of zero usage
or failure. A further live defect was found: `usage --failures` silently ignored
`--operation-id` and returned board, workspace and other unrelated failure
clusters. The pressure cluster itself contains 142 command failures. Overall
cluster totals from that unfiltered response must not be attributed to pressure.

The correction adds operation and surface predicates to both failure aggregation
and burst annotation, and passes them through the CLI. Regression coverage
separates pressure from board under their shared parent command, separates
surfaces, checks a missing operation, and exercises actual dispatcher plumbing.
Four owned factory paths are source changes until remote tests, delivery and
installed behavior are proven below. Private evidence: `usage-operation-id.json`,
`failure-clusters-operation-id.json` and the retained command-filter mismatch
reports. A matched full-summary baseline is unavailable, so no synthetic
telemetry diff or improvement claim is made.


### Further delivery and supported-promotion closure

The failure-filter correction passed focused remote race tests in job
`20261108T192801Z-4125172`: package time 1.904 s, execution 56 s, exit 0.
Its four owned paths were captured as `b20237d3-8823-4e62-a034-9dd63b815231`
at 19:32 UTC. Capture was accepted but its producer gate was inconclusive:
the installed pressure router correctly refused the sealed GOFLAGS profile
without a supported remote compiler profile. The delivery owner is integrating
the explicit Mac-only producer placement contract. This is not a passing gate.

A downstream audit found `nicos-dev/Makefile`'s supported
`promote-session-pressure` target still replaced individual product artifacts
through the old builder. That bypassed the new bundle authority fence. Its
replacement requires `SESSION_PRESSURE_RELEASE_DIR` and the pre-build
`SESSION_PRESSURE_EXPECTED_CURRENT`, then invokes the verified bundle installer,
restarts the canonical resident, and checks artifact identity through `ndev`.
It does not run a heavy Go build on the Mac. Missing inputs fail before mutation;
a dry run verifies the quoted bundle path, expected-current argument and absence
of the old builder. `factory-promotion-target-checks.json` retains both checks.
The one-path factory delivery is `196ac71c-82d8-4e09-83b8-0f890c054fbe`, accepted
19:33 UTC with the same inconclusive producer gate. The product runbook documents
the new target contract and the separate front-door native export boundary.

The retained factory wait ended at 19:30 UTC with WAIT_TIMEOUT: the original
three deliveries were still accepted at queue positions 28, 54 and 85. Their
producer continues. A single new 15-minute owned wait covers all five queued
deliveries; captures were not duplicated or reprioritized.

The WORK-87 owner then measured 42–45 ms child CPU per browser enforcement scan,
about 0.3% at a 15-second cadence. Resident duty includes child CPU, which the
main-process native sampling profile could not explain. The owner is replacing
that all-process subprocess scan with native root lookup, retaining termination
and recovery safety. This finding does not yet prove installed improvement.


### Independent 19:41 UTC runtime and writer check

The main factory publisher repaired SQLite with the external LLVM clang 18 link
profile. The schema-v4 install receipt, all three SHA-256 values and each binary's
Go build information were captured in `factory-install-recheck-1940.json`.
Standalone artifact status independently reports no CLI/helper revision mismatch.
This closes the earlier 8e6/9cf front-door mismatch; the forward native browser
fix must still align both authorities again after promotion.

The fresh full audit (`audit-1940.json`) passes policy, readings, cleanup and
artifacts. Resident, scheduler, telemetry and performance fail. At 32 normal
samples, resident duty is 0.654%, RSS 16.141 MiB, sample CPU p95 14.489 ms and
projected telemetry 528,384 bytes/day. Passing resource dimensions do not cancel
the failed CPU gate. This controller has one start and zero stops.

The factory writer's two-hour view reports 3,195 attempts, 3,079 writes, zero
recorded terminal failures and 116 unresolved attempts. A query-only, bounded
SQLite inspection of the pending counters locates 115 of these in 19:11–19:19
UTC, with none after 19:19 at this read. The remaining recent gap predates that
burst. The counters contain no command payload and cannot prove which command
failed; their timing is consistent with the separately observed native SQLite
incident. Do not mark pending attempts as successful or reset their history.
The hour-granularity current-controller metric also contains that earlier burst.
Evidence: `factory-gap-times-1941.json`.

The standalone writer has 3,894 attempts and writes, zero failures and zero gaps;
its historical p95 remains 143.697 ms, above the 100 ms ceiling. Factory p95 is
the 100 ms histogram upper bound against a 25 ms target. The complete JSONL scan
finds zero parse, validation or insecure-file errors across 42,177 factory rows
and 4,272 standalone rows. These integrity results are separate from latency.
Scheduler pressure-conditioned slowdown still fails for express-test (5.96),
test (6.05) and build (22.70) against 5.00. No weight or threshold was raised to
make the audit pass.

Self-review found a boundary risk in the proposed native browser reader: its
reused buffer must be limited to the actual kernel response size before full
argument classification. The owning agent received that review and the request
for a stale-tail regression test. The final runtime gate must include this
correction and all placement ancestry. Shared source was not edited across the
owner's claim.


### Desktop rollback prevention and final candidate

A direct 19:45 UTC bundle read found `/Applications/NDev Pressure.app` at 1.1.0
build 3, with strict deep code-signature verification passing. The running app
was PID 82123. This invalidates the current desktop acceptance window; prior
1.3.0 evidence remains historical. No actor is inferred from timestamps alone.
The clean preserved 1.3.0 build 5 candidate has executable SHA-256
`7f3396400e4983343bb46e7b550148b9141522f181ac6da2f38e88fc9eb8bc55`.
The shared worktree's different app build contains separately owned changes and
is not this audit's installation candidate.

Commit `0535927f` adds `app-status`, a deterministic bounded full-bundle digest,
and mandatory `install-app --expected-current`. The digest covers executable,
helper, resources, plist and signature bytes. Symlinks, special files, oversized
bundles, duplicate identity keys and malformed version/build values fail closed.
The installer verifies signatures before staging, after staging and after
promotion; it checks authority under its lock and again before the rename.
Older semantic versions and lower numeric builds require explicit rollback intent;
that intent never bypasses a stale authority. A private receipt and the previous
app are retained. Receipt or verification failure restores the prior app. Both
native Makefile install targets capture authority before building.

Regression tests cover helper/resource mutation, malformed identities, stale
intent, missing intent, downgrade ordering and stale rollback. Final remote
full-race and native-build evidence is pending. Real installed stale/downgrade
negative checks and restoration must follow the tested native build.

The release README now prefers external LLVM clang 18 linking and records the
SQLite query/ingestion acceptance requirement for factory binaries. The queued
0535927 standalone bundle uses the previously verified product-only Zig/internal
profile. This product has no SQLite dependency; that profile is not factory
acceptance and must not be copied to its producer. Fresh strict telemetry
coverage passed: 131 covered, 13 exempt, 144 total. The 19:42 full telemetry
summary is retained as a future comparison baseline, not a fabricated pre-change
comparison.


### Final desktop integration and fresh observation (20:22 UTC)

The earlier proposal to restore the nine/ten-pane 1.3.0 app was superseded after
comparison with the installed concurrent factory app. The canonical desktop now
includes the bounded read-only Day Gate projection, current Process Incidents
and the preserved peer Hetzner pane. The factory app's Makefile forwards every
target to the canonical app; its unsafe independent install paths are removed.
Three fixture targets verify argument forwarding. Delivery
`e1b4c18a-8dff-45b1-962f-1c90ec1355cb` landed as `ae8f228a0750b846f2332202a12a74fe99f1aaa9`.

The Day Gate model reads the real scrubbed recorded advisor wire contract through
the full `ndev` control plane, never through pressure-only argv. Reads have a
10-second wall-time limit, 8 KiB output ceiling, schema/day validation and
30-minute coalesced cache/backoff. Deep links only navigate. Residue badges do
not mask critical host pressure. Main menu commands include all panes. A live
review found a false “No successful read” toolbar; its regression test now checks
advisor freshness separately from pressure-board freshness. Source commits are
`37383cdc28cd7afb84fac3246523acd470758a6a` and
`28fa1ce577eb9bb7ccb1060af86018fe54bbb7ec`. Guarded staged patches exclude peer
Hetzner changes; none of that source is claimed as this audit's landed work.

Immutable snapshots `desktop-owned-source-2017` and
`desktop-union-source-2017` retain exact source inputs; the latter's JSON manifest
records every source hash and the owned index tree. The exact owned snapshot
passed 68 tests in nine suites. Combined `make verify` passed its tests, real
installed product contract, release compilation and strict signing with Team ID
`C82D23RNQ9`. The signed 1.4.0 build 7 bundle digest is
`d27fe84fb27eda1c95ccc3c7d290393e27427cc50be3a26d95b8152014452876`.
Guarded promotion receipt: `desktop-union-install-2024.json`. The previous build
is retained as `/Applications/NDev Pressure.app.rollback-20261108T202033.695329000Z`.
The real older 1.1.0 candidate is rejected with “app version downgrade requires
explicit --allow-rollback”; before/after bundle hashes match. Receipt:
`app-guard-downgrade-2022.json`. Earlier stale-intent receipt:
`app-guard-installed-stale.json`.

A monitor-scope self-test passes after build completion. During the concurrent
Swift builds, the broader operator self-test correctly refused its browser work
probe under red host CPU/swap pressure; that failed result remains in
`self-test-2020.json`. Resource health was passing in that same result. Do not
classify this admission refusal as a broken monitor, or erase it to obtain a green
summary. Future candidate verification should serialize these native builds to
reduce host CPU/swap pressure; both distinct source candidates still need tests.

New collector label: `com.nicos.session-pressure.acceptance-20261108T202139Z`.
Private output: `observation-0535927f-20261108T202139Z.json`. It reads the actual
installed product, retains its original identity and resident PID, records
continuity/restart failures, samples app CPU/RSS, and tests 100 warm reads per
endpoint. It refuses existing output on login/restart. Deadline is November 11
at about 20:21:39 UTC. The 30-minute app gate cannot pass before about 20:52 UTC;
24-hour and 72-hour elapsed gates require November 9 and 11 respectively. A
revision, resident restart, login interruption or timing gap can invalidate this
run; keep the failed evidence and start a distinct run after repair.

### Final telemetry review and remaining owners

The full-summary comparison at 20:18:51 UTC adds 2,185 events and zero sources
since 19:42:47 UTC. Session Pressure source p95 shifts from 299.413 to 285.684 ms
in this mixed historical projection (4.59% lower); this is directional, not a
matched causal release benchmark. The controlled paired measurements remain
17.7% lower writer p95 and 27.2% lower coordinator-read median. Strict telemetry
coverage passes: 131 covered, 13 exempt, 144 total, 57 registered sources.
There are eleven additional `telemetry.write_failed` events. The installed
`usage --failures --operation-id` still returns unrelated clusters until queued
filter delivery `b20237d3` lands and is installed. Its pressure-specific command
failure cluster has 156 records, compared with 142 at 19:25; both windows mix
revisions. Neither global cluster totals nor these historical counts establish
a new-release failure rate. No history was rebuilt or counters cleared.

| Open gate | Owner and next action | Acceptance evidence |
| --- | --- | --- |
| Forward refusal-contract release | Product publisher Claude `72754fd6` is promoting committed `92eb891`; factory publisher `01a1bc3b` owns its matching native front door. The verified 053 factory source-closure receipt is retained. | Forward product/factory/helper identity agrees, normal resident health recovers, and a distinct post-upgrade observation starts. Do not roll back the typed refusal fix. |
| Four pending factory deliveries | WORK-41 and delivery owner: keep retained wait; repair only printed producer/host faults, then install landed builder/helper/filter/promotion changes. | Each exact delivery is landed; source identity and installed dogfood checks pass. |
| Peer Hetzner source | Owner of `HetznerStore.swift`, `Views/HetznerView.swift` and related four tracked app files: publish the preserved hunks with exact ownership. | Guarded commit and combined 12-pane verification; no dropped peer pane or shortcut collision. |
| Writer reliability and latency | WORK-52/factory telemetry owner: classify each unresolved/error cohort after final native install; measure new attempts/writes/failures and p95 without clearing history. WORK-41 retains standalone latency follow-up. | Zero new unaccounted attempts/errors; factory p95 <=25 ms and standalone p95 <=100 ms over a stated fresh workload. Historical failures stay visible. |
| Work slowdown/calibration | Pressure coordinator owner: collect comparable host/revision/class cohorts; explain wait/CPU/reservation cost and pass replay promotion before changing weights. | Existing p95 slowdown and replay gates pass; no threshold relaxation. |
| App idle and elapsed acceptance | Durable collector above; next agent reads its JSON and independent installed identities. | Same app/resident authority, continuity, 30-minute app CPU <=1% and RSS <=128 MiB; actual 24/72-hour windows pass. |
| Privileged trace | Human: approve the normal macOS helper registration/trace action on a chosen live process; agent inspects the bounded receipt. | Signed same-Team helper, process identity, 5–30 s trace, bounded XPC completion and one final result. |

WORK-41 remains in progress until these gates have evidence. Source tests,
accepted captures, landed commits, installed artifacts and elapsed runtime
acceptance are separate states throughout this ledger.


At 20:23 UTC the fresh complete live audit passes policy, resident, readings,
cleanup and artifacts. Scheduler and telemetry are warnings; performance still
fails. Factory current-hour writer evidence is 401 attempts/401 writes, zero
errors/gaps, p95 histogram bound 50 ms against a 25 ms target. Its full history
still has 119 unresolved attempts. Standalone evidence is 4,728/4,728, zero
errors/gaps, p95 132.228 ms against 100 ms. The recent scheduler window has no
evaluated slowdown breach, but test/build cohorts have only 16/18 samples, below
the required 20; full history remains breached. These partial improvements do
not satisfy the outstanding performance gates. Evidence: `audit-2024.json` and
`factory-install-recheck-2024.json`.

The new collector's first 100 reads per endpoint all pass with zero errors:
status p95 95.726 ms /250 ms, board 76.181 ms /500 ms, rich 344.815 ms /1,000 ms.
Its first resident check passes with PID 90605; app PID 56895 starts observation
at 85.594 MiB RSS. First checks are not elapsed acceptance. The live artifact
read is `artifact status` (singular); an accidental `artifacts` invocation was
rejected without mutation. The failure response lists the valid singular
command. This misuse is retained in telemetry and is not a runtime defect.


### Concurrent forward release boundary (20:25 UTC)

The publisher linked the 053 schema-v4 receipt above; this closes its installed
source-closure evidence. A concurrent owner is now publishing committed
`92eb891`, which adds typed `ndev-work-refused/v1` refusal results and remote
rejection exit 11. The factory publisher owns the matching native rebuild and
canonical digest. This is beneficial delivery-plane work and must not be
replaced with 053 merely to preserve a soak window. The 053 collector is an
honest current-generation observation, not acceptance of the future release.
If this upgrade changes CLI revision or resident PID, preserve the failed or
interrupted 053 JSON. After coherent 92 identities and at least four fresh normal
samples pass, start a separate unique collector via the same launch-agent
contract. The runtime owners have this exact handoff on the session board;
WORK-41 remains open. Do not count the elapsed portions across upgrades.


### Delivery state change at 20:26:49 UTC

The final status changed to attention: parent
`64c635e4-3fae-4254-bcd6-24e913bbaba0` is paused with
`PRODUCER_BUILD_FAILED`. Its exact error is the host placement refusal:
“remote Go work blocked: GOFLAGS needs an explicit runner profile; use the
Hetzner gate-runner with an explicit compiler profile; local fallback is disabled.”
Child `2aca8f87-72ad-4d63-95cb-58f5a7248b90` is held with
`STACK_PARENT_PENDING`. The other three deliveries remain accepted. The worker
is healthy and busy. Earlier “five accepted” checkpoints are historical.

The native producer/refusal-contract owners must finish the placement repair,
then run the parent's printed recovery command:
`ndev-commit-delivery --json repo commit delivery drain --id 64c635e4-3fae-4254-bcd6-24e913bbaba0`.
Act on the parent; the child resumes through its normal stack dependency. Do not
recapture either change, use local heavy Go fallback, or retry the same unfixed
host rejection. The board contains this exact owner handoff. The retained
15-minute wait is still running (tool session 13340, private
`delivery-wait-2022.json`); it does not change queue state.


### Final live sampling correction and diagnostic improvement

At 20:31 UTC the current observation had ten product checks, zero failures and
all warm latency gates passing. It temporarily reported app PID zero at checks
8 and 9, then rediscovered the same PID 56895. Independent app authority is
still `d27fe84fb27eda1c95ccc3c7d290393e27427cc50be3a26d95b8152014452876`
and `ps` confirms the same running process. A peer's `2f71e71f` is the app
executable hash, not a replacement bundle authority. The earlier board inference
of a replacement is corrected. One-shot launchd `pgrep` and `ps` probes both
succeeded. The exact reason for the old collector's transient failure is unknown;
it discarded errors. No valid 30-minute app window has been established.

The release observer now records bounded, content-free failure codes and failure
times/counts for process discovery, ambiguous or invalid PIDs, process reads and
invalid CPU/RSS. Failed samples clear current CPU/RSS instead of retaining stale
values. The app idle window restarts after any missing sample. Product health
and app sampling remain separate. BSD CPU parsing accepts day-prefixed times
and rejects negative, nonfinite and malformed metrics. These changes require
remote race verification, a committed native release and coherent promotion;
they are not retroactive evidence for the existing observer.

Delivery `be3a3593-6e93-482c-8901-1b9c4289e446` landed as `598a33c1` at
20:28 UTC. It closes the guarded owned-index isolation change. Current totals:
three landed, two accepted, one held, one paused. The pending paths no longer
include the guarded helper and its reference. Factory service reports healthy
and busy, with source lag findings; do not restart the active worker.


### Parent recovery after native-profile activation

At 20:38 UTC the current worker is 2.33.1 and its owner confirms
`config_current`. A live sealed native producer is running through the runner
with an explicit clang-18 compiler, SDK digest and external-link profile. This
is evidence of the placement repair that the earlier refused attempt lacked.
I therefore ran the printed parent drain once. It returned `processed:0`,
`deferred:1`, `deferred_reason:QUEUED_FOR_SERVICE` for `64c635e4`.
This is a requeued recovery request, not proof of landing. The retained owned
wait continues; child `2aca8f87` stays attached. Do not duplicate the captures
or repeat the drain while the service owns this attempt.


The diagnostic change passed its targeted race suite on the canonical runner
(1.142 seconds) and landed as `1c12a2044ade8a9dd2c3ebdff7e3548b92a839c2`.
An exact committed-source native observer build is now queued on the runner.
It will be used from an immutable acceptance path against the installed CLI;
this does not replace the managed product or interfere with the sole forward
92 promoter (`01a1bc3b`). Source and helper identity will be recorded separately.
The previous retained factory wait timed out after 15 minutes with guarded
helper delivery landed and two accepted deliveries still queued. Parent recovery
is deferred to the busy service, so its durable state can remain paused until
processed. A single replacement owned wait is retained in
`delivery-wait-2041.json`; no duplicate concurrent waiter is running.


### Native diagnostic helper and current durable observer (20:46 UTC)

The exact committed `1c12a204` helper passed its release race suite again and
built for Darwin/arm64 with CGO enabled from clean source on Hetzner. Its native
SHA-256 is `106595090dff7194b88b126a5b985dbce38f6104af0747a9a519da032aa088d5`;
`go version -m` confirms exact VCS revision and `vcs.modified=false`.
The native helper's real `app-status --app` independently confirms the current
1.4.0 build 7 full-bundle authority remains `d27fe84f`.

At 20:46:33 UTC I stopped only our older launch agent, preserved its JSON and a
separate end receipt, and started one new normal-priority, private launch agent:
`com.nicos.session-pressure.acceptance-20261108T204633Z`. It runs immutable
`acceptance/2026-11-08/pressure-observer-1c12a204`, not the mutable managed helper
link. Its output is `observation-0535927f-20261108T204633Z.json`; the private
launch receipt is `observation-launch-2046.json`. No product or app was replaced,
and no old failure record or elapsed window was reset. The current managed
product remains 0535927f. App and CLI source identities are recorded separately
from the observer's source identity.

The runtime owner is now waiting for `72754fd6`'s approved native build-route
repair before one coherent product/factory promotion. The pending source covers
`sessionpressure/remote_profile.go`, `remote_work.go`, `remote_work_eligibility.go`
and their tests. Ready 92 artifacts are retained as test evidence. After the
final coherent promotion and fresh normal monitor samples, use the immutable
helper above with a unique output and label. Preserve this 053 observation,
which will report a revision-change failure if the runtime changes beneath it.
Do not combine its elapsed portion with the final release's acceptance window.


The new helper's first live 100-sample warm reads all pass with zero read errors:
status p95 53.473 ms/250, board 50.608 ms/500, rich 174.295 ms/1,000.
Its first resident check passes with PID 90605 and no restarts. The native app
sample is valid, PID 56895, RSS 58.3125 MiB, zero sampling failures. These are
fresh short-window results, not a causal speedup claim or a 30-minute/24/72-hour
verdict. Its actual deadline is 2026-11-11T20:46:33.241831Z, conditional on no
interruption. The forthcoming runtime upgrade requires a new generation.

The final compact audit at 20:47:37 UTC (`audit-2047.json`) still returns fail,
correctly preserving the writer performance gate. Policy, resident, readings,
cleanup and artifacts pass. Scheduler warns about insufficient recent cohorts,
partial pressure-conditioned evidence and historical slowdown/long waits/expiry.
Telemetry retains 116 two-hour prior-controller gaps and historical restart
churn, while the verified activation-hour authority is clean across 771 attempts
and the current resident has one start and zero stops. Performance reports CLI
writer p95 100,000 us against the 25,000 us target and standalone writer p95
130,572 us against the 100,000 us ceiling. WORK-41 remains open for this evidence;
do not turn these failures green by removing history or weakening the limits.


### Writer latency window verification (20:52 UTC)

Read-only aggregation of the native product writer health shards confirms that
the audit uses a two-hour request rounded down to whole UTC hour buckets, not an
exact release cohort. At 20:52 this includes 18:00, 19:00 and 20:00. Standalone
writer p95 is 169,003 us in hour 18, 64,451 us in hour 19 and 52,400 us in hour
20 (1,214 successful writes, zero failures in the last reported hour). Thus the
current hour is below the 100,000 us failure ceiling but still above the
25,000 us target. This does not establish causality or make the two-hour failure
false. It also cannot prove an exact post-install percentile because the
bounded authority stores hour buckets, not revision-tagged individual timings.
The private receipt is `writer-hour-cohorts-2052.json`. The next accepted runtime
needs its own natural load observations; preserve the existing failure and all
historical write gaps. No limit was raised and no telemetry was removed.


### Native app idle CPU follow-through (20:55 UTC)

The corrected observer exposed valid app CPU above the 1% idle target: 2.566%
over 482 seconds, with PID 56895 unchanged, zero sampling failures and peak RSS
58.3125 MiB. This is an early observation, not a completed 30-minute verdict.
A private five-second `sample` of that exact PID succeeded without privilege.
It shows most time waiting in the main event loop, with repeated AppKit layout
and SwiftUI dynamic relative-time text work. This identifies a plausible cost,
not a measured causal allocation of all CPU. The raw private evidence is
`app-sample-2053.txt`.

All three native age labels now use shared `ReadAgeView` with a 15-second
TimelineView cadence and explicit string formatting, instead of SwiftUI's
per-second dynamic relative text. Dates and read freshness remain visible.
The candidate is 1.4.1 build 8. Only the owned ContentView toolbar hunk is staged;
the peer Hetzner navigation hunk remains unclaimed. Existing unrelated app code
is preserved in the combined verification snapshot.

The first verification attempt copied an old Swift build cache, which failed
because its PCH embeds the old absolute module-cache path. Only this run's
redundant generated copy was removed; the original evidence was preserved.
A clean, serial `make verify` is running with the real installed CLI contract.
Do not claim the candidate is installed or the CPU target passed until the
separate installation and elapsed observation receipts exist.


### Final app installation and durable generation (20:59 UTC)

The cadence change landed in `a9b34806bee74ffd5827a9e9ce0d89a36d02dba3`.
Live review caught a clock edge when a new read timestamp was newer than the
TimelineView tick. `22595afa33540630b1e1846faec62ef03fe66567` uses the current
clock at rendering and explicitly shows “just now” for a fresh read. All 68
tests in nine suites passed again, including the real CLI/Unix API contract,
followed by a clean release build and strict Developer ID verification.

The final 1.4.1 build 9 was installed at 20:59:19 UTC using expected full-bundle
authority `e3b19974`; the resulting full-bundle digest is
`c827a2d602b4b9246aa5aae640c8d9895bb2fff9a4886800355de809db808715`.
The previous bundle has its own rollback path; stale/downgrade guards remain
enforced. `desktop-idle-final-source-2058.json` hashes the combined source,
`desktop-idle-final-verify-2058.log` records verification, and
`desktop-idle-final-install-2059.json` records compare-and-swap and provenance.
The peer Hetzner source remains unclaimed. CUA independently confirmed all
twelve panes, “just now”, then Day Gate's correct date, gate-none result and
5-second successful-read age. No app idle CPU improvement is claimed yet.

Only one acceptance launch agent is active:
`com.nicos.session-pressure.acceptance-20261108T205919Z`. It uses the immutable
`1c12a204` native helper and a new private output. Its deadline is
2026-11-11T20:59:19.372977Z, conditional on continued same-generation operation.
The previous 20:46 observation and end receipt are preserved; nothing was
reset or combined. The next product revision needs a separate generation.
The current status warm cohort misses its 250 ms p95 budget at 266.788 ms
during an elevated host load. Keep this result visible and collect naturally
after the final native profile promotion; do not substitute the earlier
passing 53 ms result for this window.


The final generation completed all 100-sample read cohorts at 20:59:59 UTC:
status p95 266.788 ms/250 fails; board 187.389 ms/500 and rich
302.615 ms/1,000 pass, with zero read errors across all 300 calls. Its first
resident check passes, PID 90605, zero restarts. App sampling is valid for
new PID 67620, RSS 114.219 MiB, zero sampling failures; CPU and idle gates have
no elapsed verdict yet. This preserves the loaded-host failure as evidence.

At 21:00 the owned delivery doctor returns structurally OK but warns about
two overdue accepted deliveries, paused parent, blocked child, an uncommitted
foreign policy and installed worker/preflight source lag. “OK” is not clean
acceptance. Service configuration is current and the worker remains busy, so
its owner must complete the safe forward refresh after the active operation.

A bounded Scratchpad addendum was written with the nicos-scratchpad skill and
read back through FTS with its literal backtick phrase intact. Entity
`01M6YFCYF43H7AQBNJ4ZYE7YVA` supersedes the earlier install and queue counts.
Self-evaluation remains 7/10 until the recorded delivery, latency, scheduler,
privileged trace and elapsed gates close. Improvements applied in this run:
retain sampling errors instead of stale app metrics, bind observers to immutable
helpers, keep generations separate, serialize native builds, avoid relocated
Swift caches, and replace per-second time labels with bounded human-scale refresh.


### Final gate refusal and first post-fix minute (21:01 UTC)

The new app's first valid 60.975-second CPU interval is 0.2624%, below the 1%
target; peak RSS remains 114.219 MiB/128. Both samples have valid app evidence,
zero sampling failures, resident PID 90605, zero restarts and passing live
health. This early directional result is not a 30-minute acceptance or a
controlled causal comparison with the previous app. The immutable collector
continues to accumulate the required interval.

The final installed `teammate finish` check refuses closure because delivery
`64c635e4-3fae-4254-bcd6-24e913bbaba0` is still paused. This was not overridden.
All seven residuals use valid agent/human owners and explicitly name pending
deliveries, nine factory paths, peer app paths, forward runtime owner, performance
and elapsed gates, and administrator trace. The private
`finish-final-command-result.json` retains the refusal; no successful final
finish receipt is claimed. WORK-41 stays in progress. The earlier score is
a self-review measure, not proof of terminal acceptance.


## Review follow-up at 22:07 UTC

At 22:12 UTC, a further review removed hand-maintained Go package lists from
the root Makefile and CI. `vet`, `test` and the new `race` target cover the
whole module, including release scripts and all new internal packages. Both
CI platforms run `make vet`, `make race` and `make build`; builds now include
the release installer/observer and compiler adapter. `make -n` and
`git diff --check` pass. This is a gate coverage change, not a new native
runtime candidate or a claim that GitHub CI has executed.

- Guarded commit `921c26785b8edaa3b335f8a458effa2b511d2f9d` adds closed
  `outcome_class` and `reason_code` fields to standalone command events.
  A completed audit finding is `expected_negative/gate_not_satisfied` only
  after report output succeeds. Input, policy, timeout, cancellation and
  command failures retain distinct classes. Encoding and output errors remain
  command failures. Wrapper suppression and privacy checks are unchanged.
- The exact six-path candidate is frozen under
  `review-outcomes-source-2205`, based on `c8afd904`. Its manifest hashes all
  owned files; targeted telemetry/dispatcher/CLI race tests use one retained
  canonical remote handle. Source publication is not a passing test receipt.
- Read-only primary-stream reconciliation: 6,238 product command events,
  373 nonzero, zero malformed. The source-specific failure query groups them
  as 373 unknown outcomes. Most are status (303), with 16 audits; no cause
  is inferred for historical exit-1 rows. The detailed counts and private
  capture names are recorded in the review report.
- Native8c packaging failed after its passing race suite on malformed
  `GOFLAGS`; c8 release guidance fixes complete-token quoting. The promoter
  retains one c8 native build, separate from the later telemetry correction.
- At 22:06, installed `doctor` remains healthy: matching `0535927f` CLI/helper,
  PID 90605, normal host, no warnings, 19.203 MiB RSS, 0.346% normal CPU,
  sample CPU p95 17.412 ms and 549,408 projected telemetry bytes/day.
- At 22:04, the original app observer has 65 passing health checks, no
  resident/app restarts or sample failures, and 3,864.6 observed app seconds
  at 0.275% CPU, 114.219 MiB peak RSS. Its status read gate remains failed;
  no elapsed or performance evidence is transferred to a new artifact pair.
