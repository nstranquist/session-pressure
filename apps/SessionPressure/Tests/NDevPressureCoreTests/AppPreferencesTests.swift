import Foundation
import Testing
@testable import NDevPressure
@testable import NDevPressureCore

/// Each test gets a private defaults domain so no real preference is read or written.
private func isolatedDefaults(_ name: String = #function) -> UserDefaults {
    let suite = "ndev-pressure-tests.\(name).\(UUID().uuidString)"
    let defaults = UserDefaults(suiteName: suite)!
    defaults.removePersistentDomain(forName: suite)
    return defaults
}

@Suite("App preferences")
struct AppPreferencesTests {
    @Test("defaults keep historical behavior")
    func defaults() {
        let d = isolatedDefaults()
        #expect(AppPreferences.refreshProfile(d) == .adaptive)
        #expect(AppPreferences.dockBadgeMode(d) == .dayGateAndLevel)
        #expect(AppPreferences.alertThreshold(d) == .off)
        let resolved = AppPreferences.clientEnvironment(base: ["HOME": "/h"], defaults: d)
        #expect(resolved.environment == ["HOME": "/h"])
        #expect(resolved.problems.isEmpty)
    }

    @Test("unknown saved enum values fall back to defaults")
    func unknownEnumValues() {
        let d = isolatedDefaults()
        d.set("turbo", forKey: AppPreferenceKey.refreshProfile)
        d.set("loud", forKey: AppPreferenceKey.alertThreshold)
        #expect(AppPreferences.refreshProfile(d) == .adaptive)
        #expect(AppPreferences.alertThreshold(d) == .off)
    }

    @Test("saved binary overrides win over environment")
    func overrideWinsOverEnvironment() {
        let d = isolatedDefaults()
        d.set("/opt/custom/session-pressure", forKey: AppPreferenceKey.pressureBinaryOverride)
        d.set("/opt/custom/ndev", forKey: AppPreferenceKey.ndevBinaryOverride)
        let base = ["SESSION_PRESSURE_BIN": "/env/session-pressure", "NDEV_BIN": "/env/ndev"]
        let resolved = AppPreferences.clientEnvironment(base: base, defaults: d, isExecutable: { _ in true })
        #expect(resolved.environment["SESSION_PRESSURE_BIN"] == "/opt/custom/session-pressure")
        #expect(resolved.environment["NDEV_BIN"] == "/opt/custom/ndev")
        #expect(resolved.problems.isEmpty)
        #expect(AppPreferences.source(savedKey: AppPreferenceKey.pressureBinaryOverride, envNames: ["SESSION_PRESSURE_BIN"], base: base, defaults: d, problems: []) == .saved)
    }

    @Test("non-executable or relative overrides are refused and reported")
    func invalidBinaryOverride() {
        let d = isolatedDefaults()
        d.set("/nope/session-pressure", forKey: AppPreferenceKey.pressureBinaryOverride)
        d.set("relative/ndev", forKey: AppPreferenceKey.ndevBinaryOverride)
        let base = ["SESSION_PRESSURE_BIN": "/env/session-pressure"]
        let resolved = AppPreferences.clientEnvironment(base: base, defaults: d, isExecutable: { $0 != "/nope/session-pressure" })
        #expect(resolved.environment["SESSION_PRESSURE_BIN"] == "/env/session-pressure")
        #expect(resolved.environment["NDEV_BIN"] == nil)
        #expect(resolved.problems.map(\.key).sorted() == [AppPreferenceKey.ndevBinaryOverride, AppPreferenceKey.pressureBinaryOverride].sorted())
        #expect(AppPreferences.source(savedKey: AppPreferenceKey.pressureBinaryOverride, envNames: ["SESSION_PRESSURE_BIN"], base: base, defaults: d, problems: resolved.problems) == .environment)
    }

    @Test("API URL must be plain loopback http(s)")
    func apiURLValidation() {
        #expect(AppPreferences.isLoopbackHTTPURL("http://127.0.0.1:8123"))
        #expect(AppPreferences.isLoopbackHTTPURL("https://localhost/api"))
        #expect(!AppPreferences.isLoopbackHTTPURL("http://example.com"))
        #expect(!AppPreferences.isLoopbackHTTPURL("http://user:pw@localhost"))
        #expect(!AppPreferences.isLoopbackHTTPURL("http://localhost?x=1"))
        #expect(!AppPreferences.isLoopbackHTTPURL("file:///tmp/sock"))

        let d = isolatedDefaults()
        d.set("http://evil.example", forKey: AppPreferenceKey.apiURLOverride)
        let resolved = AppPreferences.clientEnvironment(base: [:], defaults: d)
        #expect(resolved.environment["NDEV_PRESSURE_API_URL"] == nil)
        #expect(resolved.problems.count == 1)
    }

