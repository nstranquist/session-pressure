import AppKit
import Foundation
import NDevPressureCore
import SwiftUI

@MainActor
final class PressureStore: ObservableObject {
    enum Section: String, CaseIterable, Identifiable {
        case overview = "Overview"
        case trees = "Agent Trees"
        case diskWrites = "Disk Writes"
        case storage = "Storage"
        case work = "Work Queue"
        case policy = "Policy"
        case monitor = "Monitor"
        case telemetry = "Telemetry"

        var id: String { rawValue }

        var systemImage: String {
            switch self {
            case .overview: "gauge.with.dots.needle.67percent"
            case .trees: "tree"
            case .diskWrites: "internaldrive"
            case .storage: "externaldrive.badge.checkmark"
            case .work: "rectangle.stack"
            case .policy: "slider.horizontal.3"
            case .monitor: "heart.text.square"
            case .telemetry: "waveform.path.ecg"
            }
        }

        /// Digit used by Pressure → <title> in the menu bar. macOS App Shortcuts
        /// remap by that exact menu title, not by this digit.
        var commandDigit: String {
            switch self {
            case .overview: "1"
            case .trees: "2"
            case .diskWrites: "3"
            case .storage: "4"
            case .work: "5"
            case .policy: "6"
            case .monitor: "7"
            case .telemetry: "8"
            }
        }

        var shortcutLabel: String { "⌘\(commandDigit)" }
    }

    enum StorageTab: String, CaseIterable, Identifiable {
        case disk = "Disk reclaim"
        case idle = "Idle trees"

        var id: String { rawValue }
    }

    struct HistoricalWork: Hashable {
        var operationID: String
        var className: String
        var weight: Int
        var pid: Int?
        var previousState: String
    }

    struct DiskTraceRequest: Identifiable, Hashable {
        var pid: Int
        var processStartIdentity: String
        var durationSeconds: Int

        var id: String { "\(pid):\(processStartIdentity):\(durationSeconds)" }
    }

    /// Selected live work or a retained lifecycle view after it leaves the queue.
    enum WorkSelection: Identifiable, Hashable {
        case lease(WorkLease)
        case waiter(WorkWaiter)
        case historical(HistoricalWork)

        var id: String {
            switch self {
            case .lease(let lease):
                return "lease:\(lease.operationID ?? lease.id)"
            case .waiter(let waiter):
                return "waiter:\(waiter.operationID)"
            case .historical(let work):
                return "historical:\(work.operationID)"
            }
        }

        var operationID: String {
            switch self {
            case .lease(let lease):
                return lease.operationID ?? lease.id
            case .waiter(let waiter):
                return waiter.operationID
            case .historical(let work):
                return work.operationID
            }
        }

        var className: String {
            switch self {
            case .lease(let lease): return lease.className
            case .waiter(let waiter): return waiter.className
            case .historical(let work): return work.className
            }
        }

        var weight: Int {
            switch self {
            case .lease(let lease): return lease.weight
            case .waiter(let waiter): return waiter.weight
            case .historical(let work): return work.weight
            }
        }

        var pid: Int? {
            switch self {
            case .lease(let lease): return lease.pid
            case .waiter(let waiter): return waiter.pid
            case .historical(let work): return work.pid
            }
        }

        var isWaiter: Bool {
            if case .waiter = self { return true }
            return false
        }

        var isLease: Bool {
            if case .lease = self { return true }
            return false
        }

        var isHistorical: Bool {
            if case .historical = self { return true }
            return false
        }

        var historicalSnapshot: HistoricalWork {
            switch self {
            case .lease(let lease):
                return HistoricalWork(
                    operationID: lease.operationID ?? lease.id,
                    className: lease.className,
                    weight: lease.weight,
                    pid: lease.pid,
                    previousState: "active lease"
                )
            case .waiter(let waiter):
                return HistoricalWork(
                    operationID: waiter.operationID,
                    className: waiter.className,
                    weight: waiter.weight,
                    pid: waiter.pid,
                    previousState: "queued waiter"
                )
            case .historical(let work):
                return work
            }
        }
    }

