# Changelog

## Unreleased

### Added

- **Lease CPU: measured cores against the cores a weight reserves.** When a
  leased command exits, its terminal event records its own `wait4` rusage:
  CPU time of the command and every descendant it waited for
  (`command_cpu_ms`), the largest RSS among them (`command_peak_rss_mb`), and
  the cores its weight reserves at nominal capacity (`lease_reserved_cores` =
  weight / capacity x logical CPUs). It needs no sampling. The work report adds
  `lease_cpu`, per class: average, p50, and p90 cores, reserved cores, and
  utilization. The idle-host retune hint names each class with 10 or more runs
  that used under half of its reserved cores. The `acquired` event also records
  `queue_coordinated_inventory_age_seconds`, because the resident's leased-work
  figure can predate the queue block by a full sample interval.

- **Queue waits carry host CPU evidence.** A `work run` that queues behind
  weighted capacity records the host CPU and the leased-work CPU of its first
  block on its `acquired` event (`queue_host_cpu_*`,
  `queue_coordinated_cpu_*`). The work report counts queue waits of 30 s or
  more with that evidence (`queue_waits_with_cpu_evidence`) and the ones that
  ran while the host was under 50% CPU (`queue_waits_idle_host`). From three
  idle-host waits, the retune hint says that reserved weight, not CPU, held
  agents back, and names class weights and work capacity as the levers.

- **External-storm release for the swap-thrash derate.** Holding new heavy
  work only helps when agents are part of the memory demand. The resident now
  attributes each latched episode with `swap_thrash_cause`: `agents` (leased
  work RSS at or above `work_limits.swap_thrash_agent_share_percent` of RAM,
  default 25), `unattributed` (leased work is small, but not yet for the
  window), or `external`. When leased work stays small for
  `work_limits.swap_thrash_external_release_seconds` (default 300; negative is
  strict, never release) while the storm goes on, the episode is external and
  the derate stops deferring until the latch releases. Leased work that grows
  past the share re-engages it; the proof is sticky for the episode and for
  one window after release, so an ongoing outside storm is not charged twice.
  Missing inventory or memory size counts as small leased work. Work let
  through is labelled `swap_thrash_external`. Before, a browser- or
  editor-driven storm held every queued build for the whole storm.
- **Swap-thrash history.** The resident appends a `swap_thrash_transition`
  telemetry row on latch entry, release (with `episode_seconds` and
  `external_seconds`), and each derate release or resume, with swap-out,
  swap use, and leased-work RSS. Heartbeats carry `swap_thrash_latched` and
  `swap_thrash_cause`. Storm length was not reconstructable before.
- **Measured derate cost.** `work stats` has a `swap_derate` block: deferred
  operations, external bypasses, wait (total, p95, maximum, per day), resident
  episodes (count, latched and external time, longest, interrupted), and a
  `verdict` of `unexercised`, `within_budget`, or `over_budget` (more than 30
  minutes per day, or one wait over 15 minutes), with loosening advice.
  `doctor` explains a live storm, names the largest non-agent memory users
  when it is external, and warns when the last 24 hours are over budget. The
  doctor reads history only when the latch is set or held in the window.
  Text `doctor` now prints its warnings as `warn:` lines; before, only JSON
  showed them. JSON also has a top-level `swap_derate`.

- **Wait attribution.** `work stats` has a `wait_attribution` block. It splits
  all pre-start wait by cause: `queue_capacity`, `queue_fairness`,
  `queue_reservation`, `pressure_cpu`, `pressure_memory`, `pressure_storage`,
  `pressure_swap`, `singleflight`, and `other`. It gives each share, the
  dominant cause, `swap_share`, and up to five `long_wait_bursts` (waits of 2
  minutes or more within 15 minutes of each other). `swap_derate` adds
  `all_wait_total_ms`, `share_of_all_wait`, and a `note` when long waits had
  another cause. On 2026-11-02, builds waited up to 9 minutes while the
  derate held nothing: 98% of 2 h 18 m of wait in 3 hours was queue capacity,
  in two bursts during a 12-session spike. Loosening the derate for that
  wait would keep the wait and drop the protection.

### Changed

- **`work override --clear` reports `cleared: true`.** The JSON key was a
  count, while every other clear action in ndev reports a boolean. Read the
  count from `len(overrides)`. The nicos-tools operation contract declares
  `cleared` as a boolean, so the old count failed contract validation.

### Fixed

- **Doctor JSON no longer emits `null`.** `fixes` and `warnings` are `[]` when
  empty, and `swap_derate` is absent until a cost is loaded. A null failed the
  closed nicos-tools output contract for these keys.

- **`storage apply` skipped providers are typed.** The JSON keys stay
  `provider_id` and `reason`; the text line now lists provider IDs instead of
  Go map syntax.

