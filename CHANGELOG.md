# Changelog

## Unreleased

Target version: `0.5.0`.

- `self-test --scope monitor` verifies a healthy observe-only resident, proven
  baseline, immutable artifact, and weighted-work path. Operator scope remains
  the strict default.
- Package-scoped `go -C <dir>` and absolute `pnpm --dir <dir>` commands receive
  express work classes without a manual override.
- Storage plans report Xcode device symbols, simulator state, documentation,
  and derived data. This provider is read-only and never grants deletion.
- `io health` reads NVMe SMART lifetime wear (percent used, host TB written)
  through Apple's NVMeSMARTLib and appends `nvme-smart-health.jsonl`.
- `io history --full` returns the bounded per-hour executable list already
  stored in `disk-writes-*.jsonl`. Compact history stays token-cheap.

## 0.1.0 — 2026-08-18

- Public source: https://github.com/nstranquist/session-pressure
- `storage apply --force` skips reclaim cooldown on a named `--provider` only.
  `--auto-safe --force` is rejected. Ownership, report_only, and `--apply`
  still fail closed.
- Initial extract of engine, CLI, helper, local API, and desktop client.