    @Published var board = PressureBoard()
    @Published var selectedSection: Section = .overview {
        didSet {
            if selectedSection == .work {
                scheduleWorkFocusPoll(immediate: true)
            } else {
                stopWorkFocusPoll()
            }
            if selectedSection == .diskWrites {
                scheduleDiskFocusPoll(immediate: true)
            } else {
                stopDiskFocusPoll()
            }
            if selectedSection == .storage, storageTab == .disk {
                Task { await refreshStorageInventory() }
            }
            if selectedSection == .storage, storageTab == .idle {
                Task { await refresh(live: false, light: false) }
            }
            if selectedSection == .policy {
                Task { await refreshStorageInventory() }
            }
        }
    }

    @Published var storageTab: StorageTab = .disk {
        didSet {
            if selectedSection == .storage, storageTab == .disk {
                Task { await refreshStorageInventory() }
            }
            if selectedSection == .storage, storageTab == .idle {
                Task { await refresh(live: false, light: false) }
            }
        }
    }
    @Published var isRefreshing = false
    @Published var isWorkFocusRefreshing = false
    @Published var lastError: String?
    @Published var statusMessage: String?
    @Published var busyAction: String?

    @Published var workSelection: WorkSelection?
    @Published var workDetailEvents: [WorkLifecycleEvent] = []
    @Published var isWorkDetailLoading = false
    @Published var workDetailError: String?
    @Published var workDetailLoadedAt: Date?

    @Published var diskWriteSummary: DiskWriteSummary?
    @Published var diskWriters: [DiskWriter] = []
    @Published var diskWriteHistory: [DiskWriteHistoryPoint] = []
    @Published var diskWritePolicy: DiskWritePolicy?
    @Published var isDiskFocusRefreshing = false
    @Published var diskWriteError: String?
    @Published var diskHistoryLoadedAt: Date?
    @Published var isDiskTracing = false
    @Published var diskTracePaths: [String] = []
    @Published var diskTraceStatus: String?
    @Published var pendingDiskTraceRequest: DiskTraceRequest?

    @Published var storageProviders: [StorageProviderReport] = []
    @Published var storagePolicy: StoragePolicySnapshot?
    @Published var storageInventory: StorageSnapshot?
    @Published var storageReceipt: [StorageReceiptLine] = []
    @Published var storagePreview: StorageApplyEnvelope?
    @Published var pendingStorageApply: StorageApplyRequest?
    @Published var isStorageLoading = false
    @Published var isStorageApplying = false
    @Published var storageError: String?

    struct StorageApplyRequest: Identifiable, Hashable {
        var autoSafe: Bool
        var provider: String?
        var preview: StorageApplyEnvelope

        var id: String { autoSafe ? "auto-safe" : (provider ?? "provider") }
    }

    private var client: NDevPressureClient?
    private var pollTask: Task<Void, Never>?
    private var workFocusTask: Task<Void, Never>?
    private var diskFocusTask: Task<Void, Never>?
    private var generation = 0
    private var workFocusGeneration = 0
    private var diskFocusGeneration = 0
    private var workDetailGeneration = 0
    private var applicationActive = true
    private var windowVisible = true
    /// The main window can close while the app keeps running for its menu-bar
    /// extra. Board polling must survive that, at a slower cadence.
    private var windowOpen = true

    var binaryPath: String { client?.binaryPath ?? board.binaryPath }

    var workFocusPollActive: Bool {
        selectedSection == .work && workFocusTask != nil && interfaceAllowsFocusedRefresh
    }

    var diskFocusPollActive: Bool {
        selectedSection == .diskWrites && diskFocusTask != nil && interfaceAllowsFocusedRefresh
    }

    var workFocusPollInterval: TimeInterval {
        PressureFormat.workFocusPollInterval(
            queueDepth: board.work?.queueDepth ?? 0,
            leaseCount: board.work?.leases.count ?? 0,
            interfaceActive: interfaceAllowsFocusedRefresh
        )
    }

    private var interfaceAllowsFocusedRefresh: Bool {
        applicationActive && windowVisible
    }

