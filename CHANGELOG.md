# Changelog

## Unreleased

## 0.5.2 — 2026-11-02

### Fixed

- `storage plan` and `storage apply` now fail closed. The headline
  `estimated_reclaimable_bytes` counts only measured reclaim (`exact_gc`,
  `docker_system_df`); inventories and upper bounds stay visible per provider
  but no longer inflate the total. Go build cache cold bytes no longer satisfy
  a shortfall, because the only supported Go action is a full wipe.
- Simulator devices/runtimes and the retained Nico archives are now
  report-only. Bulk `simctl delete all` and whole-archive deletion are
  disabled until individual selection and recovery review exist.
- `storage apply` reports an `outcome` (`completed` or `no_action`) and records
  a `skipped_providers` reason when the free-space target is already met.
- The `brew-cache` summary now states that `brew cleanup --prune=all -s` also
  removes old installed formula versions.

### Documentation

- Added [Storage reclaim](docs/storage-reclaim.md) covering plan limits,
  provider effects, and archive checks.

## 0.5.1 — 2026-11-02

### Fixed

- `io health` on a Mac with no SMART-capable NVMe device (VMs, CI runners,
  external boot) now reports `available: false` with
  `reason: nvme_smart_not_found` instead of failing with
  `IOReturn 0xe00002f0`.

## 0.5.0 — 2026-11-02

### Added

- **Work inspect.** `work inspect --operation-id ID` returns a live brief for a
  lease: process tree, origin (agent, schedule, or manual), stall class,
  runtime against the class p95, recent output, and suggested actions. Live
  argv and output are read for inspection only and are never persisted to
  work events.
- **Confirmed work actions.** `work cancel --confirm` signals the live lease
  process tree and releases the lease. `work reschedule --confirm` cancels a
  scheduled lease and asks the scheduler to run the job again; it refuses,
  leaving the lease running, when the scheduler integration is not installed. `work handoff`
  writes an operator brief that an interactive agent can pick up. Destructive
  actions require `--confirm` on the CLI. The opt-in local control API exposes
  the same actions as `work.inspect`, `work.cancel`, `work.reschedule`, and
  `work.handoff`.
- **Schedule origin on leases.** Leases started by a scheduler carry the
  schedule ID, run ID, and trigger so `inspect` and `reschedule` can attribute
  them.
- **Optional Scratchpad capture.** `work handoff` also tries to capture the
  brief into an external Scratchpad "Agents" note. When that integration is not
  installed, the brief is still returned and the capture error is reported in
  `scratchpad_error`. See [OPEN-CORE.md](OPEN-CORE.md).
- **Bounded work output tee.** `work run` keeps the last 32 KiB of a lease's
  output in an owner-only file under the state directory so `inspect` can show
  it. Tee files older than one hour are pruned on the next run.
- **Monitor self-test scope.** `self-test --scope monitor` verifies a healthy
  observe-only resident, a proven baseline, the immutable artifact, and the
  weighted-work path. The strict `operator` scope remains the default.
- **NVMe SMART health.** `io health` reads NVMe SMART lifetime wear (percent
  used, host data written) through Apple's NVMeSMARTLib and appends
  deduplicated samples to `nvme-smart-health.jsonl`. Builds without cgo, and
  non-macOS builds, report the reader as unavailable instead of failing.
- **Full disk-write history.** `io history --full` returns the bounded
  per-hour executable list already stored in `disk-writes-*.jsonl`. The
  compact default stays small.
- **Xcode developer data in storage plans.** Storage plans report Xcode device
  symbols, simulator state, documentation, and derived data. The provider is
  read-only and never grants deletion.
- **`gocache-lru`.** A new `cmd/gocache-lru` binary implements the Go
  `GOCACHEPROG` protocol over a size-capped, least-recently-used on-disk cache
  (default 16 GiB) to bound Go build-cache growth.
- **GPUI desktop client (preview).** `apps/pressure-ui` adds a keyboard-first
  desktop client built on GPUI. Like the Swift client, it is a thin shell over
  `session-pressure --json` and does not sample the host. The Swift client
  also gains a work inspector view.

### Changed

- Package-scoped `go -C <dir>` and absolute `pnpm --dir <dir>` commands receive
  the express work class without a manual override.
- Agent identity treats installed but idle agents as inventory. Only live
  agent-shaped processes without a projected tree are reported as identity
  misses.
- The resident monitor reads work status from an atomic snapshot instead of
  waiting on the diagnostic ledger lock. Sample-phase average, p95, and max
  timings are reported, and budget reasons name the dominant phase.

### Fixed

- Storage cache hotness scans run on a bounded worker pool (8 workers) with a
  90-second timeout, so a very large cache can no longer stall a storage plan.
- `CGO_ENABLED=0 go build ./cmd/session-pressure` succeeds on macOS.
- `session-pressure api` finds the `session-pressure-api` binary that
  `make build` produces and honors `SESSION_PRESSURE_API_BIN`. The
  `ndev-pressure-api` names remain accepted as aliases.

### Build and CI

- `make test`, `make vet`, `make build`, and CI cover `sessionpressure/gocachelru`
  and `cmd/gocache-lru`. CI also builds the CLI with cgo disabled.

## 0.1.0 — 2026-08-18

- Public source: https://github.com/nstranquist/session-pressure
- `storage apply --force` skips reclaim cooldown on a named `--provider` only.
  `--auto-safe --force` is rejected. Ownership, report_only, and `--apply`
  still fail closed.
- Initial extract of engine, CLI, helper, local API, and desktop client.
