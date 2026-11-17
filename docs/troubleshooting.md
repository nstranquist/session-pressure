# Troubleshooting

## Doctor says the host is red

That is a live pressure reading. It does not mean the install failed. Use `session-pressure --json status --live` and `session-pressure --json work status`.

A tree or agent RSS reason with "held at warning" is a calibrated large host:
the RSS sum passed red, but kernel free memory is still high. Run
`session-pressure policy memory-levels show` to see the levels, their source
and the rule (anchor, interpolation or host override) for this host.

## Policy load fails with `memory levels:`

The calibrated policy could not resolve `memory-levels.json`. The error names
the file and the field. Run `session-pressure policy memory-levels check` to
see it again, then fix or remove the data-directory copy. The embedded copy is
used when no data-directory copy exists.

## Compatibility wrapper exits 127

`ndev session pressure` execs the product CLI. Install `session-pressure` or `ndev-pressure` on `PATH`.

## `io health` says NVMe SMART is unavailable

The command is read-only. It uses Apple's NVMeSMARTLib. Internal Apple Fabric
SSDs on this Mac expose it. Many USB bridges do not. `percentage_used` and
`host_bytes_written` are the lifetime wear fields. `power_on_hours` and
`controller_busy_minutes` can be empty on Apple ANS. Process `io history`
executables are all-volume best-effort names, not exact NAND writes.

## Desktop cannot find the CLI

Set `SESSION_PRESSURE_BIN` to the built `bin/session-pressure` file. The client prefers that name.

## Storage apply is blocked

Pageskein reclaim is closed in the extract. Named factory-only providers stay visible and do not mutate. `--auto-safe --force` is rejected. Force only skips cooldown on one `--provider`.
