import Foundation
import NDevPressureCore

/// View state for the Hetzner tab's capacity card. The Go CLI does every SSH
/// and file read; this store decides when to ask.
///
/// Reads happen only while the pane is on screen (window visible, app
/// active, Hetzner selected) AND the card itself has scrolled into view. The
/// cadence follows the window: the host buckets are 5 minutes to 2 hours
/// wide, so re-reading a 7-day view every minute only burns SSH and CPU.
/// The store lives in ContentView, so switching tabs keeps the last report
/// and returning within the cadence does not trigger a new read.
@MainActor
final class RunnerCapacityStore: ObservableObject {
    typealias Fetch = @Sendable (RunnerCapacityWindow) async throws -> RunnerCapacityEnvelope

    /// A runner as displayed: the latest good data, and when the newest read
    /// for it failed, that failure and the age of the data still shown.
    struct DisplayHost: Identifiable {
        let capacity: HostCapacity
        let staleSince: Date?
        let failure: String?
        var id: String { capacity.host }
        var isStale: Bool { staleSince != nil }
    }

    @Published private(set) var report: RunnerCapacityReport?
    @Published private(set) var hosts: [DisplayHost] = []
    @Published private(set) var error: String?
    @Published private(set) var loading = false
    @Published private(set) var fetchedAt: Date?
    @Published private(set) var window: RunnerCapacityWindow = .day
    @Published var selectedHost: String?
    @Published var focusedAlert: CapacityAlert.ID?

    private let fetch: Fetch
    private let now: @Sendable () -> Date
    private var loopTask: Task<Void, Never>?
    private var generation = UUID()
    private var paneActive = false
    private var cardVisible = false
    private var pendingRefresh = false
    /// Last good per-host data and when it was read.
    private var lastGood: [String: (HostCapacity, Date)] = [:]

    init(fetch: @escaping Fetch = { window in
        try await ClientFactory.make().runnerCapacity(window: window)
    }, now: @escaping @Sendable () -> Date = { Date() }) {
        self.fetch = fetch
        self.now = now
    }

    static func cadence(for window: RunnerCapacityWindow) -> Duration {
        switch window {
        case .sixHours: .seconds(60)
        case .day: .seconds(120)
        case .week, .fortnight: .seconds(300)
        }
    }

    /// True when the shown report belongs to another window than the picker.
    var reportIsForOtherWindow: Bool {
        guard let report else { return false }
        return report.window != window.rawValue
    }

    var selected: DisplayHost? {
        if let selectedHost, let match = hosts.first(where: { $0.id == selectedHost }) {
            return match
        }
        return hosts.first(where: { $0.capacity.isActive }) ?? hosts.first
    }

    var polling: Bool { loopTask != nil }

    // MARK: Lifecycle

    /// ContentView: window visible, app active and the Hetzner pane selected.
    func setPaneActive(_ value: Bool) {
        paneActive = value
        reconcile()
    }

    /// The card's own onAppear/onDisappear, so a card scrolled out of a lazy
    /// stack stops reading too.
    func setCardVisible(_ value: Bool) {
        cardVisible = value
        reconcile()
    }

    private func reconcile() {
        if paneActive && cardVisible { start() } else { stop() }
    }

    private func start() {
        guard loopTask == nil else { return }
        let token = UUID()
        generation = token
        loopTask = Task { [weak self] in
            while !Task.isCancelled {
                guard let self, self.generation == token else { return }
                let cadence = Self.cadence(for: self.window)
                if self.isDue(cadence) {
                    await self.refresh()
                }
                let wait = self.remaining(cadence)
                do { try await Task.sleep(for: wait) } catch { return }
            }
        }
    }

    private func stop() {
        generation = UUID()
        loopTask?.cancel()
        loopTask = nil
    }

    private func isDue(_ cadence: Duration) -> Bool {
        guard let fetchedAt, report?.window == window.rawValue else { return true }
        return now().timeIntervalSince(fetchedAt) >= Self.seconds(cadence) - 1
    }

