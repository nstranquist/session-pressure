import Charts
import SwiftUI
import NDevPressureCore

struct DiskWritesView: View {
    @EnvironmentObject private var store: PressureStore
    @State private var pendingPolicy: DiskPolicyAction?
    @State private var pendingTrace: DiskWriter?

    private enum DiskPolicyAction: String, Identifiable {
        case observe
        case alerts
        case disable

        var id: String { rawValue }
    }

    var body: some View {
        ScrollView {
            LazyVStack(alignment: .leading, spacing: 16) {
                header
                if let error = store.diskWriteError {
                    InlineError(message: error)
                }
                scopeNotice
                metrics
                writers
                history
                traceResult
                policy
            }
            .padding(20)
        }
        .navigationTitle("Disk Writes")
        .confirmationDialog(
            policyConfirmationTitle,
            isPresented: Binding(
                get: { pendingPolicy != nil },
                set: { if !$0 { pendingPolicy = nil } }
            ),
            titleVisibility: .visible
        ) {
            if let pendingPolicy {
                Button(policyButtonTitle(pendingPolicy), role: pendingPolicy == .disable ? .destructive : nil) {
                    let action = pendingPolicy
                    self.pendingPolicy = nil
                    Task { await applyPolicy(action) }
                }
            }
            Button("Cancel", role: .cancel) { pendingPolicy = nil }
        } message: {
            Text(policyConfirmationMessage)
        }
        .confirmationDialog(
            "Trace file paths for this process?",
            isPresented: Binding(
                get: { pendingTrace != nil },
                set: { if !$0 { pendingTrace = nil } }
            ),
            titleVisibility: .visible
        ) {
            if let writer = pendingTrace {
                Button("Authorize 15-second trace") {
                    pendingTrace = nil
                    Task { await store.traceDiskWriter(writer, durationSeconds: 15) }
                }
            }
            Button("Cancel", role: .cancel) { pendingTrace = nil }
        } message: {
            Text(PressureHelp.diskWriteTrace)
        }
        .confirmationDialog(
            "Run the requested interactive path trace?",
            isPresented: Binding(
                get: { store.pendingDiskTraceRequest != nil },
                set: { if !$0 { store.pendingDiskTraceRequest = nil } }
            ),
            titleVisibility: .visible
        ) {
            if let request = store.pendingDiskTraceRequest {
                Button("Authorize \(request.durationSeconds)-second trace") {
                    Task { await store.confirmPendingDiskTrace() }
                }
            }
            Button("Cancel", role: .cancel) { store.pendingDiskTraceRequest = nil }
        } message: {
            Text(PressureHelp.diskWriteTrace)
        }
    }

    private var header: some View {
        HStack(alignment: .top, spacing: 14) {
            Image(systemName: "internaldrive.fill")
                .font(.system(size: 28))
                .foregroundStyle(stateColor)
            VStack(alignment: .leading, spacing: 4) {
                HStack(spacing: 8) {
                    Text(store.diskWriteSummary?.state.displayName ?? "Waiting for sample")
                        .font(.title2.weight(.semibold))
                    if let confidence = store.diskWriteSummary?.confidence {
                        Text(confidence.displayName)
                            .font(.caption.weight(.medium))
                            .padding(.horizontal, 7)
                            .padding(.vertical, 3)
                            .background(.secondary.opacity(0.12), in: Capsule())
                    }
                }
                Text("Internal SSD write volume · adaptive quiet baseline")
                    .foregroundStyle(.secondary)
                if let capturedAt = store.diskWriteSummary?.capturedAt {
                    Text("Live sample \(capturedAt.formatted(date: .omitted, time: .standard))")
                        .font(PressureTheme.monoCaption)
                        .foregroundStyle(.tertiary)
                }
            }
            Spacer()
            if store.isDiskFocusRefreshing {
                ProgressView().controlSize(.small)
            }
            Button("Refresh live") {
                Task { await store.refreshDiskWrites(live: true, includeHistory: true) }
            }
            .disabled(store.isDiskFocusRefreshing)
        }
    }

    private var scopeNotice: some View {
        SectionCard(title: "Measurement boundary", systemImage: "scope", help: PressureHelp.diskWriterAttribution) {
            VStack(alignment: .leading, spacing: 7) {
                Label("Device total: internal solid-state block devices", systemImage: "checkmark.seal")
                Label("Writer ranking: all-volume process counters, best-effort correlation", systemImage: "exclamationmark.triangle")
                Text("A writer row is a lead for diagnosis, not proof that every byte reached the internal SSD.")
                    .font(.caption)
                    .foregroundStyle(.secondary)
            }
        }
    }