    func setApplicationActive(_ active: Bool) {
        applicationActive = active
        if active && selectedSection == .work {
            scheduleWorkFocusPoll(immediate: true)
        }
        if active && selectedSection == .diskWrites {
            scheduleDiskFocusPoll(immediate: true)
        }
    }

    func setWindowVisible(_ visible: Bool) {
        windowVisible = visible
        if visible && selectedSection == .work {
            scheduleWorkFocusPoll(immediate: true)
        }
        if visible && selectedSection == .diskWrites {
            scheduleDiskFocusPoll(immediate: true)
        }
    }

    /// Closing the main window must not stop the board poll: the app stays alive
    /// specifically to serve its menu-bar extra, and a frozen gauge there reads
    /// as a calm host instead of a stale one. Pane focus polls do stop — nothing
    /// is rendering them.
    func setWindowOpen(_ open: Bool) {
        windowOpen = open
        windowVisible = open
        if open {
            schedulePoll(immediate: true)
            if selectedSection == .work { scheduleWorkFocusPoll(immediate: true) }
            if selectedSection == .diskWrites { scheduleDiskFocusPoll(immediate: true) }
        } else {
            stopWorkFocusPoll()
            stopDiskFocusPoll()
            // Reschedule onto the slower menu-bar-only cadence.
            schedulePoll(immediate: false)
        }
    }

    /// Cadence for the whole-board poll. Windowed use follows the pressure
    /// ladder; menu-bar-only use backs off hard.
    var boardPollInterval: TimeInterval {
        windowOpen ? PressureFormat.pollInterval(for: board.level) : PressureFormat.menuBarOnlyPollInterval
    }

    func start() {
        do {
            client = try NDevPressureClient()
            lastError = nil
        } catch {
            lastError = error.localizedDescription
            client = nil
        }
        schedulePoll(immediate: true)
        if selectedSection == .work {
            scheduleWorkFocusPoll(immediate: true)
        }
        if selectedSection == .diskWrites {
            scheduleDiskFocusPoll(immediate: true)
        }
    }

    func stop() {
        pollTask?.cancel()
        pollTask = nil
        stopWorkFocusPoll()
        stopDiskFocusPoll()
    }

    func schedulePoll(immediate: Bool) {
        pollTask?.cancel()
        generation += 1
        let gen = generation
        pollTask = Task { [weak self] in
            guard let self else { return }
            if !immediate {
                let interval = self.boardPollInterval
                try? await Task.sleep(nanoseconds: UInt64(interval * 1_000_000_000))
            }
            guard !Task.isCancelled, gen == self.generation else { return }
            // With no window open only the menu-bar grid is rendering, so take
            // the compact path: host status, work, and admission.
            await self.refresh(live: false, light: !self.windowOpen)
            guard !Task.isCancelled, gen == self.generation else { return }
            self.schedulePoll(immediate: false)
        }
    }

    private func stopWorkFocusPoll() {
        workFocusTask?.cancel()
        workFocusTask = nil
        workFocusGeneration += 1
        isWorkFocusRefreshing = false
    }

    /// Near-live while visible and busy, backed off when idle, and suspended
    /// from spawning CLI reads while the app or its main window is inactive.
    func scheduleWorkFocusPoll(immediate: Bool) {
        workFocusTask?.cancel()
        workFocusGeneration += 1
        let gen = workFocusGeneration
        workFocusTask = Task { [weak self] in
            guard let self else { return }
            var refreshImmediately = immediate
            while !Task.isCancelled, gen == self.workFocusGeneration, self.selectedSection == .work {
                if !refreshImmediately {
                    let interval = self.workFocusPollInterval
                    try? await Task.sleep(nanoseconds: UInt64(interval * 1_000_000_000))
                }
                guard !Task.isCancelled, gen == self.workFocusGeneration else { return }
                guard self.selectedSection == .work else { return }
                if self.interfaceAllowsFocusedRefresh {
                    await self.refreshWorkFocus()
                }
                refreshImmediately = false
            }
        }
    }

    private func stopDiskFocusPoll() {
        diskFocusTask?.cancel()
        diskFocusTask = nil
        diskFocusGeneration += 1
        isDiskFocusRefreshing = false
    }

