# Admission reservation recovery

Owner: codex/01a1c82a-4cc8-7950-ad12-380eb2d1ecce.
Status: source validated; release and installed acceptance pending, 2026-11-11.
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

- [ ] Guarded source capture and clean committed remote release.
- [ ] Coherent installed CLI/helper/front-door identity and resident health.
- [ ] Installed finite work starts without reserve/reacquire churn.
- [ ] Headless WORK-4 checks after actual admission recovery.
- [ ] WORK-220 atomic job-space reservations and measurement remain open.
- [ ] WORK-3 seven-day real shadow and reserve-burst acceptance remain open.
