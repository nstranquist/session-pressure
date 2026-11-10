import AppKit
import NDevPressureCore
import ServiceManagement
import SwiftUI

// MARK: - Containers

enum SettingsSection: String, CaseIterable, Identifiable {
    case general
    case menuBar
    case sidebar
    case keyboard
    case alerts
    case refresh
    case controlPlane
    case guardConfig
    case diagnostics
    case about

    var id: String { rawValue }
    var title: String {
        switch self {
        case .general: "General"
        case .menuBar: "Menu bar & Dock"
        case .sidebar: "Sidebar"
        case .keyboard: "Keyboard"
        case .alerts: "Alerts"
        case .refresh: "Refresh"
        case .controlPlane: "Control Plane"
        case .guardConfig: "Guard"
        case .diagnostics: "Diagnostics"
        case .about: "About & Help"
        }
    }
    var systemImage: String {
        switch self {
        case .general: "gearshape"
        case .menuBar: "macwindow"
        case .sidebar: "sidebar.left"
        case .keyboard: "keyboard"
        case .alerts: "bell.badge"
        case .refresh: "arrow.clockwise"
        case .controlPlane: "terminal"
        case .guardConfig: "shield.lefthalf.filled"
        case .diagnostics: "stethoscope"
        case .about: "questionmark.circle"
        }
    }
}

/// Sidebar Settings pane (⌘9): left rail + scrollable single page.
/// All sections are visible in one `ScrollView` (so you can see everything)
/// and the left rail is a jump list — click to scroll to a section, and
/// scrolling the page updates the highlighted rail item (scroll-spy).
///
/// Scroll-spy is intentionally robust: it tracks minY for every row and picks
/// the section whose top has most recently passed a small viewport threshold.
/// A 100ms hold prevents fast flapping from reversing the highlight.
struct AppSettingsView: View {
    @State private var selected: SettingsSection = .general
    @State private var isProgrammaticScroll = false
    @State private var pendingCandidate: SettingsSection?
    @State private var pendingWorkItem: DispatchWorkItem?
    @State private var lastOffsets: [SettingsSection: CGFloat] = [:]
    @State private var lastScrollDirection: Int = 0

