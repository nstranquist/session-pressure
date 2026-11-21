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
- [Fleet health probe under runner load](fleet-probe-under-load-2026-11-17.md): why a loaded runner-3 looked unhealthy, the caller-budget probe fix and its A/B proof.

- [Work queue v1 rating and v2 plan](work-queue-v2-2026-11-15.md): 2026-11-15 parked-reservation stall, v1 score from telemetry, v1.1 selector schema 8 hotfix, v2 placement-first plan with runner-3 offload.

- [Interactive priority and the interactive red lane](interactive-priority-2026-11-15.md): attended agent chats go ahead of unprotected background work; capped light work under red memory, never critical or storage.
- [All admission guards disabled](guards-disabled-2026-11-15.md): at Nico's request, host, storage, launch admission and the weighted cap are off on the Mac; backup and revert steps.

- [Memory attribution and the swap lag release](memory-attribution-swap-lag-2026-11-15.md): memory families (LSP, agent host, build/test, browser) in latest.json, heartbeats and the delivery doctor; swap lag release (default off) enabled at 15 minutes on this host after approval, with telemetry evidence.

- [gocache-lru lock-free gets](gocache-lru-lock-free-2026-11-15.md): runner-3 GOCACHEPROG waits; no startup replay, lock-free gets, journal never cut; benchmarks and deploy state.

- [Remote Go limits follow the admitted runner lane](light-rerun-lane-env-2026-11-15.md): light requests rerun or admitted heavy drop the GOMAXPROCS=1 / -p=1 pins via NCD_RUNNER_LANE.

- [Storage retention providers, storage watch, tm-exclude plan](storage-retention-watch-2026-11-16.md): S2 ST6/ST7/ST3: binary-revisions, turso-backups, go-build-orphans, resolved Go caches, disk/TM/X10/quota alerts, Time Machine exclusion plan.

- [Resource-headroom admission, shadow candidate](resource-headroom-shadow-2026-11-17.md): WORK-1065 under WORK-3; CPU, disk-write and ladder beside the memory shadow, opt-in throttle, host-sample replay in `work evaluate`, seven-day numbers and open deviations.
- [Memory levels calibration](memory-levels-calibration-2026-11-17.md): WORK-1057 memory levels in `memory-levels.json`, anchors from monitor history on the 16 GiB and 48 GiB Macs, RSS rungs held at warning while kernel free memory is high, install on both Macs.

- [Browser admission: memory-derived slots](browser-admission-2026-11-17.md): WORK-1114, no fixed browser slot, runtime-priced headroom admission, `--wait` through BROWSER_PAUSED.
- [Work capacity raised to 20](work-capacity-20-2026-11-17.md): live cap had fallen back to the 12-unit default; raised to 20 from headroom evidence; delivery lane ceiling shown not to be the limit; memory guard still needs enforcement.
- [Memory guards switch](memory-guards-switch-2026-11-17.md): WORK-1125, `memory_admission = enforce` arms memory headroom and swap-thrash serialization without `enforce_admission`.
- [Resource admission live, unit cap removed](resource-admission-live-2026-11-17.md): WORK-1125/WORK-1065, `work_limits.admission = resource` default, rule v3, lock fit check, live proof and deviations. KEP: `docs/active/resource-admission/`.
- [Telemetry history archived, never deleted](telemetry-history-archive-2026-11-17.md): WORK-1009, aged day files, command shards and audit generations move to verified gzip archives beside the live file; runner capacity offloads to retention-offload-local.
- [Browser control: resident CPU cost and enforcement gate](browser-control-resident-cost-2026-11-17.md): WORK-1141, no steady-state rewrite, in-memory evidence, lock only on change, `Monitor.EnforceBrowsers` gate after a harness stopped a live browser.
- [File lock reclaim race](filelock-reclaim-race-2026-11-17.md): WORK-1225 root cause, guarded reclaim fix and stress A/B.
- [JSONL append lock race](jsonl-lock-race-2026-11-17.md): WORK-1252, `internal/lockfile` deleted, `internal/jsonl` on `internal/filelock`, n-1 sidecar contract, stress A/B and latency.

- [App split panes clear the sidebar](app-split-panes-2026-11-19.md): Work Queue and Process Incidents were blank under the macOS 26 floating sidebar; `HSplitView` replaced by the SwiftUI `ResizableSplit`.
- [Darwin cgo stable tool directory](darwin-cgo-cache-key-2026-11-21.md): WORK-1480 per-job clang -B path broke every cgo cache key; shared link, fallback, release 9c3367c, 41 s to 6-8 s runner job.
- [Remote SSH reuse](remote-ssh-reuse-2026-11-21.md): WORK-1602 one private ControlMaster per job and a one-stream artifact fetch; 6 logins to 1; upload plus download 8.0-13.9 s to 3.6-4.0 s; zstd not adopted.
- [gocache-lru SQLite index](gocache-lru-sqlite-index-2026-11-21.md): WORK-1601 put client stops replaying the 860k-row journal (1031 ms to 14.9 ms); one-time import; runner pin afcfe04f; rollback needs a cache reset.
