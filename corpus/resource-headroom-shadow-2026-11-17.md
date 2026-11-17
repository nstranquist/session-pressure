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