    var body: some View {
        NavigationSplitView {
            List(selection: $selected) {
                ForEach(SettingsSection.allCases) { sec in
                    Label(sec.title, systemImage: sec.systemImage)
                        .tag(sec)
                }
            }
            .listStyle(.sidebar)
            .navigationTitle("Settings")
            .navigationSplitViewColumnWidth(min: 180, ideal: 210, max: 260)
        } detail: {
            ScrollViewReader { proxy in
                ScrollView(.vertical, showsIndicators: true) {
                    VStack(alignment: .leading, spacing: 22) {
                        ForEach(SettingsSection.allCases) { sec in
                            SettingsDetailRow(sec: sec, proxy: proxy)
                                .id(sec)
                                .background {
                                    GeometryReader { geo in
                                        Color.clear
                                            .preference(key: SectionTopPreferenceKey.self,
                                                      value: [sec: geo.frame(in: .named("AppSettingsScroll")).minY])
                                    }
                                }
                        }
                    }
                    .padding(16)
                }
                .coordinateSpace(name: "AppSettingsScroll")
                .background {
                    Color(nsColor: .windowBackgroundColor).ignoresSafeArea()
                }
                .onReceive(NotificationCenter.default.publisher(for: Notification.Name("OpenSettingsToAbout"))) { _ in
                    selected = .about
                }
                .onPreferenceChange(SectionTopPreferenceKey.self) { offsets in
                    guard !isProgrammaticScroll else { return }
                    guard !offsets.isEmpty else { return }
                    // Scroll direction from aggregate offset movement: content
                    // moving up (offsets decreasing) is scrolling down.
                    if !lastOffsets.isEmpty {
                        let prevSum = lastOffsets.values.reduce(0, +)
                        let nowSum = offsets.values.reduce(0, +)
                        let delta = nowSum - prevSum
                        if delta < -2 { lastScrollDirection = 1 } // down
                        else if delta > 2 { lastScrollDirection = -1 } // up
                        else { lastScrollDirection = 0 }
                    }
                    lastOffsets = offsets

                    // The active section is the one whose top has most recently
                    // moved past 70pt from the viewport top. Among all rows with
                    // minY <= 70, the largest minY is the current one — even if
                    // scrolled far, earlier rows are far negative.
                    let eligible = offsets.filter { $0.value <= 70 }
                    guard let best = eligible.max(by: { $0.value < $1.value })?.key else { return }
                    guard best != selected else { return }

                    // Direction gate: while fast-scrolling down, ignore candidates
                    // that would move the rail backwards (and vice versa). This
                    // stops the inversion where a flick temporarily reports an
                    // earlier section as eligible from a stale layout pass.
                    let all = SettingsSection.allCases
                    if let curIdx = all.firstIndex(of: selected), let bestIdx = all.firstIndex(of: best) {
                        if lastScrollDirection == 1 && bestIdx < curIdx { return }
                        if lastScrollDirection == -1 && bestIdx > curIdx { return }
                    }

                    // Hold 120ms (not 80ms) to absorb fast-scroll batches before
                    // committing. Stale candidates are ignored if direction flips.
                    pendingWorkItem?.cancel()
                    pendingCandidate = best
                    let capturedDirection = lastScrollDirection
                    let work = DispatchWorkItem {
                        // Don't apply stale candidates that were superseded or
                        // that now disagree with the live scroll direction.
                        guard pendingCandidate == best, !isProgrammaticScroll else {
                            pendingCandidate = nil
                            return
                        }
                        if capturedDirection != 0 && capturedDirection != lastScrollDirection {
                            pendingCandidate = nil
                            return
                        }
                        withAnimation(.easeOut(duration: 0.12)) { selected = best }
                        pendingCandidate = nil
                    }
                    pendingWorkItem = work
                    DispatchQueue.main.asyncAfter(deadline: .now() + 0.12, execute: work)
                }
                .onChange(of: selected) { _, newValue in
                    pendingWorkItem?.cancel()
                    pendingCandidate = nil
                    // Clicking the rail jumps the page; suppress spy for the animation window.
                    isProgrammaticScroll = true
                    withAnimation(.easeInOut(duration: 0.18)) {
                        proxy.scrollTo(newValue, anchor: .top)
                    }
                    DispatchQueue.main.asyncAfter(deadline: .now() + 0.22) {
                        isProgrammaticScroll = false
                    }
                }
            }
            .navigationTitle(selected.title)
        }
        .frame(minWidth: 680, minHeight: 520)
    }
}

private struct SectionTopPreferenceKey: PreferenceKey {
    static let defaultValue: [SettingsSection: CGFloat] = [:]
    static func reduce(value: inout [SettingsSection: CGFloat], nextValue: () -> [SettingsSection: CGFloat]) {
        for (k, v) in nextValue() { value[k] = v }
    }
}

private struct SettingsDetailRow: View {
    let sec: SettingsSection
    let proxy: ScrollViewProxy

    var body: some View {
        VStack(alignment: .leading, spacing: 10) {
            switch sec {
            case .general: GeneralSettingsSection()
            case .menuBar: MenuBarDockSettingsSection()
            case .sidebar: SidebarSettingsSection()
            case .keyboard: KeyboardSettingsSection()
            case .alerts: AlertSettingsSection()
            case .refresh: RefreshSettingsSection()
            case .controlPlane: ControlPlaneSettingsSection()
            case .guardConfig: GuardConfigurationSection()
            case .diagnostics: DiagnosticsSettingsSection()
            case .about: HelpView()
            }
        }
        .padding(14)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(PressureTheme.card, in: RoundedRectangle(cornerRadius: 12, style: .continuous))
        .overlay(
            RoundedRectangle(cornerRadius: 12, style: .continuous)
                .stroke(PressureTheme.hairline, lineWidth: 1)
        )
    }
}

