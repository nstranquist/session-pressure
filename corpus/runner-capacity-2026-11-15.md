# Runner capacity and usage view

Owner: claude (Claude Code desktop session, 2026-11-15). Status: installed.

## Request

Nico asked for a management UI for the Hetzner runners (runner-3, reserve
runner-1, later hosts) to show whether the fleet is used well and to catch
spikes that limit capacity. He also asked whether Hetzner traffic limits call
for external storage.

## Decisions

1. Extend the existing Hetzner tab instead of building a new app. The tab
   already reads the fleet inventory and live jobs; it lacked use over time,
   limits and alerts.
2. Keep the app's contract: the Go CLI owns every SSH and file read
   (`remote capacity`), Swift only decodes and renders.
3. Read history from the runner's existing `host_samples` table (15-second
   samples, 14-day retention) instead of adding a sampler. Bucket on the host
   with `sqlite3 -readonly` so the payload stays small (at most 336 rows).
4. Count heavy-slot saturation per sample on the host (`sum(heavy>=slots)`),
   not from bucket maxima, so short bursts are not reported as hours.
5. Record NIC counters locally in SQLite because runners keep no network
   history. Rejected: installing vnstat on the runners (a host change outside
   this request) and a commit-delivery schema change plus runner redeploy.
   What would flip it: a runner-side network sampler in commit-delivery, which
   would give continuous history independent of the app.
6. Traffic terms are encoded per provider with their source, not inferred.

## Traffic finding (2026-11-15)

runner-3 is a Hetzner dedicated server (Ryzen 5 3600, 64 GB, RAID,
1 Gbit/s). Hetzner's Robot docs state 1 Gbit/s root servers have unlimited
traffic; a 10 Gbit/s uplink includes 20 TB/month. Measured since boot (6.2
days): 233.9 GB in, 55.5 GB out; 195.3 GB of the inbound arrived over the
tailnet (work sent from the Mac). Disk: 168 GB used of 996 GB. Neither traffic
nor capacity calls for external storage. A Hetzner Storage Box remains a
reasonable target for off-host backups (durability), which is a separate
decision.

## Live proof

First live 7-day read: runner-3 ran 3,288 jobs at 4.3% average CPU and
6.8% heavy-slot occupancy, yet all five heavy slots were full in 6 episodes
(about 22 minutes); 9 SETUP failures. runner-1 (reserve, no samples) ran
1,191 jobs with 48 SETUP failures.

## Verification

- Go: `go test ./internal/gaterunner ./sessionpressurecmd` pass (probe parse
  from a live-shaped fixture, truncation and invalid memory rejection,
  episodes across gaps, exact saturation seconds, reboot/reset/gap-safe NIC
  rates, peak-preserving downsampling, allowance projection, fleet fan-out
  with an unreachable reserve).
- Swift: contract test decodes a recorded live payload; store keeps the last
  good report on failure and re-reads on window change.

## Installed acceptance (2026-11-15)

- Source commit `f710c0bda6c9e416501f38831f12c004b1b648c7` (guarded single
  commit). Bundle cloned on runner-3 under
  `/srv/build/exports/capacity-f710c0b`, clean HEAD checked, darwin/arm64
  release built there with Go 1.27.4 and `pressure-darwin-clang`.
- CLI promoted with `session-pressure-release install --expected-current
  696fd668c9c6753fe98da5c536f3f54b6f54b43a`; `current` now reads `f710c0b`.
  Installed `remote capacity` answered for both hosts directly and through
  `ndev --json session pressure remote capacity`; an unsupported window exits 2.
- App installed with `make install-headless` (Developer ID signature verified,
  rollback preserved; archive maintenance pending because the archive disk was
  not mounted). The running app was not relaunched; it loads the card on its
  next start.
- `swift test --filter RunnerCapacity` with `PRESSURE_GATE_RUNNER_CONTRACT_BIN`
  set to the installed CLI and `PRESSURE_GATE_RUNNER_BOX=1`: 8 tests passed,
  including the real-CLI read. Full suite before install: 134 tests passed.
- Offscreen render (SwiftUI ImageRenderer, no window) of the live 7-day report
  checked alerts, tiles and aligned charts. Segmented controls do not render
  offscreen; that is a renderer limitation, not an app defect.

## Hetzner page review and performance sweep (2026-11-15)

Request: review the Hetzner page, sweep its performance, error handling and
displays, find feature and design gaps, and lazy-load to save CPU and RAM.

### Findings (before)

Measured with the installed CLI `f710c0b` on this Mac:

