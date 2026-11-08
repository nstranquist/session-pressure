# Runaway process controls — WORK-82

Nico asked Session Pressure to detect scans like the orphaned Python scan, show evidence, stop a selected process, and retain the incident. The new Process Incidents page and `processes` command cover that path.

## Contract

- The existing resident inventory supplies CPU, memory, age, ancestry and session evidence. No recursive file scan or argv collection is added.
- Known commands directly parented by PID 1, or work below an init-owned shell, require 10 minutes of age and two minutes at 60% of one CPU core. PID 1 is normal for GUI apps and services and does not prove an orphan. Other work requires 30 minutes of age and five hot minutes. The configured inventory cadence determines when evidence is available. Gaps beyond two inventory intervals plus one sample interval reset evidence, with a three-minute minimum tolerance.
- Detection is advisory. Notifications open details. They cannot invoke Stop.
- Stop persists intent, validates owner and kernel birth twice, signals exactly one PID, and distinguishes signal sent from confirmed exit. Force Stop requires a separate action after Stop. It does not terminate an entire agent tree.
- `processes run --timeout D -- COMMAND` creates and supervises a private process group, limits the deadline to one hour, and records deadline/cancellation teardown. Ordinary command exit codes are preserved. Remaining group members are removed on ordinary exit too; a sleeping group anchor handles supervisor death without reusing a released group ID. It does not guard commands launched elsewhere or descendants that deliberately escape that group.
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

## Initial installed result (17:55 UTC)

At this checkpoint, the public CLI, resident helper and native app used committed source `d4a08a745e0a0bd688ad5b06bfc947218cd7c937` (0.7.0). All 59 Swift tests passed from an immutable committed export. The signed installed executable matches that verified export. The installed CLI stopped an owned child in 0.16 seconds; a guarded sleep hit its deadline in 0.374 seconds and returned 124. The native page shows all three Chrome exits with no signal and disables Stop for historical records. Exact evidence is in [evidence.json](evidence.json).

The full shared native worktree had one navigation assertion failure from in-flight Hetzner sidebar changes; those edits were preserved and excluded from installation. Two earlier native builds reached their admission deadline while the host was under real CPU pressure. No gate was bypassed. A remote invocation with an incorrect manually transcribed revision failed before tests or compilation; the corrected run read the exact revision from the captured source file and passed.

The CLI telemetry query returned no rows, so no production usage or reliability trend is claimed. Isolated notification tests pass; a high-CPU production incident was not manufactured. Normal launchd-parented apps can meet the sustained CPU thresholds too. Alerts require operator judgement. The ndev front door still selects an older pressure binary; its resolver fix is owned by the parallel factory session. The native app resolves the public installed pressure binary correctly.


## WORK-87: page review and CPU guard hardening

The native controls were exercised through the real signed app and public CLI contract, with a separate bundle identity, private ledger, and two owned sleeping children. Cancel left the child alive. A stale birth identity produced a durable `NONE / revalidation_rejected` result. Stop confirmed TERM exit. A TERM-ignoring child stayed `stop_requested`; the separate Force Stop confirmation sent KILL and confirmed exit. Refresh retained selection and action history. No CPU load or foreign signal was needed.

The review found and corrected these faults:

- Treating every PID-1 child as orphaned gave normal GUI apps the shorter alert window. Known commands and init-owned shells now select that window; legacy labels no longer claim proof of an orphan.
- A child that exited after Stop could stay pending when no hot inventory rows remained. Later identity evidence now closes that record; transient lookup failures leave it pending.
- Missing identity and historical records could expose an unusable Stop control. The view now checks those action boundaries and labels `NONE` as “No signal.” Evidence and action times include the date.
- The deadline wrapper previously left background descendants behind when its main command exited early. A dedicated sleeping shell now pins the private group ID until cleanup, and closes the group if the supervisor dies. Tests cover early nonzero exit, supervisor death, deadline, cancellation, and fast ordinary exit.
- Darwin's single-process sysctl helper returns EIO for an empty kernel result after exit. A PID-scoped slice lookup preserves an empty result as a typed exit while retaining real lookup errors. The first Mac regression run exposed this; Linux checks alone did not prove Mac cleanup.

### Repeat the native control review

`sessionpressure/process_guard_ui_review_darwin_test.go` contains an opt-in harness. Build the Darwin package test executable on the gate runner, then run only `TestProcessIncidentNativeReview` on the Mac with `PRESSURE_NATIVE_REVIEW_DIR` set to a new absolute private directory, `PRESSURE_PROCESS_CONTRACT_BIN` set to the candidate CLI, and `PRESSURE_NATIVE_REVIEW_APP` set to the signed app bundle. The harness copies and signs the app under a separate bundle identity and removes its notification URL registration. It writes `ready.json`; use the native UI to test Cancel, stale identity, TERM, then Force Stop. Write the `complete` marker only after observing the results. The harness validates the private ledger and child exits and writes `result.json`. Its eight-minute deadline removes its own sleeping children even if the review is interrupted. No production history is changed.