    @ViewBuilder
    private var metrics: some View {
        if let summary = store.diskWriteSummary {
            VStack(alignment: .leading, spacing: 8) {
                LazyVGrid(columns: [GridItem(.adaptive(minimum: 180), spacing: 12)], spacing: 12) {
                    MetricCard(
                        title: "Current rate",
                        value: "\(formatBytes(summary.currentBytesPerSecond))/s",
                        subtitle: windowLabel(summary),
                        accent: stateColor,
                        help: PressureHelp.diskWrites
                    )
                    MetricCard(
                        title: "Last 15 minutes",
                        value: formatBytes(Double(summary.window15mBytes)),
                        subtitle: "\(String(format: "%.2f", summary.baselineRatio))× quiet p99",
                        accent: stateColor,
                        help: PressureHelp.diskWriteBaseline
                    )
                    MetricCard(
                        title: "Last 24 hours",
                        value: formatBytes(Double(summary.bytes24h)),
                        subtitle: coverageLabel(summary),
                        help: PressureHelp.diskWrites
                    )
                    MetricCard(
                        title: "Quiet p99 / 15m",
                        value: formatBytes(Double(summary.baselineP99Bytes15m)),
                        subtitle: "\(summary.baselineSamples) samples · \(summary.context)",
                        help: PressureHelp.diskWriteBaseline
                    )
                }
                if !summary.reasonCodes.isEmpty {
                    Label(summary.reasonCodes.map(reasonLabel).joined(separator: " · "), systemImage: "info.circle")
                        .font(.caption)
                        .foregroundStyle(.secondary)
                        .accessibilityLabel("Disk write model notes: \(summary.reasonCodes.joined(separator: ", "))")
                }
            }
        } else {
            EmptyHint(title: "No disk-write sample yet", systemImage: "internaldrive", detail: "The active pane takes one bounded live sample and then refreshes every 15 seconds.")
        }
    }

    private func coverageLabel(_ summary: DiskWriteSummary) -> String {
        if summary.unscoredGapBytes > 0 {
            return "includes \(formatBytes(Double(summary.unscoredGapBytes))) unscored gap"
        }
        if summary.rollingWindowIncomplete {
            return "partial observed window; still warming"
        }
        return "no unscored gap bytes"
    }

    private func reasonLabel(_ reason: String) -> String {
        reason.replacingOccurrences(of: "_", with: " ")
    }

    private var writers: some View {
        SectionCard(
            title: "Likely writers",
            systemImage: "list.number",
            trailing: "\(store.diskWriters.count)",
            help: PressureHelp.diskWriterAttribution
        ) {
            if store.diskWriters.isEmpty {
                EmptyHint(title: "No attributed writers", systemImage: "list.number", detail: "A process may be inaccessible, may have exited, or may not have written during the live window.")
            } else {
                VStack(spacing: 0) {
                    ForEach(store.diskWriters.prefix(20)) { writer in
                        HStack(spacing: 12) {
                            Image(systemName: writer.agentProcessCount > 0 ? "cpu" : "app")
                                .frame(width: 20)
                                .foregroundStyle(.secondary)
                            VStack(alignment: .leading, spacing: 2) {
                                Text(writer.executable)
                                    .font(.body.weight(.medium))
                                Text("\(writer.category) · \(writer.processCount) process\(writer.processCount == 1 ? "" : "es")")
                                    .font(.caption)
                                    .foregroundStyle(.secondary)
                            }
                            Spacer()
                            VStack(alignment: .trailing, spacing: 2) {
                                Text("\(formatBytes(writer.bytesPerSecond))/s")
                                    .font(PressureTheme.monoCaption)
                                Text("\(formatBytes(Double(writer.windowBytes))) / 15m")
                                    .font(.caption2)
                                    .foregroundStyle(.secondary)
                            }
                            if writer.pid != nil {
                                Button("Trace paths") { pendingTrace = writer }
                                    .buttonStyle(.bordered)
                                    .controlSize(.small)
                                    .disabled(store.isDiskTracing)
                            }
                        }
                        .padding(.vertical, 8)
                        if writer.id != store.diskWriters.prefix(20).last?.id {
                            Divider()
                        }
                    }
                }
            }
        }
    }

