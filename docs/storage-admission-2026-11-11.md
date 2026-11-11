# Storage admission review, 2026-11-11

Owner: codex/6af3c4ea-2860-8191-a23e-231cb2232002. Work: WORK-220.
Requested outcome: make the guard proportionate to actual disk risk, rather than
requiring an arbitrary 100 GB free. No data deletion or live threshold change is
part of this source correction. The new source default narrows critical recovery
from 20 to 15 GiB, but the installed policy remains unchanged until release
validation and an explicit migration.

## Findings and confidence

At 2026-11-11T01:31:17Z, resident statfs measured 19,539,636,224 bytes free on
`/System/Volumes/Data`: 19.54 decimal GB / 18.20 GiB. The filesystem total was
994,662,584,320 bytes. A separate df read and Foundation ordinary-capacity read
agreed within intervening writes. High confidence: physical unallocated bytes.

Nico reports about 58 GB available in the macOS display. An independent UI
inspection during this review resolved the difference: System Settings showed
932.63 GB used of 994.66 GB, or 62.03 GB available. Disk Utility's Data volume
showed 62.29 GB available, explicitly including 42.61 GB purgeable, with
19.67 GB actually free. The small differences reflect capture timing and ongoing
writes. This is the same APFS container and is not merely a GB/GiB mismatch.

The shell-side Foundation important/opportunistic capacity probes returned zero
while their XPC service was unavailable, and diskutil could not use
DiskManagement. Those zeros were unavailable data, not evidence of no purgeable
space. tmutil listed a Time Machine local snapshot dated 2026-11-09 plus three
macOS update snapshots; their contribution to the purgeable total is unknown.
The UI proves reclaimable capacity exists but not when every byte can be
reclaimed for a particular concurrent operation. Do not silently treat it as
already-empty space or delete snapshots as a diagnostic.

At the initial 01:30 UTC check, the installed application policy was warn
50 GiB, red 25 GiB, critical 10 GiB, with `block_new_at=red`;
recovery is 60, 30, 20 GiB respectively. The optional cleanup target is 60 GiB.
The source defaults use a 50 GiB cleanup target; persisted policy wins. These are
application resource limits, not macOS settings. This worker made no change to the installed values. At 01:45 UTC a fresh
read found `block_new_at=critical`, with policy mtime 01:38:12 UTC. The coordinator subsequently verified that a parallel chat applied Nico’s
explicit instruction to remove the 30/100 GB rule, recorded in board decision
`d-06gsggp7lt0frv8dng`. It means broad work currently
uses critical admission (10/20 GiB), not the initial red admission (25/30 GiB).
The authorized change was preserved. The source now proposes 15 GiB
critical recovery instead of 20 GiB. This retains a 5 GiB hysteresis band above
the 10 GiB emergency reserve, matching the existing red recovery band. These
numbers are operating heuristics, not empirically proven job-size guarantees.

The 100 **GiB** requirement is from the nicos-tools Nov 9 restructure plan,
initial commit `ca5db42c5`, not this runtime guard. It is a freeze-window entry
condition; 150 GiB is a separate cleanup-program aspiration. Neither is a
universal requirement to use the Mac or run a small build. The plan records no
peak-space calculation supporting the fixed 100 GiB. Existing local edits to
that plan and its runsheet belong to other work and were preserved.

Retained resident samples are sparse events/heartbeats, not continuous
per-workload traces. The Nov 8–11 samples include a 19.39 GiB net decrease in
54 minutes and a 33.67 GiB decrease in 114 minutes. Nov 9 23:42 UTC had 101.8 GiB
free; the current physical reading is about 18 GiB. Swap reached 7.16 GiB in this
sample window on a 16 GiB machine. Existing swap is already included in consumed
space and must not be subtracted again. Net capacity changes are not total write
traffic, per-job attribution, or a forecast of future growth.

The extraction rehearsal recorded final repositories of 2.7 MB (mobile) and
4.8 MB (desktop), but no transient high-water mark. At review time, nicos-tools
tracked HEAD contents totalled 306,762,413 logical bytes (0.286 GiB), and Git
reported 1,714,384 KiB packed plus 45,312 KiB loose objects. Those are useful
inputs, not peak requirements: clones, repacks, build caches, retained rollback
copies, and simultaneous jobs can overlap.

## Immediate correction

- Keep express tests/builds and installs usable under red pressure. The existing
  exemption accidentally admitted them even at near-zero critical capacity.
  They now respect the existing critical threshold and recovery hysteresis.
  Reclaim is still available at every storage level.
- Recovery advice requests the actual class-specific release target. Broad work
  asks for the configured release (30 GiB when blocking at red, 20 GiB with
  the concurrently changed critical-only policy); critically blocked express/install
  work asks for the configured critical release (20 GiB installed today, 15 GiB
  after the new default is explicitly adopted). It does not prescribe 50 GiB
  for every waiter.
