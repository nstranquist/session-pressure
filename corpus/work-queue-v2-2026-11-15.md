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

## 4.9 Implementation log

- 2026-11-15, v1.1 live. Released `5f02c87` through the documented flow
  (bundle, Darwin cross-build on runner-3, `session-pressure-release
  install --expected-current 14c7d24`). Finding: coordinated work did not run
  that release. It runs the launchd helper artifact, which was still
  `072e4d6` from 2026-11-11, so no queue fix from any agent since then had
  been live for hook-wrapped work. Promoted with `session-pressure monitor
  install` (enforcement kept from the persisted policy). `artifact status`:
  helper `5f02c87`, drift false. Live status after: selector schema 8, queue
  empty, three operations held only at the host memory gate.
- 2026-11-15, v2 slice 1, light-command classification
  (`sessionpressure/work_toolfamily.go`). The live holds were
  `bash -c xcrun devicectl device info detail` queued as `build` (weight 5)
  and `npm ci` queued as `test`. The queue now owns classification for
  shapes that are light by construction: whole-project dependency installs
  (`npm|pnpm|yarn|bun ci|install|i` with only flags, `go mod download`) map to
  `install` (1), and read-only Apple queries (`xcrun devicectl list`,
  `devicectl device info`, `simctl list|getenv`, `xcrun --find`,
  `--show-sdk-*`) map to `express-test` (1). An explicit `heavy` request is
  kept. Compound commands keep their strongest statement. Proof: suite on
  runner-3 passes (both packages, two HEAD-failing tests skipped), vet
  clean.
- 2026-11-15, v2 slice 2, tool-family telemetry. Work events carry
  `tool_family` from a closed vocabulary (go, node, rust, swift, xcodebuild,
  xcrun, make, python, ndev, shell, other), validated on write. A
  `bash -c` compound takes the family of its heaviest classified statement.
  `work stats` adds `by_tool_family` (operations, outcomes, wait total and
  p95, runtime), sorted by total wait, with older rows under `unrecorded`.
  This is the measurement the closed-loop placer and slice 3 need. Proof: HEAD
  plus only these files passes both packages on runner-3. (The shared tree
  did not build at the time because of another agent's unfinished
  `sampler.go` change.)

- 2026-11-15, release `e5bb660` (CLI and launchd helper, drift false). Live
  check at 15:23Z: a new `npm ci` requested as `test` was queued as
  `install` with `tool_family: node`.

### Slice 3, Node verification on runner-3 (landed 2026-11-15)

- Runner prerequisite: nicos-ops `tools/runner-tools/pnpm` installs pinned
  pnpm 11.22.0, 11.15.1, 9.15.0 and 9.12.0 under
  `/srv/build/opt/pnpm-<v>`, each SHA-256 pinned and cross-checked against
  the registry sha512. Installed on runner-3 by job
  `e14a5882-7823-4deb-b088-ddcab12e7d5e` (tests, vet and four
  `pnpm --version` checks passed). The source waits in the nicos-ops delivery
  queue (delivery `a3ba107d`). The install is live already.
- Release `d91858c`: `remote_node.go` plans a Node job (Git root, lockfile,
  Node pin from `.node-version` or the Mac, pnpm pin from `packageManager`),
  installs from the lockfile on the runner with a private HOME and npm cache,
  and runs only verification (test runners, `tsc --noEmit`, eslint). Write-back
  flags, compound shell commands and unpinned versions stay local with a
  `remote_work_declined` event. Enabled with `"node": true` in
  `remote-work.json`.
- Proof: delivery-queue-3d `npm test` on runner-3, 77 files and 266 tests
  passed, 34 s wall, about 0.6 s Mac CPU, upload 6.7 s and 22.6 MB.
  patina-ui `pnpm test` (pnpm 11.15.1), 19 files and 376 tests passed.
- Follow-up guard (this commit): a package script that calls a Mac-only tool
  (xcrun, xcodebuild, swift, osascript, launchctl, codesign and others),
  directly, in a pre or post script, or one `npm run` level deep, stays local
  with reason `package script uses Mac-only <tool>`. Tested on runner-3.
- Not done: the generic remote job refactor of `TryRemoteWork`.

### Slice 4, closed-loop placer (landed 2026-11-15, commit 6a6141c)

