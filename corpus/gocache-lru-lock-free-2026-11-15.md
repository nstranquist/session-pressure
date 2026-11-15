# gocache-lru: lock-free gets, lazy index, journal never cut

Owner: claude (session 86759fdf), took over from the previous owner with
their agreement. Status: live on runner-3 since 2026-11-15T20:52:15Z (scheduler
release 0742176 carries the pinned binary).

## Problem (runner-3, 2026-11-15)

Go jobs on runner-3 printed `go: still waiting for GOCACHEPROG` (Go
prints it when the helper's capabilities answer takes more than 5 s,
`cmd/go/internal/cache/prog.go`). 18 of the last 60 jobs printed it. The
delivery-queue-3d corpus check timed out behind it.

Causes, measured on the box:

- The shared journal `index.jsonl` held 1,123,094 rows for 861,885 live
  keys. Compaction runs only when live rows fall below half, so it never
  ran.
- Every helper (one per `go` command) replayed the whole journal under the
  exclusive `authority.lock` before it answered. Eight helpers at once
  waited for each other.
- Every get took the same lock and an fsync to record the access.
- A client whose offset survived a cut of the journal resumed mid-row after
  peers regrew it, read a broken row, and truncated the shared journal at
  its own offset. That deleted peers' rows.

## Change (`sessionpressure/gocachelru/lru.go`)

- `New` replays nothing. Under the lock it only sweeps dead clients and
  creates its pin directory.
- `get` takes no lock. It reads the action file, stats the object, pins it
  (hard link) and appends an access hint with one `O_APPEND` write, no
  fsync.
- The index loads lazily on the first put (`ensureIndex`, then a locked
  `loadIndex`). A get-only helper never replays and its `Close` only
  removes its pins.
- Replay never truncates. A partial final row stays unconsumed. `record`
  starts its row on a new line when the journal does not end with one, and
  advances the offset only when its row lands where the client expected.

## Proof

Tests (race, on runner-3): `go vet` ok, `gocachelru` ok,
`cmd/gocache-lru` ok. New tests in `lazy_index_test.go`:
TestStartupGetAndGetOnlyCloseNeverReplay, TestGetHitsWhileAPeerHoldsTheLock,
TestHintRowsKeepPutClientsAligned, TestStaleOffsetNeverCutsPeerRows (fails
on the previous HEAD with "journal lost p0's row").

Benchmarks on runner-3, 860,000-row journal (`scale_bench_test.go`):

| Version | Helper ready | 8 at once, p50 / max | Get with 8 clients, p50 / p99 |
| --- | --- | --- | --- |
| Previous HEAD | 1.86 s | 8.8 s / 15.8 s | 39.8 / 55.4 ms |
| Previous owner's WIP | 1.80 s | 2.65 s / 2.79 s | 32.9 / 47.9 ms |
| 5d74df7 | 0.077 ms | 15 ms / 36 ms | 0.071 / 0.191 ms |

## Deploy

- Pushed to GitHub 2026-11-15 (2a2d789..5d74df7, with the unpushed commits
  before it, scanned for secrets and the banned handle first).
- Pins in nicos-commit-delivery (`tools/gaterunner/install-tools.sh`,
  `internal/gaterunner/install.go`): source archive
  `64621d3b…5ba0`; amd64 `d9fd0a86…b592` built on runner-3 with
  go1.27.4; arm64 `cbe9a361…ac81` built on runner-1 with go1.27.3.
  Delivery fb584249. The box git pin moved to 2.56.1 to match the Mac
  (delivery 00602f48).
- `install-tools.sh --host runner-3` installed the binary into
  `/srv/build/bin` with both checksums verified.
- Live: scheduler release 0742176 (installed by another session from
  main, which carried the pins) has run since 20:52:15Z with gocache-lru
  `d9fd0a86…b592`. Job logs with "still waiting for GOCACHEPROG": 61 of
  about 360 jobs from 14:00 to 17:59Z, 0 of 76 jobs in the first 25 min
  on the new release. Waits had already stopped near 18:00Z, when
  intermediate releases briefly ran without the prog or with the new
  binary, so the clean comparison is the 0 of 76.
- Not yet re-tested: the delivery-queue-3d corpus check timeout (no job
  from that repository ran in the first window).
- runner-1 is a reserve: not installed there.

## Known limits

- Hint rows still grow the journal. Compaction under the put path keeps
  its old trigger; the journal is now safe to grow, not small.
