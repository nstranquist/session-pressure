# Browser control: resident CPU cost and enforcement gate

Owner: claude, session e45120b4.
Status: browser-control saving installed 2026-11-17; idle budget still exceeded, see Correction. Work: WORK-1141 (follow-up of WORK-1065).

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

## Correction after install (2026-11-17 18:30Z)

The change above landed as c2c2594 and was installed at 16:59Z
(`make promote-session-pressure`; CLI, front door and helper agree). Live
idle duty did not fall: `ps` CPU time of the helper over 30 minutes was 0.30%,
and the resident's own window read 0.31-0.36%. The per-evaluation saving is
real but was not the main cause.

The 11:39Z step is a cadence change. `recoverySampleDelay` caps the normal
90 s sample interval at 15 s whenever browser control is in `auto` mode, so
browser admission always has resident CPU evidence younger than 30 s. The host
opted in at about 11:35Z, so the resident went from about 40 to 240 samples
per hour at level normal. Duty is CPU per wall second, so it rose with the
sample rate. The 15 s cap protects a real freshness contract
(`Waiting for fresh resident CPU evidence` blocks browsers past 30 s), and
browser sessions start about once a minute on this host, so a demand-only
cadence would rarely leave 15 s.

Measured costs per 15 s sample on this host:

| Item | Cost | Duty at 15 s |
| --- | --- | --- |
| whole sample (`sample_cpu_time_avg_ms`) | 8.3 ms | 0.055% |
| automation-browser scan (1,639 processes, 29 browser-named) | 1.4 ms | 0.009% |
| 24 h wrapper-interrupt recount, per 5 min heartbeat | about 100 ms | 0.03% |
| interval CPU (`ps`) | about 45 ms | 0.30% |

A memo of the inventory-derived views (trees, consumers, families) was tried
and dropped: an A/B over 15 minutes on two identical data copies gave 0.187%
without and 0.188% with it.

The harness on a data copy reads about 0.19%, the live resident about 0.30%.
The difference comes from live-only inputs that a copy cannot reproduce (lease
and notify churn from agents, real disk writes for the disk-write observer,
browser enforcement). A copy cannot run the real helper safely, because the
helper signals live browsers.

## In-place CPU profile

The helper takes one bounded CPU profile of itself on `SIGUSR2`
(`internal/helpermain/cpu_profile.go`): 10 minutes, written to
`<data dir>/profiles/cpu-<UTC time>.pprof` with mode 0600. Nothing runs until
the signal, and a second signal during a profile is ignored. Signal only a
resident whose `source_revisions.installed_helper` includes this change: an
older helper has no handler, and the default action of `SIGUSR2` ends it.

## Open

- Live duty is 0.30% against 0.25%. Next: profile the installed resident in
  place (`kill -USR2`), then fix the largest live-only cost it shows.
- `automatedBrowserProcesses` still scans every process with arguments on each
  sample. Reusing the sampler's process list would remove that scan.
- `attachWrapperInterruptToSummary` re-parses the day's work-event shards on
  every heartbeat (about 6% of the live-data profile, growing with the day's
  work volume). `work-events.sqlite` already indexes those rows.
