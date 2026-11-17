# Operator configuration

Host names, addresses and compiler pins are operator data. They live in the
session-pressure data directory (`~/.nicos-dev/session-pressure/`) on each
machine, never in this repository. The files here are examples with
placeholder hosts (`runner-3`, `192.0.2.3`, `example-macbook-pro`).

| File in the data directory | Example | Purpose |
|---|---|---|
| `remote-work.json` | `remote-work.example.json` | Remote Go and Node work: the runner `host` and `user`, or a `fleet` inventory path; `darwin_compiler_sha256` maps each runner endpoint (name, alias or address, lower case) to its reviewed Darwin cross-compiler sha256. A Darwin cgo build runs only on a listed endpoint. With `enabled` true, `host` or `fleet` is required. |
| `memory-levels.json` | `memory-levels.example.json` | Optional full replacement of the embedded memory levels (`sessionpressure/memory_levels.json`). `hosts` holds per-host overrides keyed by the normalized host name (`scutil --get LocalHostName`, lower case). |

Copy an example, replace the placeholders with your values, and keep the
copy out of Git. Bad values are refused with an error that names the field.