/// The standard macOS Settings window (⌘,): the same sections, as tabs.
struct SettingsWindowView: View {
    var body: some View {
        TabView {
            tab { GeneralSettingsSection(); MenuBarDockSettingsSection(); SidebarSettingsSection(); KeyboardSettingsSection() }
                .tabItem { Label("General", systemImage: "gearshape") }
            tab { AlertSettingsSection(); RefreshSettingsSection() }
                .tabItem { Label("Alerts", systemImage: "bell.badge") }
            tab { ControlPlaneSettingsSection() }
                .tabItem { Label("Control Plane", systemImage: "terminal") }
            tab { GuardConfigurationSection() }
                .tabItem { Label("Guard", systemImage: "shield.lefthalf.filled") }
            tab { DiagnosticsSettingsSection() }
                .tabItem { Label("Diagnostics", systemImage: "stethoscope") }
        }
        .frame(width: 600, height: 520)
    }

    private func tab<Content: View>(@ViewBuilder _ content: () -> Content) -> some View {
        Form { content() }.formStyle(.grouped)
    }
}

// MARK: - General

struct GeneralSettingsSection: View {
    @AppStorage(AppPreferenceKey.launchPane) private var launchPane = PressureStore.Section.overview.rawValue
    @StateObject private var loginItem = LoginItemModel()

    var body: some View {
        Section("General") {
            Picker("Open on launch", selection: $launchPane) {
                Text("Last used pane").tag("lastUsed")
                Divider()
                ForEach(PressureStore.Section.allCases) { section in
                    Text(section.rawValue).tag(section.rawValue)
                }
            }
            .help(PressureHelp.launchPane)

            Toggle("Launch at login", isOn: Binding(
                get: { loginItem.isOn },
                set: { loginItem.set($0) }
            ))
            .disabled(!loginItem.isAvailable)
            .help(PressureHelp.launchAtLogin)
            if let note = loginItem.note {
                Text(note).font(.caption).foregroundStyle(.secondary)
            }
            if let error = loginItem.error {
                Text(error).font(.caption).foregroundStyle(.red)
            }
        }
        .onAppear { loginItem.reload() }
    }
}

/// Launch at login, read from the live `SMAppService` status (never a stored
/// bool, so it cannot disagree with System Settings). Default is off.
@MainActor
final class LoginItemModel: ObservableObject {
    @Published private(set) var status: SMAppService.Status = .notRegistered
    @Published private(set) var error: String?

    var isAvailable: Bool { Bundle.main.bundleURL.pathExtension == "app" }
    var isOn: Bool { status == .enabled || status == .requiresApproval }

    var note: String? {
        if !isAvailable { return "Available only in the installed app bundle." }
        if status == .requiresApproval { return "Waiting for approval in System Settings → General → Login Items." }
        return nil
    }

    func reload() {
        guard isAvailable else { return }
        status = SMAppService.mainApp.status
    }

    func set(_ on: Bool) {
        guard isAvailable else { return }
        do {
            if on { try SMAppService.mainApp.register() } else { try SMAppService.mainApp.unregister() }
            error = nil
        } catch {
            self.error = error.localizedDescription
        }
        reload()
        if status == .requiresApproval { SMAppService.openSystemSettingsLoginItems() }
    }
}

// MARK: - Menu bar and Dock

struct MenuBarDockSettingsSection: View {
    @EnvironmentObject private var store: PressureStore
    @AppStorage(AppPreferenceKey.showMenuBarExtra) private var showMenuBarExtra = true
    @AppStorage(AppPreferenceKey.dockBadgeMode) private var dockBadgeMode = DockBadgeMode.default.rawValue

