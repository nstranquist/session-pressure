import Foundation
import NDevPressureCore

/// View state for the Hetzner tab's capacity card. The Go CLI does every SSH
/// and file read; this store polls it while the card is visible and keeps the
/// last good report when a read fails, so a transient SSH error never blanks
/// the charts.
@MainActor
final class RunnerCapacityStore: ObservableObject {
    typealias Fetch = @Sendable (RunnerCapacityWindow) async throws -> RunnerCapacityEnvelope

    @Published private(set) var report: RunnerCapacityReport?
    @Published private(set) var error: String?
    @Published private(set) var loading = false
    @Published private(set) var fetchedAt: Date?
    @Published private(set) var window: RunnerCapacityWindow = .day
    @Published var selectedHost: String?

    static let pollInterval: Duration = .seconds(60)

    private let fetch: Fetch
    private var loopTask: Task<Void, Never>?
    private var generation = UUID()

    init(fetch: @escaping Fetch = { window in
        try await ClientFactory.make().runnerCapacity(window: window)
    }) {
        self.fetch = fetch
    }

    var selected: HostCapacity? {
        guard let report else { return nil }
        if let selectedHost, let match = report.hosts.first(where: { $0.host == selectedHost }) {
            return match
        }
        return report.hosts.first(where: \.isActive) ?? report.hosts.first
    }

    func start() {
        guard loopTask == nil else { return }
        let token = generation
        loopTask = Task { [weak self] in
            while !Task.isCancelled {
                guard let self, self.generation == token else { return }
                await self.refresh()
                do { try await Task.sleep(for: Self.pollInterval) } catch { return }
            }
        }
    }

    func stop() {
        generation = UUID()
        loopTask?.cancel()
        loopTask = nil
        loading = false
    }

    func setWindow(_ value: RunnerCapacityWindow) async {
        guard value != window else { return }
        window = value
        await refresh()
    }

    func refresh() async {
        guard !loading else { return }
        loading = true
        let token = generation
        let window = self.window
        do {
            let envelope = try await fetch(window)
            guard generation == token, window == self.window else { loading = false; return }
            if !envelope.ok || envelope.capacity.schemaVersion != 1 {
                error = "Unsupported runner capacity response; update session-pressure."
            } else if let failure = envelope.capacity.error {
                error = Self.describe(failure)
            } else {
                report = envelope.capacity
                fetchedAt = Date()
                error = nil
            }
        } catch {
            guard generation == token else { loading = false; return }
            self.error = error.localizedDescription
        }
        loading = false
    }

    static func describe(_ code: String) -> String {
        switch code {
        case "runner_fleet_inventory_missing":
            return "No installed runner fleet inventory (~/.nicos-dev/gaterunner-fleet/v1/fleet.json)."
        case "runner_fleet_inventory_invalid":
            return "The installed runner fleet inventory is invalid."
        case "runner_capacity_read_failed":
            return "SSH read failed"
        case "runner_capacity_payload_invalid":
            return "The runner returned an unreadable probe"
        case "network_history_unavailable":
            return "Local bandwidth history store is unavailable; showing since-boot averages only."
        case "network_history_write_failed", "network_history_read_failed":
            return "Local bandwidth history could not be updated."
        default:
            return code
        }
    }
}
