# SessionPressure

Local host admission and work coordination for many concurrent coding agents on one Mac.

This is the extracted open-core product. `ndev session pressure` in nicos-tools is a compatibility wrapper around this CLI.

## What it does

- Samples host CPU, memory, swap, and fail-open thermal / low-power signals
- Admits or defers agent work through a weighted queue
- Identifies Claude, Codex, Grok, and Kimi process trees
- Watches internal-SSD write pressure
- Ships a resident helper, a local opt-in API, and a thin native macOS client

## Install (from source)

```bash
make build
./bin/session-pressure --json doctor
```

Default state directory: `~/.nicos-dev/session-pressure`.
Override with `NDEV_SESSION_PRESSURE_HOME` or `SESSION_PRESSURE_HOME`.

## Open-core

See [OPEN-CORE.md](OPEN-CORE.md). Factory launch hooks, Toolguard, and nicos-only reclaim stay in nicos-tools.

## License

Apache-2.0. Public GitHub create/push is a human step and is not done by this tree's extract.
