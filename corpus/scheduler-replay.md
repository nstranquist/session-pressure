# Scheduler replay evidence

Owner: codex / 01a1c82a-4cc8-7950-ad12-380eb2d1ecce. Status: in progress, 2026-11-11.
Work: WORK-3. No live admission policy change is included.

## Installed baseline

The installed `ndev session pressure work evaluate --json` returned 22/22
passing deterministic scenarios, with promotion not ready. Its retained
24-hour scheduling replay included 2,617 operations: test p95 queue wait fell
from FIFO 131,250 ms to candidate 89,499 ms, while build p95 rose from
218,346 to 240,644 ms and browser p95 from 71,214 to 101,186 ms.
Both had zero capacity and protected-bypass violations. Short jobs with long
waits fell from 169 to 124. These are modelled policy comparisons on the same
retained arrivals and runtimes, not measured before/after installation effects.
Host-pressure delays are excluded by the replay. Historical versions remain
mixed; the report does not establish cancellation or dead-owner correctness.

Evidence: private session receipt `work3-installed-evaluation.json`, recorded
2026-11-11 05:01 UTC. The seven-day lifecycle classification is retained in
nicos-tools `corpus/research/2026-11-11-pressure-retained-cohorts.json`.

## Decision 1 — expose the required evidence window

The evaluator fixed its input window at 24 hours, which cannot supply the
seven-day replay requested by WORK-3. Add `work evaluate --since DURATION`;
the default remains 24h, and 168h requests seven days. Report the requested
window and start time, plus skipped lifecycle diagnostics from the event store.
Retain promotion refusal and every existing safety/performance comparison.
The regression checks place real lifecycle pairs one and 48 hours back and
verify that only the longer window includes both; invalid windows are refused.

## Open acceptance

- [x] Runner-3 command-package race checks pass for the exact final source.
- [ ] Delivery lands; a clean installed CLI supports the option.
- [ ] Run the installed seven-day replay and investigate class regressions.
- [ ] Demonstrate installed release wakeups, cancellation and dead-owner recovery.
- [ ] Use seven-day evidence to accept, revise or reject reserve/burst policy.

## Session journal

- 2026-11-11: Installed baseline exposed a performance tradeoff; WORK-3 stays
  open. The first remote test archive was empty after local Python rejected
  an unsupported extraction argument; the remote command failed before tests
  because go.mod was absent. Recreated the sealed archive from committed
  cee893a0ac5cabf67ce6b9cd4d057b2a1c927b76 plus the two owned command files
  and reran on the same runner 3. This is a setup failure, not a source result.

- 2026-11-11: The first populated test run failed because the test fixture's
  completed event omitted its required lease ID. Corrected the fixture; the
  focused race run passed (job 51929cc1-9555-4a9c-9588-2bd3f76f2800, 1.348s).
  The full package then passed (da1cbbb1-799a-49f2-8aae-08380aa4382b, 3.331s).
  Added skipped-lifecycle diagnostics to text output as well as JSON, then
  repeated the full race suite for the exact final source on runner 3:
  c63f6aed-e1c0-46ba-a434-fdad80e4728a, exit 0, 3.177s. No live policy was changed.
