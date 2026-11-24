import Foundation
import AppKit
import SwiftUI
import Testing
@testable import NDevPressure
@testable import NDevPressureCore

@Suite("Runner capacity CLI contract")
struct RunnerCapacityContractTests {
    static func recorded() throws -> RunnerCapacityEnvelope {
        let url = try #require(Bundle.module.url(forResource: "runner-capacity", withExtension: "json", subdirectory: "Fixtures"))
        return try PressureJSON.decode(RunnerCapacityEnvelope.self, from: Data(contentsOf: url))
    }

    /// The recorded payload with one host's read failed, as the CLI reports it.
    static func recorded(unreachable host: String) throws -> RunnerCapacityEnvelope {
        let url = try #require(Bundle.module.url(forResource: "runner-capacity", withExtension: "json", subdirectory: "Fixtures"))
        var root = try #require(JSONSerialization.jsonObject(with: Data(contentsOf: url)) as? [String: Any])
        var capacity = try #require(root["capacity"] as? [String: Any])
        var hosts = try #require(capacity["hosts"] as? [[String: Any]])
        for i in hosts.indices where hosts[i]["host"] as? String == host {
            // Mirrors Go's zero-valued HostCapacity for a failed read.
            var h = hosts[i].filter { ["host", "state", "provider", "platform", "heavy_slots", "light_slots", "usage"].contains($0.key) }
            for key in ["cpus", "uptime_seconds", "load1", "load5", "load15", "mem_total_bytes", "mem_available_bytes"] { h[key] = 0 }
            for key in ["buckets", "job_hours", "top_callers", "top_repos"] { h[key] = [Any]() }
            h["reachable"] = false
            h["error"] = "runner_capacity_timeout"
            h["samples_present"] = false
            hosts[i] = h
        }
        capacity["hosts"] = hosts
        root["capacity"] = capacity
        return try PressureJSON.decode(RunnerCapacityEnvelope.self, from: JSONSerialization.data(withJSONObject: root))
    }

    @Test("recorded fleet capacity decodes hosts, buckets, jobs, network and alerts")
    func recordedCapacity() throws {
        let envelope = try Self.recorded()
        let report = envelope.capacity
        #expect(envelope.ok)
        #expect(report.schemaVersion == 1)
        #expect(report.window == "24h")
        #expect(report.bucketSeconds == 900)
        #expect(report.error == nil)
        let active = try #require(report.hosts.first { $0.isActive })
        #expect(active.reachable)
        #expect(active.samplesPresent)
        #expect(!active.buckets.isEmpty)
        #expect(active.buckets.allSatisfy { $0.cpuAvgPct >= 0 && $0.cpuAvgPct <= 100 && $0.samples > 0 })
        #expect(active.usage.cpuAvgPct != nil)
        #expect(active.memTotalBytes > active.memAvailableBytes)
        #expect(active.disk != nil)
        let net = try #require(active.network)
        #expect(net.allowance.kind == "unmetered")
        #expect(net.rxBytesSinceBoot > 0)
        let reserve = try #require(report.hosts.first { $0.state == "reserve" })
        #expect(reserve.reachable)
        #expect(!reserve.samplesPresent)
        #expect(reserve.network?.allowance.kind == "monthly_included")
        #expect(reserve.network?.allowance.includedBytesPerMonth == 20_000_000_000_000)
        #expect(report.alerts.allSatisfy { ["critical", "warning", "info"].contains($0.severity) })
    }

    @Test("real CLI returns a typed capacity report")
    func realCLI() async throws {
        guard let binary = ProcessInfo.processInfo.environment["PRESSURE_GATE_RUNNER_CONTRACT_BIN"],
              ProcessInfo.processInfo.environment["PRESSURE_GATE_RUNNER_BOX"] == "1" else { return }
        let client = try NDevPressureClient(binaryPath: binary)
        let envelope = try await client.runnerCapacity(window: .sixHours)
        #expect(envelope.ok)
        #expect(envelope.capacity.schemaVersion == 1)
        #expect(envelope.capacity.window == "6h")
        #expect(!envelope.capacity.hosts.isEmpty)
    }

    @Test("byte and rate formatting use decimal units")
    func formatting() {
        #expect(CapacityFormat.bytes(233_921_541_219) == "233.9 GB")
        #expect(CapacityFormat.bytes(20_000_000_000_000) == "20.0 TB")
        #expect(CapacityFormat.rate(125_000_000) == "1.00 Gbit/s")
        #expect(CapacityFormat.duration(5400) == "1.5h")
    }
}

private actor CapacityProbe {
    var calls: [RunnerCapacityWindow] = []
    var fail = false
    var envelope: RunnerCapacityEnvelope
    init(_ envelope: RunnerCapacityEnvelope) { self.envelope = envelope }
    private var holding = false
    private var waiter: CheckedContinuation<Void, Never>?
    func setFailure(_ value: Bool) { fail = value }
    func setEnvelope(_ value: RunnerCapacityEnvelope) { envelope = value }
    func hold() { holding = true }
    func release() {
        holding = false
        waiter?.resume()
        waiter = nil
    }
    func read(_ window: RunnerCapacityWindow) async throws -> RunnerCapacityEnvelope {
        calls.append(window)
        if holding { await withCheckedContinuation { waiter = $0 } }
        if fail { throw URLError(.cannotConnectToHost) }
        return envelope
    }
}