- **`monitor install` waits for the new resident to report.** It returned as
  soon as launchd had a PID, so a resident that crashed on start, or had not
  sampled yet, still printed `installed`, and a following `artifact status`
  read the helper revision as unknown. Install now waits up to 30 s, after it
  releases the policy lock, for a report from that PID and the installed
  artifact. The text output adds `resident reporting after <time> (source
  <revision>)` or a warning; the JSON output adds `resident_report`. A slow
  first report warns and does not fail the install.

- **Leased-work CPU stays known during builds.** Leased-work CPU
  (`coordinated_work.cpu_percent`) was unknown whenever a leased process had
  started since the previous inventory. Compile, link, and test steps start all
  the time, so the figure was missing under exactly the load it measures. A
  process that started after the previous inventory used all of its CPU time
  inside the interval, so its delta from zero is exact; leased-work attribution
  now counts it. Agent-tree CPU, idle detection, and relief selection do not
  change: they still read a new process as activity with unknown CPU.

- **Swap deferrals were counted as memory deferrals.** The review now counts
  them as `swap_deferrals`, and `swap_thrash_external_bypasses` counts work
  admitted through an external storm.
- **Work that gave up at a pressure gate lost the gate.** An operation whose
  `--wait` ran out before its lease wrote no `acquired` event, and its
  terminal event had no `pressure_level`, `pressure_dimension`, or
  `pressure_reason`. Terminal events now carry the first deferral, so the
  review counts these operations by dimension. The swap derate cost also
  counts pre-lease admission wait; before, a 12 s derate hold read as 0 s.

## 0.6.0 — 2026-11-02

### Added

- **Nested work runs execute inline under the parent lease.** A leased
  command's children inherit `SESSION_PRESSURE_WORK_OPERATION`. A nested
  `work run` whose parent lease is live, whose lease leader is alive, and
  whose process group is the leased group replaces itself with the command
  and takes no second lease. Before, a build inside a build waited for
  capacity that only its own parent could free (5 + 5 on a capacity of 8).
  A process that left the group or outlived the lease uses normal admission.
  The variable is excluded from the express reuse fingerprint.

- **Source-revision check across executing binaries.** Each binary records
  the session-pressure revision it was built from: a link-time stamp
  (`-X github.com/nstranquist/session-pressure/sessionpressure.sourceRevision=<sha>`),
  or Go's VCS stamp for a standalone build. `artifact status` (JSON
  `drift.source_revisions`) and `doctor` compare the CLI, the installed
  resident helper, and a wrapper's front door
  (`SESSION_PRESSURE_FRONT_DOOR_REVISION`). A mismatch is a doctor warning
  with a fix, not a failure. A partial promotion could leave a CLI built
  hours earlier serving reports and policy commands over a current helper,
  and the sha-only drift check could not see it. A wrapper can name its full
  promotion command in `SESSION_PRESSURE_PROMOTE_COMMAND` for these hints.

- **Live swap-out pressure.** The resident reads cumulative kernel swap
  counters (`host_statistics64`, no subprocess) and reports
  `swap_in_mb_per_s` / `swap_out_mb_per_s` on every snapshot, compact status,
  and telemetry row. Swap-out is a throughput signal, not a memory level: at
  or above `swap_out_warning_mb_per_s` (default 8) it raises a `swap`
  dimension warning. It caps at warning, stays out of the memory-only ladder
  used for agent launches and automatic relief, and never corroborates sticky
  swap use. Existing policies default-fill all swap thresholds.
- **Swap-thrash latch and heavy-work derate.** The resident latches thrash
  after two consecutive samples at or above `swap_out_thrash_mb_per_s`
  (default 32) and releases after two at or below `swap_out_release_mb_per_s`
  (default 16); rates between the two hold the verdict. Snapshots, admission
  samplers (`swap_thrash_latched`), and the controller (`swap_thrash`) project
  it. Under an enforcing profile, new heavy work is then admitted only up to
  `work_limits.swap_thrash_capacity` (default: one full build). A lone
  operation always runs, an unknown coordinator status fails open, and other
  gates still apply. Observe profiles stay report-only. Work events record
  `swap_thrash_admitted` / `swap_thrash_deferred`, and the work review counts
  both.
