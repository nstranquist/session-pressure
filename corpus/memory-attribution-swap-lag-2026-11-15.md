# Memory attribution and the swap lag release

Owner: claude (side session of the delivery-queue incident). Status:
2026-11-15, Nico approved `swap_lag_release_minutes: 15` in chat. The code
ships with the release off by default; the live host policy enables it.

## Incident

On 2026-11-15 the nicos-tools delivery queue landed nothing for more than
1.5 hours with a backlog of 23. Session-pressure held every gate at memory
level red. The monitor reasons named only `claude tree 92881 RSS` and total
swap. The cause was about 6.1 GB of per-session language servers (gopls x25,
the gopls telemetry sidecar x17, tsserver, pyright) across about 21 Claude
Code sessions. Relief by hand: `go telemetry off`, SIGTERM of the LSPs, and
the `gopls-lsp`/`pyright-lsp` plugins disabled. Swap fell from 11.6 GB to
about 8.1 to 8.3 GB, but the level stayed red or warning.

Verified in this session:

- pid 92881 is `/Applications/Claude.app/Contents/MacOS/Claude`, the desktop
  app. It is one agent tree that holds every desktop session and their
  children (47 direct children at 15:14 UTC). Its RSS sum moved between
  3964 and 5124 MB from 15:03 to 15:25 UTC. The tree rung (warning 3072,
  red 5120) therefore measures the sum of all desktop sessions, not one
  runaway session. That tree warning is the "live warning" that turned
  sticky swap into red.
- Before relief, `top_host_consumers` could not show the LSPs: tsserver and
  pyright are `node`, and all of them sit inside the agent tree.

## Change 1: memory families (attribution)

`Snapshot.memory_families` (latest.json and state transitions) groups every
process into a closed family set: `lsp`, `agent_host`, `build_test`,
`browser`, `other`. Each family has process count, RSS sum, the RSS inside
agent trees, `reclaimable_mb` and a `relief` hint:

| family | relief | reclaimable |
|---|---|---|
| lsp | restart_on_demand | all of it |
| build_test | finishes_on_its_own | 0 |
| agent_host | close_idle_sessions | 0 |
| browser | owner_only | 0 |

Classification is privacy safe. Node processes use only the basename of the
first positional argument, which the darwin sampler already reads for agent
identity, so there is no new syscall. The gopls telemetry sidecar is a gopls
whose parent is gopls. No PID, path, or argument is projected.

Each agent tree also carries `lsp_process_count` and `lsp_rss_sum_mb`, and the
tree reason names that share, for example
`claude tree 92881 RSS sum 6000 MB >= red 5120 MB (language servers 5800 MB in 50 processes)`.

The heartbeat carries `memory_families` as one compact string
(`lsp=5800/5800,agent_host=2906/0,build_test=900/0`: RSS/reclaimable MB). The
largest heartbeat was 946 bytes against the 1024-byte reserve before this
change. About 60 more bytes makes the projection use the observed size; the
transition snapshot stays under its 8 KiB reserve.

Commit-delivery (separate repo) reads the families from latest.json:

- `delivery doctor` adds `QUEUE_HELD_BY_MEMORY` beside `QUEUE_NOT_LANDING`
  when the resident blocks heavy work on memory. It names the top three
  families, the reclaimable MB, and the relief for the largest reclaimable
  family.
- status `next_actions` adds `relieve_memory` with the same evidence.
- A memory hold's detail and its telemetry attributes add
  `memory_top_family`, `memory_top_family_mb` and `memory_reclaimable_mb`.

## Change 2: swap lag release (off until approved)

### Evidence from telemetry (2026-11-01 to 2026-11-15)

Reproduce with `go run ./scripts/swap-lag-history`.

