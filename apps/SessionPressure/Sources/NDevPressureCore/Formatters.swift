import Foundation

public enum PressureFormat {
    public static func mb(_ value: Double) -> String {
        if value >= 1024 {
            let gib = value / 1024
            return String(format: gib == gib.rounded() ? "%.0f GiB" : "%.1f GiB", gib)
        }
        return String(format: value == value.rounded() ? "%.0f MiB" : "%.1f MiB", value)
    }

    public static func percent(_ value: Double) -> String {
        if value == value.rounded() {
            return String(format: "%.0f%%", value)
        }
        return String(format: "%.1f%%", value)
    }

    public static func percentInt(_ value: Int) -> String {
        "\(value)%"
    }

    public static func duration(seconds: Int64?) -> String {
        guard let seconds, seconds >= 0 else { return "—" }
        let s = Int(seconds)
        if s < 60 { return "\(s)s" }
        if s < 3600 { return "\(s / 60)m" }
        if s < 86_400 {
            let h = s / 3600
            let m = (s % 3600) / 60
            return m == 0 ? "\(h)h" : "\(h)h \(m)m"
        }
        let d = s / 86_400
        let h = (s % 86_400) / 3600
        return h == 0 ? "\(d)d" : "\(d)d \(h)h"
    }

    public static func durationMS(_ milliseconds: Int64) -> String {
        if milliseconds < 1_000 { return "\(milliseconds)ms" }
        return duration(seconds: milliseconds / 1_000)
    }

    public static func relative(_ date: Date?) -> String {
        guard let date else { return "—" }
        let formatter = RelativeDateTimeFormatter()
        formatter.unitsStyle = .abbreviated
        return formatter.localizedString(for: date, relativeTo: Date())
    }

    public static func shortTime(_ date: Date?) -> String {
        guard let date else { return "—" }
        return date.formatted(date: .omitted, time: .shortened)
    }

    public static func bytes(_ value: Int64?) -> String {
        guard let value else { return "—" }
        if value < 1024 { return "\(value) B" }
        if value < 1024 * 1024 {
            return String(format: "%.1f KiB", Double(value) / 1024)
        }
        if value < 1024 * 1024 * 1024 {
            return String(format: "%.2f MiB", Double(value) / (1024 * 1024))
        }
        return String(format: "%.1f GiB", Double(value) / (1024 * 1024 * 1024))
    }

    public static func pid(_ value: Int?) -> String {
        guard let value, value > 0 else { return "—" }
        return String(value)
    }

    public static func shortSession(_ id: String?) -> String {
        guard let id, !id.isEmpty else { return "—" }
        if id.count <= 12 { return id }
        return String(id.prefix(8)) + "…"
    }

    public static func agentLabel(_ agent: String) -> String {
        agent.isEmpty ? "agent" : agent
    }

    /// Adaptive UI poll interval mirroring resident cadence philosophy.
    public static func pollInterval(for level: PressureLevel) -> TimeInterval {
        switch level {
        case .critical: return 5
        case .red: return 8
        case .warning: return 12
        case .normal, .unknown: return 20
        }
    }

    /// Cadence while the main window is closed. The menu-bar extra outlives the
    /// window by design, so its gauge must keep moving — but nothing else is on
    /// screen, so this is deliberately far slower than the windowed ladder.
    public static let menuBarOnlyPollInterval: TimeInterval = 60

    public static let workFocusBusyPollInterval: TimeInterval = 2.5
    public static let workFocusIdlePollInterval: TimeInterval = 10
    public static let workFocusSuspendedCheckInterval: TimeInterval = 30

    /// Work Queue focus cadence: near-live for active work, lower churn when
    /// empty, and no CLI refresh while the interface is inactive.
    public static func workFocusPollInterval(
        queueDepth: Int,
        leaseCount: Int,
        interfaceActive: Bool
    ) -> TimeInterval {
        guard interfaceActive else { return workFocusSuspendedCheckInterval }
        if queueDepth > 0 || leaseCount > 0 { return workFocusBusyPollInterval }
        return workFocusIdlePollInterval
    }

    public static func shortOperationID(_ id: String?) -> String {
        guard let id, !id.isEmpty else { return "—" }
        if id.count <= 12 { return id }
        return String(id.prefix(8)) + "…"
    }

    public static func shortDigest(_ digest: String?) -> String {
        guard let digest, !digest.isEmpty else { return "—" }
        if digest.hasPrefix("sha256:"), digest.count > 19 {
            return "sha256:" + String(digest.dropFirst(7).prefix(8)) + "…"
        }
        if digest.count <= 16 { return digest }
        return String(digest.prefix(12)) + "…"
    }
}
