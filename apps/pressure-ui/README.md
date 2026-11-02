# SessionPressure GPUI client (preview)

Secondary desktop client for SessionPressure. It lives next to the Swift app
in this repository:

- Swift (macOS menubar): `apps/SessionPressure`
- GPUI (this crate): `apps/pressure-ui`

Both are thin shells over `session-pressure --json`. Neither samples the host.

## Why this exists

The Swift client is the signed macOS menubar app. This GPUI client is a
keyboard-first, pane-for-pane operator surface with a richer work inspector:

- Always-on status strip (level, memory, CPU, capacity, queue)
- Command palette (`⌘K`) for every pane and mutation
- Work Queue is **three columns**: queue · live inspect · output tail
- Live `work inspect` output, not a “look in the terminal” footnote
- Confirmed cancel / reschedule / profile apply
- Dark GPU UI on GPUI + `gpui-component`

## Panes (parity with Swift)

| Shortcut | Pane | What is better |
|---|---|---|
| ⌘1 | Overview | Doctor + reasons + consumers + trees on one page |
| ⌘2 | Agent Trees | Filterable list |
| ⌘3 | Disk Writes | Same board projection |
| ⌘4 | Storage | Volume + idle candidates together |
| ⌘5 | Work Queue | 3-pane inspect + live output |
| ⌘6 | Policy | One-click profiles behind confirm |
| ⌘7 | Monitor | LaunchAgent / artifact / recovery |
| ⌘8 | Telemetry | Newest-first event stream |

## Run

```bash
# from the repository root
make -C apps/pressure-ui run

# or
cd apps/pressure-ui
cargo run -p session-pressure-ui --release
```

Needs `session-pressure` on `PATH` or `SESSION_PRESSURE_BIN`. First build
fetches Zed's GPUI from git and compiles it — budget minutes, not seconds.

## Test

```bash
cargo test -p pressure-core
```

`pressure-core` has no GPUI dependency. It decodes the same scrubbed
`board-full.json` fixture the Swift tests use.