- Records whose primary reason is the sticky swap rung at red or critical
  (run at 15:30 UTC): 13, all on 2026-11-15 between 13:31 and 15:24 UTC. The
  thrash latch was set for 8 of them, and the longest calm before any of the
  other 5 was 1 min 17 s.
  **The 2026-11-15 hold was real thrash, not swap lag.** The swap-out bursts
  after relief reached 40 to 254 MB/s about once a minute, and swap used rose
  again from 7.9 to 9.8 GB between 15:08 and 15:15.
- Calm gaps between thrash episodes at swap >= 8192 MB: n=128, p25 27 s,
  p50 83 s, p75 5 min 13 s, p90 25 min 18 s, p95 49 min 56 s.
- What a release after W calm minutes would have released at swap >= 8 GB:

| W | gaps released | minutes released | relapse into thrash within 10 min |
|---|---|---|---|
| 5 min | 32 | 1622 | 16 (50%) |
| 10 min | 21 | 1493 | 7 (33%) |
| 15 min | 16 | 1400 | 3 (19%) |
| 20 min | 14 | 1326 | 4 (29%) |
| 30 min | 10 | 1202 | 1 (10%) |

Limit of this evidence: heartbeats carry only the primary reason every
5 minutes, and state transitions carry no swap-out rate. The calm-gap table
comes from thrash transitions, which are exact. From today, heartbeats carry
`swap_calm_seconds` and `swap_lag_released`, so the next review can measure
the release directly.

### Policy (implemented, default off)

`thresholds.swap_lag_release_minutes` (0 = off, else at least 5) and
`thresholds.swap_lag_max_growth_mb` (default 256). The sticky swap rung does
not escalate a live warning to red when all of these hold:

1. live swap I/O is available and the thrash latch is not set;
2. swap-out is at or under the release rate (16 MB/s) now, and has been for
   the whole window (the resident's calm run, measured at its own sample
   time; admission samplers project it and never extend it);
3. swap used has not grown more than 256 MB since the calm run began;
4. free memory is above the warning line (25%);
5. the live level is exactly warning.

Every thrash protection stays: live red (free, tree or agent total) still
escalates to critical, swap-out thrash still warns and latches the derate,
and a band sample (16 to 32 MB/s) restarts the window. A resident restart
loses the calm run, which only delays a release. The worker honours the
resident's `swap_lag_released` verdict because its pinned library (v0.5.2)
re-evaluates without it.

### Proposal for Nico

Enable `swap_lag_release_minutes: 15` in `~/.nicos-dev/session-pressure/policy.json`.
15 minutes is the shortest window whose relapse rate (19%) falls under one in
five, and it keeps 93% of the releasable calm minutes of the 5-minute window.
A relapse costs little: the thrash latch re-enters on two hot samples
and derates heavy work, and the first band or hot sample restarts the window.

The larger finding is the tree rung. Today one desktop app tree sums all its
sessions, so 21 sessions at about 200 MB each pass the 3072 MB warning with no
runaway. That is a separate threshold question (per-session trees inside the
desktop app, or a tree rung that excludes `lsp`), not part of this change.

## Proof

- session-pressure: `go test ./sessionpressure/` targeted run, see the
  journal entry of this session. Darwin vet: `GOOS=darwin go vet ./sessionpressure/`.
- commit-delivery: `go test ./internal/commitdelivery/ -run 'MemoryHold|HostPressure|...'`.

## Change 3: the tree rung excludes language servers (Nico, 2026-11-15)

Decision: the tree rung judges each agent tree without its language servers
(`rss_sum_mb - lsp_rss_sum_mb`), and it judges the largest such tree, not the
first tree by raw RSS. Free memory, the agent total and swap still count the
LSPs. The inventory refresh trigger uses the same size.

Why: one Claude desktop tree (pid 92881) hosts every desktop session and its
per-session LSPs. On 2026-11-15 about 5.8 GB of LSPs alone held that tree past
warning, and through the swap rung every gate at red. LSPs restart on demand
and now have their own memory family, so the tree rung keeps measuring agent
work, which is what it was built for.