    var body: some View {
        Section {
            Toggle("Show in menu bar", isOn: $showMenuBarExtra)
                .help(PressureHelp.showMenuBarExtra)
            Picker("Dock badge", selection: $dockBadgeMode) {
                ForEach(DockBadgeMode.allCases) { Text($0.displayName).tag($0.rawValue) }
            }
            .help(PressureHelp.dockBadge)
            .onChange(of: dockBadgeMode) { _, _ in store.preferencesChanged(rebuildClient: false) }
        } header: {
            Text("Menu bar & Dock")
        } footer: {
            Text("To show the menu bar icon again, open NDev Pressure from the Dock or Applications, then choose Settings (⌘9 or ⌘,).")
        }
    }
}

// MARK: - Alerts

struct AlertSettingsSection: View {
    @EnvironmentObject private var store: PressureStore

    var body: some View {
        AlertSettingsContent(alerts: store.alerts)
    }
}

/// Observes the notifier directly so permission and last-alert state update live.
private struct AlertSettingsContent: View {
    @ObservedObject var alerts: LevelAlertNotifier
    @AppStorage(AppPreferenceKey.alertThreshold) private var threshold = AlertThreshold.default.rawValue

    var body: some View {
        Section {
            Picker("Notify when pressure reaches", selection: $threshold) {
                ForEach(AlertThreshold.allCases) { Text($0.displayName).tag($0.rawValue) }
            }
            .help(PressureHelp.alertThreshold)
            .onChange(of: threshold) { _, next in
                guard next != AlertThreshold.off.rawValue else { return }
                Task { await alerts.requestAuthorization() }
            }
            if alerts.isAvailable {
                LabeledContent("macOS permission", value: alerts.authorizationLabel)
                HStack {
                    Button("Send test alert") {
                        Task {
                            await alerts.requestAuthorization()
                            alerts.sendTest()
                        }
                    }
                    Button("Notification Settings…") { LevelAlertNotifier.openNotificationSettings() }
                }
                if let last = alerts.lastAlertAt {
                    LabeledContent("Last alert", value: PressureFormat.relative(last))
                }
            } else {
                Text("Notifications are available only in the installed app bundle.")
                    .font(.caption).foregroundStyle(.secondary)
            }
        } header: {
            Text("Alerts")
        } footer: {
            Text("Only rising transitions alert, at most once per level every 10 minutes. App launch never alerts.")
        }
        .task { await alerts.refreshAuthorization() }
    }
}

// MARK: - Refresh

struct RefreshSettingsSection: View {
    @EnvironmentObject private var store: PressureStore
    @AppStorage(AppPreferenceKey.refreshProfile) private var profile = RefreshProfile.default.rawValue

    private var selected: RefreshProfile { RefreshProfile(rawValue: profile) ?? .default }

    var body: some View {
        Section {
            Picker("Refresh profile", selection: $profile) {
                ForEach(RefreshProfile.allCases) { Text($0.displayName).tag($0.rawValue) }
            }
            .help(PressureHelp.refreshProfile)
            .onChange(of: profile) { _, _ in store.preferencesChanged(rebuildClient: false) }
            Text(PressureFormat.cadenceSummary(selected))
                .font(PressureTheme.monoCaption)
                .foregroundStyle(.secondary)
                .textSelection(.enabled)
        } header: {
            Text("Refresh")
        } footer: {
            Text("App reads only. The resident guard keeps its own sample cadence (see Guard configuration).")
        }
    }
}

// MARK: - Sidebar

struct SidebarSettingsSection: View {
    @EnvironmentObject private var store: PressureStore
    @AppStorage(AppPreferenceKey.showDayGate) private var showDayGate = true
    @AppStorage(AppPreferenceKey.showHetzner) private var showHetzner = true

