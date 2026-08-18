# Architecture

## Role

**SessionPressure** is a thin native macOS shell over the stable
`session-pressure` JSON control plane. It does **not** reimplement host
sampling, policy evaluation, admission, or relief. Authority stays in:

- `internal/sessionpressure` (Go core)
- `session-pressure …` (CLI surface)
- Resident helper `session-pressure-helper` (LaunchAgent)

The product CLI is `cmd/session-pressure`. The app prefers it through
`SESSION_PRESSURE_BIN` / `NDEV_PRESSURE_BIN` and can fall back to `ndev`.
No second policy store or telemetry database is introduced.

## Process model

```
┌─────────────────────────────────────┐
│  NDev Pressure.app                  │
│  ┌──────────────┐  ┌──────────────┐ │
│  │ Main window  │  │ MenuBarExtra │ │
│  │ (8 panes)    │  │ (at-a-glance)│ │
│  └──────┬───────┘  └──────┬───────┘ │
│         │  shared PressureStore     │
│         └──────────┬────────────────┘
│                    │ Process + JSON
└────────────────────┼────────────────┘
                     ▼
            session-pressure --json session pressure …
                 (product CLI; ndev wrapper is the fallback)
                     │
     ~/.nicos-dev/session-pressure/
       policy.json · latest.json · work-leases.json
       snapshots-… · disk-writes-… · actions-… · LaunchAgent
```

The resident samples internal solid-state block-device counters with IOKit and
per-process cumulative disk bytes with libproc. The latter covers all mounted
volumes, so the model exposes it only as likely-contributor evidence. The app
does not sample either source itself.

An optional `NDevPressureTraceHelper` launch daemon is embedded in the app for
an operator-confirmed 5–30 second `fs_usage` trace. It is outside the resident
loop, communicates over a narrow XPC protocol, revalidates PID/start identity
before and after attach, returns at most 100 unique paths from a 256 KiB capture,
and persists nothing.

## Packages

| Target | Responsibility |
|--------|----------------|
| `NDevPressureCore` | Codable models, `NDevPressureClient`, formatters, `PressureHelp` tooltip copy, ISO date decoding |
| `NDevPressure` | SwiftUI scenes, store, views, menu bar |
| `NDevPressureTraceHelper` | Privileged, interactive-only XPC path trace with bounded capture and identity revalidation |
| `nicos-dev/cmd/ndev-pressure` | Temporary product-local CLI boundary; forwards the stable JSON protocol to the ndev authority |

No third-party SPM dependencies. macOS 14+, Swift 6.

## Refresh strategy

`PressureStore` polls adaptively:

| Level | Interval |
|-------|----------|
| normal / unknown | ~20s |
| warning | ~12s |
| red | ~8s |
| critical | ~5s |

Every adaptive board refresh runs compact status, work status, and admission in
parallel. Full status is limited to Overview, Agent Trees, and Monitor. Policy
and monitor state load once and then refresh on their own panes; doctor and
calibration load only where rendered; idle and telemetry load only on their
panes. Existing rich values remain in the board instead of being fetched again.
**Live sample** additionally forces a fresh host probe. The light menu-bar path
uses only compact status, work status, and admission.

The resident keeps its last native process inventory during a healthy cadence.
When the 180-second inventory interval expires, the refresh is scheduled after
the heartbeat and the sample reports the cached inventory with its age. This
keeps normal heartbeats responsive while cumulative process CPU still appears
in the resident's interval accounting. The first inventory, operator/live
samples, memory-pressure samples, and cleanup checks remain synchronous; a
forced refresh never consumes stale inventory silently.

The resident uses the same split for the typed thermal and low-power probe.
The first sample and warning-or-critical samples read `pmset` synchronously.
Healthy samples reuse the last completed result and schedule the next probe
after the heartbeat. Operator and live host samples remain synchronous. A
probe failure stays visible in the typed error field; it never turns an
unavailable signal into a block. Serious or critical thermal evidence remains
a safety floor.

