# Memory levels calibration (2026-11-17)

Owner: claude/305356a5 (WORK-1057). Status: 2026-11-17, implemented and
verified on runner-3. Install state is in "Release and install" below.

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
| 48gib-workstation | 25/15/8 | 12288/24576/36864 | 20480/26624/32768 | 36864/45056/53248 | 50 | 36 MB |

- 16 GiB values are exactly the tuned laptop ladder, unchanged.
- 48 GiB tree: warning about 1.1x the M4 p95 tree, red about 1.45x, critical
  about 1.8x, the same spacing the laptop has over its own p95. Agent total
  warning sits above tree critical, as on the laptop. Swap keeps the old
  0.25/0.5/0.75 of memory. Free % is the kernel's own measure and stays.
- RSS escalation 50 on the M4: its lowest free level in history was 46%, at a
  normal 8.9 GB tree. At 50% free a 48 GiB host still has about 24 GiB free.
- Guard RSS 36 MB on the M4: 30.6 MB was measured with 994 processes in the
  inventory; 36 gives about 20% headroom. The laptop stays at 30 MB.

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
  escalation 75%, guard 33 MB). 8 GiB and 96 GiB scale MB values and keep
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

## Release and install

Pending at the time of writing; updated below when done.

## Open

- `phys_footprint` per process in the native inventory, so the RSS rungs stop
  double counting shared pages.
- Recalibrate the 48 GiB anchor after a week of M4 history (72 samples today).
