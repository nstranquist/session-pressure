# Storage watch makes external-drive checks opt-in (2026-11-27)

## Problem

`session-pressure storage watch` hardcoded an external archive drive default
and evaluated an `x10-mounted` condition on every run. On hosts that back up
to cloud object storage (such as Backblaze B2) without a permanently attached
external archive drive, the watch either emitted stale drive info or warned
after 24 hours with a hardcoded action asking the operator to plug in a
retired drive.

## Decision

Make external-drive monitoring opt-in via `--drive PATH` instead of probing a
hardcoded external volume path by default:

- With no `--drive` argument, `storage watch` checks local disk capacity, Time
  Machine freshness, and Time Machine volume quota, and prunes any legacy
  `x10-mounted` or `external-drive` episode from saved state.
- With `--drive PATH`, `storage watch` evaluates `external-drive` using the
  configured volume's basename and suggests connecting the drive or omitting
  `--drive` if retired.

## Change

- `sessionpressurecmd/session_pressure_storage_watch.go`: removed the
  hardcoded external drive default, gated external-drive probing and
  evaluation on an explicit `DrivePath`, renamed the opt-in condition to
  `external-drive` with neutral summaries/actions, and deleted legacy
  `x10-mounted` episodes from `storageWatchState.Episodes`.
- `sessionpressurecmd/session_pressure_storage.go`: updated the `watch` help
  line to describe optional `--drive PATH`.
- `sessionpressurecmd/session_pressure_storage_watch_test.go`: verified
  default runs emit no external-drive condition and clear legacy `x10-mounted`
  episodes, and verified explicit `--drive` mount/grace/24h warning behavior.

## Verification

- `go test -count=1 ./sessionpressurecmd/...` and `go vet ./...` pass.
- Promoted clean `HEAD` via `make release-promote` and verified
  `session-pressure storage watch --dry-run` emits zero external-drive alerts.
