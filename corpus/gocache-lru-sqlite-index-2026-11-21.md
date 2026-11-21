# gocache-lru: SQLite index instead of journal replay (WORK-1601, 2026-11-21)

Status: 2026-11-21, released on the Mac (release 5bf8e7c) and live on runner-3 (pin afcfe04f in nicos-commit-delivery, scheduler release fac26514). Owner: claude/3bd639ce-6e3f-4246-9482-a95928c1b14e. Work: WORK-1601.

## Problem

Each `go` command that writes to the cache starts one gocache-lru client. Before this change a client that put anything replayed the whole `index.jsonl` journal into an in-memory heap to find eviction candidates. The runner journal had about 860,000 rows (170 MB). On the Mac benchmark one put client cost 1031 ms and 944 MB of allocation. On the runner, under the 200% CPU quota of a job, it cost about 3.8 s of every remote build.

## Change (5bf8e7c)

- `index.sqlite` beside the cache (modernc.org/sqlite, pure Go, `CGO_ENABLED=0`). WAL mode. Puts use `synchronous=FULL`.
- Schema: `entries(key PRIMARY KEY, kind, size, access) WITHOUT ROWID`, an index on `(kind, access)`, and a `totals` row that triggers keep equal to the sum of the rows. A put reads the totals and the oldest rows, not the whole index.
- Get-only clients never take the authority lock. Their access hints stay in memory and are written at close with `busy_timeout=2s` and `synchronous=OFF`. A lost hint only makes an entry look older.
- Put, eviction and the startup sweep keep the authority flock.
- The legacy `index.jsonl` is imported once in bulk: drop the triggers and the index, insert in key order, recompute the totals, recreate them. Later puts import only the new tail (`journal_generation`, `journal_offset` in `meta`). Torn, invalid and incomplete final rows are skipped.
- Eviction priority is `max(access, retry)`. An object that is still pinned (link count above 1) is demoted for 1 s, so a close can still evict objects that it unpinned.
- A corrupt index row is forgotten without touching the file system. A failed put is fail-soft: the object is served unindexed.

## Evidence

| Measure | Before | After |
|---|---|---|
| Put client, 860k-row cache (Mac, `BenchmarkPutClientLargeIndex`) | 1031 ms, 944 MB/op | 14.9 ms, 40 KB/op |
| One-time journal import (Mac) | n/a | 21.6 s first version, 4.0 s with the bulk path |
| Start and close of a client (Mac) | 3.2-3.7 ms | 6.2-6.6 ms |

The Mac start cost grows by about 2.3-3 ms because the modernc libc for darwin imports `honnef.co/go/netdb`, whose init costs 2.3 ms and 3.6 MB per process. The Linux libc does not import it, so the runner does not pay it.

On runner-3 the pinned binary was built by `install-tools.sh` from the GitHub archive, and its sha256 matched the pin computed on the Mac (amd64 `729c4fd5…`). The first put after the release imported the runner journal: `index.sqlite` was 176 MB beside the 170 MB journal at 20:43Z.

Provider build replays (`ndev --json catalog drift` with a fresh private `NICOS_CATALOG_PROVIDER_CACHE_NAMESPACE_ROOT`), route time and runner stage:

| Replays | Route | Runner wait and run |
|---|---|---|
| WORK-1480 baseline (18:18-18:19Z) | 26.9-28.3 s | 14.2-15.6 s |
| After SSH reuse and this index (w1-w3, 20:42-20:43Z) | 18.3 / 19.0 / 15.9 s | 9.8 / 12.0 / 9.0 s |

The rest of the WORK-1601 cut (one-pass input copy on the runner) is recorded in nicos-commit-delivery `corpus/runner-input-copy-2026-11-21.md`.

## Deviations

- The one-time import holds the authority lock for about 4 s on a large journal. It happens once per cache.
- `index.jsonl` stays on disk after the import. Older clients that still append to it are tail-imported. Removing it is a later cleanup, after every client is on 5bf8e7c.
- Not measured on the runner: the put-client cost by itself. The job command fell from about 3 s (whole seconds in the job log) to 2.2 s (`dur_ms`), but that number also includes the build.

## Rollback

On the runner: pin session-pressure `ccb2bcc5` again in nicos-commit-delivery `install-tools.sh` and `install.go`, run `install-tools.sh`, and install a scheduler release. The old client reads only `index.jsonl`, and the new client does not append to it. So objects written while 5bf8e7c ran are invisible to the old size cap and are never evicted by it. Gets still find them, because a get reads the object file directly. For a clean rollback, move the cache directory aside before the old client runs (`/srv/build/cache/go-build`) and pay one cold rebuild. On the Mac: `session-pressure-release install --expected-current 5bf8e7c…` of the previous release, with the same note for the local cache.