| Read | Wall | Peak RSS | Payload |
| --- | --- | --- | --- |
| `remote capacity` (every poll) | 1.9-2.4 s | about 17 MB | 34-85 KB |
| `remote inspect` without box (every 15 s) | 0.3-0.4 s | 23 MB | 705 KB when the ledger changed |
| `remote inspect` with box (every 60 s) | 2.7 s | 31 MB | 1.26 MB, about 550 KB of it unchanged job receipts |

1. Capacity polling started on `onAppear` only, so it kept reading over SSH
   while the window was hidden or the app inactive.
2. A window change during an in-flight read was dropped until the next poll,
   and the labels showed the new window over the old data.
3. One failed host read blanked that host's tile.
4. The page was an eager `VStack`: every card, chart and up to 1,000 rows
   were built even when off screen; `filteredRuns`/`filteredJobs` were
   recomputed about four times per render, on every 15-second refresh.
5. Two separate runner selections (capacity tile, job-history picker).
6. The job log sat at the bottom of the page, far from the selected row, as
   one SwiftUI `Text` (slow for a 100 KB log).
7. Raw error codes (`gate_runner_read_failed`) were shown to the operator, and
   SSH failures could not be told apart (timeout vs. connect vs. remote).
8. `DateFormatter` was allocated per alert row.
9. No way to read exact values on the charts, or to see when an alert happened.

### Decisions

1. Jobs cursor in the CLI: `--known-jobs SHA256` returns
   `jobs_unchanged=true` and no rows. Same shape as the existing ledger cursor.
2. Typed SSH failure kinds (`timeout`, `connect_failed`, `remote_failed`,
   `output_too_large`) in `box_failure` and `runner_capacity_<kind>`; the app
   maps every code to operator text.
3. Capacity store moved to `ContentView`; it reads only while the pane is
   active (window visible, app active, Hetzner selected) AND the card is on
   screen. Cadence follows the bucket size: 6h every 60 s, 24h every 120 s,
   7d/14d every 300 s. Returning within the cadence does not re-read.
4. Mid-read window change sets a pending refresh; a result for a stale window
   is not shown. While the new window loads, the card says which window the
   shown data covers.
5. Per-host stale merge: a failed host keeps its last good data with a STALE
   badge, the failure text and the data age.
6. Page is a `LazyVStack` of separate cards; filtered lists are cached in
   `@State`, keyed by store revision counters plus filters; rows are
   `Equatable`; search is debounced 180 ms.
7. One runner selection: a capacity tile or alert also points the job receipts
   at that runner. The runner picker moved into the receipts card.
8. Job log opens inline under the selected row in an `NSTextView`.
9. Charts share one hover position (`chartXSelection`) with a readout line;
   tapping an alert highlights its episode as a band on every chart. Plots
   are clipped to the window.

### After (installed CLI `14c7d24`)

| Read | Wall | Peak RSS | Payload |
| --- | --- | --- | --- |
| `remote inspect` with box, both cursors match (steady state) | 2.6 s | 25 MB | 33 KB |
| same, new receipt landed | 2.6 s | 25 MB | 583 KB |
| unknown host | fails fast | - | `box_failure=connect_failed` |

The steady-state per-minute box read went from 1.26 MB to 33 KB (97% less
JSON for the app to decode). Wall time is SSH-bound and did not change.

### Verification

- Go: `gofmt`, `go vet`, `go test ./internal/gaterunner ./sessionpressurecmd`
  pass (new `TestClassifySSHFailures`).
- Swift: 140 tests pass, including new tests for stale host merge,
  mid-read window change, pane/card visibility gating, cadence and the jobs
  cursor (unchanged receipts do not bump `jobsRevision`).
- Installed CLI `14c7d24` built on runner-3 from a verified bundle
  (`/srv/build/exports/hetzner-perf-14c7d24`), installed with
  `--expected-current f710c0b`. Installed-CLI contract tests: 25 pass.
- App installed with `make install-headless` (Team ID C82D23RNQ9, rollback
  kept, not relaunched).
- Offscreen render of the card checked layout, tiles, stale-safe charts and
  the hover hint. The fixture alert has no time range, so the episode band is
  covered by code review, not by the render.

### Open gaps (not done here)

- Critical capacity alerts reach the operator only when the tab is open.
  A background check that feeds the app's notification bell (or the
  commit-delivery digest) would catch spikes while away.
- Network history is sampled only while the app polls; a runner-side sampler
  (decision 5 above) would make it continuous.
- runner-1 records no `host_samples`; its CPU/slot history stays empty
  until its runner build is updated.
