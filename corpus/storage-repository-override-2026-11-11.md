# Per-repository storage floor, 2026-11-11

Status: implemented; installation and proof recorded below. Owner:
claude/side session of local_a2feecf0 (Garrid storage hold).

## Decision

Nico, 2026-11-11: "for the ~/dev/garrid build specifically, we're gonna lift
the 10gb restriction. as last resort you can clear go cache and other caches if
we fall below 5gb."

Garrid deliveries (commit-delivery worker 2.42.0) were held for about 20
minutes with `ndev-work-refused/v1 kind=admission: storage pressure work-wait
grace exceeded` while the data volume sat near 10 GiB free. The installed
policy then was: critical entry 10 GiB, critical recovery 15 GiB,
`block_new_at: critical`, enforcement on.

Scope: Garrid only. Every other repository keeps the 10 GiB block. Clearing
caches below 5 GiB is a separate last-resort operator step and is not
automated here.

## Design

- `storage_policy.repository_overrides` maps a canonical repository root to
  `{critical_free_bytes, reason}`. It is persisted in `policy.json`, validated
  on every load (absolute clean key, not `/`, floor at least 1 GiB, one-line
  reason up to 256 bytes, at most 64 entries) and omitted when empty.
- Scoping key: the canonical repository that owns the Git object store of the
  work's working directory (`StorageRepositoryRoot`). Commit-delivery runs
  Mac checks as `ndev session pressure work run --class test ...` inside a
  candidate slot under `~/.nicos-dev/commit-delivery/v1/candidates/`; the
  slot's `.git/objects` is a symlink to `<repo>/.git/objects`. A linked
  worktree reaches the same store through `commondir`. So the key needs no
  change to commit-delivery and no new flag.
- Admission: for a matching repository, disk-growing work is held only while
  physical free bytes are below the floor. The machine-wide diagnostic level
  and hysteresis remain in the snapshot but do not decide this admission. A
  held decision reports dimension `storage`, its floor as the release target
  and a reason naming the repository. Reclaim is always admitted.
- Fail closed: a persisted policy that fails to load (corrupt or invalid
  override) holds disk-growing work. A repository that cannot be resolved
  never receives an override; it keeps the machine-wide policy.
- Visibility: `storage status` JSON carries `storage_policy.repository_overrides`
  and `repository_admission` (floor, whether the repository exists, and
  `admits_now` when the sample is available and enforcement is on).
- Mutation: `storage policy repository set PATH --critical-free SIZE [--reason
  TEXT]` and `storage policy repository clear PATH`, under the policy mutation
  lock, followed by a resident reload (the resident fences relief actions on
  an exact persisted-policy match). Telemetry records only the verb leaf.
- `Policy` now holds a map, so it compares with `Policy.Equal`.

## Rejected alternatives

| Alternative | Why rejected | What would flip it |
| --- | --- | --- |
| Lower the machine-wide critical floor to 5 GiB | Lifts the block for every repository; Nico scoped it to Garrid | An explicit machine-wide decision |
| `storage policy observe` | Removes storage admission for all work | Never for this need |
| Raw working-directory prefix key | Delivery checks run in candidate slots, not `~/dev/garrid` | Commit-delivery running checks in the real checkout |
| New `work run --repository` flag passed by commit-delivery | Needs a second product release and lets any caller name any repository | Work runs whose directory has no Git store |
| Environment variable override | Implicit, unaudited, and not visible in status | None |
| Per-repository hysteresis | Needs per-repository latch state; the requirement is a plain floor | Observed flapping at the floor |

## Verification

- Focused Darwin tests (installed coordinator, local placement):
  `go test ./sessionpressure -run 'StorageRepository|ConfiguredStorageAdmission|CommandLeaf|Storage|PolicyMigrat|LegacyStorage'`
  and `go test ./sessionpressurecmd -run 'StorageRepository|PolicyMigrateStorage|SessionPressureStorage'` passed.
- Required cases (`TestStorageRepositoryOverrideScopesAdmission`,
  `TestConfiguredStorageAdmissionAppliesRepositoryOverride`): Garrid at 7 GiB
  admitted; another repository at 7 GiB held; Garrid at 4 GiB held with a
  5 GiB release target; exact 5 GiB admits; machine hysteresis does not hold
  Garrid; unresolved repository at 7 GiB held; corrupt or invalid persisted
  override held; reclaim admitted.
- Resolver (`TestStorageRepositoryRootResolvesCheckoutWorktreeAndCandidate`):
  checkout, subdirectory, linked worktree, candidate slot and its
  subdirectory, symlinked path, other repository, outside any repository,
  missing path, malformed `.git` file.

- Remote full suite on runner-3, candidate tree (HEAD 378c08b plus this
  change), job 967ec9cb-2548-4572-9ed4-6adde012adfe: `go vet ./...` passed;
  22 packages passed; one failure,
  `TestGoReuseSingleflightAndExplicitExecution` ("in-process queue timeout
  after 20s" waiting for identical express work). That test injects an
  always-allowed admission and does not reach storage or policy code. Isolated
  rerun, job 16efce81-6eab-4583-ac8f-3fb04d5d9f32, `-count=3`: it passed 3/3
  (35.6 s each), and all six new tests passed 3/3. Recorded as a load-timing
  flake of that test under the full parallel suite, not fixed here.

## Open items

- [x] Remote full suite on the active runner (one unrelated timing flake; see above).
- [ ] Follow-up: `TestGoReuseSingleflightAndExplicitExecution` 20 s wait budget
  flakes under full-suite runner load.
- [ ] Guarded publication.
- [ ] Native Darwin release build, install with the expected-current fence.
- [ ] Set the Garrid override to 5 GiB and confirm `storage status` JSON.
