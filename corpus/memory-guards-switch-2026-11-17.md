# Memory guards get their own switch (2026-11-17)

Status: 2026-11-17, code landed, helper install and live switch below.
Owner: claude/5e967f73-34c6-46d6-b36f-b097ff336e18. Work: WORK-1125.
Follows [work capacity raised to 20](work-capacity-20-2026-11-17.md).

## Problem

Nico turned host-pressure enforcement off on 2026-11-15
([guards disabled](guards-disabled-2026-11-15.md)). Memory-headroom admission
(`work_limits.memory_admission = enforce`) and swap-thrash serialization
(`swap_thrash_capacity`) were both gated by `configuredWorkSwapThrashEnforced`,
which required `enforce_admission`. With enforcement off, the weighted cap was
the only guard on local work, so the cap could not safely go past 20.

## Change

- `work_limits.memory_admission = enforce` is now its own switch. It arms the
  memory-headroom gate and swap-thrash serialization when the policy is
  enabled, also when `enforce_admission` is false.
- `enforce_admission` keeps its old meaning. An enforcing profile still arms
  swap-thrash serialization without the memory switch. Red-level host
  blocking (`AdmissionForSnapshot`) still follows `enforce_admission` only, so
  it stays off on this Mac.
- Unchanged fail-open rules: an empty queue never waits on memory
  (`memoryHeadroomHold` needs a running lease), a missing or stale sample
  fails open (`no_sample` never holds), unknown coordinator status fails open,
  a lease the process already owns is never held, and a lone heavy operation
  always runs under thrash. Only `no_headroom` holds. A red memory level does
  not hold through this switch.
- `policy.go`: `WorkMemoryAdmissionEnforced` and `WorkSwapThrashEnforced` are
  the one definition of the switch. The work gate uses
  `memoryGuardsEnforced` (policy enabled plus `memory_admission = enforce`,
  read fresh on every admission check).
- `doctor`: `work.memory_admission_enforced` and `work.swap_thrash_enforced`
  show the switch. The swap-thrash warning now says "report-only" when neither
  switch is on. Before, it claimed serialization that did not happen.
- Applying the `observe` profile keeps `memory_admission`, so the switch
  survives a profile re-apply.

## Tests

`sessionpressure/memory_guards_switch_test.go`: switch table, persisted
observe policy, swap-thrash deferral under the switch, fail-open rules,
doctor wording. Full `go vet ./...` and `go test ./...` on the Linux gate runner (runner-3).