    @Test("token file override replaces an inline env token; no token value is stored")
    func tokenFileOnly() {
        let d = isolatedDefaults()
        d.set("/secure/token", forKey: AppPreferenceKey.apiTokenFileOverride)
        let resolved = AppPreferences.clientEnvironment(
            base: ["NDEV_PRESSURE_API_TOKEN": "inline-secret"], defaults: d, fileExists: { $0 == "/secure/token" })
        #expect(resolved.environment["NDEV_PRESSURE_API_TOKEN_FILE"] == "/secure/token")
        #expect(resolved.environment["NDEV_PRESSURE_API_TOKEN"] == nil)
        #expect(!AppPreferenceKey.all.contains { $0.lowercased().hasSuffix("token") })
    }

    @Test("API timeout is clamped to 1–30 and 0 means unset")
    func apiTimeoutClamp() {
        let d = isolatedDefaults()
        #expect(AppPreferences.clientEnvironment(base: [:], defaults: d).environment["NDEV_PRESSURE_API_TIMEOUT_SECONDS"] == nil)
        d.set(99, forKey: AppPreferenceKey.apiTimeoutSeconds)
        #expect(AppPreferences.clientEnvironment(base: [:], defaults: d).environment["NDEV_PRESSURE_API_TIMEOUT_SECONDS"] == "30")
        d.set(-4, forKey: AppPreferenceKey.apiTimeoutSeconds)
        #expect(AppPreferences.clientEnvironment(base: [:], defaults: d).environment["NDEV_PRESSURE_API_TIMEOUT_SECONDS"] == "1")
    }

    @Test("reset removes only app-owned keys")
    func resetScope() {
        let d = isolatedDefaults()
        d.set("relaxed", forKey: AppPreferenceKey.refreshProfile)
        d.set(false, forKey: AppPreferenceKey.showMenuBarExtra)
        d.set("12", forKey: "traceHelperRegisteredBuild")
        let removed = AppPreferences.reset(d)
        #expect(Set(removed) == [AppPreferenceKey.refreshProfile, AppPreferenceKey.showMenuBarExtra])
        #expect(d.string(forKey: "traceHelperRegisteredBuild") == "12")
    }

    @Test("menu bar key name is unchanged so saved values survive")
    func legacyKey() {
        #expect(AppPreferenceKey.showMenuBarExtra == "showMenuBarExtra")
    }

    @Test("API transport resolver matches the client")
    func transportResolver() {
        let missing = ["NDEV_PRESSURE_API_SOCKET": "/absent-settings-test.sock", "HOME": "/tmp"]
        #expect(NDevPressureAPIClient.transport(environment: missing) == .cliFallback(reason: "no API socket at /absent-settings-test.sock"))
        #expect(NDevPressureAPIClient(environment: missing) == nil)
        let url = ["NDEV_PRESSURE_API_URL": "http://127.0.0.1:9"]
        #expect(NDevPressureAPIClient.transport(environment: url) == .url("http://127.0.0.1:9"))
        #expect(NDevPressureAPIClient(environment: url) != nil)
        let bad = ["NDEV_PRESSURE_API_URL": "http://example.com"]
        if case .cliFallback = NDevPressureAPIClient.transport(environment: bad) {} else { Issue.record("expected CLI fallback") }
        #expect(NDevPressureAPIClient(environment: bad) == nil)
    }
}

@Suite("Refresh profiles")
struct RefreshProfileTests {
    @Test("adaptive equals the historical cadence")
    func adaptiveUnchanged() {
        #expect(PressureFormat.pollInterval(for: .critical, profile: .adaptive) == 5)
        #expect(PressureFormat.pollInterval(for: .red, profile: .adaptive) == 8)
        #expect(PressureFormat.pollInterval(for: .warning, profile: .adaptive) == 12)
        #expect(PressureFormat.pollInterval(for: .normal, profile: .adaptive) == 20)
        #expect(PressureFormat.workFocusPollInterval(queueDepth: 1, leaseCount: 0, interfaceActive: true, profile: .adaptive) == 2.5)
    }