- Data that set the design (2026-11-14 to 15 events): local Node `test` run
  p50 5.5 s, remote Node job p50 32 s. Remote Go `test` p50 161 s, of which
  `runner_wait_and_run_ms` p50 117 s. Local waits have long tails (p90 250 s
  for `test`, 600 s for `build`). So the right answer depends on the Mac's
  state at the moment of the request, not on a static rule.
- `work_placer.go`. For an eligible auto-placed Go or Node check, the placer
  keeps it local only when the Mac can start it now (no waiters, no admission
  holds, capacity for its weight, host level normal) and the measured local
  p75 runtime of the same check is below its measured remote p50 duration.
  "Same check" is `placement_key`, an opaque sha256 of cwd and argv, now on
  every work event and every `remote_work_finished` row. With no local
  history, a check with remote p50 at most 45 s runs locally to measure it.
  Busy or unknown keeps remote. Explicit `--placement remote` is never
  overridden. Each decision is a `work_placement_decided` control event with
  both ETAs and sample counts, and a local decision prints one stderr line.
  Opt-in with `"placer": true` in `remote-work.json` (enabled 2026-11-15,
  backup `remote-work.json.bak-claude-placer-20261115`).
- Also in this commit: with no `.node-version`, the Mac's Node major selects
  the runner pin (Mac 26.13.1 runs on 26.13.0). Three declines on
  2026-11-15 had the reason `Node "26.13.1" is not pinned`.
- Proof: package tests and vet passed on runner-3 (two suites skip the
  two tests that fail on clean HEAD there). Release `6a6141c` installed, helper
  promoted, drift false. Live at 16:20Z: delivery-queue-3d `npm test`
  recorded `placement: remote, reason: mac_busy: 8 waiters queued`, then 77
  test files passed on runner-3. The Mac was at memory red then (swap
  9.3 GB, swap-out 64 MB/s), so this host-pressure hold was correct, not a
  queue stall.
- Not yet observed live: a `local_faster` or `explore_local` decision. Both
  need an idle Mac. Check `work_placement_decided` rows after the next quiet
  period.

### Slice 5, stall guard (landed 2026-11-15, commits 0ebf4d4, 4fde84e)

- Condition: free capacity beside a live waiter that fits (heartbeat within
  30 s, not a parked pressure reservation), while work admission allows it,
  with no new lease for 30 s. `admission.Allowed` carries the policy: with
  enforcement on, red refuses and is not a stall; in observe mode red admits,
  so an idle queue at red is a stall. At warning, the derated ceiling is the
  capacity.
- Live (`work_stall.go`, `monitor.go`, helper): the resident monitor runs
  `QueueStallDetector.Observe` on each sample from the published queue
  snapshot (no mutation lock). The first crossing of an episode records
  `work_queue_stall` (operation, class, weight, used, effective capacity,
  selector decision, protected operation) and raises one toast "Work queue
  stalled". The end records `work_queue_stall_cleared` with the duration. Gap:
  the app routes only `ndev-pressure://disk-writes`, so the toast's
  `ndev-pressure://work` link opens the app but not the Work tab. That is
  slice 6.
- Replay: `work evaluate` adds `replay.queue_stalls`, built from work events
  (queue entry = acquired_at - queue_wait_ms, capacity as recorded on the
  events at that time) and the resident level timeline (state transitions,
  heartbeats, resident stops). Only normal-level time is judged, because the
  enforcement mode and warning derate at event time are not recorded.
- First version error, found and fixed the same day: the replay used the
  current policy capacity (1000 since the guards were disabled) for history,
  which produced a false 1117 s stall. 4fde84e uses event-time capacity.
- Proof: tests and vet passed on runner-3. Release 4fde84e installed,
  helper promoted, drift false. `work evaluate --since 24h` on real events:
  3 episodes, 529 s idle beyond the 30 s threshold, longest 518 s
  (12:17Z, 6 of 8 used, a weight-2 waiter fitting, normal level). That is
  before the v1.1 fix went live. No live stall event yet; the queue has run
  at capacity 1000 since 17:13Z.
- Known v1.1 gap, seen in `guards-disabled-2026-11-15.md`: a resumed pressure
  reservation whose owner process kept an older policy (derated capacity 4,
  weight 5) never acquired, while other processes honoured its protection.
  The guard detects this shape. The automatic remedy landed the same day,
  see "Selected-start bound".

