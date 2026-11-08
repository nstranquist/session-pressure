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

    @Test("action controls fail closed without a live process identity")
    func actionBoundaries() throws {
        let url = try #require(Bundle.module.url(forResource: "process-incidents", withExtension: "json", subdirectory: "Fixtures"))
        let data = try Data(contentsOf: url)
        let envelope = try #require(JSONSerialization.jsonObject(with: data) as? [String: Any])
        let report = try #require(envelope["report"] as? [String: Any])
        let rows = try #require(report["incidents"] as? [[String: Any]])
        let original = try #require(rows.first)
        func decode(_ fields: [String: Any]) throws -> ProcessIncident {
            var row = original
            fields.forEach { row[$0] = $1 }
            return try PressureJSON.decode(ProcessIncident.self, from: JSONSerialization.data(withJSONObject: row))
        }
        let active = try decode(["source": "resident", "state": "active", "start_identity": "darwin:1:2"])
        #expect(active.canStop)
        #expect(!active.canForceStop)
        let pending = try decode(["source": "resident", "state": "stop_requested", "start_identity": "darwin:1:2"])
        #expect(pending.canStop && pending.canForceStop)
        let invalidCases: [[String: Any]] = [
            ["state": "active", "start_identity": "darwin:1:2"], // operator history remains closed to actions
            ["source": "resident", "state": "active", "start_identity": ""],
            ["source": "resident", "state": "active", "start_identity": "darwin:1:2", "pid": 1],
            ["source": "resident", "state": "recovered", "start_identity": "darwin:1:2"]
        ]
        for fields in invalidCases {
            let incident = try decode(fields)
            #expect(!incident.canStop && !incident.canForceStop)
        }
    }

    @Test("legacy rejected actions display No signal")
    func noSignalLabel() throws {
        let action = try PressureJSON.decode(ProcessIncidentAction.self, from: Data(#"{"at":"2026-11-08T17:00:00Z","signal":"","result":"revalidation_rejected"}"#.utf8))
        #expect(action.signalLabel == "No signal")
    }
}
