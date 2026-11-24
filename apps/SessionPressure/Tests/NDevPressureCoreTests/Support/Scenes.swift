import CryptoKit
import Foundation
import SwiftUI
@testable import NDevPressure
@testable import NDevPressureCore

/// Where a scene's data comes from. Fixtures are recorded CLI output in
/// `Fixtures/` and make the scene the same on every run. Live reads the
/// installed helper the way the app does (`make render LIVE=1`).
enum SceneSource: String, Sendable {
    case fixtures
    case live
}

/// One named app page in a given state, built without a window.
///
/// The gate (`SceneGateTests`) requires each scene to fit its minimum width
/// and to draw. `make render` writes chosen scenes to PNG for a person or
/// an agent to look at.
struct Scene: Sendable {
    let name: String
    /// The sidebar page the scene shows; nil for a popover or panel.
    let section: PressureStore.Section?
    let summary: String
    /// The narrowest width the scene must fit. Pages use the detail pane of
    /// the narrowest main window.
    let minWidth: CGFloat
    /// Fixture files the scene reads in `.fixtures` mode.
    let fixtures: [String]
    /// Render height when `make render` gets no HEIGHT. Tall enough to show
    /// the part of the page the scene is about.
    var height: CGFloat = 900
    let make: @MainActor @Sendable (SceneSource) async throws -> AnyView
}

enum SceneCatalog {
    static let pageWidth = WindowLayout.narrowestDetailWidth

