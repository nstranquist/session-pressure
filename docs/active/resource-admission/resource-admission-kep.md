# KEP: Resource-headroom admission replaces the weighted unit cap

## Summary

Session-pressure admits heavy work on measured host headroom, not on a
fixed count of weighted units. `work_limits.admission = resource` is the
default. Under it the resource ladder (`mac-resource-headroom-v3`,
`sessionpressure/work_resource_headroom.go`) decides each start, and the
configured `capacity`, `warning_capacity`, `swap_thrash_capacity` and class
weights stay in the policy only as telemetry and a data-format contract.

## Progress

- 2026-11-17: memory-guard switch landed (delivery 6d34597d, WORK-1125).
- 2026-11-17: rule v3, live gate, lock fit check, event and status fields,
  exports, stall guard. Landed 669bc82, installed and live 14:18Z. 24 h
  proof owed: see `corpus/resource-admission-live-2026-11-17.md`.

## Motivation

Nico, 2026-11-17: "i want EVERYTHING to be resource-based, not stupid
hard-cap based." The unit cap was raised 12, 20, then 1000 by hand on the
same day. A cap is either too low (idle host, long waits: 152 long waits and
23 wait timeouts in 24 h at cap 12 to 20, with free memory at p50 75%) or
too high (no guard at all: cap 1000 with `memory_admission` unset).

## Goals

- Decide starts from live memory headroom, swap activity and memory level.
- Keep work moving: an empty host never waits, a missing sample fails open.
- No unit count decides admission.
- Give external schedulers (commit-delivery worker) the same view and rule.

## Non-Goals

- CPU parallelism limits applied live. The limit stays recorded only; CPU
  shortage lowers CPU priority instead (see Decision Log).
- Restarting memory families (the relieve step holds; it does not act).
- Red-level host blocking. It stays with `enforce_admission`, off on this Mac.

## Proposal

Promote the WORK-1065 shadow ladder to the live admission gate, gated on an
enabled policy and `admission = resource`, independent of
`enforce_admission`. Remove the weighted cap from admission by making the
coordinator's admission limits unbounded under resource admission.

## Design Details

- Policy: `WorkLimits.Admission` (`resource` | `weighted`). `LoadPolicy`
  fills an empty value with `resource`. `admissionLimits()` sets capacity to
  2^20 and turns the warning derate off under resource. The exclusive
  benchmark class still weighs the whole capacity, so it still runs alone.
- Gate (`workrun.go`): `resourceAdmissionDecision` runs before the older
  guards. It holds on step `queue` or `relieve`. It never holds a lease the
  process owns. Under resource admission the swap-thrash serializer, the
  memory-headroom guard and the warning derate stand down.
- Live probe: the same ladder with `RemoteEligible = false`, so a hold never
  turns into a local start because the shadow would send it away.
- Throttle: a live `throttle` step applies `BackgroundIO`
  (`taskpolicy -d throttle`) and, unless the dimension is `io`, a CPU
  priority of nice 10 (`WorkThrottle.CPUNice`) instead of the lease default
  nice 5. Its parallelism limit (`ThrottleCores`) is not applied. The
  caller's explicit throttle wins.
- Lock fit (`worklease.go`, `resourceFit`): when the selected waiter or a
  direct acquire would take its lease, its predicted peak is checked against
  headroom computed from the leases in the locked state. Refusal is
  `ErrWorkResource`, retried in the wait loop as blocker `resource`.
- Stall guard: under resource admission a waiter is a stall candidate only
  if it fits memory (`resourceFit`), so a waiter that rightly waits for
  memory is not alerted.
- Rule v3: core predictor p50 at every scope; critical and thrash with no
  running lease give `throttle:*:keep_progress`; thrash the resident
  attributes to processes outside the coordinator gives
  `throttle:swap:external_thrash` with background I/O, and the memory fit
  still applies.

## Decision Log

- 2026-11-17 (later): CPU shortage lowers CPU priority, not parallelism. The
  48 h replay scored v3's core limit against the busy cores the same runs
  measured: `cpu_saturated` (limit 1) slowed 9 of 94 measured runs, adding at
  least 1.7% of their runtime; `partial_headroom` slowed 4 of 6, adding at
  least 33%. A limit fixed at start cannot follow contention that changes by
  the second. Nice 10 below the leases' nice 5 lets new CPU-short work use
  every idle core and yield only when running work needs the core, so it
  cannot slow a run on an idle host. The replay keeps the core-limit score
  (`cpu_throttle`) as evidence.
