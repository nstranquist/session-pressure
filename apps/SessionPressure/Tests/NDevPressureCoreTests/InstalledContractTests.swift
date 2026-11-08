import Foundation
import Testing
@testable import NDevPressureCore

@Suite("Installed product integration")
struct InstalledContractTests {
    @Test("real CLI identity and SMART decode; Unix API agrees with CLI")
    func installedContract() async throws {
        guard let binary = ProcessInfo.processInfo.environment["PRESSURE_CONTRACT_BIN"] else { return }
        let cli = try NDevPressureClient(binaryPath: binary, environment: ["NDEV_PRESSURE_API_SOCKET": "/absent-pressure-test.sock"])
        let identity = try await cli.capabilities()
        #expect(identity.ok)
        #expect(identity.capabilities.sourceRevision?.count == 40)
        #expect(identity.capabilities.operations.contains("status"))
        let health = try await cli.diskHealth()
        #expect(health.ok)
        #expect(health.snapshot.capturedAt.timeIntervalSince1970 > 0)

        let state = FileManager.default.temporaryDirectory.appendingPathComponent("pressure-contract-\(UUID().uuidString)")
        try FileManager.default.createDirectory(at: state, withIntermediateDirectories: true)
        let socket = "/tmp/pressure-\(UUID().uuidString.prefix(8)).sock"
        let server = Process()
        server.executableURL = URL(fileURLWithPath: binary).deletingLastPathComponent().appendingPathComponent("session-pressure-api")
        server.arguments = ["serve", "--state-dir", state.path, "--socket", socket, "--ndev-bin", binary]
        server.standardOutput = FileHandle.nullDevice
        server.standardError = FileHandle.nullDevice
        try server.run()
        defer {
            if server.isRunning { server.terminate() }
            server.waitUntilExit()
            try? FileManager.default.removeItem(at: state)
            try? FileManager.default.removeItem(atPath: socket)
        }
        for _ in 0..<100 {
            if FileManager.default.fileExists(atPath: socket) { break }
            try await Task.sleep(for: .milliseconds(50))
        }
        let api = try #require(NDevPressureAPIClient(environment: ["NDEV_PRESSURE_API_SOCKET": socket]))
        let response = try await api.projection("/v1/pressure/capabilities", as: CapabilitiesEnvelope.self)
        #expect(response.capabilities.sourceRevision == identity.capabilities.sourceRevision)
        let status = try await api.projection("/v1/pressure/status", as: StatusEnvelope.self)
        #expect(status.snapshot != nil || status.snapshotSummary != nil || status.latestMonitor != nil || status.latestMonitorSummary != nil)
    }
}
