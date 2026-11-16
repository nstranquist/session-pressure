# Pressure review recovery, 2026-11-16

Status: source fixes locally published. WORK-904 release bundle prepared and privately verified. Live promotion waits for native contract verification and a controlled resident transition.
Owner: codex/01a1e4b2-9ce5-79d0-aeec-e700790a3e64, WORK-899.
Original owner: Gemini Antigravity conversation 8ab4cd6c-4c00-427f-a585-ee279cf28544.

The Gemini review stopped before commit, ranked recommendations and board publication. Its source edits were still isolated in the shared checkout. The recovery preserves the live Claude storage work.

## Ranked findings

| Rank | Finding and action | Confidence | Verification |
| --- | --- | --- | --- |
| 1 | Share resident helper implementation across both build entrypoints. Each old main omitted callbacks present in the other. | 9/10 | Compared new helpermain with both committed mains. Remote builds and focused tests pass. Callback and stall notification tests now run on Linux as well as Darwin. |
| 2 | Include total admission wait when doctor calculates swap derate share. | 9/10 | Regression fixture has 60 seconds swap wait of 240 total seconds. Focused remote test passes. Installed doctor still reports total wait as zero. |
| 3 | Give a live diagnostic command when a verified running monitor fails a resource budget, instead of reinstall advice. | 9/10 | Focused doctor regression passes. Source preserves install/restart advice for missing, stale or mismatched monitors. |
| 4 | Resolve one agent catalog snapshot per sample for trees, host consumers and memory families. | 8/10 | Reviewed shared snapshot flow and remote focused tests. Runtime latency improvement is not measured. |
| 5 | Correct policy profile label interpolation in SwiftUI. | 9/10 | One-line source correction reviewed. Native tests were not rerun during recovery. |
| 6 | Increase the Rust test fixture timeout from two to five seconds. | 6/10 | Recovered edit retained. No timing trend or fresh Rust test result establishes root-cause removal. |
| 7 | Remove remaining factory coupling and mutable local module replacement. | 7/10 | Existing WORK-895 already covers two-home consolidation and replace directives. Recommendation needs integration there, not another extraction effort. |

Scores express evidence strength. They are not acceptance thresholds.

## Validation and limits

The isolated candidate contains the recovered Gemini paths only, without concurrent storage edits. Canonical remote verification on runner-3 passed go vet ./..., focused sessionpressure and helper package tests, and both helper entrypoint builds. Job: 9a3be38e-ab12-44c0-bd8c-49584ee41328, exit 0, peak memory 618635264 bytes. Full Go suite, Darwin tests, Swift tests and Rust tests are not verified in this recovery. Gemini historical test claims remain in its source transcript.

Installed doctor was dogfooded during recovery. Its binaries disagree: CLI 7020ea2f3c26, front door cd9c32dc646f, resident helper 2084bf932eb2. It reports 348 minutes/day swap derate wait with all_wait_total_ms 0, and a resource budget failure. Source fixes do not constitute an installed release. No resident restart, policy tuning, cleanup or process termination occurred.

Board decision d-06gu9dlo33kmglc780 records recovery and ownership. The original transcript is ~/.gemini/antigravity/brain/8ab4cd6c-4c00-427f-a585-ee279cf28544/.system_generated/logs/transcript_full.jsonl. No separate plan document was found for this chat.

## WORK-904 release preparation

Owner: codex/01a1e4b2-9ce5-79d0-aeec-e700790a3e64. Status: 2026-11-16, prepared, live promotion pending.

Source fixes were locally published as e039c1eaea92e020207122534c0f46232179cf2c, guarded receipt 37e18062f03cf163ffd7b7a5b0b65956. A sealed bundle of that committed main ref passed local verification. Canonical runner-3 request 91e55bd9-78ca-4b31-b046-d3b6b4d93721 cloned the named ref, verified it in the clean repository, passed release and compiler-adapter tests, then built all six Darwin/arm64 binaries with CGO enabled through /srv/build/opt/pressure-darwin-clang. Exit 0, peak memory 439963648 bytes. The first request b7a1ea18-b410-47b2-9e41-82f944259322 failed before compilation because bundle verification ran outside a Git repository. Corrected verification remained on runner 3.

The completed bundle is retained on runner-3 at /srv/build/exports/pressure-work904-e039c1e/bundle and on this Mac at /Users/user/Documents/Codex/2026-11-16/lets-l/work/pressure-release-bundle. Built at 2026-11-16T14:25:21.804144856Z. Its guarded installer verified and installed the complete bundle into the private prefix /Users/user/Documents/Codex/2026-11-16/lets-l/work/pressure-staged-bin, with expected-current none. Capabilities reports e039c1e. This does not change the live product authority.

A real read-only doctor probe through the private CLI reads existing resident telemetry and now reports all_wait_total_ms 70426360, swap wait 20899518, share 0.296757. The old installed CLI reported all_wait_total_ms 0 for that same swap wait. This verifies the attribution fix against real data. The probe correctly retains a revision mismatch against resident helper 2084bf932eb2. Healthy runtime acceptance is not claimed.

Live .local/bin authority remains 7020ea2f3c266d9732d9e012f91ec0a428361315, captured before the build and read back after the private install. Native InstalledContractTests remain paused. The factory promote-session-pressure target invokes monitor install, which restarts the resident. It was not run. A coherent live promotion also requires the ndev front-door source revision to agree. No live executable, policy, resident or GUI was changed.

Next owner action: after Nico lifts the applicable test pause and authorizes the controlled resident transition, run the native installed-contract check using the staged CLI, verify current authority and dependency provenance, rebuild the front door against the same module revision, then use the guarded release promotion with the actual observed authority. Retain all active runs and review transition effects first. Collect installed doctor/artifact status and the defined observation evidence. WORK-904 remains open for that acceptance, not marked complete by artifact preparation.
