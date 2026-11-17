# Fleet health probe under runner load (2026-11-17)

Owner: claude/garrid-desktop-parity
Status: 2026-11-17. Fixed, released and installed on mac-48g
(`current` = `7a9fc8c`).

## Symptom

Deliveries stopped with `fleet assignment: no eligible healthy fleet runner
… (runner-3: SSH health probe: signal: killed drained=false)`. The worker
retries, and pauses the delivery after four failures.

## Cause (measured 2026-11-17 about 05:50Z)

- runner-3 (12 cores) ran about 13 gate jobs at once. Load average was 20
  to 26, CPU pressure `some avg300` was 31 percent. Memory (58 GiB free) and
  disk I/O were not limiting. Every top process was under 90 seconds old, so
  nothing was stuck. The host admits by memory (`heavy_slots: 1024`, nicos-ops
  `c999a1c`), and max shards went from 4 to 6 on 2026-11-16 (`194ab52`).
- Under that load a fresh SSH login took about 8 seconds (`true`), and
  `sshd` has the same CPU weight (100) as the build jobs. Idle, the same login
  takes 1.7 seconds and `gaterunner health` answers in under 10 ms.
- `runnerfleet.Probe` gave the whole SSH call a fixed 12-second deadline. When
  login plus the three remote commands passed 12 seconds, the context killed
  ssh and the host was reported unhealthy. The scheduler was healthy the
  whole time and jobs kept passing.

## Fix

- The probe deadline is the caller's deadline minus `probeCallerMargin`
  (2 s), or `probeDefaultBudget` (24 s) without a caller deadline.
- `gaterunner-fleet` uses a 27-second context, so it answers before the
  commit-delivery worker's 30-second limit (`GateFleetCommand`) kills it.
- One retry after ssh's own failure (exit 255) when at least
  `probeRetryPause + probeMinAttempt` (5 s) remain. A remote command failure
  is the host's answer and is not retried.
- `ConnectTimeout` 5 → 10 seconds, so a slow banner exchange under load is not
  cut before the overall budget.
- `ObservedAt` is stamped when the answer arrives. commit-delivery's parallel
  fleet check rejects observations older than 30 seconds, so a probe-start
  timestamp would age a slow answer.
- Errors say `no answer within N s` and `(k attempt(s), N s)`.

Rejected: an SSH ControlMaster for the probe. nicos-ops `gate-runner.md` says
not to add ControlMaster for `gaterunner-N` lane tools.

## Proof

- `go test -race -run TestProbe ./runnerfleet`: 9 new tests pass (budget from
  caller, default budget, transport retry, no retry on remote failure, attempt
  cap, no retry without budget, timeout message, ObservedAt, invalid endpoint).
  The full `./runnerfleet` package passes.
- Live A/B with a fake `ssh` on PATH that sleeps 14 s and then runs the real
  ssh: the installed `cd39a44` selector reported both runners
  `SSH health probe: signal: killed` (the production error). The patched
  selector reported both healthy in 15.9 s. Without the delay it answers in
  2.1 s.

## Release and install (2026-11-17)

- Source `7a9fc8cdc248f0a968e9428d9326a3daf3d8bb94` (guarded single commit,
  local, not pushed). Sealed bundle sha256 `e3b509e2…2a9b60`, shipped with
  `remote-verify.sh --repo session-pressure --candidate-dir`.
- runner-3 job `5d32f875-ce3c-4321-85dd-a022e1e26858`: bundle verified,
  clean clone at the revision, `go test` of `runnerfleet`, `cmd/gaterunner-fleet`
  and `scripts/release` passed, six darwin/arm64 binaries built (CGO,
  `pressure-darwin-clang`), exit 0. The job waited about 10 minutes in the
  queue behind the load burst. Export: `/srv/build/exports/fleetprobe-7a9fc8c/out`.
  A first request (`749de19a`) failed in setup because the runner has no
  `session-pressure` clone; `--candidate-dir` needs none.
- `session-pressure-release install --expected-current cd39a44…` promoted
  `7a9fc8c`. The resident helper was not restarted.
