# Resource-headroom admission, shadow candidate (2026-11-17)

Owner: claude/bb3c2dab. Status: 2026-11-17, source and tests written, shadow
only, not installed, delivery state in the WORK-1065 comments.
Work: WORK-1065, a candidate under WORK-3 (owner codex). Epic: WORK-765.

## Decision

Nico, 2026-11-17, relayed by the Claude triage session: "no fixed number cap.
I want a resource-based, intelligent admission system that keeps the Mac from
crashing or thrashing without human-set limits." Then: "Build WORK-1065 now as
a candidate under WORK-3, shadow first."

This follows WORK-772 (advisory session counts, no launch limit, done by
codex) and builds on the memory-headroom shadow of 2026-11-15
(`work_headroom.go`, rule `mac-headroom-tree-p90-scope-p95-ramp-v4`).

## What was built

| Part | File | State |
| --- | --- | --- |
| Decision and ladder | `sessionpressure/work_resource_headroom.go` | shadow only |
| Opt-in throttle | `sessionpressure/work_throttle.go` | caller opt-in only |
| Replay with host samples | `sessionpressure/work_resource_replay.go` | in `work evaluate` |
| Event fields | `sessionpressure/workevent.go` | additive, schema version unchanged |
| Wiring | `sessionpressure/workrun.go`, `work_headroom.go`, `workreplay.go`, `sessionpressurecmd/session_pressure_work.go` | |

Rule name: `mac-resource-headroom-v1`.

- Inputs: memory headroom (as before), swap-out rate and the thrash latch,
  CPU headroom, the disk-write state, and the most reclaimable memory family.
- CPU headroom = logical CPUs x (1 - host CPU) - reserve (logical / 8, at
  least 1 core) - predicted cores of leases younger than 90 s.
- Cost: busy cores = command CPU time / runtime of completed runs. p90 for the
  same placement key (3 samples), p95 for the workload or class (5 samples),
  else 2 cores. Peak memory is the existing predictor.
- Ladder: admit, throttle, remote, relieve, queue. No step names a process to
  stop.
- Per-session cost comes from leases and the session digest on work events.
  It does not use agent tree RSS, because one desktop app tree holds every
  desktop session.
- Kept from the live queue: critical and red stay with the level gate, the
  capped red lane admits light interactive work, a queue with nothing running
  gets a throttled admit, a missing sample fails open.
- No policy field and no enforce mode were added. `promotion_ready` is
  unchanged and stays with WORK-3.

## Proof (output seen in this session, 2026-11-17)

- `go test ./sessionpressure -run 'Resource|Throttle|CoreHistory|SessionCost'
  -count=1`: ok, 1.051 s. Covers the ladder table (17 cases), the 16 GB /
  10 CPU and 48 GB / 14 CPU fixtures, the guard test
  `TestResourceAdmissionHasNoFixedCount`, CPU and memory ramps, the throttle
  environment and wrapper, and the replay on a synthetic trace.
- `go test ./sessionpressurecmd -count=1`: ok, 11.116 s.
- `go test ./sessionpressure -count=1`: one failure,
  `TestResidentSamplerRepeatedFreshInventoriesStayWithinRSSBudget` (40.1 MB
  against 30 MB). It fails the same way with every WORK-1065 test skipped
  (40.6 MB) and passes alone 3 of 3 (28.0 to 28.7 MB). Recorded as WORK-1095.
  Not verified: whether it failed before this change's non-test code.
- Not run: the race detector, runner-3, an installed build.

## Seven-day replay, from source, 2026-11-17 about 12:00Z

`go run ./cmd/session-pressure --json work evaluate --since 168h` on the
48 GB, 14-CPU Mac. The event store on this host holds 891 lease starts in the
window. 890 were evaluated, 1 had no host sample.

| Step | Starts | Share |
| --- | --- | --- |
| admit | 599 | 67.3% |
| throttle | 82 | 9.2% |
| remote | 11 | 1.2% |
| queue | 198 | 22.2% |

Deciding dimension: none 516, cpu 210, level 159, io 5.

| Class | Starts | Delayed | Throttled | Live wait p95 |
| --- | --- | --- | --- | --- |
| browser | 16 | 12.5% | 0% | 189.2 s |
| build | 145 | 33.1% | 13.1% | 200.2 s |
| express-build | 176 | 22.2% | 3.4% | 84.5 s |
| express-test | 239 | 18.8% | 0.4% | 38.6 s |
| heavy | 3 | 0% | 33.3% | 159.9 s |
| install | 21 | 23.8% | 9.5% | 26.3 s |
| test | 290 | 24.1% | 18.3% | 120.1 s |

Safety: 0 starts were followed by a swap-thrash latch or a critical level
within 10 minutes. The candidate's count is therefore also 0, and the safety
criterion has no evidence in this window.

## Deviations from the acceptance criteria, and why

1. **Safety replay has no data.** The trace on this host has no thrash or
   critical sample after a start. The 16 GB Mac had such episodes on
   2026-11-15, but its event store is not on this host. Remaining: run the
   same command on the old Mac's store, or wait for an elapsed shadow window.
2. **The candidate is more conservative than the live queue.** It would have
   delayed 22% of starts that the live queue ran without a thrash or critical
   sample after them. This is not a pass. Two causes are visible:
   - 159 decisions stopped at the level gate. The level comes from the static
     thresholds in the policy, and the admission guards on this Mac are off
     (2026-11-15). So this part still depends on human-set thresholds. The
     candidate keeps the gate because the corpus lists it as a guarantee.
     Remaining: decide with WORK-3's owner whether red should become a
     headroom outcome, not an input.
   - 210 decisions were CPU. Host CPU on this Mac is often above 85%. With a
     1.8-core reserve, a 4-core prediction does not fit. Remaining: check the
     busy-core predictions against measured runs, as `work headroom --replay`
     does for memory, and review the reserve and the use of the rolling
     against the live CPU figure.
