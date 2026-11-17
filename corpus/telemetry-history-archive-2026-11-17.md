# Telemetry history is archived, never deleted (2026-11-17)

Status: 2026-11-17, source verified, delivery through the M4 lander.
Owners: claude/338dc620 (original, stopped 2026-11-17T03:29Z), then
claude/ed6760d6 (current). Work: WORK-1009, child of WORK-1005.

## Problem

Nico, 2026-11-16: telemetry is never cleared. The nicos-tools manifest
(`nicos-dev/internal/telemetryretention/retention.yaml`, store
`session-pressure-telemetry`) listed these loss paths:

- Day files past `RetentionDays=14` were removed: `sessionpressure/telemetry.go`
  (snapshots, actions, storage-actions, work-events), `disk_write_store.go`
  and `workevent.go`.
- Command telemetry shards past 35 days were removed
  (`internal/telemetry/writer.go`).
- The legacy `actions.jsonl` was rewritten without its old rows.
- The API `audit.jsonl` rotation at 4 MB removed the older `.1` generation
  (`sessionpressurecontrol/store.go`).
- runner-capacity `net_samples` and `probe_samples` were deleted past 35 days.
  That part landed separately in 4fbc457 (`internal/gaterunner/capacity_offload.go`).

## Change

New `internal/jsonl/archive.go`:

- `ArchiveFile(src)` writes `<src>.gz` beside the source. It fsyncs the gzip,
  reads it back, and compares length and sha256 with the source. It links
  the archive into place, so an existing archive is never replaced. Only
  then does it remove the source. If the source changed during the copy, or
  any step fails, the source stays.
- `ArchiveCopy(src, now)` writes `<src>.<UTC>.gz` and keeps the source. The
  legacy actions file uses it before a rewrite that drops rows.
- `RotateArchive(path, now)` renames the live file to
  `<path>.<20060102T150405Z>[-N]` and archives it. That is the
  `jsonl.Family` naming in nicos-tools, so ndev readers list the generations.
  It also sweeps older uncompressed generations, including a legacy `.1`.
  If a peer rotated first, the error wraps `os.ErrNotExist`. If compression
  fails, the error wraps `ErrRotatedNotArchived` and the generation stays on
  disk. The audit writer treats both as success and appends to the fresh file.

The three day-file prunes and the command-shard prune call `ArchiveFile`
instead of `os.Remove`. The live readers glob `*.jsonl`, so they skip the
`.jsonl.gz` archives. The work-event SQLite index still drops rows of archived
shards. The archived shard keeps those rows.

## Verification

- Remote runner job 0c2f7d86 (candidate tree): `go vet` plus `go test` for
  `internal/jsonl`, `internal/telemetry`, `sessionpressurecontrol` and
  `sessionpressure/...` passed.
- Local: `internal/jsonl`, `internal/telemetry` and `sessionpressurecontrol`
  passed.
- `ndev telemetry retention --check --root session-pressure` shows no new
  and no stale session-pressure site once the manifest classifies
  `archive.go`. The other roots have their own open findings.

## Where the history is

- `~/.nicos-dev/session-pressure/*-YYYYMMDD.jsonl.gz`
- `~/.nicos-dev/telemetry/sources/session-pressure/events-*.jsonl.gz`
- `<api root>/audit.jsonl.<UTC>[-N].gz`
- runner capacity: `~/.nicos-dev/backups/retention-offload/session-pressure-telemetry/<YYYY-MM>.sqlite`

The archives grow in place without a bound. Their offsite copy belongs to
WORK-981 (Mac offsite tier).
