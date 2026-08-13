# Operator guide

## Install & launch

```bash
cd apps/desktop/ndev-pressure
make run          # build release, install to /Applications, open
# or
open -a "NDev Pressure"
```

Requires `ndev` on `PATH`, or set `NDEV_PRESSURE_BIN` / `NDEV_BIN` / `NDEV_PATH`.
`ndev-pressure` is the staged extraction bridge; when present it forwards to
the same ndev authority and preserves the JSON contract.

## Daily use

1. **Menu bar** — level badge (OK / WARN / RED / CRIT). Click for free mem, CPU, swap, agent RSS, work capacity, and a shortcut to confirmed policy controls.
2. **Overview** — full board: ring, metrics, memory trend, top host consumers, admission, explicit prevention/observation coverage, guard chips, top trees.
3. **Agent Trees** — sort/filter; use **Live sample** when inventory looks stale. Trees come from the Go **agent identity catalog** (Codex/Claude/Grok/Kimi), not from ad-hoc name matching in the app. Diagnose missing agents with:

   ```bash
   ndev --json session pressure identity show --live
   ```

   Custom installs: optional fail-closed overlay at
   `~/.nicos-dev/session-pressure/agent-identity.json` (see
   `docs/active/07-31-1610-session-pressure-product-boundary/FOLLOWUPS-agent-identity-2026-08-04.md`).
4. **Disk Writes** — inspect internal-SSD rate and 15m/24h totals, adaptive confidence, likely process writers, and hourly history. The selected, active pane refreshes one live report every 15 seconds and stops when hidden/inactive. Alerts are opt-in. **Trace paths** requires a fresh live PID, explicit confirmation, and helper authorization; paths remain in memory only.
5. **Work Queue** — see who holds capacity and who is waiting (builds/tests). While this pane is visible and active, work status refreshes about every 2.5s with live work and backs off to 10s when empty; inactive/minimized windows stop spawning focus reads. Use **Run now** on a waiter to promote that exact live operation for the next admission, or **Run all** to pin the whole queue as one ordered promotion sequence. Neither is modal-confirmed — both only reorder the queue and are undone by promoting something else. Click any lease or waiter row for a detail drawer: identity, lifecycle events, and where process output lives (launching agent/terminal — not stored here). When live ownership ends, the drawer becomes read-only lifecycle history.
6. **Policy** — choose Balanced, Throughput, Interactive, or Observe (confirmations required), then use the legacy protection buttons when you need the explicit admission/auto-shed flags.
7. **Monitor** — LaunchAgent install/status/uninstall, helper budget health, and copy-only recovery evidence after an unclean start.
8. **Idle Cleanup** — list old quiet trees; **SIGTERM** only with exact session ID (CLI revalidates).
9. **Telemetry** — recent transitions and audited relief actions.

## Keyboard

| Shortcut | Action |
|----------|--------|
| ⌘1 … ⌘8 | Switch panes |
| ⌘R | Refresh (resident path) |
| ⌘⇧R | Live sample |

## Tooltips

Hover almost any control for context (SwiftUI `.help` tooltips). Copy lives in
`NDevPressureCore/HelpCopy.swift` so UI and docs stay aligned:

- Level badges explain normal → critical semantics
- Metric cards explain free memory / CPU / swap / agent RSS budgets
- Memory momentum is a bounded trend indicator, never a pressure or relief trigger
- Host-consumer rows are prompt-free executable aggregates; coverage rows distinguish enforced, coordinated, observed, and attention states
- Policy buttons explain enforce vs observe vs auto-shed
- Tree rows show pid, session, quiescent samples, typed ready/busy evidence when available, and RSS-sum caveats
- Work leases/waiters explain capacity weights; **Run now** and **Run all** explain the one-shot operator override sequence; the detail drawer explains lifecycle vs process output
- Disk-write cards explain the internal-device/all-volume-attribution boundary, confidence gate, bounded history, notification opt-in, and interactive trace privacy

## Safety notes

- **Observe only** never blocks launches or sheds trees.
- **Balanced** is the normal protected daily-driver mode. CPU warning is
  advisory; new work is blocked only after corroborated red CPU evidence or a
  safety-floor exception.
- **Throughput** keeps the full weighted ceiling and green express lane. It
  does not bypass memory, swap, storage, serious thermal, or state-integrity
  floors.
- **Interactive** is the only mode that derates *new* work at CPU/memory
  warning. Existing leases drain at their original weight, and warning
  derating is fail-open if coordinator evidence is unavailable.
- The controller reports `normal`, `busy`, `stressed`, or `emergency`. A CPU
  block requires current red CPU plus resident rolling corroboration (or the
  sustained monitor count); a one-sample spike stays admissible. Serious
  thermal state constrains heavy work, while critical thermal or memory/swap
  pressure can block a launch.
- **Full protection** can block launches at red and may SIGTERM one old quiescent tree at sustained critical — only via the resident helper, never the UI process.
- Direct external agent launches and unrelated apps are observed machine-wide
  but are not globally intercepted. They appear in whole-host attribution.
- Automatic SIGTERM authority remains agent-tree-only even when a browser, VM,
  simulator, or other process is the largest host consumer.
- If a live admission probe fails, recent resident red/critical evidence still
  blocks. Only the no-recent-evidence case remains explicitly fail-open.
- Hook-confirmed ready trees are preferred and known-busy trees are excluded;
  unknown trees retain the original age/CPU/quiescence gates.
- Tree and host rows show `sampling` when current cumulative CPU evidence is
  unavailable. This is not treated as 0%; manual cleanup and automatic relief
  remain fail-closed until a stable PID/start-time delta is available.