    static let all: [Scene] = [
        page("overview", .overview, "Overview from a full recorded board", fixtures: ["board-full"]) { _ in AnyView(OverviewView()) },
        page("day-gate", .dayGate, "Day Gate with a recorded advisor brief", fixtures: ["board-full", "advisor-brief"]) { _ in AnyView(DayGateView()) },
        page("agent-trees", .trees, "Agent Trees from a full recorded board", fixtures: ["board-full"]) { _ in AnyView(TreesView()) },
        Scene(name: "process-incidents", section: .processIncidents, summary: "Process Incidents with recorded history", minWidth: pageWidth, fixtures: ["board-full", "process-incidents"]) { source in
            let store = try await SceneStores.pressure(source, for: .processIncidents)
            let view = source == .live
                ? ProcessIncidentsView()
                : ProcessIncidentsView(read: { _ in try SceneFixtures.decode(ProcessIncidentEnvelope.self, "process-incidents").report })
            return AnyView(view.environmentObject(store))
        },
        page("disk-writes", .diskWrites, "Disk Writes with a recorded live sample, 24 h history and SMART read", fixtures: ["board-full", "io-status", "io-history", "io-health"]) { _ in AnyView(DiskWritesView()) },
        page("storage-disk", .storage, "Storage, disk reclaim tab", fixtures: ["board-full", "storage-providers"]) { _ in AnyView(StorageView()) },
        Scene(name: "storage-idle", section: .storage, summary: "Storage, idle trees tab", minWidth: pageWidth, fixtures: ["board-full", "storage-providers"]) { source in
            let store = try await SceneStores.pressure(source, for: .storage)
            store.storageTab = .idle
            return AnyView(StorageView().environmentObject(store))
        },
        page("work-queue", .work, "Work Queue with recorded leases and placement", fixtures: ["board-full", "work-placement"]) { _ in AnyView(WorkView()) },
        Scene(name: "work-queue-selected", section: .work, summary: "Work Queue with a lease open in the detail pane", minWidth: pageWidth, fixtures: ["board-full", "work-placement"]) { source in
            let store = try await SceneStores.pressure(source, for: .work)
            if let lease = store.board.work?.leases.first { store.workSelection = .lease(lease) }
            return AnyView(WorkView().environmentObject(store))
        },
        Scene(name: "work-queue-large-runner", section: .work, summary: "Work Queue with a 1,024-slot runner (2026-11-24 overflow)", minWidth: pageWidth, fixtures: ["board-full", "work-placement"]) { source in
            let store = try await SceneStores.pressure(source, for: .work)
            store.workPlacement = try SceneFixtures.workPlacement(heavySlots: 1024, lightSlots: 1024)
            return AnyView(WorkView().environmentObject(store))
        },
        Scene(name: "hetzner", section: .hetzner, summary: "Hetzner with recorded delegate runs, receipts and capacity", minWidth: pageWidth, fixtures: ["board-full", "gate-runner", "runner-capacity"]) { source in
            let (store, hetzner, capacity) = try await SceneStores.hetzner(source)
            return AnyView(HetznerView(hetzner: hetzner, capacity: capacity).environmentObject(store))
        },
        Scene(name: "hetzner-capacity-alert", section: .hetzner, summary: "Hetzner with a runner's open capacity alert focused", minWidth: pageWidth, fixtures: ["board-full", "gate-runner", "runner-capacity"]) { source in
            let alerts = source == .fixtures ? try SceneFixtures.runnerCapacityWithAlerts() : nil
            let (store, hetzner, capacity) = try await SceneStores.hetzner(source, capacity: alerts)
            if let alert = capacity.report?.alerts.first(where: { $0.since != nil }) {
                capacity.selectedHost = alert.host
                capacity.focusedAlert = alert.id
            }
            return AnyView(HetznerView(hetzner: hetzner, capacity: capacity).environmentObject(store))
        },
        Scene(name: "hetzner-job-log", section: .hetzner, summary: "Hetzner with a receipt's job log open (long lines)", minWidth: pageWidth, fixtures: ["board-full", "gate-runner", "runner-capacity"], height: 3200) { source in
            let (store, hetzner, capacity) = try await SceneStores.hetzner(source)
            if let job = hetzner.jobs.first {
                hetzner.selectJob(job)
                for _ in 0..<50 where hetzner.jobLog == nil && hetzner.jobLogError == nil {
                    try await Task.sleep(for: .milliseconds(20))
                }
            }
            return AnyView(HetznerView(hetzner: hetzner, capacity: capacity).environmentObject(store))
        },
        page("policy", .policy, "Policy from a full recorded board", fixtures: ["board-full"]) { _ in AnyView(PolicyView()) },
        page("monitor", .monitor, "Monitor from a full recorded board", fixtures: ["board-full"]) { _ in AnyView(MonitorView()) },
        page("telemetry", .telemetry, "Telemetry with 40 recorded events; no host has recorded a relief action", fixtures: ["board-full"]) { _ in AnyView(TelemetryView()) },
        Scene(name: "notifications", section: nil, summary: "Bell popover with two runner capacity alerts", minWidth: NotificationPopoverBody.width, fixtures: ["board-full", "runner-capacity"], height: 420) { source in
            let watch = source == .fixtures
                ? RunnerAlertWatch(fetch: { _ in try SceneFixtures.runnerCapacityWithAlerts() }, enabled: { true }, notify: { .off })
                : RunnerAlertWatch(enabled: { true }, notify: { .off })
            let store = try await SceneStores.pressure(source, for: .overview, runnerWatch: watch)
            await watch.check()
            return AnyView(NotificationPopoverBody().environmentObject(store).frame(width: NotificationPopoverBody.width))
        },
        Scene(name: "settings", section: .settings, summary: "Settings, General section", minWidth: pageWidth, fixtures: ["board-full"]) { source in
            let store = try await SceneStores.pressure(source, for: .settings)
            return AnyView(AppSettingsView(selected: .constant(.general)).environmentObject(store))
        },
    ]

    /// Scenes by name, in catalog order. An unknown name throws.
    static func named(_ names: [String]) throws -> [Scene] {
        let wanted = Set(names)
        let unknown = wanted.subtracting(all.map(\.name))
        guard unknown.isEmpty else { throw SceneError.unknown(unknown.sorted(), known: all.map(\.name)) }
        return all.filter { wanted.contains($0.name) }
    }

    /// A page that needs only the pressure store.
    private static func page(
        _ name: String,
        _ section: PressureStore.Section,
        _ summary: String,
        fixtures: [String],
        _ view: @escaping @MainActor @Sendable (PressureStore) -> AnyView
    ) -> Scene {
        Scene(name: name, section: section, summary: summary, minWidth: pageWidth, fixtures: fixtures) { source in
            let store = try await SceneStores.pressure(source, for: section)
            return AnyView(view(store).environmentObject(store))
        }
    }
}

