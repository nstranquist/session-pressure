import Foundation
import Testing
@testable import NDevPressureCore

/// Contract tests against a **real recorded** `ndev --json session pressure board
/// --full --live --include all` payload, scrubbed of host paths and session ids.
///
/// Hand-written fixtures failed twice while building this surface: once by
/// typing `coverage.limitations` as a list when the compact projection emits a
/// count, and once by inventing a `name` key on a coverage surface that actually
/// carries `id`. Both passed review and both would have thrown at runtime. A
/// recording cannot drift from the CLI the way an imagined shape can.
///
/// Re-record with:
///   ndev --json session pressure board --full --live --include all
/// then re-run the scrubber (see the fixture header in git history).
@Suite("Board contract against recorded CLI output")
struct BoardContractTests {
    private func recordedBoard() throws -> Data {
        let url = try #require(
            Bundle.module.url(forResource: "board-full", withExtension: "json", subdirectory: "Fixtures")
                ?? Bundle.module.url(forResource: "board-full", withExtension: "json"),
            "board-full.json fixture is missing from the test bundle"
        )
        return try Data(contentsOf: url)
    }

    @Test("every board section decodes from real CLI output")
    func decodesRecordedBoard() throws {
        let env = try PressureJSON.decode(BoardEnvelope.self, from: recordedBoard())

        #expect(env.action == "board")
        #expect(env.outputScope == "full")

        // Always-on sections.
        let work = try #require(env.work, "work section")
        #expect(work.capacity > 0)
        #expect(work.used >= 0)
        #expect(work.queueDepth == work.waiters.count)
        #expect(env.admission != nil)
        let health = try #require(env.health, "health section")
        #expect(!health.protectionMode.isEmpty)

        // Shapes that previously diverged from my assumptions.
        let coverage = try #require(env.coverage, "coverage section")
        #expect(!coverage.status.isEmpty)
        #expect(!coverage.surfaces.isEmpty, "coverage surfaces should decode, not silently empty")
        #expect(env.effectiveLaunchd != nil)

        // Opt-in sections requested via --include all.
        #expect(env.policy != nil)
        #expect(env.doctor != nil)
        #expect(env.calibration != nil)
        #expect(env.inventory != nil)
        #expect(env.idleSource == "live" || env.idleSource == "resident")
        #expect(env.events != nil)
        #expect(env.actions != nil)

        // --live must carry a real snapshot, not only the resident one.
        let snapshot = try #require(env.effectiveSnapshot, "snapshot section")
        #expect(snapshot.physicalMemoryMB > 0)
    }

    /// The whole point of the composite read is that the app can build its board
    /// from it without a second process.
    @Test("recorded board folds into a complete display board")
    func foldsRecordedBoardIntoDisplayState() throws {
        let env = try PressureJSON.decode(BoardEnvelope.self, from: recordedBoard())
        let board = PressureBoard(composite: env, previous: nil, binaryPath: "/usr/bin/ndev", live: true)

        #expect(board.work != nil)
        #expect(board.admission != nil)
        #expect(board.health != nil)
        #expect(board.coverage != nil)
        #expect(board.policy != nil)
        #expect(board.launchd != nil)
        #expect(board.doctor != nil)
        #expect(board.calibration != nil)
        #expect(board.liveSample == true)
        #expect(board.binaryPath == "/usr/bin/ndev")
        #expect(board.snapshot.physicalMemoryMB > 0)
        // Sidebar summary reads these directly; zeros here render a dead UI.
        #expect(board.snapshot.freePercent > 0)
    }

    /// Decoding must never be all-or-nothing on optional detail: a board whose
    /// opt-in sections were not requested is still a usable board.
    @Test("minimal board decodes without any opt-in section")
    func decodesMinimalBoard() throws {
        let minimal = Data("""
        {"ok": true, "action": "board", "output_scope": "compact",
         "work": {"capacity": 8, "used": 0, "available": 8,
                  "leases": [], "waiters": [], "queue_depth": 0},
         "admission": {"allowed": true, "level": "normal", "source": "resident"}}
        """.utf8)
        let env = try PressureJSON.decode(BoardEnvelope.self, from: minimal)
        #expect(env.work?.capacity == 8)
        #expect(env.doctor == nil)
        #expect(env.coverage == nil)
        #expect(env.inventory == nil)

        // Folding a partial read must reuse prior detail, not blank the panes.
        var previous = PressureBoard()
        previous.calibration = WorkCalibration(operationCount: 11)
        previous.doctor = nil
        let board = PressureBoard(composite: env, previous: previous, binaryPath: "/x", live: false)
        #expect(board.calibration?.operationCount == 11)
        #expect(board.work?.capacity == 8)
    }
}
