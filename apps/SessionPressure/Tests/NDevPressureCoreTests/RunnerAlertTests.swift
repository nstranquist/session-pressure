import Foundation
import AppKit
import SwiftUI
import Testing
@testable import NDevPressure
@testable import NDevPressureCore

@Suite("Runner alert triage and gate")
struct RunnerAlertTriageTests {
    @Test("only warning and critical alerts that hold now are live")
    func triage() throws {
        let env = try SceneFixtures.runnerCapacity(alerts: [
            SceneFixtures.runnerAlert("info", "underused"),
            SceneFixtures.runnerAlert("critical", "disk_high"),
            SceneFixtures.runnerAlert("warning", "cpu_spike", since: "2026-11-15T08:00:00Z", until: "2026-11-15T08:10:00Z"),
            SceneFixtures.runnerAlert("critical", "memory_pressure", since: "2026-11-15T12:40:00Z", until: "2026-11-15T12:50:00Z"),
            SceneFixtures.runnerAlert("critical", "host_unreachable", host: "runner-1"),
        ])
        let live = RunnerAlertTriage.live(env.capacity)
        #expect(live.map(\.code) == ["host_unreachable", "disk_high", "memory_pressure"])
        #expect(live.allSatisfy { $0.isCritical })
        #expect(live[1].id == "runner/runner-3/disk_high/critical")
        #expect(live[1].capacityAlertID == "runner-3/disk_high/disk_high")
    }

    @Test("launch is a baseline; new alerts fire once; severity and cooldown apply")
    func gate() {
        func live(_ code: String, _ severity: String) -> LiveRunnerAlert {
            LiveRunnerAlert(id: "runner/h/\(code)/\(severity)", capacityAlertID: "h/\(code)/\(code)", host: "h", code: code,
                            severity: severity, title: code, detail: "", since: nil, until: nil)
        }
        var gate = RunnerAlertGate(cooldown: 1800)
        let t0 = Date(timeIntervalSince1970: 1_000_000)
        let disk = live("disk_high", "critical")
        let cpu = live("cpu_spike", "warning")
        #expect(gate.observe([disk], notify: .critical, at: t0).isEmpty)
        #expect(gate.observe([disk, cpu], notify: .critical, at: t0 + 300).isEmpty)
        let mem = live("memory_pressure", "critical")
        #expect(gate.observe([disk, cpu, mem], notify: .critical, at: t0 + 600) == [mem])
        #expect(gate.observe([disk, cpu, mem], notify: .critical, at: t0 + 900).isEmpty)
        // Ends, then flaps back inside the cooldown: no repeat.
        _ = gate.observe([disk], notify: .critical, at: t0 + 1200)
        #expect(gate.observe([disk, mem], notify: .critical, at: t0 + 1500).isEmpty)
        // After the cooldown it is news again.
        _ = gate.observe([disk], notify: .critical, at: t0 + 2500)
        #expect(gate.observe([disk, mem], notify: .critical, at: t0 + 2800) == [mem])
        // Warning setting includes warnings; off never fires.
        var warn = RunnerAlertGate()
        _ = warn.observe([], notify: .warning, at: t0)
        #expect(warn.observe([cpu], notify: .warning, at: t0 + 300) == [cpu])
        var off = RunnerAlertGate()
        _ = off.observe([], notify: .off, at: t0)
        #expect(off.observe([disk], notify: .off, at: t0 + 300).isEmpty)
    }
}

private actor WatchProbe {
    var calls: [RunnerCapacityWindow] = []
    var envelope: RunnerCapacityEnvelope
    var fail = false
    init(_ envelope: RunnerCapacityEnvelope) { self.envelope = envelope }
    func set(_ e: RunnerCapacityEnvelope) { envelope = e }
    func setFail(_ v: Bool) { fail = v }
    func read(_ w: RunnerCapacityWindow) throws -> RunnerCapacityEnvelope {
        calls.append(w)
        if fail { throw URLError(.timedOut) }
        return envelope
    }
}

