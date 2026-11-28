# App install maintenance works without an external archive disk (2026-11-27)

## Problem

When `session-pressure-release install-app` finished installing the desktop app,
it called `history.maintain("")`. If more than one local rollback existed and
no `archive_root` setting was stored in `history.sqlite`, `maintain` returned
an error before verifying rollbacks, importing legacy receipts, or running
`PRAGMA integrity_check`. `install-app` then printed a hardcoded warning:

```
Installed app; backup maintenance pending. Mount the archive disk and run session-pressure-release app-maintain.
```

Hosts that rely on automatic offsite cloud backups (such as Backblaze B2 over
the user's state directory) instead of a locally mounted external drive hit
this warning on every app promotion and skipped local rollback indexing and
SQLite integrity verification.

## Decision

Treat `--archive-root` as optional instead of mandatory when multiple rollbacks
exist:

- With no `archive_root` configured, `maintain` verifies every rollback bundle,
  moves any legacy rollback beside the app bundle into private `rollbacks/`,
  indexes every rollback in `history.sqlite`, imports legacy receipts, and runs
  `PRAGMA integrity_check`. No rollback is ever deleted without verified
  archival to an explicit archive directory.
- With an explicit `archive_root` configured, two-phase verified archival,
  newest-1 local retention, and `VACUUM INTO` snapshots behave exactly as
  before.
- Add `app-maintain --clear-archive-root` so hosts retiring an old archive path
  can switch back to default local-plus-offsite-backup retention cleanly.
- Replace hardcoded `"Mount the archive disk"` messages with the actual error
  text and neutral recovery guidance (`--clear-archive-root` or fixing the
  path).

## Change

- `scripts/release/app_history.go`: removed the `len(backups) > 1 && root == ""`
  refusal in `maintain`, kept all rollbacks inside local `rollbacks/` when
  `root == ""`, added `clearArchiveRoot()`, and replaced the stale archive-disk
  error text in `checkArchiveRoot`.
- `scripts/release/main.go`: added `--clear-archive-root` to `app-maintain`
  (mutually exclusive with `--archive-root`) and printed the real maintenance
  error instead of `"Mount the archive disk..."`.
- `scripts/release/app_history_test.go`: replaced the obsolete
  `no-archive-config` failure expectation with
  `TestAppHistoryMaintainsLocalRollbacksWithoutArchiveRoot` covering
  local-only maintenance, receipt import, rollback indexing, and
  `clearArchiveRoot`.
- `scripts/release/README.md`: removed the hardcoded external volume example
  and documented default local-plus-offsite-backup maintenance alongside
  optional `--archive-root` and `--clear-archive-root`.

## Verification

- `go test -count=1 ./scripts/release/...` and `go vet ./...` pass.
- Live `go run ./scripts/release app-maintain` on the installed app verified
  and indexed all local rollbacks and passed `PRAGMA integrity_check` with zero
  warnings.
