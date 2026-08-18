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

## Closed set

Factory launch hooks, Toolguard, and nicos-only reclaim stay in nicos-tools. The extract pageskein reclaim path returns `ErrNotAvailable`.

Canonical boundary: [OPEN-CORE.md](../OPEN-CORE.md). Desktop operator guide: [apps/SessionPressure/docs/operator-guide.md](../apps/SessionPressure/docs/operator-guide.md).
