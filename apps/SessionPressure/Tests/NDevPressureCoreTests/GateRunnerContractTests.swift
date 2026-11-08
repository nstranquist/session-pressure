import Foundation
import Testing
@testable import NDevPressureCore

@Suite("Gate-runner CLI contract")
struct GateRunnerContractTests {
    @Test("recorded output preserves comparison authority, dates and acronym fields")
    func recordedInspection() throws {
        let url = try #require(Bundle.module.url(forResource: "gate-runner", withExtension: "json", subdirectory: "Fixtures"))
        let envelope = try PressureJSON.decode(GateRunnerEnvelope.self, from: Data(contentsOf: url))
        let inspection = envelope.inspection
        #expect(envelope.ok)
        #expect(inspection.schemaVersion == 1)
        #expect(inspection.runs.count == 5)
        #expect(inspection.jobs.count == 3)
        #expect(inspection.runs.allSatisfy { $0.at != nil && !$0.deliveryID.isEmpty })
        #expect(inspection.runs.contains { $0.agreement == nil })
        #expect(inspection.runs.contains { $0.agreement == true })
        #expect(inspection.runs.contains { $0.agreement == false })
        #expect(inspection.runs.contains { $0.localEnvironment != nil })
        #expect(inspection.runs.contains { $0.remoteRunMS != nil && $0.remoteWallMS != nil })
        #expect(inspection.ledgerSignature.count == 64)
        #expect(inspection.boxReachable)
        #expect(inspection.health?.memTotalMB != nil)
        #expect(inspection.health?.diskUsedPercent != nil)
        #expect(inspection.jobs.allSatisfy { $0.at != nil && $0.durationSeconds != nil })
    }

    @Test("real CLI returns a typed inspection and accepts its unchanged cursor")
    func realCLI() async throws {
        guard let binary = ProcessInfo.processInfo.environment["PRESSURE_GATE_RUNNER_CONTRACT_BIN"] else { return }
        let client = try NDevPressureClient(binaryPath: binary)
        let first = try await client.gateRunnerInspection(includeBox: false)
        #expect(first.ok)
        #expect(first.inspection.schemaVersion == 1)
        #expect(!first.inspection.boxRequested)
        if !first.inspection.ledgerSignature.isEmpty {
            let next = try await client.gateRunnerInspection(includeBox: false, knownLedger: first.inspection.ledgerSignature)
            #expect(next.ok)
            // A concurrent worker may append between reads. Equality alone
            // determines whether this particular response must be unchanged.
            if next.inspection.ledgerSignature == first.inspection.ledgerSignature {
                #expect(next.inspection.ledgerUnchanged)
                #expect(next.inspection.runs.isEmpty)
            }
        }
        guard ProcessInfo.processInfo.environment["PRESSURE_GATE_RUNNER_BOX"] == "1" else { return }
        let remote = try await client.gateRunnerInspection()
        #expect(remote.inspection.boxRequested)
        #expect(remote.inspection.boxReachable)
        #expect(remote.inspection.boxError == nil)
        #expect(remote.inspection.health != nil)
        // SETUP receipts can exist before the runner creates a job log.
        // Bind log verification to a completed receipt selected by the caller,
        // rather than assume the newest concurrent receipt has a log.
        if let jobID = ProcessInfo.processInfo.environment["PRESSURE_GATE_RUNNER_LOG_ID"] {
            let log = try await client.gateRunnerJobLog(id: jobID)
            #expect(log.utf8.count <= 100_000)
        }
    }
}
