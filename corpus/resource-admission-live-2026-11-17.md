# Resource admission goes live, unit cap removed (2026-11-17)

Status: 2026-11-17, landed (669bc82), installed and live at 14:18Z. 24 h report owed 2026-11-18.
Owner: claude/5e967f73-34c6-46d6-b36f-b097ff336e18 (takes over WORK-1065
from claude/bb3c2dab under WORK-3, owner codex). Work: WORK-1125, WORK-1065.
Contract: [KEP](../docs/active/resource-admission/resource-admission-kep.md).
Follows [resource-headroom shadow](resource-headroom-shadow-2026-11-17.md)
and [memory guards switch](memory-guards-switch-2026-11-17.md).

## Problem

Nico, 2026-11-17: "i want EVERYTHING to be resource-based, not stupid
hard-cap based." On the same day the unit cap went 12, 20, then 1000 by
hand. At 1000 with `memory_admission` unset and `enforce_admission` false,
nothing guarded memory. At 12 to 20, an idle host made work wait: 24 h at
cap 12 to 20 had 2,087 operations, 152 long waits, 23 wait timeouts, 0
memory deferrals and 0 swap-thrash episodes, while free memory was at p50
75% (min 46%) and swap stayed at 0.

## Change

- `work_limits.admission`: `resource` (default) or `weighted`. A policy file
  without it loads as `resource`. Under resource admission the unit cap,
  the warning derate, `swap_thrash_capacity` and class weights do not decide
  a start. They stay in the policy as telemetry and data format.
- The resource ladder decides live on an enabled policy, also with
  `enforce_admission` false. Red-level host blocking stays off.
- Only `queue` and `relieve` hold a start: memory short of the predicted
  peak, swap thrash caused by agent work, or a critical level, each only
  while another lease runs. A missing sample or unreadable coordinator fails
  open. A lease the process owns is never held.
- A memory fit check under the coordinator state lock refuses a start whose
  predicted peak exceeds the headroom left by the leases that exist at that
  moment (`ErrWorkResource`, blocker `resource`, wait cause
  `queue_resource`). Without a cap, this is what stops many starts that all
  read the same headroom.
- Rule v3: the busy-core predictor is the median at every scope. An empty
  host never waits on critical or thrash. Thrash the resident attributes to
  processes outside the coordinator throttles agent work to background I/O
  instead of queueing it.
- Applied live from a `throttle` step: background disk I/O
  (`taskpolicy -d throttle`), and since the CPU follow-up below a CPU
  priority of nice 10. Not applied live: the CPU parallelism limit.
- Stall guard: under resource admission a waiter is a stall only when it
  fits memory.
- Events: `resource_decision`, `resource_rule`, `throttle_background_io`.
  Status and doctor: `admission`. Go API for the delivery worker:
  `ReadResourceHeadroom`, `DecideResourceAdmission`.

## Evidence used

- CPU predictions against measured busy cores of completed runs (5 s or
  longer): measured/predicted p50 0.13 to 0.23, p90 about 0.5. The p90/p95
  predictor read 4 to 6 times high. v2 replay over 24 h: 1,551 starts,
  admit 869, throttle 657 (cpu 664 deciding), remote 25, queue 0.
- Live shadow v2 after the cap went to 1000 (13:33 to 13:35Z):
  `queue:memory:no_headroom` 20 times with about 38 units in use and model
  headroom -15 to -29 GB. At 13:36Z: 14 leases with 25.3 GB predicted (10
  ramping), measured coordinated RSS 2.2 GB, available 23.6 GB. The memory
  model is conservative, and a hold lasts at most the 3 min ramp.

## Deviations

1. The CPU parallelism limit is a recorded decision, not a live throttle.
   Closed by the CPU follow-up below: CPU shortage lowers CPU priority
   instead.
2. The memory model charges a ramping lease its full p90 peak for 3 min.
   With many starts this holds work that would fit (13:36Z: 25.3 GB charged,
   2.2 GB measured). The lock check uses the same model. Remaining: charge
   ramping leases max(measured tree RSS, a fraction of the p90) once
   per-lease RSS is sampled.
3. FIFO head-of-line: a large head that does not fit memory holds smaller
   waiters behind it.
4. The relieve step holds; it does not restart a family.
5. Stall guard for weighted admission is unchanged.

## Tests

`sessionpressure/resource_admission_live_test.go` and the v3 rows in
`work_resource_headroom_test.go`. Full `go vet ./...` and `go test ./...`
on the Linux gate runner (runner-3).

## Install and live proof

