import Foundation

/// Central tooltip / help copy so UI labels stay consistent with CLI semantics.
public enum PressureHelp {
    public static func ring(level: PressureLevel, utilization: PressureUtilization) -> String {
        """
        \(Self.level(level))

        Arc is memory used (\(utilization.memoryUsedPercent)%), which is 100 − free. Color is the policy level and can rise from CPU or agent RSS without changing the fill.
        Free memory: \(utilization.freePercent)%.
        Host CPU: \(PressureFormat.percent(utilization.hostCPUPercent)).
        """
    }

    public static func level(_ level: PressureLevel) -> String {
        switch level {
        case .normal:
            return "Normal: free memory, host CPU, and agent RSS are below warning thresholds."
        case .warning:
            return "Warning: elevated host pressure. Monitor more often; admission still open unless policy escalates."
        case .red:
            return "Red: new canonical agent launches and heavy-work admissions are blocked when enforcement is on. CPU can reach red but not critical by itself."
        case .critical:
            return "Critical: severe free-memory / agent RSS pressure. With auto-shed enabled, the resident may gracefully SIGTERM one old quiescent agent tree after sustained samples, preferring hook-confirmed ready sessions and excluding known-busy sessions."
        case .unknown:
            return "Pressure level not yet sampled. Refresh or wait for the resident monitor."
        }
    }

