# Runaway process controls — WORK-82

Nico asked Session Pressure to detect scans like the orphaned Python scan, show evidence, stop a selected process, and retain the incident. The new Process Incidents page and `processes` command cover that path.

## Contract

- The existing resident inventory supplies CPU, memory, age, ancestry and session evidence. No recursive file scan or argv collection is added.
- Orphaned work requires 10 minutes of age and two minutes at 60% of one CPU core. Other work requires 30 minutes of age and five hot minutes. The configured inventory cadence determines when evidence is available. Gaps beyond two inventory intervals plus one sample interval reset evidence, with a three-minute minimum tolerance.
- Detection is advisory. Notifications open details. They cannot invoke Stop.
- Stop persists intent, validates owner and kernel birth twice, signals exactly one PID, and distinguishes signal sent from confirmed exit. Force Stop requires a separate action after Stop. It does not terminate an entire agent tree.
- `processes run --timeout D -- COMMAND` creates and supervises a private process group, limits the deadline to one hour, and records deadline/cancellation teardown. Ordinary command exit codes are preserved. It does not guard commands launched elsewhere or descendants that deliberately escape that group.
- A private atomic ledger retains at most 200 incidents and 20 actions per incident. Reads and CLI receipt imports are bounded. Unchanged idle observations do not acquire a disk-writing lock. Detection can resend an alert after a crash between send and acknowledgement.
- The approved stop of Python PID 8523 is imported from `orphan-scan.receipt.json`. It is an operator-reported historical record, not retrospective kernel proof. Historical records cannot authorize a stop.

## Verification

Go tests, race checks, vet and Darwin compilation run on gaterunner-1. Swift tests and the signed native build run on the Mac through work admission. Exact final commands, build identity and installed dogfood results are recorded in evidence.json at closeout.

The tests cover sustained thresholds, normal and low-power inventory cadence, stale/repeated/unknown CPU evidence, PID reuse, owner mismatch, notification persistence and retry, exact-PID signalling, durable intent failure, force escalation, corrupt storage, concurrent mutation, file permissions, real owned child teardown, deadline/cancellation, fast nonzero exit status and strict CLI validation. Swift decodes a real CLI recording and verifies notification navigation does not invoke a stop.

## Self-review and process corrections

1. A fixed three-minute gap conflicted with the normal three-minute inventory interval and the four-minute low-power interval. Sampling jitter could prevent every alert. The guard now derives tolerance from the policy; regression tests cover both cadences.
2. The first idle benchmark took 15.44 ms and 923 KB per 4,000-process inventory. The file lock fsynced metadata even when the ledger did not change. A read-only idle path and index-valued PID map reduced this to about 2.55 ms and 148 KB in the first rerun. Final benchmark evidence is retained separately.
3. An existing profile persistence test sent TERM after a child marker, before the parent necessarily forwarded a completed JSON timing event. It failed twice. The test now synchronizes on the completed event instead of assuming output timing.
4. One new test initially missed its io import. The remote compiler caught it; the import was fixed before publication. No failed check is treated as passing evidence.
5. The first candidate transfer included ignored build output. That owned transfer was stopped. Later candidates enumerate Git-owned and unignored source files and record content hashes; they are about 4 MB.
6. The installed standalone WIP initializer unexpectedly created an empty branch despite the main-only requirement. The lane was aborted and that unchanged branch ref was removed. Publication uses the canonical guarded serial helper with an exact path claim, immutable main/HEAD expectations, isolated index, preflight and compare-and-swap. The installed skill copy lacks its adjacent shared helpers, so the canonical repository helper is used. A delivery dry-run was also verified to be preview-only; it was not counted as accepted or landed.

7. A second existing fixture discarded the closure that owns its lock file and waited forever with `select {}`. The full suite observed the lock released while the child should still be alive. The fixture now retains the unlock closure and has a one-minute deadline, avoiding finalizer release and runtime deadlock exit.

Human guide: [Process Incidents](../../human/process-incidents.html).

8. Live review found three unmanaged headless screenshot jobs, running for one to three hours with GPU helpers consuming more than three cores. Nico approved stopping them, but all three had exited before the final identity check. No signal was sent by this session. Historical receipts now distinguish an observed exit (`NONE`, `operator_observed_exit`) from a reported TERM/KILL action. Reused or absent PIDs do not receive a signal.

## Installed result

The public CLI, resident helper and native app now use committed source `d4a08a745e0a0bd688ad5b06bfc947218cd7c937` (0.7.0). All 59 Swift tests passed from an immutable committed export. The signed installed executable matches that verified export. The installed CLI stopped an owned child in 0.16 seconds; a guarded sleep hit its deadline in 0.374 seconds and returned 124. The native page shows all three Chrome exits with no signal and disables Stop for historical records. Exact evidence is in [evidence.json](evidence.json).

The full shared native worktree had one navigation assertion failure from in-flight Hetzner sidebar changes; those edits were preserved and excluded from installation. Two earlier native builds reached their admission deadline while the host was under real CPU pressure. No gate was bypassed. A remote invocation with an incorrect manually transcribed revision failed before tests or compilation; the corrected run read the exact revision from the captured source file and passed.

The CLI telemetry query returned no rows, so no production usage or reliability trend is claimed. Isolated notification tests pass; a high-CPU production incident was not manufactured. Normal launchd-parented apps can meet the sustained CPU thresholds too. Alerts require operator judgement. The ndev front door still selects an older pressure binary; its resolver fix is owned by the parallel factory session. The native app resolves the public installed pressure binary correctly.