    /// Live writer attribution is hydrated only while this pane is visible.
    /// One bounded native sample per resident cadence adds no background daemon
    /// and does not create a per-sample durable write loop.
    func scheduleDiskFocusPoll(immediate: Bool) {
        diskFocusTask?.cancel()
        diskFocusGeneration += 1
        let gen = diskFocusGeneration
        diskFocusTask = Task { [weak self] in
            guard let self else { return }
            var refreshImmediately = immediate
            while !Task.isCancelled, gen == self.diskFocusGeneration, self.selectedSection == .diskWrites {
                if !refreshImmediately {
                    try? await Task.sleep(nanoseconds: 15_000_000_000)
                }
                guard !Task.isCancelled, gen == self.diskFocusGeneration else { return }
                guard self.selectedSection == .diskWrites else { return }
                if self.interfaceAllowsFocusedRefresh {
                    let historyStale = self.diskHistoryLoadedAt.map { Date().timeIntervalSince($0) > 300 } ?? true
                    await self.refreshDiskWrites(live: true, includeHistory: historyStale)
                }
                refreshImmediately = false
            }
        }
    }

    func refresh(live: Bool = false, light: Bool = false) async {
        guard let client else {
            do {
                self.client = try NDevPressureClient()
            } catch {
                lastError = error.localizedDescription
                return
            }
            return await refresh(live: live, light: light)
        }

        isRefreshing = true
        defer { isRefreshing = false }

        do {
            // Light path: host status, work, and admission in a single process.
            if light {
                board = try await client.refreshBoard(
                    live: live,
                    includeIdle: false,
                    includeTelemetry: false,
                    fullStatus: false,
                    includePolicy: false,
                    includeMonitor: false,
                    includeDoctor: false,
                    includeCalibration: false,
                    previous: board
                )
            } else {
                let richStatus = selectedSection == .overview || selectedSection == .trees || selectedSection == .monitor
                board = try await client.refreshBoard(
                    live: live,
                    includeIdle: selectedSection == .storage && storageTab == .idle,
                    includeTelemetry: selectedSection == .telemetry,
                    fullStatus: richStatus,
                    includePolicy: board.policy == nil || selectedSection == .policy,
                    includeMonitor: board.launchd == nil || selectedSection == .monitor,
                    includeDoctor: selectedSection == .overview || selectedSection == .monitor,
                    includeCalibration: selectedSection == .overview || selectedSection == .work,
                    previous: board
                )
            }
            lastError = nil
            refreshWorkSelectionFromBoard()
            updateDockBadge()
            if selectedSection == .diskWrites {
                await refreshDiskWrites(live: true, includeHistory: diskWriteHistory.isEmpty)
            }
        } catch {
            lastError = error.localizedDescription
        }
    }

    /// Silent work-status poll for the Work Queue focus path (no full-board isRefreshing).
    func refreshWorkFocus() async {
        guard let client else { return }
        isWorkFocusRefreshing = true
        defer { isWorkFocusRefreshing = false }
        do {
            let workEnv = try await client.workStatus()
            var next = board
            next.work = workEnv.work
            next.refreshedAt = Date()
            next.binaryPath = client.binaryPath
            board = next
            refreshWorkSelectionFromBoard()
            updateDockBadge()
        } catch {
            if board.work == nil {
                lastError = error.localizedDescription
            }
        }
    }

    func refreshDiskWrites(live: Bool = true, includeHistory: Bool = false) async {
        guard let client, !isDiskFocusRefreshing else { return }
        isDiskFocusRefreshing = true
        defer { isDiskFocusRefreshing = false }
        do {
            // The full status already carries the same bounded writer report;
            // do not launch a second live `io top` sample from the UI.
            let status = try await client.diskWriteStatus(live: live, full: true)
            diskWriteSummary = status.report?.summary ?? status.summary
            diskWriters = status.report?.writers ?? []
            if let policy = status.diskWritePolicy {
                diskWritePolicy = policy
            }
            if includeHistory {
                let history = try await client.diskWriteHistory(since: "24h", limit: 20)
                diskWriteHistory = history.history
                diskHistoryLoadedAt = Date()
            }
            diskWriteError = nil
        } catch {
            diskWriteError = error.localizedDescription
        }
    }