When the **Work Queue** pane is selected and its window is active and visible, a
second focus poll runs **work status only** (no idle/telemetry/policy board): every
~2.5s while leases or waiters exist and every 10s when empty. Inactive or minimized
windows retain only a cheap suspended task check and spawn no `ndev` status reads.
Leaving the pane cancels the focus poll. A live selection that disappears becomes
read-only lifecycle history rather than remaining falsely queued/running.

The selected, active **Disk Writes** pane takes one full live write report every
15 seconds. The CLI composes that fresh rate/writer sample with the fresh
resident-owned adaptive state; the report already contains its bounded writer
list, so the app does not launch a second `io top` process. Hourly history loads
initially and at most once per five minutes. Leaving the pane, minimizing the
window, or deactivating the app cancels this focus loop.

## Mutations

Reads go through `session pressure board`, one composite contract carrying host
status, the weighted work queue, and launch admission, plus opt-in doctor,
calibration, policy, monitor, idle, and telemetry sections. Before it the app
spawned one `ndev` per contract — 4-5 cold starts per refresh, on a host the app
exists to keep unloaded. The per-contract fan-out is retained and used only when
the installed helper rejects `board` as an unknown subcommand; any other failure
surfaces rather than silently halving fidelity.

The **Storage** pane (⌘4) has two tabs. Disk reclaim reads `storage providers` /
`storage status` and mutates only through typed `storage apply --auto-safe` or
`storage apply --provider ID`, plus `storage policy enable|observe`. Preview is
dry-run (no `--apply`). Confirm adds `--apply`. Output is streamed into an
append-only receipt. Idle trees is the former Idle Cleanup surface (operator
SIGTERM). The Overview storage card opens this pane. The app does not spawn an
agent, PTY, or arbitrary argv.

**The board's sections must run concurrently.** The fan-out it replaced issued
its five children in parallel, so its wall time was the slowest child, not the
sum. A first sequential implementation of `board` measured **757ms** median
against **370ms** for the fan-out — it bought the process saving with double the
UI latency. Anything added to this verb belongs inside the concurrent block, not
after `wide.Wait()`.

Measured on one host, `--full --include doctor,calibration`. **Compare minima,
not medians:** the calibration section reads the work-event ledger, which other
agents write continuously, so medians swing by 2× under contention and once made
the concurrent build look like a regression when it was not.

| | processes | wall (min) | child CPU |
|---|---|---|---|
| fan-out | 5 | 397ms | 763ms |
| board, sequential | 1 | ~757ms | 573ms |
| board, concurrent | 1 | 342ms | 577ms |

Per-section minima confirm the overlap is real — the verb costs its slowest
section, not their sum:

| | min |
|---|---|
| `board` bare | 105ms |
| `board --full` | 105ms |
| `board --full` + doctor (115ms standalone) | 119ms |
| `board --full` + doctor + calibration (326ms standalone) | 333ms |

**Admission is derived, not probed, unless `--live`.**
`ConfiguredAgentLaunchAdmission` samples the host on every call and measured
~300ms — alone larger than every other section combined, and it made `board`
worse than slow: a resident read reported resident numbers everywhere and a
live-probed admission beside them, two instants presented as one. A board
without `--live` now evaluates `AgentLaunchAdmissionFromSnapshot` against the
snapshot it already holds. Bare board went **299ms → 105ms**. Provenance stays
explicit — `resident+memory-gated-launch` versus `live-host-probe+…` — so a
display read can never be mistaken for a launch gate. `session pressure check`
and every real launch gate still take the live probe; nothing about admission
*enforcement* changed.

What remains: **calibration is now the dominant section** (326ms, a full 24h
work-event ledger read) and it is requested on the Overview and Work panes. That
is the next thing to attack, not process count.

All mutations shell out and then re-read the board:

