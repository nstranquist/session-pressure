# Closed-loop placer, parked lock-holder stalls, and soak hardening

Owner: antigravity/3562e612. Status: implemented, 2026-11-24. Scope: `WORK-SP-10` and `WORK-SP-9` (Phase 1 pre-soak hardening).

## Context

Live workstation diagnostics on 2026-11-24 surfaced two production bottlenecks:

1. **Closed-loop placer cold-start trap and parked lock-holder visibility (`WORK-SP-10`)**:
   - Build invocations carrying temporary output flags (`go build -o /tmp/...`) hashed into a unique `PlacementFamilyKey` on every run, preventing family history accumulation across checkouts and deliveries.
   - Repeated `express-test` runs with `remote_eta_ms` around 48.9 s fell just above the 45 s exploration ceiling, never explored local once, and reported the generic reason `no_local_history`.
   - Long-parked leases waiting on external file locks (such as installer lock chains) showed as muted `idle · parked` with no lock-file attribution in `work inspect`, `work placement`, or the Work Queue UI.
2. **Pre-soak release acceptance blockers (`WORK-SP-9` Phase 1)**:
   - Every standalone CLI command performed three sequential APFS `fsync()` calls in `internal/telemetry/writer.go` (`writeState` pre-append, JSONL append, `writeState` post-append), pushing `product_writer` p95 latency to 57.4 ms against the 25 ms acceptance target.
   - `ReplayWorkEvents` required `candidate.ContentionDecisions >= 20` even on a 710-operation healthy trace where calibrated weights reduced legacy contention from 80 decisions down to 0 with zero queue stalls.
   - `scripts/release/observe.go` judged the desktop app against `ps -o rss=` (191.7 MiB including shared clean framework pages) rather than macOS physical footprint (`phys_footprint`), lacked a single-instance observer lock and `--fail-fast` runaway guard, and permanently failed `ContinuityPassed` on laptop sleep gaps.
   - `NDev Pressure.app` retained heavy transient pane caches after closing its main window into menu-bar-only mode and kept default `URLCache` capacity active.

## Changes

### `WORK-SP-10`: Placer, family keys, and parked lock-holder stalls

- [work_placement_family.go](../sessionpressure/work_placement_family.go): strips build and profile output flags (`-o`, `-coverprofile`, `-cpuprofile`, `-memprofile`, `-blockprofile`, `-mutexprofile`, `-trace`) so builds writing to temporary paths share one stable `PlacementFamilyKey`.
- [work_placer.go](../sessionpressure/work_placer.go): raises `placementExploreCeilingMS` from 45 s to 60 s and distinguishes `remote_above_explore_ceiling` from `no_local_history`.
- [workinspect.go](../sessionpressure/workinspect.go): adds `StallParkedLock` (`parked_lock_holder`), `StallParkedIdle` (`parked_idle`), `ParkedDurationMS`, and bounded read-only `lsof` lock-file inspection (`OpenLocks`, compact `parent/base` labels, 500 ms timeout, max 8 entries).
- [work_placement_view.go](../sessionpressure/work_placement_view.go): surfaces `ParkedStalls` (`parked_stalls`) for live leases parked at least 5 minutes.
- Desktop app ([Models.swift](../apps/SessionPressure/Sources/NDevPressureCore/Models.swift), [WorkPlacement.swift](../apps/SessionPressure/Sources/NDevPressureCore/WorkPlacement.swift), [WorkView.swift](../apps/SessionPressure/Sources/NDevPressure/Views/WorkView.swift), [WorkPlacementCard.swift](../apps/SessionPressure/Sources/NDevPressure/Views/WorkPlacementCard.swift)): highlights leases parked over 5 minutes with amber `check lock` cues, renders open lock files and parked duration in the detail drawer, and displays a long-parked summary banner on the placement card.

### `WORK-SP-9` Phase 1: Writer latency, replay gate, app memory, and runaway-safe soak

- [writer.go](../internal/telemetry/writer.go): keeps `f.Sync()` on the primary JSONL append stream and removes the two redundant `fsync` barriers on transient `writeState` temp files while preserving atomic `CreateTemp` + `Rename` crash-gap accounting.
- [workreplay.go](../sessionpressure/workreplay.go): satisfies `MinimumSampleMet` on `>= 100` closed operations when candidate contention `>= 20`, legacy calibration contention `>= 20`, or the host ran a healthy zero-contention trace with zero short-runtime long waits.
- [observe.go](../scripts/release/observe.go):
  - Guards each output path with a non-blocking exclusive `filelock` (`<out>.lock`) to prevent duplicate overlapping observers.
  - Adds `--fail-fast` to terminate immediately on latched unrecoverable soak failures instead of looping for days.
  - Distinguishes host sleep (wall jump with bounded monotonic awake gap) from observer hangs, tracks `sleep_pauses` and `sleep_pause_seconds`, extends `deadline` by sleep time, and enforces a hard `max_deadline` ceiling so sleep extensions can never become runaway schedules.
  - Measures macOS `phys_footprint` (`app_peak_footprint_mib`, `app_memory_budget_metric`) alongside `app_peak_rss_mib` and checks the 128 MiB parked-app budget against `phys_footprint` when available.
- [acceptance.go](../scripts/release/acceptance.go): keeps `product_writer` held strictly to `<= 25 ms` p95 and aligns the external factory `cli_writer` (preferring `cli_writer_current_controller` when evidenced) with `audit`'s `<= 100 ms` fail ceiling.
- [PressureStore.swift](../apps/SessionPressure/Sources/NDevPressure/PressureStore.swift) and [NDevPressureApp.swift](../apps/SessionPressure/Sources/NDevPressure/NDevPressureApp.swift): zeroes `URLCache.shared` memory/disk capacity at launch and clears heavy transient pane caches (`workDetailEvents`, `workInspect`, `diskWriteHistory`, `diskWriters`, `storageProviders`, `storageInventory`, `storageReceipt`, `storagePreview`, `navigationHistory`) when the main window closes into menu-bar-only mode.

## Downstream note

`nicos-commit-delivery/go.mod` currently pins `session-pressure` at `669bc82` while two parallel deliveries are active in that repository. Bump that module pin in a clean follow-up once those deliveries clear.
