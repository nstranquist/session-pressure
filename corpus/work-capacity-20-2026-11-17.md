# Work capacity raised to 20 on the 48 GiB Mac (2026-11-17)

Status: applied 13:22Z, 2026-11-17. Owner: claude (Claude Code desktop
session, Opus 5.5). Request (Nico, in chat): "in the session pressure limits
perhaps our delivery queue is getting capped in how much our computer can put
on it ... lets raise those".

## Finding

- Live `work_limits.capacity` was 12, the code default for 14 logical CPUs
  (`max(2, cpus-2)` in `defaultWorkLimits`). Nico's standing decision from
  2026-11-16 is capacity 16 or higher ("higher capacity is better", see
  `work-queue-v2-2026-11-15.md`). Every 2026-11-17 policy backup already shows
  12, so the value went back to the default between 2026-11-16 02:16Z and
  2026-11-17 04:50Z. The actor is not recorded.
- At 12 units, one build (7) and one test (4) fill the pool. The 24 h work
  report (to 13:21Z) had 152 long-wait operations and 23 wait timeouts.
- The delivery worker's repository lane ceiling (3) was not the limit. Over
  236 schedule records in 24 h there were no `lane_ceiling` or `capacity`
  waits. The waits were `repo_running` (1530), `dispatched` (1241),
  `behind_group` (284) and `behind_mark` (21). In 56 records the host verdict
  was busy, so no lane opened. That verdict comes from the session-pressure
  resident level, and it stays as the guard against overload.

## Change

- `~/.nicos-dev/session-pressure/policy.json`: `work_limits.capacity` 12 to
  20. Class weights, the profile (`observe`) and `enforce_admission` (false)
  are unchanged. Backup:
  `policy.json.bak-20261117T132148Z-before-cap20`.
- Why 20 and not more: `work headroom` at 13:20Z had 12,016 MB of headroom
  after the 6 GiB reserve, with 12 units in flight (predicted 5,924 MB). The
  8 added units hold about two more tests, about 5.6 GB at the test p90 of
  2,779 MB. That fits. 24 units would add about 11 GB at p90, which is the
  whole margin, and the Mac already had 17 GB in the memory compressor.
- Not done: memory-headroom admission (`work_limits.memory_admission`) and
  swap-thrash serialization act only when `enforce_admission` is true
  (`configuredWorkSwapThrashEnforced`). Nico turned enforcement off on
  2026-11-15, so the weighted cap is the only guard on local work today.
  To go past 20 safely, the memory guard must work without the host-pressure
  block. Follow-up: make memory admission and swap-thrash serialization
  independent of `enforce_admission`.

## Proof

- `session-pressure --json policy show`: ok, capacity 20, profile observe.
- Telemetry `resident_policy_changed` at 13:22:39Z. The resident restarted
  on the new file.
- `session-pressure work status` at 13:23Z: `capacity=20 used=4
  available=16 queue=0`.

## Reverting

Copy the backup over `policy.json`. The resident restarts on the change.