    public static let freeMemory =
        "Host free memory percentage from native probes. Warning ≤25%, red ≤15%, critical ≤8% (defaults for a 16 GiB host)."
    public static let hostCPU =
        "Normalized host CPU from Mach host statistics (capped at 100%). Warning ≥85%, red ≥95%. CPU alone never raises critical."
    public static let memoryMomentum =
        "Diagnostic trend from up to five recent resident free-memory samples. Declining begins at −1 percentage point/min and rapid decline at −4. It estimates time to the red threshold but never raises pressure or authorizes relief."
    public static let swap =
        "Swap used is a corroborating signal only. Sticky swap after prior pressure does not keep the host critical without a current free-memory or agent-tree breach."
    public static let agentRSS =
        "Sum of resident-set sizes across complete agent process trees (Codex/Claude/Grok/Kimi). Shared pages can be double-counted; the name is intentional."
    public static let guardRSS =
        "Resident helper (or operator sample) self RSS. Budget ceiling is 30 MiB for the installed helper; the full ndev binary is not the resident."
    public static let sampleCost =
        "Wall-clock and CPU time for the last sample. Guard budgets cap sample wall p95 (~2s) and sample CPU p95 (~50ms)."
    public static let storage =
        "Available bytes on the writable APFS data volume from one constant-cost filesystem probe. Storage pressure is separate from memory pressure: it can gate disk-growing work but cannot terminate agent trees or delete personal data."
    public static let storageBeginSafeReclaim =
        "Preview, then confirm, the typed session-pressure storage apply --auto-safe path. The app streams that command's own output as a receipt. It never starts Grok, Codex, a PTY, or arbitrary argv. --auto-safe requires storage policy enable."
    public static let storageAutoSafe =
        "Closed auto_safe class: browser-dead-profiles and pnpm-store. Factory-only or blocked rows stay visible and non-actionable, including pageskein reclaim in the open extract."
    public static let storageOperator =
        "Named operator providers. Each Apply control previews then confirms --provider ID. go-build-cache is never part of --auto-safe. Operator --force skips only that provider's reclaim cooldown."
    public static let storageReportOnly =
        "Inventory only. downloads, library-caches, go-module-cache, and mobile-sync cannot be applied from this app."
    public static let storageReceipt =
        "Append-only log of the typed storage apply command and its JSON/text output. This is not an agent terminal and does not capture unrelated process argv."
    public static let storagePolicyEnable =
        "storage policy enable turns on disk-growth admission enforcement so --auto-safe apply is allowed. Observe keeps storage enabled but turns enforcement off. Named provider apply remains available either way."
    public static let keyboardRemap =
        "Pane keys are Pressure menu items (⌘1–⌘8, plus ⌘R / ⌘⇧R). Remap in System Settings → Keyboard → Keyboard Shortcuts → App Shortcuts. Add NDev Pressure and use the exact menu title (Storage, Work Queue, Live Sample). Idle trees has no global key; it is a tab inside Storage."
    public static let diskWrites =
        "Exact cumulative block writes for internal solid-state devices, sampled by the existing resident. This diagnostic state never changes host-pressure admission, cleanup, or process-relief authority."
    public static let diskWriterAttribution =
        "Best-effort libproc write deltas grouped by executable basename. Process counters cover all mounted volumes, so they correlate likely writers but cannot prove that one process wrote the exact internal-SSD device bytes."
    public static let diskWriteBaseline =
        "A per-work-context quiet p99 baseline. Learning requires at least two elapsed hours; confident scoring requires seven elapsed days. Anomalous windows do not train the baseline upward."
    public static let diskWriteTrace =
        "Interactive, bounded 5–30 second path tracing for one PID. It requires explicit administrator authorization, revalidates PID start identity, never runs in the background, and does not persist paths."
    public static let admissionAllowed =
        "Admission is open: canonical agent launches and new work leases may proceed under current policy."
    public static let admissionBlocked =
        "Admission is blocked at the current pressure level while enforcement is enabled. Reduce load or wait for recovery."
    public static let workCapacity =
        "Shared weighted capacity for builds, tests, emulators, browsers, and generic heavy work. Package-scoped go/cargo/node/swift jobs use express-test/express-build (lighter). Default benchmark leaves residual capacity for express work; benchmark-exclusive (or --exclusive) reserves the full capacity for clean-host evidence. Default capacity is 8 on a 10-core laptop."
    public static let workLease =
        "Active capacity lease held by a live owner PID. The command is already executing under that PID. Leases never store command arguments; dead/reused PIDs are reclaimed. Click a row for lifecycle audit detail — stdout/stderr stay with the launching agent terminal."
    public static let workWaiter =
        "Queued heavy-work request waiting for capacity or host pressure recovery. A protected head forces bounded capacity drain so smaller arrivals cannot starve it. Position is queue order. Use Run now on a waiter to promote that exact live operation without starting a new command. Click a row for lifecycle audit detail."
    public static let workOverride =
        "One-shot priority override (Work Queue → Run now). The selected live waiter is promoted to run as soon as capacity and host-pressure gates allow — it does not invent a new process or re-read commands (commands are never stored). Active leases are never preempted. It applies immediately without a confirmation step because it only reorders a queue you are already looking at and is undone by promoting something else. Replaces any earlier override sequence and is audited with a unique opaque request identity."
    public static let workOverrideClear =
        "Release the pinned promotion sequence (ndev session pressure work override --clear --confirm). The queue returns to ordinary bounded-lookahead policy and every release is audited per operation. Work that already acquired a lease keeps running — clearing changes admission order only, never a live process."
    public static let workOverrideAll =
        "Run all pins every waiter now in the queue as one ordered promotion sequence (ndev session pressure work override --all --confirm). The head runs first and each entry inherits the reservation as its predecessor acquires, so the queue drains in the order shown instead of letting later small arrivals bypass it. It never preempts a lease, raises capacity, or bypasses host pressure — with the host red, pinned work still waits."
    public static let workDetail =
        "Privacy-bounded inspector for one work operation: identity, capacity role, and durable lifecycle events. Command argv and process stdout/stderr are intentionally never captured by the pressure control plane — they remain on the agent/terminal that launched `ndev session pressure work run`."
    public static let workOutputLocation =
        "Process output is not mirrored here. Look at the agent session or terminal that invoked `ndev session pressure work run` (or work batch). Pressure only stores opaque digests plus queued/acquired/started/completed lifecycle rows under ~/.nicos-dev/session-pressure/work-events-*.jsonl."
    public static let policyFull =
        "Full protection: block launches at red and allow graceful auto-shed of one old quiescent tree after sustained critical pressure. Hook-confirmed ready sessions are preferred; known-busy sessions are excluded."
    public static let policyAdmission =
        "Admission only: block launches at red; never automatically signal processes."
    public static let policyObserve =
        "Observe only: monitor and write telemetry, but never block launches or shed trees."
    public static let policyInit =
        "Write the tuned observe-only default policy under ~/.nicos-dev/session-pressure/policy.json."
    public static let policySuggestion =
        "Work-style card: a calibration hint, or a return to balanced when the live style is not the daily-driver default. It never applies itself. Copy for agent pastes a closed-count brief; Apply runs the typed profile command after a confirmation. multi-agent-soft is observe-only with earlier soft-launch warnings. Balanced turns launch blocking back on; auto-shed is an explicit confirm option."
    public static let copyForAgent =
        "Copy a closed-count brief to the clipboard so you can paste it into an agent. No argv, paths, or prompts are included."
    public static let monitorInstall =
        "Install the low-priority user LaunchAgent (com.nicos.session-pressure) using a digest-verified helper artifact."
    public static let monitorUninstall =
        "Stop and remove the LaunchAgent; force observe-only on incomplete uninstall; keep telemetry."
    public static let monitorOnce =
        "Take one foreground diagnostic sample and persist a manual telemetry event (never automatic relief)."
    public static let idleApply =
        "Operator-confirmed graceful SIGTERM. Requires exact root PID + session ID from this inventory; the CLI re-samples and rejects identity/activity drift."
    public static let refresh =
        "Reload status, policy, work, admission, idle inventory, and telemetry from ndev (uses resident latest sample)."
    public static let liveSample =
        "Force a live host sample via ndev session pressure snapshot / status --live. Slightly more expensive than reading resident telemetry."
    public static let trees =
        "Complete agent process trees: each outer agent plus descendants. RSS is a sum, not unique physical pages. CPU is derived from native cumulative-time deltas; unavailable evidence is shown as sampling and cannot authorize relief."
    public static let hostConsumers =
        "Whole-host RSS and CPU grouped by a bounded executable basename. CPU is derived from native cumulative-time deltas; unavailable evidence is shown as sampling. This projection contains no PID, path, arguments, environment, or prompt text; agent-owned process counts are marked separately."
    public static let coverage =
        "Machine-readable truth about which paths are enforced, coordinated, merely observed, or need attention. Direct external launches are visible but are not globally intercepted, and automatic relief remains agent-tree-only."
    public static let telemetry =
        "Bounded sparse telemetry: state transitions and five-minute heartbeats. Full commands are never stored."
    public static let guardBudget =
        "Self-imposed budgets for helper RSS, CPU duty, sample cost, and daily telemetry growth. Budget breaches suppress automatic relief."
    public static let binaryPath =
        "Path to the ndev binary this app shells out to. Override with NDEV_BIN or NDEV_PATH."

