import SwiftUI
import NDevPressureCore

struct OverviewView: View {
    @EnvironmentObject private var store: PressureStore
    @State private var confirmApplySuggestion = false
    @State private var suggestionExpanded = false

    private var snap: PressureSnapshot { store.board.snapshot }
    private var health: StatusHealth? { store.board.health }
    private var coverage: CoverageReport? { store.board.coverage }
    private var policy: PressurePolicy? { store.board.policy }
    private var work: WorkStatus? { store.board.work }
    private var admission: Admission? { store.board.admission }

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 16) {
                header
                storageCTA
                doctorStrip
                suggestionCard
                metricsGrid
                reasonsAndAdmission
                chipsRow
                hostConsumersPreview
                coveragePanel
                topTreesPreview
                workPreview
            }
            .padding(20)
        }
        .background(PressureTheme.bg)
        .confirmationDialog(
            store.policySuggestion?.confirmTitle ?? "Apply work style?",
            isPresented: $confirmApplySuggestion,
            titleVisibility: .visible
        ) {
            if let suggestion = store.policySuggestion {
                if suggestion.kind == .restoreDefault {
                    Button("Apply balanced") {
                        Task { await store.applyPolicyProfile(suggestion.profile, withAutoShed: false) }
                    }
                    Button("Apply balanced + auto-shed") {
                        Task { await store.applyPolicyProfile(suggestion.profile, withAutoShed: true) }
                    }
                } else {
                    Button(suggestion.weakensProtection ? "Apply and turn off protection" : "Apply \(suggestion.title)", role: suggestion.weakensProtection ? .destructive : nil) {
                        Task { await store.applyPolicyProfile(suggestion.profile, withAutoShed: suggestion.withAutoShed) }
                    }
                }
            }
            Button("Cancel", role: .cancel) {}
        } message: {
            Text(store.policySuggestion?.confirmMessage ?? "")
        }
    }

    private var header: some View {
        HStack(alignment: .center, spacing: 20) {
            PressureRing(
                level: snap.level,
                freePercent: snap.freePercent,
                hostCPU: snap.hostCPUPercent,
                hostCPUAvailable: snap.hostCPUAvailable
            )

            VStack(alignment: .leading, spacing: 10) {
                HStack(spacing: 10) {
                    Text("Host Pressure")
                        .font(.largeTitle.weight(.semibold))
                    LevelBadge(level: snap.level)
                }

                Text(policy?.modeLabel ?? "Policy unknown")
                    .font(.title3)
                    .foregroundStyle(.secondary)

                HStack(spacing: 8) {
                    if let health {
                        StatusChip(label: "Monitor", ok: health.monitorHealthy)
                        StatusChip(label: "Daily driver", ok: health.dailyDriverReady)
                        StatusChip(label: "Operator", ok: health.operatorReady)
                        StatusChip(
                            label: health.protectionMode.replacingOccurrences(of: "_", with: " "),
                            ok: health.dailyDriverReady || health.protectionMode == "observe"
                        )
                    }
                }

                Text("Updated \(PressureFormat.relative(store.board.refreshedAt)) · \(store.board.liveSample ? "live" : "resident")")
                    .font(.caption)
                    .foregroundStyle(.tertiary)
            }

            Spacer(minLength: 0)
        }
        .padding(18)
        .background(PressureTheme.card, in: RoundedRectangle(cornerRadius: 16, style: .continuous))
        .overlay(
            RoundedRectangle(cornerRadius: 16, style: .continuous)
                .stroke(PressureTheme.levelColor(snap.level).opacity(0.25), lineWidth: 1)
        )
    }

    @ViewBuilder
    private var storageCTA: some View {
        let storage = snap.storage
        if storage.available && storage.level >= .warning {
            HStack(spacing: 12) {
                LevelBadge(level: storage.level)
                VStack(alignment: .leading, spacing: 2) {
                    Text("Storage \(storage.level.displayName.lowercased()) · \(PressureFormat.bytes(storage.availableBytes)) free")
                        .font(.headline)
                    Text("Use Storage → Begin safe reclaim. Typed apply only — no agent terminal.")
                        .font(.caption)
                        .foregroundStyle(.secondary)
                }
                Spacer()
                Button("Open Storage") {
                    store.openStorage(tab: .disk)
                }
                .buttonStyle(.borderedProminent)
                .help(PressureHelp.storageBeginSafeReclaim)
            }
            .padding(14)
            .frame(maxWidth: .infinity, alignment: .leading)
            .contentShape(RoundedRectangle(cornerRadius: 14, style: .continuous))
            .onTapGesture { store.openStorage(tab: .disk) }
            .background(PressureTheme.card, in: RoundedRectangle(cornerRadius: 14, style: .continuous))
            .overlay(
                RoundedRectangle(cornerRadius: 14, style: .continuous)
                    .stroke(PressureTheme.levelColor(storage.level).opacity(0.35), lineWidth: 1)
            )
        }
    }

    @ViewBuilder
    private var doctorStrip: some View {
        if let doctor = store.board.doctor {
            VStack(alignment: .leading, spacing: 10) {
                HStack(spacing: 10) {
                    Text("Doctor")
                        .font(.headline)
                    StatusChip(label: "ok", ok: doctor.ok ?? false)
                    if let mode = doctor.protectionMode {
                        StatusChip(label: mode.replacingOccurrences(of: "_", with: " "), ok: doctor.ok ?? false)
                    }
                    if let mon = doctor.monitor {
                        StatusChip(label: "Monitor", ok: mon.healthy ?? false)
                    }
                    if let work = doctor.work {
                        Text("queue \(work.used ?? 0)/\(work.capacity ?? 0) depth \(work.queueDepth ?? 0)")
                            .font(.caption.monospaced())
                            .foregroundStyle(.secondary)
                        if work.expressGreen == true {
                            StatusChip(label: "express green", ok: true)
                        }
                    }
                    Spacer(minLength: 0)
                }
                if let soft = doctor.launchSoftPressure {
                    Text("soft-launch would_block=\(soft.wouldBlock == true ? "yes" : "no") noise_suppressed=\(soft.noiseSuppressed == true ? "yes" : "no")")
                        .font(.caption)
                        .foregroundStyle(.secondary)
                }
                if !doctor.fixes.isEmpty {
                    VStack(alignment: .leading, spacing: 8) {
                        ForEach(doctor.fixes.prefix(3), id: \.self) { fix in
                            CopyableOperatorText(text: "fix: \(fix)", agentPaste: fix)
                        }
                    }
                }
                interruptAndSuggestionRow
            }
            .padding(14)
            .frame(maxWidth: .infinity, alignment: .leading)
            .background(PressureTheme.card, in: RoundedRectangle(cornerRadius: 14, style: .continuous))
        } else if store.board.wrapperInterruptOperations > 0 {
            // Calibration-only strip when doctor envelope is unavailable.
            VStack(alignment: .leading, spacing: 8) {
                Text("Work forensics")
                    .font(.headline)
                interruptAndSuggestionRow
            }
            .padding(14)
            .frame(maxWidth: .infinity, alignment: .leading)
            .background(PressureTheme.card, in: RoundedRectangle(cornerRadius: 14, style: .continuous))
        }
    }

    @ViewBuilder
    private var interruptAndSuggestionRow: some View {
        let interrupts = store.board.wrapperInterruptOperations
        if interrupts > 0 {
            HStack(spacing: 10) {
                StatusChip(label: "wrapper interrupts \(interrupts)", ok: false)
                Spacer(minLength: 0)
            }
        }
    }

    @ViewBuilder
    private var suggestionCard: some View {
        if let suggestion = store.policySuggestion {
            WorkStyleSuggestionCard(
                suggestion: suggestion,
                expanded: $suggestionExpanded
            ) {
                confirmApplySuggestion = true
            }
        }
    }

    private var metricsGrid: some View {
        LazyVGrid(columns: [GridItem(.adaptive(minimum: 160), spacing: 12)], spacing: 12) {
            MetricCard(
                title: "Free memory",
                value: PressureFormat.percentInt(snap.freePercent),
                subtitle: "of \(PressureFormat.mb(snap.physicalMemoryMB))",
                accent: freeAccent,
                progress: Double(100 - snap.freePercent) / 100,
                help: PressureHelp.freeMemory
            )
            MetricCard(
                title: "Host CPU",
                value: PressureFormat.percent(snap.hostCPUPercent),
                subtitle: snap.hostCPUAvailable ? "\(snap.logicalCPUCount) logical cores" : "CPU probe unavailable",
                accent: cpuAccent,
                progress: min(1, snap.hostCPUPercent / 100),
                help: PressureHelp.hostCPU
            )
            MetricCard(
                title: "Memory momentum",
                value: snap.memoryMomentum.displayName,
                subtitle: memoryMomentumSubtitle,
                accent: memoryMomentumAccent,
                help: PressureHelp.memoryMomentum
            )
            MetricCard(
                title: "Storage available",
                value: PressureFormat.bytes(snap.storage.availableBytes),
                subtitle: snap.storage.available ? "\(PressureFormat.percent(snap.storage.freePercent)) · \(snap.storage.volumePath)" : (snap.storage.error ?? "probe unavailable"),
                accent: PressureTheme.levelColor(snap.storage.level),
                progress: snap.storage.totalBytes > 0 ? min(1, 1 - Double(snap.storage.availableBytes) / Double(snap.storage.totalBytes)) : nil,
                help: PressureHelp.storage + " Click to open Storage (⌘4).",
                action: { store.openStorage(tab: .disk) }
            )
            MetricCard(
                title: "Swap used",
                value: PressureFormat.mb(snap.swapUsedMB),
                subtitle: "corroborating signal",
                accent: PressureTheme.levelColor(snap.swapUsedMB >= 8192 ? .red : snap.swapUsedMB >= 4096 ? .warning : .normal),
                help: PressureHelp.swap
            )
            MetricCard(
                title: "Agent RSS sum",
                value: PressureFormat.mb(snap.agentRSSSumMB),
                subtitle: "\(snap.agentTreeCount) trees · \(snap.processCount) procs",
                accent: PressureTheme.levelColor(snap.agentRSSSumMB >= 11264 ? .red : snap.agentRSSSumMB >= 8192 ? .warning : .normal),
                progress: min(1, snap.agentRSSSumMB / 13312),
                help: PressureHelp.agentRSS
            )
            MetricCard(
                title: "Guard RSS",
                value: PressureFormat.mb(snap.guardRSSMB),
                subtitle: snap.guardBudgetOK ? "budget ok · \(snap.guardRole ?? "—")" : "budget breach",
                accent: snap.guardBudgetOK ? PressureTheme.levelColor(.normal) : PressureTheme.levelColor(.red),
                help: PressureHelp.guardRSS
            )
            MetricCard(
                title: "Sample cost",
                value: String(format: "%.0f ms", snap.sampleDurationMS),
                subtitle: "cpu \(String(format: "%.1f", snap.sampleCPUTimeMS)) ms",
                accent: .secondary,
                help: PressureHelp.sampleCost
            )
        }
    }

    private var reasonsAndAdmission: some View {
        HStack(alignment: .top, spacing: 12) {
            SectionCard(
                title: "Pressure reasons",
                systemImage: "list.bullet.indent",
                help: "Policy breach strings that raised the current level (CPU, free memory, agent totals, largest tree, swap corroboration)."
            ) {
                if snap.reasons.isEmpty {
                    Text(snap.level == .normal ? "No active breaches." : "No reason strings in sample.")
                        .font(.callout)
                        .foregroundStyle(.secondary)
                } else {
                    VStack(alignment: .leading, spacing: 6) {
                        ForEach(snap.reasons, id: \.self) { reason in
                            HStack(alignment: .top, spacing: 8) {
                                Image(systemName: "arrow.right.circle.fill")
                                    .foregroundStyle(PressureTheme.levelColor(snap.level))
                                    .font(.caption)
                                Text(reason)
                                    .font(.callout)
                                    .textSelection(.enabled)
                            }
                        }
                    }
                }
            }

            SectionCard(
                title: "Admission",
                systemImage: "lock.shield",
                help: "Whether new canonical agent launches are allowed under the current policy and live/projected sample."
            ) {
                if let admission {
                    HStack(spacing: 10) {
                        Image(systemName: admission.allowed ? "checkmark.seal.fill" : "xmark.seal.fill")
                            .foregroundStyle(admission.allowed ? PressureTheme.levelColor(.normal) : PressureTheme.levelColor(.red))
                            .font(.title2)
                            .help(admission.allowed ? PressureHelp.admissionAllowed : PressureHelp.admissionBlocked)
                        VStack(alignment: .leading, spacing: 4) {
                            Text(admission.allowed ? "Launches admitted" : "Launches blocked")
                                .font(.headline)
                                .help(admission.allowed ? PressureHelp.admissionAllowed : PressureHelp.admissionBlocked)
                            Text(admission.source ?? "—")
                                .font(PressureTheme.monoCaption)
                                .foregroundStyle(.secondary)
                                .help("Probe source for this admission decision (e.g. live-host-probe).")
                        }
                    }
                    if !admission.reasons.isEmpty {
                        VStack(alignment: .leading, spacing: 4) {
                            ForEach(admission.reasons, id: \.self) { r in
                                Text("• \(r)")
                                    .font(.caption)
                                    .foregroundStyle(.secondary)
                            }
                        }
                        .padding(.top, 4)
                    }
                    if let queue = admission.workQueue {
                        VStack(alignment: .leading, spacing: 3) {
                            Text(queue.wouldBlock ? "New-launch backpressure active" : "New-launch queue headroom")
                                .font(.caption.weight(.semibold))
                                .foregroundStyle(queue.wouldBlock ? PressureTheme.levelColor(.red) : .secondary)
                            Text("queue \(queue.queueDepth)/\(queue.queueDepthBlock) · oldest \(PressureFormat.durationMS(queue.oldestWaitMS))/\(PressureFormat.durationMS(queue.oldestWaitBlockMS)) · \(queue.enforced ? "enforced" : "observed")")
                                .font(PressureTheme.monoCaption)
                                .foregroundStyle(.secondary)
                        }
                        .padding(.top, 5)
                        .help("Aggregate work-queue pressure can stop new agent sessions while explicit resumes remain available by default. No command or prompt data is read.")
                    }
                    if let warning = admission.warning, !warning.isEmpty {
                        InlineError(message: warning)
                    }
                } else {
                    Text("Admission state unavailable.")
                        .foregroundStyle(.secondary)
                }
            }
        }
    }

    private var chipsRow: some View {
        SectionCard(
            title: "Guard health",
            systemImage: "heart.text.square",
            help: PressureHelp.guardBudget
        ) {
            FlowChips(items: guardChips)
        }
    }

    private var guardChips: [(String, Bool, String?)] {
        var items: [(String, Bool, String?)] = []
        if let health {
            items.append(("Monitor healthy", health.monitorHealthy, nil))
            items.append(("Latest fresh", health.latestMonitorFresh, health.latestMonitorAgeSeconds.map { String(format: "%.0fs", $0) }))
            items.append(("Baseline", true, "\(health.residentNormalSamples ?? 0)/\(health.requiredNormalSamples ?? 4) normal"))
        }
        items.append(("Budget", snap.guardBudgetOK, PressureFormat.mb(snap.guardRSSMB)))
        let inventoryDetail = snap.processInventoryAgeSeconds.map { age in
            "\(snap.processInventoryFresh ? "fresh" : "cached") · \(String(format: "%.0fs", age)) · \(snap.processInventorySource ?? "unknown")"
        } ?? (snap.processInventoryFresh ? "fresh" : "unavailable")
        items.append(("Inventory", snap.processInventoryAvailable, inventoryDetail))
        if let duty = snap.guardCPUDutyPercent {
            // Budget already folds idle/pressure ceilings; surface the duty figure.
            items.append(("CPU duty", snap.guardBudgetOK, PressureFormat.percent(duty)))
        }
        if let bytes = snap.telemetryProjectedBytesPerDay {
            items.append(("Telemetry/day", bytes < 1_048_576, PressureFormat.bytes(bytes)))
        }
        return items
    }

    private var hostConsumersPreview: some View {
        SectionCard(
            title: "Top host consumers",
            systemImage: "memorychip",
            trailing: "\(snap.topHostConsumers.count) shown",
            help: PressureHelp.hostConsumers
        ) {
            if snap.topHostConsumers.isEmpty {
                EmptyHint(title: "No host attribution", systemImage: "memorychip", detail: "Wait for a fresh resident process inventory.")
            } else {
                VStack(spacing: 0) {
                    ForEach(Array(snap.topHostConsumers.prefix(10).enumerated()), id: \.element.id) { index, consumer in
                        HStack(spacing: 12) {
                            Text("\(index + 1)")
                                .font(PressureTheme.monoCaption)
                                .foregroundStyle(.tertiary)
                                .frame(width: 22, alignment: .trailing)
                            VStack(alignment: .leading, spacing: 3) {
                                Text(consumer.executable)
                                    .font(.callout.weight(.semibold))
                                Text("\(consumer.category.replacingOccurrences(of: "_", with: " ")) · \(consumer.processCount) process(es)\(consumer.agentProcessCount > 0 ? " · \(consumer.agentProcessCount) agent-owned" : "")")
                                    .font(PressureTheme.monoCaption)
                                    .foregroundStyle(.secondary)
                            }
                            Spacer()
                            VStack(alignment: .trailing, spacing: 3) {
                                Text(PressureFormat.mb(consumer.rssSumMB))
                                    .font(.callout.weight(.medium))
                                Text(consumer.cpuAvailable ? "cpu \(PressureFormat.percent(consumer.cpuPercentSum))" : "cpu sampling")
                                    .font(PressureTheme.monoCaption)
                                    .foregroundStyle(.secondary)
                            }
                        }
                        .padding(.vertical, 8)
                        if index < min(9, snap.topHostConsumers.count - 1) {
                            Divider().opacity(0.5)
                        }
                    }
                }
            }
        }
    }

    private var coveragePanel: some View {
        SectionCard(
            title: "Prevention & observation coverage",
            systemImage: "scope",
            trailing: coverage?.status.replacingOccurrences(of: "-", with: " ") ?? "unavailable",
            help: PressureHelp.coverage
        ) {
            if let coverage {
                VStack(spacing: 0) {
                    ForEach(Array(coverage.surfaces.enumerated()), id: \.element.id) { index, surface in
                        HStack(alignment: .top, spacing: 10) {
                            Image(systemName: coverageIcon(surface.state))
                                .foregroundStyle(coverageColor(surface.state))
                                .frame(width: 18)
                            VStack(alignment: .leading, spacing: 3) {
                                HStack(spacing: 6) {
                                    Text(surface.label).font(.callout.weight(.semibold))
                                    Text(surface.state)
                                        .font(PressureTheme.monoCaption)
                                        .foregroundStyle(coverageColor(surface.state))
                                }
                                Text("\(surface.scope) · \(surface.detail)")
                                    .font(.caption)
                                    .foregroundStyle(.secondary)
                                    .fixedSize(horizontal: false, vertical: true)
                            }
                            Spacer()
                        }
                        .padding(.vertical, 7)
                        if index < coverage.surfaces.count - 1 { Divider().opacity(0.4) }
                    }
                }
                if !coverage.limitations.isEmpty {
                    VStack(alignment: .leading, spacing: 5) {
                        Text("Explicit boundaries").font(.caption.weight(.semibold))
                        ForEach(coverage.limitations, id: \.self) { limitation in
                            Text("• \(limitation)")
                                .font(.caption)
                                .foregroundStyle(.secondary)
                        }
                    }
                    .padding(.top, 8)
                }
            } else {
                Text("Coverage report unavailable; update the ndev binary and refresh.")
                    .foregroundStyle(.secondary)
            }
        }
    }

    private var topTreesPreview: some View {
        SectionCard(
            title: "Top agent trees",
            systemImage: "tree",
            trailing: "\(snap.topAgentTrees.count) shown",
            help: PressureHelp.trees
        ) {
            if snap.topAgentTrees.isEmpty {
                EmptyHint(title: "No agent trees", systemImage: "tree", detail: "When Codex/Claude/Grok/Kimi trees appear they show here.")
            } else {
                VStack(spacing: 0) {
                    ForEach(Array(snap.topAgentTrees.prefix(8).enumerated()), id: \.element.id) { index, tree in
                        TreeRow(tree: tree, rank: index + 1)
                        if index < min(7, snap.topAgentTrees.count - 1) {
                            Divider().opacity(0.5)
                        }
                    }
                }
                Button {
                    store.selectedSection = .trees
                } label: {
                    Label("Open full tree inventory", systemImage: "arrow.right")
                }
                .buttonStyle(.borderless)
                .padding(.top, 8)
            }
        }
    }

    private var workPreview: some View {
        SectionCard(
            title: "Heavy-work coordinator",
            systemImage: "rectangle.stack",
            help: PressureHelp.workCapacity
        ) {
            if let work {
                CapacityBar(used: work.used, capacity: work.capacity, available: work.available)
                if work.leases.isEmpty && work.waiters.isEmpty && work.admissionHoldCount == 0 {
                    Text("No active leases, waiters, or held work.")
                        .font(.caption)
                        .foregroundStyle(.secondary)
                        .padding(.top, 4)
                } else {
                    HStack(spacing: 16) {
                        Text("\(work.leases.count) lease(s)")
                            .font(.caption.weight(.medium))
                        Text("\(work.queueDepth) waiting")
                            .font(.caption.weight(.medium))
                            .foregroundStyle(work.queueDepth > 0 ? PressureTheme.levelColor(.warning) : .secondary)
                        if work.admissionHoldCount > 0 {
                            // Held work is blocked before the queue exists, so an
                            // empty queue next to idle capacity is not the whole story.
                            Label("\(work.admissionHoldCount) held", systemImage: "hand.raised.fill")
                                .font(.caption.weight(.semibold))
                                .foregroundStyle(PressureTheme.levelColor(.red))
                        }
                    }
                    .padding(.top, 6)
                    if work.admissionHoldCount > 0 {
                        Text("Held at the host-pressure gate for up to \(PressureFormat.durationMS(work.longestAdmissionHoldMS)) — not queued, no capacity charged.")
                            .font(.caption)
                            .foregroundStyle(.secondary)
                            .padding(.top, 2)
                    }
                    if let head = work.waiters.first {
                        HStack(spacing: 10) {
                            Text("Head #\(head.position ?? 1) \(head.className) · weight \(head.weight) · waited \(PressureFormat.durationMS(head.waitMS))")
                                .font(.caption)
                                .foregroundStyle(.secondary)
                                .lineLimit(1)
                            Spacer()
                            Button {
                                store.selectedSection = .work
                            } label: {
                                Label("Open queue", systemImage: "arrow.right")
                            }
                            .buttonStyle(.borderless)
                            .help("Work Queue lists every waiter with Run now, Run all, and a lifecycle detail drawer.")
                            if work.overrideOperationID == head.operationID {
                                Label("next", systemImage: "bolt.fill")
                                    .font(.caption2.weight(.semibold))
                                    .foregroundStyle(PressureTheme.levelColor(.red))
                            } else if head.isPressureReserved {
                                Label("held", systemImage: "pause.circle.fill")
                                    .font(.caption2.weight(.semibold))
                                    .foregroundStyle(PressureTheme.levelColor(.warning))
                                    .help("Reserved and waiting for host pressure to clear. Run now cannot start it sooner.")
                            } else {
                                Button {
                                    Task { await store.overrideWork(waiter: head) }
                                } label: {
                                    Label("Run now", systemImage: "play.fill")
                                }
                                .buttonStyle(.borderedProminent)
                                .controlSize(.small)
                                .disabled(store.busyAction != nil)
                                .help(PressureHelp.workOverride)
                            }
                        }
                        .padding(.top, 8)
                        if work.queueDepth > 1 {
                            HStack(spacing: 10) {
                                if work.overrideQueueDepth > 0 {
                                    Text("\(work.overrideQueueDepth) of \(work.queueDepth) pinned")
                                        .font(.caption)
                                        .foregroundStyle(PressureTheme.levelColor(.red))
                                }
                                Spacer()
                                Button {
                                    Task { await store.overrideAllWork() }
                                } label: {
                                    Label("Run all", systemImage: "forward.end.fill")
                                }
                                .buttonStyle(.bordered)
                                .controlSize(.small)
                                .disabled(store.busyAction != nil || work.overrideQueueDepth >= work.queueDepth || !work.supportsOverrideSequence)
                                .help(work.supportsOverrideSequence
                                    ? PressureHelp.workOverrideAll
                                    : "Unavailable until this host's work state advances past its single-slot override; Run now still promotes one waiter.")
                            }
                            .padding(.top, 4)
                        }
                    }
                }
            } else {
                Text("Work status unavailable.")
                    .foregroundStyle(.secondary)
            }
        }
    }

    private var freeAccent: Color {
        if snap.freePercent <= 8 { return PressureTheme.levelColor(.critical) }
        if snap.freePercent <= 15 { return PressureTheme.levelColor(.red) }
        if snap.freePercent <= 25 { return PressureTheme.levelColor(.warning) }
        return PressureTheme.levelColor(.normal)
    }

    private var cpuAccent: Color {
        if snap.hostCPUPercent >= 95 { return PressureTheme.levelColor(.red) }
        if snap.hostCPUPercent >= 85 { return PressureTheme.levelColor(.warning) }
        return PressureTheme.levelColor(.normal)
    }

    private var memoryMomentumSubtitle: String {
        if let minutes = snap.minutesToMemoryRed {
            return String(format: "red threshold in ~%.1f min", minutes)
        }
        if snap.memoryMomentumSampleCount < 3 {
            return "learning · \(snap.memoryMomentumSampleCount)/3 samples"
        }
        return String(format: "%+.2f percentage points/min", snap.freePercentSlopePerMinute)
    }

    private var memoryMomentumAccent: Color {
        switch snap.memoryMomentum {
        case .rapidDecline: PressureTheme.levelColor(.red)
        case .declining: PressureTheme.levelColor(.warning)
        case .recovering: PressureTheme.levelColor(.normal)
        case .steady, .unknown: .secondary
        }
    }

    private func coverageIcon(_ state: String) -> String {
        switch state {
        case "enforced": "checkmark.shield.fill"
        case "coordinated": "rectangle.3.group.fill"
        case "observed": "eye.fill"
        default: "exclamationmark.triangle.fill"
        }
    }

    private func coverageColor(_ state: String) -> Color {
        switch state {
        case "enforced": PressureTheme.levelColor(.normal)
        case "coordinated": .blue
        case "observed": .secondary
        default: PressureTheme.levelColor(.warning)
        }
    }
}

