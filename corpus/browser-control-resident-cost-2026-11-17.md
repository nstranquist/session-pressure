# Browser control: resident CPU cost and enforcement gate

Owner: claude, session e45120b4.
Status: idle budget met (about 0.12% against 0.25%, no profile running); WORK-1315, WORK-1354 and WORK-1419 closed (2026-11-18).

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

## Third profile and the memory fix (2026-11-17 19:45Z)

A 600s profile of 56f36b8 read 1510ms, 0.25%. The level was normal in 19 of
21 checks, but the host was busy (host CPU 52 to 99%, disk state high). Costs: pressure
sample 0.35s, disk-write observer 0.19s (was 0.51s), browser enforcement
0.17s (was 0.62s), GC mark 0.16s, memory-family classification 0.12s. The
wrapper-interrupt count no longer shows in the top 80 nodes.

RSS went from 47 MB to 51 MB (budget 44 MB). The incremental counter kept
10.4 MB of validator state for 5,166 operations, about 2 KB each, mostly a
full copy of each terminal event. The shared lifecycle validator is now
compact: identities are 64-bit FNV hashes, enum strings are the package
constants, seen types are a bit set, and the terminal event keeps only the
fields the reconciliation rule reads. Retained state on the same data: 1.9 MB,
same count (523). The rule logic does not change. A hash collision could
hide a changed lease or digest only with a probability of about 2^-64 per
pair.

## Result (2026-11-17 20:53Z, installed 6c67fc0)

| Revision | Profile duty (600s) | Guard idle duty | RSS peak |
| --- | --- | --- | --- |
| e5be021 (before) | 0.32% | 0.30% | 47 MB |
| 5e3346e (disk fix with state trigger) | 0.53% (busy window) | 0.65% | 47 MB |
| 56f36b8 / d35eade | 0.25% | 0.232% | 51 to 54 MB |
| 6c67fc0 | 0.21% | 0.194% | 33 MB |

The host was busy in every window (host CPU 40 to 99%, disk state high), so
these are upper bounds for an idle host. The guard figure is its own
64-sample window at level normal, read after startup and the profile had
left the window. Readings at 20:35Z, 20:44Z and 20:53Z: 0.261%, 0.228%,
0.194%.

In the 6c67fc0 profile the disk-write per-process pass takes 0.13s in 600s,
about 10 passes at 13 ms (it was about 40 passes).

## WORK-1315: remaining idle costs (2026-11-17 22:10Z, installed fe40179)

Baseline: a 10-minute in-place profile of 6c67fc0 (21:25Z) took 1.00 s of
CPU (0.17%). Runtime thread wakeups were about 300 ms, `Sampler.Sample`
160 ms, the disk observer 120 ms, browser enforcement 80 ms, GC 80 ms.

Changes in fe40179:

- `proc_pid_rusage` is skipped for rows of another effective uid. A live
  probe showed 0 of 211 such rows readable for a non-root caller, so the
  skip changes no result. Accessible PIDs after install: 758 of 974, the same
  split as before. A darwin test checks that every skipped row is refused.
  It passed on the Mac on 2026-11-18 after the native-test pause was lifted.
- Browser enforcement reads `kern.proc.uid`. The probe was mixed: rounds of
  93, 101 and 91 us a call against 108, 98 and 111 us for `kern.proc.all`.
  Treat it as about equal. The change is for a smaller result set, not a
  proven speed gain.
- Disk observer: writer totals are cached by a generation that moves only on
  a process pass or prune. Point slices are pruned in place.
- Identity catalog: the overlay is stat'ed at most every 5 s and the catalog
  recompiles only when the overlay changed. It used to stat on every lookup
  and recompile every 5 s. Review fix (2026-11-17): a catalog that holds an
  overlay error recompiles at every check, so a transient read error does not
  stick, and the fingerprint includes the inode, so an atomic replace with the
  same size and mtime is seen.
- `privacySafeExecutable` returns already safe names without a rune slice.
- Sampler (option 2): the resident samples every 15 s but refreshes its
  inventory every 180 s. Trees, host consumers and memory families are now
  reused while the enriched inventory and the catalog are unchanged. Top
  trees are cloned before the per-sample semantic-state pass.

Result, steady-state profile 22:00Z: 0.89 s in 600 s (0.15%), against 1.00 s.
`Sampler.Sample` 160 to 40 ms, disk observer 120 to 100 ms, GC 80 to 60 ms,
writer aggregation gone. Browser enforcement read 80 to 120 ms, within the
profile's 10 ms resolution and host load; the probe above is the cleaner
measure. A first profile right after install (21:49Z) read 1.05 s because it
held one-time startup work, mainly the first full read of the day's 31 MB
work-event shard by the interrupt counter (90 ms).