- Installed checks: `gaterunner-fleet status` healthy for both hosts in 2.3 s;
  with the 14 s fake-ssh delay healthy in 16.3 s; `select` with a fresh key
  and the delay bound runner-3 in 15.7 s (binding key `probe-check-*`).
- `session pressure status` still reports `monitor_healthy: false`. That is the
  resident's own RSS and idle-duty budget over its 64-sample window, which
  this CLI change does not touch.

## Open

- runner-3 still oversubscribes CPU in bursts. A per-job CPU cap or a
  higher `sshd` CPU weight is a separate runner-policy decision.
- commit-delivery `parallelFleetDepth` ignores hosts with more than 128 heavy
  slots, so runner-3 (1024) never adds parallel fleet capacity there.

## Follow-up: probe history (2026-11-17)

The 8 s login was one sample and did not reproduce under two saturated
bursts (logins about 1.6 s). Its most likely cause was a short storage stall,
not CPU. The full diagnosis is in nicos-commit-delivery
`corpus/cpu-reserve-runner-3-2026-11-17.md`. To explain the next slow
login with data, every probe's SSH time now goes to `probe_samples` in
`runner-capacity.sqlite` (host, time, outcome, attempts, `probe_ms`). The
runner records PSI stall shares in `host_samples` at the same time.

## Transport state (WORK-1100, 2026-11-17)

Decision source: muse-workspace `corpus/runner-candidate-delta-transport.md`,
"Probe decision: transport state apart from health". nicos-ops
`docs/gaterunner-fleet.md` asks for transport state apart from health.

- `Observation.transport` is `ok`, `slow` or `unreachable`. `probe_ms` (from
  7f3ebf0) is the elapsed SSH time beside it.
- `slow`: the host answered, but at 5 s or more, or only on the retry after
  an ssh failure. `healthy` stays the scheduler's own answer, so a slow but
  healthy runner stays selectable.
- `unreachable`: no answer. The budget ran out, ssh exited 255 on every
  attempt, or ssh did not start. `select` refuses the host with
  `<host>: transport unreachable after <N>ms: <error>`, and `status` fails
  with `active runner <host> is unreachable (transport, <N>ms)`.
- A remote command failure (exit status other than 255) is the host's
  answer. Its transport is `ok` or `slow`, and health comes from that answer.
- Not changed: the probe budget, the retry and `ConnectTimeout`. A longer
  timeout would hide congestion that WORK-1042 and WORK-1058 removed at the
  source.

### Slow threshold: 5 s

Probe history (`probe_samples` in `runner-capacity.sqlite`, 12:18Z to 12:22Z,
72 probes, all healthy). The samples came from the new build. runner-3 had
load average 13.6 on 12 cores and CPU PSI `some avg60` 31 percent at the
time.

| host | n | min | median | p95 | max |
| --- | --- | --- | --- | --- | --- |
| runner-3 | 36 | 1583 ms | about 1612 ms | about 2400 ms | 4050 ms |
| runner-1 | 36 | 1675 ms | about 1705 ms | about 2020 ms | 2097 ms |

Reasons for 5 s:

- It is above all normal jitter seen, including the 4.05 s outlier on a
  loaded but healthy host. A 3 s threshold would flag about 1 in 36 routine
  probes, so `slow` would be noise.
- It is below the 8 s pathological login from the morning incident, so that
  case reports `slow`.
- An answer at 5 s still leaves about 18 s of the 25 s selector budget.

Revisit when `probe_samples` holds a few days of data: compare the p99 to
5 s, and join slow probes to the runner's `host_samples` PSI shares.

Proof: `go test -race ./runnerfleet ./cmd/gaterunner-fleet ./internal/gaterunner`
passes. New tests: `TestProbeTransportOK`,
`TestProbeTransportSlowKeepsSchedulerHealth`, `TestProbeTransportUnreachable`
and `TestAssignRefusesUnreachableTransport`. The live `gaterunner-fleet status`
from the source build showed `"transport":"ok"` for runner-3 and
runner-1 at 1.6 to 4.1 s.
