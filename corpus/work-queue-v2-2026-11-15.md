# Work queue: v1 rating and v2 plan (2026-11-15)

Owner: claude/003c4d55. Status: plan, 2026-11-15. v1.1 hotfix landed in the
same session (selector schema 8). v2 is not implemented.

## 1. The incident

At about 14:00Z on 2026-11-15 the Work Queue page showed 1 of 8 units used,
7 free, and 10 waiters that did not move. The head was a `build` (weight 5)
with `protected_pressure_reservation`. Fifteen minutes later the same shape
came back with a `browser` reservation at the head and 5 waiters blocked
behind it.

Root cause (read in `sessionpressure/worklease.go` and `workrun.go`):

1. After an operation gets a lease, `workrun` checks host pressure again. If
   pressure is up, `ReserveForPressure` turns the lease into a protected
   waiter and the owner goes back to `waitForWorkAdmission`.
2. The selector treated that reservation as a protected head and returned
   `protected_bounded_drain`: it kept free units idle for it.
3. The owner was not trying to acquire. It waited on host pressure (CPU
   warning, memory red, swap 13 to 17 GB, swap-out 200 to 530 MB/s). So the
   capacity it reserved could not be used by anyone, for the whole pressure
   episode.
4. A second form: under the Interactive warning ceiling (4), a protected
   weight-5 build can never fit, but it still held the drain.

The result is a head-of-line block across the whole host while the Mac is
under the exact load the agents create. It does not clear by itself.

## 2. v1 rating

Evidence: `ndev --json session pressure work stats --since 24h`,
`work evaluate --since 24h`, per-day `work-events-*.jsonl`, and
`work-control-events.jsonl`. Numbers are from 2026-11-15 about 14:40Z.

24h window: 301 operations, 1074 events. Total wait 28,040 s (467 min).

| Wait cause | Share | Operations |
|---|---|---|
| queue_reservation (this bug) | 36% | 41 |
| queue_other | 16% | 189 |
| queue_capacity | 13% | 28 |
| pressure_memory | 11% | 21 |
| queue_fairness | 11% | 14 |
| pressure_cpu | 9% | 46 |
| pressure_swap | 4% | 5 |

- One burst, 13:26Z to 14:34Z: 63 operations, 23,013 s of wait.
- 21 wait timeouts, 69 long waits. End-to-end SLO (p95 bounded slowdown 5):
  breached.
- `browser`: 55 operations, 38 cancelled.
- `work evaluate`: 22 of 22 deterministic scenarios pass. The scenarios test
  the rules as designed. None tested a parked reservation, so the suite was
  green while the queue was stalled.

Per day (acquire wait p95, operations, remote finished):

| Day | Ops | p95 wait | Wait min | Remote |
|---|---|---|---|---|
| 11-09 | 1295 | 0 s | 30 | 362 |
| 11-10 | 2972 | 70 s | 534 | 262 |
| 11-11 | 2263 | 81 s | 487 | 190 |
| 11-12 | 656 | 0 s | 3 | 76 |
| 11-15 | 311 | 449 s | 337 | 32 |

Remote share fell from 28% (11-09) to about 10% (11-15).

Score, out of 10:

| Area | Score | Why |
|---|---|---|
| Safety (host stays usable) | 8 | Gates hold. Memory red is enforced. |
| Liveness (work always moves) | 2 | Total stall with free capacity, twice in one hour. |
| Throughput under agent load | 3 | 16 GB Mac, single weight, swap thrash. |
| Remote offload | 3 | Go-only, opt-in shapes, 10% share today. runner-3 has 12 cores, 62 GB, load 4.5. |
| Observability | 7 | Very good event data. Evaluate did not catch the stall. |
| Operator control | 6 | Run now and Run all work, but the operator must notice. |
| Overall | 4 | Correct by its own rules, not fit for the current load. |

## 3. v1.1 hotfix (landed)

Selector schema 7 to 8, in `sessionpressure/worklease.go`:

- New waiter field `resumed_at`. A pressure reservation with no `resumed_at`
  is parked: it keeps its queue place but selection skips it, and it accrues
  no bypass. Status shows `pressure_parked: true`.
- The owner's next `TryAcquire` sets `resumed_at` in the same locked
  transaction. From then on it is a protected head again, as before.
- A protected head heavier than the effective ceiling no longer holds a drain.
  Feasible successors run with reason
  `protected_head_exceeds_effective_capacity`, and the head is not charged a
  bypass.
- Two new `work evaluate` scenarios: `pressure-parked-head-does-not-idle` and
  `derated-head-does-not-idle`.

- The FIFO selector (`fifo-v1`) had the same parked-head bug and gets the
  same skip.
- The remote code default host is now `runner-3` (was the retired
  `runner-2`). The installed config already named `runner-3`.

Proof (runner-3, `work run --placement remote`, 2026-11-15):

- `TestSelectWorkWaiter*`, `TestWorkSelector*`,
  `TestWorkLeasePressureReservation*`, `TestRemoteEndpoint*`: pass, `-count=2`
  on clean HEAD plus this patch only, and `-count=3` on the shared tree.
- `./sessionpressurecmd`: pass.
- Two failures are not from this change. They fail the same way on a clean
  `git archive HEAD` export on the same runner:
  `TestGoReuseSingleflightAndExplicitExecution` (20 s file-lock timeout) and
  `TestGoProfileSurvivesFailureTimeoutAndSignal`. Both run nested `go`
  processes with fixed timeouts and the runner reran the job heavy after a
  light-lane OOM. Open item below.