- The compact menu-bar surface cannot change process authority directly.
- **Run now** and **Run all** change queue priority only. Neither preempts an
  active lease or bypasses weighted capacity or host-pressure admission. The
  selected waiter's existing process still owns and runs its private command; the
  app never sees or reconstructs argv. A newer override replaces the earlier
  sequence outright, stale rows are rejected, and each distinct request is
  durably audited with an opaque cryptographic request identity independent of
  wall-clock resolution. Leases are already running — there is no separate
  “submit” action for them.
- Neither control is confirmed by a modal. Both are reversible reorderings of a
  queue already on screen, they cannot start, stop, or preempt anything, and a
  mistake is undone by promoting something else. Confirmation stays where it
  changes process authority: policy modes, monitor install/uninstall, idle
  SIGTERM, and privileged disk tracing.
- Agents that own a finite work invocation may use `work run --priority`.
  Their request appends behind any operator-pinned sequence and is audited
  separately; it has the same queue-only boundary and cannot disable a safety
  gate.
- **Run all** pins every waiter now in the queue as one ordered sequence. The
  head runs first and each entry inherits the reservation as its predecessor
  acquires, so the queue drains in the order shown rather than letting later
  small arrivals bypass it. The snapshot is taken inside the coordinator's own
  state lock, so the pinned order is the queue as it actually was.
- **Clear pins** releases the sequence and hands the queue back to ordinary
  policy. Work that already acquired a lease keeps running — releasing changes
  admission order only.
- Closing the main window does not stop monitoring. The app keeps its menu-bar
  gauge live on a 60-second compact poll; pane focus polls stop because nothing
  is rendering them.
- Recovery commands are displayed and copyable for manual review; the app never executes one automatically.
- Idle **SIGTERM** is operator-confirmed and fail-closed on drift.
- Uninstalling the monitor forces observe-only if bootout races.
- Disk observation is diagnostic only. It never raises host pressure, changes
  admission, pauses work, or grants signal/cleanup authority.
- Device totals cover internal solid-state block devices. Writer rows use
  best-effort process disk counters across every volume, so they identify a
  likely lead rather than prove exact internal-SSD or file attribution.
- "Learning" is expected for a new model. Provisional confidence requires two
  elapsed hours and 480 context samples; confident thresholds require seven
  elapsed days and 6,720 samples. Notifications additionally require a sustained
  incident and a dominant writer, and are disabled by default.
- A path trace is interactive-only, 5–30 seconds, PID/start-identity revalidated,
  and bounded to 100 unique paths from a 256 KiB capture. Neither the helper nor
  the resident persists raw trace output or paths.
- Production trace readiness requires the embedded helper and app to be signed
  by the intended team and approved through Service Management. An ad-hoc local
  build validates packaging but is not evidence of production authorization.

## Troubleshooting

| Symptom | Check |
|---------|--------|
| "ndev not found" | Install nicos-dev; set `NDEV_BIN` |
| Empty trees / consumers | Inventory may be cached; inspect its age/source chip, use **Live sample**, or wait for the 180s resident refresh |
| Coverage says observed | The surface is visible but intentionally not globally intercepted; route heavy work through `ndev session pressure work run` |
| Codex appears stuck during file I/O | The project Bash hook does not match Read/Write/Edit. Check `ndev --json session pressure work status` for a queued shell operation. Check `ndev --json session pressure check` for host pressure. |
| Operator not ready | Review health reasons and any unclean-shutdown recovery hint; daily-driver protection alone does not clear operator work |
| Admission blocked | Host at red+ with enforcement; free capacity or wait |
| Work waits for minutes | This usually means weighted capacity or a protected drain. It is not a silent denial. Follow the runner stderr progress. Inspect the waiter's `blocker`, `bypass_count`, and `protection_reason`. |
| Budget breach | See Monitor pane; helper may suppress auto-shed until clean |
| Dock badge stuck | Level elevated; recover host load or open Overview |
| Disk state says unavailable | Confirm macOS/internal SSD visibility and resident freshness; CPU/memory protection remains independent |
| No likely writers | Processes may be inaccessible, short-lived, or idle during the live delta; refresh while the write is active |
| Device bytes exceed writer bytes | Expected when processes exit or are inaccessible; writer counters are all-volume correlation, not a byte ledger |
| Trace helper unavailable | Install a properly signed app in `/Applications`, approve its launch daemon, then retry; ordinary observation does not require it |

## Related CLI

```bash
ndev --json session pressure status --full
ndev --json session pressure snapshot
ndev session pressure policy show
ndev --json session pressure policy profile show
ndev session pressure policy profile apply balanced
ndev session pressure policy profile apply interactive
ndev session pressure monitor status
ndev --json session pressure work status
ndev --json session pressure work override --operation-id ID --confirm
ndev --json session pressure work override --all --confirm
ndev --json session pressure work override --clear --confirm
ndev session pressure work run --class test --priority -- go test ./...
ndev --json session pressure work history --operation-id ID
ndev --json session pressure board --include doctor,calibration
ndev --json session pressure io status --live
# Without --live, top returns the persisted lead; --live returns a bounded list.
ndev --json session pressure io top --live --limit 5
ndev --json session pressure io history --since 24h --limit 20
ndev session pressure io policy enable-alerts
ndev --json session pressure io trace --pid 1234
ndev --json session pressure idle --limit 10
ndev --json session pressure telemetry --since 24h --limit 20
```

KEP: `docs/active/07-13-1448-session-host-pressure-guard/session-host-pressure-guard-kep.md`

SSD watchdog KEP: `docs/active/07-22-1704-session-pressure-ssd-write-watchdog/session-pressure-ssd-write-watchdog-kep.md`
