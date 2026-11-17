# Browser control: resident CPU cost and enforcement gate

Owner: claude, session e45120b4.
Status: implemented and tested, 2026-11-17. Work: WORK-1141 (follow-up of WORK-1065).

## Problem

The resident guard's idle CPU duty (`guard_idle_cpu_duty_percent`) rose from
about 0.09% early on 2026-11-17 to 0.30-0.37% later that day. The budget is
`resource_budgets.max_idle_cpu_percent = 0.25`. Doctor reported
`monitor_healthy=false` and `operator_ready=false` from this alone.

The step came at 11:39Z with only two agent trees and no install. The first
`browser_stopped` control event of the day was at 11:35Z, when the host opted in
to browser control (`browser-control.json`, mode `auto`).

## Evidence

A scratch harness ran `Monitor.Run` with a pprof CPU profile against private
data directories (not part of this repository):

| Run (Mac, nice 10) | Duty |
| --- | --- |
| empty data dir, disk-write observer on | 0.131% |
| empty data dir, disk-write observer off | 0.079% |
| clone of the live data dir (browser control opted in) | 0.272% |

The live-data profile: `EnforceBrowserControl` was 24% of resident CPU. Of that,
`BrowserControlStatus` took the control file lock, re-read `latest.json`,
re-loaded and validated `policy.json`, and wrote the record with
`atomicWrite` on every sample. `automatedBrowserProcesses` (a second
full process scan with arguments) took the rest.

Root cause of the write: `LastSample` advanced on every sample that was 5 s
newer, also in steady unblocked state. So `state != previous` was always true,
and every resident sample rewrote an identical record.

## Decisions

1. `LastSample` advances only while browser work is blocked. It only spaces
   recovery samples, so steady unblocked state no longer changes the record.
2. The resident passes its in-memory snapshot and policy
   (`browserControlEvidence`) instead of re-reading them from disk. CLI callers
   (`BrowserControlStatus`, `EnforceBrowserControl`) keep the disk path.
3. The resident path evaluates without the lock first. Writers replace the
   record by atomic rename, so an unlocked read is never torn. It takes the
   lock and re-evaluates only when the record must change.
4. `Monitor.EnforceBrowsers` (default false) gates browser enforcement. Only
   the resident (`internal/helpermain` `configureMonitor`) and the explicit
   `monitor run` set it. Incident: the profiling harness embedded `Monitor.Run`
   and stopped one live agent browser (an idle-leased sentinel) at 16:32:05Z.
   An embedder must never signal host browsers by default.

## Measurements

Go benchmark, gate runner (linux), real `latest.json` and `policy.json`, per
resident evaluation:

| Path | ns/op | B/op | allocs/op |
| --- | --- | --- | --- |
| old resident (disk reads, lock, rewrite) | about 730,000 | 59,900 | 237 |
| disk reads, no rewrite | about 550,000 | 58,200 | 222 |
| new resident evidence, unchanged record | about 31,000 | 4,440 | 41 |

The Mac saves more than this, because lock files and the fsync'd rename cost
more there.

Tests: `TestBrowserControlSteadyStateDoesNotRewrite`,
`TestBrowserControlResidentEvidenceSkipsDisk`,
`TestBrowserControlSteadyResidentPathTakesNoLock` (each fails on the old code)
and `TestNewMonitorDoesNotEnforceBrowsersByDefault`, plus the resident wiring
check in `internal/helpermain`.

## Open

- Installed resident duty after this change: not yet measured. Measure
  `guard_idle_cpu_duty_percent` for 24 h at host level normal after install.
- `automatedBrowserProcesses` still scans every process with arguments on each
  sample. Reusing the sampler's process list would remove that scan.
- `attachWrapperInterruptToSummary` re-parses the day's work-event shards on
  every heartbeat (about 6% of the live-data profile, growing with the day's
  work volume). `work-events.sqlite` already indexes those rows.
