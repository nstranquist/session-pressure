# Post-land release pipeline and doctor source-lag reporting (2026-11-24)

Work item: `WORK-SP-11`.

## Problem

Landed commits on `main` could drift for days ahead of the installed
`~/.local/bin/session-pressure` CLI, LaunchAgent resident helper, and
`/Applications/NDev Pressure.app`:

1. `session-pressure doctor` checked agreement among executing binaries
   (`cli`, `front_door`, `installed_helper`), so a consistently stale install
   reported `mismatch: false` with no signal that `HEAD` had moved ahead.
2. Promoting CLI, resident helper, and signed app required multiple manual
   commands, and `scripts/release build` refused to run whenever parallel
   agents left unrelated edits in the main checkout.

## Solution

1. **Doctor source-vs-installed commit lag (`sessionpressure/source_lag.go`):**
   - `DiscoverSourceRepo` locates a local git checkout whose `go.mod` declares
     `module github.com/nstranquist/session-pressure` (or honors
     `SESSION_PRESSURE_SOURCE_REPO`, with `off`/`none` to disable). It never
     mistakes another repository such as `nicos-tools` for `session-pressure`.
   - `InspectSourceLagAtRepo` compares the installed revision against git
     `HEAD` (`current`, `behind`, `ahead`, `diverged`, `dirty`, `unknown`,
     `unavailable`), returning commit counts and commit timestamps.
   - `PressureDoctor` surfaces `source_lag` in JSON and concise text output,
     emitting a warning and remediation (`make -C <repo> release-promote`)
     only when `behind` or `diverged`. Packaged/non-git hosts, shallow clones,
     and `ahead` installs produce zero false alarms, and lag never flips
     `doctor.ok` to false.
2. **Single-command clean-HEAD headless promotion (`scripts/release/promote.go`, `Makefile`):**
   - `make release-promote` (and `make release-promote-dry-run`) invokes
     `scripts/release promote`.
   - Captures both CLI `--expected-current` and app `--expected-app`
     authorities before any build so concurrent promotions fail closed under
     kernel locks.
   - Clones committed `HEAD` into a clean temporary directory outside the
     checkout so dirty working-tree edits never leak into a release.
   - Supports `--bundle <dir>` for Hetzner-built `darwin/arm64` bundles as
     well as standalone clean-clone builds.
   - Promotes CLI, runs `session-pressure --json monitor install` for the
     LaunchAgent helper, builds and verifies the signed `.app`, installs it
     headlessly via `installApp` (no `open`, no focus steal), and verifies
     `doctor` reports zero revision mismatch and zero commits behind `HEAD`.