    private var history: some View {
        SectionCard(title: "Hourly writes", systemImage: "chart.bar", trailing: "last 24h") {
            if store.diskWriteHistory.isEmpty {
                EmptyHint(title: "History is still empty", systemImage: "chart.bar", detail: "Hourly feature checkpoints appear after resident observation; raw 15-second samples are never persisted.")
            } else {
                Chart(store.diskWriteHistory.reversed()) { point in
                    BarMark(
                        x: .value("Hour", point.hour, unit: .hour),
                        y: .value("Written", Double(point.bytesWritten))
                    )
                    .foregroundStyle(color(for: point.state ?? .unknown).gradient)
                }
                .chartYAxis {
                    AxisMarks { value in
                        AxisGridLine()
                        AxisValueLabel {
                            if let bytes = value.as(Double.self) {
                                Text(formatBytes(bytes))
                            }
                        }
                    }
                }
                .frame(height: 190)
                .help("Feature-only hourly totals. Restart-spanning bytes are shown separately as unscored gaps, never folded into the scored baseline.")
            }
        }
    }

    @ViewBuilder
    private var traceResult: some View {
        if store.isDiskTracing || store.diskTraceStatus != nil || !store.diskTracePaths.isEmpty {
            SectionCard(title: "Interactive path trace", systemImage: "doc.text.magnifyingglass", help: PressureHelp.diskWriteTrace) {
                if store.isDiskTracing {
                    HStack { ProgressView(); Text("Tracing in the privileged helper…") }
                }
                if let status = store.diskTraceStatus {
                    Text(status).font(.caption).foregroundStyle(.secondary)
                }
                if !store.diskTracePaths.isEmpty {
                    VStack(alignment: .leading, spacing: 4) {
                        ForEach(store.diskTracePaths, id: \.self) { path in
                            Text(path).font(PressureTheme.monoCaption).textSelection(.enabled)
                        }
                    }
                }
            }
        }
    }

    private var policy: some View {
        SectionCard(title: "Observation policy", systemImage: "bell.badge", help: PressureHelp.diskWrites) {
            HStack {
                VStack(alignment: .leading, spacing: 3) {
                    Text(policyLabel).font(.headline)
                    Text("Alerts are opt-in; observation never gates work or kills a process.")
                        .font(.caption)
                        .foregroundStyle(.secondary)
                }
                Spacer()
                Button("Observe only") { pendingPolicy = .observe }
                    .disabled(store.busyAction != nil)
                Button("Enable alerts") { pendingPolicy = .alerts }
                    .disabled(store.busyAction != nil)
                Button("Disable", role: .destructive) { pendingPolicy = .disable }
                    .disabled(store.busyAction != nil)
            }
        }
    }

    private var policyLabel: String {
        guard let policy = store.diskWritePolicy else { return "Policy unavailable" }
        if !policy.enabled { return "Disabled" }
        return policy.notificationsEnabled ? "Observing + alerts" : "Observing quietly"
    }

    private var policyConfirmationTitle: String {
        guard let pendingPolicy else { return "Change disk-write policy?" }
        return switch pendingPolicy {
        case .observe: "Enable quiet disk-write observation?"
        case .alerts: "Enable disk-write notifications?"
        case .disable: "Disable disk-write observation?"
        }
    }

    private var policyConfirmationMessage: String {
        guard let pendingPolicy else { return "" }
        return switch pendingPolicy {
        case .observe: "The resident samples every 15 seconds and keeps feature-only hourly checkpoints. Notifications stay off."
        case .alerts: "The resident may notify on a sustained high incident after the adaptive confidence gate. Alerts are rate-limited."
        case .disable: "This stops future device and writer samples. Existing bounded history remains until retention pruning."
        }
    }

    private func policyButtonTitle(_ action: DiskPolicyAction) -> String {
        switch action {
        case .observe: "Enable observation"
        case .alerts: "Enable alerts"
        case .disable: "Disable observation"
        }
    }

    private func applyPolicy(_ action: DiskPolicyAction) async {
        switch action {
        case .observe: await store.observeDiskWrites()
        case .alerts: await store.enableDiskWriteAlerts()
        case .disable: await store.disableDiskWrites()
        }
    }

    private var stateColor: Color {
        color(for: store.diskWriteSummary?.state ?? .unknown)
    }

    private func color(for state: DiskWriteState) -> Color {
        switch state {
        case .normal: PressureTheme.levelColor(.normal)
        case .unusual: PressureTheme.levelColor(.warning)
        case .high: PressureTheme.levelColor(.red)
        case .learning: .blue
        case .disabled, .unavailable, .unknown: .secondary
        }
    }

    private func formatBytes(_ value: Double) -> String {
        PressureFormat.bytes(Int64(clamping: UInt64(max(0, value.rounded()))))
    }

    private func windowLabel(_ summary: DiskWriteSummary) -> String {
        guard let seconds = summary.measurementWindowSeconds else { return "bounded native delta" }
        return String(format: "%.1fs native window", seconds)
    }
}
