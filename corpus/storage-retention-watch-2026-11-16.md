# Storage retention providers, storage watch and tm-exclude plan, 2026-11-16

Status: implemented and tested in source; installed release pending (see
below). Owner: claude/side session of local_0c5481cf (repo restructure
program slice S2, steps ST6, ST7 and ST3).

## Why

The S2 storage program (nicos-tools
`docs/active/11-09-1339-repo-restructure-program/STORAGE-PROGRAM.md`) found
that `storage plan` could not see the space that grew between 11-09 (100 GiB
free) and 11-16 (26 GiB free):

- `go-build-cache` measured `~/Library/Caches/go-build` while builds wrote
  `go-build-capped` through `GOCACHEPROG=gocache-lru`.
- Dead per-process Go caches (`go-build-<name>-<pid>`) had no provider.
- `~/.local/bin` held about 20 GiB of superseded binary revisions (93 ndev
  builds, 136 commit-delivery builds, 90 session-pressure releases).
- `providers` without `--measure` reported 0 bytes for everything.

## What changed

- `go-build-cache` resolves `go env GOCACHE`. New report-only
  `go-build-capped` resolves the gocache-lru `-dir` and `-max` from
  `GOCACHEPROG`.
- New operator providers, measured on every inventory (`exact_gc`):
  - `binary-revisions`: keep `current` and other links in each store,
    launchd pins (live and `.bak` plists), running-process paths, entries
    younger than the provider min age (at least 1 h), the newest 3 per store
    and the newest clean VCS build (same rule as nicos-commit-delivery
    `binarypublish`). Stores: `~/.local/bin/.*.revisions` (files) and
    `.session-pressure-releases` (directories).
  - `turso-backups`: keep the newest 2 dated daily copies; never `pinned`.
  - `go-build-orphans`: `go-build-<name>-<pid>` with a dead pid, older than the
    min age; never GOCACHE, the capped cache or unsuffixed caches.
  Apply re-plans and removes only fresh candidates that sit directly in the
  store root and match the store name pattern.
- New report-only inventories: `nicos-dev-tmp`, `openbook-worktrees`,
  `docgraph-generations`, `session-workspaces`, `context-builds`,
  `perf-copies`. Removal of these needs a value review (G2 packet).
- `delivery-go-build-cache` reads the worker's `go-build-cache-trim.json`
  receipt when not measuring.
- `storage watch [--dry-run] [--test-alert]`: disk free (warning at the policy
  warning band, 50 GiB; critical at the red band, 25 GiB), Time Machine latest
  backup older than 48 h, X10 Pro unmounted more than 24 h, NicosTM above 90%
  of its quota. One alert per episode, again on escalation and every 24 h.
  Routes: notify-inbox toast, `storage-watch-alerts.jsonl`, one Scratchpad
  note (`ndev scratchpad capture --target agents`). State in
  `storage-watch-state.json`.
- `storage tm-exclude plan [--limit N]`: fixed sudo exclusions and sticky
  rebuildable folders (node_modules, target, .build, .next, .venv, venv, Pods,
  DerivedData, .turbo, .wrangler, .svelte-kit, .expo, `~/dev/perf-*`,
  `~/dev/.ndev-work*`) with measured sizes and the estimated backed-up bytes
  against the 300 GiB target. No apply yet: it ships after the NicosTM volume
  exists (ST2, Nico).

## Not done here (deviations)

- Fleet event plane route: the plane has no `pressure.storage.*` kind yet
  (fleet-event-plane KEP, open item `pressure.*`). A new kind needs a plane
  vocabulary change and feature handshake in nicos-commit-delivery first.
- `telemetry-rotate` and `dormant-build-outputs` providers: not built;
  `nicos-telemetry-archive` and `dev-root-leftovers` already report them.
- `storage watch` schedule: run it hourly once the release is installed.

## Proof

- `go test ./sessionpressurecmd/` green (fixture trees for each provider:
  current, launchd, process, young, clean-build and linked-directory pins).
- Live inventory from source, 2026-11-16: `binary-revisions` 19.28 GiB
  (342 candidates, 19 protected), `go-build-orphans` 1.93 GiB (6),
  `turso-backups` 1.13 GiB (1).
