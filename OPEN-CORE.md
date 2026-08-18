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

Product names in this tree: `session-pressure` (CLI), `session-pressure-helper`
(LaunchAgent), `session-pressure-api` (local control plane). The nicos-tools
wrapper remains `ndev session pressure` and execs `ndev-pressure`.
