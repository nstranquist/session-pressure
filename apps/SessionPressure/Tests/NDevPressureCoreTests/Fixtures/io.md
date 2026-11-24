# Recorded disk-write contract

`io-status.json`, `io-history.json` and `io-health.json` are real output
captured on the primary Mac on 2026-11-24 (UTC) with the installed helper:

```text
ndev --json session pressure io status --live --full
ndev --json session pressure io history --since 24h --limit 20
ndev --json session pressure io health
```

These are the three reads behind the Disk Writes page (`refreshDiskWrites`
and "Read SMART"). The sample caught a full-state backup writing about
260 MB/s, so the state is `high` and the writer list is populated. The only
edits: `io-health.json` has `data_dir` set to `/redacted` and the SSD
`serial` set to `redacted`. Keys are sorted. Re-record with the same three
commands and the same two edits.
