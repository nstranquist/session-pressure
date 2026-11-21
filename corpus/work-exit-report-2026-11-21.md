# Work run exit source report (WORK-1627, 2026-11-21)

Status: 2026-11-21, landed (9c1282d, CLI refusal follow-up 1fc9aee) and installed on the Mac. Owner: claude/3bd639ce-6e3f-4246-9482-a95928c1b14e. Work: nicos-tools WORK-1627.

## Problem

`work run` exits with the wrapped command's status. A wrapper that sees only the number cannot tell a wrapped command's exit 2 (for example, a test binary's usage error or a gate that says no) from a session-pressure usage error, which is also 2. ndev's `session pressure work run` rows used the number alone, so every wrapped exit 2 became `usage_error`/`invalid_input`. Policy codes 11-15 and 124, 126, 127 and 130 had the same collision.

session-pressure's own telemetry was already correct: it records only statuses it chooses before the helper starts. The wrong rows came from the ndev wrapper (`session.pressure.command` operation event and the `command.run` invocation row).

## Contract

`SESSION_PRESSURE_WORK_EXIT_REPORT` names a file that the wrapper creates (empty, mode 0600, absolute path) before it starts `work run`, and reads after `work run` exits.

```json
{"schema_version":1,"source":"child","exit_code":2}
```

- `source` is `child` (the wrapped command set the status) or `session_pressure` (session-pressure chose it: arguments, policy, admission, transport, internal failure).
- `child` covers a local child, a remote runner job, a replayed reuse receipt, a child that died from a forwarded signal, and a nested run that replaced `work run` with the wrapped command.
- `exit_code` is the status that `work run` returned. A nested run writes the report before its exec, so it has no `exit_code`; the wrapper sees the status. A report whose `exit_code` differs from the observed status does not apply (`AppliesTo`).
- Writers open an existing regular file only (`O_NOFOLLOW`, no create). Readers accept 4096 bytes at most, refuse unknown fields, versions and sources, and treat an empty file as no report (an older release).
- The helper removes the variable from its environment, so no wrapped command inherits it. A nested `work run` therefore cannot overwrite an outer run's report.

Where the source comes from: `RunWorkCommand` returns `(code, err)`, and it returns an error with every status it chooses itself. `WorkExitSourceFor(err)` is the single classification.

Writers:

| Stage | Writes |
|---|---|
| CLI `work run` refuses an argument or `--json` (2), or cannot start the helper (1) | `session_pressure` (`ReportOwnWorkExit`) |
| Helper refuses arguments or options | `session_pressure` |
| Helper finishes a run | `WorkExitSourceFor(err)` with the code |
| Nested run, before exec | `child` without a code; a failed exec rewrites it as `session_pressure` |

## Consumer (nicos-tools ndev)

`ndev session pressure work run` creates the file, passes the path, and records `exit_source` on both rows. A nonzero `child` exit is `expected_negative`/`gate_not_satisfied` (130 is `cancelled`/`interrupted`), the class that nicos-tools telemetry ingest already gives a wrapped command exit (`wrappedCommandExit`). A `session_pressure` exit or a missing report keeps the exit-code mapping (2 stays `invalid_input`).

WORK-1627 asked for `command_failure`. `expected_negative`/`gate_not_satisfied` was chosen because the reviewed ingest rule already uses it for wrapped exits, so the live rows and the ingest backfill agree, and a failing test is not an ndev failure.

## Evidence

Tests: `work_exit_report_test.go` (round trip, no create, no symlink, rejected content, the variable is removed, a child 2 against an own usage 2, nested report before exec), helper `TestRunWorkCommandReportsOwnUsageError`, CLI `TestPressureWorkRunReportsCLIRefusals`.

Installed release 9c1282d, direct check: `work run --class express-test -- sh -c 'exit 2'` returned 2, wrote `{"schema_version":1,"source":"child","exit_code":2}`, and the child saw the variable unset. Before the follow-up, `work run --no-such-flag` left the file empty, because the CLI refused it before the helper started; 1fc9aee closes that.

## Rollback

Install the previous release with `session-pressure-release install --expected-current <current>`. An older release writes no report, and ndev falls back to the exit-code mapping.
