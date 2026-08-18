import AppKit
import SwiftUI
import NDevPressureCore

/// Compact menu bar extra for at-a-glance pressure without opening the full window.
struct MenuBarLabel: View {
    @EnvironmentObject private var store: PressureStore

    var body: some View {
        Image(systemName: iconName)
            .symbolRenderingMode(.palette)
            .foregroundStyle(PressureTheme.levelColor(store.board.level), .primary)
            .help("NDev Pressure · \(store.board.level.displayName) · used \(store.board.snapshot.utilization.memoryUsedPercent)%. \(PressureHelp.level(store.board.level))")
    }

    private var iconName: String {
        if store.board.level == .unknown {
            return "gauge.with.dots.needle.bottom.50percent.badge.minus"
        }
        return store.board.snapshot.utilization.menuBarGaugeSymbol
    }
}

struct MenuBarContent: View {
    @EnvironmentObject private var store: PressureStore
    @Environment(\.openWindow) private var openWindow

    private var snap: PressureSnapshot { store.board.snapshot }

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            HStack {
                LevelBadge(level: snap.level)
                Spacer()
                Text(PressureFormat.relative(store.board.refreshedAt))
                    .font(.caption2)
                    .foregroundStyle(.secondary)
            }
            .padding(.horizontal, 12)
            .padding(.vertical, 10)

            Divider()

            grid
                .padding(12)

            if let reason = snap.reasons.first {
                Text(reason)
                    .font(.caption2)
                    .foregroundStyle(.secondary)
                    .lineLimit(2)
                    .padding(.horizontal, 12)
                    .padding(.bottom, 8)
            }

            if let suggestion = store.policySuggestion {
                Button {
                    store.copyToPasteboard(suggestion.agentPaste)
                } label: {
                    Label("Copy \(suggestion.headline) brief", systemImage: "doc.on.doc")
                        .frame(maxWidth: .infinity, alignment: .leading)
                }
                .buttonStyle(.plain)
                .padding(.horizontal, 12)
                .padding(.bottom, 6)
                .help(PressureHelp.copyForAgent)

                Button {
                    store.selectedSection = .overview
                    openWindow(id: "main")
                    NSApp.activate(ignoringOtherApps: true)
                } label: {
                    Label("Review \(suggestion.headline)", systemImage: "lightbulb")
                        .frame(maxWidth: .infinity, alignment: .leading)
                }
                .buttonStyle(.plain)
                .padding(.horizontal, 12)
                .padding(.bottom, 8)
                .help(PressureHelp.policySuggestion)
            }

            Divider()

            Button {
                openWindow(id: "main")
                NSApp.activate(ignoringOtherApps: true)
            } label: {
                Label("Open NDev Pressure", systemImage: "macwindow")
                    .frame(maxWidth: .infinity, alignment: .leading)
            }
            .buttonStyle(.plain)
            .padding(.horizontal, 12)
            .padding(.vertical, 8)
            .help("Open the full multi-pane pressure console.")

            Button {
                Task { await store.refresh(live: false, light: true) }
            } label: {
                Label("Refresh", systemImage: "arrow.clockwise")
                    .frame(maxWidth: .infinity, alignment: .leading)
            }
            .buttonStyle(.plain)
            .padding(.horizontal, 12)
            .padding(.vertical, 6)
            .help(PressureHelp.refresh)

            Button {
                Task { await store.refresh(live: true, light: false) }
            } label: {
                Label("Live sample", systemImage: "bolt.horizontal.circle")
                    .frame(maxWidth: .infinity, alignment: .leading)
            }
            .buttonStyle(.plain)
            .padding(.horizontal, 12)
            .padding(.vertical, 6)
            .help(PressureHelp.liveSample)

            Divider()

            Button {
                store.openStorage(tab: .disk)
                openWindow(id: "main")
                NSApp.activate(ignoringOtherApps: true)
            } label: {
                Label("Open Storage", systemImage: "externaldrive.badge.checkmark")
                    .frame(maxWidth: .infinity, alignment: .leading)
            }
            .buttonStyle(.plain)
            .padding(.horizontal, 12)
            .padding(.vertical, 8)
            .help(PressureHelp.storageBeginSafeReclaim)

            Button {
                store.selectedSection = .policy
                openWindow(id: "main")
                NSApp.activate(ignoringOtherApps: true)
            } label: {
                Label("Open Policy Controls", systemImage: "shield.lefthalf.filled")
                    .frame(maxWidth: .infinity, alignment: .leading)
            }
            .buttonStyle(.plain)
            .padding(.horizontal, 12)
            .padding(.vertical, 8)
            .help("Open the confirmed policy controls. The menu bar never changes process authority directly.")

            Divider()

            Button("Quit NDev Pressure") {
                NSApp.terminate(nil)
            }
            .buttonStyle(.plain)
            .padding(.horizontal, 12)
            .padding(.vertical, 8)
        }
        .frame(width: 300)
    }

    private var grid: some View {
        VStack(alignment: .leading, spacing: 6) {
            row("Used mem", PressureFormat.percentInt(snap.utilization.memoryUsedPercent))
            row("Free mem", PressureFormat.percentInt(snap.freePercent))
            row("Host CPU", PressureFormat.percent(snap.hostCPUPercent))
            row("Swap", PressureFormat.mb(snap.swapUsedMB))
            row("Agent RSS", PressureFormat.mb(snap.agentRSSSumMB))
            row("Trees", "\(snap.agentTreeCount)")
            if let writes = store.diskWriteSummary ?? snap.diskWrite {
                row("Disk writes", "\(writes.state.displayName) · \(PressureFormat.bytes(Int64(clamping: writes.window15mBytes)))/15m")
            }
            if let work = store.board.work {
                row("Work", "\(work.used)/\(work.capacity) · q \(work.queueDepth)")
            }
            row("Storage", "\(snap.storage.level.shortLabel) · \(PressureFormat.bytes(snap.storage.availableBytes))")
            row("Mode", store.board.policy?.modeLabel ?? "—")
            if let admission = store.board.admission {
                row("Admission", admission.allowed ? "allowed" : "blocked")
            }
        }
        .font(PressureTheme.monoCaption)
    }

    private func row(_ title: String, _ value: String) -> some View {
        HStack {
            Text(title)
                .foregroundStyle(.secondary)
            Spacer()
            Text(value)
        }
    }
}
