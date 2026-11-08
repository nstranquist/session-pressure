# Session Pressure 0.7.0 implementation and acceptance ledger

Owner: Codex, session `01a1bbc7-3cf2-72b0-b843-563bca14e3dd`.
Central work item: WORK-41 (`e6011b2df33955484962f546b6767e02`).
Baseline source: `c136a8f415595b6e9b8f4ee01c85315aef75de80`.


Current checkpoint (19:05 UTC): the standalone implementation is committed through
`3e5da8f`. The installed unified native release is `cc23fb2`, independently
verified across all five binaries and the real Swift/Unix API contract. Its
resident passes current budgets. The latest control optimization awaits the
concurrent remote owner's combined promotion. Four factory deliveries remain
accepted, not landed. Historical writer/scheduler performance and elapsed
acceptance remain open; WORK-41 stays in progress.

## Product contract

The SwiftUI app is the primary desktop interface. The GPUI app is a tested preview.
Admission protection is the default operating mode. Automatic agent termination stays off.
Storage admission applies to growing work: warn at 50 GiB, red at 25 GiB, critical at 10 GiB;
release thresholds are 60/30/20 GiB. Cleanup requires explicit confirmation.
Remote work uses existing Hetzner jobs and receipts. This program does not add a remote scheduler.

## Acceptance checklist

| Area | Implemented result | Acceptance state |
| --- | --- | --- |
| Installation identity | Capabilities expose product version, revision, executable and platform. Five-binary promotion verifies hashes, clean source, Darwin CGO and identity before switching one authority link; previous authority and first-migration files are preserved. | Installed; release receipts retained. Factory front door update is accepted, not landed. |
| Readiness | Admission-only is a valid ready mode; persisted policy, storage, monitor freshness and resident baseline remain explicit gates. | Installed. A resident restart requires four new normal samples; the 18:16 resident later passed its resource budgets. |
| Telemetry | Real product writer and reader; privacy validation, closed fields, failure classes, attempted/written/failed/gap counters and freshness; bounded sharding and full lock-to-append timing. Factory correlation and product-owned registry fragment implemented. | Product installed; factory integration queued. Historical gaps and factory latency failures remain visible. |
| Retention | Owned JSONL, writer health, disk maintenance and monitor streams have bounded retention; pruning and append failures are reported. Helper artifacts retain active plus two rollback revisions. | Implemented and tested. Unique legacy install backups are preserved. |
| Work coordinator | Private host identity and OS/architecture/CPU/revision cohorts, explicit legacy counts and reuse miss reasons. Mixed-cohort retunes fail closed. Current CPU/fairness/capacity gates remain in force. | Installed. Replay promotion and live slowdown SLO still fail; no unsupported weight increase applied. |
| Native desktop | Ten sections, including concurrent WORK-82 Process Incidents, refresh on entry; requests coalesce and have time/output bounds; per-section age/errors retain prior results. Settings show executing identity. SMART age and native write-window units are correct. | 59 tests pass; signed app installed and all ten sections inspected. Idle budget is an elapsed gate. |
| GPUI preview | Executable validation, bounded stdout/stderr, nonblocking reads, process-group cleanup, timeouts and explicit failed envelopes; supported-operation matrix documented. | Ten core tests and full Rust build pass; remains a preview. |
| Policy | Exact legacy defaults migrate idempotently; custom settings stay intact. Admission and storage enforcement enabled, automatic shedding disabled. Cleanup remains confirmed and manual. | Installed and live policy audit passes. |
| Go cache helper | Immutable content-addressed objects, cross-process pinning until close, crash recovery, strict sizes and bounded streaming/journal/maintenance. Pinned objects can temporarily exceed the soft limit. | Race tests and real Go cache integration pass on runner. Live reuse history still reports zero hits and concrete miss reasons. |
| Local API | Product owns CLI/Unix API contracts. Unix socket permissions/peer UID, request deadlines and response bounds; native client has CLI fallback and loopback redirect limits. | Real installed Swift/Unix-API contract passes, including capabilities, SMART and live query. |
| Privileged trace | Signed helper verifies Team ID; administrator authorization, PID identity, XPC completion and independent trace wall time are bounded. Restricted entitlement without provisioning profile removed after a real launch failure. | Signed app launches. Administrator-approved trace is not exercised by this audit. |
| Validation | Entire Go race suite, targeted boundary/race tests, fresh Swift tests, GPUI tests/build, real installed API contract, native SMART read and UI review. | Immediate checks pass. Warm latency, idle and 24/72-hour evidence are recorded independently below. |
| Delivery | Product changes published with guarded exact-path commits. Four factory deliveries accepted. Clean native bundle built on Hetzner and promoted with receipts. | Factory landing/install and long observation remain open. |

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
| `aa7b8fa9-4821-45c1-874b-85265d691db0` | Canonical standalone binary priority, trace context, exact-once tests, product registry fragment and owner | Accepted 17:08 UTC, not landed |
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

### Remaining gates and ownership

| Gate | Owner / next action | Acceptance |
| --- | --- | --- |
| Factory delivery + install | Agent: retain delivery wait, inspect terminal receipts, export committed native candidate and use atomic publisher | Four deliveries landed; installed wrapper/registry/trace contract and source digest receipt match |
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
