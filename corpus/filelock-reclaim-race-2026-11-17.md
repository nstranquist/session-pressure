# File lock reclaim race (WORK-1225, 2026-11-17)

Owner: claude. Status: fixed, 2026-11-17.

## Symptom

`TestExclusiveCriticalSectionAcrossProcesses` failed about one full Linux
gate run in three (verify of c2c2594 passed 2 of 3). The ledger showed a
second process start while the first still held the lock. No helper process
had died.

## Root cause

A time-of-check to time-of-use race in stale reclaim in
`internal/filelock`. A waiter that lost the create (`os.Link` returned
EEXIST) called `isStale`, then `removeLock`:

1. Waiter W lost the create to owner A.
2. A released: the sidecar was removed.
3. W called `isStale`. A missing sidecar counted as stale.
4. Successor B published its own live sidecar.
5. W removed the sidecar blindly. That was B's live lock.
6. W created its own sidecar. B and W were now both inside.

The same race applied to a dead-owner sidecar: two waiters judged it stale,
the faster one reclaimed and created, and the slower one removed the faster
one's live lock. No dead process is needed for the first form, so the
d4d6151 change (no fsync of the ownership record) is not the cause.

## Fix

- A missing sidecar is never removed. The waiter retries the create at once.
- A stale sidecar (dead PID, or malformed and older than the n-1
  initialization grace) is removed only by the waiter that holds the
  reclaim guard: a non-blocking `flock` on `path + ".lock.reclaim"`. Under
  the guard the waiter judges the sidecar again and removes it only if it is
  still stale. Inside the guard no other waiter removes the sidecar, and
  `os.Link` never replaces an existing file, so the file judged stale is the
  file removed.
- The guard is non-blocking. A waiter that finds it held polls. A stopped
  reclaimer cannot hang others, and the kernel drops the guard when its
  process exits.
- A removal that fails (other than "not found") makes the waiter poll until
  its timeout instead of spinning.
- The link-published sidecar format is unchanged, so n-1 binaries still
  exclude each other with this version. An n-1 binary still reclaims without
  the guard, so the race stays possible only while an old binary runs.
- The guard file stays next to the sidecar. It is empty. The hostcleanup
  store reads only `*.json`, so it ignores it.

Unix only (`syscall.Flock`). The repository has no Windows target.

## Evidence

- Reproduction (stress test, 16 processes x 100 back-to-back acquisitions,
  200 us hold, 30 runs on the Linux gate runner):
  - old code f82f802: 12 of 30 runs overlapped (14 overlaps in 48,000
    acquisitions), job 20261117T183023Z-1310847.
  - fixed code: 0 of 30, job 20261117T183140Z-1316884.
- A plain `-count=200` run of the old 12-process test under 6 busy loops did
  not reproduce (job 62a79687): the window is microseconds wide.
- Regression tests: `TestReclaimNeverRemovesSuccessorAfterOwnerReleased`,
  `TestReclaimJudgesAgainUnderGuard` (a seam puts a competing reclaimer
  between the first judgement and the guard),
  `TestReclaimGuardExcludesConcurrentReclaimers`. The cross-process test now
  runs 12 processes x 20 rounds, so each run crosses the window far more
  often (about 1.2 s a run, was 0.46 s).
- Acceptance runs of the committed cross-process test (12 x 20), fixed code:
  - Linux gate runner, under 6 busy loops: 500 of 500 passed, with
    `go vet` and the package suite, job 20261117T183323Z-1323785.
  - Mac (M4), on a working desktop host: 500 of 500 passed in 8 min 44 s.
- Latency, `BenchmarkShortLockHandoff` on the Mac, 6 x 300 handoffs: old
  4.86 to 5.31 ms/op, new 4.64 to 5.90 ms/op. The difference is within
  run-to-run noise. The uncontended path and the handoff path are unchanged;
  only the rare stale path adds an open and a flock.
