# All admission guards disabled on Nico's Mac — 2026-11-15

Status: applied 16:27 to 16:30 UTC, 2026-11-15, at Nico's request ("i think we
need full disabling of ndev pressure / session pressure guards ... disable
all"). Owner: claude/2be14703-c035-42fd-a4e3-ec254777804c.

## Why

Under red pressure, admission waits held work for many minutes at a time. The
commit-delivery worker's Darwin checks waited up to 5 minutes per attempt
and then were held. Those waits kept one of the worker's 2 lanes busy, so
deliveries in other repositories queued for up to 110 minutes with no run.
Nico asked to remove the guards.

## What changed (`~/.nicos-dev/session-pressure/policy.json`)

Backup: `policy.json.bak-20261115T162718Z-before-disable-all`.

| Guard | Before | After | How |
|---|---|---|---|
| Host-pressure admission | `enforce_admission: true`, profile `interactive` | `false`, profile `observe` | `ndev session pressure policy observe` |
| Automatic shedding | off | off | same |
| CPU-warning capacity derating | on | off | same |
| Storage admission | `storage.enforce_admission: true` | `false` | `ndev session pressure storage policy observe` |
| Weighted work cap | `work_limits.capacity: 8` | `1000` | direct edit, then `policy observe` reloaded the monitor |
| Agent launch admission | `launch_admission.mode: soft` | `off` | direct edit |

The resident monitor keeps sampling, so telemetry continues. Remote Go
routing is unchanged.

## Stuck queue after the change

The policy change did not release existing waiters. The queue head
(`d26d09ea`, `swift build` in `apps/SessionPressure`, helper
`sha256-88bc7a4b…`) held a pressure reservation whose owner never resumed it.
Ten waiters, including new ones, sat behind it with 0 of 1000 units used.
`work cancel` refuses waiters, so its helper (pid 9251) got SIGTERM. Its
command had not started. The queue then drained (7 leases, empty queue). The
uninstalled `pressureParked` selection fix in `sessionpressure/worklease.go`
targets the same stall.

## Proof

`ndev session pressure work run --class build --wait 5s -- echo LEASE_OK`
completed in 1.5 s while `status` read `level=red`, swap 10.7 GB and swap-out
364 MB/s.

## Reverting

Copy the backup over `policy.json`, then run
`ndev session pressure policy profile apply interactive`. Running
`policy enable`, `storage policy enable` or any profile apply turns guards
back on. An agent that runs one of these brings the guards back.
