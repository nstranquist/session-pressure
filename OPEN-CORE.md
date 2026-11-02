# OPEN-CORE boundary — SessionPressure

SessionPressure is open-core. The local host coordinator in the extracted
repository is open source (Apache-2.0). Factory adapters and any later hosted
or notarized paid layer stay proprietary.

**Open (extracted repository / local product):**

- host sampler for CPU, memory, swap, and fail-open thermal or low-power probes
- policy and the four work styles `balanced`, `throughput`, `interactive`, `observe`
- weighted work coordinator, leases, evaluate, and replay
- agent identity catalog for generic Claude, Codex, Grok, and Kimi install shapes
- internal-SSD write watchdog and likely-writer attribution
- doctor, self-test, audit, and board JSON
- resident helper and LaunchAgent install
- standalone CLI
- local opt-in control API on an owner-only Unix socket or explicit loopback
- thin native macOS desktop client
- generic storage capacity plus a provider interface

**Closed (stays in nicos-tools):**

- `ndev session pressure` compatibility wrapper
- Toolguard heavy-work classify and wrap
- agent-host launch gates for claude, grok, codex, kimi, and session exec
- nicos-specific reclaim providers (pageskein, nicos caches, factory paths)
- catalog and operation-contract generation
- Codex broker, Agent Studio, and other factory consumers
- notarized or paid binary distribution, if that layer is sold

**The line, in one sentence:** anything a developer runs on their own Mac to
admit and coordinate local agent work is open. Factory launch hooks, Toolguard,
and nicos-only reclaim stay closed.

**Optional integration points (open code, closed counterpart):** a few open
code paths call out to the proprietary `ndev` CLI when it is installed. They
are integration points, not dependencies. Each one fails closed or degrades to
a plain result when `ndev` is absent, and none is needed for admission,
sampling, or the work coordinator.

| Surface | What the open code does | Without `ndev` |
|---|---|---|
| Scratchpad capture | `work handoff` runs `ndev --json scratchpad capture --target agents` to file the brief in a Scratchpad "Agents" note. | The brief is still returned. The failure is reported in `scratchpad_error`. Nothing else changes. |
| Schedule origin | Leases and `work inspect` read `NDEV_SCHEDULE_ID`, `NDEV_SCHEDULE_RUN_ID`, and `NDEV_SCHEDULE_TRIGGER` from the lease environment to attribute scheduled work. | The variables are unset, so the origin is reported as agent or manual. |
| Reschedule | `work reschedule --confirm` checks that `ndev` is on `PATH`, cancels the scheduled lease, then runs `ndev schedule run-now <id>`. | The command is refused before anything is cancelled, and the lease keeps running. |
| Binary lookup aliases | `NDEV_SESSION_PRESSURE_HOME`, `NDEV_PRESSURE_BIN`, `NDEV_PRESSURE_API_BIN`, `NDEV_BIN` (desktop clients), and the `ndev-pressure*` executable names are accepted as compatibility aliases for installs created by the `ndev` wrapper. | The `SESSION_PRESSURE_*` variables and `session-pressure*` names are used. |
| Control API runner | `session-pressure-api` runs actions as `<bin> --json session pressure …`, where `<bin>` is `--ndev-bin`, else `ndev` or `ndev-go` on `PATH`. The product CLI accepts that prefix, so `--ndev-bin` can point at `session-pressure`. | With no runner found, actions return exit 127 and an explicit error. |

**Public Go packages:** `sessionpressure`, `sessionpressurecmd`,
`sessionpressurecontrol`, `sessionpressurecleanup`, and `pkg/`. Leftover
`internal/atomicfile`, `internal/jsonl`, `internal/telemetry`, and
`internal/orb` are factory copies that shipped in the extract. They are not
the product contract. Do not import them from another module.

Product names in this tree: `session-pressure` (CLI), `session-pressure-helper`
(LaunchAgent), `session-pressure-api` (local control plane). The nicos-tools
wrapper remains `ndev session pressure` and execs `ndev-pressure`.
