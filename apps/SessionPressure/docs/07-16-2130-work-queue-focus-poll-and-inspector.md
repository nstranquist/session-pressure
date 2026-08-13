# 2026-07-16 — Work Queue focus poll + inspector

## Shipped

- 2.5s live-work / 10s empty work-status focus poll only while Work Queue is visible and active; inactive/minimized windows spawn no focus reads
- Run now (confirmed override) for waiters; leases are already running
- Detail drawer: lifecycle from `work history`, output-location honesty, copy/reveal helpers, and read-only history after live ownership ends

## Verification

- `swift test` (core + queue admission / history decode + PressureStore lifecycle transitions)
- `make install` → `/Applications/NDev Pressure.app`

## Review notes

- Drawer Run now must share `pendingWorkOverride` confirmation with queue rows (fixed in closeout).
- When a watched waiter acquires, selection promotes to lease and reloads lifecycle once.
- Missing waiters/leases transition to read-only history and stale confirmations are dismissed.
- Poll cadence is visibility-aware and idle-backed-off; optional later: FSEvents on work-leases.json.
- Optional stdout capture remains out of scope unless the control plane adds an explicit tee contract.

See also: architecture.md, operator-guide.md, README.
