# Darwin cgo builds: a stable clang tool directory (WORK-1480, 2026-11-21)

Status: 2026-11-21, released and live on the Mac (release 9c3367c). Owner: claude/f808812b-0720-40c8-acff-bd5352a6e4ca. Work: WORK-1480.

## Problem

A remote `go build` in the `darwin-arm64-cgo` profile took about 40 s on the runner, even when nothing had changed. Example: the nicos-tools `catalog-providers` binary, which links `mattn/go-sqlite3`.

The profile passes `CC=/srv/build/opt/pressure-darwin-clang -B<tools>/`. The `-B` directory gives clang LLVM's strip. Until this change it was per job (`<job>/tools`). Go puts `CC` into the action ID of every package in a cgo build. So each job had a new key for the standard library, `modernc.org/*` and `go-sqlite3`, and rebuilt all of them. `sqlite3-binding.c` alone took 27.8 s at `-O2 -g`.

## Evidence (runner-3, same flags, `GOMAXPROCS=2 -p=2`, `-debug-trace`)

| Case | Build time |
|---|---|
| New `-B` path | 53.9 s |
| Same `-B` path, same source directory | 1.0 s |
| Same `-B` path, another source directory | 0.98 s |

`-trimpath` already makes the cache portable across job directories, so only `CC` broke the cache. The `GOMAXPROCS=2` and `-p=2` pins were not the lever.

## Change (9c3367c)

- `remoteDarwinSharedTools = /srv/build/cache/pressure-darwin-tools` holds only the `strip` link.
- The preflight checks the link with `readlink`. It makes the link when it is missing or wrong, using a temporary link that it renames into place, so a concurrent job never sees a missing strip.
- The preflight sets `NCD_PRESSURE_DARWIN_TOOLS`. `CC` reads it through `${NCD_PRESSURE_DARWIN_TOOLS:?}`.
- When the shared directory cannot be made (for example a pool user without write access), the job falls back to its own `<job>/tools`. That job still builds, only without the cross-job cache.
- Tests cover the shared link, reuse with no leftover temp file, repair of a wrong link, and the fallback.

## Rollout

`ndev` reads remote work from the standalone release, and the resident helper builds the remote command. So the fix needed a full release:

1. `go run ./scripts/release build` from a clean clone of 9c3367c.
2. `session-pressure-release install --expected-current b657c00…`.
3. `session-pressure monitor install`.
4. ndev rebuilt with `build-committed-ndev`.

After that, `artifact status` reported 9c3367c for the CLI, the front door and the helper. The release also carried ccb2bcc (gocachelru DiskPath grace window), which had landed on main but had not been installed.

## Result

The first build with the new `CC` filled the cache (runner job 41 s). After that the runner job took 6-8 s. For the end-to-end results of the provider build, see the nicos-tools corpus `catalog-rows-post-land-2026-11-17.md`.

## Remaining cost

- In `runner_wait_and_run_ms` (about 14-15 s), the job script itself takes 6-8 s and runner preparation about 7 s.
- Download is about 5 s (the gzip artifact) and upload about 3.4 s.

These are the next levers.
