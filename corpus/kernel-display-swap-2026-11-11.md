# Kernel, display and swap profile, 2026-11-11

Status: 2026-11-11 investigation; idle Cowork VM stopped, paging persists;
VM-only restart unavailable in production controls. No source repair established.
Owner: codex/01a1ca44-81fd-72b2-a89f-41b779deb34d. Work: WORK-619.
Constraint: keep Claude and Chrome running; preserve active work and desktop focus.

## Verified evidence

The sections through "Decision and remaining verification" describe the initial
profile phase. The later intervention and its correction are recorded below.

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

## Authorized idle-VM experiment, 16:50–16:56 CST

Nico authorized a Cowork-only restart conditional on no active tasks, while
keeping Claude and Chrome running. Immediately before acting, all 196 logged
guest process groups had matching all-processes-exited events, the local schedule
contained zero tasks, and the latest guest command had exited at 15:35:50 CST.
The active Claude Code task was running through host executables. PID 58581 and
its 05:42:55 start time were checked again before signaling.

At 22:50:12.709672Z, SIGTERM was sent only to that VM process. Claude subsequently
reported guest disconnection with **zero active guest processes** to terminate.
The VM exited and no replacement appeared during subsequent checks. This was an
OS process stop, not a graceful vendor restart and not a completed VM restart.
The restart route should have been established before stopping the VM; the
earlier expectation of automatic recovery was not supported by the result.

Claude PID 41388 and Chrome PID 9492 retained their original start times. The
Code task continued responding. No application was quit, no data was deleted,
and no active Code process, tab, chat, display setting or backup was changed.

| Window (UTC, ending) | Length | Swap in / out, decimal MB/s | Kernel CPU, one core = 100% | WindowServer CPU |
|---|---:|---:|---:|---:|
| Before stop, 22:50:03.898 | 5.651 s | 14.31 / 21.11 | 167.3 / 129.8% | 89.7 / 92.0% |
| After stop, 22:52:45.541 | 12.420 s | 27.67 / 33.22 | 148.0 / 102.3% | 91.5 / 92.2% |
| Settled, 22:53:49.352 | 22.324 s | 26.33 / 26.16 | 180.6 / 170.7% | 89.1 / 90.7% |

The first cumulative `top` process sample was discarded in each window. These
are observational windows of different lengths with concurrent work, not a
controlled benchmark. The stop did **not** resolve paging or kernel load in the
measured windows. A later resident sample at 22:55:56Z reported 16.8 MB/s in,
3.6 MB/s out over 15.257 s and 83.06% host CPU across ten logical CPUs. Variable
paging alone is not evidence of sustained recovery or causation. Its host level
was normal, while the monitor's own rolling budget was not passing; those are
separate results. Current monitor RSS was 10.80 MiB; the budget warning was
normal-state self CPU duty 0.592% above its 0.500% limit, not memory. A subsequent
two-interval CPU check still measured kernel 161.9 / 205.7% and WindowServer
87.0 / 88.0% of one core. This also rules out declaring recovery from the single
lower swap-out sample.

After explicit permission for visible VM-only interaction, Claude 5.1231.2's
Help > Troubleshooting menu and Desktop app settings were inspected. They expose
destructive Cowork bundle/data reset-and-app-restart commands, but no production
VM-only start/restart command. Those reset commands were not used. Settings were
closed and the running Code task was restored. A renderer-internal restart IPC
exists in the installed bundle; it is not an exposed external control. No vendor
bundle patch, debugger injection or private-state rewrite was attempted.

## Recurrence decision

The repeated busy-unmount failures and retained open-file records justify a
Cowork cleanup investigation, but removing that VM did not remove the current
bottleneck. A periodic VM kill, broad folder deletion or app restart would not
be a verified recurrence fix. None was installed. Current evidence does not
establish a vendor memory leak, an independent WindowServer defect, or a source
change in an owned repository that would resolve this pressure.

The requested full VM restart remains incomplete: the production app has no
VM-only restart control available in the inspected surfaces, and whole-app reset
would violate the keep-running constraint. Recovery through a supported vendor
control is required before a clean-VM comparison can be completed. Future
authorized VM-reset experiments must establish both stop and start controls
before interruption, recheck guest activity, retain the same protected app
identities, and measure paging plus kernel CPU rather than allocated swap alone.
WORK-619 remains open; neither recurrence prevention nor host recovery is claimed.
