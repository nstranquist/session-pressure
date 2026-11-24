# Garrid delivery queue and software factory integration contracts (2026-11-24)

Owner: antigravity. Status: implemented, 2026-11-24. Work: WORK-SP-8 (Epic WORK-SP-6).

## Scope

Verified and hardened the wire and CLI contracts between `session-pressure`,
the `ndev-commit-delivery` lane, and Garrid factory consumers:

1. **Work repository resource registration**:
   - After workspace consolidation moved the repository to its canonical tools
     location, native `--work WORK-n` delivery links failed with
     `WORK_REPO_UNREGISTERED`.
   - Updated the Work `session-pressure` repository resource atomically via
     `work control apply` (`repository.put`, revision 2) so delivery evidence
     links attach cleanly.
2. **Control API (`sessionpressurecontrol`) factory parity**:
   - Added read-only projections `GET /v1/pressure/check` (preserving
     structured non-zero admission JSON at HTTP 200) and
     `GET /v1/pressure/browser` (`browser status`), advertised via
     `/v1/health` capabilities.
   - Added `operation_id` filtering (`safeID`-validated -> `--operation-id`)
     to `GET /v1/pressure/work?view=history` so factory inspectors and desktop
     clients can query one operation's lifecycle without scanning the window.
   - Added closed-argv typed actions `recovery.clear`, `browser.pause`, and
     `browser.auto` under `/v1/actions/preview` and `/v1/actions/approve`
     (with single-use preview replay rejection and audit logging).
   - Wired `RunResult.StdoutExceeded` so truncated CLI output returns explicit
     `output_limit` instead of `invalid_authority_json`.
3. **Delivery lane & `work-run` contracts**:
   - Added `sessionpressurecontrol/factory_contract_test.go` verifying:
     - `/v1/health` capabilities, non-zero `check` JSON preservation, invalid
       `operation_id` rejection, and `output_limit` reporting.
     - Preview -> Approve -> Replay-409 lifecycle for `recovery.clear`,
       `browser.pause`, and `browser.auto`.
     - `SESSION_PRESSURE_WORK_EXIT_REPORT` distinguishing `session_pressure`
       exit 2 from child exit 2 and stripping the variable via
       `TakeWorkExitReportPath`.
     - Concurrent delivery-lane fair-share and `disk_write_high` throttling vs
       interactive and solo-run exemptions under `DecideResourceAdmission`.

## Downstream follow-up noted (cross-repo)

- `nicos-commit-delivery/go.mod` currently pins
  `github.com/nstranquist/session-pressure` at `669bc82` (2026-11-17) for
  `internal/commitdelivery/host_pressure.go`. No `policySkew` is observed
  against live `policy.json`, but bumping that module pin in
  `nicos-commit-delivery` will bring the embedded `sessionpressure` library up
  to date with the 2026-11-24 fixes.

## Verification

- `go vet ./sessionpressurecontrol ./pkg/sessionpressurecontrol`
- `go test -race -count=1 ./sessionpressurecontrol`
- Delivery capture with `--work WORK-SP-8` verifying live Work repo linking
  succeeds without `WORK_REPO_UNREGISTERED`.
