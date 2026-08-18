# Changelog

## Unreleased

- Public source: https://github.com/nstranquist/session-pressure
- `storage apply --force` skips reclaim cooldown on a named `--provider` only.
  `--auto-safe --force` is rejected. Ownership, report_only, and `--apply`
  still fail closed.
- Initial extract of engine, CLI, helper, local API, and desktop client.