- Correction on the record: I first reported the singleflight failure as
  caused by this patch. A truncated log hid that it also fails on HEAD.

Live action: at 14:47Z I used Run now on a `build` stuck behind a parked
`browser` reservation. It acquired, then its own post-lease check hit memory
red (swap 13 to 17 GB) and it became a pressure reservation too. The queue
was no longer the blocker. Host memory was.

Mixed versions: waiters started by the old binary still run the old selector
until they exit. Old binaries ignore `resumed_at`. The new owner sets it again
on every attempt, so an old rewrite cannot strand it.

## 4. v2 plan

Goal: work always moves, the Mac does only Mac-only work, and every other
job goes to runner-3 by default. The loop measures itself and corrects
its own placement.

v1 stays selectable (`scheduling_policy: bounded-lookahead-v1` or `fifo-v1`)
for rollback. "Disable v1" means the default changes to `placement-v2`
after the shadow and canary gates below.

### 4.1 Placement first, then admission

Every `work run` gets a placement decision before it enters a queue:

- `mac-only`: declared or detected (Xcode, Swift, simulator, emulator,
  browser, Darwin hooks, Mac-side driver binaries). Goes to the local pool.
- `remote-capable`: Go, Node/pnpm, Rust/cargo, Python, make targets with a
  Linux profile. Goes to the remote pool on runner-3 by default.
- `either`: small express work where upload cost is larger than run cost.
  The closed loop decides (4.4).

Today only `RemoteGoCommand` shapes route remotely, and everything else falls
to the Mac. v2 adds profiles per toolchain, each with a source and
environment boundary like the current Go profile.

### 4.2 Two pools, one queue view

- Local pool: Mac capacity as a resource vector (CPU units, memory MB), not
  one weight. Memory is the binding limit on a 16 GB Mac. Derates apply per
  dimension.
- Remote pool: slots from `remote capacity` (fleet inventory, live runner
  load). runner-3 only. Runner 1 stays reserve and is never automatic.
  Runner 2 is retired and is never contacted. (The code default host in
  `remote_work.go` still says `runner-2`. The installed config says
  `runner-3`. v2 removes the default and fails closed without fleet
  inventory.)
- One queue view in the app: each row shows its placement, its state, and an
  ETA.

### 4.3 Readiness states, never reserve for work that is not ready

States: `queued`, `host_blocked`, `ready`, `running`, `done`. Only `ready`
work can hold capacity or a drain. This generalizes the v1.1 fix: any
operation waiting on something other than capacity (host pressure, browser
pause, storage) is visible but never blocks others.

### 4.4 Closed-loop placement

For each class and repository the loop keeps measured numbers: local run p50
and p95, remote preparation (source collect, upload), remote run, and cleanup.
Most of this already exists in `remote_work_finished` events. For `either`
work, the placer picks the lower expected completion time:

`local ETA = local queue wait estimate + local runtime`
`remote ETA = remote queue wait + preparation + remote runtime`

When the Mac is at warning or red, local ETA includes the host-blocked wait,
so work flows remote exactly when the Mac is the bottleneck. A remote failure
caused by infrastructure never falls back to the Mac (828b1ea rule). It
reports and retries on the same bound host.

### 4.5 Backfill with an estimated start time

Replace the bypass counter and age rules with reservation backfill: a
protected ready head gets an estimated start time from lease runtime
estimates. A successor may start only if its p95 runtime ends before that
time, or if it does not use the head's resources. This keeps the "no
starvation" promise and removes idle capacity during drains.

### 4.6 Liveness guard

A watchdog in the resident: if there is free capacity, a ready waiter that
fits, and no admission for 30 s, it records `queue_stall`, raises a bell
alert, and admits the oldest ready waiter that fits. It is a safety net, not
the scheduler. `work evaluate` adds replay of the 2026-11-15 events and fails
if any interval shows free capacity with fitting ready waiters.

### 4.7 Rollout gates

1. Shadow: v2 decides beside v1 (the shadow fields already exist:
   `CandidateSchedulingPolicy`, `ShadowSelectedOperationID`). Seven days of
   replay on real events. v2 must show zero stall intervals and lower p95
   wait for every class.
2. Canary: v2 is the default for `express-*` and `test`. v1 stays default
   for the rest. Two days.
3. Default: `placement-v2` for all. v1 stays selectable for one month.

Diagnostics to watch, not acceptance thresholds: remote share, p95 wait per
class, stall intervals, Mac swap-out during agent load, runner-3 load.

### 4.8 App work (NDev Pressure)

- Work Queue: placement column, readiness state, ETA, remote slot bar beside
  the local capacity bar, `parked` badge in place of a blocking `held`.
- Hetzner tab: remote queue and per-repository remote share.
- Bell alert on `queue_stall`.

## 5. Open items

- [ ] Install the v1.1 binary and confirm in live status that parked
      reservations no longer block (needs the old waiters to drain).
- [ ] Swift app: show `pressure_parked` as `parked`, not `held`.
- [x] Fix the `runner-2` code default in `remote_work.go`.
- [ ] Make `TestGoReuseSingleflightAndExplicitExecution` and
      `TestGoProfileSurvivesFailureTimeoutAndSignal` pass on runner-3
      (they fail on clean HEAD there).
- [ ] Record a tool family (closed vocabulary, never argv) on work events.
      Today events carry only a command digest, so nobody can say which
      local work was Go, Swift, Node or `bash -c` wrapped. Closed-loop
      placement needs it.
- [ ] v2 slices 4.1 to 4.8, each with its own commit and corpus entry.
