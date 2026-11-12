# Output envelope parity

Status: 2026-11-12, producer race, full consumer race and normal-route installed read acceptance passed.
Owner: codex / 01a1c82a-4cc8-7950-ad12-380eb2d1ecce.
Work: WORK-2, Session Pressure contract repair discovered during WORK-83 verification.

The cross-repository producer contract gate found six inline response maps without the required `action` discriminator. Browser, remote help/status, process list, stop and historical record now emit explicit action strings. The strict checker remains unchanged. No process stop is invoked for acceptance.

The same gate also found 28 stale consumer schema declarations. These are separate consumer repairs; this producer change alone does not make the full contract package pass.

- [x] Runner-3 race test of the sealed producer candidate: job `a3ed5ab9-8f4f-4c4a-9ac1-14c1728c29be`, `go test -race -count=1 ./sessionpressurecmd`, exit 0, 3.513 seconds.
- [x] Guarded source commit 696fd668c9c6753fe98da5c536f3f54b6f54b43a. Complete native bundle passed runner 3 job 9b382135-781b-494c-a497-f4abac32cde5 and was promoted with expected-current 072e4d6744e2a392802e58846c98beb07e81ed38. Normal ndev passed browser status, remote help/status and processes list with the required discriminators.
- [x] Consumer schema parity and full strict contract race passed on runner 3: c9a70db5-ee0c-4155-be88-132d6f65f035, 66.989 seconds.

Evidence is retained in `/Users/user/.nicos-dev/<session-workspace>` and `work2-envelope-gate.log`.

Installed verification initially found the factory helper shadowed by the older authoritative standalone release. The complete six-artifact bundle was verified and promoted through the native installer; no binary precedence override or mixed bundle was used. Nine read-only normal-route commands passed their exact generated closed schemas, and all nine rejected an undeclared-field probe. Stop and historical record were not invoked. Consumer verification also exposed three skipped-history diagnostic fields written through a helper; the consumer checker now follows checked envelope arguments into same-package helpers and has a seeded missing-field regression. The initial checker was unchanged for the producer discriminator repair; this later strengthening is a separate consumer correction.

Private raw proof remains in the session workspace work2-installed-read-proof.json. Receipt summaries live in nicos-tools corpus/research/session-pressure-output-parity-installed-2026-11-12.json. WORK-2 remains open for delivery fairness observation and review; this is its cross-item contract repair slice. No fairness activation, process termination, cleanup or deletion occurred.
