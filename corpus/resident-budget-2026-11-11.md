# Resident memory-budget accounting

Owner: codex, session 01a1ca44-81fd-72b2-a89f-41b779deb34d.
Status: implemented, installed and verified, 2026-11-11. Work: WORK-604.

## Scope and evidence

Keep Claude and Chrome running. Investigate remaining host CPU pressure and
monitor self-budget warnings without cancelling jobs or changing limits.

At 22:00 UTC the installed a4d3a0e resident had 21.17 MiB current reported RSS,
a rolling maximum of 21.17 MiB, and lifetime peak 39.86 MiB. The 30 MiB rolling
memory breach had expired. Normal-state CPU duty was 0.6203% against 0.5%.
At 22:06 UTC it recovered to 0.4676% with unchanged process and policy. These
windows distinguish a historical breach from current consumption.

The short process sample caught active compilation, Codex desktop rendering,
kernel and WindowServer work, plus live swap traffic. The compiler finished
before inspection; no abandoned/stuck compiler was established. Host pressure
and monitor self-health are independent. No app was stopped or signalled.

Three reads per native component measured mean CPU: cached sample 7.664 ms,
disk device 0.610 ms, disk processes 4.381 ms, browser roots 2.093 ms and browser
status 1.920 ms. These short measurements do not prove a sustained CPU defect.
The first status probe reconciled browser recovery metadata; the maintained
probe now uses private copies of control/policy/latest files to avoid live writes.

## Decision 1: fresh guard RSS on every Darwin heartbeat

The guard copied its own RSS from the host inventory, which is reused for up to
240 seconds. A transient RSS spike could therefore be counted repeatedly in the
64-sample rolling budget after actual memory fell. It could also hide growth
between inventories. Read only the guard's native proc_pid_rusage counter per
heartbeat, using ResidentSize and the existing MiB conversion. Preserve the
lifetime peak and rolling-window semantics. Never substitute a cached healthy
value after a self-counter error; report a sample error instead.

No CPU/memory limit, process-inventory cadence, browser control or application
lifecycle changes. Other platforms retain their existing inventory accounting.

## Verification and open items

- [x] Focused Darwin regression tests and bounded counter-cost check passed.
  Fresh self RSS averaged 0.002 ms CPU over three native reads; cached sampling
  averaged 7.960 ms. Counter failure, zero, NaN and infinity reject stale fallback.
  The rolling-window boundary assertion was subsequently strengthened; final
  candidate verification below must include that assertion.
- [x] Remote package verification on active runner-3 passed: job
  6f89dca4-9d57-4bdf-bff0-ffc030d7dc82, input digest
  5b7a615be957abe0b3faf8c958403cef84410ab1edca77313e65d59cd1ba05e0,
  package suite 73.825 seconds, exit 0. The subsequently strengthened boundary
  assertion passed against committed ca9e784 in job
  3f76df21-5bf2-48e0-9971-67966bccca40 (focused tests, exit 0).
- [x] Reviewed and guarded-published seven exact owned paths as
  ca9e784e8444dc5f03c583500dfab028e9d81ba8; publication
  e9f72c30fc355c76a480318afd75d8a2. Local main contains the change;
  this receipt does not claim a remote push.
- [x] Built native Darwin arm64/CGO release on runner-3, job
  7af6eee7-5060-402d-bc4f-973d52f968cd, exit 0. Installed at
  22:12:56 UTC, helper SHA
  5d0aa1404be60719bfbf0a39ebba29c4cf2c9fa2462f101d86ac5654318b340d.
  Prior release retained. Two preceding release attempts failed before a usable
  bundle: missing Git verifier context, then incorrectly tokenized GOFLAGS.
  Both failures are retained; release instructions now document the working
  clean-checkout/native-driver path and complete linker-token quoting.
- [x] Installed identity and real elapsed rolling window passed, exit 0:
  22:13:23.691966–22:29:36.092820 UTC (972.400854 seconds), 64 unique
  collected samples, 65 resident samples, 44 normal samples. Same PID 90171,
  revision and helper hash throughout. Rolling RSS maximum 25.203125 MiB
  against 30 MiB; idle CPU duty 0.4017808254% against 0.5%; sample CPU
  p95 21.308 ms; sample errors 0; guard_budget_ok true.

