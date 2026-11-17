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

## Open

- `phys_footprint` per process in the native inventory, so the RSS rungs stop
  double counting shared pages.
- Recalibrate the 48 GiB anchor after a week of M4 history (72 samples today).
