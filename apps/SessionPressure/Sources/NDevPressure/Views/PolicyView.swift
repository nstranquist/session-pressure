import SwiftUI
import NDevPressureCore

struct PolicyView: View {
    @EnvironmentObject private var store: PressureStore
    @State private var confirmEnableFull = false
    @State private var confirmEnableAdmission = false
    @State private var confirmObserve = false
    @State private var confirmInit = false
    @State private var pendingProfile = "balanced"
    @State private var confirmProfile = false

    private var policy: PressurePolicy? { store.board.policy }

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 16) {
                SectionCard(
                    title: "Protection mode",
                    systemImage: "shield.lefthalf.filled",
                    help: "Effective policy flags: enabled, enforce_admission, auto_shed_critical. Mutations go through ndev session pressure policy."
                ) {
                    if let policy {
                        HStack(spacing: 12) {
                            LevelBadge(level: store.board.level)
                            VStack(alignment: .leading, spacing: 4) {
                                Text(policy.modeLabel)
                                    .font(.title2.weight(.semibold))
                                Text("enabled=\(policy.enabled ? "true" : "false") · admission=\(policy.enforceAdmission ? "true" : "false") · auto-shed=\(policy.autoShedCritical ? "true" : "false")")
                                    .font(PressureTheme.monoCaption)
                                    .foregroundStyle(.secondary)
                            }
                            Spacer()
                        }

                        HStack(spacing: 10) {
                            Menu("Work style") {
                                ForEach(["balanced", "throughput", "interactive", "observe"], id: \.self) { profile in
                                    Button(profile.replacingOccurrences(of: "-", with: " ").capitalized) {
                                        pendingProfile = profile
                                        confirmProfile = true
                                    }
                                }
                            }
                            .menuStyle(.borderedButton)
                            .disabled(store.busyAction != nil)
                            .help("Choose the work style. Only Interactive derates new work during warning pressure; safety floors remain active in every enforcing mode.")

                            Button("Full protection") { confirmEnableFull = true }
                                .buttonStyle(.borderedProminent)
                                .tint(PressureTheme.levelColor(.red))
                                .disabled(store.busyAction != nil)
                                .help(PressureHelp.policyFull)

                            Button("Admission only") { confirmEnableAdmission = true }
                                .buttonStyle(.bordered)
                                .disabled(store.busyAction != nil)
                                .help(PressureHelp.policyAdmission)

                            Button("Observe only") { confirmObserve = true }
                                .buttonStyle(.bordered)
                                .disabled(store.busyAction != nil)
                                .help(PressureHelp.policyObserve)

                            Button("Init defaults") { confirmInit = true }
                                .buttonStyle(.borderless)
                                .disabled(store.busyAction != nil)
                                .help(PressureHelp.policyInit)
                        }
                        .padding(.top, 8)

                        Text("Balanced and Throughput keep warning pressure advisory. Interactive may lower only new work arrivals to preserve responsiveness. Memory/swap, storage, serious thermal, and state-integrity floors remain enforced when protection is on.")
                            .font(.caption)
                            .foregroundStyle(.secondary)
                            .padding(.top, 4)
                    } else {
                        Text("No policy loaded. Initialize defaults to write observe-only policy.")
                            .foregroundStyle(.secondary)
                        Button("Init observe-only policy") { confirmInit = true }
                            .buttonStyle(.borderedProminent)
                    }
                }

                if let t = policy?.thresholds {
                    SectionCard(
                        title: "Thresholds",
                        systemImage: "chart.bar",
                        help: "Default 16 GiB host ladder. Free-memory and agent RSS can raise critical; CPU stops at red; swap only corroborates."
                    ) {
                        thresholdTable(t)
                    }
                }

                if let b = policy?.resourceBudgets {
                    SectionCard(
                        title: "Guard resource budgets",
                        systemImage: "gauge.with.needle",
                        help: PressureHelp.guardBudget
                    ) {
                        LazyVGrid(columns: [GridItem(.adaptive(minimum: 180), spacing: 10)], spacing: 10) {
                            budget("Max self RSS", b.maxSelfRSSMB.map { PressureFormat.mb($0) })
                            budget("Idle CPU duty", b.maxIdleCPUPercent.map { PressureFormat.percent($0) })
                            budget("Pressure CPU duty", b.maxPressureCPUPercent.map { PressureFormat.percent($0) })
                            budget("Sample wall p95", b.maxSampleDurationMS.map { String(format: "%.0f ms", $0) })
                            budget("Sample CPU p95", b.maxSampleCPUTimeMS.map { String(format: "%.0f ms", $0) })
                            budget("Telemetry / day", b.maxTelemetryBytesPerDay.map { PressureFormat.bytes($0) })
                        }
                    }
                }

                if let policy {
                    SectionCard(title: "Cadence", systemImage: "clock") {
                        LazyVGrid(columns: [GridItem(.adaptive(minimum: 160), spacing: 10)], spacing: 10) {
                            budget("Normal sample", policy.sampleIntervalSeconds.map { "\($0)s" })
                            budget("Pressure sample", policy.pressureSampleIntervalSeconds.map { "\($0)s" })
                            budget("Critical sample", policy.criticalSampleIntervalSeconds.map { "\($0)s" })
                            budget("Block new at", policy.blockNewAt)
                        }
                    }
                }

                if let launch = policy?.launchAdmission {
                    SectionCard(
                        title: "New-launch backpressure",
                        systemImage: "person.crop.circle.badge.clock",
                        help: "Aggregate work demand can block new canonical agent sessions without preventing queued work or explicit resumes from draining."
                    ) {
                        LazyVGrid(columns: [GridItem(.adaptive(minimum: 160), spacing: 10)], spacing: 10) {
                            budget("Mode", launch.mode)
                            budget("Queue threshold", String(launch.queueDepthBlock))
                            budget("Oldest wait", "\(launch.oldestWaitBlockSeconds)s")
                            budget("Explicit resume", launch.resumeBehavior)
                        }
                    }
                }
            }
            .padding(20)
        }
        .background(PressureTheme.bg)
        .confirmationDialog("Enable full protection?", isPresented: $confirmEnableFull, titleVisibility: .visible) {
            Button("Enable admission + auto-shed", role: .destructive) {
                Task { await store.enableProtection(autoShed: true) }
            }
            Button("Cancel", role: .cancel) {}
        } message: {
            Text("Blocks canonical launches at red and allows graceful relief of one old quiescent agent tree after sustained critical pressure.")
        }
        .confirmationDialog("Enable admission-only?", isPresented: $confirmEnableAdmission, titleVisibility: .visible) {
            Button("Enable admission (no auto-shed)") {
                Task { await store.enableProtection(autoShed: false) }
            }
            Button("Cancel", role: .cancel) {}
        } message: {
            Text("Blocks launches at red but never automatically signals processes.")
        }
        .confirmationDialog("Switch to observe-only?", isPresented: $confirmObserve, titleVisibility: .visible) {
            Button("Observe only") {
                Task { await store.observeOnly() }
            }
            Button("Cancel", role: .cancel) {}
        } message: {
            Text("Keeps monitoring and telemetry but disables admission blocks and automatic shedding.")
        }
        .confirmationDialog("Write default policy?", isPresented: $confirmInit, titleVisibility: .visible) {
            Button("Init observe-only defaults") {
                Task { await store.initPolicy(force: false) }
            }
            Button("Force overwrite", role: .destructive) {
                Task { await store.initPolicy(force: true) }
            }
            Button("Cancel", role: .cancel) {}
        }
        .confirmationDialog("Apply work style?", isPresented: $confirmProfile, titleVisibility: .visible) {
            Button("Apply \(pendingProfile.replacingOccurrences(of: "-", with: " ").capitalized)") {
                Task {
                    await store.applyPolicyProfile(pendingProfile)
                }
            }
            Button("Cancel", role: .cancel) {}
        } message: {
            Text(pendingProfile == "interactive"
                ? "Interactive mode enables warning-capacity derating for new work only. Existing leases drain and safety floors are unchanged."
                : "Apply this mode's admission and scheduling policy? Existing leases continue running.")
        }
    }

    private func thresholdTable(_ t: PolicyThresholds) -> some View {
        VStack(spacing: 0) {
            thresholdHeader
            thresholdRow("Host CPU %", t.hostCPUWarningPercent, t.hostCPURedPercent, nil)
            thresholdRow("Free memory %", t.freeWarningPercent.map(Double.init), t.freeRedPercent.map(Double.init), t.freeCriticalPercent.map(Double.init), lowerIsWorse: true)
            thresholdRow("Swap MiB", t.swapWarningMB, t.swapRedMB, t.swapCriticalMB)
            thresholdRow("Agent total MiB", t.agentTotalWarningMB, t.agentTotalRedMB, t.agentTotalCriticalMB)
            thresholdRow("Largest tree MiB", t.treeWarningMB, t.treeRedMB, t.treeCriticalMB)
        }
        .font(PressureTheme.monoCaption)
    }

    private var thresholdHeader: some View {
        HStack {
            Text("Signal").frame(maxWidth: .infinity, alignment: .leading)
            Text("Warn").frame(width: 72, alignment: .trailing)
            Text("Red").frame(width: 72, alignment: .trailing)
            Text("Crit").frame(width: 72, alignment: .trailing)
        }
        .font(.caption.weight(.semibold))
        .foregroundStyle(.secondary)
        .padding(.bottom, 6)
    }

    /// `lowerIsWorse` marks the one inverted signal in the ladder: free memory
    /// escalates as it falls, every other row escalates as it rises. Rendering
    /// both directions identically made a 15% red threshold read like a ceiling.
    private func thresholdRow(_ name: String, _ w: Double?, _ r: Double?, _ c: Double?, lowerIsWorse: Bool = false) -> some View {
        HStack {
            HStack(spacing: 5) {
                Text(name)
                Image(systemName: lowerIsWorse ? "arrow.down.right.circle" : "arrow.up.right.circle")
                    .font(.caption2)
                    .foregroundStyle(.tertiary)
                    .help(lowerIsWorse
                        ? "Escalates as this value falls: at or below each threshold."
                        : "Escalates as this value rises: at or above each threshold.")
            }
            .frame(maxWidth: .infinity, alignment: .leading)
            Text(threshold(w, lowerIsWorse)).frame(width: 72, alignment: .trailing).foregroundStyle(PressureTheme.levelColor(.warning))
            Text(threshold(r, lowerIsWorse)).frame(width: 72, alignment: .trailing).foregroundStyle(PressureTheme.levelColor(.red))
            Text(threshold(c, lowerIsWorse)).frame(width: 72, alignment: .trailing).foregroundStyle(PressureTheme.levelColor(.critical))
        }
        .padding(.vertical, 5)
    }

    private func threshold(_ value: Double?, _ lowerIsWorse: Bool) -> String {
        guard value != nil else { return "—" }
        return (lowerIsWorse ? "≤" : "≥") + fmt(value)
    }

    private func fmt(_ value: Double?) -> String {
        guard let value else { return "—" }
        if value == value.rounded() { return String(format: "%.0f", value) }
        return String(format: "%.1f", value)
    }

    private func budget(_ title: String, _ value: String?) -> some View {
        VStack(alignment: .leading, spacing: 4) {
            Text(title.uppercased())
                .font(.caption2.weight(.semibold))
                .foregroundStyle(.secondary)
            Text(value ?? "—")
                .font(.callout.monospaced())
        }
        .padding(10)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(Color.primary.opacity(0.04), in: RoundedRectangle(cornerRadius: 8, style: .continuous))
    }
}
