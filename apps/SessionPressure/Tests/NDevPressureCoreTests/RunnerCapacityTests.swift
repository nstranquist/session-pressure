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
    func setFailure(_ value: Bool) { fail = value }
    func read(_ window: RunnerCapacityWindow) throws -> RunnerCapacityEnvelope {
        calls.append(window)
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
        #expect(store.selected?.isActive == true)
        let reserve = try #require(store.report?.hosts.first { $0.state == "reserve" })
        store.selectedHost = reserve.host
        #expect(store.selected?.host == reserve.host)
        store.selectedHost = "retired-host"
        #expect(store.selected?.isActive == true)
    }

    @Test("inventory errors map to operator text")
    func inventoryError() {
        #expect(RunnerCapacityStore.describe("runner_fleet_inventory_missing").contains("fleet.json"))
        #expect(RunnerCapacityStore.describe("other_code") == "other_code")
    }
}

/// Offscreen evidence render: no window is created, so it never touches the
/// operator's desktop. Runs only when CAPACITY_SNAPSHOT_PATH is set;
/// CAPACITY_SNAPSHOT_FIXTURE selects another recorded payload.
@Suite("Runner capacity offscreen render")
@MainActor
struct RunnerCapacityRenderTests {
    @Test("renders the capacity card to PNG")
    func render() async throws {
        let env = ProcessInfo.processInfo.environment
        guard let out = env["CAPACITY_SNAPSHOT_PATH"] else { return }
        let envelope: RunnerCapacityEnvelope
        if let path = env["CAPACITY_SNAPSHOT_FIXTURE"] {
            envelope = try PressureJSON.decode(RunnerCapacityEnvelope.self, from: Data(contentsOf: URL(fileURLWithPath: path)))
        } else {
            envelope = try RunnerCapacityContractTests.recorded()
        }
        let window = RunnerCapacityWindow(rawValue: envelope.capacity.window) ?? .day
        let store = RunnerCapacityStore(fetch: { _ in envelope })
        await store.setWindow(window)
        await store.refresh()
        let card = RunnerCapacityCard(store: store)
            .frame(width: 1100)
            .padding(16)
            .background(Color(nsColor: .windowBackgroundColor))
        let renderer = ImageRenderer(content: card)
        renderer.scale = 2
        let image = try #require(renderer.nsImage)
        let tiff = try #require(image.tiffRepresentation)
        let png = try #require(NSBitmapImageRep(data: tiff)?.representation(using: .png, properties: [:]))
        try png.write(to: URL(fileURLWithPath: out))
    }
}