- `policy enable|observe|init`
- `monitor install|uninstall|once`
- `idle --apply --root-pid … --session-id …`
- `work override --operation-id … --confirm` / `work override --all --confirm`
- `io policy observe|enable-alerts|disable`
- interactive trace handoff followed by an authorized helper request

Actions that change process authority — policy modes, monitor install/uninstall,
idle SIGTERM, privileged tracing — use confirmation dialogs in the UI; the CLI
still revalidates before any signal. Queue reordering (Run now / Run all) is not
modal-confirmed: it cannot start, stop, or preempt a process, and is reversed by
promoting something else. The menu-bar extra navigates to the main
Policy pane instead of changing authority directly. Every CLI child also has a
hard 30-second timeout and bounded decoded output.
The capture path continuously drains bounded in-memory pipes; recurring polls
do not create stdout/stderr temp files on the SSD.

The work override is a one-shot priority mutation against exact opaque operation
IDs from the current typed status. The Go coordinator, not SwiftUI, keeps the
selected waiter protected until it acquires, is cancelled, or its owner
disappears. Existing leases are never preempted, capacity and host admission
remain authoritative, and a later override replaces the prior sequence.

`--clear` releases the sequence, so a bulk promotion is reversible rather than
only endable by draining. Releases are audited per operation and never touch a
lease that already acquired.

`--all` pins the whole queue as one ordered sequence, stored in work state
schema 8 as an active head plus an ordered pending tail (`override_queue`). The
coordinator advances the head as each pinned waiter acquires, cancels, or
expires, and drops entries whose waiter has left the queue — the app holds no
promotion state of its own, so a pinned drain survives quitting it. The queue
snapshot is resolved inside the state lock, so `--all` cannot pin a list that
changed between a client's read and its write. An n-1 (schema 7) helper still
reads the active head and simply loses the pending tail.

## Privacy

- Full process commands never enter durable telemetry (CLI contract).
- Whole-host attribution groups only a bounded executable basename, category,
  RSS/CPU sums, process count, and agent-owned count. It contains no PID, path,
  argv, environment, or prompt text; transitions retain at most two buckets.
- macOS process CPU is derived from two native cumulative CPU-time readings
  matched by PID and process start time. Missing, regressing, or reused-PID
  evidence remains explicitly unavailable; it never becomes an idle zero and
  cannot authorize manual cleanup or automatic relief.
- The UI never displays argv/env from work leases.
- The UI cannot start a queued command itself: commands are intentionally never
  persisted. **Run now** / **Run all** (work override) promote already-running
  waiters through the Go coordinator so they acquire next, in the pinned order,
  after normal safety gates admit them.
  Active leases are already executing; they are never preempted from the UI.
- Process stdout/stderr are not mirrored in the app. Click a lease/waiter for a
  privacy-bounded lifecycle drawer (`work history` events + digests + ledger path).
- Session IDs are shown for operator resume/cleanup only.
- Hook state is projected only as typed `ready` / `busy` plus its timestamp;
  prompt bodies and hashes are absent from pressure JSON and the UI.
- Durable disk-write history contains hourly byte totals, gap totals, histogram
  features, and bounded executable basenames. It never contains PID, process
  start identity, argv, executable path, file path, prompt, or environment.
- File paths exist only in the interactive trace reply held by the app. The
  helper caps duration/output/path count, captures no background history, and
  writes no trace artifact.
- Trace authorization is separate from observation. A missing, unsigned, or
  unapproved helper leaves device/process observation fully available.

## Trend and coverage truth

- The Go resident, not SwiftUI, derives memory momentum from up to five recent
  free-memory samples. It labels steady/declining/rapid decline/recovering and
  may estimate minutes to the red threshold. Momentum is diagnostic only: it
  cannot raise pressure or authorize relief.
- `status` returns typed enforcement coverage. Canonical agent launches and
  repo toolguards can be enforced; weighted work is coordinated; direct
  external launches and unrelated apps are observed but are not globally
  intercepted.