### Observe-mode level fix (2026-11-15, with slice 6)

- Found while reviewing slices 4 and 5 under the observe-only policy (all
  guards disabled at 16:27Z, `guards-disabled-2026-11-15.md`). In observe mode
  `ConfiguredWorkHostAdmission` returns `{Allowed: true, Level: normal,
  Source: observe-only}` whatever the host does. The placer then saw a red
  Mac as idle, and the shadow memory admitter copied that `normal` level over
  the resident's real level.
- Fix: `macIdleProbe` also reads the resident `latest.json`. No sample within
  the 2 min TTL means busy ("no fresh resident sample"), and a level other
  than normal means busy ("host level <level>"). `work_headroom.go` no longer
  overrides the resident level with the admission level. Test:
  `TestMacIdleProbeUsesTheResidentLevel`.

### Slice 6, app (landed 2026-11-15, commits 89e394b, fa0d828)

- Shipped: helper fa0d828 installed and promoted (drift false, observe
  policy preserved). The app was installed with `make install-headless`, so
  a running app picks it up at its next launch. Go tests and vet passed on
  runner-3. The Swift suite passed locally (150 tests).

- Helper: `session-pressure --json work placement [--window D]
  [--no-runners]` (`work_placement_view.go`). It is one read-only join:
  - placer decisions (`work_placement_decided`), grouped by reason prefix,
    with the last 10 in full;
  - offloaded runs (`remote_work_finished`): count, non-zero exits, p50 and
    p90 duration, last 10;
  - Mac-only local placements (`work_local_placement`) by reason;
  - the stall guard's last episode. An episode is open only while its
    waiter is still queued, because a stopped monitor cannot record the clear;
  - a start ETA for each waiter. This is a strict FIFO simulation from the
    7-day class p50 runtimes. An unmeasured class makes every later ETA
    unknown, not guessed. It ignores pressure holds and lookahead, and the
    payload states that basis;
  - live slots of the active fleet hosts only (`runnerfleet.Observe`; reserve
    and retired hosts are never probed). The 8 s bound and 2.7 s measured
    cost are the reasons the app polls it slower than work status.
- App: a "Placement & offload" card in the Work view (offload and placer
  chips, a runner slot bar with queued overflow, decision and remote-run
  stats, reasons, recent decisions). A red stall banner shows at the top
  while an episode is open. Each waiter row has an ETA. The
  `ndev-pressure://work` deep link, which the stall toast opens, now selects
  the Work view. The placement view refreshes at most every 30 s while the
  Work view is focused, or every 5 s during a stall. The parked badge from
  the v1.1 work ships in the same build.
- Evidence: offscreen `ImageRenderer` renders (no window) of a fixture with
  a stall and of the live payload. The Swift suite passed (150 tests). The
  first render showed three defects that are now fixed: "in 0s" age text,
  `mac_busy` reasons split by waiter count, and "remote failed" counting
  real test failures as runner trouble (now "remote exit ≠ 0" with help
  text).
- Live numbers at 17:50Z (6 h window): 16 placer decisions, 0 kept local.
  Reasons were 12 `no_local_history` and 4 `mac_busy`. There were 142 remote
  runs: 47 with a non-zero exit, p50 2 min, p90 10 min. Runner heavy slots
  were seen at 3/8 to 13/8 (13 includes queued jobs).
- Finding for the placer: placement keys hardly repeat. Of 35 keyed remote
  runs there were 23 distinct keys, and only 3 keys had the 2 successful
  samples the placer needs. Agents vary `-run` patterns and work in temp
  checkouts, so the exact cwd+argv key rarely matches. The placer will stay
  remote-only until a coarser fallback key exists (open item below).

### Placer family key (2026-11-15)

- Problem (measured): of 35 keyed remote runs, 23 distinct exact keys, and
  only 3 had the 2 successful samples the placer needs.