Effect on the incident shape (test `TestTreeRungExcludesLanguageServers`):
raw tree 8706 MB, 5800 MB of LSPs, rung 2906 MB under the 3072 MB warning, so
swap 9724 MB with free 33% is normal, not red. Agent work of 3200 MB beside the
same LSPs still breaches warning and is corroborated to red by swap.

The reason names the exclusion:
`claude tree 92881 RSS sum 3200 MB without language servers (tree 9000 MB, language servers 5800 MB in 50 processes) >= warning 3072 MB`.

Worker consistency: commit-delivery's pinned library (v0.5.2) still counts
LSPs. The worker now follows the fresh resident's published level whenever it
is below `block_new_at` and the library computes a memory block. This
replaces the narrower lag-release check.

Not changed: relief and shedding still choose trees by raw RSS, because
shedding a tree frees its LSPs too.

## Installed acceptance (2026-11-15 15:41Z)

- Commit `b5f09f7`. Bundle cloned on runner-3 under
  `/srv/build/exports/sp-b5f09f7`, clean HEAD checked, `go vet ./...` clean,
  `go test ./... -count=1` ok for every package except
  `internal/filelock` `TestExclusiveCriticalSectionAcrossProcesses`, which
  this change does not touch and which passed 3/3 on rerun (flaky).
  darwin/arm64 built with Go 1.27.4 and `pressure-darwin-clang`.
- On the Mac before release: the targeted tests and
  `TestResidentSampler64SampleCPUAndLatencySoak` passed alone (the soak
  missed its p95 budget once inside a full run under host thrash).
  `/bin/true` and the singleflight reuse test failed in the full local run
  for reasons outside this change.
- `session-pressure-release install --expected-current e5bb660`, then
  `session-pressure monitor install`: resident reports source `b5f09f75be7b`.
- Live latest.json: `memory_families` = agent_host 3616 MB (claude x30,
  Claude helpers x21), other 2124 MB, browser 1843 MB, build_test 85 MB,
  lsp 9.5 MB (gopls x4, after the hand relief). Tree reason reads
  `claude tree 92881 RSS sum 4114 MB >= warning 3072 MB (language servers 10 MB in 4 processes)`.

## Installed acceptance of Change 3 (2026-11-15 15:55Z)

- Commit `82a7ee5`. On runner-3: clean clone of the bundle, `go vet ./...`
  clean, `go test ./... -count=1` with no failing package, darwin/arm64 built
  with Go 1.27.4 and `pressure-darwin-clang`.
- The install guard refused `--expected-current b5f09f7` because another
  session had installed `d91858c`. `82a7ee5` contains `d91858c`, so the install
  went ahead with `--expected-current d91858c`. Then
  `session-pressure monitor install`: resident reports source `82a7ee558794`.
- Live latest.json: level warning. Reasons: `claude tree 92881 RSS sum 4663 MB
  without language servers (tree 4672 MB, language servers 9 MB in 4
  processes) >= warning 3072 MB` and `swap 8148 MB >= warning 4096 MB with
  live warning pressure`. The language servers were already relieved by
  hand, so the rung is now real agent memory (`agent_host` 4057 MB). The
  calm run started at the restart (`swap_calm_start_used_mb` 8097).

## Open

- [x] Nico approved `swap_lag_release_minutes: 15` (2026-11-15, in chat).
      Set in `~/.nicos-dev/session-pressure/policy.json` (backup
      `policy.json.bak-20261115-pre-swaplag`). The installed resident ignores
      the field until the new release is installed.
- [ ] Install the release, then check that heartbeats carry
      `swap_calm_seconds` and that a calm 15-minute window logs
      `swap_lag_released`.
- [x] After install, `latest.json` carries `memory_families` (see above).
- [ ] The `other` family holds `sha256-*` session-pressure helper processes
      (about 15). Name them as their own member or family.
- [x] Tree rung excludes language servers (Change 3).
