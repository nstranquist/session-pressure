# Work queue wait audit and desktop app fixes (2026-11-24)

Owner: antigravity. Status: implemented, 2026-11-24. Work: WORK-SP-2, WORK-903.

## Problem

Live telemetry over 72 hours (2,243 operations, 507.8 s cumulative wait) showed
agents waiting repeatedly in the work queue while the host had 35.6 GB of free
memory headroom:

1. **Phantom browser memory charge (`sessionpressure/browser_admission.go`)**:
   Short-lived attach subcommands (`snapshot`, `click`, `eval`, `screenshot`,
   average runtime 163 ms) were charged the full 928 MB detached Chrome for
   Testing cost and kept charged in `browser-admissions.json` after `Release()`
   until a newer resident sample passed `StartedAt` (up to 90 s). A live
   snapshot held 82 released rows with 0 active sessions totaling 76,096 MB of
   phantom charge, driving available headroom negative and causing 4–16 s
   `BROWSER_NO_HEADROOM` retry stalls.
2. **Invisible browser waits (`sessionpressure/workrun.go`)**:
   `waitForBrowserAdmission` ran before `queuedEvent` without publishing
   `HoldAdmission`, hiding blocked browser agents from `work status` and
   attributing their waits to `other`/`pressure_other`.
3. **Coordinator per-run overhead and dead waits (`workevent_index.go`,
   `work_resource_headroom.go`, `workrun.go`, `worklease.go`)**:
   - `syncWorkEventShard` opened `_txlock=immediate` exclusive SQLite write
     transactions on unchanged shards for every read.
   - `workrun.go` scanned 72 h peak-RSS history twice for browser runs and
     ran full shard/output pruning synchronously on every invocation.
   - `shadowResourceProbe.decide` queried lease session owners from SQLite
     even at `LevelNormal` and for interactive runs where the map is unused.
   - `waitForWorkAdmission` slept for the full 15 s retry interval without
     watching `work-capacity.notify`.
   - Under `admission=resource`, `selectWorkWaiter` only checked weight against
     the `1<<20` sentinel capacity, causing oversized heads to block smaller
     fitting waiters for 20 s (`workSelectedStartBound`) before bypass.
4. **Desktop app (`apps/SessionPressure`) and control-plane defects**:
   - Resource admission rendered raw `10/1048576` capacity bars across Work
     Queue, Overview, Doctor strip, Menu Bar, and CLI board text.
   - `BrowserControlView` instantiated a standalone client and polled every
     10 s outside `PressureStore` visibility/profile controls.
   - `PressureStore` swapped notification bell routes between Storage and Disk
     Writes and omitted active work-queue stalls from the bell.
   - `OverviewView` hardcoded 16 GiB thresholds instead of `policy.thresholds`,
     turned the `observe-only` chip red, and labeled parked reservations `held`.
   - `WorkDetailDrawer` offered a clickable `Run now` button on pressure-reserved
     waiters; `StorageView` ignored `store.storageInventory`; `MonitorView` had
     no confirmed in-app action for `recovery clear`.
   - `sessionpressurecmd/session_pressure_storage_watch.go` hardcoded an
     unconditional `ndev scratchpad` exec inside the open-core repo (WORK-903).
   - `sessionpressurecontrol/server.go` lacked `/v1/pressure/work` support for
     `view=headroom` and `view=placement`.

## Changes

- **Slice 1 (`sessionpressure/`)**:
  - Classify attach-only browser subcommands at 64 MB (`AttachOnly: true`) and
    drop their charge and ledger row immediately on `Release()`, while keeping
    full detached-session retention for launch commands (`open`, external
    scripts).
  - Publish/release `HoldAdmission` during `waitForBrowserAdmission` and record
    browser wait blocker/dimension cleanly on work events.
  - Share one `peakRSSHistory` probe between browser admission and
    `shadowMemory`, skip SQLite write transactions on unchanged shards, add
    `idx_work_events_event_ts`, gate `shadowResourceProbe` owner lookups to
    warning non-interactive runs with `>= 2` running leases, and throttle
    work-artifact pruning to 10 minutes via `work-prune.stamp`.
  - Wake `waitForWorkAdmission` on `watchWorkCapacity` (with a 250 ms debounce
    floor) when held on memory or CPU.
  - Pass `workResourceFitBudget` into waiter selection (`selectWorkWaiterWithBudget`,
    `selectStateWaiterWithBudget`) so smaller fitting waiters bypass oversized
    heads immediately under resource admission while preserving bounded bypass
    protection.
- **Slice 2 (`apps/SessionPressure/`)**:
  - Decode resource-admission fields, predicted peaks, `start_expired`,
    `WorkMemoryHeadroom`, and `RecoveryClearEnvelope`.
  - Render resource headroom in Work Queue and concise active/parked summaries
    in Overview, Doctor strip, and Menu Bar.
  - Move browser control state and polling into `PressureStore`, fix swapped
    Storage/Disk-Health bell routes, add work-stall bell alerts, use policy
    thresholds and `observe-only` in Overview, hide `Run now` on pressure-reserved
    waiters in `WorkDetailDrawer`, use `store.storageInventory` in `StorageView`,
    and add confirmed `Clear recovery hint` in `MonitorView`.
- **Slice 3 (`sessionpressurecmd/`, `sessionpressurecontrol/`, `corpus/`)**:
  - Replace hardcoded `ndev scratchpad` exec in storage watch with opt-in
    `SESSION_PRESSURE_STORAGE_WATCH_NOTE_CMD` (WORK-903).
  - Format CLI `board` text cleanly for resource admission.
  - Add `view=headroom` and `view=placement` (`window`, `no_runners`) to
    `/v1/pressure/work`.

## Verification

- `go vet ./...`
- `go test -race -count=1 ./sessionpressure -run 'TestAttachOnlyBrowser|TestWaitForBrowserAdmission|TestWaitForWorkAdmission|TestResourceAdmission|TestShadowResource|TestMaybePruneWork|TestBrowserAdmission|TestWorkEventIndex|TestSelectWorkWaiter|TestResourceFit'`
- `go test -short -count=1 ./sessionpressure`
- `go test -count=1 ./sessionpressurecmd ./sessionpressurecontrol`
- `cd apps/SessionPressure && swift test` (157 tests in 29 suites)
- `cd apps/pressure-ui && cargo test -p pressure-core`

## Operational follow-ups left for host install

- Rebuild and install the updated `session-pressure` CLI, helper artifact, and
  `NDev Pressure.app` once deliveries land on `main`.
- Clear the stale pending `recovery-hint.json` and 0-byte `*.tmp-*` residue
  left by the 2026-11-23 disk-full event.
- Review `remote-work.json` (`placer: false`) and the high `disk_write_high`
  I/O throttle rate observed on the local host.