    private func remaining(_ cadence: Duration) -> Duration {
        guard let fetchedAt else { return cadence }
        let left = Self.seconds(cadence) - now().timeIntervalSince(fetchedAt)
        return .milliseconds(Int(max(5, left) * 1000))
    }

    private static func seconds(_ d: Duration) -> Double {
        Double(d.components.seconds) + Double(d.components.attoseconds) / 1e18
    }

    // MARK: Reads

    func setWindow(_ value: RunnerCapacityWindow) async {
        guard value != window else { return }
        window = value
        await refresh()
    }

    /// A refresh requested while one is in flight runs once that read ends,
    /// so a window change made mid-read is never dropped.
    func refresh() async {
        if loading {
            pendingRefresh = true
            return
        }
        loading = true
        defer { loading = false }
        repeat {
            pendingRefresh = false
            await readOnce()
        } while pendingRefresh
    }

    private func readOnce() async {
        let window = self.window
        do {
            let envelope = try await fetch(window)
            // A newer window was picked during the read: the pending read
            // fetches it; do not show this one under the wrong label.
            guard window == self.window else { pendingRefresh = true; return }
            if !envelope.ok || envelope.capacity.schemaVersion != 1 {
                error = "Unsupported runner capacity response; update session-pressure."
            } else if let failure = envelope.capacity.error {
                error = Self.describe(failure)
            } else {
                apply(envelope.capacity)
                error = nil
            }
        } catch {
            self.error = Self.describeTransport(error)
        }
    }

    private func apply(_ r: RunnerCapacityReport) {
        let at = now()
        var next: [DisplayHost] = []
        for host in r.hosts {
            if host.reachable {
                lastGood[host.host] = (host, at)
                next.append(DisplayHost(capacity: host, staleSince: nil, failure: nil))
            } else if let (good, goodAt) = lastGood[host.host], good.platform == host.platform {
                // Keep the last good picture, clearly marked, instead of
                // blanking the tile on one failed SSH read.
                next.append(DisplayHost(capacity: good, staleSince: goodAt, failure: host.error))
            } else {
                next.append(DisplayHost(capacity: host, staleSince: nil, failure: host.error))
            }
        }
        // Hosts removed from the inventory leave the cache too.
        let names = Set(r.hosts.map(\.host))
        lastGood = lastGood.filter { names.contains($0.key) }
        report = r
        hosts = next
        fetchedAt = at
        if let focusedAlert, !r.alerts.contains(where: { $0.id == focusedAlert }) {
            self.focusedAlert = nil
        }
    }

    // MARK: Operator text

    static func describe(_ code: String) -> String {
        switch code {
        case "runner_fleet_inventory_missing":
            return "No installed runner fleet inventory (~/.nicos-dev/gaterunner-fleet/v1/fleet.json)."
        case "runner_fleet_inventory_invalid":
            return "The installed runner fleet inventory is invalid."
        case "runner_capacity_timeout":
            return "SSH read timed out (12 s). The host may be overloaded or the tailnet slow."
        case "runner_capacity_connect_failed", "runner_capacity_read_failed":
            return "SSH connection failed (host down, DNS, tailnet or key)."
        case "runner_capacity_remote_failed":
            return "The read-only probe failed on the host."
        case "runner_capacity_output_too_large":
            return "The host's probe output exceeded 1 MiB."
        case "runner_capacity_payload_invalid":
            return "The host returned an unreadable probe."
        case "network_history_unavailable":
            return "Local bandwidth history store is unavailable; showing since-boot averages only."
        case "network_history_write_failed", "network_history_read_failed":
            return "Local bandwidth history could not be updated."
        default:
            return code
        }
    }

    static func describeTransport(_ error: Error) -> String {
        if let e = error as? NDevPressureClientError {
            return "Capacity read failed: \(e.localizedDescription)"
        }
        return "Capacity read failed: \(error.localizedDescription)"
    }
}
