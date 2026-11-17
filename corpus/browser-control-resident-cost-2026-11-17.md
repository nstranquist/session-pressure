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

## Live profile and the disk-write fix (2026-11-17 18:40Z)

A 600s `SIGUSR2` profile of the installed resident (e5be021) read 1910ms of
CPU, 0.32%. The largest costs:

| Cost | Share | Cadence |
| --- | --- | --- |
| Disk-write observer, per-process pass (`pid_rusage` on about 1,640 pids) | 26% | every 15s disk tick |
| Pressure sample | 13.6% | sampler cadence |
| Wrapper-interrupt recount (`attachWrapperInterruptToSummary`) | 9.4% | each heartbeat, grows through the day |
| Browser enforcement | 7.3% | 15s in browser auto mode |
| GC and scheduler | most of the rest | |

The disk-write observer runs on its own 15s ticker, apart from the browser
cadence, and was the largest single cost. Fix: the device and swap counters
stay on every tick, and the per-process attribution pass runs once a minute
(`diskWriteProcessPassInterval`). The pass also runs on a tick that writes a
whole burst (1 GiB), after a device error, and on the first tick. The disk
state is not a trigger: since the baseline learned, every hour on this Mac
has ended `high` (about 180 GB written an hour), so a state trigger put the
pass back on every tick. The first install (e196c04) had that trigger and
measured no saving. The pass writer point covers the whole
span since the previous pass, so no bytes are lost, and swap bytes from the
skipped ticks go into it. A gap on any tick since the previous pass rebases the
process counters. Callers other than the resident `Run` loop (the live report,
tests) keep a pass on every tick. Tests:
`sessionpressure/disk_write_process_pass_test.go`.

Cost: writer rates and names can be up to a minute old while writes are quiet.
The 15-minute window and the burst check use the device counter and do not
change.

## Second profile and two more fixes (2026-11-17 19:10Z)

A second 600s profile, of 5e3346e (disk fix with the state trigger), read
0.53%. The host was busy in that window (level warning, host CPU 92%, disk
state high), so the sample cadence was higher than when the host is idle. The costs:

| Cost | CPU in 600s |
| --- | --- |
| Pressure sample | 0.71s |
| Browser enforcement | 0.62s (process scan 0.36s, control record write while blocked 0.26s) |
| Disk-write observer | 0.51s (the state trigger kept the pass on every tick) |
| GC mark | 0.35s |
| Wrapper-interrupt recount | 0.22s |

Fixes in the same delivery as the corrected disk-write trigger:

- Browser scan (`browser_process_darwin.go`): the result of the argv read is
  kept per process (pid, kernel start time, command name) until the process
  exits, and `os.Getuid` is read once per scan, not once per row. A benchmark
  on the live process table went from about 3.4 ms to 1.4 ms per scan.
- Wrapper-interrupt count (`workevent_interrupts.go`): the resident keeps the
  parse state and reads only complete lines appended since the previous
  heartbeat. It resets when the window's first day changes, when a shard is
  replaced or shrinks, when the shard list changes other than by new names at
  the end, and after any error. So its result is always the result of a fresh
  pass. On a copy of the live shards (24 MB, 18,251 rows) fed in 20 steps,
  every step matched a fresh pass and the final count matched the old code
  (523). All 20 incremental counts took 108 to 147 ms in total. The old code
  took 136 to 161 ms for each count. Event IDs are kept as 64-bit FNV hashes.

## Open

- Measure live duty after this delivery is installed, at level normal,
  against 0.25%.
- The control record is written on every sample while browsers are blocked
  (`LastSample` advances). That is pressure-time cost, not idle cost.
- The pressure sample itself is now the largest cost.
- `automatedBrowserProcesses` still scans every process with arguments on each
  sample. Reusing the sampler's process list would remove that scan.
- `attachWrapperInterruptToSummary` re-parses the day's work-event shards on
  every heartbeat (about 6% of the live-data profile, growing with the day's
  work volume). `work-events.sqlite` already indexes those rows.
