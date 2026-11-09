import Foundation

/// App-side preferences for the NDev Pressure desktop client.
///
/// These values change only how this app reads and presents the control plane.
/// Guard policy (thresholds, admission, shedding) stays owned by the Go guard
/// and is changed only through its own typed commands.
public enum AppPreferenceKey {
    /// Existing key; the name is kept so saved values survive upgrades.
    public static let showMenuBarExtra = "showMenuBarExtra"
    public static let launchPane = "launchPane"
    public static let lastPane = "lastPane"
    public static let dockBadgeMode = "dockBadgeMode"
    public static let alertThreshold = "alertThreshold"
    public static let refreshProfile = "refreshProfile"
    public static let showDayGate = "sidebarShowDayGate"
    public static let showHetzner = "sidebarShowHetzner"
    public static let pressureBinaryOverride = "pressureBinaryOverride"
    public static let ndevBinaryOverride = "ndevBinaryOverride"
    public static let apiSocketOverride = "apiSocketOverride"
    public static let apiURLOverride = "apiURLOverride"
    public static let apiTokenFileOverride = "apiTokenFileOverride"
    public static let apiTimeoutSeconds = "apiTimeoutSeconds"

    /// Every key this app owns. Reset removes exactly these and nothing else
    /// (for example, never the trace-helper registration record).
    public static let all: [String] = [
        showMenuBarExtra, launchPane, lastPane, dockBadgeMode, alertThreshold,
        refreshProfile, showDayGate, showHetzner, pressureBinaryOverride,
        ndevBinaryOverride, apiSocketOverride, apiURLOverride,
        apiTokenFileOverride, apiTimeoutSeconds,
    ]

    /// Keys that change how the control-plane client is built.
    public static let clientKeys: [String] = [
        pressureBinaryOverride, ndevBinaryOverride, apiSocketOverride,
        apiURLOverride, apiTokenFileOverride, apiTimeoutSeconds,
    ]
}

/// How often the app reads the control plane. Adaptive is the historical
/// cadence; the slower profiles scale every interval and keep a hard ceiling so
/// the menu-bar gauge never looks calm while it is really stale.
public enum RefreshProfile: String, CaseIterable, Identifiable, Sendable {
    case adaptive
    case relaxed
    case minimal

    public static let `default`: RefreshProfile = .adaptive
    public static let maximumInterval: TimeInterval = 300

    public var id: String { rawValue }

    public var displayName: String {
        switch self {
        case .adaptive: "Adaptive"
        case .relaxed: "Relaxed (2× slower)"
        case .minimal: "Minimal (4× slower)"
        }
    }

    public var multiplier: Double {
        switch self {
        case .adaptive: 1
        case .relaxed: 2
        case .minimal: 4
        }
    }

    public func scaled(_ base: TimeInterval) -> TimeInterval {
        min(base * multiplier, Self.maximumInterval)
    }
}

public enum DockBadgeMode: String, CaseIterable, Identifiable, Sendable {
    /// Historical behavior: Day Gate badge when a brief exists, else level.
    case dayGateAndLevel
    case levelOnly
    case off

    public static let `default`: DockBadgeMode = .dayGateAndLevel
    public var id: String { rawValue }

    public var displayName: String {
        switch self {
        case .dayGateAndLevel: "Day Gate and pressure level"
        case .levelOnly: "Pressure level only"
        case .off: "Off"
        }
    }

    /// Level badge text used when no Day Gate badge applies.
    public static func levelBadge(_ level: PressureLevel) -> String? {
        switch level {
        case .normal, .unknown: nil
        case .warning: "!"
        case .red: "RED"
        case .critical: "CRIT"
        }
    }
}

public enum AlertThreshold: String, CaseIterable, Identifiable, Sendable {
    case off
    case red
    case critical

    public static let `default`: AlertThreshold = .off
    public var id: String { rawValue }

    public var displayName: String {
        switch self {
        case .off: "Off"
        case .red: "Red or Critical"
        case .critical: "Critical only"
        }
    }

    public var minimumLevel: PressureLevel? {
        switch self {
        case .off: nil
        case .red: .red
        case .critical: .critical
        }
    }
}

/// Decides when a pressure-level notification is due. Pure, so it is tested
/// without the notification center.
///
/// Rules: fire only on a rising transition into a level at or above the
/// threshold; never on the first observed sample (app launch is not news) and
/// never for unknown; at most once per level within the cooldown.
public struct LevelAlertGate: Sendable {
    public static let defaultCooldown: TimeInterval = 10 * 60

    public let cooldown: TimeInterval
    private var previous: PressureLevel?
    private var lastFired: [PressureLevel: Date] = [:]

    public init(cooldown: TimeInterval = LevelAlertGate.defaultCooldown) {
        self.cooldown = cooldown
    }

    /// Records `level` and returns it when an alert is due.
    public mutating func observe(_ level: PressureLevel, threshold: AlertThreshold, at now: Date = Date()) -> PressureLevel? {
        // An unknown sample is missing evidence, not a transition. Keep the last
        // known level so a read failure does not re-arm the alert.
        guard level != .unknown else { return nil }
        defer { previous = level }
        guard let previous else { return nil }
        guard let minimum = threshold.minimumLevel, level >= minimum, level > previous else { return nil }
        if let last = lastFired[level], now.timeIntervalSince(last) < cooldown { return nil }
        lastFired[level] = now
        return level
    }
}

/// Where a resolved control-plane value came from.
public enum PreferenceSource: String, Sendable {
    case saved = "Saved override"
    case environment = "Environment"
    case automatic = "Automatic"
    case missing = "Not found"
}

