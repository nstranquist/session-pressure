# Architecture

SessionPressure is a local host coordinator. One policy store and one telemetry store. The desktop client does not grow a second evaluator.

## Open set

- host sampler
- policy and the four work styles
- weighted work coordinator
- agent identity catalog
- internal-SSD write watchdog
- doctor, self-test, audit, board
- resident helper and LaunchAgent
- standalone CLI and local opt-in API
- thin native macOS client

## Memory levels

The free, swap, tree and agent-total rungs and the resident's self-RSS budget
come from `memory-levels.json`, not from code. The shipped copy is embedded
(`sessionpressure/memory_levels.json`). A full replacement may sit in the data
directory (`~/.nicos-dev/session-pressure/memory-levels.json`).

- Each anchor is one measured host shape (16 GiB laptop, 48 GiB workstation)
  with the monitor evidence it came from. A host between anchors gets
  interpolated values. Outside the anchors, MB values scale with memory and
  percent values stay at the nearest anchor. `hosts.<short-hostname>` can
  override single values with a required `reason`.
- `rss_escalation_free_percent` holds the RSS-sum rungs at warning while the
  kernel free level (`kern.memorystatus_level`) is above it. Summed RSS double
  counts shared pages, so a large host can pass red with 90% free. 100 means
  the RSS rungs always escalate (the 16 GiB laptop).
- `policy.json` `memory_levels` selects the source: `calibrated` re-resolves
  the levels on every load, `manual` (or empty) keeps `policy.json`'s own
  thresholds. `policy init` writes `calibrated`.
- Commands: `session-pressure policy memory-levels show`, `check [FILE]`,
  `calibrated`, `manual`. A bad file fails the policy load and names the file
  and field. Calibration record:
  [memory-levels-calibration-2026-11-17](../corpus/memory-levels-calibration-2026-11-17.md).

## Closed set

Factory launch hooks, Toolguard, and nicos-only reclaim stay in nicos-tools. The extract pageskein reclaim path returns `ErrNotAvailable`.

Canonical boundary: [OPEN-CORE.md](../OPEN-CORE.md). Desktop operator guide: [apps/SessionPressure/docs/operator-guide.md](../apps/SessionPressure/docs/operator-guide.md).