- 2026-11-17: CPU is not applied live. Measured busy cores of completed
  runs were 0.13 to 0.23 of the p90/p95 prediction (p50 of the ratio), and a
  parallelism limit is fixed for the life of a command while CPU contention
  changes by the second. v3 moves the predictor to p50; CPU stays a recorded
  decision until a replay shows the p50 throttle does not lengthen runs.
- 2026-11-17: red is not an input (v2, kept). Critical stays a gate.
- 2026-11-17: the relieve step holds like queue. No live step restarts a
  family yet.
- 2026-11-17: FIFO head-of-line under memory shortage is accepted. A large
  head waits for memory and smaller waiters wait behind it. This keeps big
  work from starving.

## Command / Data Contract Changes

All additive. Readers that ignore unknown fields are unaffected.

- Policy: `work_limits.admission` (string, `resource` | `weighted`).
- Work event: `resource_decision` (closed vocabulary
  `step:dimension:reason`), `resource_rule`, `throttle_background_io`, `throttle_cpu_nice` (0 to 20).
  New
  blocker `resource`. New reason `external_thrash`. New wait cause
  `queue_resource`. Rows with `shadow_resource_rule` v1 and v2 stay valid.
- `WorkStatus.admission`, `WorkWaiterStatus.predicted_peak_mb`, waiter
  record `predicted_peak_mb`, doctor `work.admission`.
- Go API: `ReadResourceHeadroom(ctx, dir, coordinator, now)`,
  `DecideResourceAdmission(headroom, request)`, `ErrWorkResource`,
  `WorkBlockerResource`.
- `capacity`, `used` and `available` remain in `WorkStatus`. Under resource
  admission capacity reads 1048576 and used/available are telemetry.

## Migration / Hard Cutover Plan

Policy files without `admission` load as `resource`. To return to the unit
cap, set `"admission": "weighted"` in `work_limits` and restart nothing (the
gate re-reads the policy on every check; the coordinator reads it at the
next `work run`).

## Test Plan

`sessionpressure/resource_admission_live_test.go`: default and cap removal,
coordinator past the configured cap, lock fit cases, blocker vocabulary,
gate holds only queue and relieve, live throttle is background I/O and CPU
priority with no core cap, older guards
stand down, event vocabulary and v2 rows, exported ladder, doctor wording,
stall guard. `work_resource_headroom_test.go`: v3 ladder rows and p50
predictor. `work_resource_replay_test.go`: the CPU-throttle score. Full `go vet ./...` and `go test ./...` on the Linux gate runner.

## Rollout / Validation / Failure Modes

- Rollout: install through `scripts/release`; the live policy gains
  `admission: resource` on load.
- Validation: `work status` shows `admission=resource`; `doctor` shows
  `work.admission`; 24 h `work report` compares waits, timeouts, resource
  deferrals and swap with the cap-20 day.
- Failure modes: an over-high memory prediction holds work for at most the
  3 min ramp of the running leases; a stale sample fails open; a coordinator
  read error fails open.

## Implementation Plan

- [x] Rule v3 ladder and exports
- [x] Live gate, lock fit, events, status, doctor, stall guard
- [x] Gate-runner tests
- [x] Land (669bc82), install, live proof (2026-11-17 14:18Z)
- [x] CPU priority instead of a core cap, from the 48 h replay (2026-11-17)
- [ ] 24 h report (2026-11-18)

## Milestones

- M1 (2026-11-17): live on the M4.
- M2 (2026-11-18): 24 h report reviewed.

## Graduation Criteria

Beta when a 24 h report shows no swap-thrash episode caused by agent work,
fewer long waits than the cap-20 day, and no false stall alert.

## Outcomes & Retrospective

Pending the 24 h report.

## Alternatives Considered

- Raise the cap again: rejected by Nico.
- Keep the cap and add memory admission only: still caps an idle host.
- Apply the v3 core limit live: rejected after the 48 h replay (it slows
  13 of 100 measured runs). Nice 10 is applied instead.

## Open Questions

- Should the commit-delivery worker take a resource-priced lease instead of
  reading the view? A lease API is the better long-term contract.

## Deferred / Future Roadmap

- Live relieve step (restart a reclaimable family).

## Issues / Regrets

- The cap was raised three times on one day before this landed.
