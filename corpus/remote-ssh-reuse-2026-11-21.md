# Remote work: one SSH connection per job (WORK-1602, 2026-11-21)

Status: 2026-11-21, released and live on the Mac (release 19c9c9c, then 5bf8e7c). Owner: claude/3bd639ce-6e3f-4246-9482-a95928c1b14e. Work: WORK-1602.

## Problem

A remote Go job opened a new SSH connection for each step: the fleet probe, the source upload, the runner subscription, the artifact manifest, the artifact and the cleanup. Each new login from the Mac to runner-3 costs about 1.7 s. The provider build (`catalog-providers`, darwin cgo, 6.1 MB gzip artifact) paid about 8.4 s for upload plus download, while its runner work was 1-2 s.

## Count of SSH calls per job

Counted with an `ssh` shim on PATH (one line per invocation, with `-O` and `ControlMaster` state):

| Version | New logins | Reused sessions | Other |
|---|---|---|---|
| Before (9c3367c) | 6 | 0 | artifact fetched as manifest + body: 2 calls |
| After (19c9c9c) | 1 (the fleet probe becomes the master) | 3 (upload, runner, fetch) | `ssh -O exit` at the end |

## Change (19c9c9c)

- `remote_ssh.go`: `ControlMaster=auto`, `ControlPersist=60`, `ControlPath=/tmp/sp-ssh-<uid>/<job16>-%C`. The directory must be a real directory of this user with mode 0700 (Lstat, owner check), so another local user cannot plant or read the socket.
- The socket is private to one job. This is not the shared master that the gate-runner rule forbids on the `gaterunner-N` aliases: jobs never compete for sessions, and a dropped master fails only that job's calls. The next call opens a new master, and the runner subscription reconnects to the same durable job.
- Path guard: OpenSSH binds `<ControlPath>.<16 random chars>` before it renames the socket into place, so the guard counts 40 (`%C`) plus 17 bytes against the 103-byte macOS socket limit. The first version used `/tmp/session-pressure-ssh-<uid>` and failed with exit 255 ("too long for Unix domain socket"); the regression test `TestRemoteSSHControlPathFitsTheBoundSocket` covers it.
- `fetchRemoteArtifact`: one `cat -- <manifest> <artifact>` stream. The manifest line is validated (64 lower-case hex, two spaces, the exact path, newline), the body is hashed while it is written, and a nonzero remote exit fails the fetch.
- `runnerfleet.ProbeVia(opts...)`: the fleet probe takes the job's options, so the probe opens the master.
- Opt-out: `SESSION_PRESSURE_SSH_MUX=off` gives the old one-connection-per-call path.

## zstd: not adopted

The artifact is already gzip from the runner (`tar -czf`, 6.1 MB). A zstd stream of the same bytes is the same size, so it saves no transfer. A zstd archive instead of gzip would save about 0.6 s of runner CPU (WORK-1480 table) but needs a typed, tested Mac decoder in the publish path. The item allowed zstd only with that decoder. The gain did not justify a new decoder dependency, so it was not done.

## Result (installed release, provider build, `remote_work_finished` telemetry)

| Run | Upload + download | Job total |
|---|---|---|
| Before 1-4 (old helper) | 8.0 / 13.9 / 8.9 / 8.2 s | 25.5-31.7 s |
| After 1-3 (19c9c9c installed) | 3.6 / 4.0 / 3.6 s | 18.9 / 20.6 / 18.4 s |

Acceptance (upload plus download measurably below 8.4 s across 3 replays, release installed): met.

## Floor that stays

- Upload on the runner side: `cp -al` of the cached source into the candidate (0.34 s) and a full sha256 re-check (0.19 s). These are the integrity check of the uploaded tree.
- Download: about 1.6 s for 6.1 MB over the reused connection.

## Rollback

Set `SESSION_PRESSURE_SSH_MUX=off` for the old path at once, or install the previous release with `session-pressure-release install --expected-current <current>`.
