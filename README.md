# SessionPressure

[![ci](https://github.com/nstranquist/session-pressure/actions/workflows/ci.yml/badge.svg)](https://github.com/nstranquist/session-pressure/actions/workflows/ci.yml)

Local host admission and work coordination for many concurrent coding agents on one Mac.

This is the extracted open-core product. `ndev session pressure` in nicos-tools is a compatibility wrapper around this CLI.

## What it does

- Samples host CPU, memory, swap, and fail-open thermal / low-power signals
- Admits or defers agent work through a weighted queue
- Identifies Claude, Codex, Grok, and Kimi process trees
- Watches internal-SSD write pressure
- Ships a resident helper, a local opt-in API, and a thin native macOS client

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

## Install

From source:

```bash
make build
```

That writes `bin/session-pressure`, `bin/session-pressure-helper`, and `bin/session-pressure-api`. Put `bin/` on `PATH`, or set `SESSION_PRESSURE_BIN`.

Desktop client:

```bash
make -C apps/SessionPressure test
make -C apps/SessionPressure run
```

The app still uses the on-disk brand NDev Pressure through this release.

## Configuration

Default state directory: `~/.nicos-dev/session-pressure`.

| Variable | Role |
|---|---|
| `NDEV_SESSION_PRESSURE_HOME` | Primary state-directory override |
| `SESSION_PRESSURE_HOME` | Alias of the primary override |
| `SESSION_PRESSURE_BIN` | Exact product CLI path for the desktop client |
| `NDEV_PRESSURE_BIN` | Compatibility alias for the same path |

Policy lives at `$SESSION_PRESSURE_HOME/policy.json`. `policy init` writes observe-only defaults. `policy enable` is the first command that can block launches. `policy profile apply` accepts `balanced`, `throughput`, `interactive`, or `observe`.

Do not copy a live `~/.nicos-dev/session-pressure` tree into issues, fixtures, or pull requests.

## Troubleshooting

- **`doctor` reports the host red or `ok=false`.** That is live host pressure, not a broken install. Check `session-pressure --json status --live`.
- **`ndev session pressure` exits 127.** The nicos-tools wrapper cannot find the product CLI. Install `session-pressure` or `ndev-pressure` on `PATH`.
- **Desktop says the binary is missing.** Set `SESSION_PRESSURE_BIN` to the built `bin/session-pressure`. The client prefers that name over nicos-tools paths.
- **`storage apply` refuses a provider.** Extract pageskein reclaim is closed. Factory-only providers stay visible and non-actionable. See [OPEN-CORE.md](OPEN-CORE.md).
- **`--auto-safe --force` is rejected.** Force skips cooldown on one named `--provider` only. Ownership, `report_only`, and `--apply` still fail closed.

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
| `pkg/` | Portable helpers other packages in this module may use |

`internal/atomicfile`, `internal/jsonl`, `internal/telemetry`, and `internal/orb` are leftover copies from the factory extract. They are not the public API. Do not import them from another module. A later extract pass may hide or delete them; see [OPEN-CORE.md](OPEN-CORE.md).

nicos-tools keeps Toolguard, agent-launch hooks, and nicos-only reclaim providers. See [OPEN-CORE.md](OPEN-CORE.md) and [docs/architecture.md](docs/architecture.md).

## Examples

```bash
./examples/doctor.sh
./bin/session-pressure --json work status
./bin/session-pressure --json storage plan --target-free 50GiB
```

## License

Apache-2.0.
