# Memory levels calibration (2026-11-17)

Owner: claude/305356a5 (WORK-1057). Status: 2026-11-17, implemented,
verified on runner-3, and installed on mac-48g and
mac-16g (`c4d3b9c`).

## Symptom

On 2026-11-17 05:39Z the 48 GiB mac-48g was `red` with the reason
`claude tree 994 RSS sum 16143 MB >= red 15360 MB`, at 90% free memory, no
swap and `memory_momentum=recovering`. The fleet plane on runner-3 raised
`host.memory` red for the M4 from this, and heavy work there was throttled.
The resident's self-RSS budget also failed there (30.6 MB > 30 MB).

## Cause

- `DefaultPolicy` scaled every MB rung linearly from the 16 GiB laptop
  (`tree_red_mb = 0.3125 * memory`). The M4 policy was written from that
  formula: tree 9216/15360/21504 MB.
- The tree and agent-total rungs judge summed RSS. It double counts shared
  pages, and the overcount grows with process count, not with memory. The M4
  Claude desktop tree had about 1000 processes.
- The kernel's own free level (`kern.memorystatus_level`), swap and swap-out
  rate all said the M4 had large headroom.

## Signals that exist

| Signal | Source | Host-size neutral |
|---|---|---|
| free % | `kern.memorystatus_level` | yes |
| swap used | `vm.swapusage` | no (MB) |
| swap-out rate | `HOST_VM_INFO64` swapouts | throughput only, caps at warning |
| tree / agent RSS sum | native process inventory | no, and double counts shared pages |
| physical memory | `hw.memsize` (Linux: `/proc/meminfo` MemTotal) | the key for anchors |
| compressor, `memory_pressure` level | not sampled | free % already reflects them |

## Monitor history (copied to muse-workspace `session-pressure-calibration/data`)

mac-16g, 16 GiB, 2026-11-03..17, 2811 samples:

- free % p1 30, p5 31, p50 48, min 25. No sample at or under 20%.
- agent RSS sum p50 2236 MB, p95 4413 MB, max 7593 MB.
- swap p50 3439 MB, p95 10845 MB, max 17077 MB.
- 205 red and 4 critical samples. Tree red 51 times.

mac-48g, 48 GiB, 2026-11-17 04:10..10:25Z, 72 samples:

- free % p5 68, p50 76, min 46. Swap 0 in every sample.
- claude tree RSS sum p50 13814 MB, p95 18294 MB, max 21746 MB (at 90% free).
- 62 warning and 8 red samples, every one from the tree rung.

Free memory on both hosts does not follow the RSS sum: the M4 had its largest
RSS sums at 90% or more free.

## Decision

- Memory levels live in `memory-levels.json`. JSON, not YAML: `policy.json`,
  `runtime.json` and every other state file here are JSON; the standard library
  decodes it strictly (`DisallowUnknownFields`, no trailing data) without a new
  dependency in the 30 MB resident; agents read it with `jq`. JSON has no
  comments, so every anchor has a required `evidence` field and every host
  override a required `reason`.
- The embedded `sessionpressure/memory_levels.json` is the default. A full
  replacement may sit at `<data dir>/memory-levels.json`.
- Anchors keyed on physical memory, with values interpolated between anchors
  (a formula keyed on memory size, calibrated by measured hosts). Outside the
  anchors MB values scale with memory; percent and guard values stay at the
  nearest anchor. `hosts.<short-hostname>` overrides single values.
- `rss_escalation_free_percent`: the tree and agent-total rungs reach red or
  critical only while kernel free % is at or under this value. Above it they
  hold at warning and the reason says `held at warning: host free N% > M%`.
  Free %, swap and swap-out keep their own levels, and swap still escalates a
  held warning.
