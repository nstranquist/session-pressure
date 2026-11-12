# Output envelope parity

Status: 2026-11-12, producer race gate passed; installed and consumer gates pending.
Owner: codex / 01a1c82a-4cc8-7950-ad12-380eb2d1ecce.
Work: WORK-2, Session Pressure contract repair discovered during WORK-83 verification.

The cross-repository producer contract gate found six inline response maps without the required `action` discriminator. Browser, remote help/status, process list, stop and historical record now emit explicit action strings. The strict checker remains unchanged. No process stop is invoked for acceptance.

The same gate also found 28 stale consumer schema declarations. These are separate consumer repairs; this producer change alone does not make the full contract package pass.

- [x] Runner-3 race test of the sealed producer candidate: job `a3ed5ab9-8f4f-4c4a-9ac1-14c1728c29be`, `go test -race -count=1 ./sessionpressurecmd`, exit 0, 3.513 seconds.
- [ ] Guarded commit and installed read-only CLI acceptance.
- [ ] Consumer schema parity and full strict contract gate.

Evidence is retained in `/Users/user/.nicos-dev/<session-workspace>` and `work2-envelope-gate.log`.
