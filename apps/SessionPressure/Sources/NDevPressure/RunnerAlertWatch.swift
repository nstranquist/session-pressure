import Foundation
import NDevPressureCore

/// Background runner capacity check. Reads the 6-hour capacity report every
/// 5 minutes for as long as the app runs (window open, hidden or closed to
/// the menu bar) so a spike or a failing runner reaches the notification bell
/// without the Hetzner tab open.
///
/// Cost per check, measured on the installed CLI: one SSH read per runner,
/// about 2 s wall, about 17 MB transient CLI memory, 34-85 KB of JSON. A
/// report the Hetzner tab already read for the same window counts as a check,
/// so the tab and the watch never read twice. The check is skipped while this
/// Mac is at critical pressure.
@MainActor
final class RunnerAlertWatch: ObservableObject {
    typealias Fetch = @Sendable (RunnerCapacityWindow) async throws -> RunnerCapacityEnvelope

    /// Failed checks in a row before the bell reports the check itself.
    static let failureThreshold = 3

    @Published private(set) var live: [LiveRunnerAlert] = []
    @Published private(set) var lastCheckAt: Date?
    @Published private(set) var lastError: String?
    @Published private(set) var consecutiveFailures = 0
    @Published private(set) var checking = false
    @Published private(set) var lastNotifiedAt: Date?
    @Published private(set) var skippedForPressure = false

    /// Called with alerts that are news under the notify setting.
    var onFire: ([LiveRunnerAlert]) -> Void = { _ in }
    /// The Mac's own pressure; a critical host skips the SSH fan-out.
    var shouldSkip: () -> Bool = { false }

    private let fetch: Fetch
    private let now: @Sendable () -> Date
    private let enabled: () -> Bool
    private let notify: () -> RunnerAlertNotify
    private var gate = RunnerAlertGate()
    private var loopTask: Task<Void, Never>?
    /// Any attempt, including skips and failures; drives the cadence.
    private var lastAttemptAt: Date?

    init(
        fetch: @escaping Fetch = { window in try await ClientFactory.make().runnerCapacity(window: window) },
        now: @escaping @Sendable () -> Date = { Date() },
        enabled: @escaping () -> Bool = { AppPreferences.runnerWatch() },
        notify: @escaping () -> RunnerAlertNotify = { AppPreferences.runnerAlertNotify() }
    ) {
        self.fetch = fetch
        self.now = now
        self.enabled = enabled
        self.notify = notify
    }

    var isRunning: Bool { loopTask != nil }
    var isEnabled: Bool { enabled() }

    /// The bell reports the check itself only after repeated failures, so one
    /// slow SSH read does not raise an alert.
    var checkFailing: Bool { consecutiveFailures >= Self.failureThreshold }

    // MARK: Lifecycle

    func start() {
        guard loopTask == nil, enabled() else { return }
        loopTask = Task { [weak self] in
            // Let launch work settle before the first SSH fan-out.
            try? await Task.sleep(for: .seconds(20))
            while !Task.isCancelled {
                guard let self else { return }
                if self.isDue { await self.check() }
                do { try await Task.sleep(for: self.untilDue) } catch { return }
            }
        }
    }

    func stop() {
        loopTask?.cancel()
        loopTask = nil
    }

    /// Settings toggled the watch: start or stop, and clear the bell when off.
    func preferencesChanged() {
        if enabled() {
            start()
        } else {
            stop()
            live = []
            consecutiveFailures = 0
            lastError = nil
        }
    }

    private var isDue: Bool {
        guard let lastAttemptAt else { return true }
        return now().timeIntervalSince(lastAttemptAt) >= RunnerAlertTriage.interval - 1
    }

    private var untilDue: Duration {
        guard let lastAttemptAt else { return .seconds(RunnerAlertTriage.interval) }
        let left = RunnerAlertTriage.interval - now().timeIntervalSince(lastAttemptAt)
        return .milliseconds(Int(max(5, left) * 1000))
    }

    // MARK: Checks

    /// One background check. Skipped while the Mac is at critical pressure;
    /// the next check is due at the normal interval.
    func check() async {
        guard !checking, enabled() else { return }
        lastAttemptAt = now()
        skippedForPressure = shouldSkip()
        if skippedForPressure { return }
        checking = true
        defer { checking = false }
        do {
            let envelope = try await fetch(RunnerAlertTriage.window)
            guard envelope.ok, envelope.capacity.schemaVersion == 1 else {
                fail("Unsupported runner capacity response; update session-pressure.")
                return
            }
            if let error = envelope.capacity.error {
                fail(RunnerCapacityStore.describe(error))
                return
            }
            observe(envelope.capacity)
        } catch {
            fail(RunnerCapacityStore.describeTransport(error))
        }
    }

    /// A report the Hetzner tab read. Only the watch's own window counts, so
    /// alert rules stay the same whichever path read it.
    func ingest(_ report: RunnerCapacityReport) {
        guard enabled(), report.window == RunnerAlertTriage.window.rawValue, report.error == nil else { return }
        observe(report)
    }

    private func observe(_ report: RunnerCapacityReport) {
        let alerts = RunnerAlertTriage.live(report)
        let fired = gate.observe(alerts, notify: notify(), at: now())
        if live != alerts { live = alerts }
        lastCheckAt = now()
        lastAttemptAt = lastCheckAt
        lastError = nil
        consecutiveFailures = 0
        if !fired.isEmpty {
            lastNotifiedAt = now()
            onFire(fired)
        }
    }

    private func fail(_ message: String) {
        // Keep the last live set: a failed read is missing evidence, not an
        // all-clear. Unreachable runners arrive as their own alerts.
        lastError = message
        consecutiveFailures += 1
    }
}