- `PlacementFamilyKey` (`work_placement_family.go`, prefix `family-v1`):
  - project identity by declaration: Go module path, package.json name,
    Swift package directory name, git root name, else the cwd itself;
  - cwd relative to that root, so temporary checkouts of one repo match;
  - argv[0] by base name; test selection and verbosity flags dropped
    (`-run`, `-skip`, `-count`, `-timeout`, `-shuffle`, `-v`, `-failfast`,
    `-json`, `-t`, `--testNamePattern`, `--grep`, `--reporter`,
    `--verbose`, `--silent`); absolute paths inside the root made relative;
    positional arguments sorted as a set. Build-changing flags (`-race`,
    `-tags`) and the package set stay in the key.
- Recorded on every work event (`placement_family_key`, validated digest)
  and on `remote_work_finished` (Go and Node). The placer reads both scopes
  in one pass and uses the exact key when it has 2 samples on both sides,
  otherwise the family. `work_placement_decided` records `history_scope`
  (`key` or `family`) and the family digest, and the stderr line names the
  scope.
- Known trade-off: a family mixes `-run TestOne` with whole-package runs, so
  its p75 local and p50 remote come from a mixed population. Both sides mix
  the same way, and the loop corrects a wrong call because each run adds a
  sample. History starts empty: rows before this release carry no family key.
- Not changed: the headroom predictor still uses the exact key tier.
- Shipped: commit 8753188, helper installed and promoted (drift false,
  observe policy kept). Tests and vet passed on runner-3. Live check at
  about 18:10Z: a real `go test` produced a decision with `history_scope:
  key` and the family digest, and its `remote_work_finished` and work events
  carry `placement_family_key`. The Mac was at warning, so the decision was
  `mac_busy: host level warning`, which is the observe-mode level fix working.

### Selected-start bound (2026-11-15, Nico: "Yes, build it")

- Problem: each process selects with its own policy. A head whose owner
  kept an older, derated policy never saw itself fit, while every other
  process saw it selected and waited (or honoured its drain). Free capacity
  sat idle with no end.
- Rule (`worklease.go`, `workSelectedStartBound` = 20 s): when a process
  other than the owner sees the queue head selected to start, it stamps
  `selected_at` on the waiter record under the state lock. After 20 s
  without a start (ten 2 s retries by the owner), selection, in both the
  lookahead and FIFO selectors, moves the waiter behind every waiter that
  is trying to start. It keeps its record, accrues no bypass, holds no
  drain, and can still start when nothing ahead of it fits. 20 s is below
  the 30 s stall-guard threshold, so the guard reports only stalls this
  rule cannot clear.
- Stamp rules: only the head is stamped, never a successor that a view
  selected by passing over the head. The first implementation stamped any
  selected waiter. A stale owner's view selects the successor, so both
  would have expired and the stall would have come back; the design was
  fixed before any test ran. Non-owners clear the stamp when the head is no
  longer selected (for example, capacity was taken), unless it has already
  expired. The owner never starts or clears its own stamp, because its view
  is the one in question.
- Visible in status as `start_expired` and `selected_at` on the waiter.
  Built-in `work evaluate` check: `stuck-selected-head-does-not-idle`.
- Tests: selector demotion in both policies; stamp rules; and a coordinator
  reproduction (owner acquiring at capacity 4, successor at 8). The
  successor is blocked for 20 s and then starts, and the demoted head still
  starts with a fresh view.
- Compatibility: older helpers drop `selected_at` on rewrite. That only
  restarts the bound.
- Shipped: commit 6085e2a. Tests passed on runner-3; the `go vet` run
  before the eval-check addition passed. Helper installed and promoted
  (drift false). On the live policy `work evaluate` passes
  `stuck-selected-head-does-not-idle`, with 23 of 25 checks passing.
- Policy state at release (2026-11-16 02:16Z): profile `interactive`,
  `enforce_admission` true, work capacity 16. Another actor set this after
  the guards-disabled period (backup `policy.json.bak-20261115T190053Z-
  before-cap16`). `monitor install` without `--enforce` rewrites the policy
  file unchanged (it only sets `enabled`, already true), so this release
  did not change it.
- The 2 failing checks, `calibrated-classes` and
  `express-lookahead-under-benchmark`, fail the same way with the previous
  helper 8753188 (22 of 24). They come from class weights calibrated for
  capacity 8 now running under capacity 16, not from this change.