The five-second identity-catalog expiry is not changed: its measured contribution
has not established a worthwhile CPU repair. Whole-host CPU remains shared work;
no individual hot GUI process is classified as faulty without further evidence.

## Installed verification protocol

A single retained collector reads the resident's existing latest sample into
private SQLite; it launches no additional sampler and changes no policy.
Evidence: `~/.nicos-dev/session-pressure/evidence/work604/installed-window.sqlite`
and its final JSON summary. Identity is bound to PID 90171, revision ca9e784 and
the helper SHA above. It fails on a changed identity or stale/future samples.
Acceptance requires 960 seconds elapsed, at least 65 monitor samples, unchanged
limits, no sample errors and passing resource budgets. No second restart occurs
inside the window. First collected RSS fell while process inventory remained
cached, directly exercising the repaired installed path.

Before deployment at 22:09 UTC the old resident already passed its full CPU
window (0.4443% duty). The repair therefore addresses truthful RSS freshness;
it is not evidence that the repair reduced CPU. Shared host activity and swap
traffic remain separate from resident budget health.

Telemetry query found successful canonical `session pressure` events; aggregate
usage/failure projections returned no observations despite those rows. That
projection gap cannot establish zero failures or complete coverage. Native
identity-bound measurements are the primary acceptance evidence.

The `ndev` front door was aligned without another resident restart through the
committed native build workflow. Runner job
65793db5-58ae-4ec5-b674-d030d659188e passed, exit 0; factory revision
58e4bf55dcbc145e2b92cb31be7dabdf4028e2c5 pinned session-pressure ca9e784.
Installed artifact status confirms CLI, front door, candidate helper and resident
helper all stamp ca9e784, with mismatch false. Front-door binary SHA:
5aa4fcc26b818599e19220e7d1c8d4fa008f47fd22f403c9d78cc7fe29b7471b.

## Bounded host recheck

At 16:22:37–39 CST, the final two interval samples showed host CPU about
57% busy across ten logical CPUs, with kernel_task 105–114% of one core,
WindowServer 26–32%, Codex renderer 11–23%, and Claude renderer 9.5–32%.
These are overlapping shared application/kernel work, not per-chat attribution.
Chrome renderer was 10.9% in one interval. Claude desktop still had its original
05:05:26 CST start; the observed Chrome renderer still dated 03:56:48 CST.
No application signal, pause, restart or tab change was performed.

Two transient revision-backed CLI processes exited before identity inspection;
they are not established runaway workloads. Nicos Monitor and Nicos System
Backup were identifiable application processes (started 16:06 and 16:10 CST),
with only single-interval ~10% readings, insufficient to establish a sustained
fault. Work admission had no active leases or waiters at the selected recheck.
Allocated/compressed memory and active swap remain relevant host costs, but do
not identify abandoned work or authorize deleting storage. No additional
confirmed CPU defect was found in these bounded measurements.

## Closeout

Self-review: production code, counter-error behavior, exact expiry boundary,
focused native checks, remote suite, sealed release, source-stamp alignment and
installed rolling window agree. No additional confirmed CPU defect remains from
this investigation. The historical CPU-budget recovery preceded the fix; no CPU
reduction is attributed to the fresh-RSS change. Host load can still fluctuate
with concurrent work, swap and shared GUI rendering. No CPU limits were raised.

Self-score: 9/10. Durable source, native and installed-window proof support the
RSS repair; per-chat GUI attribution and future workload pressure remain outside
what this bounded investigation can establish. Improvements applied: pin the
exact rolling expiry boundary, isolate the maintained cost probe from live
browser metadata, and document the native build driver/linker quoting pitfalls.

Final telemetry dogfood returned 10 successful canonical command events. The
full-baseline diff retained all sources and added 19 session-pressure source
rows. This is bounded operational evidence, not a population-wide failure rate.
Private receipts/logs live under `/tmp/work604-*`; durable installed acceptance
SQLite and JSON live under
`~/.nicos-dev/session-pressure/evidence/work604/`. Product source is ca9e784;
this later proof/documentation commit does not require reinstalling binaries.