- Tests: full `go vet ./...` and `go test ./...` on the Linux gate runner,
  24 packages ok (job abc34d5a). The lane verified it again before landing.
- Landed 669bc82 (delivery ea47432c). The first capture (fcab16c6) was
  stacked on the memory-guard delivery and paused with PATH_CONFLICT,
  because it rewrites the parent's two functions in `policy.go`. Captured
  again on top of the landed parent with `--replace`.
- Built from a clean clone at 669bc82, installed at 14:18Z with
  `--expected-current e3364280...` when no lease was in flight, then
  `monitor install` (resident reporting after 0.8 s).
- Live policy: backup `policy.json.bak-20261117-before-resource-admission`,
  then `work_limits.admission = "resource"` (a file without the key loads as
  resource anyway; the explicit value documents it). `enforce_admission`
  stays false. `memory_admission = enforce` stays and is unused under
  resource admission.
- Proof at 14:18Z: `work status` prints `admission=resource
  capacity=1048576`. `doctor`: `work.admission: resource`. `work headroom`:
  level normal, available 26,050 MB, reserve 6,144 MB, headroom 19,829 MB,
  swap used 2,623 MB, no thrash. A dogfood `work run --class express-test`
  recorded `resource_decision: admit:none:fits` and
  `resource_rule: mac-resource-headroom-v3` on acquired, started and
  completed events.
- 24 h baseline at install (mostly before the change, cap 12 to 1000):
  2,087+ operations, 152 long waits, 23 wait timeouts, 539 cancelled (516
  wrapper interrupts), 152 reservation deferrals, 2 memory deferrals, 0
  swap-thrash, 0 resource deferrals. Compare `work report --since 24h` on
  2026-11-18 after 14:18Z.

## CPU follow-up: priority, not a core cap (WORK-1065, 2026-11-17)

Question: should the ladder's CPU throttle (v3, p50 predictor) apply live?
The rule was "apply it if a replay shows it does not lengthen runs".

- Replay: `work evaluate --since 48h` now scores each throttle decision's
  core limit against the busy cores (command CPU time / runtime) that the
  same run measured unthrottled (`cpu_throttle`, runs of 5 s or longer).
  This is a lower bound: the average hides bursts above it.
- 48 h, 2,294 evaluated starts. v3 throttles 420 (v2 throttled 664 of
  1,551). By reason:
  - `cpu:cpu_saturated` (limit 1): 371 decisions, 94 measured, 9 slowed,
    busy/limit p50 0.25 and p95 1.92, at least 122.9 s added to 7,271 s
    (1.7%).
  - `cpu:partial_headroom`: 7 decisions, 6 measured, 4 slowed, busy/limit
    p50 1.13, at least 32 s added to 96.5 s (33%).
  - `io:disk_write_high`: 42 decisions, 20 measured, 0 slowed.
- Verdict: the core limit lengthens runs (13 of 100 measured CPU-throttled
  runs), so it fails the rule and stays a recorded value.
- Applied instead: a live `throttle` step whose dimension is not `io` starts
  the command at nice 10, below the lease default nice 5 and the interactive
  host at nice 0. A nice value is no cap: the run uses every idle core and
  yields only to work that needs the core. It cannot slow a run on an idle
  host, and it keeps work already running ahead of new CPU-short work.
- Events: `throttle_cpu_nice` (0 to 20) on the run's events.
- Tests: `TestLiveResourceThrottleAppliesIOAndCPUPriorityNotACoreCap`,
  `TestWorkThrottleNiceKeepsTheLeaseDefault`,
  `TestReplayResourceHeadroomScoresTheCPUThrottle`. Full `go vet ./...` and
  `go test ./...` on the Linux gate runner (runner-3).
- Remaining: `Setpriority` runs right after the child starts, so a
  grandchild forked before it keeps nice 0 (existing race, also for nice 5).
  The 24 h report should compare runtimes of niced runs with the same class
  unthrottled.
- Landed 850bdd6 (code, delivery c25bfb20) and c13f379 (docs, delivery
  18e378d5). Built from a clean clone at c13f379 and installed at 14:58Z with
  `--expected-current 669bc82...`, then `monitor install` (resident reporting
  after 1 s). Eleven leases were running at install. Their processes keep
  the old binary, and the next `work run` reads the new one.
- Live proof at 14:59Z, host CPU headroom -1.8 cores: three
  `work run --class express-test -- sh -c 'ps -o nice= -p $$'` runs. The
  first was `admit:level:red_lane` and printed nice 5. The next two were
  `throttle:cpu:cpu_saturated`, printed nice 10 and recorded
  `throttle_cpu_nice: 10` with rule v3.
