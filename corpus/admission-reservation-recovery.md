# Admission reservation recovery

Owner: codex/01a1c82a-4cc8-7950-ad12-380eb2d1ecce.
Status: source and installed recovery verified; programme acceptance remains open, 2026-11-11.
Work: WORK-3, WORK-220; acceptance also unblocks WORK-4.

## Decisions

1. Preserve the configured CPU, memory, swap and storage safety limits. At the
   post-acquisition check, evaluate fast-lane capacity excluding this operation's
   own not-yet-started lease. Other leases and measured coordinated CPU still
   count. Unleased callers receive no capacity credit. This prevents success at
   acquisition from revoking its own admission at the half-idle boundary.
2. Use the native explicit policy migration for the previously shipped storage
   recovery defaults. The installed migration changed only
   storage.critical_release_bytes from 21474836480 to 16106127360. Critical
   entry remains 10737418240; block_new_at remains critical. No data was deleted.
   The 15 GiB recovery default is a heuristic, not a measured job-space guarantee.

## Proof and journal

- Installed source 86aa23ffcb0725bf3fc19c11d3a25807a0f04961 entered repeated
  reservation/reacquisition for owned operation 8f2056c02d5a582b67c6de410b74f08e.
  Its child never started; only that owned wrapper was stopped.
- Runner-3 regression ab331c2d-67e9-409d-90e1-422905afd220 failed: reserving
  weight 2 with 4 free units admitted; acquiring it left 2 free and the same
  red sample revoked admission. The test also requires real additional
  contention and unleased callers to remain blocked.
- Corrected source passed focused fast-lane, admission and reservation tests
  with the race detector: runner-3 job 779ac887-c561-4ab9-8c80-e356e28085ee,
  package time 2.826 s. This is source evidence, not installed acceptance.
- An earlier regression submission e919e10b-38fa-48d3-9c77-784da2b66b8b ran
  against an empty snapshot after a local Python archive API mismatch. It failed
  before tests (missing go.mod); the snapshot was corrected before the real run.
- Native migration restarted the resident at PID 44970; its artifact verified.
  The first post-restart read had only one normal sample, so daily-driver-ready
  was false. A fresh full resident window remains required.

Private receipts are retained under the session workspace for this Owner:
work3-fastlane-regression-valid-gate.log, work3-fastlane-fixed-gate.log,
work4-flags-managed-storybook.log, work220-policy-migration.json,
storage-policy-before-1007.json, storage-policy-after-1009.json,
work220-post-migration-status.json and work220-post-migration-storage.json.

## Open acceptance

- [x] Guarded source capture and clean committed remote release.
- [x] Coherent installed CLI/helper/front-door identity and resident health.
- [x] Installed work starts without reserve/reacquire churn; isolated cancellation and release recover capacity.
- [ ] Headless WORK-4 checks after actual admission recovery.
- [ ] WORK-220 atomic job-space reservations and measurement remain open.
- [ ] WORK-3 seven-day real shadow and reserve-burst acceptance remain open.

## Installed recovery, 2026-11-11

Guarded capture published source 2581b6c06e42be54c403c611cf5237cf596d4219.
Runner-3 release job 467f396e-76c3-4f7c-b697-264d6407761d passed from a
clean committed bundle. Compare-and-swap installation replaced 86aa23f only;
the resident helper artifact SHA-256 is
a8ffde03f7fc78b144b9ca4e19c05e9c9158888636fe24bf8fd3fe348f949e7f.
The clean ndev factory subsequently passed runner-3 job
127f0473-d977-41c5-8582-d9357e735e11 and installed nicos-tools 8cdcf760f4f0
with this Pressure pin. Native doctor verified CLI, helper and front door all
at 2581b6c, mismatch false, resident PID 78550 loaded, healthy and fresh.

The 64-sample resident window had eight normal samples, RSS 17 MiB, observed
idle duty 0.217692%, sample CPU p95 21.535 ms (maximum 124.807 ms), and projected
telemetry 528384 bytes/day. An earlier old-source window exceeded the 0.5%
idle diagnostic at 0.563196%; it is not represented as passing. The fresh
window is a host observation, not a controlled performance comparison. The
24-hour swap-derate report still records 37 deferred operations and 67.85
minutes of wait; full admission-performance acceptance remains open.

The installed private-state lifecycle verifier passed with the live policy
unchanged: explicit cancellation, persisted dead-owner recovery and four
concurrent releases all returned capacity; final used capacity was zero.
Release wakeup was 106 ms and concurrent release 110 ms in this run. These
samples do not establish a tail-latency distribution.

The real Flags Storybook operation 40f39c1e21ceee5b6437d72f282b3c59 recorded
queued/acquired/started at the corrected source, with no reserve/reacquire
events, and listened only at 127.0.0.1:6009. It waited 62.295 seconds for
ordinary capacity. It does not reproduce the old half-idle boundary on demand;
the failing-then-passing source regression establishes that boundary. Headless
browser work remains refused under actual pressure/fairness admission, so no
UI acceptance is claimed.

Additional private receipts: work3-fastlane-native-build.log,
work3-fastlane-install.log, work3-fastlane-installed-lifecycle/result.json,
work3-fastlane-frontdoor-build.log, work3-fastlane-coherent-doctor.json and
work4-fixed-storybook-lifecycle.json.