    func traceDiskWriter(_ writer: DiskWriter, durationSeconds: Int) async {
        guard let client, let pid = writer.pid else {
            diskTraceStatus = "A fresh live PID is required before tracing."
            return
        }
        isDiskTracing = true
        diskTracePaths = []
        diskTraceStatus = "Requesting a revalidated process identity…"
        defer { isDiskTracing = false }
        do {
            let handoff = try await client.diskWriteTraceHandoff(pid: pid, durationSeconds: durationSeconds)
            guard let identity = handoff.processStartIdentity, !identity.isEmpty else {
                throw PrivilegedTraceError.invalidReply
            }
            try await traceDiskProcess(pid: pid, processStartIdentity: identity, durationSeconds: durationSeconds)
        } catch {
            diskTraceStatus = error.localizedDescription
        }
    }

    func handleDeepLink(_ url: URL) {
        guard url.scheme == "ndev-pressure", url.host == "disk-writes" else { return }
        selectedSection = .diskWrites
        guard url.path == "/trace",
              let components = URLComponents(url: url, resolvingAgainstBaseURL: false),
              let pidText = components.queryItems?.first(where: { $0.name == "pid" })?.value,
              let pid = Int(pidText), pid > 1,
              let identity = components.queryItems?.first(where: { $0.name == "start" })?.value,
              !identity.isEmpty,
              identity.utf8.count <= 96
        else {
            return
        }
        let durationText = components.queryItems?.first(where: { $0.name == "duration" })?.value ?? "15s"
        guard durationText.hasSuffix("s"),
              let duration = Int(durationText.dropLast()),
              (5...30).contains(duration)
        else {
            diskTraceStatus = "The trace link requested an invalid duration."
            return
        }
        pendingDiskTraceRequest = DiskTraceRequest(
            pid: pid,
            processStartIdentity: identity,
            durationSeconds: duration
        )
        diskTraceStatus = "Confirm the interactive path trace in NDev Pressure."
    }

    func confirmPendingDiskTrace() async {
        guard let request = pendingDiskTraceRequest else { return }
        pendingDiskTraceRequest = nil
        isDiskTracing = true
        diskTracePaths = []
        defer { isDiskTracing = false }
        do {
            try await traceDiskProcess(
                pid: request.pid,
                processStartIdentity: request.processStartIdentity,
                durationSeconds: request.durationSeconds
            )
        } catch {
            diskTraceStatus = error.localizedDescription
        }
    }

    private func traceDiskProcess(pid: Int, processStartIdentity: String, durationSeconds: Int) async throws {
        diskTraceStatus = "Waiting for administrator authorization…"
        let result = try await PrivilegedTraceClient().trace(
            pid: pid,
            processStartIdentity: processStartIdentity,
            durationSeconds: durationSeconds
        )
        diskTracePaths = result.paths
        if result.paths.isEmpty {
            diskTraceStatus = "No file paths were observed in the bounded trace window."
        } else if result.truncated {
            diskTraceStatus = "Showing the first \(result.paths.count) unique paths; the bounded result was truncated."
        } else {
            diskTraceStatus = "Observed \(result.paths.count) unique path\(result.paths.count == 1 ? "" : "s"); nothing was persisted."
        }
    }

    // MARK: - Work inspector

    func selectWorkItem(_ selection: WorkSelection) {
        workSelection = selection
        Task { await loadWorkDetail(for: selection.operationID) }
    }

    func clearWorkSelection() {
        workSelection = nil
        workDetailEvents = []
        workDetailError = nil
        workDetailLoadedAt = nil
        workDetailGeneration += 1
        isWorkDetailLoading = false
    }

    func reloadWorkDetail() async {
        guard let selection = workSelection else { return }
        await loadWorkDetail(for: selection.operationID)
    }

