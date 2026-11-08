# Verified release and acceptance

Build Go binaries on gaterunner-1 from a clean committed checkout:

```text
go run ./scripts/release build --platform darwin/arm64 --out /absolute/export
```

Copy that bundle to the Mac. Run its `session-pressure-release install --out
/absolute/export --prefix /Users/nicostran/.local/bin`. The installer verifies
all five binaries against their SHA-256 values, platform, module and clean Git
revision. It probes the candidate before it changes the shared `current` link.
A failed promotion restores the prior link and first-install files. Receipts
and preserved prior files are in `.session-pressure-releases` under the prefix.
To roll back an existing managed release, atomically replace `current` with
the receipt's `previous_release`. Do not rewrite a running executable.

The native Makefile uses `install-app` to stage and verify a signed app before
promotion. It preserves the prior app beside the destination with `.rollback-`
and a UTC timestamp. Concurrent app and CLI installations use kernel locks.

Run installed integration checks with `PRESSURE_CONTRACT_BIN` set to the
absolute installed CLI, then `swift test --filter InstalledContractTests` from
`apps/SessionPressure`. This exercises real identity and SMART payloads and a
temporary owner-only Unix API server. It does not require elevated privileges.

Run the installed release helper's `observe --binary /absolute/session-pressure
--out /absolute/evidence.json --duration 72h`. Keep it under a user launch agent
for a durable run. The JSON records 100 warm reads for status, board and rich
board; p95 budgets are 250, 500 and 1,000 ms. It then checks identity and status
each minute. App CPU and peak RSS use the same PID over at least 30 minutes;
the budget is 1% CPU and 128 MiB RSS. An app restart starts a new app window.
Use a parked app for the idle budget. Interactive use is an activity sample.
The 24/72-hour flags require actual elapsed time. Failures remain counted.
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
It sends implicit stdin compile-probe output to `/dev/null`; explicit compiler
outputs are unchanged. This prevents Zig's `-.o` probe artifact from making
later binaries report modified source. Build checks source cleanliness after
each target and stops before producing a promotable bundle if it changes.
For the current Zig toolchain, use `GOFLAGS=-ldflags=-linkmode=internal`.
