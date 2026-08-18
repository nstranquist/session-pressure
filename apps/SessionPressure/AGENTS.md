# NDev Pressure — agent notes

## What this is

Native SwiftUI desktop + menu bar UI for **`ndev session pressure`**. Thin client
only — do not reimplement sampling, policy, or relief in Swift.

## Do

- Shell out via `NDevPressureClient` (`ndev --json session pressure …`)
- Keep tooltip copy in `NDevPressureCore/HelpCopy.swift` so UI and docs stay aligned
- Prefer Codable models that tolerate missing optional JSON fields
- Confirm destructive / protection-mode mutations in the main UI; compact surfaces may navigate to those controls but must not bypass confirmation

## Don't

- Add Electron/Tauri/Node or large asset packs
- Persist a second telemetry database
- Display work-lease command arguments (privacy contract)
- Bypass `ndev` for policy/monitor mutations
- Spawn Grok, Codex, SwiftTerm, a PTY, or `ndev session exec` from Storage / Begin safe reclaim
- Run live `storage plan` or `storage apply --apply` against the operator volume from tests

## Verify

```bash
make test
make build
# optional: make run
```

**Do not hand-write CLI fixtures.** Model tests decode a scrubbed recording of
real output at `Tests/NDevPressureCoreTests/Fixtures/board-full.json`. Two
hand-written fixtures diverged from what `ndev` actually emits while this
surface was built — `coverage.limitations` is a list in the full projection but
a count in the compact one, and coverage surfaces carry `id`, not `name`. Both
mistakes type-check and both throw at runtime. Re-record with:

```bash
ndev --json session pressure board --full --live --include all
```

then scrub host paths and session ids before committing.

## Catalog

- Extract product: `product.session-pressure` (`~/tools/session-pressure`)
- Compatibility wrapper still listed as `product.ndev-pressure` in nicos-tools
- Do not implement the same UI a second time under `nicos-tools/apps/desktop/ndev-pressure`
