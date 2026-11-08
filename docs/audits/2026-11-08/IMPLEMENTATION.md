# Session Pressure 0.7.0 implementation and acceptance ledger

Owner: Codex, session `01a1bbc7-3cf2-72b0-b843-563bca14e3dd`.
Central work item: WORK-41 (`e6011b2df33955484962f546b6767e02`).
Baseline source: `c136a8f415595b6e9b8f4ee01c85315aef75de80`.

## Product contract

The SwiftUI app is the primary desktop interface. The GPUI app is a tested preview.
Admission protection is the default operating mode. Automatic agent termination stays off.
Storage admission applies to growing work: warn at 50 GiB, red at 25 GiB, critical at 10 GiB;
release thresholds are 60/30/20 GiB. Cleanup requires explicit confirmation.
Remote work uses existing Hetzner jobs and receipts. This program does not add a remote scheduler.

## Acceptance checklist

| Area | Required result | State |
| --- | --- | --- |
| Installation identity | One canonical installed product CLI; capabilities expose version, revision and executable; promotion verifies committed source and preserves rollback | In progress |
| Readiness | Admission-only mode can be ready; explicit policy, storage and monitor faults remain visible | In progress |
| Telemetry | Real writer and health reader, privacy validation, correlated exact-once product/factory commands, outcome classes, denominators and freshness; historical errors retained | Pending |
| Retention | Bounded retention for every owned stream; maintenance failures are visible | Pending |
| Work coordinator | Host attribution and wait causes; measured safe reservation calibration; verified cache hits and miss reasons; existing performance thresholds retained | Pending |
| Native desktop | All nine sections refresh on entry; bounded/coalesced requests; per-section freshness and errors; identity, SMART and write units accurate | Pending |
| GPUI preview | Executable validation, bounded output and process cleanup, timeouts, explicit nonzero outcomes and tested supported operation matrix | In progress |
| Policy | One authority for admission, manual cleanup and storage admission; no automatic agent termination | Pending |
| Go cache helper | Immutable atomic objects, cross-process pinning until close, crash recovery, bounded streaming, strict sizes, bounded maintenance and soft limit while pinned | Pending |
| Local API | Standalone product default, capabilities, native Unix socket transport, bounded response streams, authorization and approval deadlines | In progress |
| Privileged trace | Signed local package, independent wall timeout, XPC timeout and cleanup; OS approval state reported honestly | Pending |
| Validation | Remote candidate Go tests/race/benchmarks, fresh Swift/GPUI tests, real scrubbed fixtures, all-pane UI review, cache protocol and actual Go integration | Pending |
| Delivery | Guarded commits, installed revision receipts, rollback proof, 24/72-hour observation recorded separately from immediate checks | Pending |

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

Implementation started. No new installation or soak result has been established.
