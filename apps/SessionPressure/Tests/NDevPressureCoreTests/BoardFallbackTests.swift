import Foundation
import Testing
@testable import NDevPressureCore

/// The composite read degrades to the per-contract fan-out when the installed
/// `ndev` predates `session pressure board`. That path had unit coverage only on
/// the error-classification helper — the actual downgrade had never executed.
/// These drive a stub binary so the whole client path runs for real.
@Suite("Board fallback against a stub ndev")
struct BoardFallbackTests {
    /// Writes an executable stub that answers the pressure contracts this test
    /// needs, optionally rejecting `board` the way an older build does.
    private func stubNDev(supportsBoard: Bool) throws -> (client: NDevPressureClient, calls: URL) {
        let dir = URL(fileURLWithPath: NSTemporaryDirectory())
            .appendingPathComponent("ndev-board-stub-\(UUID().uuidString)")
        try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        let calls = dir.appendingPathComponent("calls.log")
        let binary = dir.appendingPathComponent("ndev")

        let boardArm = supportsBoard
            ? #"""
              echo "{\"ok\":true,\"action\":\"board\",\"work\":{\"capacity\":8,\"used\":1,\"available\":7,\"leases\":[],\"waiters\":[],\"queue_depth\":0},\"admission\":{\"allowed\":true,\"level\":\"normal\"},\"health\":{\"monitor_healthy\":true,\"protection_mode\":\"full\"}}"
              exit 0
              """#
            : #"""
              echo "ndev session pressure: unknown subcommand \"board\"" >&2
              exit 2
              """#

        let script = #"""
        #!/bin/bash
        echo "$*" >> "__CALLS__"
        case "$*" in
          *"pressure board"*)
            __BOARD_ARM__
            ;;
          *"pressure status"*)
            echo '{"action":"status","has_latest_monitor":true,"health":{"monitor_healthy":true,"protection_mode":"full"},"latest_monitor_summary":{"free_percent":31,"host_cpu_percent":42.0}}'
            ;;
          *"pressure work status"*)
            echo '{"action":"work.status","work":{"capacity":8,"used":1,"available":7,"leases":[],"waiters":[],"queue_depth":0}}'
            ;;
          *"pressure check"*)
            echo '{"action":"check","admission":{"allowed":true,"level":"normal"}}'
            ;;
          *)
            echo '{}'
            ;;
        esac
        exit 0
        """#
            .replacingOccurrences(of: "__CALLS__", with: calls.path)
            .replacingOccurrences(of: "__BOARD_ARM__", with: boardArm)

        try script.write(to: binary, atomically: true, encoding: .utf8)
        try FileManager.default.setAttributes([.posixPermissions: 0o755], ofItemAtPath: binary.path)
        return (try NDevPressureClient(binaryPath: binary.path), calls)
    }

    private func invocations(_ calls: URL) -> [String] {
        (try? String(contentsOf: calls, encoding: .utf8))?
            .split(separator: "\n").map(String.init) ?? []
    }

    @Test("a board-capable ndev is read with exactly one process")
    func compositeReadIssuesOneCall() async throws {
        let stub = try stubNDev(supportsBoard: true)
        let board = try await stub.client.refreshBoard(
            live: false, includeIdle: false, includeTelemetry: false, fullStatus: false,
            includePolicy: false, includeMonitor: false, includeDoctor: false,
            includeCalibration: false, previous: nil
        )
        #expect(board.work?.capacity == 8)
        #expect(board.admission?.allowed == true)

        let calls = invocations(stub.calls)
        #expect(calls.count == 1, "composite read should be a single process, got: \(calls)")
        #expect(calls.first?.contains("pressure board") == true)
    }

    @Test("an ndev without the board verb falls back to the per-contract fan-out")
    func fallbackFanOutProducesTheSameBoard() async throws {
        let stub = try stubNDev(supportsBoard: false)
        let board = try await stub.client.refreshBoard(
            live: false, includeIdle: false, includeTelemetry: false, fullStatus: false,
            includePolicy: false, includeMonitor: false, includeDoctor: false,
            includeCalibration: false, previous: nil
        )
        // Degraded transport, identical display state.
        #expect(board.work?.capacity == 8)
        #expect(board.admission?.allowed == true)
        #expect(board.health?.monitorHealthy == true)
        #expect(board.snapshot.freePercent == 31)

        let calls = invocations(stub.calls)
        #expect(calls.contains { $0.contains("pressure board") }, "should have attempted board first")
        #expect(calls.contains { $0.contains("work status") }, "should have fallen back to work status")
        #expect(calls.contains { $0.contains("pressure check") }, "should have fallen back to check")
        #expect(calls.count > 1, "fallback must issue the fan-out, got: \(calls)")
    }
}
