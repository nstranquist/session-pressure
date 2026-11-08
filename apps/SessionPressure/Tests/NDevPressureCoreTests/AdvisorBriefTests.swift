import Foundation
import Testing
@testable import NDevPressureCore

@Suite("Advisor brief contract")
struct AdvisorBriefTests {
    // Scrubbed recording of `ndev --json advisor brief show`.
    private var fixture: String {
        get throws {
            let url = Bundle.module.url(forResource: "advisor-brief", withExtension: "json", subdirectory: "Fixtures")!
            return try String(contentsOf: url, encoding: .utf8)
        }
    }

    @Test("residue badges preserve critical host state")
    func residueBadge() throws {
        var brief = try PressureJSON.decode(AdvisorBrief.self, from: Data(try fixture.utf8))
        // Exercise badge states independently of the recorded empty-residue day.
        brief.lanes.residue = [AdvisorBriefItem(id: "one"), AdvisorBriefItem(id: "two")]
        #expect(brief.residueCount == 2)
        #expect(brief.dockBadge(for: .normal) == "R2")
        #expect(brief.dockBadge(for: .red) == "RED·R2")
        #expect(brief.dockBadge(for: .critical) == "CRIT·R2")
        #expect(AdvisorBrief(day: "2026-11-08").dockBadge(for: .normal) == nil)
    }

    @Test("advisor uses the full control plane without pressure argv pairing")
    func separateAdvisorRouting() async throws {
        try await withClient(response: try fixture) { client, pressureMarker in
            let brief = try await client.advisorBrief()
            #expect(brief.day == "2026-11-08")
            #expect(!FileManager.default.fileExists(atPath: pressureMarker.path))
        }
    }

    @Test("unsupported brief schemas are rejected")
    func rejectsUnknownSchema() async throws {
        try await withClient(response: #"{"schema":"future","day":"2026-11-08"}"#) { client, _ in
            do {
                _ = try await client.advisorBrief()
                Issue.record("unsupported schema accepted")
            } catch NDevPressureClientError.malformedJSON { }
        }
    }

    @Test("brief output is bounded to eight KiB")
    func rejectsOversizedBrief() async throws {
        try await withClient(response: String(repeating: "x", count: 9000)) { client, _ in
            do {
                _ = try await client.advisorBrief()
                Issue.record("oversized output accepted")
            } catch NDevPressureClientError.responseTooLarge { }
        }
    }

    private func withClient(
        response: String,
        body: (NDevPressureClient, URL) async throws -> Void
    ) async throws {
        let directory = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
        try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
        defer { try? FileManager.default.removeItem(at: directory) }
        let pressure = directory.appendingPathComponent("session-pressure")
        let advisor = directory.appendingPathComponent("ndev")
        let marker = directory.appendingPathComponent("pressure-called")
        try "#!/bin/sh\ntouch '\(marker.path)'\nexit 91\n".write(to: pressure, atomically: true, encoding: .utf8)
        let payload = directory.appendingPathComponent("brief.json")
        try response.write(to: payload, atomically: true, encoding: .utf8)
        try "#!/bin/sh\n[ \"$*\" = '--json advisor brief show' ] || exit 92\n/bin/cat '\(payload.path)'\n".write(to: advisor, atomically: true, encoding: .utf8)
        for executable in [pressure, advisor] {
            try FileManager.default.setAttributes([.posixPermissions: 0o700], ofItemAtPath: executable.path)
        }
        let client = try NDevPressureClient(binaryPath: pressure.path, advisorBinaryPath: advisor.path, environment: [:])
        try await body(client, marker)
    }
}
