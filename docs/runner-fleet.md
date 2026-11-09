# Gate-runner fleet

`gaterunner-fleet` is the shared placement library and CLI for SessionPressure,
commit delivery, and the canonical remote verification wrapper. It assigns new
work across active hosts. The host's durable scheduler owns admission and the
verdict. Fleet placement never grants platform authority to a gate.

The inventory is an authored JSON document, normally
`~/.nicos-dev/gaterunner-fleet/v1/fleet.json`. It has schema
`nicos.gaterunner-fleet.v1`, a nonempty `generation`, and at most 64 hosts.
Each host has these required fields:

| Field | Meaning |
|---|---|
| `name`, `user` | SSH endpoint; uses existing host-key verification |
| `state` | `active`, `staging`, `draining`, `reserve`, or `disabled` |
| `provider` | `hetzner-cloud`, `hetzner-dedicated`, or `other` |
| `platform` | `linux/amd64` or `linux/arm64` |
| `source_commit` | Exact 40-character scheduler source revision |
| `weight` | Relative share, 1–10000 for active hosts |
| `heavy_slots`, `light_slots` | Expected live scheduler capacity |
| `max_shards` | Host cap for delivery's shard runner; zero leaves it uncapped |
| `repositories` | Exact runner clone names; `session-pressure-work` enables source uploads through SessionPressure |
| `profiles` | Proven profiles, such as `linux-native`, `darwin-arm64-nocgo`, `darwin-arm64-cgo` |

Only active hosts with the requested repository, profile and lane are eligible.
Before assigning new work, bounded SSH probes check health, protocol, exact
scheduler source, platform, lane capacity and drain state. Weighted rendezvous
selection includes a queue-depth penalty. Its snapshot is advisory: the chosen
scheduler still enforces hard resource limits. A host becoming unavailable after
selection holds that request on that host. No execution retry changes hosts.

An immutable binding records the host, inventory digest and generation, request
identity digest, profile, repository and lane. Bindings live beside the inventory
in `bindings/`, keyed by SHA-256 of the request key. Atomic publication and fsync
make concurrent callers agree. These JSON files are execution receipts, not a
mutable queue database. Existing bindings are read before inventory or network
access, so drain, inventory removal or a probe failure cannot move a reconnect.
A reused key with different inputs is refused. Keep bindings at least as long
as the corresponding remote request; do not delete them to retry a failure.

```sh
gaterunner-fleet validate --config /absolute/path/fleet.json
gaterunner-fleet status --config /absolute/path/fleet.json
gaterunner-fleet select --config /absolute/path/fleet.json \
  --key REQUEST --identity EXACT_INPUT_IDENTITY --repo nicos-tools \
  --profile linux-native --lane heavy
session-pressure remote enable --fleet /absolute/path/fleet.json
```

Delivery's `gate-delegate.json` accepts `fleet` as an optional absolute path.
Its existing transport allowlist and committed platform policy still apply.
`additional_hosts` remain evidence-only mirrors, separate from active placement.
An explicit probe host bypasses fleet selection. The manual wrapper selects from
the default inventory when present; `GATE_FLEET_CONFIG` overrides its location.
`GATE_BOX_HOST` explicitly pins a host; `GATE_FLEET_CONFIG=off` keeps legacy
runner-2 routing. Missing explicit inventory or selector fails before upload.

To enroll a host, first keep it in `staging`. Prove SSH identity, clock, scheduler
revision, service supervision, hard resource limits, disk headroom, toolchain and
each advertised profile using candidate-bound receipts on that host. Then change
it to `active` in one atomic inventory replacement and increment `generation`.
Reserve hosts never receive automatic assignments. To drain a host, change its
inventory state and use its scheduler drain control. Existing jobs and bindings
stay there. To retire it, resolve its outstanding requests before removing access.

The fleet CLI's status aggregates all host observations and exits nonzero if an
active host is unhealthy or drained. It reports unavailable reserve/staging hosts
without making them eligible. It requires no Hetzner API credentials. Provisioning
is separate: Hetzner Cloud manages cloud VMs; Robot manages dedicated servers.
