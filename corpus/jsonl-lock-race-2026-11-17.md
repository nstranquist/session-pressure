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

## Exclusive-rename publish (WORK-1259, 2026-11-17)

Done. `filelock` now publishes the finished sidecar with an exclusive
rename: `renameat2(RENAME_NOREPLACE)` on Linux, `renamex_np(RENAME_EXCL)` on
Darwin (`internal/filelock/publish_*.go`). This drops the temp-file unlink
of the hard-link path. The hard link stays as the fallback when the file
system returns EINVAL, ENOSYS or ENOTSUP. The fallback is remembered for the
process, and other OSes use the link only. The create also calls
`MkdirAll` only when the open finds the directory missing, which drops one
stat per attempt. The sidecar format is unchanged.

Evidence, same job, old (52f8f41 `filelock`) and new alternating:

- Linux gate runner (job 20261117T192912Z-3155407):
  - `BenchmarkAppendLine` median 95.3 us old, 89.3 us new (5 rounds each;
    old 88 to 101, new 86 to 98).
  - `BenchmarkUncontendedAcquire` in one binary: hard link 73.1 us, rename
    69.7 us (medians of 5).
  - `BenchmarkShortLockHandoff`: equal (3.71 ms both).
  - jsonl stress 16 x 100, 200 us hold, -count=30: 30/30, 0 overlaps.
    filelock cross-process test x500 under 6 busy loops: 500/500.
- Mac (M4, a working desktop host, so wide spread):
  - `BenchmarkUncontendedAcquire`: hard link 0.74 to 0.81 ms, rename 0.26
    to 0.71 ms (medians 0.78 and 0.36).
  - `BenchmarkAppendLine` median 4.09 ms old, 1.52 ms new; handoff 8.3 ms
    old, 7.3 ms new. Both spread widely with desktop load.
  - filelock cross-process test x500: 500/500. jsonl stress: 0 overlaps in
    every run. In 42 new-code runs, 2 failed when a helper's 5 s lock wait
    ran out under 16-way contention on the loaded Mac. The old code fails
    the same way more often (2 of 12 runs against 0 of 12 new in the same
    back-to-back comparison).

The Linux gain is small (about 5%) but in the same direction for every
measure. The Mac gain is large. The change is kept.

Installed 2026-11-17 at about 19:52Z: a Darwin bundle cross-built from clean
d35eade on the Linux gate runner, promoted with `make
promote-session-pressure` (expected-current 56f36b8, which already held the
WORK-1225 and WORK-1252 fixes). Doctor and artifact status: CLI, front door
and helper all report d35eade. After the restart, work events, work-control
events and monitor telemetry kept appending complete JSON lines, and the new
code left no temp sidecars.

## Open

- Waiters poll with backoff (1 ms up to 50 ms) and have no cross-process
  queue. Under heavy contention on a loaded Mac a waiter can starve past its
  5 s wait (seen only in the 16-process stress, old and new code). Real
  callers do not run 16-way on one file. If they ever do, a kernel-queued
  wait (blocking flock on the reclaim guard or a wait file) would give FIFO
  handoff.
