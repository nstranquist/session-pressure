# Recorded runner-capacity contract

`runner-capacity.json` is real output captured on the Mac at 2026-11-15
(UTC) with a locally built candidate:

```text
session-pressure --json remote capacity --window 24h
```

It read the installed two-host inventory (runner-3 active, runner-1
reserve) over SSH. The only edit replaces the Mac's device name inside caller
names with `example-mac`. runner-1 runs an older runner build without
`host_samples`, so its `samples_present` is false; the contract test relies on
that to cover the no-samples path. The network series is empty because this
was the first local NIC sample.
