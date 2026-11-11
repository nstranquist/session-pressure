# Kernel, display and swap profile, 2026-11-11

Status: 2026-11-11 investigation; confirmed paging cost, no runtime repair applied.
Owner: codex/01a1ca44-81fd-72b2-a89f-41b779deb34d. Work: WORK-619.
Constraint: keep Claude and Chrome running; preserve active work and desktop focus.

## Verified evidence

A system-wide Time Profiler capture ran at 16:33:09–16:33:22 CST (12.805 s).
This is a short running-thread sample, with profiler overhead, not a sustained
CPU benchmark. Kernel thread names are available; full kernel call stacks are not.
Of 16,504 sampled kernel milliseconds, 12,979 (78.64%) were in compressed-swap
trigger, compressor, page scanner and swap-out threads. NVMe work contributed
754 sampled milliseconds. Do not attribute kernel CPU solely to heat or graphics.

WindowServer had 4,684 sampled milliseconds. Inclusive display-update stacks
contained 2,969 ms; Metal layer compositing contained 1,385 ms. Inclusive counts
overlap and must not be added. These establish display work, not a faulty client
or redundant repaint loop. External main display: 5120×2134 backing pixels,
2560×1067 logical at 75 Hz. Built-in display: 3456×2234 backing pixels,
1728×1117 logical at 120 Hz. Neither display configuration was changed.
`pmset -g therm` reported no recorded thermal/performance warning; thermal state
remains unknown in the monitor. Direct WindowServer `sample` was privilege-denied;
a privilege-check attempt was blocked by automatic approval review. No escalation
or foreground capture was used; the system-wide trace succeeded without it.

The installed resident's 22:40:51Z sample reported host CPU 75.08% across ten
logical CPUs, swap-in 13.7 MB/s and swap-out 17.5 MB/s over 16.42 seconds, allocated
swap 10880.8 MiB. A separate 8.028-second VM-counter delta measured 15,879,272
bytes/s swap-in and 13,493,605 bytes/s swap-out; compression and decompression
processed approximately 1.49 and 1.40 billion logical page bytes/s respectively.
Those logical page rates are not compressed disk throughput. Paging pressure
therefore remained active even while allocated swap fell. No improvement claim.

## Workload attribution and limits

Claude's Cowork VM, started 05:42:55 CST, has a 4.3 GiB physical footprint in
`vmmap`/`sample`, with a configured 4 GiB guest. Its RSS alone hides this footprint.
Do not add `top` MEM and CMPRS counters: their accounting overlaps. File inventory
found 54,546 open-file records, including 52,547 beneath a dependency directory.
Cowork logs record 686 failed unmount cleanups today and successful child-process
exits, most recently 21:35:50Z. This supports a retained-resource hypothesis,
not proof of a memory leak or proof that every session is idle. No VM files,
mounts, caches, sessions or processes were removed or restarted. Permission was
requested for an idle-only Cowork VM restart while preserving the Claude app;
no dependent action occurs without a reply and a supported safe control path.

The Nicos System Backup process was enumerating directories in its current
full-home inventory. Its ledger advanced to 35,533 discovered stores, against
37,211 in the preceding run. The authored backup policy explicitly includes
caches and archives; broad enumeration is required coverage, not a confirmed
loop. No backup policy was weakened and no backup was cancelled.

## Decision and remaining verification

No independent WindowServer defect, thermal fault, backup loop or owned software
repair is established by this capture. The strongest actionable suspect is
Cowork resource retention, within a larger concurrent application working set.
Stopping protected work, editing unsupported vendor settings, flushing caches or
reducing display quality would not constitute a verified source repair.

Before any approved Cowork reset: verify task inactivity and supported control,
then capture matched paging/CPU windows afterward, prove Claude and Chrome remain
running, and distinguish a temporary reset benefit from recurrence prevention.
An inability to perform that safely must be reported, not treated as recovery.

Numeric profile summaries and paging deltas are retained in private SQLite:
`~/.nicos-dev/session-pressure/evidence/work619/profile.sqlite`.
Raw temporary trace: `/tmp/kernel-windowserver-20261111.trace`.
This report intentionally omits private command text, chat contents and file lists.