    public static func section(_ name: String) -> String {
        switch name {
        case "Overview":
            return "At-a-glance host pressure, admission, guard health, top trees, and work capacity."
        case "Agent Trees":
            return "Sortable inventory of agent process trees from the latest sample."
        case "Disk Writes":
            return "Live internal-SSD write volume, adaptive quiet baseline, likely all-volume process writers, bounded hourly history, and explicit interactive path tracing."
        case "Work Queue":
            return "Weighted heavy-work coordinator: capacity, protected-head drain, active leases, adaptive 2.5s busy / 10s idle focus polling while visible, Run now and Run all queue promotions, waiters, and a lifecycle detail drawer that becomes read-only history when work exits."
        case "Policy":
            return "Protection mode controls and threshold / budget tables from the effective policy."
        case "Monitor":
            return "LaunchAgent lifecycle and resident helper health / sample cost."
        case "Idle Cleanup":
            return "Old low-CPU trees eligible for operator-confirmed graceful stop. Lives under Storage → Idle trees."
        case "Telemetry":
            return "Recent pressure transitions, heartbeats, and audited relief actions."
        case "Storage":
            return "Disk reclaim and idle trees. ⌘4. Disk tab is typed storage apply; Idle trees is operator-confirmed SIGTERM. The Overview storage card opens this pane."
        default:
            return name
        }
    }

    public static func metricTitle(_ title: String) -> String {
        switch title.lowercased() {
        case "free memory": return freeMemory
        case "host cpu": return hostCPU
        case "memory momentum": return memoryMomentum
        case "swap used": return swap
        case "agent rss sum": return agentRSS
        case "guard rss": return guardRSS
        case "sample cost": return sampleCost
        case "storage available": return storage
        default: return title
        }
    }
}
