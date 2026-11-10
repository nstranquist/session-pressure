# SessionPressure

[![ci](https://github.com/nstranquist/session-pressure/actions/workflows/ci.yml/badge.svg)](https://github.com/nstranquist/session-pressure/actions/workflows/ci.yml)

Local host admission and work coordination for many concurrent coding agents on one Mac.

This is the extracted open-core product. `ndev session pressure` in nicos-tools is a compatibility wrapper around this CLI.

See [repository ownership and delivery](docs/repository-boundaries.md) before
changing the product or its factory integration.

## What it does

- Samples host CPU, memory, swap use, live swap-out rate, and fail-open thermal or low-power signals.
- Admits or defers agent work through a weighted queue, and serializes heavy work while memory thrashes.
- Identifies Claude, Codex, Grok, and Kimi process trees.
- Watches internal-SSD write pressure and NVMe SMART lifetime wear.
- Inspects live work and lets an operator cancel, reschedule, or hand it off.
- Alerts on sustained runaway processes, offers identity-checked stop controls, and retains incident history.
- Ships a resident helper, a local opt-in API, and a thin native macOS client.

## Quick Start

```bash
git clone https://github.com/nstranquist/session-pressure.git
cd session-pressure
make init
./bin/session-pressure --json doctor
```

The doctor command is observe-only. It does not enable admission blocks and does not rewrite policy unless you run `policy init` or `policy enable`.

To write the tuned observe-only default policy, then sample once:

```bash
./bin/session-pressure policy init
./bin/session-pressure --json snapshot
```

After the resident has four normal samples, verify the installed observe-only
path:

```bash
./bin/session-pressure --json self-test --scope monitor --wait 10m
```

This scope verifies the resident, its immutable artifact, and the weighted-work
path. It does not claim that launch admission or automatic relief is active.
The default `self-test` scope is `operator`. It requires the full protection
policy.

## Install

From source:

```bash
make build
```

That writes six tools: `session-pressure`, `session-pressure-helper`,
`session-pressure-api`, `gocache-lru`, `session-pressure-release`, and
`session-pressure-release-cc` under `bin/`. Put `bin/` on `PATH`, or set
`SESSION_PRESSURE_BIN`. The release helper installs verified bundles and records
artifact-bound acceptance observations. The compiler adapter supports the pinned
Darwin native release profile; a portable build is not a native release receipt.

Desktop client:

```bash
make -C apps/SessionPressure test
make -C apps/SessionPressure run
```

The app still uses the on-disk brand NDev Pressure through this release.

A second desktop client built on GPUI is in preview under
[`apps/pressure-ui`](apps/pressure-ui/README.md). It is keyboard-first and
adds a three-column work inspector. Like the Swift client, it is a thin shell
over `session-pressure --json`. It needs a Rust toolchain; the first build
compiles GPUI and takes several minutes.

```bash
make desktop-gpui
```

## Configuration

Default state directory: `~/.nicos-dev/session-pressure`.

| Variable | Role |
|---|---|
| `NDEV_SESSION_PRESSURE_HOME` | Primary state-directory override |
| `SESSION_PRESSURE_HOME` | Alias of the primary override |
| `SESSION_PRESSURE_BIN` | Exact product CLI path for the desktop client |
| `NDEV_PRESSURE_BIN` | Compatibility alias for the same path |
| `SESSION_PRESSURE_API_BIN` | Exact `session-pressure-api` path for `session-pressure api` |

Policy lives at `$SESSION_PRESSURE_HOME/policy.json`. `policy init` writes observe-only defaults. `policy enable` is the first command that can block launches. `policy profile apply` accepts `balanced`, `throughput`, `interactive`, or `observe`. Add `--multi-agent` to any profile for earlier soft-launch warnings when many agents share the host. Enforcement stays as the profile sets it.

### Process incidents and bounded scans

The resident records sustained CPU incidents from its existing process inventory.
A known command whose parent is PID 1, or work below an init-owned shell,
must be at least 10 minutes old and use 60% of one core for two minutes.
PID 1 is normal for many GUI apps and services; it does not prove an orphan.
Other processes require 30 minutes of age and five hot minutes.
An alert opens **Process Incidents** in the native app. Inspect its evidence,
then use **Stop** or the separate **Force Stop** action. Detection never stops
a process automatically. Each action checks ownership and kernel start identity
and retains its result in private, bounded history.

Scope scans to a named project and give them a deadline:

```bash
session-pressure processes run --timeout 2m -- rg --files /path/to/project
```

For expensive work, run that command through `work run --class heavy --`.
`processes list` reads history; `processes --help` describes the controls and
their limits. See the [human guide](docs/human/process-incidents.html).

### SSD burst alerts

The resident flags a high write burst when valid internal SSD samples contain
at least 1 GiB within one minute. At the default 15-second sampling interval,
a single large sample can trigger the signal before the adaptive baseline is
ready. Counter resets and unscored gaps do not trigger it. Burst windows are
excluded from quiet-baseline training. `io status` reports `burst_active`,
`burst_window_bytes`, and the `absolute_write_burst` reason when applicable.
Device counters measure host writes, not NAND wear. Process attribution is
partial and covers all volumes; it does not prove which process wrote the SSD.

Notifications require `session-pressure io policy enable-alerts`. They are
limited to four per day and open the Disk Writes view. Detection does not stop
processes or launch an agent. Use `io top --live` to inspect current candidates
and `io health` to inspect the drive's SMART counters.

### Swap-thrash derate

While the host swaps hard (the resident latch, `swap_thrash_latched`), enforcing
profiles admit new heavy work only up to `work_limits.swap_thrash_capacity`
(default: one full build). A lone operation always runs. The resident
attributes each episode (`swap_thrash_cause`). If leased work stays under
`swap_thrash_agent_share_percent` of RAM (default 25) for
`swap_thrash_external_release_seconds` (default 300), the storm is `external`
and the derate stops holding work until the latch releases. `doctor` then
names the largest non-agent memory users; close them to end the storm.

Measure the cost with `session-pressure --json work stats --since 72h` and read
`work_stats.swap_derate`, not total wait. Most wait is usually queue capacity:
`work_stats.wait_attribution` splits all wait by cause, and `swap_share` is the
derate's part. If `swap_derate.verdict` is `over_budget`, loosen in
`policy.json` `work_limits`:

| Change | Effect |
|---|---|
| Lower `swap_thrash_external_release_seconds` (min 60) | Lift the derate sooner in outside storms |
| Raise `swap_thrash_agent_share_percent` | Count more leased RSS as "small" |
| Set `swap_thrash_capacity` to `capacity` | Turn the derate off; the latch still reports |
| Set `swap_thrash_external_release_seconds` to `-1` | Strict: hold work for the whole storm |

### Override beyond capacity

`work override` (Run now) normally changes queue order only: the selected
waiter still has to fit inside `work_limits.capacity`. Set
`work_limits.allow_override_beyond_capacity` to `true` in `policy.json`
(or `session-pressure policy override-beyond-capacity on`) and an explicit
override also admits its waiter past that weighted ceiling, so leased weight
can exceed `capacity` until the overridden work finishes.

The toggle widens only the logical cap. The host-pressure admission gate is
untouched: red-level pressure in any dimension and the shared admission
latch still block the waiter before selection, and the warning/swap derates
still govern ordinary admissions.

Default is `false` in the shipped policy template. The live policy on this
Mac is set to `true` (operator decision, 2026-11-10). Risk: sustained
oversubscription hides misclassification - a job holding a heavy weight
while using almost no CPU still blocks the pool for everyone who is *not*
overridden, and the cap no longer surfaces the mistake. Pair the toggle
with weight-classification review (`work stats`, `work report`).

Motivating case (2026-11-10): the pool sat at weight 10 of capacity 8 - a
heavy-classified telemetry job at ~4% CPU, a parked lease whose supervisor
had died, and a test lease - while a weight-2 waiter whose operator
override had been requested still could not admit, because the override
only reordered the queue. It hit its wait bound and died unleased.

Do not copy a live `~/.nicos-dev/session-pressure` tree into issues, fixtures, or pull requests.

## Troubleshooting

- **`doctor` shows host red or `ok=false`.** This result means live host pressure. It does not mean a broken install. Run `session-pressure --json status --live` for details.
- **`ndev session pressure` exits 127.** The nicos-tools wrapper cannot find the product CLI. Install `session-pressure` or `ndev-pressure` on `PATH`.
- **Desktop says the binary is missing.** Set `SESSION_PRESSURE_BIN` to the built `bin/session-pressure`. The client prefers that name over nicos-tools paths.
- **`storage apply` refuses a provider.** Extract pageskein reclaim is closed. Factory-only providers stay visible and non-actionable. See [OPEN-CORE.md](OPEN-CORE.md).
- **`--auto-safe --force` is rejected.** Force skips cooldown on one named `--provider` only. Ownership, `report_only`, and `--apply` still fail closed.

For storage plan limits, provider effects, and archive checks, see
[Storage reclaim](docs/storage-reclaim.md).

## Architecture

The public tree owns sampling, policy, the work coordinator, identity, the SSD write watchdog, doctor, helper, CLI, local API, and the thin Swift client.

## Public packages

Import and document these as the product:

| Package | Role |
|---|---|
| `sessionpressure` | Sampling, policy, admission queue |
| `sessionpressurecmd` | CLI |
| `sessionpressurecontrol` | Local control API |
| `sessionpressurecleanup` | Cleanup helpers |
| `pkg/` | Portable helpers that other packages in this module can use |

`internal/telemetry` owns the standalone command stream, privacy validation,
writer health, retention and factory-health reads. Internal packages are not
public APIs and cannot be imported from another module. The remaining extracted
helpers include `internal/atomicfile`, `internal/jsonl`, and `internal/orb`. See
[OPEN-CORE.md](OPEN-CORE.md).

nicos-tools keeps Toolguard, agent-launch hooks, and nicos-only reclaim providers. See [OPEN-CORE.md](OPEN-CORE.md) and [docs/architecture.md](docs/architecture.md).

## Examples

```bash
./examples/doctor.sh
./bin/session-pressure --json work status
./bin/session-pressure --json storage plan --target-free 50GiB
```

The work coordinator recognizes package-scoped `go -C <dir> test/build` and
`pnpm --dir <dir> test/build/typecheck` commands. It assigns the express class
without a manual `--class` flag. Recursive and multi-package commands keep the
full test or build weight.

### Inspect live work

Every lease has an operation ID, shown by `work status`. To see what a lease
is doing:

```bash
./bin/session-pressure --json work inspect --operation-id ID
```

The brief names the live process, its origin, a stall class, runtime against
the class p95, and recent output. Live argv and output are read for
inspection only and are not written to work events. Follow-up actions:

```bash
./bin/session-pressure work cancel --operation-id ID --confirm
./bin/session-pressure work reschedule --operation-id ID --confirm
./bin/session-pressure work handoff --operation-id ID
```

`cancel` signals the lease's process tree and releases the lease. `reschedule`
applies only to scheduled work and needs the optional scheduler integration.
`handoff` writes a brief for an interactive agent. See
[OPEN-CORE.md](OPEN-CORE.md) for the optional integrations.

### Cap the Go build cache

`gocache-lru` is a Go `GOCACHEPROG` helper. It keeps the build cache under a
size cap and evicts the least recently used entries first:

```bash
export GOCACHEPROG="$PWD/bin/gocache-lru -max 16GiB"
```

The default cache directory is `~/Library/Caches/go-build-capped`. Set `-dir`
to change it.

## License

Apache-2.0.

## Workload performance review

Use a registered workload and an explicit cohort to compare like-for-like runs:

```bash
session-pressure work run --workload catalog --cohort warm --profile-go-test -- go test -json ./internal/catalog
session-pressure --json work report --since 48h --full
```

Registered workloads: `commitdelivery.full`, `commitdelivery.shard`,
`precommithook`, `catalog`, `cli`, `docgraph-ask`, `browser-mcp-script`,
`docs.noop`, `docs.incremental`, `docs.full`, `candidate-verify-path`.
Cohorts: `warm`, `cold`, `cached`, `fixture`, `live`; omitted labels stay unknown.
Batch manifests accept optional `workload` and `cohort` fields. Budgets are
advisory warm execution targets. Reports retain failures and separate queue,
pressure, reuse and execution. Every execution of at least one minute is reviewed,
including intentional benchmarks. p95 requires 20 samples in the same cohort.

`--profile-go-test` requires direct `go test -json`, executes rather than reuses
an exit-only receipt, and preserves child streams and status. Structured package
and test timings are stored in the data directory's `work-profiles/` (seven days,
128 MiB total, 2,000 timing rows per run). Arbitrary output and argv are not saved.
Oversized lines and excess records are counted as dropped. Batch progress includes
per-step completion time and failure status. Profiling is off by default.

On macOS, native capacity notifications wake queued work for a locked recheck.
The two-second fallback remains active; pressure sampling keeps its own cadence.
Set `SESSION_PRESSURE_WORK_NOTIFY=0` to return a process to timer-only waits.
There are no model keepalive calls, forced compactions, or cache-retention promises.
