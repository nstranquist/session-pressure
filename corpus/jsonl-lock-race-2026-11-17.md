# JSONL append lock race (WORK-1252, 2026-11-17)

Owner: claude. Status: fixed, 2026-11-17.

## Symptom

`internal/jsonl` (`AppendLine`, `AppendLineWithin`, `AppendLineDurable`)
took its cross-process lock from `internal/lockfile`. That package had the
same stale-reclaim race that WORK-1225 fixed in `internal/filelock`, and two
more defects. Two processes could append to the same JSONL file at the same
time. The writers are the work-event ledger, telemetry snapshots and
actions, the disk-write store, storage receipts, the NVMe SMART history and
the notify inbox.

## Root cause

`internal/lockfile` had three defects:

1. Unguarded reclaim. A waiter that lost the create judged the sidecar, and
   if it judged it stale it ran `os.Remove` on whatever file was there. A
   missing sidecar also counted as stale. A successor that published its
   live sidecar in between was deleted, and two writers were inside (the
   WORK-1225 race).
2. Age preemption. A live owner older than 30 s counted as stale, so a slow
   append (for example a writer paged out under swap) was preempted.
3. Empty sidecar window. The sidecar was created with `O_EXCL` and written
   afterwards. A competitor that read the empty file counted it stale and
   removed it.

## Decision: delete `internal/lockfile`, use `internal/filelock`

`internal/filelock` already has the WORK-1225 fixes: a missing sidecar means
"retry", a stale sidecar is removed only under the non-blocking `flock`
reclaim guard after a second judgement, a live owner is never preempted for
age, and the sidecar is published complete with a hard link. Keeping two
lock packages would keep two copies of the hardest code in the repository.
`internal/jsonl` now calls `filelock.WithFileLockContext`, and
`internal/lockfile` is deleted. `internal/jsonl` was its only importer.

On-disk contract, compatible with n-1 binaries:

- Same sidecar path (`<file>.lock`) and the same JSON body
  (`{"pid","timestamp","token"}`), so old and new binaries exclude each
  other.
- An n-1 writer can still show an empty sidecar for a moment. `filelock`
  leaves a malformed sidecar alone for 30 s (`lockInitializationGrace`).
- An n-1 unlock removes a sidecar only when its token matches or the token
  is empty, and new sidecars always carry a token.
- An n-1 waiter still has defects 1 and 2, so the race stays possible only
  while an old binary appends to the same file.
- New files beside a JSONL file: `<file>.lock.reclaim` (empty guard, only
  on the stale path) and short-lived `<file>.lock.tmp-<pid>-<token>`. No
  reader picks them up: every store globs `*.jsonl`, and the archive sweep
  takes only names that continue with a digit.

API change: a lock wait that runs out now matches `filelock.ErrTimeout`
instead of `lockfile.ErrLocked`. No caller tested for `ErrLocked`.

## Evidence

- Stress test `TestAppendLineExclusiveAcrossProcesses` (16 processes x 100
  back-to-back `AppendLine` calls, 200 us spin inside the lock, a ledger
  records entry and exit). Linux gate runner:
  - old code, 30 runs: 30 of 30 overlapped, 465 overlaps in 48,000
    appends, job aebfe471.
  - same runner, old and new binaries in one job, 10 runs each: old 10 of
    10 overlapped (155 overlaps), new 0 of 10, job b32dc98c.
  - new code, 30 runs: 0 of 30 overlapped, in the same job as the full
    `go vet ./...` and `go test ./...` (job ID not captured).
  - Mac (M4): old code overlapped in 2 of 3 default runs (12 x 20); new
    code 0 of 5.
- Regression tests, one for each defect. Each fails on the old code and
  passes on the new code (Linux gate and Mac):
  - `TestAppendDoesNotRemoveSuccessorPublishedAfterStaleJudgement` (the
    test holds the reclaim guard, swaps in a live successor, then releases
    it),
  - `TestAppendDoesNotPreemptAgedLiveOwner`,
  - `TestAppendDoesNotStealSidecarDuringInitialization`.
  `TestAppendReclaimsDeadOwnerSidecar` checks that dead-owner recovery
  still works. The `filelock` seam tests from WORK-1225 cover the
  missing-sidecar form of defect 1 at the package level.
- Latency, Linux gate runner, same job b32dc98c, 6 alternating runs of
  5,000 appends:
  - uncontended `AppendLine`: old 63 to 71 us, new 83 to 92 us (+21 us).
    The cost is the private temp file plus the hard-link publish that
    closes defect 3 (one extra link and one extra unlink).
  - contended, 16 processes: old 491 to 498 us per append (with overlaps),
    new 359 to 400 us. The `filelock` retry starts at 1 ms and doubles to
    50 ms; the old poll was a flat 50 ms.
  - `AppendLineDurable` is dominated by fsync (3.3 to 6.3 ms on both).
- Mac numbers (old 0.54 to 1.88 ms uncontended) were too noisy on a busy
  desktop host to compare.

## Open

- The +21 us uncontended cost could drop with an exclusive rename
  (`renameat2(RENAME_NOREPLACE)` on Linux, `renamex_np(RENAME_EXCL)` on
  Darwin) in place of link plus unlink, with a link fallback for file
  systems that refuse it. That is a `filelock` change for every caller and
  needs its own Linux and Darwin proof.