    var body: some View {
        Section("Sidebar") {
            Toggle("Show Day Gate", isOn: $showDayGate)
            Toggle("Show Hetzner", isOn: $showHetzner)
        }
        .help(PressureHelp.sidebarPanes)
        .onChange(of: showDayGate) { _, _ in store.preferencesChanged(rebuildClient: false) }
        .onChange(of: showHetzner) { _, _ in store.preferencesChanged(rebuildClient: false) }
    }
}

// MARK: - Control plane

struct ControlPlaneSettingsSection: View {
    @EnvironmentObject private var store: PressureStore
    @AppStorage(AppPreferenceKey.apiTimeoutSeconds) private var apiTimeout = 0
    @State private var showOperations = false

    private var base: [String: String] { ProcessInfo.processInfo.environment }

    var body: some View {
        Section {
            PathOverrideRow(
                title: "session-pressure binary",
                key: AppPreferenceKey.pressureBinaryOverride,
                resolved: store.binaryPath.isEmpty ? nil : store.binaryPath,
                source: source(AppPreferenceKey.pressureBinaryOverride, ["SESSION_PRESSURE_BIN", "NDEV_PRESSURE_BIN"], resolved: !store.binaryPath.isEmpty),
                help: PressureHelp.pressureBinaryOverride,
                requireExecutable: true
            )
            PathOverrideRow(
                title: "ndev binary",
                key: AppPreferenceKey.ndevBinaryOverride,
                resolved: store.advisorBinaryPath,
                source: source(AppPreferenceKey.ndevBinaryOverride, ["NDEV_BIN", "NDEV_PATH"], resolved: store.advisorBinaryPath != nil),
                help: PressureHelp.ndevBinaryOverride,
                requireExecutable: true
            )
        } header: {
            Text("Control plane")
        } footer: {
            Text("Saved paths win over environment variables. Apps opened from Finder or the Dock do not see your shell environment, so set overrides here.")
        }

        Section("Local API") {
            VStack(alignment: .leading, spacing: 4) {
                Text("Transport")
                Text(NDevPressureAPIClient.transport(environment: ClientFactory.environment()).summary)
                    .font(PressureTheme.monoCaption)
                    .foregroundStyle(.secondary)
                    .textSelection(.enabled)
                    .frame(maxWidth: .infinity, alignment: .leading)
            }
            .help(PressureHelp.apiTransport)
            PathOverrideRow(title: "Socket", key: AppPreferenceKey.apiSocketOverride, resolved: nil, source: nil,
                            help: PressureHelp.apiTransport, requireExecutable: false, placeholder: "Automatic (~/.nicos-dev/session-pressure/api/…)")
            PathOverrideRow(title: "URL", key: AppPreferenceKey.apiURLOverride, resolved: nil, source: nil,
                            help: PressureHelp.apiTransport, requireExecutable: false, placeholder: "http://127.0.0.1:port", choosesFile: false)
            PathOverrideRow(title: "Token file", key: AppPreferenceKey.apiTokenFileOverride, resolved: nil, source: nil,
                            help: PressureHelp.apiTokenFile, requireExecutable: false, placeholder: "None")
            Stepper(value: $apiTimeout, in: 0...AppPreferences.apiTimeoutRange.upperBound) {
                LabeledContent("Request timeout", value: apiTimeout == 0 ? "Default (10 s)" : "\(apiTimeout) s")
            }
            .help(PressureHelp.apiTimeout)
            .onChange(of: apiTimeout) { _, _ in store.preferencesChanged(rebuildClient: true) }
        }

        if !store.preferenceProblems.isEmpty {
            Section("Ignored overrides") {
                ForEach(store.preferenceProblems, id: \.self) { problem in
                    Label(problem.message, systemImage: "exclamationmark.triangle.fill")
                        .foregroundStyle(.orange)
                        .font(.caption)
                }
            }
        }

        Section("Executing product") {
            if let identity = store.capabilities {
                LabeledContent("Version", value: identity.version)
                LabeledContent("Source revision", value: identity.sourceRevision ?? "Unknown")
                LabeledContent("Executable") { Text(identity.executable).font(PressureTheme.monoCaption).textSelection(.enabled) }
                LabeledContent("Platform", value: "\(identity.os)/\(identity.arch)")
                LabeledContent("Native sampling", value: identity.nativeSampling ? "Yes" : "No")
                DisclosureGroup("Supported operations (\(identity.operations.count))", isExpanded: $showOperations) {
                    Text(identity.operations.joined(separator: ", "))
                        .font(.caption).foregroundStyle(.secondary).textSelection(.enabled)
                }
            } else {
                Text("Product identity has not been read.").foregroundStyle(.secondary)
            }
            if let error = store.identityError { Text(error).foregroundStyle(.red).font(.caption) }
            Button("Refresh product identity") { Task { await store.refreshIdentity() } }
        }
    }