    @Test("slower profiles scale and stay bounded", arguments: RefreshProfile.allCases)
    func scaled(profile: RefreshProfile) {
        for level in PressureLevel.allCases {
            let interval = PressureFormat.pollInterval(for: level, profile: profile)
            #expect(interval == min(PressureFormat.pollInterval(for: level) * profile.multiplier, RefreshProfile.maximumInterval))
            #expect(interval <= RefreshProfile.maximumInterval)
        }
        #expect(profile.scaled(PressureFormat.menuBarOnlyPollInterval) <= RefreshProfile.maximumInterval)
        // Suspended work-focus checks are not scaled: they only wake to re-check visibility.
        #expect(PressureFormat.workFocusPollInterval(queueDepth: 1, leaseCount: 1, interfaceActive: false, profile: profile) == 30)
        #expect(PressureFormat.cadenceSummary(profile).contains("Menu bar only"))
    }
}

@Suite("Level alert gate")
struct LevelAlertGateTests {
    let t0 = Date(timeIntervalSince1970: 1_800_000_000)

    @Test("first sample never alerts, even at critical")
    func firstSampleSilent() {
        var gate = LevelAlertGate()
        #expect(gate.observe(.critical, threshold: .red, at: t0) == nil)
    }

    @Test("rising into the threshold alerts once; staying or falling does not")
    func risingOnly() {
        var gate = LevelAlertGate()
        _ = gate.observe(.normal, threshold: .red, at: t0)
        #expect(gate.observe(.warning, threshold: .red, at: t0) == nil)
        #expect(gate.observe(.red, threshold: .red, at: t0) == .red)
        #expect(gate.observe(.red, threshold: .red, at: t0.addingTimeInterval(30)) == nil)
        #expect(gate.observe(.critical, threshold: .red, at: t0.addingTimeInterval(60)) == .critical)
        #expect(gate.observe(.red, threshold: .red, at: t0.addingTimeInterval(90)) == nil)
    }

    @Test("cooldown suppresses flapping per level")
    func cooldown() {
        var gate = LevelAlertGate(cooldown: 600)
        _ = gate.observe(.normal, threshold: .red, at: t0)
        #expect(gate.observe(.red, threshold: .red, at: t0) == .red)
        _ = gate.observe(.normal, threshold: .red, at: t0.addingTimeInterval(60))
        #expect(gate.observe(.red, threshold: .red, at: t0.addingTimeInterval(120)) == nil)
        _ = gate.observe(.normal, threshold: .red, at: t0.addingTimeInterval(700))
        #expect(gate.observe(.red, threshold: .red, at: t0.addingTimeInterval(720)) == .red)
    }

    @Test("unknown is missing evidence and does not re-arm")
    func unknownIgnored() {
        var gate = LevelAlertGate(cooldown: 0)
        _ = gate.observe(.red, threshold: .red, at: t0)
        #expect(gate.observe(.unknown, threshold: .red, at: t0) == nil)
        #expect(gate.observe(.red, threshold: .red, at: t0) == nil)
    }

    @Test("off and critical-only thresholds")
    func thresholds() {
        var off = LevelAlertGate()
        _ = off.observe(.normal, threshold: .off, at: t0)
        #expect(off.observe(.critical, threshold: .off, at: t0) == nil)
        var crit = LevelAlertGate()
        _ = crit.observe(.normal, threshold: .critical, at: t0)
        #expect(crit.observe(.red, threshold: .critical, at: t0) == nil)
        #expect(crit.observe(.critical, threshold: .critical, at: t0) == .critical)
    }
}

@Suite("Settings launch pane")
@MainActor
struct LaunchPaneTests {
    @Test("launch pane choice, last used, and hidden fallback")
    func launchPane() {
        let d = isolatedDefaults()
        #expect(PressureStore.launchSection(d) == .overview)
        d.set(PressureStore.Section.work.rawValue, forKey: AppPreferenceKey.launchPane)
        #expect(PressureStore.launchSection(d) == .work)
        d.set("lastUsed", forKey: AppPreferenceKey.launchPane)
        d.set(PressureStore.Section.telemetry.rawValue, forKey: AppPreferenceKey.lastPane)
        #expect(PressureStore.launchSection(d) == .telemetry)
        d.set(PressureStore.Section.hetzner.rawValue, forKey: AppPreferenceKey.lastPane)
        d.set(false, forKey: AppPreferenceKey.showHetzner)
        #expect(PressureStore.launchSection(d) == .overview)
        d.set("not-a-pane", forKey: AppPreferenceKey.launchPane)
        #expect(PressureStore.launchSection(d) == .overview)
    }

    @Test("dock badge level text")
    func dockBadge() {
        #expect(DockBadgeMode.levelBadge(.normal) == nil)
        #expect(DockBadgeMode.levelBadge(.warning) == "!")
        #expect(DockBadgeMode.levelBadge(.red) == "RED")
        #expect(DockBadgeMode.levelBadge(.critical) == "CRIT")
    }
}
