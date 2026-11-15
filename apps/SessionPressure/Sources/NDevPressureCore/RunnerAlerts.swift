import Foundation

/// Which runner capacity alerts raise a macOS notification. The bell always
/// lists live warning and critical alerts while the background check is on.
public enum RunnerAlertNotify: String, CaseIterable, Identifiable, Sendable {
    case off
    case critical
    case warning

    /// Notifications still need macOS permission; this default never prompts.
    public static let `default`: RunnerAlertNotify = .critical
    public var id: String { rawValue }

    public var displayName: String {
        switch self {
        case .off: "Off"
        case .critical: "Critical only"
        case .warning: "Warning or critical"
        }
    }

    public func allows(_ severity: String) -> Bool {
        switch self {
        case .off: false
        case .critical: severity == "critical"
        case .warning: severity == "critical" || severity == "warning"
        }
    }
}

/// A capacity alert that describes the runner now, not only history.
public struct LiveRunnerAlert: Identifiable, Hashable, Sendable {
    /// Stable per host, code and severity: an ongoing episode keeps its ID,
    /// a warning that becomes critical gets a new one.
    public let id: String
    /// `CapacityAlert.id`, to focus the alert on the Hetzner page.
    public let capacityAlertID: String
    public let host: String
    public let code: String
    public let severity: String
    public let title: String
    public let detail: String
    public let since: Date?
    public let until: Date?

    public var isCritical: Bool { severity == "critical" }
}

public enum RunnerAlertTriage {
    /// The window the background check reads: 5-minute buckets keep episode
    /// ends precise, and the read is the cheapest of the four.
    public static let window: RunnerCapacityWindow = .sixHours
    /// How often the background check reads.
    public static let interval: TimeInterval = 300

    /// Keeps warning and critical alerts that hold now. Point-in-time and
    /// whole-window alerts (no episode range) hold for this read; an episode
    /// holds while it reached into the last two buckets.
    public static func live(_ report: RunnerCapacityReport) -> [LiveRunnerAlert] {
        let recent = report.capturedAt.addingTimeInterval(-2 * Double(max(report.bucketSeconds, 60)))
        return report.alerts.compactMap { alert in
            guard alert.severity == "critical" || alert.severity == "warning" else { return nil }
            if alert.since != nil {
                guard let until = alert.until, until >= recent else { return nil }
            }
            return LiveRunnerAlert(
                id: "runner/\(alert.host)/\(alert.code)/\(alert.severity)",
                capacityAlertID: alert.id,
                host: alert.host, code: alert.code, severity: alert.severity,
                title: alert.title, detail: alert.detail,
                since: alert.since, until: alert.until
            )
        }
        .sorted { ($0.isCritical ? 0 : 1, $0.host, $0.code) < ($1.isCritical ? 0 : 1, $1.host, $1.code) }
    }
}

/// Decides which live runner alerts are news. Pure, so it is tested without
/// the notification center.
///
/// Rules: the first check records a baseline and fires nothing (app launch is
/// not news); an alert fires when its ID was not live at the previous check
/// and its severity passes the setting; each ID fires at most once per
/// cooldown, so a flapping episode does not repeat. A failed check is missing
/// evidence and leaves the previous set unchanged.
public struct RunnerAlertGate: Sendable {
    public static let defaultCooldown: TimeInterval = 30 * 60

    public let cooldown: TimeInterval
    private var previous: Set<String>?
    private var lastFired: [String: Date] = [:]

    public init(cooldown: TimeInterval = RunnerAlertGate.defaultCooldown) {
        self.cooldown = cooldown
    }

    public mutating func observe(_ alerts: [LiveRunnerAlert], notify: RunnerAlertNotify, at now: Date = Date()) -> [LiveRunnerAlert] {
        let ids = Set(alerts.map(\.id))
        defer {
            previous = ids
            lastFired = lastFired.filter { now.timeIntervalSince($0.value) < cooldown }
        }
        guard let previous else { return [] }
        return alerts.filter { alert in
            guard !previous.contains(alert.id), notify.allows(alert.severity) else { return false }
            if let last = lastFired[alert.id], now.timeIntervalSince(last) < cooldown { return false }
            lastFired[alert.id] = now
            return true
        }
    }
}