enum SceneError: Error, CustomStringConvertible {
    case unknown([String], known: [String])
    case missingFixture(String)

    var description: String {
        switch self {
        case .unknown(let names, let known):
            "unknown scene \(names.joined(separator: ", ")); known: \(known.joined(separator: " "))"
        case .missingFixture(let name):
            "fixture \(name).json is missing from the test bundle"
        }
    }
}

/// Recorded fixtures, shared by the scenes and the contract tests.
enum SceneFixtures {
    static func url(_ name: String) throws -> URL {
        guard let url = Bundle.module.url(forResource: name, withExtension: "json", subdirectory: "Fixtures") else {
            throw SceneError.missingFixture(name)
        }
        return url
    }

    static func data(_ name: String) throws -> Data {
        try Data(contentsOf: url(name))
    }

    static func decode<T: Decodable>(_ type: T.Type, _ name: String) throws -> T {
        try PressureJSON.decode(type, from: data(name))
    }

    /// SHA-256 over the named fixtures' bytes, in order. The render manifest
    /// records it so a PNG can be traced to the data it drew.
    static func digest(_ names: [String]) throws -> String {
        var hash = SHA256()
        for name in names { hash.update(data: try data(name)) }
        return "sha256:" + hash.finalize().map { String(format: "%02x", $0) }.joined()
    }

    /// The recorded placement report with every runner's slot counts set.
    static func workPlacement(heavySlots: Int, lightSlots: Int) throws -> WorkPlacementReport? {
        guard var json = try JSONSerialization.jsonObject(with: data("work-placement")) as? [String: Any],
              var placement = json["placement"] as? [String: Any],
              var runners = placement["runners"] as? [[String: Any]] else { return nil }
        for i in runners.indices {
            runners[i]["heavy_slots"] = heavySlots
            runners[i]["light_slots"] = lightSlots
        }
        placement["runners"] = runners
        json["placement"] = placement
        return try PressureJSON.decode(WorkPlacementEnvelope.self, from: JSONSerialization.data(withJSONObject: json)).placement
    }

    /// A runner alert as the CLI emits it.
    static func runnerAlert(_ severity: String, _ code: String, host: String = "runner-3", since: String? = nil, until: String? = nil) -> [String: Any] {
        var alert: [String: Any] = ["severity": severity, "code": code, "host": host, "title": code, "detail": "\(code) detail"]
        if let since { alert["since"] = since }
        if let until { alert["until"] = until }
        return alert
    }

    /// The recorded capacity payload re-captured with the given alerts.
    static func runnerCapacity(window: String = "6h", alerts: [[String: Any]], capturedAt: String = "2026-11-15T12:55:00Z") throws -> RunnerCapacityEnvelope {
        guard var root = try JSONSerialization.jsonObject(with: data("runner-capacity")) as? [String: Any],
              var capacity = root["capacity"] as? [String: Any] else { throw SceneError.missingFixture("runner-capacity") }
        capacity["window"] = window
        capacity["bucket_seconds"] = window == "6h" ? 300 : 900
        capacity["captured_at"] = capturedAt
        capacity["alerts"] = alerts
        root["capacity"] = capacity
        return try PressureJSON.decode(RunnerCapacityEnvelope.self, from: JSONSerialization.data(withJSONObject: root))
    }

    /// Two open alerts on one runner: memory pressure and full heavy slots.
    static func runnerCapacityWithAlerts() throws -> RunnerCapacityEnvelope {
        var memory = runnerAlert("critical", "memory_pressure", since: "2026-11-15T12:40:00Z", until: "2026-11-15T12:55:00Z")
        memory["title"] = "Memory pressure"
        memory["detail"] = "2 episodes below 10% available; lowest 3.1 GiB of 62.7 GiB."
        var slots = runnerAlert("warning", "heavy_saturated", since: "2026-11-15T12:30:00Z", until: "2026-11-15T12:55:00Z")
        slots["title"] = "Heavy slots full"
        slots["detail"] = "All 5 heavy slots were full for 14m in 1 episode."
        return try runnerCapacity(alerts: [memory, slots])
    }

