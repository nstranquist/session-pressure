# 2026-11-24 — GPUI Preview Parity (`WORK-SP-12`)

- **Work item:** `WORK-SP-12`
- **Scope:** `apps/pressure-ui/crates/pressure-core` and `apps/pressure-ui/crates/session-pressure-ui`

## Summary

Brought the Rust GPUI preview app (`apps/pressure-ui`) to parity with Swift NDev Pressure's resource-admission, alert-routing, stall-alert, and policy-threshold improvements:

1. **Resource-admission & work headroom decoding + UI:**
   - Extended `WorkStatus` (`admission`, `parked_weight`, `is_resource_admission`, `active_lease_count`, `parked_lease_count`), `WorkLease` (`parked`, `parked_at`, `predicted_peak_mb`, `predicted_median_mb`), `WorkWaiter` (`pressure_parked`, `selected_at`, `start_expired`, `predicted_peak_mb`, `is_pressure_reserved`), and `DoctorWork` (`is_resource_admission` including the `1048576` sentinel).
   - Added `WorkMemoryHeadroom`, `WorkHeadroomEnvelope`, `RecoveryClearEnvelope`, and `PressureClient::{work_headroom, work_placement, recovery_clear}`.
   - Updated the status bar, Overview, and Work Queue pane to display live memory headroom (`avail − reserve − in-flight · swap-out`), active/parked counts, per-item `~peak` memory and `start expired` / `pressure parked` indicators, and confirmed recovery-hint clearing in Monitor.
2. **Swapped alert route fixes & work stall alerts:**
   - Added `WorkPlacementEnvelope`, `WorkPlacementReport`, `WorkQueueStall`, `ParkedLeaseStall`, `WaiterETA`, `RunnerSlots`, and `PressureBoard::alerts(...)`.
   - Routed storage warnings to `Section::Storage`, disk-health probe errors to `Section::DiskWrites`, active work queue stalls and parked-lock stalls to `Section::Work`, and pending recovery hints to `Section::Monitor`, with clickable alert buttons in Overview and stall banners in Work Queue.
3. **Policy-driven thresholds:**
   - Decoded `PolicyThresholds`, `PolicyWorkLimits`, and `PolicyLaunchAdmission` on `PressurePolicy`, plus `swap_out_mb_per_s` and `swap_thrash_latched` on `PressureSnapshot`.
   - Wired policy-driven level evaluators (`free_memory_level`, `host_cpu_level`, `agent_rss_level`, `agent_rss_ceiling_mb`, `swap_level`) into Overview metrics and rendered effective thresholds and admission settings in the Policy pane.

## Verification

- `cargo test -p pressure-core`
- `make desktop-gpui`
- `make secret-scan`
