import SwiftUI
import NDevPressureCore

struct TelemetryView: View {
    @EnvironmentObject private var store: PressureStore

    private var events: [TelemetryEvent] { store.board.telemetryEvents }
    private var actions: [PressureAction] { store.board.reliefActions }

    /// Latest non-zero interrupt projection from heartbeat summaries or board calibration.
    private var interruptForensics: (count: Int, rate: Double?) {
        if let cal = store.board.calibration, cal.interruptCount > 0 {
            let rate = cal.interruptProjection?.wrapperInterruptRatePerHour
            return (cal.interruptCount, rate)
        }
        for event in events.reversed() {
            if let n = event.summary?.wrapperInterruptOperations, n > 0 {
                return (n, event.summary?.wrapperInterruptRatePerHour)
            }
        }
        return (store.board.wrapperInterruptOperations, store.board.calibration?.interruptProjection?.wrapperInterruptRatePerHour)
    }

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 16) {
                interruptChipCard

                SectionCard(
                    title: "State transitions & heartbeats",
                    systemImage: "waveform.path.ecg",
                    trailing: "\(events.count)"
                ) {
                    if events.isEmpty {
                        Text("No recent telemetry events (24h window).")
                            .foregroundStyle(.secondary)
                    } else {
                        VStack(spacing: 0) {
                            ForEach(events) { event in
                                eventRow(event)
                                Divider().opacity(0.4)
                            }
                        }
                    }
                }

                SectionCard(
                    title: "Relief actions",
                    systemImage: "bolt.horizontal.circle",
                    trailing: "\(actions.count)"
                ) {
                    if actions.isEmpty {
                        Text("No audited relief actions in window.")
                            .foregroundStyle(.secondary)
                    } else {
                        VStack(spacing: 0) {
                            ForEach(actions) { action in
                                actionRow(action)
                                Divider().opacity(0.4)
                            }
                        }
                    }
                }
            }
            .padding(20)
        }
        .background(PressureTheme.bg)
    }

    @ViewBuilder
    private var interruptChipCard: some View {
        let forensics = interruptForensics
        SectionCard(
            title: "Wrapper interrupt forensics",
            systemImage: "exclamationmark.shield",
            trailing: forensics.count > 0 ? "\(forensics.count)" : "0"
        ) {
            if forensics.count > 0 {
                HStack(spacing: 10) {
                    StatusChip(label: "wrapper interrupts \(forensics.count)", ok: false)
                    if let rate = forensics.rate {
                        Text(String(format: "%.2f / hour", rate))
                            .font(PressureTheme.monoCaption)
                            .foregroundStyle(.secondary)
                    }
                    Spacer(minLength: 0)
                }
                Text("Counts only — closed cancel outcomes (wrapper_interrupt / signal_interrupt). No argv.")
                    .font(.caption)
                    .foregroundStyle(.tertiary)
            } else {
                Text("No wrapper-interrupt signals in the 24h work/telemetry window.")
                    .foregroundStyle(.secondary)
            }
        }
    }

    private func eventRow(_ event: TelemetryEvent) -> some View {
        HStack(alignment: .top, spacing: 12) {
            if let level = event.snapshot?.level ?? event.summary?.level {
                LevelBadge(level: level, compact: true)
            }
            VStack(alignment: .leading, spacing: 3) {
                Text(event.event ?? "event")
                    .font(.callout.weight(.semibold))
                if let snap = event.snapshot {
                    Text("free \(snap.freePercent)% · cpu \(PressureFormat.percent(snap.hostCPUPercent)) · agents \(PressureFormat.mb(snap.agentRSSSumMB)) · trees \(snap.agentTreeCount)")
                        .font(PressureTheme.monoCaption)
                        .foregroundStyle(.secondary)
                    momentumLine(snap.memoryMomentum, slope: snap.freePercentSlopePerMinute, eta: snap.minutesToMemoryRed)
                } else if let summary = event.summary {
                    Text("free \(summary.freePercent)% · cpu \(PressureFormat.percent(summary.hostCPUPercent)) · agents \(PressureFormat.mb(summary.agentRSSSumMB)) · trees \(summary.agentTreeCount)")
                        .font(PressureTheme.monoCaption)
                        .foregroundStyle(.secondary)
                    if let n = summary.wrapperInterruptOperations, n > 0 {
                        Text("wrapper interrupts \(n)\(summary.wrapperInterruptRatePerHour.map { String(format: " · %.2f/h", $0) } ?? "")")
                            .font(PressureTheme.monoCaption)
                            .foregroundStyle(.orange)
                    }
                    momentumLine(summary.memoryMomentum, slope: summary.freePercentSlopePerMinute, eta: summary.minutesToMemoryRed)
                }
            }
            Spacer()
            Text(PressureFormat.shortTime(event.timestamp))
                .font(PressureTheme.monoCaption)
                .foregroundStyle(.secondary)
        }
        .padding(.vertical, 8)
    }

    @ViewBuilder
    private func momentumLine(_ momentum: MemoryMomentum, slope: Double, eta: Double?) -> some View {
        if momentum != .unknown {
            Text("memory \(momentum.displayName.lowercased()) · \(String(format: "%+.2f pp/min", slope))\(eta.map { String(format: " · red ~%.1f min", $0) } ?? "")")
                .font(PressureTheme.monoCaption)
                .foregroundStyle(.secondary)
        }
    }

    private func actionRow(_ action: PressureAction) -> some View {
        HStack(alignment: .top, spacing: 12) {
            Image(systemName: "bolt.fill")
                .foregroundStyle(PressureTheme.levelColor(.red))
            VStack(alignment: .leading, spacing: 3) {
                Text("\(action.kind ?? "action") · \(action.result ?? "—")")
                    .font(.callout.weight(.semibold))
                Text("\(action.agent ?? "agent") pid \(action.rootPID ?? 0) · \(action.signal ?? "—") · \(PressureFormat.mb(action.rssSumMB ?? 0))")
                    .font(PressureTheme.monoCaption)
                    .foregroundStyle(.secondary)
                if let semantic = action.semanticState {
                    Text("semantic selection: \(semantic.displayName.lowercased())")
                        .font(PressureTheme.monoCaption)
                        .foregroundStyle(.secondary)
                }
                if let executable = action.primaryHostExecutable {
                    Text("host leader: \(executable) · \(PressureFormat.mb(action.primaryHostRSSSumMB ?? 0)) · agent share \(PressureFormat.percent(action.agentRSSSharePercent ?? 0))")
                        .font(PressureTheme.monoCaption)
                        .foregroundStyle(.secondary)
                }
                if let scope = action.reliefScope {
                    Text("relief authority: \(scope.replacingOccurrences(of: "_", with: " "))")
                        .font(PressureTheme.monoCaption)
                        .foregroundStyle(.secondary)
                }
                if let reason = action.reason, !reason.isEmpty {
                    Text(reason)
                        .font(.caption)
                        .foregroundStyle(.secondary)
                }
            }
            Spacer()
            Text(PressureFormat.shortTime(action.timestamp))
                .font(PressureTheme.monoCaption)
                .foregroundStyle(.secondary)
        }
        .padding(.vertical, 8)
    }
}