Tried and reverted: resident GOMAXPROCS=2. Thread wakeups (`mcall`,
`pthread_cond_signal`, `kevent`) stayed at about 300 ms, so the cap gave no
measured gain and was removed.

Tests: full `go test ./...` passed on the Linux gate runner. The darwin-only tests
were not run then, because native Mac tests were paused (WORK-775). After
the pause was lifted (2026-11-18 02:47Z) `delivery verify --full` ran the
darwin suites on b657c00 and 161a82c and passed. Each new test of WORK-1315,
WORK-1354 and the review fix fails when its fix is removed (seven mutation
checks run on the Mac, 2026-11-18).
The installed resident exercised the darwin paths: inventory from libproc,
disk attribution counts unchanged, browser control running.

## WORK-1354: the runtime wakeups were the profiler (2026-11-18 02:50Z)

The 300 ms of runtime wakeups in each 10-minute profile (`runtime.schedule`,
`wakep`, `kevent`, `pthread_cond_signal`) were mostly the measurement.
`runtime/pprof` runs a writer goroutine that wakes every 100 ms. On this
mostly idle process every wake spins up a second thread through `wakep`,
which then finds nothing, polls `kevent` and parks.

Evidence, installed 4fc2162 (pid 35866), host load about 20-25:

| Window (201 s) | Resident CPU | CPU % | Context switches/s |
| --- | --- | --- | --- |
| CPU profile running | 0.40 s | 0.20% | 75 |
| No profile | 0.24 s | 0.12% | 24 |

- A 30 s execution trace (`go tool trace -d=parsed`) counted wakes per
  goroutine. The two top wakers, 297 wakes each, were `pprof.profileWriter`
  and the trace's own CPU reader. The resident's goroutines (the monitor
  loop and the disk observer) woke about 12 times each in 30 s.
- An idle Go program with a signal handler and a 15 s ticker makes about
  0 context switches a second, so the switch count is a usable signal.
- The 5 s demand poll (`waitForResidentSample`) costs 30-50 ms in 600 s in
  the profiles and adds under 1 switch a second. Making it event-driven
  would add a blocked kqueue thread and a fallback path for about 0.005% CPU.
  Not done, by the WORK-1354 stop rule.
- The resident's real idle cost is about 0.12% at this load. A profile
  overstates it by about 0.08% (about 480 ms per 10-minute window).
  Read `runtime.schedule` in these profiles as mostly profiler cost.
  For a cost number, use CPU-time deltas (`ps -o time=`) or
  `guard_idle_cpu_duty_percent` with no profile running.

Profile tool changes:

- After the profile window, SIGUSR2 also records a 30 s execution trace,
  `profiles/trace-<UTC>.out`. It runs after the window, so profiles stay
  comparable with the ones above.
- A resident stop during a profile now flushes the shorter profile (waiting
  up to 2 s) instead of leaving an empty file. The install restart at about 02:33Z left
  `cpu-20261118T022139Z.pprof` empty before this fix.

## Open

- Runtime wakeups are closed (WORK-1354): they were mostly the profiler.
  The remaining real costs are the process-table reads (sysctl and
  `proc_pid_rusage` syscalls) and the IOKit disk counter.
- Browser enforcement still reads the process table on every sample. Reusing
  the sampler's inventory would remove it, but the inventory can be 180 s
  old, which delays a stop. That trade needs a decision.
- The disk process pass needs every row for its total PID count, so it keeps
  `kern.proc.all`.
- After a restart the interrupt counter reads the whole day's work-event
  shard once. Measured 2026-11-18 03:25Z on a 39.5 MB shard (WORK-1419),
  three rounds each, same count (533):

  | Route | Time | Heap allocated | Process max RSS |
  | --- | --- | --- | --- |
  | JSONL scan (kept) | about 155 ms | 50 MB | 24.8 MB |
  | `work-events.sqlite` read | about 78 ms | 89 MB | 33.1 MB |

  The index would save about 75 ms once per restart and add about 8 MB to
  the guard's RSS, which already peaks at 38 MB against its 44 MB budget.
  It also needs the index to be current, and only other processes keep it
  current. Not done: this confirms the design note in
  `sessionpressure/workevent_index.go` that the resident never loads a
  database engine.
- The control record is written on every sample while browsers are blocked.
  That is pressure-time cost, not idle cost.
