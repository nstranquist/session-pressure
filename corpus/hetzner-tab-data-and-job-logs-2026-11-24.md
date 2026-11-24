# Hetzner tab: real delegate data, full receipt window, job logs

Work: WORK-SP-3. Owner: claude (session 46175392). Date: 2026-11-24.
Status: landed and pushed as `5de9b4e`, installed and live-checked on
the primary Mac the same day.

## What was wrong

Measured live against runner-3 before the change:

| Symptom | Cause |
| --- | --- |
| Gate-delegate card showed 0 runs | The worker moved the ledger from `gate-delegate/runs.jsonl` to the `gate-delegate.runs` stream in `~/.nicos-dev/commit-delivery/v1/side-streams.sqlite`. The reader still read the JSONL file. |
| Receipts covered 206 of 1,000 lines (about 48 minutes) | Receipt lines reach about 21 KB (escaped commands). The 512 KB wire bound cut the list. |
| Each refresh pulled 6.5 MB over SSH | `boundedCapture` embedded `bytes.Buffer`, so `io.Copy` used `ReadFrom` and skipped the limit. |
| Delegate rows showed `go run` | The `command` field is a short label. The real argv is in `verify.argv`. |
| Receipt commands showed `ncd` where a newline was | The runner writes `cmd=` with bash `printf %q`, which uses `$'…'` quoting for multi-line commands. The generic unescape dropped the backslash. |
| No way to read a job's output in the app | Not built. |

## Decisions

- Delegate runs are read from side-streams.sqlite through `/usr/bin/sqlite3
  -readonly` (no in-process engine). A closed WAL store has no `-shm`, so a
  read-only open fails with SQLITE_CANTOPEN. The reader then retries with
  `immutable=1`, but only when the `-wal` file is missing or empty
  (`internal/gaterunner/sqlite_read.go`). The cursor signature is
  `stream:count:max(seq)`.
- The runner cuts each receipt line to 512 bytes with awk and marks it with
  a trailing `\x01`. `Job.command_truncated` tells the client. The job log
  carries the whole command.
- `cmd=` is decoded by `unquoteShell`, tested by a round trip through real
  `bash printf %q`. A cut line that ends inside a quote keeps its prefix.
- Job log read (`remote job-log --id`): up to 200 KB whole; longer logs are
  the first 40 KB plus the last 160 KB with an "N bytes omitted" line.
  Receipt IDs read `/srv/build/logs/jobs/<id>.log`; lowercase UUIDs read
  `/srv/build/scheduler/<uuid>.log`. Any other ID is rejected before SSH.
  The JSON adds `job_log` (kind, bytes, omitted_bytes, command) and keeps
  `log` for older clients.
- App: live jobs, recent runs and receipts open an inline log panel. A
  running job's log follows on each 15 s tick and gets one last read when it
  ends. A failed refetch keeps the last good log.

## Proof (this session)

- Live: delegate runs 0 to 1,000 (936 verdicts used, 1 divergence that was
  hidden before). Receipts 206 to 1,000 (about 3 days, 436 cut). Inspection
  JSON 1.55 MB. SSH receipt read 6.5 MB to about 0.5 MB.
- Live job logs: a running scheduler job; a cut receipt returns its full
  20.5 KB command, decoded into lines; a 47 MB log returns head and tail
  with 47,174,624 bytes omitted.
- `go test ./internal/gaterunner/ ./sessionpressurecmd/` pass. `swift test`
  passes 159 tests in 29 suites.
- Windowless ImageRenderer render (no window, nothing on the desktop) shows
  the running-job panel with the FOLLOWING chip, the receipt panel with the
  full command and copy button, and delegate rows with full argv.

## Installed acceptance (2026-11-24 20:11Z)

- First lane run paused with VERIFY_FAILED on the Linux runner: the test
  helper passed a >128 KiB SQL script as one argv string, over Linux
  `MAX_ARG_STRLEN` (macOS has no such cap). The helper now sends it on
  stdin; the replacement delivery passed the runner gate and the full
  publish verify.
- CLI: `session-pressure-release install` from a clean clone of `5de9b4e`
  (expected current `37e6094`), `monitor install` (resident reporting
  after 800 ms, source `5de9b4e`), front door rebuilt with
  `build-committed-ndev`; `artifact status` reports cli, front door and
  helper agree on `5de9b4e`.
- Through the installed front door: `remote inspect` returns 1,000 runs and
  1,000 receipts with the box reachable; `remote job-log` returns
  `job_log` with the decoded multi-line command.
- App: `make install-headless` (signed, Team ID verified, no relaunch). The
  running app picks the new build up on its next launch.

## Open

- The render harness lives in `/tmp/hz/render`. It could become a repo tool
  for windowless view checks.
