import AppKit
import SwiftUI
import NDevPressureCore

struct ContentView: View {
    @EnvironmentObject private var store: PressureStore
    @Environment(\.scenePhase) private var scenePhase

    var body: some View {
        NavigationSplitView {
            sidebar
                .navigationSplitViewColumnWidth(min: 200, ideal: 220, max: 280)
        } detail: {
            detail
        }
        .frame(minWidth: 980, minHeight: 640)
        .toolbar { toolbar }
        .task { store.start() }
        .onAppear { store.setWindowOpen(true) }
        .onDisappear {
            // Do NOT stop the store here. The app deliberately outlives its
            // window to serve the menu-bar extra, and stopping the poll left
            // that gauge and the dock badge frozen at whatever the pressure
            // happened to be when the window closed.
            store.setWindowOpen(false)
        }
        .onChange(of: scenePhase, initial: true) { _, phase in
            store.setApplicationActive(phase == .active)
        }
        .onOpenURL { store.handleDeepLink($0) }
        .onReceive(NotificationCenter.default.publisher(for: NSWindow.didMiniaturizeNotification)) { _ in
            store.setWindowVisible(false)
        }
        .onReceive(NotificationCenter.default.publisher(for: NSWindow.didDeminiaturizeNotification)) { _ in
            store.setWindowVisible(true)
        }
    }

    private var sidebar: some View {
        VStack(spacing: 0) {
            HStack(spacing: 10) {
                LevelBadge(level: store.board.level, compact: true)
                VStack(alignment: .leading, spacing: 2) {
                    Text("NDev Pressure")
                        .font(.headline)
                    Text(store.board.policy?.modeLabel ?? "—")
                        .font(.caption2)
                        .foregroundStyle(.secondary)
                        .lineLimit(1)
                }
                Spacer()
            }
            .padding(.horizontal, 14)
            .padding(.vertical, 12)

            Divider()

            List(selection: $store.selectedSection) {
                ForEach(PressureStore.Section.allCases) { section in
                    Label {
                        HStack {
                            Text(section.rawValue)
                            Spacer()
                            Text(section.shortcutLabel)
                                .font(.caption.monospaced())
                                .foregroundStyle(.tertiary)
                        }
                    } icon: {
                        Image(systemName: section.systemImage)
                    }
                    .tag(section)
                    .help(PressureHelp.section(section.rawValue) + " \(section.shortcutLabel). Remap in System Settings → Keyboard → App Shortcuts using this menu title.")
                }
            }
            .listStyle(.sidebar)

            Divider()

            VStack(alignment: .leading, spacing: 6) {
                HStack(spacing: 6) {
                    Circle()
                        .fill(store.isRefreshing ? PressureTheme.levelColor(.warning) : PressureTheme.levelColor(.normal))
                        .frame(width: 7, height: 7)
                    Text(store.isRefreshing ? "Refreshing…" : "Live")
                        .font(.caption)
                    Spacer()
                    if store.isRefreshing {
                        ProgressView().controlSize(.mini)
                    }
                }
                .help(store.isRefreshing
                    ? "Refreshing ndev session pressure contracts…"
                    : store.workFocusPollActive
                        ? "Full board adaptive poll plus Work Queue focus poll (~2.5s work status)."
                        : store.diskFocusPollActive
                            ? "Compact board poll plus one bounded live disk-write sample every 15 seconds."
                            : "Adaptive poll active. Interval shortens under warning/red/critical.")
                Text(summaryLine)
                    .font(PressureTheme.monoCaption)
                    .foregroundStyle(.secondary)
                    .lineLimit(2)
                    .help("Compact free memory · host CPU · agent tree count from the latest board.")
            }
            .padding(12)
            .help(store.binaryPath.isEmpty ? PressureHelp.binaryPath : "Using \(store.binaryPath)")
        }
        .background(.thinMaterial)
    }

    @ViewBuilder
    private var detail: some View {
        switch store.selectedSection {
        case .overview: OverviewView()
        case .trees: TreesView()
        case .diskWrites: DiskWritesView()
        case .storage: StorageView()
        case .work: WorkView()
        case .policy: PolicyView()
        case .monitor: MonitorView()
        case .telemetry: TelemetryView()
        }
    }

    @ToolbarContentBuilder
    private var toolbar: some ToolbarContent {
        ToolbarItemGroup(placement: .primaryAction) {
            if let message = store.statusMessage ?? store.busyAction {
                Text(message)
                    .font(.caption)
                    .foregroundStyle(.secondary)
            }
            if let error = store.lastError {
                // Unconfirmed actions (Run now, Run all) fail silently otherwise:
                // a waiter can leave the queue between a 2.5s poll and the click,
                // and an icon-only glyph is not a report of that.
                Button {
                    store.lastError = nil
                } label: {
                    Label(error, systemImage: "exclamationmark.triangle.fill")
                        .lineLimit(1)
                        .truncationMode(.middle)
                        .foregroundStyle(PressureTheme.levelColor(.warning))
                }
                .frame(maxWidth: 420)
                .help("\(error)\n\nClick to dismiss.")
            }
            Button {
                Task { await store.refresh(live: false, light: false) }
            } label: {
                Label("Refresh", systemImage: "arrow.clockwise")
            }
            .disabled(store.isRefreshing)
            .keyboardShortcut("r", modifiers: [.command])
            .help(PressureHelp.refresh + " (⌘R)")

            Button {
                Task { await store.refresh(live: true, light: false) }
            } label: {
                Label("Live sample", systemImage: "bolt.horizontal.circle")
            }
            .disabled(store.isRefreshing)
            .keyboardShortcut("r", modifiers: [.command, .shift])
            .help(PressureHelp.liveSample + " (⌘⇧R)")
        }
    }

    private var summaryLine: String {
        let s = store.board.snapshot
        return "free \(s.freePercent)% · cpu \(Int(s.hostCPUPercent.rounded()))% · \(s.agentTreeCount) trees"
    }
}