    private func loadWorkDetail(for operationID: String) async {
        guard let client else {
            workDetailError = "ndev client unavailable"
            return
        }
        workDetailGeneration += 1
        let gen = workDetailGeneration
        isWorkDetailLoading = true
        workDetailError = nil
        defer {
            if gen == workDetailGeneration {
                isWorkDetailLoading = false
            }
        }
        do {
            // The ledger filters server-side now; the client-side filter stays
            // as a cheap guard so an older helper that ignores the flag cannot
            // spill another operation's lifecycle into this drawer.
            let history = try await client.workHistory(since: "48h", limit: 200, operationID: operationID)
            guard gen == workDetailGeneration else { return }
            let matched = history.workEvents
                .filter { $0.operationID == operationID }
                .sorted { ($0.timestamp ?? .distantPast) < ($1.timestamp ?? .distantPast) }
            workDetailEvents = matched
            workDetailLoadedAt = Date()
            if matched.isEmpty {
                workDetailError = nil
            }
        } catch {
            guard gen == workDetailGeneration else { return }
            workDetailError = error.localizedDescription
        }
    }

    func refreshWorkSelectionFromBoard() {
        guard let selection = workSelection else { return }
        guard let work = board.work else { return }
        switch selection {
        case .lease(let lease):
            if let updated = work.leases.first(where: {
                ($0.operationID ?? $0.id) == (lease.operationID ?? lease.id) || $0.id == lease.id
            }) {
                workSelection = .lease(updated)
            } else {
                transitionWorkSelectionToHistory(selection)
            }
        case .waiter(let waiter):
            if let updated = work.waiters.first(where: { $0.operationID == waiter.operationID }) {
                workSelection = .waiter(updated)
            } else if let asLease = work.leases.first(where: { $0.operationID == waiter.operationID }) {
                // Waiter acquired between polls — promote selection to lease.
                workSelection = .lease(asLease)
                Task { await loadWorkDetail(for: asLease.operationID ?? waiter.operationID) }
            } else {
                transitionWorkSelectionToHistory(selection)
            }
        case .historical:
            break
        }

    }

    private func transitionWorkSelectionToHistory(_ selection: WorkSelection) {
        let historical = selection.historicalSnapshot
        workSelection = .historical(historical)
        if client != nil {
            Task { await loadWorkDetail(for: historical.operationID) }
        }
    }

    // MARK: - Actions

    func enableProtection(autoShed: Bool) async {
        await mutate("Enabling protection…") { client in
            _ = try await client.policyEnable(autoShed: autoShed)
        }
    }

    func observeOnly() async {
        await mutate("Switching to observe…") { client in
            _ = try await client.policyObserve()
        }
    }

    func applyPolicyProfile(_ profile: String, withAutoShed: Bool = false) async {
        await mutate("Applying (profile) mode…") { client in
            _ = try await client.policyProfileApply(profile, withAutoShed: withAutoShed)
        }
    }

    var policySuggestion: PolicySuggestion? {
        PolicySuggestionFactory.current(from: board.calibration, policy: board.policy)
    }

    func copyToPasteboard(_ string: String, done: String = "Copied") {
        NSPasteboard.general.clearContents()
        NSPasteboard.general.setString(string, forType: .string)
        statusMessage = done
        Task {
            try? await Task.sleep(nanoseconds: 1_200_000_000)
            if statusMessage == done {
                statusMessage = nil
            }
        }
    }

    func observeDiskWrites() async {
        await mutate("Enabling write observation…") { client in
            _ = try await client.diskWritePolicyObserve()
        }
    }

    func enableDiskWriteAlerts() async {
        await mutate("Enabling write alerts…") { client in
            _ = try await client.diskWritePolicyEnableAlerts()
        }
    }

    func disableDiskWrites() async {
        await mutate("Disabling write observation…") { client in
            _ = try await client.diskWritePolicyDisable()
        }
    }

    func initPolicy(force: Bool) async {
        await mutate("Writing default policy…") { client in
            _ = try await client.policyInit(force: force)
        }
    }

    func installMonitor(enforce: Bool) async {
        await mutate("Installing monitor…") { client in
            _ = try await client.monitorInstall(enforce: enforce)
        }
    }

