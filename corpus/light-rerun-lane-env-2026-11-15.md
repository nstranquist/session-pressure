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
