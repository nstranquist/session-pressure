# Windowless view probe, scene gate and `make render`

Work: WORK-SP-14 (SP-5). Owner: claude (session 46175392). Date:
2026-11-24. Status: built and tested; delivery recorded in WORK-SP-14.

## Why

The Work Queue overflow (`work-queue-slot-bar-overflow-2026-11-24.md`)
passed review, type checks and every unit test. Nobody saw it until Nico
opened the page. Agents had no way to look at a page without opening the
app on Nico's desktop, which the agent rules forbid. The old evidence
renders used `ImageRenderer`, which does not draw `ScrollView` content and
never saw a whole page.

## What it is

- `Tests/NDevPressureCoreTests/Support/ViewProbe.swift` lays out, measures
  and draws any SwiftUI view in an `NSHostingView` that is never attached
  to a window. No window-server window exists, nothing appears on the
  desktop and focus never moves. Every gate run asserts `window == nil`.
- `Support/Scenes.swift` is the scene catalog: 18 named app states (every
  sidebar page, plus Work Queue with a lease open and with a 1,024-slot
  runner, Hetzner with a capacity alert focused and with a job log open,
  Storage's idle tab, and the bell popover). A scene builds the real page
  view from recorded fixtures, or from the installed helper with `LIVE=1`.
- `SceneGateTests` runs in plain `swift test`. Each scene must fit the
  narrowest detail pane (`WindowLayout.narrowestDetailWidth`: 980 pt
  minimum window minus the 280 pt widest sidebar = 700 pt), no platform
  view may reach past the host's edges, and the page must draw (ink, the
  share of pixels unlike the background, at least 1%). A second test
  requires a scene for every sidebar page.
- `make render [SCENES=…] [LIVE=1] [APPEARANCE=light] [WIDTH=…]
  [HEIGHT=…] [SCALE=…] [RENDER_DIR=…]` writes PNGs and `manifest.json`
  (scene, size, scale, appearance, source, minimum and needed width,
  escapes, ink, fixture names and SHA-256, source revision).

## What the probe found out about windowless AppKit

| Finding | Consequence |
| --- | --- |
| A page that sets no background is transparent without a window. White text in dark mode vanished in the PNG, and the earlier probe read this as "menu pickers, unselected segments and labels do not draw". | `host` paints `windowBackgroundColor` behind the page, as the window does. With it, menu and segmented pickers and all labels draw. |
| A `List` is an `NSTableView`. A table builds rows only for its `visibleRect`, which is empty without a window. | `host` builds the rows inside each table's clip view (`buildTableRows`). A probe self-test checks list rows draw. |
| SwiftUI widens a page's `ScrollView` to its content's minimum width and centres it (the bug drew a 4,133 pt scroll view at x = −1,716 in a 700 pt host). | `fit` (`sizeThatFits` at the offered width) catches it on a plain page. |
| A `GeometryReader` accepts any offered width, so `fit` cannot see an overflow inside `ResizableSplit`. | `horizontalEscapes` walks the laid-out platform views and reports any that reach past the host. With the old one-segment-per-slot `SlotBar` restored, the gate failed through this check: `PlatformContainer spans x -1220…1921 in a 700 pt host`. |

## Bugs the gate found on first run

`ResizableSplit` overflowed whenever its container was narrower than both
pane minimums plus the 1 pt divider. At the narrowest window:

- Work Queue with a lease open: 320 + 1 + 480 = 801 pt in 700 pt. The
  detail pane ran from x 481 to 801, so its Reload and Close buttons
  (x 750–787) were off screen.
- Process Incidents: 290 + 1 + 400 = 691 pt inside 660 pt of padded
  content; the detail ran 11 pt past the window edge.

Fix: `SplitLayout.widths` (Components.swift) keeps the old behaviour when
both minimums fit and, when they do not, shrinks both panes in proportion
to their minimums, so a split never outgrows its container. The drag
handle uses the same function. `SplitLayoutTests` covers both cases.