### Guard CPU use next time

Choose a named project root and a deadline before a scan. Use `processes run --timeout` for a new command and the work admission gate for expensive work. Heavy Go checks run on gaterunner-1. The bounded runner removes descendants in its private group on command completion, timeout, cancellation, and supervisor death. It cannot contain a descendant that deliberately changes session/group. The resident records sustained CPU evidence for other work and alerts the operator; it does not kill legitimate work automatically. Browser work was paused at the final recorded checkpoint and no browser probe was launched for this review.

### Publication review

The product lane preview reported repository-policy authorization, but the actual capture returned `POLICY_NOT_FOUND`. No preview ID is counted as a delivery. Publication used the WIP skill's canonical guarded serial helper with exact owned staged paths and `--isolate-owned-staged`; foreign staged remote-work changes and the separate Hetzner help hunk remained intact. The initial release transfer failed because a raw SHA is not a named bundle ref, and an archive script used a Python API newer than the host. Both failed before release build. The corrected transfer uses a named HEAD bundle, checks out the exact committed revision remotely, and extracts a trusted Git archive with path validation.

### Review boundary

The native UI fixture passed in 92.17 seconds. The isolated signed Swift build passed 61 tests in eight suites. Linux package tests, race checks, and vet passed. Final Darwin regression and installation evidence are recorded in `evidence.json`; this document does not substitute an accepted delivery or source test for an installed result. CLI telemetry returned no failure clusters, but no usage rows; a production reliability trend remains unmeasured. Notification retry and threshold behavior use fixtures rather than a manufactured production CPU incident.

### Installed follow-up and competing app sources

The reviewed product app (source 3d1e4edf, 61 tests) was installed and verified at 18:46 UTC. A later factory-app install replaced it: both trees use `/Applications/NDev Pressure.app`, but the factory sidebar had Day Gate and Hetzner without Process Incidents. The review therefore added the same incident models, view, client methods, notification navigation and action-boundary tests to `apps/desktop/ndev-pressure`. Delivery `efdd4a8d-7be6-4375-a172-7905119de740` captures only the owned changes. Foreign Hetzner edits are preserved. Future installation reviews must check the installed sidebar and executable identity after other app installers have run; an earlier installed hash alone does not establish current behavior.

The optional factory-checkout lookup also caused a coverage read error for the extracted product. Commit `55021632ed0a95d3906a2197e8411285f625aac4` treats only a typed missing factory checkout as optional; permission and other errors remain errors. Full package tests and vet passed on Hetzner, and the official release was installed. The board no longer reports that coverage error. Concurrent product work later promoted cc23fb2, which includes these changes. The executing ndev front door still reported 8273bb1 at the 19:00 checkpoint; this is a real revision warning, not a failed process control.

At 18:57 the resident was healthy and daily-driver ready: 47 samples, 27 normal, 15.66 MiB RSS and 12.90 ms sample CPU p95. After the concurrent cc23fb2 installation, the fresh resident was healthy but had only one of four required normal baseline samples; daily-driver readiness was temporarily unproven. Evidence records both checkpoints instead of carrying the older green result forward.

The broader WORK-50 browser review remains open. The standalone installed engine passed five checks, but this does not prove the integrated ndev installation. The retained seven-day browser report had a 7.6% non-OK rate against a 6% gate, and screenshot/eval failures still require review. Source delivery, installed engine checks, integrated browser behavior and telemetry are separate evidence.


### Final installed page and live health checkpoint

The combined factory snapshot passed 52 Swift tests in seven suites. Its signed release passed verification and was installed through the atomic app installer. Installed SHA-256 `37ad4a5c8b10e9b78d0240bd5c018c50b98ac285b77ba93950934558cf08cd08` matches the tested executable. The provenance manifest records each source file, including the preserved concurrent Hetzner changes; this is a combined snapshot, not a claim that all of that source has landed. The installed sidebar contains Process Incidents, Day Gate and Hetzner.

The final installed executable was then copied to a private app identity and exercised against the installed CLI with two owned sleeping children. `TestProcessIncidentNativeReview` passed in 227.21 seconds. Cancel left the child active and did not add an action. A stale kernel identity recorded NONE / revalidation rejected. TERM confirmed exit. A TERM-resistant child remained pending until the separate Force Stop confirmation recorded KILL / exit confirmed. Refresh retained selection and history. The review app quit and the bounded harness cleaned its children. No foreign process received a signal.

At 19:07 UTC the resident was fresh but **unhealthy**: normal-state CPU duty 1.751% exceeded its 0.5% policy budget. RSS 14.28 MiB, sample CPU p95 13.037 ms and projected telemetry 528,384 bytes/day passed their limits. The earlier green checkpoint does not close this failure. Installed cc23fb2 still used the old paused polling loop. Concurrent source commit `3e5da8f` reduces paused idle sampling and keeps a lightweight five-second demand check; its complete remote race suite and native build passed. Promotion is separate and respects the concurrent release owner's live remote-proof hold. No CPU budget was raised and no browser pause was lifted.
