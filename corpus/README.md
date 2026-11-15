# Session Pressure corpus

Owner: codex. Status: active, 2026-11-10.

- [Remote input preservation](remote-input-preservation.md): WORK-254 decisions,
  source provenance, proof and remaining installation checks.
- [Scheduler replay](scheduler-replay.md): WORK-3 baseline, seven-day window,
  validation and open installed acceptance.
- [App installation history](app-install-history.md): SQLite receipt preservation,
  X10 archival and one local rollback, with migration and installer proof.
- Architecture and environment: [runner fleet](../docs/runner-fleet.md) and
  [release contract](../scripts/release/README.md).

This index starts with the remote input work; it does not claim that older
product decisions have all been reconstructed.

- [Admission reservation recovery](admission-reservation-recovery.md): WORK-3 owned-lease regression and WORK-220 explicit policy migration.
- [Work-event SQLite read index](work-event-sqlite-index.md): WORK-103 indexed reads, compatible writes, fallback recovery and installed acceptance.

- [Resident memory-budget accounting](resident-budget-2026-11-11.md): WORK-604 fresh self RSS, preserved budget windows and current CPU investigation.

- [Kernel, display and swap profile](kernel-display-swap-2026-11-11.md): WORK-619 current paging attribution and protected Cowork investigation.

- [Per-repository storage floor](storage-repository-override-2026-11-11.md): Garrid-only 5 GiB admission floor, repository resolution through the Git object store, fail-closed policy reads.

- [Runner capacity and usage view](runner-capacity-2026-11-15.md): Hetzner tab capacity card, `remote capacity` contract, traffic terms and first live findings.

- [Work queue v1 rating and v2 plan](work-queue-v2-2026-11-15.md): 2026-11-15 parked-reservation stall, v1 score from telemetry, v1.1 selector schema 8 hotfix, v2 placement-first plan with runner-3 offload.

- [Interactive priority and the interactive red lane](interactive-priority-2026-11-15.md): attended agent chats go ahead of unprotected background work; capped light work under red memory, never critical or storage.
- [All admission guards disabled](guards-disabled-2026-11-15.md): at Nico's request, host, storage, launch admission and the weighted cap are off on the Mac; backup and revert steps.

- [Memory attribution and the swap lag release](memory-attribution-swap-lag-2026-11-15.md): memory families (LSP, agent host, build/test, browser) in latest.json, heartbeats and the delivery doctor; swap lag release (default off) enabled at 15 minutes on this host after approval, with telemetry evidence.

- [gocache-lru lock-free gets](gocache-lru-lock-free-2026-11-15.md): runner-3 GOCACHEPROG waits; no startup replay, lock-free gets, journal never cut; benchmarks and deploy state.
