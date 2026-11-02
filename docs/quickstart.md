# Quick start

Build the CLI, then run doctor. Doctor is observe-only.

```bash
make init
./bin/session-pressure --json doctor
```

Write observe-only policy and take one sample:

```bash
./bin/session-pressure policy init
./bin/session-pressure --json snapshot
```

Install the resident monitor. Wait until `resident_normal_samples` is at least
four. Then test the observe-only installed path:

```bash
./bin/session-pressure monitor install
./bin/session-pressure --json status
./bin/session-pressure --json self-test --scope monitor --wait 10m
```

Monitor scope does not enable launch admission or automatic relief. Run the
default operator-scope self-test only after an approved policy enables both
protections.

Default state directory is `~/.nicos-dev/session-pressure`. Override it with `SESSION_PRESSURE_HOME` or `NDEV_SESSION_PRESSURE_HOME`.

Read internal SSD lifetime wear (observe-only). This is NVMe SMART, not the
15-minute write-rate window:

```bash
./bin/session-pressure --json io health
./bin/session-pressure --json io history --since 168h --limit 48 --full
```

`io health` writes `nvme-smart-latest.json` and appends
`nvme-smart-health.jsonl` when percent used or host writes change.
