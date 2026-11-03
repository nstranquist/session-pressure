# SessionPressure

[![ci](https://github.com/nstranquist/session-pressure/actions/workflows/ci.yml/badge.svg)](https://github.com/nstranquist/session-pressure/actions/workflows/ci.yml)

Local host admission and work coordination for many concurrent coding agents on one Mac.

This is the extracted open-core product. `ndev session pressure` in nicos-tools is a compatibility wrapper around this CLI.

## What it does

- Samples host CPU, memory, swap use, live swap-out rate, and fail-open thermal or low-power signals.
- Admits or defers agent work through a weighted queue, and serializes heavy work while memory thrashes.
- Identifies Claude, Codex, Grok, and Kimi process trees.
- Watches internal-SSD write pressure and NVMe SMART lifetime wear.
- Inspects live work and lets an operator cancel, reschedule, or hand it off.
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

That writes `bin/session-pressure`, `bin/session-pressure-helper`, `bin/session-pressure-api`, and `bin/gocache-lru`. Put `bin/` on `PATH`, or set `SESSION_PRESSURE_BIN`.

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

`internal/atomicfile`, `internal/jsonl`, `internal/telemetry`, and `internal/orb`
are leftover copies from the factory extract. They are not the public API. Do
not import them from another module. A later extract pass can hide or delete
them. See [OPEN-CORE.md](OPEN-CORE.md).

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
