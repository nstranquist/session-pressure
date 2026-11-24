# Verified release and acceptance

## Single-command post-land promote (clean HEAD, headless)

Promote the CLI bundle (`~/.local/bin/session-pressure`), LaunchAgent resident
helper, and signed `/Applications/NDev Pressure.app` together from a clean clone
of committed `HEAD` without stealing desktop focus:

```sh
# Preview authorities and plan without changing installed artifacts
make release-promote-dry-run

# Promote from a pre-built Hetzner darwin/arm64 bundle + clean HEAD signed app (preferred on Mac)
make release-promote RELEASE_BUNDLE=/absolute/export

# Or build and promote everything directly from a clean temporary clone of HEAD
make release-promote
```

`session-pressure-release promote` captures both `--expected-current` (CLI
revision) and `--expected-app` (app bundle digest) before building, clones
committed `HEAD` into a clean temporary directory outside the checkout so dirty
parallel edits never contaminate a release, installs the CLI and resident
helper under their kernel locks, builds and installs the signed macOS app
headlessly (no `open`, no focus steal), rebuilds the recorded front door
(below), and verifies `session-pressure --json doctor` reports zero revision
mismatch and zero commit lag.

### Front door

A host wrapper that execs the CLI and also links this module in-process (a
"front door", on the primary Mac `ndev`) has its own build. Promotion
rebuilds it too:

1. The wrapper names itself on every call it forwards
   (`SESSION_PRESSURE_FRONT_DOOR`, package `frontdoor`): the argv that reaches
   the CLI through it and the argv that rebuilds it from committed source.
   The CLI records that in `front-door.json` in its state directory, and
   `doctor` reports it as `front_door`.
2. The doctor check runs the installed CLI directly, without the wrapper
   variables it may have inherited. An inherited front-door revision names
   whatever wrapped the promotion, not what is installed.
3. When `doctor` reports a front door, promotion runs its rebuild, then runs
   `doctor` through it and requires that it links the promoted revision and
   that the CLI, front door and helper agree. The receipt's `front_door`
   records the result.

