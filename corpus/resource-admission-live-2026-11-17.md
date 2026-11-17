# Resource admission goes live, unit cap removed (2026-11-17)

Status: 2026-11-17, code and tests written, landing and install below.
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
- Applied live: background disk I/O (`taskpolicy -d throttle`) from a
  `throttle` step. Not applied live: CPU parallelism (see deviations).
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

1. CPU is a recorded decision, not a live throttle. Reason above. Remaining:
   replay v3 (p50) against completed runs and apply the throttle when the
   replay shows it does not lengthen runs.
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

Pending.