    func uninstallMonitor() async {
        await mutate("Uninstalling monitor…") { client in
            _ = try await client.monitorUninstall()
        }
    }

    func sampleOnce() async {
        await mutate("Sampling once…") { client in
            _ = try await client.monitorOnce()
        }
    }

    func refreshStorageInventory() async {
        guard let client else {
            storageError = "session-pressure client unavailable"
            return
        }
        isStorageLoading = true
        defer { isStorageLoading = false }
        do {
            async let providersTask = client.storageProviders()
            async let statusTask = client.storageStatus()
            let providers = try await providersTask
            let status = try await statusTask
            storageProviders = providers.providers
            storageInventory = status.storage ?? providers.storage ?? board.snapshot.storage
            storagePolicy = status.storagePolicy ?? providers.storagePolicy ?? storagePolicy
            storageError = nil
        } catch {
            storageError = error.localizedDescription
        }
    }

    var storageAutoSafeActionable: [StorageProviderReport] {
        storageProviders.filter { $0.tier == .autoSafe && $0.isActionable }
    }

    /// False only after inventory has loaded and every auto_safe row is blocked or factory-only.
    var canBeginAutoSafeReclaim: Bool {
        storageProviders.isEmpty || !storageAutoSafeActionable.isEmpty
    }

    func previewSafeReclaim(autoSafe: Bool, provider: String? = nil) async {
        guard let client else {
            storageError = "session-pressure client unavailable"
            return
        }
        isStorageApplying = true
        defer { isStorageApplying = false }
        let command = StorageReclaim.applyArguments(autoSafe: autoSafe, provider: provider, apply: false)
        storageReceipt = [StorageReceiptLine(kind: .command, text: command.joined(separator: " "))]
        if autoSafe, storagePolicy?.enforceAdmission != true {
            appendStorageReceipt(StorageReceiptLine(
                kind: .status,
                text: "Storage policy enforcement is off. Preview is allowed; confirm will refuse --auto-safe until you enable storage policy."
            ))
        }
        storagePreview = nil
        pendingStorageApply = nil
        do {
            let envelope = try await client.storageApply(
                autoSafe: autoSafe,
                provider: provider,
                apply: false,
                onOutputLine: { [weak self] line in
                    Task { @MainActor in
                        self?.appendStorageReceipt(StorageReceiptLine(kind: .stdout, text: line))
                    }
                }
            )
            storagePreview = envelope
            for line in StorageReclaim.receiptLines(from: envelope, command: command) {
                appendStorageReceipt(line)
            }
            pendingStorageApply = StorageApplyRequest(autoSafe: autoSafe, provider: provider, preview: envelope)
            storageError = nil
        } catch {
            storageError = error.localizedDescription
            appendStorageReceipt(StorageReceiptLine(kind: .error, text: error.localizedDescription))
        }
    }

    func openStorage(tab: StorageTab = .disk) {
        storageTab = tab
        selectedSection = .storage
    }

    func confirmStorageApply(_ request: StorageApplyRequest) async {
        pendingStorageApply = nil
        if request.autoSafe, let policy = storagePolicy, !policy.enforceAdmission {
            storageError = "Enable storage policy before --auto-safe apply. Named provider apply remains available."
            appendStorageReceipt(StorageReceiptLine(kind: .error, text: storageError ?? ""))
            return
        }
        guard let client else {
            storageError = "session-pressure client unavailable"
            return
        }
        isStorageApplying = true
        defer { isStorageApplying = false }
        let command = StorageReclaim.applyArguments(autoSafe: request.autoSafe, provider: request.provider, apply: true)
        appendStorageReceipt(StorageReceiptLine(kind: .command, text: command.joined(separator: " ")))
        do {
            let envelope = try await client.storageApply(
                autoSafe: request.autoSafe,
                provider: request.provider,
                apply: true,
                onOutputLine: { [weak self] line in
                    Task { @MainActor in
                        self?.appendStorageReceipt(StorageReceiptLine(kind: .stdout, text: line))
                    }
                }
            )
            for line in StorageReclaim.receiptLines(from: envelope, command: command) {
                appendStorageReceipt(line)
            }
            storageError = envelope.ok == false ? (envelope.error ?? "storage apply failed") : nil
            await refreshStorageInventory()
            await refresh(live: false, light: false)
        } catch {
            storageError = error.localizedDescription
            appendStorageReceipt(StorageReceiptLine(kind: .error, text: error.localizedDescription))
        }
    }

