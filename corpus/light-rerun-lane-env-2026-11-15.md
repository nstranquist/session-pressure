# Remote Go limits follow the admitted runner lane (2026-11-15)

Owner: claude (side session of local_f0012eda). Status: fixed in source, 2026-11-15.

Two express-test `go test ./cmd/ndev-go/` runs on runner-3 were rerun
heavy after the light lane's 300 s cap, then killed as `stalled` (exit 137).
The heavy rerun kept the light pins that `remoteGoInvocation` baked into the
command (`GOMAXPROCS=1`, `GOMEMLIMIT=768MiB`, `-p=1`), so it crawled at a
fraction of a core with no output. Full evidence is in nicos-commit-delivery
`corpus/light-rerun-lane-env-2026-11-15.md`.

Decision: the runner names the admitted lane in `NCD_RUNNER_LANE`. A light
request's command applies the light pins only when that lane is light (or
the variable is absent, on an older runner). Otherwise it uses the heavy
limits, with the existing elastic and fixed branches.

Proof: `TestRemoteGoInvocationLightFollowsTheAdmittedLane` runs the generated
shell for an older runner, a light attempt, and heavy reruns in fixed and
elastic CPU mode.

## Shipped (WORK-832, 2026-11-16)

- Installed: Mac release 6475f5d (CLI and helper, doctor `mismatch:
  false`) already contains 8c1ea78. The helper binary carries the
  `NCD_RUNNER_LANE` branch.
- Correction: the test above failed inside a runner job (job
  67661aad). It inherited the runner's own `NCD_RUNNER_LANE=heavy`, so its
  "older runner" case saw heavy limits. e0cf4ca drops `NCD_RUNNER_LANE`,
  `NCD_CPU_MODE` and `GOMAXPROCS` from the base env and logs the job's real
  limits.
- Live proof: an express-test `go test -v ./sessionpressure/` from the
  installed CLI, requested light, ran on runner-3 as job 9b549789 and
  passed. The job saw `NCD_RUNNER_LANE=heavy`, `NCD_CPU_MODE=elastic`,
  `GOMAXPROCS` unset, `GOMEMLIMIT=3GiB` and no `-p=1`. Before the fix the
  same admission kept `GOMAXPROCS=1`, `-p=1` and `768MiB`.
- Pushed 3161427..e0cf4ca after gitleaks (11 commits, no leaks), the shared
  redact registry (patch unchanged) and a grep for the banned handle and
  private hosts (none).