## Other changes

- `WindowLayout` (ContentView.swift) names the window and sidebar limits.
  `NotificationPopoverBody.width` names the 320 pt popover width. The gate
  reads these, so a change to the window cannot silently weaken it.
- `ProcessIncidentsView(read:)` takes the incident reader, like
  `HetznerStore` and `RunnerCapacityStore` already did. The default is the
  live client.
- Removed the three env-gated `ImageRenderer` render tests (Work placement,
  Runner capacity, Runner alert popover). Their states are scenes now.
  `WorkPlacementLayoutTests` and `RunnerAlertTests` use the shared probe and
  fixture helpers.

## Proof (this session)

- `swift test`: 171 tests in 31 suites pass; the scene gate takes about
  2.6 s for 18 scenes.
- Gate before the split fix: `process-incidents` and
  `work-queue-selected` failed with the escapes above. After: all pass.
- Negative check: `SlotBar.maxSegments` set back to one segment per slot
  makes `work-queue-large-runner` fail; restored, it passes.
- `make render LIVE=1`: all 18 scenes rendered from the installed helper
  in 39 s, all fit 700 pt. Live Work Queue, the receipt log panel and
  light-mode Overview were checked by eye.

## Limits and next steps

- Disk Writes is gated with recorded data since WORK-SP-15 (below).
  Telemetry was already populated: `board-full.json` carries 40 events.
  Its Relief actions card stays empty because no host has recorded a
  relief action (none in a year on the primary Mac, none in 90 days on
  the second Mac), and a hand-made row would break the fixture rule. Its
  rows are wrapping text only.
- The job-log scene's log text is built in Swift (`SceneFixtures.jobLog`,
  a typed `GateRunnerJobLog` with a 600-character line), not CLI JSON. It
  exists to stress layout, not to test the decode contract.
- The gate checks horizontal fit at the narrowest window. It does not
  judge vertical clipping, colour or text truncation. Use `make render`
  and look.
- Ink is a blank-page detector, not a visual diff. A pixel-diff baseline
  was rejected: dates and relative ages change every run, and the failures
  that reached Nico were layout and blank pages, which the gate catches.
  Whether goldens are ever worth adding is Nico's call (WORK-SP-14).

## Follow-up: recorded Disk Writes data (WORK-SP-15)

WORK-SP-14 said Disk Writes and Telemetry had no recorded data. That was
half wrong: Telemetry draws the 40 events in `board-full.json`. Disk Writes
did show only its empty states.

- `Fixtures/io-status.json`, `io-history.json` and `io-health.json` are the
  three real reads behind the page (`io status --live --full`, `io history
  --since 24h --limit 20`, `io health`), recorded 2026-11-24 while a
  full-state backup wrote about 260 MB/s. `Fixtures/io.md` lists the
  commands and the two scrubs (data dir, SSD serial).
  `DiskWriteContractTests` decodes them and fails if a re-record drops
  the scrubs.
- The `disk-writes` scene now draws the state header, rate tiles, SMART
  card, four writers, the hourly chart and the policy card. It fits 700 pt
  in dark and light mode.
- Real data showed two wrong labels, now fixed in `DiskWritesView`.
  - The producer omits `baseline_p99_bytes_15m` and `baseline_ratio` until
    a nonzero quiet baseline exists (`nonzero_baseline_insufficient`).
    Swift decoded them as 0, so the page claimed "0.00× quiet p99" and a
    quiet p99 of "0 B". It now says "no quiet baseline yet" and "None
    yet".
  - The hourly chart's last date and hour axis label ran past the plot
    and was cut to "Nov 24…". The axis now has hour-only labels every 6 h.
- Not fixed here: writer names come from the kernel's 16-character
  process name (`nicos-system-bac`, `nicos-delivery-p`). A full name needs
  a producer change.
- `swift test`: 175 tests in 32 suites pass.