/// A saved override that the app refused to apply.
public struct PreferenceProblem: Hashable, Sendable {
    public var key: String
    public var message: String
}

public enum AppPreferences {
    /// API timeout bounds match `NDevPressureAPIClient` (1–30 s). Zero = unset.
    public static let apiTimeoutRange: ClosedRange<Int> = 1...30

    public static func refreshProfile(_ defaults: UserDefaults = .standard) -> RefreshProfile {
        defaults.string(forKey: AppPreferenceKey.refreshProfile).flatMap(RefreshProfile.init(rawValue:)) ?? .default
    }

    public static func dockBadgeMode(_ defaults: UserDefaults = .standard) -> DockBadgeMode {
        defaults.string(forKey: AppPreferenceKey.dockBadgeMode).flatMap(DockBadgeMode.init(rawValue:)) ?? .default
    }

    public static func alertThreshold(_ defaults: UserDefaults = .standard) -> AlertThreshold {
        defaults.string(forKey: AppPreferenceKey.alertThreshold).flatMap(AlertThreshold.init(rawValue:)) ?? .default
    }

    /// Saved string override, trimmed; empty means "not set".
    public static func string(_ key: String, _ defaults: UserDefaults = .standard) -> String? {
        guard let raw = defaults.string(forKey: key)?.trimmingCharacters(in: .whitespacesAndNewlines),
              !raw.isEmpty else { return nil }
        return (raw as NSString).expandingTildeInPath
    }

    /// Overlay saved control-plane overrides onto `base` using the exact
    /// environment names the client already reads, so saved values and shell
    /// env share one resolution path. Saved values win. Invalid overrides are
    /// not applied; `problems` reports them for the Settings page.
    ///
    /// A token value is never stored; only the token file path.
    public static func clientEnvironment(
        base: [String: String] = ProcessInfo.processInfo.environment,
        defaults: UserDefaults = .standard,
        isExecutable: (String) -> Bool = { FileManager.default.isExecutableFile(atPath: $0) },
        fileExists: (String) -> Bool = { FileManager.default.fileExists(atPath: $0) }
    ) -> (environment: [String: String], problems: [PreferenceProblem]) {
        var env = base
        var problems: [PreferenceProblem] = []

        func binary(_ key: String, _ name: String, label: String) {
            guard let path = string(key, defaults) else { return }
            if path.hasPrefix("/"), isExecutable(path) {
                env[name] = path
            } else {
                problems.append(PreferenceProblem(key: key, message: "\(label) override is not an executable file: \(path). Automatic resolution is used."))
            }
        }
        binary(AppPreferenceKey.pressureBinaryOverride, "SESSION_PRESSURE_BIN", label: "session-pressure")
        binary(AppPreferenceKey.ndevBinaryOverride, "NDEV_BIN", label: "ndev")

        if let socket = string(AppPreferenceKey.apiSocketOverride, defaults) {
            if socket.hasPrefix("/") {
                env["NDEV_PRESSURE_API_SOCKET"] = socket
            } else {
                problems.append(PreferenceProblem(key: AppPreferenceKey.apiSocketOverride, message: "API socket must be an absolute path."))
            }
        }
        if let url = string(AppPreferenceKey.apiURLOverride, defaults) {
            if isLoopbackHTTPURL(url) {
                env["NDEV_PRESSURE_API_URL"] = url
            } else {
                problems.append(PreferenceProblem(key: AppPreferenceKey.apiURLOverride, message: "API URL must be http(s) on localhost, 127.0.0.1 or ::1, without credentials, query or fragment."))
            }
        }
        if let tokenFile = string(AppPreferenceKey.apiTokenFileOverride, defaults) {
            if tokenFile.hasPrefix("/"), fileExists(tokenFile) {
                env["NDEV_PRESSURE_API_TOKEN_FILE"] = tokenFile
                // A file override must not lose to an inline token in env.
                env.removeValue(forKey: "NDEV_PRESSURE_API_TOKEN")
            } else {
                problems.append(PreferenceProblem(key: AppPreferenceKey.apiTokenFileOverride, message: "API token file does not exist: \(tokenFile)."))
            }
        }
        let timeout = defaults.integer(forKey: AppPreferenceKey.apiTimeoutSeconds)
        if timeout != 0 {
            env["NDEV_PRESSURE_API_TIMEOUT_SECONDS"] = String(min(max(timeout, apiTimeoutRange.lowerBound), apiTimeoutRange.upperBound))
        }
        return (env, problems)
    }

    /// Delegates to the API client so Settings and the client share one rule.
    public static func isLoopbackHTTPURL(_ raw: String) -> Bool {
        NDevPressureAPIClient.isAllowedURL(raw)
    }

    /// Source of an environment-backed value after the overlay.
    public static func source(
        savedKey: String,
        envNames: [String],
        base: [String: String],
        defaults: UserDefaults = .standard,
        problems: [PreferenceProblem]
    ) -> PreferenceSource {
        if string(savedKey, defaults) != nil, !problems.contains(where: { $0.key == savedKey }) { return .saved }
        if envNames.contains(where: { !(base[$0] ?? "").isEmpty }) { return .environment }
        return .automatic
    }

    /// Remove every app-owned preference. Returns the keys removed.
    @discardableResult
    public static func reset(_ defaults: UserDefaults = .standard) -> [String] {
        let present = AppPreferenceKey.all.filter { defaults.object(forKey: $0) != nil }
        present.forEach { defaults.removeObject(forKey: $0) }
        return present
    }
}