A front door whose executable is gone, an unreadable record, or a `--ref`
other than `HEAD` (a front door builds the sibling checkout's `HEAD`) is
reported in `front_door.status` and does not fail the promotion.
`--skip-front-door` leaves the front door alone. `--skip-doctor` also skips
it, because doctor is what names it. A host with no front door is
unaffected. To forget a removed front door, delete `front-door.json`.

Pass `--verify-lifecycle` (`RELEASE_EXTRA_FLAGS="--verify-lifecycle"`) to also
run `scripts/verify-installed-lifecycle.py` in isolated private state after
promotion. `session-pressure doctor` checks the installed revision against the
local `session-pressure` git `HEAD` (override with `SESSION_PRESSURE_SOURCE_REPO`,
or disable with `SESSION_PRESSURE_SOURCE_REPO=off`) and stays silent on packaged
or non-git hosts.

## Manual runner build and guarded install details

Build Go binaries on the inventory-selected active runner (currently
runner-3; runner-2 is retired) from a clean committed checkout. Keep runner-1 as explicit
reserve; use the canonical remote verification wrapper to bind one host:

```text
go run ./scripts/release build --platform darwin/arm64 --out /absolute/export
```

For a native Darwin cross-build from a Linux runner, compile the release driver
for the runner first with `CGO_ENABLED=0 GOFLAGS= go build -o /absolute/release-builder
./scripts/release`. Then run that driver from the clean source checkout with
`CC=/srv/build/opt/pressure-darwin-clang` and
`GOFLAGS="'-ldflags=-s -w -linkmode=external'"`. The inner quotes keep all linker
options in one Go flag token; quoting only the value after `-ldflags=` splits
`-linkmode` into an invalid Go command flag. Keep the driver and release output
outside the source checkout so its clean-source check remains meaningful.

Before admitting a sealed bundle to the runner, run `git bundle verify BUNDLE`
and `git bundle list-heads BUNDLE`. Clone its named ref explicitly with
`git clone --branch main BUNDLE DESTINATION`, then check that `git rev-parse HEAD`
matches the intended revision and that the clone is clean. A bundle with only
`refs/heads/main` does not define the remote's default HEAD.

Before the remote build, read `.session-pressure-releases/current` under the
install prefix. Retain that observed revision with the job. On first install,
use `none`. Copy the bundle to the Mac, then run its installer:

```text
session-pressure-release install --out /absolute/export --prefix /Users/user/.local/bin --expected-current OBSERVED_REVISION
```

The installer verifies all six binaries against their SHA-256 values, platform,
module and clean Git revision. Under its kernel lock, it requires the current
revision to match `--expected-current`. A stale job cannot overwrite a later
promotion. It also rejects an older build timestamp unless `--allow-rollback`
is explicit. Build time is a downgrade fence; it does not establish Git ancestry.
If the authority changed, review the newer release before preparing a new intent.

The installer probes the candidate before it changes the shared `current` link.
A failed promotion restores the prior link and first-install files. Receipts
record the expected authority and rollback flag. Preserved prior files remain in
`.session-pressure-releases` under the prefix. For an intended rollback, run the
current installer against the preserved bundle with the actual current revision
and `--allow-rollback`. Do not rewrite a running executable. Old preserved
installer executables predate this guard; always use the current installer.

The native Makefile uses `install-app` to stage and verify a signed app before
promotion. It preserves the prior app in private history under
`~/.nicos-dev/session-pressure/app-installs/<destination-sha256>/rollbacks`.
Installation receipts are committed to owner-only `history.sqlite` in that
directory. Concurrent app and CLI installations use kernel locks.
Before building an app, capture `session-pressure-release app-status --app
"/Applications/NDev Pressure.app" --digest-only`. Pass that digest to
`install-app --expected-current DIGEST`. First installation uses `none`.
The digest binds the full bundle, including the privileged helper and resources.
The installer checks it under the app lock and again before promotion. It also
rejects a lower SemVer version or, at the same version, a lower numeric build.
An intentional rollback needs the actual current digest and `--allow-rollback`.
Both native install targets capture the authority before their build. A signed
older app alone is not authority to overwrite the current installation.

Configure an existing external archive directory once with
`session-pressure-release app-maintain --archive-root "/Volumes/X10 Pro/NDev Pressure app history"`.
This imports legacy receipts without changing their bytes, archives older
rollbacks and retains the newest one locally. It verifies every copy and a
consistent external SQLite snapshot before removing originals. Subsequent app
installs reuse this setting. If the disk is unavailable or verification fails,
installation retains extra local copies and reports maintenance pending;
mount the disk and run `session-pressure-release app-maintain` to retry.
It never deletes external history. A legacy empty `.install.lock` beside the
app is intentionally retained for older-installer coordination. See the
[migration decisions and proof](../../corpus/app-install-history.md).

Run installed integration checks with `PRESSURE_CONTRACT_BIN` set to the
absolute installed CLI, then `swift test --filter InstalledContractTests` from
`apps/SessionPressure`. This exercises real identity and SMART payloads and a
temporary owner-only Unix API server. It does not require elevated privileges.

Run the installed release helper's `observe --binary /absolute/session-pressure
--out /absolute/evidence.json --duration 72h`. Keep it under a user launch agent
for a durable run. The JSON records 100 warm reads for status, board and rich
board; p95 budgets are 250, 500 and 1,000 ms. It then checks identity and status
each minute (allowed interval: 10 seconds to 5 minutes). The monitoring
window and its deadline start after warm-up; warm-up time does not count toward
the soak. The collector binds the CLI SHA-256 and the whole app bundle identity
at startup, checks both each cycle, and checks the process executable path.
Any observed artifact change remains a health failure for this run. Set `--app`
when the bundle is outside `/Applications/NDev Pressure.app`.
A successful JSON read alone does not pass health: the resident
must be installed, loaded, verified, healthy, fresh and within its resource
budget, with persisted policy. Resident PID changes count as failures.
`health_passed` requires continuous sampling and zero failures.
App CPU and peak RSS use the same PID over at least 30 minutes;
the budget is 1% CPU and 128 MiB RSS. An app restart or failed process sample starts a new app window. CPU counters
that decrease invalidate the sample and start a fresh window on recovery.
`app_restarts` preserves observed PID changes.
`app_sample_ok` distinguishes a current sample from a missing one.
`app_sample_failures`, `last_app_sample_error` and `last_app_sample_failure`
preserve diagnostic history after recovery. Missing samples clear current CPU,
RSS and elapsed values; they cannot pass the idle gate. Process discovery must
find exactly one app PID. CPU parsing accepts long-running BSD process times.
Use a parked app for the idle budget. Interactive use is an activity sample.
The 24/72-hour flags require actual elapsed time and continuous sampling, with
no gap greater than twice the sample interval plus 30 seconds. These flags
describe elapsed observation. Use `accepted_24h` and `accepted_72h` for the
combined verdict: the full monitored interval, all three read gates, healthy
resident, bound artifacts, app budget, and zero health/sample failures or
resident/app restarts must all pass. A recovered 30-minute app window alone
cannot erase an earlier failure from the combined verdict.
Failures remain counted. Each run requires an unused output path. A launch
agent restart refuses to overwrite an interrupted run's evidence. Review that
run and choose a new dated path to begin a new continuous window.
The output has constant size; collection ends at the requested deadline.

Automatic shedding stays off. Calibration promotion still requires its replay
gate. A passing test does not establish an installed release or elapsed soak.

Darwin bundles require CGO for the native NVMe SMART reader. Cross builds must
set `CC` to a Darwin target compiler and provide its SDK. Explicit
`CGO_ENABLED=0` fails before building. Promotion checks this requirement in
both the manifest and each binary's Go build information. A runnable portable
binary does not qualify as a complete Darwin release.

The runner can compile `go build -o /outside/checkout/release-cc ./scripts/release-cc`
and use that adapter as `CC`, with `PRESSURE_TARGET_CC` naming the real compiler.
It isolates compiler invocations in temporary directories and preserves relative
input, include and explicit output paths. Implicit stdin compile-probe output
goes to `/dev/null`. This prevents Zig's `-.o` probe artifact from making
later binaries report modified source. Build checks source cleanliness after
each target and stops before producing a promotable bundle if it changes.
The preferred Darwin profile is LLVM clang 18 with external linking:
`CC=/srv/build/opt/pressure-darwin-clang` and
`GOFLAGS="'-ldflags=-s -w -linkmode=external'"`. The inner quotes enclose the
complete Go flag token, including `-ldflags=`. Quotes around only its value do
not work. Check the exact environment with `go list -m` before remote admission;
this parses the flags without compiling. Record the actual compiler, linker
and SDK identity in the build evidence. The older Zig/internal profile was
tested for the small standalone product, but it broke SQLite parameter binding
in the factory binary despite passing a version probe. Do not use that profile
for `ndev` or a delivery producer. Native acceptance must exercise filtered
SQLite queries and ingestion when a binary includes that dependency.

Compile the release driver for the Linux host before applying the target
compiler environment. For example, use `CGO_ENABLED=0 GOFLAGS= go build -o
/absolute/release-builder ./scripts/release`, then run that driver with the
Darwin `CC` and `GOFLAGS` above. Applying the Darwin compiler to `go run`
can make the host build select Linux C sources with a macOS SDK. The same
separation applies to the factory export driver.


The factory compatibility target uses the same installer contract. After building
the native product bundle on the gate runner, invoke it with the current revision
captured before that build:

```sh
make -C ~/dev/nicos-tools/nicos-dev promote-session-pressure \
  SESSION_PRESSURE_RELEASE_DIR=/absolute/export \
  SESSION_PRESSURE_EXPECTED_CURRENT=OBSERVED_REVISION
```

This target installs the complete product bundle and restarts the resident.
The factory `ndev` front door still needs its own committed native export and
atomic publish; its embedded product stamp is checked by `artifact status`.
Do not use an older factory target that replaces individual product artifacts.
`SESSION_PRESSURE_INSTALL_PREFIX` defaults to `~/.local/bin`, the authoritative
standalone location. The target does not run a heavy Go build on the Mac.
