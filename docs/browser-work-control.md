# Browser pause and remote work

In the desktop app, open **Work Queue**. **Pause browser work** blocks new
agent browser work and stops same-user automated Chrome roots. Your normal
Chrome windows remain open. The pause stays on until an operator changes it.

**Automatic recovery** enables the host load gate. Missing, stale, or invalid
CPU evidence blocks browser work. After a load stop, two distinct fresh resident
samples at or below the CPU recovery threshold release the gate. Repeated reads
of one cached sample do not count as recovery. The default CPU red threshold is
95%; the recovery threshold is 80%.

With either mode enabled, browser work has one execution slot and a 120-second
deadline. The resident also stops excess or expired automated Chrome roots.
The agent toolguard hook applies the same pause to supported browser MCP tools,
captures, Playwright, Puppeteer, SnapRef, and `ndev browser` commands. This is
agent hook and process enforcement, not an operating-system sandbox.

```bash
ndev session pressure browser pause
ndev session pressure browser auto
ndev session pressure browser status
ndev session pressure remote enable
ndev session pressure remote status
ndev session pressure work run --class test -- go test ./...
```

The enabled remote route sends eligible direct `go test`, `go build`, and
`go vet` commands through the work wrapper to gaterunner-1 before Mac admission.
Remote jobs share one host slot, two Go workers, a 3 GiB Go memory target, and a
30-minute deadline. A remote failure never silently retries on the Mac.
Mac-specific compiler profiles and external local-file arguments keep local
admission and report a reason. Explicit executable builds require
`CGO_ENABLED=0` and `-o`; their Darwin arm64 output is fetched back atomically.

Uploads preserve local replacement modules and repository fixtures, including
pending unignored files. They exclude Git history, ignored build artifacts,
credentials, symlinks, and files above 20 MiB, with a 512 MiB total limit.
The private `work-control-events.jsonl` log records browser stops, remote
declines, exit codes, and durations in two bounded 4 MiB segments.