- Decision (Nico, in chat, 2026-11-16): capacity 16 stays, and higher
  capacity is preferred ("no 16 is good, and more might be better. higher
  capacity is better"). Do not restore capacity 8 or the warning drop to 4.
  Consequence for this workstream: memory, not the unit budget, has to be
  the guard that stops overload, so promoting the memory-headroom admitter
  matters more. The two failing checks assume capacity 8 and should become
  capacity-relative.
- Seen while testing: `TestRemoteGoInvocationLightFollowsTheAdmittedLane`
  (commit 8c1ea78, another session) fails inside a runner job. It reads
  `os.Environ()`, and the runner sets `NCD_RUNNER_LANE`, so its "older
  runner" case cannot hold there. It was skipped for this run and left to
  its owner.

### Path to higher capacity (2026-11-16, Nico: "Yes, build it")

Goal: Nico prefers capacity 16 or higher. Memory, not the unit budget, has
to stop overload.

- Eval checks are capacity-relative. `calibrated-classes` no longer requires
  `2 x build > capacity`, which made two builds mutually exclusive only at
  capacity 8. `express-lookahead-under-benchmark` derives the occupied weight
  from capacity (`max(benchmark, capacity - build + 1)`). Both pass with the
  live weights at capacity 8, 16 and 32 (test
  `TestEvaluationChecksHoldAtHigherCapacity`).
- Process-tree peak memory (`tree_rss*.go`). The work-run process samples
  its command's tree once a second. On macOS it uses one `kern.proc.all`
  sysctl for pid and ppid, then `proc_pid_rusage` for tree members only; on
  Linux it reads `/proc`. Work events carry `command_tree_peak_rss_mb` and
  `command_tree_peak_samples`. A tree shorter than one sample can still be
  missed; zero samples means the command ended first.
- Headroom rule v2 (`mac-headroom-p90-v2`): the predictor uses
  max(tree peak, wait4 peak). v1 rows stay valid on read and age out of the
  72 h window. Until then, class and workload p90s still mix in v1
  single-process samples that can be too low.
- Enforce mode, off by default: `work_limits.memory_admission` = `shadow`
  (default) or `enforce`. With admission enforced, enforce holds a new
  operation before its lease only when its predicted peak does not fit
  headroom (`no_headroom`) and at least one other coordinated lease runs.
  So an empty queue never waits on memory it cannot free. A missing sample
  fails open. Red, critical and swap thrash stay with the existing level
  and swap gates. A lease this process already owns is never held. The hold
  uses the existing `memory` dimension, with a reason starting "memory
  headroom:". `work headroom` reports the mode.
- Not switched on. Turning on enforce needs Nico's approval, after the
  tree-peak history has filled (about 24 to 72 h of runs).
- Shipped: commit dcea11f. Tests and vet passed on runner-3 (Linux
  `/proc` sampler); the new tests also passed on macOS, including a live
  tree sample. Helper installed and promoted (drift false). Policy
  unchanged: interactive, enforced, capacity 16, memory admission shadow.
- Live proof right after release: three real `browser` jobs recorded wait4
  peaks of 179 to 181 MB and tree peaks of 455 to 724 MB (2 to 3 samples
  each). The old figure undercounted them by up to 4x. `work evaluate`
  passes 25 of 25 at capacity 16, against 23 of 25 before. `work headroom`
  reports rule v2 and mode shadow.

## 5. Open items

- [ ] Memory-headroom admission (request from the commit-delivery session,
  2026-11-15, parallel-landing note P4, delivery 3b9a7f2c: remove slots,
  admit by predicted peak memory against measured headroom). Nico decided in
  chat on 2026-11-15: the capped red lane stays (attended chat work and
  `delivery-darwin`). Nico said "Yes, start it" for the headroom view and the
  shadow admitter.
  - Done 2026-11-15 (ae71ac0, 9f647d4, f3b34f6, live, helper promoted):
    `session-pressure --json work headroom` and shadow decisions on every
    queued and acquired work event (`shadow_memory_decision`,
    `shadow_predicted_peak_mb`, `shadow_prediction_source`,
    `shadow_headroom_mb`). Model in `work_headroom.go`: headroom = free
    percent x physical - physical/8 - in-flight, where in-flight is the class
    p90 predictions of running leases minus measured leased RSS. Prediction
    is p90 by placement key (3 or more samples), then workload (5 or more),
    then class (5 or more), else 1024 MB, over 72 h of completed work.
    Refusals: no fresh sample, critical, red (except red-lane candidates),
    swap thrash latched or swap-out at 32 MB/s or more, no headroom.
  - Findings while building: the generic event Read cost 1.1 s for 72 h
    (whole-operation load and lifecycle validation). A narrow completed-row
    query brought `work headroom` to 0.08 s, and the slice 4 placer uses it
    too. A storage-warning admission carries no memory snapshot, so the probe
    reads the resident sample directly. At 16:47Z, 165 MB/s of swap-out sat
    beside 3 GB of free-percent headroom, which is why swap-out refuses.
  - Known limit: `command_peak_rss_mb` is the largest single process of a
    command (wait4), not its tree. Parallel test binaries can exceed it.
    Promotion condition (agreed with the commit-delivery session,
    2026-11-15): the placement-key and workload tiers are not promoted for
    multi-process commands (`go test`, Swift builds) until the tree peak is
    measured.
  - Review fixes the same day: completed rows with a peak under 8 MB are not
    samples. They are argv probes or wrappers whose work ran in a process
    wait4 never saw. 440 s express-build runs reported about 1 MB, and so did
    two `delivery-darwin` probes. The estimator is versioned
    (`mac-headroom-p90-v1`, in `shadow_memory_rule` and the view's `rule`),
    so replay never mixes it with the runner's memadmit (p95 x 1.10,
    5 samples, 2 GiB default).
  - Next: after 24 to 48 h, compare shadow decisions with live outcomes in
    `work evaluate` (shadow admit while live waited; shadow refuse while
    live ran, and what pressure followed). Decide the CPU bound from that
    data. Promotion needs Nico's approval. Owner: claude (work queue v2).
- [x] Delivery Darwin ordering (request from the commit-delivery session,
  2026-11-15, design note nicos-commit-delivery
  `docs/11-15-parallel-landing/parallel-landing-kep.md` P5, recorded there in
  delivery c5e02bc4). Decision: no protected waiter, because protection holds
  the drain and caused the v1 stall. Done 2026-11-15: `--workload
  delivery-darwin` (set by the delivery worker, never inferred) makes the
  operation interactive with source `workload:delivery-darwin`. It gets
  interactive ordering ahead of unprotected background work with no drain
  hold, and the capped red lane. Nico approved red-lane use in chat on
  2026-11-15. The lane caps are unchanged: memory or swap red only, weight at
  most FastLaneMaxWeight (2), known p90 peak RSS within the limit, and at most
  2 red-lane units of coordinated work. A weight-5 Swift build still waits at
  red. `--no-interactive` with this workload is refused. No priority pin.
  Tests and vet passed on runner-3. Owner: claude (work queue v2).
- [ ] Install the v1.1 binary and confirm in live status that parked
      reservations no longer block (needs the old waiters to drain).
- [x] Swift app: show `pressure_parked` as `parked`, not `held` (slice 6
      build, 2026-11-15).
- [x] Placer fallback key (Nico: "Yes, build it", 2026-11-15). See
      "Placer family key" in 4.9. Next check: after 24 h, count decisions by
      `history_scope` and compare family-scoped local runtimes with the
      remote p50 they beat. Owner: claude (work queue v2).
- [ ] Runner gocache-lru: go test jobs on runner-3 wait for
      GOCACHEPROG for more than 6 min while one gocache-lru holds
      `authority.lock` and reads the 26 GB cache under a 2-CPU cgroup cap.
      Two jobs of this workstream hung this way on 2026-11-15. Nico asked the
      "Cost center Mac menu bar" session to fix it. The uncommitted
      `gocachelru/lru.go` edit seen at 17:44Z does not belong to this
      workstream.
- [x] Fix the `runner-2` code default in `remote_work.go`.
- [ ] Make `TestGoReuseSingleflightAndExplicitExecution` and
      `TestGoProfileSurvivesFailureTimeoutAndSignal` pass on runner-3
      (they fail on clean HEAD there).
- [ ] Record a tool family (closed vocabulary, never argv) on work events.
      Today events carry only a command digest, so nobody can say which
      local work was Go, Swift, Node or `bash -c` wrapped. Closed-loop
      placement needs it.
- [ ] v2 slices 4.1 to 4.8, each with its own commit and corpus entry.
