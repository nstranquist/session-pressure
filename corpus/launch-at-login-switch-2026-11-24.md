# NDev Pressure login switch, 2026-11-24 (WORK-1776)

- **Work item**: `WORK-1776`. Parent: `WORK-NDEV-64` (`ndev mac menubar`, the registry of Nico's menu bar apps).
- **Commit**: `4e221f1` feat(app): add --launch-at-login on|off|status switch.

## Why

`ndev mac menubar` shows whether each menu bar app starts at login. It used to read the login items with `sfltool dumpbtm`, and that asks macOS for an admin password on every read. With `sfltool` removed, ndev can read an app's own login item only through the app. NDev Pressure has login item code (`SMAppService.mainApp`, behind Settings > Launch at login), so it read as `unknown` until it took the switch.

## What changed

- `Sources/NDevPressure/LaunchAtLoginSwitch.swift` is the process entry (`NDevPressureEntry`). With `--launch-at-login on|off|status` it acts on `SMAppService.mainApp`, prints `launch-at-login: <enabled|not-registered|requires-approval|not-found|unknown> (<bundle path>)` and exits before any UI starts. Exit codes: 0 ok, 1 the service call failed, 2 bad usage. Every other launch runs `NDevPressureApp.main()`.
- `NDevPressureApp` lost its `@main` attribute.
- `off` also unregisters an item in `requires-approval`, so a login item that Nico turned off in System Settings can be cleared.
- The switch uses the same service as the Settings toggle, so both stay in sync.
- Other apps copy this file: it is the reference for the switch contract (`nicos-tools/docs/human/mac-menubar-apps.html`).

## Proof

- `swift test` covers the state names, the line format and the usage exit code (`Tests/NDevPressureCoreTests/LaunchAtLoginSwitchTests.swift`).
- Installed with `make install-headless` (no relaunch). The installer printed "backup maintenance pending. Mount the archive disk and run session-pressure-release app-maintain". That is not part of this change and was left for the archive disk owner.
- Installed binary on the primary Mac: `--launch-at-login status` takes 26-28 ms warm (5 runs) and 277 ms cold.
- `ndev mac menubar login ndev-pressure off`, `on` (via `app`), `off`: each state change printed as expected, and the start state (off) was restored. authd logged no `system.privilege.admin` authorization.
