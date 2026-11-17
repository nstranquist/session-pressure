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
