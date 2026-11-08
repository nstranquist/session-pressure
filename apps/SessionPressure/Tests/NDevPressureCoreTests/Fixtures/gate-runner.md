# Recorded gate-runner contract

`gate-runner.json` is a scrubbed subset of real output captured on the Mac at
2026-11-08T21:38:55 UTC with:

```text
pressure-read-candidate-2130 --json remote inspect
```

The private candidate was built on the canonical Hetzner runner from the sealed
`review-gaterunner-source-2130` snapshot with the reviewed read API. It is a
portable read-contract executable, not a promoted native
release. Its targeted Go race suite passed before capture. The snapshot and
unmodified recording remain in the private acceptance evidence directory.

The actual response contained 93 delegate rows, 668 job receipts, configuration
and live remote health. The fixture retains five representative rows and three
jobs, including true, false and unknown recorded agreement, an environment
failure and a local fallback. Paths, repository and caller names, delivery IDs,
commands and failure detail are scrubbed. Dates, optional/null values, durations,
health values, JSON field names and the original comparison authority remain.
The raw recording SHA-256 is recorded in the private capture receipt.

Re-record from the installed CLI after promotion. Do not synthesize a CLI JSON
fixture or use a portable candidate as installed/native acceptance evidence.
