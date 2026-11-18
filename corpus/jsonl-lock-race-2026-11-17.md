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
- An n-1 waiter still has defects 1 and 2, so the race stays possible
  while an old binary appends to the same file. For one file this is not
  transitional: nicos-tools also appends to
  `~/.nicos-dev/notifications/inbox.jsonl` (`nicos-dev/internal/notify`,
  `cmd/nicos-logs`) through its own unfixed `lockfile`. The race on the
  inbox stays until WORK-1374 fixes nicos-tools.
- New files beside a JSONL file: `<file>.lock.reclaim` (empty guard, only
  on the stale path) and `<file>.lock.tmp-<pid>-<token>`. The temp file is
  removed after publish, but a crash between create and publish leaves it
  behind. The guard file is never removed, so it outlives an archived day
  file. Both are empty, so only the file count grows. No reader picks them
  up: every store globs `*.jsonl`, and the archive sweep takes only names
  that continue with a digit.

Behaviour changes for callers:

- A lock wait that runs out now matches `filelock.ErrTimeout` instead of
  `lockfile.ErrLocked`. No caller tested for `ErrLocked`.
- Goroutines in one process used to wait on a `sync.Mutex` with no limit
  and then got their own 5 s for the cross-process wait. Now one deadline
  covers the in-process queue and the cross-process wait. Many goroutines
  appending to one file, or slow `AppendLineDurable` fsyncs under swap, can
  now return `ErrTimeout` where the old code only waited. Callers log or
  return the error; none retries on the error type.
- Recovery after a crash changed. The old code reclaimed any sidecar older
  than 30 s. `filelock` kept a sidecar while its PID was alive, so a dead
  owner whose PID was later reused (after a crash or a host restart)
  blocked appends to that file until that process exited. Fixed by
  WORK-1373 (see below).

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
- Regression tests. Each jsonl test below fails on the old code and passes
  on the new code (Linux gate and Mac):
  - Defect 1: `TestAppendHonoursReclaimGuardAndKeepsSuccessor` pins only
    the jsonl side. A waiter does not remove a stale sidecar while another
    process holds the reclaim guard, and after the guard is released it
    keeps a live successor. The old code fails it because it removed the
    sidecar at once with no guard. It does not reach the second judgement
    under the guard, the exact defect-1 window. That needs the unexported
    seam and is pinned in `filelock` by `TestReclaimJudgesAgainUnderGuard`
    and `TestReclaimNeverRemovesSuccessorAfterOwnerReleased` (WORK-1225),
    which run on the code jsonl now calls. (The test was first named
    `TestAppendDoesNotRemoveSuccessorPublishedAfterStaleJudgement`, a
    stronger claim than it checks. It was renamed after review on
    2026-11-18.)
  - Defect 2: `TestAppendDoesNotPreemptAgedLiveOwner`.
  - Defect 3: `TestAppendDoesNotStealSidecarDuringInitialization`.
  `TestAppendReclaimsDeadOwnerSidecar` checks that dead-owner recovery
  still works.
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

## Review, 2026-11-18

An independent review of 52f8f41 found no way for two new binaries to
overlap. Rerun on main d92cfdc (with WORK-1259): `go test -race -count=3`
of jsonl and filelock passed on the Mac and on the Linux gate runner; the
16x100 stress was 0 of 30; the regression tests passed 20 of 20 (job
e1292a42). The review's other findings are recorded above: the inbox shared
with nicos-tools (WORK-1374), PID reuse (WORK-1373), the in-process wait
limit, leftover guard and temp files, and the defect-1 test scope. The
stress helper now waits up to 1 minute, so a loaded host cannot fail it on
the 5 s default. The test checks exclusion, not latency.

## Owner identity by process start time (WORK-1373, 2026-11-18)

A sidecar's owner is live only if its PID is alive and that process started
no later than the sidecar timestamp plus 10 s (`ownerStartSlack`). A live
PID whose process started after the record is a different process that
reused the PID after the owner died, so the sidecar is stale and the guarded
reclaim removes it. This covers a writer killed inside the lock and a host
restart. A real live owner of any age is still never preempted.

- Start time: Darwin `sysctl kern.proc.pid` (`p_starttime`, microseconds);
  Linux `/proc/stat` btime plus the start tick in `/proc/<pid>/stat`
  (USER_HZ 100, one-second btime resolution). Other platforms, or an
  unreadable start time, keep the old rule: a live PID is the owner. That is
  the safe side: never two inside.
- Slack: the owner writes its timestamp after it starts, so a real owner is
  always on the live side. The 10 s covers btime resolution and wall-clock
  steps backwards. A smaller slack risks preempting a real owner; a larger
  one only delays recovery when a PID is reused within the slack of the dead
  owner's acquisition.
- The check runs only when a create fails (one sysctl or two small /proc
  reads per judgement). The uncontended path is unchanged.
- The guarded stale path also removes `<file>.lock.tmp-<pid>-<token>` files
  of dead (or reused) PIDs, judged by the file's mtime. A live process's
  temp file is never removed, because its publish would then fail. The
  `.lock.reclaim` guard file stays: removing a flock file while another
  waiter may open it would split the guard.
- Sidecar format unchanged, so n-1 binaries are compatible. An n-1 waiter
  still uses its own rule.

Evidence:

- `TestSidecarOfReusedPIDIsReclaimed` (a live child process, sidecar
  recorded an hour before it started) failed on d92cfdc+e1da99a (2 s
  timeout) and passes now. `TestAppendReclaimsSidecarOfReusedPID` pins the
  same for jsonl.
- Kept: `TestSidecarOfLiveOwnerStartedBeforeTimestampIsKept` (real child),
  `TestAgedOwnerStartedBeforeRecordIsKept` (stubbed start time, record an
  hour old), `TestAgedLockOwnedByLivePIDIsNotReclaimed` and the jsonl
  defect-2 test, which now plant PID 1. Planting the test's own PID with an
  old timestamp is the reused-PID case, so those two tests changed owner.
- `TestOwnerAliveUsesStartTimeWithSlack` pins the slack edges and the
  unknown-start fallback. `TestPlatformProcessStartTimeReadsRealProcesses`
  checks the real readers against a spawned child (within 2 s) and a dead
  PID. `TestReclaimSweepsTempRecordsOfDeadProcesses` pins the sweep.
- Linux gate runner, job 9a7e4189: full `go vet ./...` and `go test ./...`
  passed; `-race -count=2` of filelock and jsonl passed; no new test
  skipped; filelock cross-process 20 of 20; jsonl 16x100 stress 0 of 30.
  Mac: `-race` filelock and jsonl passed.
- Latency, same job, 6 alternating runs, old (main) vs new:
  `BenchmarkAppendLine` 82 to 101 us vs 87 to 102 us;
  `BenchmarkShortLockHandoff` 3.88 to 4.11 ms vs 3.89 to 4.14 ms. Within
  noise on a shared runner (core count changed during the job).

## Open

- WORK-1374: nicos-tools `lockfile` and `filelock` still race; they share
  the notify inbox with this repository.

- Waiters poll with backoff (1 ms up to 50 ms) and have no cross-process
  queue. Under heavy contention on a loaded Mac a waiter can starve past its
  5 s wait (seen only in the 16-process stress, old and new code). Real
  callers do not run 16-way on one file. If they ever do, a kernel-queued
  wait (blocking flock on the reclaim guard or a wait file) would give FIFO
  handoff.