private struct FlowChips: View {
    let items: [(String, Bool, String?)]

    var body: some View {
        // Simple wrapping via LazyVGrid — keeps layout cheap.
        LazyVGrid(columns: [GridItem(.adaptive(minimum: 150), spacing: 8)], alignment: .leading, spacing: 8) {
            ForEach(Array(items.enumerated()), id: \.offset) { _, item in
                StatusChip(
                    label: item.0,
                    ok: item.1,
                    detail: item.2,
                    help: chipHelp(item.0, ok: item.1, detail: item.2)
                )
            }
        }
    }

    private func chipHelp(_ label: String, ok: Bool, detail: String?) -> String {
        let state = ok ? "ok" : "needs attention"
        let extra = detail.map { " · \($0)" } ?? ""
        switch label {
        case "Monitor healthy":
            return "Resident LaunchAgent health and freshness. \(state)\(extra)"
        case "Daily driver":
            return "Unattended daily-driver readiness (baseline proof, budget-clean, protection mode). \(state)\(extra)"
        case "Operator":
            return "Daily-driver readiness plus recovery-state review. \(state)\(extra)"
        case "Latest fresh":
            return "Whether latest.json is within the max age for admission projection. \(state)\(extra)"
        case "Baseline":
            return "Normal samples required before this resident PID may auto-shed. \(state)\(extra)"
        case "Budget":
            return "\(PressureHelp.guardBudget) \(state)\(extra)"
        case "Inventory":
            return "Native process inventory availability, age, and source. Admission projects it only inside the tighter trust window. \(state)\(extra)"
        case "CPU duty":
            return "Helper CPU duty between samples. Idle ceiling ~0.25% of one core; higher under pressure cadence. \(state)\(extra)"
        case "Telemetry/day":
            return "Projected daily telemetry JSONL growth vs 1 MiB/day budget. \(state)\(extra)"
        default:
            return "\(label): \(state)\(extra)"
        }
    }
}

