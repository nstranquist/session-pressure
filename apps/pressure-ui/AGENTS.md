# Session Pressure GPUI — agent notes

Secondary UI. Thin JSON client. Do not reimplement sampling.

- CLI: `session-pressure --json session pressure …`
- Models live in `crates/pressure-core` so tests do not pull Zed/GPUI
- UI crate: `crates/session-pressure-ui`
- Do not add this under nicos-tools
- Mutations that destroy processes require the in-app confirm overlay
- Live argv/output is inspect-only; never persist into work-events
