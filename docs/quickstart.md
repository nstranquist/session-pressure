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

Default state directory is `~/.nicos-dev/session-pressure`. Override it with `SESSION_PRESSURE_HOME` or `NDEV_SESSION_PRESSURE_HOME`.
