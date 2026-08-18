# Storage reclaim UI — keep / defer

Verdicts for the SessionPressure desktop recommendation. Later work should
not re-litigate deferred items without a new benefit.

Live host reclaim is **deferred**: the operator is taking care of storage
elsewhere. This slice must not run `storage plan` or `storage apply --apply`
against the operator volume.

| Item | Verdict | Benefit |
| --- | --- | --- |
| Storage pane | **Keep** | The app already shows storage-critical bytes and has no reclaim surface. |
| Begin safe reclaim | **Keep** | Operator-confirmed typed apply is the only safe one-click path. |
| Typed `auto_safe` apply in the control plane | **Keep** | Preview/approve currently only knows named `--provider`. |
| Storage-policy enable/observe in the app | **Keep** | `--auto-safe --apply` is refused while enforcement is off. |
| Live typed receipt (CLI output, not a PTY) | **Keep** | Apply can run for minutes; the operator needs that command's own output. |
| Honest blocked / factory-only reasons | **Keep** | Extract `browser-dead-profiles` is pageskein-blocked; a green button would lie. |
| Unix-socket API client | **Defer** | CLI fallback already works; not required to recognize Begin safe reclaim. |
| `/v1/events` SSE board stream | **Defer** | Efficiency only; not required for typed reclaim. |
| Grok/Codex read-only “Explain this plan” | **Defer** | Narrator is optional; mutation stays typed. |
| Live agent PTY / SwiftTerm / `session exec` | **Defer** | Control plane forbids arbitrary argv; JTBD is without tailing a terminal. |
| Rename NDev Pressure branding | **Defer** | Does not change reclaim behavior. |
| Catalog reindex | **Defer** | Not this slice. |
| Live host reclaim (`plan` / `--apply` on this Mac) | **Defer** | Operator is reclaiming storage elsewhere. |

## Closeout (2026-08-14)

Shipped in the extract desktop: Storage ⌘4, Idle trees tab, typed preview/confirm, factory/pnpm skip, Policy Keyboard + App Shortcuts remap, live board capacity, nil-vs-off storage policy copy, CLI as apply authority when policy is not loaded.

`make test` / `make publish-ready` now include `./sessionpressurecontrol` and `apps/SessionPressure` swift tests. App installed to `/Applications/NDev Pressure.app`.

Human-gated leftovers stay deferred: notarization, catalog reindex, branding rename, live host `--apply`.
