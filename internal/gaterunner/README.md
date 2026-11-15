# Gate-runner evidence contract

`session-pressure --json remote inspect [--no-box] [--known-ledger SHA256]
[--known-jobs SHA256]`
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

The box job text has its own SHA-256 `jobs_signature`. A matching
`--known-jobs` returns `jobs_unchanged=true` and no job rows; the client keeps
its prior rows. On a live runner-3 read this removes about 550 KB of the
1.26 MB response each minute while receipts do not change.

A failed SSH read sets `box_failure` (and capacity host errors use
`runner_capacity_<kind>`) to one of `timeout`, `connect_failed`,
`remote_failed` or `output_too_large`, so clients can say what failed
without parsing stderr.

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

## Runner capacity

`session-pressure --json remote capacity [--window 6h|24h|7d|14d]` returns
schema 1 in `capacity`. It reads every host in the installed fleet inventory
in parallel, one SSH request per host with the same 12-second deadline and a
1 MiB stdout bound. The probe is fixed text plus three integers and one
timestamp computed locally; no inventory value reaches the remote shell.

Per host it reports:

- the runner's own 15-second `host_samples` (scheduler `queue.db`), bucketed
  on the host: 5 minutes (6h), 15 minutes (24h), 1 hour (7d), 2 hours (14d).
  Each bucket carries CPU average and 15-second peak, I/O wait, minimum free
  memory, heavy/light slot use, and the exact share of samples with every
  heavy slot busy. A runner without `host_samples` reports
  `samples_present=false`; nothing is estimated in its place.
- a `jobs.log` rollup over the window (last 50,000 receipts): jobs per hour,
  non-zero and SETUP exits, job time, and the top callers and repositories.
- load, memory, disk of `/srv/build` (or `/`), and the default-route NIC
  counters, link speed and tailnet share.
- the provider's published traffic terms (Hetzner dedicated 1 Gbit/s:
  unmetered; Hetzner Cloud EU: 20 TB outbound per month) and a monthly
  projection from the since-boot average.

Runners keep no network history, so each read records NIC counters in
`runner-capacity.sqlite` in the session-pressure data directory (at most one
sample per host every 30 seconds, kept 35 days). Rates come only from
consecutive samples on the same boot and interface no more than 15 minutes
apart; reboots, counter resets and gaps break the series. Coverage seconds
are reported so a partial history is never read as a quiet one.

Alerts are diagnostics, not gates: unreachable host (critical when active),
current load and memory, disk at 80/90%, CPU episodes at or above 85%,
heavy-slot saturation episodes, memory below 10%, I/O wait at or above 10%,
peak NIC rate at or above 70% of the port, projected traffic at 70/90% of an
allowance, SETUP failures, a non-zero exit share of 20% (10+ jobs), missing
samples, and an active runner averaging under 5% CPU and 10% heavy-slot use
over a day or more. Consecutive flagged buckets merge into one episode; a
missing bucket ends it.

The desktop's Hetzner tab shows this as "Fleet capacity & usage" and polls
once a minute while visible, keeping the last good report on failure.
