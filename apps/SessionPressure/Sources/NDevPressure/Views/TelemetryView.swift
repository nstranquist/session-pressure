import SwiftUI
import NDevPressureCore

struct TelemetryView: View {
    @EnvironmentObject private var store: PressureStore

    private var events: [TelemetryEvent] { store.board.telemetryEvents }
    private var actions: [PressureAction] { store.board.reliefActions }

    private var interruptForensics: WorkCancellationTelemetry? {
        guard store.board.sectionErrors["calibration"] == nil else { return nil }
        return WorkCancellationTelemetry(calibration: store.board.calibration)
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

    private var interruptChipCard: some View {
        SectionCard(
            title: "Interruptions & wait timeouts",
            systemImage: "clock.arrow.circlepath",
            trailing: "Last 24 hours"
        ) {
            if let counts = interruptForensics {
                HStack(spacing: 16) {
                    Text("Signal cancellations: \(counts.signalCancellations)")
                    Text("Wait timeouts: \(counts.waitTimeouts)")
                }
                .font(PressureTheme.monoCaption)
                if counts.unspecifiedInterruptions > 0 {
                    Text("Unspecified interruptions: \(counts.unspecifiedInterruptions)")
                        .font(PressureTheme.monoCaption)
                    Text(PressureHelp.legacyInterruptions)
                        .font(.caption)
                        .foregroundStyle(.secondary)
                }
                if counts.total == 0 {
                    Text("No interruptions or wait timeouts in the last 24 hours.")
                        .foregroundStyle(.secondary)
                }
                Text(PressureHelp.workCancellationHistory)
                    .font(.caption)
                    .foregroundStyle(.secondary)
            } else {
                Text("Current interruption counts unavailable. Refresh to retry.")
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
                        Text("Interrupt/cancel count \(n)\(summary.wrapperInterruptRatePerHour.map { String(format: " · %.2f/h", $0) } ?? "")")
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