- `policy.json` gets `memory_levels: calibrated|manual`. Empty is manual, so a
  policy keeps its own thresholds until the operator opts in (and tests that
  write their own thresholds stay deterministic). `policy init` writes
  `calibrated`. Switching to `calibrated` also writes the resolved values into
  `policy.json`, so an older binary that ignores the field reads the same ladder.

| Anchor | free % w/r/c | swap MB | tree MB | agent total MB | RSS escalation | guard RSS |
|---|---|---|---|---|---|---|
| 16gib-laptop | 25/15/8 | 4096/8192/12288 | 3072/5120/7168 | 8192/11264/13312 | 100 (always) | 30 MB |
| 48gib-workstation | 25/15/8 | 12288/24576/36864 | 20480/26624/32768 | 36864/45056/53248 | 50 | 44 MB |

- 16 GiB values are exactly the tuned laptop ladder, unchanged.
- 48 GiB tree: warning about 1.1x the M4 p95 tree, red about 1.45x, critical
  about 1.8x, the same spacing the laptop has over its own p95. Agent total
  warning sits above tree critical, as on the laptop. Swap keeps the old
  0.25/0.5/0.75 of memory. Free % is the kernel's own measure and stays.
- RSS escalation 50 on the M4: its lowest free level in history was 46%, at a
  normal 8.9 GB tree. At 50% free a 48 GiB host still has about 24 GiB free.
- Guard RSS 44 MB on the M4: its self RSS over 76 samples (to 10:47Z, about
  1000 processes in the inventory) was p50 34.0, p95 37.1, p99 40.0, max 40.0
  MB; 44 is about 1.1x p99. A first value of 36 MB (from the single 30.6 MB
  reading in the brief) was under the M4's own p95 and was corrected before
  install. The laptop stays at 30 MB (its p99 is 30.5 MB, p95 24.8 MB).

Rejected:

- Editing only the M4 `policy.json`: it fixes one host and leaves the formula.
- Dropping the RSS rungs: the laptop needs them; on 2026-11-15 a runaway tree
  was the first signal there.
- Per-process `phys_footprint` in place of RSS: the right long-term metric for
  shared pages, but it is a sampler change with its own CPU and RSS budget.
  Recorded as follow-up.

## Proof

Tests in `sessionpressure/memory_levels_test.go`:

- The embedded file parses. 16 GiB `DefaultPolicy` equals the old laptop
  ladder field for field, with guard budget 30 MB.
- 48 GiB uses the measured anchor. 32 GiB interpolates (tree red 15872 MB,
  escalation 75%, guard 37 MB). 8 GiB and 96 GiB scale MB values and keep
  percent values.
- Host override applies to the normalized hostname only
  (`Example-MacBook-Pro.local` to `example-macbook-pro`).
- Thirteen bad configs fail with the field in the error (unknown field, order,
  anchor order, escalation and guard ranges, schema, trailing data, host key,
  host reason, host band, empty anchors, missing evidence).
- `LoadPolicy`: calibrated mode resolves the native memory size, an explicit
  caller memory size wins, manual and empty modes keep `policy.json`, a
  data-dir file and host override are used, a bad data-dir file fails with the
  file path and field.
- M4 evidence sample (tree 16143 MB, 90% free, no swap) is `normal`; the M4
  maximum (21746 MB) is `warning`; a 40 GB tree is held at warning at 90% free,
  critical at 45% free; 12% free is red; 30 GB swap still escalates to red.
- Laptop: tree 7008 MB at 68% free stays red, 7200 MB at 90% free stays
  critical, and the M4 evidence load is critical on the laptop.

runner-3: see "Release and install".

## Release and install (2026-11-17)

- Commits (guarded single commits, local, not pushed): `f96fd9f` (memory
  levels) and `c4d3b9c` (48 GiB guard budget 36 to 44 MB, measured on the M4
  just before install).
- runner-3 job `6e0ba7aa` (candidate tree of `f96fd9f`): `go vet ./...`,
  `go test ./...` and a darwin/arm64 `go vet` of `sessionpressure` and
  `sessionpressurecmd` passed, exit 0. Darwin-only `sessionpressure` tests
  passed locally.