@Suite("Runner capacity store")
@MainActor
struct RunnerCapacityStoreTests {
    @Test("a failed read keeps the last good report and reports the error")
    func keepsLastGood() async throws {
        let probe = CapacityProbe(try RunnerCapacityContractTests.recorded())
        let store = RunnerCapacityStore(fetch: { try await probe.read($0) })
        await store.refresh()
        #expect(store.report != nil)
        #expect(store.error == nil)
        let firstFetch = store.fetchedAt
        await probe.setFailure(true)
        await store.refresh()
        #expect(store.report != nil)
        #expect(store.error != nil)
        #expect(store.fetchedAt == firstFetch)
    }

    @Test("changing the window re-reads with the new window")
    func windowChange() async throws {
        let probe = CapacityProbe(try RunnerCapacityContractTests.recorded())
        let store = RunnerCapacityStore(fetch: { try await probe.read($0) })
        await store.refresh()
        await store.setWindow(.week)
        await store.setWindow(.week)
        #expect(await probe.calls == [.day, .week])
    }

    @Test("selection defaults to the active runner and follows explicit choice")
    func selection() async throws {
        let probe = CapacityProbe(try RunnerCapacityContractTests.recorded())
        let store = RunnerCapacityStore(fetch: { try await probe.read($0) })
        await store.refresh()
        #expect(store.selected?.capacity.isActive == true)
        let reserve = try #require(store.hosts.first { $0.capacity.state == "reserve" })
        store.selectedHost = reserve.id
        #expect(store.selected?.id == reserve.id)
        store.selectedHost = "retired-host"
        #expect(store.selected?.capacity.isActive == true)
    }

    @Test("one failed host read keeps that host's last good data, marked stale")
    func staleHostMerge() async throws {
        let good = try RunnerCapacityContractTests.recorded()
        let active = try #require(good.capacity.hosts.first { $0.isActive })
        let failed = try RunnerCapacityContractTests.recorded(unreachable: active.host)
        let probe = CapacityProbe(good)
        let store = RunnerCapacityStore(fetch: { try await probe.read($0) })
        await store.refresh()
        #expect(store.hosts.allSatisfy { !$0.isStale })
        await probe.setEnvelope(failed)
        await store.refresh()
        let shown = try #require(store.hosts.first { $0.id == active.host })
        #expect(shown.isStale)
        #expect(shown.capacity.reachable)
        #expect(!shown.capacity.buckets.isEmpty)
        #expect(shown.failure == "runner_capacity_timeout")
        #expect(store.hosts.filter { $0.id != active.host }.allSatisfy { !$0.isStale })
        await probe.setEnvelope(good)
        await store.refresh()
        #expect(store.hosts.allSatisfy { !$0.isStale })
    }

    @Test("a window change during a read is fetched after it, not dropped")
    func windowChangeMidRead() async throws {
        let probe = CapacityProbe(try RunnerCapacityContractTests.recorded())
        await probe.hold()
        let store = RunnerCapacityStore(fetch: { try await probe.read($0) })
        let first = Task { await store.refresh() }
        while await probe.calls.isEmpty { await Task.yield() }
        await store.setWindow(.week)
        #expect(store.loading)
        await probe.release()
        await first.value
        #expect(await probe.calls == [.day, .week])
        #expect(!store.loading)
        #expect(store.report != nil)
    }

    @Test("polling runs only while the pane is active and the card is visible")
    func visibilityGating() async throws {
        let probe = CapacityProbe(try RunnerCapacityContractTests.recorded())
        let store = RunnerCapacityStore(fetch: { try await probe.read($0) })
        store.setCardVisible(true)
        #expect(!store.polling)
        store.setPaneActive(true)
        #expect(store.polling)
        while store.fetchedAt == nil { await Task.yield() }
        store.setCardVisible(false)
        #expect(!store.polling)
        // Returning inside the cadence does not re-read.
        store.setCardVisible(true)
        try await Task.sleep(for: .milliseconds(50))
        #expect(await probe.calls == [.day])
        store.setPaneActive(false)
        #expect(!store.polling)
    }

    @Test("cadence follows the window's bucket size")
    func cadence() {
        #expect(RunnerCapacityStore.cadence(for: .sixHours) == .seconds(60))
        #expect(RunnerCapacityStore.cadence(for: .day) == .seconds(120))
        #expect(RunnerCapacityStore.cadence(for: .fortnight) == .seconds(300))
    }

    @Test("inventory errors map to operator text")
    func inventoryError() {
        #expect(RunnerCapacityStore.describe("runner_fleet_inventory_missing").contains("fleet.json"))
        #expect(RunnerCapacityStore.describe("runner_capacity_timeout").contains("timed out"))
        #expect(RunnerCapacityStore.describe("other_code") == "other_code")
    }
}
