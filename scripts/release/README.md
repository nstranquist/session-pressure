# Verified release and acceptance

Build Go binaries on gaterunner-1 from a clean committed checkout:

```text
go run ./scripts/release build --platform darwin/arm64 --out /absolute/export
```

Before the remote build, read `.session-pressure-releases/current` under the
install prefix. Retain that observed revision with the job. On first install,
use `none`. Copy the bundle to the Mac, then run its installer:

```text
session-pressure-release install --out /absolute/export --prefix /Users/nicostran/.local/bin --expected-current OBSERVED_REVISION
```

The installer verifies all five binaries against their SHA-256 values, platform,
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
promotion. It preserves the prior app beside the destination with `.rollback-`
and a UTC timestamp. Concurrent app and CLI installations use kernel locks.
Before building an app, capture `session-pressure-release app-status --app
"/Applications/NDev Pressure.app" --digest-only`. Pass that digest to
`install-app --expected-current DIGEST`. First installation uses `none`.
The digest binds the full bundle, including the privileged helper and resources.
The installer checks it under the app lock and again before promotion. It also
rejects a lower SemVer version or, at the same version, a lower numeric build.
An intentional rollback needs the actual current digest and `--allow-rollback`.
Both native install targets capture the authority before their build. A signed
older app alone is not authority to overwrite the current installation.

Run installed integration checks with `PRESSURE_CONTRACT_BIN` set to the
absolute installed CLI, then `swift test --filter InstalledContractTests` from
`apps/SessionPressure`. This exercises real identity and SMART payloads and a
temporary owner-only Unix API server. It does not require elevated privileges.

Run the installed release helper's `observe --binary /absolute/session-pressure
--out /absolute/evidence.json --duration 72h`. Keep it under a user launch agent
for a durable run. The JSON records 100 warm reads for status, board and rich
board; p95 budgets are 250, 500 and 1,000 ms. It then checks identity and status
each minute. A successful JSON read alone does not pass health: the resident
must be installed, loaded, verified, healthy, fresh and within its resource
budget, with persisted policy. Resident PID changes count as failures.
`health_passed` requires continuous sampling and zero failures.
App CPU and peak RSS use the same PID over at least 30 minutes;
the budget is 1% CPU and 128 MiB RSS. An app restart starts a new app window.
Use a parked app for the idle budget. Interactive use is an activity sample.
The 24/72-hour flags require actual elapsed time and continuous sampling, with
no gap greater than twice the sample interval plus 30 seconds. These flags
describe elapsed observation; also check failures and all budget results.
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
For the current Zig toolchain, use `GOFLAGS=-ldflags=-linkmode=internal`.


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
