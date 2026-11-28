# Recorded disk-write contract

`io-status.json`, `io-history.json` and `io-health.json` are real output
captured on the primary Mac on 2026-11-24 (UTC) with the installed helper:

```text
ndev --json session pressure io status --live --full
ndev --json session pressure io history --since 24h --limit 20
ndev --json session pressure io health
```

These are the three reads behind the Disk Writes page (`refreshDiskWrites`
and "Read SMART"). The only edits: `io-health.json` has `data_dir` set to
`/redacted` and the SSD `serial` set to `redacted`. Keys are sorted.
Re-record with the same three commands and the same two edits.

`io-status.json` was re-recorded on 2026-11-24 at 22:10 UTC,
when writer names became full program names instead of the kernel's
16-character cut. It came from `session-pressure --json io status --live
--full` built from that change. Of eight back-to-back samples it is the one
with the most writers (5). It includes `Google_Chrome_Helper_(Aperitif)`,
which the kernel cuts to `Google_Chrome_He`. A quiet Mac gives an empty or
short writer list, so take several samples and keep the busiest.