3. **Wait is not modelled.** The replay does not move later arrivals. It
   reports the delayed and throttled share beside the live p95 wait. A p95
   wait for the candidate needs the scheduler replay to take host samples.
4. **No 48-hour shadow run.** The change is not installed, so no live event
   carries a shadow resource decision yet.
5. **The throttle is not proven on real builds.** Unit tests cover the
   environment and the wrapper. No leased command was run with
   `NDEV_WORK_THROTTLE_CORES`. `taskpolicy -d throttle` was not run.
6. **The relieve step and the io dimension are thin in replay.** Heartbeats
   carry no memory families or disk-write state.
7. **Remote eligibility is coarse.** It is "placement not pinned local and
   the tool family is Go or Node".
8. **Work event size.** The new fields add about 150 bytes to queued and
   acquired events. The 1,024-byte limit was not measured for the largest
   real event.

## Coordination

No Codex session was live. The interface is in a WORK-3 comment of
2026-11-17. The WORK-3 owner has not answered.

## Next

- Deliver, then build and install through the release contract when no lease
  is in flight.
- Measure deviation 2 before any talk of promotion.
- KEP with a typed sidecar: not written. This is a cross-subsystem contract
  (work events are read by the delivery worker), so one is owed.

## Rule v2, same day (2026-11-17, about 13:30Z)

Status: 2026-11-17, source and tests written, not installed. The merge owner
(claude session 6b131cdd) builds and installs one 0.7.3 release that holds
this, after it merges refs/old-mac/main-20261117 on top. Nico approved the
48-hour shadow install after that merge, shadow only ("Yes, after merge",
relayed by the triage session).

Changes from v1, made because of deviation 2 above:

- Level red is not an input by itself. Work that fits measured headroom runs
  at red. Critical stays a gate. The red lane still admits light interactive
  work that would otherwise wait.
- A CPU shortage never queues work. It throttles to the cores that are free,
  or to one core (`cpu_saturated`). Only memory and swap activity can queue.
  Reason: CPU contention costs time and does not thrash the host. This is a
  design choice by claude/bb3c2dab; the WORK-3 owner has not reviewed it.
- The replay estimates the candidate's wait: live wait plus the hold until a
  later host sample would release held work (`candidate_wait_p95_ms`,
  `held`, `hold_p50_ms`, `hold_p95_ms`, `unresolved_holds`).
- Rows from a v1 build stay readable (rule name and the `red` reason).
- WORK-1095: the resident RSS budget test measures in a fresh copy of the
  test binary.

Proof seen: `go test ./sessionpressure ./sessionpressurecmd -count=1` ok
(117.0 s and 10.5 s), with the RSS test at 27.5 MB. `go vet` exit 0.

Seven-day replay under v2, from source. The window moved, so it holds 955
starts against 891 for v1. The two runs are not the same sample.

| Step | v1 (891 starts) | v2 (955 starts) |
| --- | --- | --- |
| admit | 67.3% | 67.5% |
| throttle | 9.2% | 30.4% |
| remote | 1.2% | 2.1% |
| queue | 22.2% | 0% |

v2 deciding dimension: none 644, cpu 292, io 18. Held starts: 0, so the
estimated candidate p95 wait equals the live p95 wait in every class.

| Class | Starts | Throttled | Remote | Live and candidate wait p95 |
| --- | --- | --- | --- | --- |
| browser | 17 | 17.6% | 0% | 385.8 s |
| build | 151 | 27.8% | 1.3% | 303.8 s |
| express-build | 193 | 31.6% | 5.7% | 146.1 s |
| express-test | 263 | 23.6% | 2.3% | 66.9 s |
| heavy | 4 | 50% | 0% | 276.8 s |
| install | 21 | 52.4% | 0% | 26.3 s |
| test | 305 | 35.7% | 0.3% | 229.1 s |

What this does and does not show:

- v2 would hold nothing in this window. Memory never decided: the 48 GB host
  had headroom for every start.
- v2 would throttle 30% of starts. A throttled run takes longer. The replay
  does not model that runtime cost, so "wait is equal" is not "cost is equal".
- Safety is still without evidence: no start in the window was followed by
  thrash or a critical level.
- `work evaluate` exits 1 because the scheduler candidate's promotion gate is
  not ready. That is WORK-3's gate and is the same before this change.

## Shadow install (2026-11-17, 13:06Z)

Status: 2026-11-17, shadow live on the M4. 48-hour review due
2026-11-19T13:10Z.

- Installed a64443a, the main after the private-name history rewrite. It
  holds 0.7.3, rule v2, WORK-1100 and the phys_footprint change. The fence was
  `--expected-current ff27be449f`, the old-Mac footprint build that an old-Mac
  session installed at 12:25Z.
- Built from a clean clone of origin/main on the Mac (darwin/arm64, cgo).
  The resident monitor was promoted with `session-pressure monitor install`.
  The policy stays observe-only.
- First live events with `shadow_resource_rule` `mac-resource-headroom-v2`:
  13:09:46Z. Live admission did not change.
- Review: `session-pressure --json work evaluate --since 48h`, the
  `resource_headroom` block. Check the throttle share and any thrash or
  critical level that follows a start.