    private func source(_ key: String, _ env: [String], resolved: Bool) -> PreferenceSource {
        let s = AppPreferences.source(savedKey: key, envNames: env, base: base, problems: store.preferenceProblems)
        return s == .automatic && !resolved ? .missing : s
    }
}

/// One saved path/URL override: edit, Choose…, Clear; commits on Return or Apply
/// so the client is not rebuilt on every keystroke.
struct PathOverrideRow: View {
    @EnvironmentObject private var store: PressureStore
    let title: String
    let key: String
    let resolved: String?
    let source: PreferenceSource?
    let help: String
    let requireExecutable: Bool
    var placeholder: String = "Automatic"
    var choosesFile: Bool = true

    @State private var draft = ""

    private var saved: String { UserDefaults.standard.string(forKey: key) ?? "" }

    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            HStack {
                Text(title)
                Spacer()
                if let source {
                    Text(source.rawValue)
                        .font(.caption)
                        .foregroundStyle(source == .missing ? .red : .secondary)
                }
            }
            HStack {
                TextField(title, text: $draft, prompt: Text(placeholder))
                    .labelsHidden()
                    .textFieldStyle(.roundedBorder)
                    .font(PressureTheme.monoCaption)
                    .onSubmit(commit)
                if draft != saved {
                    Button("Apply", action: commit)
                }
                if choosesFile {
                    Button("Choose…", action: choose)
                }
                Button("Clear") { draft = ""; commit() }
                    .disabled(saved.isEmpty && draft.isEmpty)
            }
            if let resolved {
                Text("In use: \(resolved)")
                    .font(PressureTheme.monoCaption)
                    .foregroundStyle(.secondary)
                    .textSelection(.enabled)
                    .lineLimit(2)
            }
        }
        .help(help)
        .onAppear { draft = saved }
        .onChange(of: saved) { _, next in draft = next }
    }

    private func commit() {
        let value = draft.trimmingCharacters(in: .whitespacesAndNewlines)
        if value.isEmpty {
            UserDefaults.standard.removeObject(forKey: key)
        } else {
            UserDefaults.standard.set(value, forKey: key)
        }
        draft = value
        store.preferencesChanged(rebuildClient: true)
    }

    private func choose() {
        let panel = NSOpenPanel()
        panel.canChooseFiles = true
        panel.canChooseDirectories = false
        panel.allowsMultipleSelection = false
        panel.showsHiddenFiles = true
        panel.treatsFilePackagesAsDirectories = true
        panel.message = requireExecutable ? "Choose an executable for \(title)." : "Choose \(title)."
        if panel.runModal() == .OK, let url = panel.url {
            draft = url.path
            commit()
        }
    }
}

// MARK: - Guard configuration (read-only)

struct GuardConfigurationSection: View {
    @EnvironmentObject private var store: PressureStore