- Automatic relief remains limited to a freshly revalidated old quiescent
  Codex/Claude/Grok/Kimi tree. Whole-host attribution does not grant kill
  authority over browsers, simulators, VMs, or other applications.
- Agent tree ownership is catalog-driven in Go (`sessionpressure` agent
  identity table), not ad-hoc name matching in the app:
  - **Shipped rules** — exact basenames (`codex`, `claude`, `grok`, `kimi`),
    node script basenames, path probes (Claude pure SemVer `p_comm`, Grok
    `grok-<digit>…` truncations), and trusted home-relative install roots
    (`~/.local/share/claude/versions/`, `~/.grok/bin/*`, `~/.grok/downloads/`).
  - **Version numbers are never hard-coded** — only install-layout shapes.
  - **Operator overlay** — optional
    `~/.nicos-dev/session-pressure/agent-identity.json` may *expand* roots and
    basenames only; corrupt overlays fail closed to shipped defaults (never
    silently widen ownership). Inspect with
    `ndev session pressure identity show [--live]`.
  - **Session-state hints** — hook session files can fill missing Agent when
    the session id is known; they never override a conflicting process label
    (relief safety).
  - Coverage surface `agent_identity` reports unlabeled agent-shaped processes
    and overlay failures.
- A failed live admission probe reuses recent resident evidence, including a
  red/critical block. With no recent evidence, it stays visibly fail-open so
  the guard cannot become a machine-wide availability dependency.
- Disk anomaly state is independent from pressure level and admission. The
  adaptive model learns context-specific quiet p99 values, rebases counter
  resets/wake gaps/device changes, excludes anomalous samples from training,
  and cannot authorize throttling, cleanup, or termination.

## Performance budgets (app)

| Metric | Target |
|--------|--------|
| Release binary | ~2–4 MB |
| App bundle | < 10 MB |
| Steady UI RSS | prefer < 120 MB after warm-up |
| Base poll process spawn | compact status + work + admission; larger calls only for the visible pane |
| Disk focus poll | one full live report / 15s while visible and active; hourly history <= 1 / 5m |
| Trace capture | interactive only; 5–30s, <= 256 KiB raw capture, <= 100 returned paths |

The **guard's** budgets (helper RSS, duty, sample p95, telemetry/day) are
displayed, not enforced by this app.

## Tooltips

All operator-facing chrome should expose `.help(...)`. Canonical strings live in
`NDevPressureCore/HelpCopy.swift` (`PressureHelp`) and are reused by:

- Level badges / pressure ring
- Metric cards, status chips, capacity bar
- Sidebar sections, toolbar refresh/live actions
- Policy/monitor mutation buttons
- Tree rows, work leases/waiters
- Menu bar extra

## Screenshots

`docs/screenshots/overview.png` is a retina mock of the Overview pane for README
and catalog evidence (live `screencapture` may black out windows without Screen
Recording permission for the capturing process). Regenerate with the AppKit
mock path in `script/` or a permitted interactive capture of the real window.

## Key files

```
Sources/NDevPressureCore/
  Models.swift            # Snapshot, policy, work, telemetry envelopes
  DiskWrites.swift        # Disk summaries, writers, history, policy, trace handoff
  TraceProtocol.swift     # Narrow privileged-helper XPC contract
  TraceParsing.swift      # Bounded fs_usage path extraction
  NDevPressureClient.swift
  Formatters.swift
  HelpCopy.swift          # Tooltip / help strings
  JSONCoding.swift
Sources/NDevPressure/
  PressureStore.swift
  ContentView.swift
  PrivilegedTraceClient.swift
  Components.swift
  Views/*.swift
  MenuBarScene.swift
  NDevPressureApp.swift
Sources/NDevPressureTraceHelper/
  main.swift
  TraceService.swift
docs/
  architecture.md
  operator-guide.md
  screenshots/overview.png
```
