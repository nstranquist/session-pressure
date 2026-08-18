# Troubleshooting

## Doctor says the host is red

That is a live pressure reading. It does not mean the install failed. Use `session-pressure --json status --live` and `session-pressure --json work status`.

## Compatibility wrapper exits 127

`ndev session pressure` execs the product CLI. Install `session-pressure` or `ndev-pressure` on `PATH`.

## Desktop cannot find the CLI

Set `SESSION_PRESSURE_BIN` to the built `bin/session-pressure` file. The client prefers that name.

## Storage apply is blocked

Pageskein reclaim is closed in the extract. Named factory-only providers stay visible and do not mutate. `--auto-safe --force` is rejected. Force only skips cooldown on one `--provider`.
