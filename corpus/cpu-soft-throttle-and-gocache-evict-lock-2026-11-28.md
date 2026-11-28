# Separate soft CPU saturation from hard survival blocks and serialize gocache-lru trimming (2026-11-28)

Status: implemented in `sessionpressure` and `sessionpressure/gocachelru`; requires post-land `make release-promote` to update installed `~/.local/bin/session-pressure`, LaunchAgent helper, `gocache-lru`, and front door.

## Problem

Two coupled issues caused valid long-running local work to abort under high CPU while host memory and thermal state were healthy:

1. **Controller hard-blocked work on pure CPU saturation (`sessionpressure/controller.go`):**
   - `ClassifyController` set `BlockWork = true` whenever `cpuStress` triggered (host CPU >= 95% with corroborated rolling or consecutive evidence), even when free memory was 59%, swap-out was 0.0 MB/s, and thermal state was nominal.
   - Downstream consumers (work admission and commit-delivery pre-prepare checks) treated `BlockWork = true` as a hard freeze/OOM survival block and aborted or held valid runs.
   - Additionally, checking `cpuStress` before `thermal == ThermalStateSerious` masked a real serious thermal block under `dimension = "cpu"`, and `AdmissionForSnapshot` could leave `Dimension = "cpu"` when both CPU red and serious thermal occurred together.

2. **Concurrent `gocache-lru` processes competed on per-put eviction (`sessionpressure/gocachelru`):**
   - Because `gocache-lru` runs as `GOCACHEPROG` (one helper process per concurrent `go` command), a full cache near its `-max` cap caused every concurrent client to hold `authority.lock`, re-open and check the legacy `index.jsonl` via a SQLite transaction on every `put`, and run `evictLocked()` with up to 256 separate `synchronous=FULL` autocommit `DELETE` transactions per `put`.
   - When multiple Go builds ran concurrently (up to 9 helpers observed burning ~71% aggregate CPU), processes contended continuously over `authority.lock` and eviction passes.

## Changes

### 1. Soft CPU throttle vs hard survival block (`sessionpressure/controller.go`, `sessionpressure/types.go`)

- Added `ThrottleWork bool` (`json:"throttle_work,omitempty"`) to `ControllerDecision` as a non-blocking soft-throttle signal.
- `BlockWork = true` is now reserved exclusively for real survival risks:
  - Memory `>= LevelCritical` or `>= policy.BlockNewAt`
  - Thermal `ThermalStateCritical` or `ThermalStateSerious`
- `thermal == ThermalStateSerious` is evaluated before `cpuStress`, and `AdmissionForSnapshot` upgrades `Dimension` from `"cpu"` to `controller.Dimension` whenever `controller.BlockWork` is true so a serious thermal floor is never masked by simultaneous CPU saturation.
- Corroborated `cpuStress` keeps `Mode = ControllerModeStressed`, `CPUStress = true`, `ThrottleWork = true`, and `Dimension = "cpu"` for clear telemetry and UI visibility without setting `BlockWork = true`. `AdmissionForWorkSnapshot` therefore allows heavy work under pure CPU saturation while memory and thermal floors still fail closed immediately.
- Non-external `SwapThrash` sets `ThrottleWork = true` without setting `BlockWork = true`; external `SwapThrash` leaves `ThrottleWork = false`.

### 2. Non-blocking singleton eviction lock and batched SQLite deletes (`sessionpressure/gocachelru/lru.go`, `sessionpressure/gocachelru/index.go`)

- Preserved `GOCACHEPROG` multi-client correctness (concurrent `go` commands still get their own stdin/stdout helper) while adding a non-blocking singleton `evict.lock` (`LOCK_EX|LOCK_NB`) in `tryEvict`:
  - `putReader` and `Close` release `authority.lock` before calling `tryEvict`, preventing lock-order inversion and avoiding blocking puts during eviction.
  - When another `gocache-lru` process already holds `evict.lock`, peers skip eviction cleanly and immediately (`evictSkippedBusy`).
  - Mid-build puts share a short cross-process cooldown (`defaultPutEvictCooldown = 2s`, bypassed after `defaultPutEvictBytes = 16 MiB` or on `Close`) stamped into `evict.lock` to avoid back-to-back eviction storms when remaining objects are pinned or inside the grace window.
- Batched all `DELETE FROM entries` in `evictLocked` into one SQLite transaction (`commitEvictionLocked`) instead of up to 256 individual `synchronous=FULL` fsyncs, and recorded durable eviction/skip counters (`last_evict_at`, `evict_runs_total`, `evict_removed_total`, `evict_reclaimed_bytes_total`, `evict_skipped_busy_total`, `evict_skipped_cooldown_total`) in `index.sqlite`'s `meta` table inside the same transaction.
- Pushed the `graceWindow` cutoff into the SQLite `entries_by_kind_access` index query (`WHERE kind = ? AND access <= ?`) so in-grace caches return zero rows in O(log N).
- Cached `os.Stat` size and mtime of `index.jsonl` per client after sync so unchanged legacy journals do not open the file or start a SQLite transaction on every `put`.
- Added capped exponential backoff (5 ms to 50 ms) in `withLockWithin` to reduce spin CPU during brief `authority.lock` contention.

## Verification and release

- Verified with `go vet ./...` and full `go test -count=1 ./...` across all packages, including new controller and `gocachelru` regression tests.
- Commit delivery (`.ndev/commit-delivery.yaml`) runs Linux and Darwin test gates and lands on `main`, but does not automatically rebuild the installed host binaries. After landing, run `make release-promote` to rebuild and atomically install `~/.local/bin/session-pressure`, `session-pressure-helper`, `gocache-lru`, and the recorded front door.
