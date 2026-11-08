import Foundation
import Testing
@testable import NDevPressureCore

/// Captured by the real Go CLI from the operator receipt in docs/reviews.
@Suite("Process incident CLI contract")
struct ProcessIncidentContractTests {
    @Test("recorded CLI history decodes and cannot authorize a historical stop")
    func recordedHistory() throws {
        let url = try #require(Bundle.module.url(forResource: "process-incidents", withExtension: "json", subdirectory: "Fixtures"))
        let envelope = try PressureJSON.decode(ProcessIncidentEnvelope.self, from: Data(contentsOf: url))
        #expect(envelope.ok)
        #expect(envelope.report.schemaVersion == 1)
        let incident = try #require(envelope.report.incidents.first)
        #expect(incident.pid == 8523)
        #expect(incident.ppid == 8475)
        #expect(incident.executable == "python3")
        #expect(incident.source == "operator_record")
        #expect(incident.state == "recorded_closed")
        #expect(!incident.canStop)
        #expect(!incident.canForceStop)
        #expect(incident.startIdentity.isEmpty)
        #expect(incident.firstSeen.timeIntervalSince1970 > 0)
        #expect(incident.actions?.first?.signal == "TERM")
        #expect(incident.actions?.first?.result == "operator_reported_exit")
    }

    @Test("installed CLI list decodes through the native client")
    func installedHistory() async throws {
        guard let binary = ProcessInfo.processInfo.environment["PRESSURE_PROCESS_CONTRACT_BIN"] else { return }
        let client = try NDevPressureClient(binaryPath: binary)
        let result = try await client.processIncidents()
        #expect(result.ok)
        #expect(result.report.schemaVersion == 1)
    }
}