    var body: some View {
        Section {
            if let policy = store.board.policy {
                LabeledContent("Mode", value: policy.modeLabel)
                HStack(spacing: 8) {
                    StatusChip(label: "Guard", ok: policy.enabled, detail: policy.enabled ? "on" : "off")
                    StatusChip(label: "Admission", ok: policy.enforceAdmission, detail: policy.enforceAdmission ? "enforced" : "observe")
                    StatusChip(label: "Auto-shed", ok: true, detail: policy.autoShedCritical ? "at critical" : "off",
                               help: "Automatic shedding of one old quiescent tree at sustained critical.")
                }
                if let blockAt = policy.blockNewAt, !blockAt.isEmpty {
                    LabeledContent("Block new launches at", value: blockAt.capitalized)
                }
                LabeledContent("Guard sample interval", value: sampleSummary(policy))
            } else {
                Text("Guard policy has not been read yet.").foregroundStyle(.secondary)
            }

            if let disk = store.diskWritePolicy ?? store.board.policy?.diskWrite {
                LabeledContent("Disk-write watchdog",
                               value: disk.enabled ? "On · \(disk.profile) · every \(disk.sampleIntervalSeconds) s" : "Off")
            }
            if let storage = store.storagePolicy {
                LabeledContent("Storage policy",
                               value: storage.enabled
                                   ? "On · \(storage.enforceAdmission ? "enforced" : "observe") · target free \(PressureFormat.bytes(storage.targetFreeBytes))"
                                   : "Off")
            }

            if let launchd = store.board.launchd {
                HStack(spacing: 8) {
                    StatusChip(label: "LaunchAgent", ok: launchd.installed && launchd.loaded,
                               detail: launchd.installed ? (launchd.loaded ? "loaded" : "not loaded") : "not installed")
                    StatusChip(label: "Artifact", ok: launchd.artifactVerified ?? false,
                               detail: (launchd.artifactVerified ?? false) ? "verified" : "unverified")
                    StatusChip(label: "PID", ok: (launchd.pid ?? 0) > 0, detail: PressureFormat.pid(launchd.pid))
                }
            }
            LabeledContent("Disk-trace helper", value: PrivilegedTraceClient.helperStatusLabel)

            if let at = store.board.sectionRefreshedAt["policy"] {
                LabeledContent("Read") { ReadAgeView(date: at) }
            }
            if let error = store.board.sectionErrors["policy"] ?? store.board.sectionErrors["monitor"] {
                Text(error).font(.caption).foregroundStyle(.red)
            }

            HStack {
                Button("Open Policy") { store.selectedSection = .policy }
                Button("Open Monitor") { store.selectedSection = .monitor }
                Spacer()
                Button("Refresh") { Task { await store.refresh(live: false, light: false) } }
            }
        } header: {
            Text("Guard configuration")
        } footer: {
            Text(PressureHelp.guardConfiguration)
        }
        .help(PressureHelp.guardConfiguration)
    }

    private func sampleSummary(_ policy: PressurePolicy) -> String {
        func s(_ v: Int?) -> String { v.map { "\($0) s" } ?? "—" }
        return "normal \(s(policy.sampleIntervalSeconds)) · pressure \(s(policy.pressureSampleIntervalSeconds)) · critical \(s(policy.criticalSampleIntervalSeconds))"
    }
}

// MARK: - Keyboard

struct KeyboardSettingsSection: View {
    var body: some View {
        Section {
            Grid(alignment: .leading, horizontalSpacing: 16, verticalSpacing: 4) {
                ForEach(PressureStore.Section.allCases) { section in
                    GridRow {
                        Text(section.shortcutLabel).font(.caption.monospaced()).foregroundStyle(.secondary)
                        Text(section.rawValue).font(.caption)
                    }
                }
                GridRow {
                    Text("⌘R").font(.caption.monospaced()).foregroundStyle(.secondary)
                    Text("Refresh").font(.caption)
                }
                GridRow {
                    Text("⇧⌘R").font(.caption.monospaced()).foregroundStyle(.secondary)
                    Text("Live sample").font(.caption)
                }
            }
            Button("Open Keyboard Shortcuts…") { KeyboardSettings.openAppShortcuts() }
                .help(PressureHelp.keyboardRemap)
        } header: {
            Text("Keyboard")
        } footer: {
            Text("Remap in System Settings → Keyboard → Keyboard Shortcuts → App Shortcuts. The menu title must match exactly.")
        }
    }
}

