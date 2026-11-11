# Resident memory-budget accounting

Owner: codex, session 01a1ca44-81fd-72b2-a89f-41b779deb34d.
Status: implementation and verification in progress, 2026-11-11. Work: WORK-604.

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
- [ ] Remote package verification on active runner-3 with candidate identity.
- [ ] Review and commit exact owned paths through guarded delivery.
- [ ] Build/install committed native release through canonical remote workflow.
- [ ] Verify installed identity and real elapsed rolling-window budget.

The five-second identity-catalog expiry is not changed: its measured contribution
has not established a worthwhile CPU repair. Whole-host CPU remains shared work;
no individual hot GUI process is classified as faulty without further evidence.