    /// A job log with a long unbroken line, as real build output has.
    static func jobLog(id: String) -> GateRunnerJobLog {
        var lines = ["$ go test ./...", String(repeating: "x", count: 600) + " (one 600-character line)"]
        for i in 1...200 { lines.append("ok  \texample.com/module/package\(i)\t0.\(i % 10)\(i % 7)s") }
        let log = lines.joined(separator: "\n")
        return GateRunnerJobLog(id: id, kind: "receipt", log: log, bytes: Int64(log.utf8.count), command: "go test ./...")
    }
}

/// Stores filled from fixtures, or read live through the app's own paths.
@MainActor
enum SceneStores {
    static func pressure(_ source: SceneSource, for section: PressureStore.Section, runnerWatch: RunnerAlertWatch? = nil) async throws -> PressureStore {
        switch source {
        case .fixtures:
            let store = runnerWatch.map { PressureStore(runnerWatch: $0) } ?? PressureStore()
            store.board = PressureBoard(
                composite: try SceneFixtures.decode(BoardEnvelope.self, "board-full"),
                previous: nil,
                binaryPath: "",
                live: false
            )
            store.advisorBrief = try SceneFixtures.decode(AdvisorBrief.self, "advisor-brief")
            store.workPlacement = try SceneFixtures.decode(WorkPlacementEnvelope.self, "work-placement").placement
            store.workPlacementLoadedAt = Date()
            let storage = try SceneFixtures.decode(StorageProvidersEnvelope.self, "storage-providers")
            store.storageProviders = storage.providers
            store.storageInventory = storage.storage ?? store.board.snapshot.storage
            store.storagePolicy = storage.storagePolicy
            // Disk Writes after its first live sample and a "Read SMART".
            let io = try SceneFixtures.decode(DiskWriteStatusEnvelope.self, "io-status")
            store.diskWriteSummary = io.report?.summary ?? io.summary
            store.diskWriters = io.report?.writers ?? []
            store.diskWritePolicy = io.diskWritePolicy
            store.diskWriteHistory = try SceneFixtures.decode(DiskWriteHistoryEnvelope.self, "io-history").history
            store.diskHistoryLoadedAt = Date()
            store.diskHealth = try SceneFixtures.decode(DiskHealthEnvelope.self, "io-health").snapshot
            return store
        case .live:
            let client = try ClientFactory.make()
            let store = runnerWatch.map { PressureStore(client: client, runnerWatch: $0) } ?? PressureStore(client: client)
            store.board = try await client.refreshBoard(previous: nil)
            switch section {
            case .dayGate: await store.refreshAdvisorBrief(force: true)
            case .work:
                await store.refreshWorkPlacement(force: true)
                await store.refreshBrowserControl(force: true)
                await store.refreshWorkHeadroom()
            case .diskWrites: await store.refreshDiskWrites(live: false, includeHistory: true)
            case .storage, .policy, .settings: await store.refreshStorageInventory()
            default: break
            }
            return store
        }
    }

    /// `capacity` replaces the recorded capacity payload in `.fixtures` mode.
    static func hetzner(_ source: SceneSource, capacity override: RunnerCapacityEnvelope? = nil) async throws -> (PressureStore, HetznerStore, RunnerCapacityStore) {
        let store = try await pressure(source, for: .hetzner)
        let hetzner: HetznerStore
        let capacity: RunnerCapacityStore
        switch source {
        case .fixtures:
            hetzner = HetznerStore(
                inspect: { _, _, _, _, _ in try SceneFixtures.decode(GateRunnerEnvelope.self, "gate-runner") },
                readLog: { id, _, _ in SceneFixtures.jobLog(id: id) }
            )
            capacity = RunnerCapacityStore(fetch: { _ in try override ?? SceneFixtures.decode(RunnerCapacityEnvelope.self, "runner-capacity") })
        case .live:
            hetzner = HetznerStore()
            capacity = RunnerCapacityStore()
        }
        await hetzner.refreshNow()
        await capacity.refresh()
        return (store, hetzner, capacity)
    }
}
