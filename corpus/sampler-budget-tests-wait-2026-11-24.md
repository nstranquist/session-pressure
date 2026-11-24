# Sampler budget tests measured a wait, not work (WORK-1662, 2026-11-24)

Status: 2026-11-24, fixed in `sessionpressure/native_process_darwin_test.go`. Owner: claude/3bd639ce-6e3f-4246-9482-a95928c1b14e. Work: nicos-tools WORK-1662.

## Problem

`go test ./sessionpressure` failed once in 4 full runs on 2026-11-21, while three packages ran in parallel and the release builds wrote heavily to disk. The run printed only `FAIL`, so the failing test name was not kept.

## Reproduction attempts

| Run | Condition | Result |
|---|---|---|
| `go test -count=5 -json` | with `./sessionpressurecmd ./internal/helpermain` in parallel | 5/5 pass (252 s) |
| two concurrent test binaries, `-test.count=5 -test.shuffle=on` | double load, random order | 10/10 pass |
| `-test.count=3` | `session-pressure monitor install` every 10 s (the 2026-11-21 condition) | 3/3 pass |
| total | | 21 of 22 full-package runs pass, 0 reproductions |

The resident history (`snapshots-20261121.jsonl`) shows free memory at 78-82 % (level normal) from 21:40 to 21:56Z, so no memory-pressure path ran.

## Cause: two live budgets that measured a wait

The failure did not reproduce, so the cause comes from measurement. All five live-host budget assertions in the package were measured:

| Test | Measured | Budget | Margin |
|---|---|---|---|
| native inventory | 2-3 ms | 1 s | wide |
| native disk-write scan | 2-3 ms | 500 ms | wide |
| repeated fresh inventory RSS | 23.7-25.0 MB (isolated child) | resident budget | stable |
| 64-sample soak, `duration_p95` | 256-266 ms | 300 ms | 12 % |
| operator sample with agent trees, duration | 428-1082 ms | 1500 ms | 28 % at the max |

Both thin margins are waits, not sampler work:

- A sample needs a host CPU window of `hostCPUMinimumLiveWindow` (250 ms) since the last baseline. The resident samples seconds apart and never waits. The soak called `Sample` back to back, so each of its 64 samples polled for about 250 ms. Its `duration_p95` was that wait plus about 10 ms of work.
- An operator sample polls until the kernel's host CPU counters advance, capped at `hostCPUInitialMaxWait` (1250 ms). Twenty `session-pressure --json snapshot` calls measured the `host_cpu` phase at median 302 ms (quiet) and 252 ms (all cores busy), with steps to 501, 551, 752, 801 and 852 ms. The work around it was about 230 ms. Two more kernel steps put the total over 1500 ms, with no change in the sampler's work.

The operator sample test runs only when agent trees exist, which was true on 2026-11-21 with many agents running. It is the most likely failing test. That is an inference, not a captured failure.

A second host dependency in the soak: free memory at the warning level forces a fresh inventory and a power probe on every sample, which breaks its `freshInventories == 1` assertion. It did not happen on 2026-11-21, but the soak calls itself a healthy-cadence soak and read the real host.

## Change

- Soak: the sampler clock moves on by `hostCPUMinimumLiveWindow` between samples (`time.Now().Add(cadence)`). The 64 samples still span 16 s, so inventory age behaves as before, and the clock runs at wall speed inside a sample, so durations stay real. Free memory is pinned at a healthy 90 %.
- Operator sample: the budget applies to work (`SampleDurationMS` minus `SamplePhaseMS["host_cpu"]`, 1500 ms, 2000 ms under `-race`). The wait has its own cap, `hostCPUInitialMaxWait + hostCPUInitialPollInterval` (1300 ms). A missing `host_cpu` phase fails the test.
- Production code and budgets did not change.

## Result

| Test | Before | After (20 runs, all cores busy with 2x `yes`) |
|---|---|---|
| soak `duration_p95` | 256-266 ms, budget 300 ms | 0.13-0.22 ms |
| soak `cpu_p95` | 5-12 ms | 0.11-0.13 ms |
| operator sample | total 428-1082 ms, budget 1500 ms | work median 212 ms, max 296 ms; wait 251 ms, cap 1300 ms |

40 of 40 runs of the two tests passed with all cores busy. Before the change, the old soak binary also passed with all cores busy (265 ms), so CPU load alone does not trigger the failure. The kernel counter cadence does.

## Known limit

The soak budgets (300 ms duration, 50 ms CPU p95) are the resident's production budgets, and the soak checks the contract, not small regressions: real per-sample work is now 0.1-0.2 ms. A forced inventory on every sample, the expensive regression, still fails the `freshInventories == 1` check. A tighter test-only threshold was not added, because a number chosen by the agent would be a new gate without a production meaning.

## Follow-up hardening (WORK-SP-1, 2026-11-24)

Post-landing review caught four remaining wall-clock risks across `./sessionpressure`:

- `TestNativeSamplerBuildsAgentTreesWithinOperatorBudget`: widened `maxWaitMS` slack above `hostCPUInitialMaxWait` from 50 ms to 250 ms (500 ms under `-race`) so the allowed 1250 ms `process-inventory-fallback` timeout path does not flake on scheduler wake-up jitter.
- `TestNativeDiskWriteCountersAreBoundedAndIncludeSelf`: added 3x `-race` scaling (`500ms` to `1500ms`), matching `TestNativeProcessInventoryIsFastAndIncludesSelf`.
- `TestResidentSessionHintsDoNotBlockSample`: replaced the unguarded 50 ms wall-clock check on `os.TempDir()` with a deterministic blocked `sessionHintReader` seam proving the sample path returns immediately while an async refresh is stalled on I/O.
- `TestBrowserControlSteadyResidentPathTakesNoLock`: replaced the 500 ms wall-clock check with an already-canceled context passed under the held exclusive file lock, deterministically failing if the steady path attempts lock acquisition.

Verified with `go vet ./...`, 3x targeted `go test -race`, and full `go test -race -count=1 ./sessionpressure` (46.9 s, pass).