- runner-3 job `2db96138`: sealed bundle of `refs/heads/main` at
  `c4d3b9c` verified and cloned clean, `go test` of `./sessionpressure/`,
  `./sessionpressurecmd/` and `./internal/helpermain/` passed, six darwin/arm64
  binaries built with CGO (`pressure-darwin-clang`). Export
  `/srv/build/exports/memory-levels-c4d3b9c/out`, helper sha256 in its
  `release.json`. (Job `907f3b44` built `f96fd9f`; superseded, not installed.)
- mac-48g: `session-pressure-release install --expected-current
  7a9fc8c…` promoted `c4d3b9c`; `session-pressure monitor install` restarted
  the resident (`source c4d3b9c21c7c`); `policy memory-levels calibrated`.
  Backup `policy.json.bak-20261117-before-memory-levels`.
- mac-16g: same, with `--expected-current 7020ea2…`. Its thresholds
  and guard budget are byte-identical before and after (`jq -S .thresholds`
  diff is empty; `max_self_rss_mb` 30). Backup as on the M4.

Installed checks:

| Host | Before | After |
|---|---|---|
| mac-48g | 10:47:14Z red, `claude tree 994 RSS sum 18289 MB >= red 15360 MB`, free 76%, swap 0 | 10:50:55Z normal, agent RSS 20051 MB, free 77%, swap 0 |
| mac-16g | 10:29:02Z red, `claude tree 749 RSS sum 7008 MB >= red 5120 MB`, free 68% | 10:51:38Z red, `claude tree 749 RSS sum 6703 MB >= red 5120 MB`, free 65% |

- The fleet plane (`nicos-delivery-plane state --endpoint
  http://runner-3:7790`) cleared `host.memory:mac:mac-48g` at
  10:51:26Z, and no `host.*` finding for either Mac is active.
- The M4 resident's first sample after restart failed only its CPU budget
  (`current sample CPU 52.2 ms > budget 50.0 ms`); the self-RSS check passed
  (23.7 MB of 44 MB). The next sample (10:52:26Z) was normal with
  `monitor_healthy: true` and no budget reason (self RSS 25.5 MB of 44 MB).

## mac-48g limit raise (2026-11-17 13:22Z, another session)

From 12:24Z mac-48g judged red on `claude tree RSS sum 27885 MB >= red 26624
MB` at 50% free and 0 swap: the 48gib anchor held the RSS rungs at warning
only while free was above 50%. That held every nicos-tools gate (0 landed in
30 minutes, backlog 52). Nico: "let's absolutely raise the limits". The
landing session wrote a data-dir `memory-levels.json` on mac-48g with a host
override: tree RSS 28672/36864/45056 MB and `rss_escalation_free_percent` 25.
The footprint calibration below folds that override into the 48gib anchor and
retires the data-dir file.

## Footprint calibration (WORK-1105)

### Change

- The native inventory keeps `phys_footprint` from the `proc_pid_rusage` V2
  call it already makes per process: no extra syscall. Trees, the agent
  total, latest.json and heartbeats carry footprint next to the RSS sums
  (`a64443a`, was `ff27be4` before the history rewrite).
- Anchors may set `tree_footprint_mb` and `agent_total_footprint_mb` (every
  anchor or none, with a required `footprint_evidence`). Where a sample has
  footprint readings, those bands judge the tree and agent-total rungs; RSS
  bands are the fallback. A process without a reading counts with its RSS.

### Data

One sample a minute from each resident's own latest.json (no extra sampling),
2026-11-17 12:25Z to 13:47Z: 83 samples on mac-16g, 82 on mac-48g. The window
was planned for 4 hours and was cut at 80 minutes on request; it includes
mac-48g falling to 40% free, the case that matters. Raw files and the Go
analysis and replay programs are in muse-workspace
`session-pressure-calibration/footprint/`.

