# Work-event SQLite read index

Owner: codex / 01a1c82a-4cc8-7950-ad12-380eb2d1ecce.
Status: installed acceptance verified, 2026-11-11.
Work: WORK-103.

## Contract and decisions

1. Keep daily JSONL as the compatible durable write/interchange ledger. Agent
   reads use `work-events.sqlite`, indexed by operation, event and timestamp.
   The SQLite file is rebuildable and owner-only. Readers incrementally import
   appended records, re-import shortened/replaced shards and drop pruned ones.
   The resident monitor retains its bounded streaming counter.
2. Preserve the scan fallback when the index is unavailable. Both paths must
   return the same selected events and lifecycle diagnostics. Select matching
   whole operations before validating lifecycles; structural malformed rows,
   duplicate identities and impossible future timestamps still fail closed
   across the ledger. A filter must not hide a fault within its own operation.
3. Verify the installed CLI on frozen private ledger copies. Keep original
   ledger bytes and arbitrary command output out of the committed proof. The
   Go verifier records counts, digests, checks and timings. Normalize only
   command observation timestamps; three clock-derived rates permit a 1e-5
   relative/absolute tolerance. All event rows and other fields compare exactly.

## Source and installed journal

- Original index implementation: `8cd9d1c2a468b091f8d703398c3ec1d5be15f0be`.
  Its author's historical APFS measurements on 14 shards / 84,000 events were
  terminal lookup 217 ms to 0.39 ms, 24-hour read 352 ms to 48 ms, append
  19.8 ms to 10.5 ms and full read 362 ms to 388 ms. These are prior reported
  measurements, not newly reproduced installed timings or a universal speedup.
- Installed baseline source `20fb8e493c2ff5e52d738b9a11ee4912ddc42fed`
  contains that implementation. CLI and resident helper agreed; the verified
  helper SHA-256 was
  `68c2d1a82dc51d738815087edff48a929e6879220da32edadf76c8608d3e09c8`.
- Runner-3 job `683dd25a-2225-45e6-8865-272a8e064fd9` passed the existing
  index tests with the race detector (1.767 s package time).
- First installed parity attempt exposed wall-clock variation in three rates;
  event counts and the other statistics matched. The verifier was corrected
  to account only for those three rates, without weakening event comparisons.
- Second installed attempt exposed a real recovery inconsistency: an event
  filter through the scan fallback reported five skipped records from one
  unrelated malformed operation; the indexed path correctly scoped them out.
  The fallback now selects matching whole operations before lifecycle checks.
- Runner-3 job `c3fabef6-cd70-4536-9d06-52c75da53729` passed all WorkEvent
  tests with the race detector (2.483 s), including regression coverage for
  unfiltered, window, event and exact-operation reads with malformed lifecycles.
  Version 0.7.1 is a conservative patch release for this recovery correction.

Private evidence lives in the owner's native session workspace: installed
identity under `work103-installed/`, original failing frozen snapshots under
`work103-installed-replay/` and `work103-installed-replay-v2/`, and remote
receipts `work103-remote-index.log` / `work103-fallback-race.log`. No live ledger,
lease, policy or running monitor was changed by those replay attempts.

## Installed acceptance and limits

Clean guarded source `a4d3a0e01fb7256b3eba12f8481162412476e064` built with
native Darwin CGO on runner 3 (`2ef8c94e-0d4e-4e95-85c9-70456e9093fe`). The
installer compared its observed previous revision before promotion. The resident
helper is verified at the same revision. The rebuilt factory front door
`40e8cfbc991109b993300343ffb9131525597eb9` embeds that product revision;
installed doctor reports all three identities equal and the monitor fresh and
healthy. The existing swap-derate warning (121 minutes/day versus its
30-minute diagnostic budget) remains visible; no admission-policy recovery is claimed.

The hash-bound installed replay passed 16 checks on 55,757 records / 15 shards,
including fallback, filters, shrink, append, prune, rebuild and corrupt-ledger
refusal. Warm exact-operation median was 30 ms indexed versus 371 ms scanning;
warm 24-hour statistics median was 230 ms versus 417 ms. Cold construction was
2,103 ms versus a 465 ms scan; rebuild was 2,026 ms versus 462 ms. Full history
was 407 versus 423 ms and event filtering 392 versus 372 ms. Index maintenance
and cold construction have a real cost; these observations support faster warm
lookups, not a universal speedup. The scan remains a compatible recovery path.
Avoiding cold import would sacrifice recovery or require a separate maintenance
lifecycle; the current read cache preserves old-writer compatibility and
durable-log authority. Measurements compare paths in the same executable,
not causal fleet results or an old/new version benchmark.

The first successful installed replay emitted 30 successful Work commands and two
intentional malformed-private-ledger refusals in a 17-second window, plus one
successful monitor install. These negative fixtures remain expected refusals.
The second replay binds one resolved executable by SHA-256 before and after the
batch, with a two-minute command deadline. Machine-readable counts, timings,
identities, source jobs and limits are in [the acceptance report](research/2026-11-11-work-event-index-acceptance.json).
No private ledger rows or arbitrary command text are committed. The catalog
contract landed as `40e8cfbc991109b993300343ffb9131525597eb9`; its owned delivery
doctor passed. The verifier improvement prevents concurrent installation from
silently mixing revisions.

## Acceptance

- [x] Source SQLite import, parity, lifecycle and recovery regression checks.
- [x] Identify installed baseline and reproduce the actual fallback defect.
- [x] Guarded source capture and clean committed native-CGO remote release.
- [x] Guarded installation and installed frozen-ledger acceptance.
- [x] Fresh installed telemetry and indexed/scan comparison measurements.
- [x] Catalog storage contract delivered; final proof and closure linked.
