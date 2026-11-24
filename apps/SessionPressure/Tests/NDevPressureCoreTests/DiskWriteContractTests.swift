import Foundation
import Testing
@testable import NDevPressureCore

/// The Disk Writes reads decode from real CLI output, not from JSON written
/// by hand (Fixtures/io.md).
@Suite("Recorded disk-write contract")
struct DiskWriteContractTests {
    @Test("io status --live --full decodes with summary, writers and policy")
    func status() throws {
        let status = try SceneFixtures.decode(DiskWriteStatusEnvelope.self, "io-status")
        let summary = try #require(status.report?.summary)
        let writers = try #require(status.report?.writers)
        #expect(summary.state == .learning)
        #expect(summary.topWriter?.executable == "scp")
        #expect(writers.count == 5)
        #expect(writers.first?.pid != nil)
        #expect(status.diskWritePolicy?.profile == "quiet-adaptive-v1")
    }

    @Test("writers carry full program names, not the kernel's 16-character cut")
    func fullWriterNames() throws {
        let writers = try #require(try SceneFixtures.decode(DiskWriteStatusEnvelope.self, "io-status").report?.writers)
        #expect(writers.contains { $0.executable == "Google_Chrome_Helper_(Aperitif)" })
        #expect(!writers.contains { $0.executable == "Google_Chrome_He" })
    }

    @Test("io history decodes every recorded hour")
    func history() throws {
        let history = try SceneFixtures.decode(DiskWriteHistoryEnvelope.self, "io-history")
        #expect(history.history.count == history.returnedCount)
        #expect(history.history.count > 1)
        #expect(history.history.allSatisfy { $0.bytesWritten > 0 })
    }

    @Test("io health decodes the SMART snapshot")
    func health() throws {
        let health = try SceneFixtures.decode(DiskHealthEnvelope.self, "io-health")
        #expect(health.snapshot.available)
        #expect(health.snapshot.percentageUsed == 2)
        #expect(health.snapshot.hostBytesWritten > 0)
        #expect(health.snapshot.mediaErrors == 0)
    }

    @Test("the public recording keeps the data dir and SSD serial scrubbed")
    func healthIsScrubbed() throws {
        let root = try #require(JSONSerialization.jsonObject(with: SceneFixtures.data("io-health")) as? [String: Any])
        let snapshot = try #require(root["snapshot"] as? [String: Any])
        #expect(root["data_dir"] as? String == "/redacted")
        #expect(snapshot["serial"] as? String == "redacted")
    }
}