// MARK: - Diagnostics

struct DiagnosticsSettingsSection: View {
    @EnvironmentObject private var store: PressureStore
    @State private var confirmReset = false
    @State private var copied = false

    private var stateDirectory: URL {
        let env = ProcessInfo.processInfo.environment
        if let path = env["NDEV_SESSION_PRESSURE_HOME"] ?? env["SESSION_PRESSURE_HOME"], !path.isEmpty {
            return URL(fileURLWithPath: path)
        }
        return FileManager.default.homeDirectoryForCurrentUser.appendingPathComponent(".nicos-dev/session-pressure")
    }

    var body: some View {
        Section {
            LabeledContent("App", value: Self.appVersion)
            LabeledContent("State folder") {
                Text(stateDirectory.path).font(PressureTheme.monoCaption).textSelection(.enabled)
            }
            HStack {
                Button("Reveal State Folder") {
                    NSWorkspace.shared.activateFileViewerSelecting([stateDirectory])
                }
                Button(copied ? "Copied" : "Copy Diagnostics") {
                    NSPasteboard.general.clearContents()
                    NSPasteboard.general.setString(diagnostics, forType: .string)
                    copied = true
                }
                .help(PressureHelp.copyDiagnostics)
                Spacer()
                Button("Reset App Preferences…", role: .destructive) { confirmReset = true }
                    .help(PressureHelp.resetPreferences)
            }
        } header: {
            Text("Diagnostics")
        }
        .confirmationDialog("Reset all NDev Pressure app preferences?", isPresented: $confirmReset) {
            Button("Reset Preferences", role: .destructive) {
                AppPreferences.reset()
                store.preferencesChanged(rebuildClient: true)
            }
        } message: {
            Text("Saved overrides, alerts, refresh, Dock and sidebar choices return to defaults. Guard policy is not changed.")
        }
    }

    static var appVersion: String {
        let info = Bundle.main.infoDictionary ?? [:]
        let version = info["CFBundleShortVersionString"] as? String ?? "dev"
        let build = info["CFBundleVersion"] as? String ?? "—"
        return "\(version) (\(build))"
    }

    private var diagnostics: String {
        let defaults = UserDefaults.standard
        var lines = [
            "NDev Pressure \(Self.appVersion)",
            "macOS \(ProcessInfo.processInfo.operatingSystemVersionString)",
            "session-pressure: \(store.binaryPath.isEmpty ? "not found" : store.binaryPath)",
            "ndev: \(store.advisorBinaryPath ?? "not found")",
            "product: \(store.capabilities.map { "\($0.version) \($0.sourceRevision ?? "")" } ?? "unread")",
            "transport: \(NDevPressureAPIClient.transport(environment: ClientFactory.environment()).summary)",
            "level: \(store.board.level.rawValue)  policy: \(store.board.policy?.modeLabel ?? "unread")",
            "trace helper: \(PrivilegedTraceClient.helperStatusLabel)",
            "preferences:",
        ]
        // Paths and choices only. The token file content is never read here.
        for key in AppPreferenceKey.all where key != AppPreferenceKey.lastPane {
            if let value = defaults.object(forKey: key) { lines.append("  \(key)=\(value)") }
        }
        for problem in store.preferenceProblems { lines.append("ignored: \(problem.message)") }
        for (key, value) in store.board.sectionErrors.sorted(by: { $0.key < $1.key }) {
            lines.append("error \(key): \(value)")
        }
        if let error = store.lastError { lines.append("last error: \(error)") }
        return lines.joined(separator: "\n")
    }
}