struct TreeRow: View {
    let tree: AgentTree
    var rank: Int? = nil
    var showActions: Bool = false
    var onApplyIdle: (() -> Void)? = nil

    private var rowHelp: String {
        var parts = [
            "\(tree.agent) tree rooted at pid \(tree.rootPID)",
            "\(tree.processCount) processes · \(PressureFormat.mb(tree.rssSumMB)) RSS sum · age \(PressureFormat.duration(seconds: tree.elapsedSeconds))",
        ]
        if let session = tree.sessionID, !session.isEmpty {
            parts.append("session \(session)")
        }
        if let q = tree.quiescentSamples, q > 0 {
            parts.append("\(q) consecutive quiescent samples")
        }
        if let semantic = tree.semanticState {
            parts.append("hook state \(semantic.displayName.lowercased())")
        }
        parts.append(PressureHelp.trees)
        return parts.joined(separator: "\n")
    }

    var body: some View {
        HStack(spacing: 12) {
            if let rank {
                Text(String(format: "%02d", rank))
                    .font(PressureTheme.monoCaption)
                    .foregroundStyle(.tertiary)
                    .frame(width: 22, alignment: .trailing)
            }

            Circle()
                .fill(PressureTheme.agentColor(tree.agent))
                .frame(width: 8, height: 8)

            VStack(alignment: .leading, spacing: 2) {
                HStack(spacing: 6) {
                    Text(PressureFormat.agentLabel(tree.agent))
                        .font(.callout.weight(.semibold))
                    Text(tree.executable)
                        .font(PressureTheme.monoCaption)
                        .foregroundStyle(.secondary)
                }
                HStack(spacing: 10) {
                    Text("pid \(tree.rootPID)")
                    Text("\(tree.processCount) procs")
                    if let q = tree.quiescentSamples, q > 0 {
                        Text("q \(q)")
                            .foregroundStyle(.secondary)
                    }
                    if let semantic = tree.semanticState {
                        Text(semantic.displayName.lowercased())
                            .foregroundStyle(semantic == .ready ? PressureTheme.levelColor(.normal) : .secondary)
                    }
                    Text(PressureFormat.shortSession(tree.sessionID))
                        .foregroundStyle(.secondary)
                }
                .font(PressureTheme.monoCaption)
                .foregroundStyle(.secondary)
            }

            Spacer()

            VStack(alignment: .trailing, spacing: 2) {
                Text(PressureFormat.mb(tree.rssSumMB))
                    .font(.callout.monospaced().weight(.semibold))
                    .help("RSS sum across the tree (shared pages may be double-counted).")
                Text(PressureFormat.duration(seconds: tree.elapsedSeconds))
                    .font(PressureTheme.monoCaption)
                    .foregroundStyle(.secondary)
                    .help("Age of the root process.")
            }

            if !tree.cpuAvailable || tree.cpuPercentSum > 0.05 {
                Text(tree.cpuAvailable ? PressureFormat.percent(tree.cpuPercentSum) : "sampling")
                    .font(PressureTheme.monoCaption)
                    .foregroundStyle(tree.cpuAvailable ? PressureTheme.levelColor(.warning) : .secondary)
                    .frame(width: 58, alignment: .trailing)
                    .help(tree.cpuAvailable ? "Aggregate CPU percent across tree processes." : "CPU activity evidence is unavailable; cleanup and automatic relief fail closed.")
            }

            if showActions, let onApplyIdle, tree.sessionID != nil {
                Button("SIGTERM", action: onApplyIdle)
                    .buttonStyle(.bordered)
                    .controlSize(.small)
                    .tint(PressureTheme.levelColor(.red))
                    .help(PressureHelp.idleApply)
            }
        }
        .padding(.vertical, 8)
        .contentShape(Rectangle())
        .help(rowHelp)
    }
}