@Suite("Runner alert watch")
@MainActor
struct RunnerAlertWatchTests {
    @Test("fires only for alerts that start after the first check")
    func firesOnNews() async throws {
        let quiet = try SceneFixtures.runnerCapacity(alerts: [SceneFixtures.runnerAlert("critical", "disk_high")])
        let loud = try SceneFixtures.runnerCapacity(alerts: [SceneFixtures.runnerAlert("critical", "disk_high"), SceneFixtures.runnerAlert("critical", "memory_low_now")])
        let probe = WatchProbe(quiet)
        let watch = RunnerAlertWatch(fetch: { try await probe.read($0) }, enabled: { true }, notify: { .critical })
        var fired: [String] = []
        watch.onFire = { fired += $0.map(\.code) }
        await watch.check()
        #expect(watch.live.map(\.code) == ["disk_high"])
        #expect(fired.isEmpty)
        await probe.set(loud)
        await watch.check()
        #expect(fired == ["memory_low_now"])
        #expect(await probe.calls == [.sixHours, .sixHours])
    }

    @Test("failures keep live alerts and surface after three in a row")
    func failures() async throws {
        let probe = WatchProbe(try SceneFixtures.runnerCapacity(alerts: [SceneFixtures.runnerAlert("warning", "load_high")]))
        let watch = RunnerAlertWatch(fetch: { try await probe.read($0) }, enabled: { true }, notify: { .critical })
        await watch.check()
        await probe.setFail(true)
        await watch.check()
        await watch.check()
        #expect(!watch.checkFailing)
        await watch.check()
        #expect(watch.checkFailing)
        #expect(watch.live.map(\.code) == ["load_high"])
        #expect(watch.lastError != nil)
        await probe.setFail(false)
        await watch.check()
        #expect(!watch.checkFailing)
        #expect(watch.lastError == nil)
    }

    @Test("a tab read of the same window counts; other windows and disabled do not")
    func ingestAndGating() async throws {
        let probe = WatchProbe(try SceneFixtures.runnerCapacity(alerts: []))
        var on = true
        let watch = RunnerAlertWatch(fetch: { try await probe.read($0) }, enabled: { on }, notify: { .critical })
        watch.ingest(try SceneFixtures.runnerCapacity(window: "24h", alerts: [SceneFixtures.runnerAlert("critical", "disk_high")]).capacity)
        #expect(watch.lastCheckAt == nil)
        watch.ingest(try SceneFixtures.runnerCapacity(alerts: [SceneFixtures.runnerAlert("critical", "disk_high")]).capacity)
        #expect(watch.live.map(\.code) == ["disk_high"])
        #expect(watch.lastCheckAt != nil)
        watch.shouldSkip = { true }
        await watch.check()
        #expect(watch.skippedForPressure)
        on = false
        watch.shouldSkip = { false }
        await watch.check()
        watch.preferencesChanged()
        #expect(watch.live.isEmpty)
        #expect(!watch.isRunning)
        #expect(await probe.calls.isEmpty)
    }

    @Test("live runner alerts reach the bell; an ended episode's dismissal is pruned")
    func bell() async throws {
        let probe = WatchProbe(try SceneFixtures.runnerCapacity(alerts: [SceneFixtures.runnerAlert("critical", "disk_high")]))
        let watch = RunnerAlertWatch(fetch: { try await probe.read($0) }, enabled: { true }, notify: { .off })
        let store = PressureStore(runnerWatch: watch)
        await watch.check()
        let item = try #require(store.notifications.first { $0.id == "runner/runner-3/disk_high/critical" })
        #expect(item.severity == .critical)
        #expect(item.runnerAlert?.host == "runner-3")
        store.dismissNotification(id: item.id)
        #expect(!store.notifications.contains { $0.id == item.id })
        await probe.set(try SceneFixtures.runnerCapacity(alerts: []))
        await watch.check()
        // Let the store's observation hop run.
        for _ in 0..<5 { await Task.yield() }
        #expect(!store.dismissedNotificationIDs.contains(item.id))
        await probe.set(try SceneFixtures.runnerCapacity(alerts: [SceneFixtures.runnerAlert("critical", "disk_high")]))
        await watch.check()
        #expect(store.notifications.contains { $0.id == item.id })
    }
}