- **Idle-lease parking.** The `work run` wrapper samples its own leased
  process group's CPU (own plus reaped-child rusage, every 15 s). After
  `work_limits.idle_park_seconds` (default 120; negative disables) under 1%
  of one core, the lease is parked: it stays held, but it is charged zero
  weight, so queued work can start. At 5% of one core or more it is
  re-charged at once, even past the ceiling. Nothing is preempted, and only
  new admissions wait. Group page-ins count as activity: more than 4/s blocks
  parking, and 64/s or more re-charges the lease. Thus a process stalled on
  swap thrash is never parked, and parking cannot admit more work into the
  thrash. Light activity between the thresholds holds the current verdict. Missing evidence and failed writes never leave weight released.
  Parking applies to test, build, express, and heavy classes, and never to
  benchmarks, emulators, browsers, install, or reclaim. Work status adds
  `parked`, `parked_at`, and `parked_weight`. Terminal events add
  `idle_parked_ms` and `idle_park_count`, and the work review counts
  `idle_parked_operations` and `idle_parked_ms`. An n-1 helper ignores the
  field and charges full weight.
- **`state_lock` wait blocker.** A coordinator state-lock timeout inside a
  `--wait` budget now retries (250 ms) instead of failing the command; nothing
  was mutated, so the retry is safe. `filelock.ErrTimeout` types both the file
  and in-process queue timeouts. An exhausted budget still returns
  `context.DeadlineExceeded` with the lock cause attached.
- **Kernel swap attribution for disk writes.** Swap-out bytes have no owning
  process counter, so the disk-write plane now reports them as the synthetic
  `kernel_swap` writer (`category: system`, `owner_kind: kernel`), clamped to
  the device delta. Summaries add `swap_out_bytes_15m` and `swap_share_15m`;
  hourly history adds `swap_out_bytes`. The Disk Writes pane shows a Kernel
  swap tile when swap is present.

### Changed

- **Native free-memory read.** The sampler reads `kern.memorystatus_level`
  (the value `memory_pressure -Q` prints) through sysctl instead of forking
  `memory_pressure` on every resident sample and admission poll; under swap
  thrash that fork was most of the sample wall time. The subprocess remains
  the fallback.
- **Quieter wait progress.** Human and JSONL wait lines key only on stage,
  blocker, queue position, and pressure. Used capacity and running-duration
  reasons no longer defeat deduplication, and an unchanged wait repeats at
  30 s, doubling to a 2 min cap. Any change still prints at once.
- **`policy profile apply --multi-agent`.** Any profile can take the earlier
  multi-agent soft-launch warnings. Before, the only route to them was the
  observe-only `multi-agent-soft` profile, so many-agent hosts had to choose
  between protection and early warnings.

### Fixed

- **Revision checks survive `-trimpath` builds.** Go omits `-ldflags` from
  build info when `-trimpath` is set (golang/go#52372), so the installed
  helper's stamp became unreadable from the file. The resident now reports
  its own revision as `guard_source_revision` in `latest.json`, and artifact
  status uses that report when the file carries no stamp and the resident
  runs the exact installed binary.
- **`monitor install` finds the helper beside a symlinked CLI.** When a
  publisher installs the CLI as a stable symlink into a revision store
  (`bin/cli -> bin/.cli.revisions/current -> sha256-…`), the resolved path
  has no helper beside it. The helper lookup now walks the symlink chain
  from the invoked path and uses the first hop with a published helper.
- **The work report no longer advises an enforcing host to drop its
  protection.** On queue pressure, an enforcing host was told to apply
  `multi-agent-soft`, which turns off launch blocking, the CPU latch, memory
  floors, and the swap-thrash derate. The suggestion now keeps the live
  profile and adds `--multi-agent` (JSON `suggested_policy_profile_flags`).
  It goes away when those warnings are already live. Observe-only hosts get
  the old suggestion. The desktop app shows the additive command.

- **Per-process CPU on Apple Silicon.** libproc rusage CPU totals are mach
  absolute-time ticks, not nanoseconds. The native sampler now converts them
  with `mach_timebase_info` (125/3 on Apple Silicon). Before this fix,
  per-process, agent-tree, host-consumer, and coordinated-work CPU percentages
  read about 41.7x low on Apple Silicon. Relief and resource-cleanup idle
  checks could treat a tree that used a full core as idle, and the fast-lane
  coordinated-CPU ceiling could not engage. Host CPU and the resident's
  self-budget use other counters and were not affected. Intel hosts (timebase
  1/1) are unchanged. Telemetry rows from before this release keep the low
  values.
- `pressure_dimension` validation accepts `thermal` (already emitted by the
  controller) and the new `swap`.
- **The quiet baseline no longer learns memory thrash as normal.** Windows
  where kernel swap is at least half of device writes are reported with
  `swap_dominant` and `baseline_excluded_swap` and never train the baseline,
  including during learning. Previously a sustained swap storm trained p99 up
  to the 64 GiB/15m bucket, so the model labeled roughly 1 TB/hour as
  `normal`.
- Learned baselines move to epoch 2. Restore drops epoch-1 context and
  executable histograms but keeps their hourly traffic, rolling totals, and
  device rebase point, so history stays visible while the model relearns.

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