- New default critical recovery is 15 GiB instead of 20 GiB. Entry remains
  10 GiB. The explicit policy migration handles only exact former shipped
  threshold sets; custom thresholds/targets are preserved. Loading a persisted
  policy does not silently migrate it. All authority switches remain unchanged.
- The 60 GiB cleanup goal is explicitly optional, separate from admission.
- Show physical free space in GiB and decimal GB. Missing capacity does not
  produce a fabricated shortage.
- Recommend a read-only plan first. Do not advertise personal Trash deletion,
  full Go-cache wipes, or report-only simulator providers as automatic relief.
- Keep the source-default unknown-job 25/30 GiB fallback, all host/memory checks, existing
  unavailable-probe behavior, and operator observe/disabled policy semantics.

## Budgeted admission plan

A fixed lower number cannot establish that every compiler or extraction fits.
The next contract should admit known work when:

`physical free >= operating reserve + outstanding reservations + job peak growth`

Use 10 GiB only as an explicitly provisional operating reserve inherited from
existing critical policy, not a measured universal macOS requirement. No
reclaimable-space credit. A job estimate is not a filesystem quota. Reserve
atomically with the existing work lease; preserve it for live children and
crashes, release on verified completion, and reject missing/stale capacity.
Unknown jobs retain their configured gate; do not silently overwrite an
operator-selected admission band. Keep full reservations until remaining
growth can be measured reliably. Independent applications can consume space,
so the formula still needs monitoring and a stop/recovery plan.

Before enabling this contract, cover concurrent acquisition, explicit zero vs
unknown budget, overflow, expired wrappers with living children, remote/local
placement, filesystem identity, mixed installed versions, and release/reacquire.
Collect operation-specific peak traces; do not rename aggregate host depletion
as measured job growth. This work is tracked in WORK-220, not claimed shipped.

For extraction, replace the old blanket gate with a freshly measured rehearsal
budget plus operating reserve, retained recovery copies, and concurrent-growth
allowance, bound to the exact source/commands. A quiescent freeze and serial
copies can reduce that budget. Until these inputs exist, do not substitute a
made-up 30/40/50 GiB minimum or treat a small final repo as a peak measurement.

## Verification

- Focused Darwin tests via the installed work coordinator passed:
  `go test ./sessionpressure -run 'Test(EvaluateStorage|Storage|NativeStorage|LoadPolicyMigratesStorage|FormatStorage|DefaultStorage)' -count=1`.
  Operation `07a15cd63a40f446a3216d2d0b02f462`, 31.8 s including compilation,
  0.282 s test execution. Final broader storage regression also passed after the
  custom-hysteresis correction: `go test ./sessionpressure -run 'Storage|FormatStorage' -count=1`,
  operation `81e8772ca3cefc0eab4c827a88883f50`, 35.1 s total / 0.356 s tests.
  Explicit local placement was for Darwin native capacity
  validation. This was a functional test, not an isolated disk-peak benchmark.
- Regression coverage: all work classes at critical entry and latched recovery,
  red-mode access, observe-only policy, exact custom-byte targets, unavailable
  capacity, and no destructive or excessive cleanup instructions.
- Source tests do not establish installation, live release acceptance, or a soak.
  Publication and installation status must be reported separately.

## Current release blocker

A remote focused regression request was refused before execution: no eligible
healthy runner for `session-pressure-work/linux-native/light`; runner-3 SSH
health probe exited 255. Local fallback was disabled and not bypassed. The final
native focused checks above passed. The production five-artifact native bundle
must still be built from committed source on a healthy canonical runner, then
installed with the expected-current fence and resident acceptance checks. The
installed revision at review was `b90b4b80bf0a74417faa81535d9d96db79b905aa`.

Numeric-change validation adds the exact 10 GiB entry boundary and 15 GiB
critical recovery boundary, custom-threshold/target preservation, unchanged
operator authority settings, migration idempotence, and persistence compatibility.
An existing critical-release value of 20 GiB survives a normal policy load.

The final source-default/migration regression passed on Darwin:
`go test ./sessionpressure -run 'Storage|CriticalRecovery' -count=1`, operation
`1735ddea56cac93b644f4520a2e1f83a`, 78.9 s total / 0.480 s tests. Independent
read-only review approved the patch; it did not duplicate execution.

The CLI migration integration also passed:
`go test ./sessionpressurecmd -run TestCmdSessionPressurePolicyMigrateStorage -count=1`,
operation `29462a08a157917c442e67f379955e23`, 63.6 s total / 0.261 s tests.
The fake launchd controller and temporary policy directory mean this test did
not migrate the real host policy or restart its resident.