    func enableStoragePolicy() async {
        await mutate("Enabling storage admission…") { client in
            let env = try await client.storagePolicyEnable()
            await MainActor.run { self.storagePolicy = env.storagePolicy ?? self.storagePolicy }
        }
        await refreshStorageInventory()
    }

    func observeStoragePolicy() async {
        await mutate("Storage policy observe…") { client in
            let env = try await client.storagePolicyObserve()
            await MainActor.run { self.storagePolicy = env.storagePolicy ?? self.storagePolicy }
        }
        await refreshStorageInventory()
    }

    private func appendStorageReceipt(_ line: StorageReceiptLine) {
        if line.kind == .command || line.kind == .status,
           storageReceipt.contains(where: { $0.kind == line.kind && $0.text == line.text }) {
            return
        }
        storageReceipt.append(line)
    }

    func applyIdle(tree: AgentTree) async {
        guard let sessionID = tree.sessionID, !sessionID.isEmpty else {
            lastError = "Idle apply requires a detected session ID on the tree."
            return
        }
        await mutate("Signaling idle tree \(tree.rootPID)…") { client in
            _ = try await client.idleApply(rootPID: tree.rootPID, sessionID: sessionID)
        }
    }

    /// Run now is not confirmed. It cannot preempt a lease, start a command, or
    /// bypass a pressure or capacity gate — it only reorders a queue the
    /// operator is already looking at, and is reversible by promoting something
    /// else. A modal on a reversible reordering was pure friction.
    func overrideWork(waiter: WorkWaiter) async {
        await mutate("Promoting queued \(waiter.className) task…") { client in
            _ = try await client.workOverride(operationID: waiter.operationID)
        }
    }

    /// Pins the whole live queue as one ordered promotion sequence. The waiter
    /// list is resolved by the coordinator under its state lock, so this never
    /// pins the stale snapshot the UI happens to be rendering.
    func overrideAllWork() async {
        let depth = board.work?.queueDepth ?? 0
        await mutate("Promoting \(depth) queued task\(depth == 1 ? "" : "s")…") { client in
            _ = try await client.workOverrideAll()
        }
    }

    /// Releases the pinned sequence. This is what makes Run all reversible —
    /// without it a bulk promotion could only end by draining.
    func clearWorkOverride() async {
        await mutate("Releasing pinned promotions…") { client in
            _ = try await client.workOverrideClear()
        }
    }

    private func mutate(_ label: String, body: (NDevPressureClient) async throws -> Void) async {
        guard let client else {
            lastError = "ndev client unavailable"
            return
        }
        busyAction = label
        statusMessage = label
        defer {
            busyAction = nil
        }
        do {
            try await body(client)
            statusMessage = "Done"
            await refresh(live: false, light: false)
            if selectedSection == .work {
                await refreshWorkFocus()
            }
            // Re-enable the controls as soon as the board is truthful again. The
            // trailing sleep only fades the "Done" text; leaving busyAction set
            // across it disabled every Run now / Run all for an extra 1.2s, which
            // is very visible now that promotion takes no confirmation step.
            busyAction = nil
            try? await Task.sleep(nanoseconds: 1_200_000_000)
            if statusMessage == "Done" { statusMessage = nil }
        } catch {
            lastError = error.localizedDescription
            statusMessage = nil
        }
    }

    private func updateDockBadge() {
        let app = NSApplication.shared
        switch board.level {
        case .normal, .unknown:
            app.dockTile.badgeLabel = nil
        case .warning:
            app.dockTile.badgeLabel = "!"
        case .red:
            app.dockTile.badgeLabel = "RED"
        case .critical:
            app.dockTile.badgeLabel = "CRIT"
        }
    }
}
