# Gate-runner evidence contract

`session-pressure --json remote inspect [--no-box] [--known-ledger SHA256]`
returns schema 1 in `inspection`. This is a read-only adapter over existing
commit-delivery receipts and the canonical gate runner. It does not schedule
jobs, change policy, or create another telemetry database.

The local ledger reads at most the last 4 MiB and 1000 complete JSONL rows.
Partial writes are excluded. Missing files produce an explicit empty state;
read errors are separate fields. Symlinks and special files are rejected.
The SHA-256 signature describes the committed suffix. A matching
`--known-ledger` returns `ledger_unchanged=true` without decoding that history.
Clients keep their prior runs, truncation flag and malformed count in that case.
Configuration still refreshes. Its displayed map is sorted by local path.

Remote inspection uses one SSH request, a 12-second process-tree deadline,
connect/keepalive limits, 1 MiB stdout and 4 KiB stderr limits. It reads the
last 1000 canonical job receipts and current Linux health. Missing or invalid
health is an explicit error. Nonfinite loads cannot enter JSON. The response
keeps at most 512 KiB each of delegate rows and jobs, and 256 KiB of config.
Counts and aggregates describe only the loaded window, not lifetime totals.

`remote job-log --id JOB` accepts only the canonical timestamp/PID job ID
and reads the first 100,000 bytes. Arbitrary paths and remote shell arguments
are not accepted. A failed log read is an error, not proof that a log never
existed.

`agreement` is the worker's recorded comparability verdict. A missing value
is unknown, even when two result booleans exist. A local environment failure
must not be promoted into a code disagreement or a clean agreement.

The desktop uses the shared bounded CLI client. It coalesces refreshes,
keeps prior successful data on errors, rejects results from cancelled view
generations, and polls only while its pane is visible and the app is active.