| | mac-16g | mac-48g |
|---|---|---|
| free % | 46..57 | 40..70 |
| swap MB | 1135..1199 | 0 |
| tree RSS MB p50 / max | 5560 / 6465 | 23003 / 28524 |
| tree footprint MB p50 / max | 10060 / 11962 | 23372 / 26954 |
| tree footprint / RSS p50 | 1.8 | 1.1 |
| free vs agent RSS, r | +0.63 | +0.42 |
| free vs agent footprint, r | -0.36 | -0.78 |

On both hosts free memory rises with the RSS sum: under pressure the kernel
compresses and swaps pages out of RSS, so RSS reads as the opposite of load.
Footprint counts those pages and tracks free memory the right way. On mac-16g
footprint is about twice RSS for that reason.

### Bands

- mac-48g: the fit of free % on footprint (r -0.78) puts free at 35/25/15% at
  tree footprint 30708/34086/37464 MB and agent footprint 32122/34927/37733
  MB. Bands sit there, rounded down to 1 GiB, one step ahead of the free
  rungs: tree 30720/33792/36864, agent total 31744/34816/37888. The RSS
  fallback takes the 13:22Z override (tree 28672/36864/45056, hold at 25%).
- mac-16g: the fit is too weak to extrapolate (r -0.36 over 46..57% free), so
  the tuned RSS ladder is carried over. Red sits at the footprint quantile
  where RSS red sat (q0.17, 9826 MB, rounded down to 9728); warning and
  critical are the RSS bands times the median ratio 1.8. Tree 5632/9728/12800,
  agent total 15360/21504/25600.

Replay of the collected samples through `EvaluateMemoryPressure`:

| | footprint bands | RSS ladder alone |
|---|---|---|
| mac-16g | red 71, warning 12 | red 68, warning 15 |
| mac-48g | normal 82 | normal 82 |

mac-16g keeps its protection, now on a measure that follows free memory. It
is red most of the window, as it was before; that is its tuned choice, not a
change here.

### Resident cost

The footprint is read from a struct the inventory already fetched, so CPU
does not change; memory grows by 16 bytes a process.

| | sample CPU p95 before / after | guard RSS max in window |
|---|---|---|
| mac-16g | 26.0 / 26.0 ms (budget 150) | 30.3 MB, p95 28.1 (budget 30) |
| mac-48g | 31.7 / 29.7 ms (budget 50) | 36.3 MB (budget 44) |

mac-16g guard RSS already ran 29.1..29.5 MB before this change; its
rolling-p95 budget check passes. It has the least headroom of the two.

### Install (2026-11-17 13:55Z)

c186238 passed `go vet ./...`, `go test ./...`, darwin vet and the release
build on runner-3 (job c1b05ad8), and the darwin packages locally. Installed
with `session-pressure-release install --expected-current` on mac-16g (from
ff27be4) and mac-48g (from 6744a73), then `monitor install` and
`policy memory-levels calibrated`. On mac-48g the data-dir
`memory-levels.json` (the 13:22Z override) moved to
`memory-levels.json.folded-into-c186238-20261117T1400Z`, so the embedded
anchors apply. `policy memory-levels show` on both: source embedded,
footprint bands on.

First samples after install:

| Host | Level | Cause | Free | Agent footprint | Guard RSS |
|---|---|---|---|---|---|
| mac-16g | red | tree footprint 11298 MB >= red 9728 MB | 52% | 11565 MB | 18.5 MB |
| mac-48g | red | host CPU 99.6% (memory rungs normal) | 44% | 28960 MB | 24.1 MB |

## Open

- Recheck both footprint ladders after a week of history. The heartbeats now
  carry `agent_footprint_mb` and `top_tree_footprint_mb`, so the analysis can
  run on the residents' own snapshots.
- mac-16g guard RSS margin is under 2 MB.
