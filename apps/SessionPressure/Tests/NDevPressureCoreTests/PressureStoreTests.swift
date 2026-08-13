import Foundation
import Testing
@testable import NDevPressure
@testable import NDevPressureCore

@Suite("Pressure store work lifecycle")
@MainActor
struct PressureStoreTests {
    @Test("focus polling is inactive when the app or window is inactive")
    func focusPollingRespectsVisibility() {
        let store = PressureStore()
        store.setApplicationActive(false)
        store.selectedSection = .work
        #expect(store.workFocusPollActive == false)
        #expect(store.workFocusPollInterval == 30)

        store.setApplicationActive(true)
        #expect(store.workFocusPollActive == true)
        #expect(store.workFocusPollInterval == 10)

        store.setWindowVisible(false)
        #expect(store.workFocusPollActive == false)
        #expect(store.workFocusPollInterval == 30)
        store.stop()
    }

    @Test("disk focus polling exists only while its pane is visible and active")
    func diskFocusPollingRespectsVisibility() {
        let store = PressureStore()
        store.setApplicationActive(false)
        store.selectedSection = .diskWrites
        #expect(store.diskFocusPollActive == false)

        store.setApplicationActive(true)
        #expect(store.diskFocusPollActive == true)

        store.setWindowVisible(false)
        #expect(store.diskFocusPollActive == false)

        store.setWindowVisible(true)
        #expect(store.diskFocusPollActive == true)
        store.selectedSection = .overview
        #expect(store.diskFocusPollActive == false)
        store.stop()
    }

    @Test("disk trace deep links require an in-app confirmation")
    func diskTraceDeepLinkRequiresConfirmation() throws {
        let store = PressureStore()
        let url = try #require(URL(string: "ndev-pressure://disk-writes/trace?pid=4242&start=darwin%3A1%3A2&duration=15s"))
        store.handleDeepLink(url)

        #expect(store.selectedSection == .diskWrites)
        #expect(store.pendingDiskTraceRequest?.pid == 4242)
        #expect(store.pendingDiskTraceRequest?.processStartIdentity == "darwin:1:2")
        #expect(store.pendingDiskTraceRequest?.durationSeconds == 15)
        #expect(store.isDiskTracing == false)

        store.pendingDiskTraceRequest = nil
        let invalid = try #require(URL(string: "ndev-pressure://disk-writes/trace?pid=4242&start=darwin%3A1%3A2&duration=31s"))
        store.handleDeepLink(invalid)
        #expect(store.pendingDiskTraceRequest == nil)
        store.stop()
    }

    @Test("missing waiter becomes read-only lifecycle history")
    func missingWaiterTransitionsToHistory() throws {
        let live = try decodeWork("""
        {
          "work": {
            "capacity": 8,
            "used": 0,
            "available": 8,
            "leases": [],
            "waiters": [{
              "operation_id": "00000000000000000000000000000001",
              "class": "test",
              "weight": 3,
              "pid": 101,
              "position": 1
            }],
            "queue_depth": 1
          }
        }
        """)
        let empty = try decodeWork("""
        {
          "work": {
            "capacity": 8,
            "used": 0,
            "available": 8,
            "leases": [],
            "waiters": [],
            "queue_depth": 0
          }
        }
        """)
        let waiter = try #require(live.work?.waiters.first)
        let store = PressureStore()
        store.board.work = live.work
        store.workSelection = .waiter(waiter)

        store.board.work = empty.work
        store.refreshWorkSelectionFromBoard()

        guard case .historical(let historical) = store.workSelection else {
            Issue.record("missing waiter remained live: \(String(describing: store.workSelection))")
            return
        }
        #expect(historical.operationID == waiter.operationID)
        #expect(historical.previousState == "queued waiter")
    }

    /// The pinned sequence is control-plane truth, so the UI must read it from
    /// the status envelope rather than tracking promotion state of its own.
    @Test("override sequence projects a head, a pinned tail, and unpinned rows")
    func overrideSequenceProjectsPinnedPositions() throws {
        let pinned = try decodeWork("""
        {
          "work": {
            "capacity": 8,
            "used": 0,
            "available": 8,
            "leases": [],
            "waiters": [
              {"operation_id": "00000000000000000000000000000001", "class": "test", "weight": 3,
               "pid": 101, "position": 1, "protected": true,
               "protection_reason": "priority_override", "override_position": 1},
              {"operation_id": "00000000000000000000000000000002", "class": "build", "weight": 5,
               "pid": 102, "position": 2, "protected": true,
               "protection_reason": "priority_override_queued", "override_position": 2},
              {"operation_id": "00000000000000000000000000000003", "class": "browser", "weight": 2,
               "pid": 103, "position": 3}
            ],
            "queue_depth": 3,
            "override_operation_id": "00000000000000000000000000000001",
            "override_queue": ["00000000000000000000000000000002"],
            "override_queue_depth": 2
          }
        }
        """)
        let work = try #require(pinned.work)
        #expect(work.overrideQueueDepth == 2)
        #expect(work.overrideQueue == ["00000000000000000000000000000002"])
        #expect(work.waiters[0].isOverrideQueued == false)
        #expect(work.waiters[1].isOverrideQueued == true)
        #expect(work.waiters[1].overridePosition == 2)
        #expect(work.waiters[2].isOverrideQueued == false)
        #expect(work.waiters[2].overridePosition == 0)
    }

    /// Run all must be disabled, not merely error, while the persisted state can
    /// only carry a single override head.
    @Test("override sequence support is gated on the persisted work-state schema")
    func overrideSequenceSupportFollowsSchema() throws {
        func work(_ schema: Int?) throws -> WorkStatus {
            let version = schema.map { "\"schema_version\": \($0)," } ?? ""
            return try #require(decodeWorkStatus("""
            {"work": {\(version) "capacity": 8, "used": 0, "available": 8,
                      "leases": [], "waiters": [], "queue_depth": 0}}
            """))
        }
        #expect(try work(6).supportsOverrideSequence == false)
        #expect(try work(7).supportsOverrideSequence == false)
        #expect(try work(8).supportsOverrideSequence == true)
        #expect(try work(9).supportsOverrideSequence == true)
        // Absent means a helper too old to report it; assume capable and let the
        // coordinator be the authority rather than disabling on a guess.
        #expect(try work(nil).supportsOverrideSequence == true)
    }

    /// A schema-7 helper reports only the single head. Reading depth zero there
    /// would render "0 pinned" while a promotion is live and leave Run all
    /// enabled against state it cannot see.
    @Test("single-slot override from an n-1 helper still reports one pinned")
    func legacyOverrideDerivesQueueDepth() throws {
        let legacy = try decodeWork("""
        {
          "work": {
            "capacity": 8,
            "used": 0,
            "available": 8,
            "leases": [],
            "waiters": [{"operation_id": "00000000000000000000000000000001", "class": "test",
                         "weight": 3, "pid": 101, "position": 1}],
            "queue_depth": 1,
            "override_operation_id": "00000000000000000000000000000001"
          }
        }
        """)
        let work = try #require(legacy.work)
        #expect(work.overrideQueueDepth == 1)
        #expect(work.overrideQueue.isEmpty)
    }

    /// `overrides` is the new sequence receipt; an n-1 helper emits only
    /// `override`, and the envelope must present that as a one-item sequence.
    @Test("override envelope reads both the sequence and the legacy single receipt")
    func overrideEnvelopeAcceptsBothShapes() throws {
        let sequence = try PressureJSON.decode(WorkOverrideEnvelope.self, from: Data("""
        {
          "ok": true,
          "action": "work.override",
          "pinned": 2,
          "override": {"operation_id": "00000000000000000000000000000001", "class": "test",
                       "weight": 3, "pid": 101, "previous_position": 1,
                       "requested_at": "2026-07-25T10:00:00Z", "already_requested": false,
                       "override_position": 1},
          "overrides": [
            {"operation_id": "00000000000000000000000000000001", "class": "test", "weight": 3,
             "pid": 101, "previous_position": 1, "requested_at": "2026-07-25T10:00:00Z",
             "already_requested": false, "override_position": 1},
            {"operation_id": "00000000000000000000000000000002", "class": "build", "weight": 5,
             "pid": 102, "previous_position": 2, "requested_at": "2026-07-25T10:00:00Z",
             "already_requested": false, "override_position": 2}
          ]
        }
        """.utf8))
        #expect(sequence.pinned == 2)
        #expect(sequence.receipts.count == 2)
        #expect(sequence.receipts[1].overridePosition == 2)

        let legacy = try PressureJSON.decode(WorkOverrideEnvelope.self, from: Data("""
        {
          "ok": true,
          "action": "work.override",
          "override": {"operation_id": "00000000000000000000000000000001", "class": "test",
                       "weight": 3, "pid": 101, "previous_position": 1,
                       "requested_at": "2026-07-25T10:00:00Z", "already_requested": false}
        }
        """.utf8))
        #expect(legacy.pinned == 1)
        #expect(legacy.receipts.count == 1)
        #expect(legacy.receipts[0].overridePosition == 1)
    }

    @Test("missing lease becomes read-only lifecycle history")
    func missingLeaseTransitionsToHistory() throws {
        let live = try decodeWork("""
        {
          "work": {
            "capacity": 8,
            "used": 3,
            "available": 5,
            "leases": [{
              "id": "11111111111111111111111111111111",
              "operation_id": "00000000000000000000000000000002",
              "class": "test",
              "weight": 3,
              "pid": 202
            }],
            "waiters": [],
            "queue_depth": 0
          }
        }
        """)
        let empty = try decodeWork("""
        {
          "work": {
            "capacity": 8,
            "used": 0,
            "available": 8,
            "leases": [],
            "waiters": [],
            "queue_depth": 0
          }
        }
        """)
        let lease = try #require(live.work?.leases.first)
        let store = PressureStore()
        store.board.work = live.work
        store.workSelection = .lease(lease)

        store.board.work = empty.work
        store.refreshWorkSelectionFromBoard()

        guard case .historical(let historical) = store.workSelection else {
            Issue.record("missing lease remained live: \(String(describing: store.workSelection))")
            return
        }
        #expect(historical.operationID == lease.operationID)
        #expect(historical.previousState == "active lease")
    }

    /// The composite read must fold into exactly the board the fan-out produced,
    /// including reusing prior detail for sections this read did not request.
    @Test("composite board read folds every section and reuses prior detail")
    func compositeBoardFoldsSections() throws {
        let env = try PressureJSON.decode(BoardEnvelope.self, from: Data("""
        {
          "ok": true,
          "action": "board",
          "output_scope": "compact",
          "has_latest_monitor": true,
          "has_recovery_hint": false,
          "health": {"monitor_healthy": true, "daily_driver_ready": true,
                     "operator_ready": true, "protection_mode": "full"},
          "latest_monitor_summary": {"level": "warning", "free_percent": 22,
                                     "host_cpu_percent": 71.5, "agent_tree_count": 4},
          "work": {"capacity": 8, "used": 3, "available": 5, "leases": [], "waiters": [],
                   "queue_depth": 0},
          "admission": {"allowed": true, "level": "warning", "source": "resident"}
        }
        """.utf8))
        #expect(env.action == "board")
        #expect(env.effectiveSnapshot?.freePercent == 22)
        #expect(env.work?.capacity == 8)
        #expect(env.admission?.allowed == true)
        #expect(env.doctor == nil)

        var previous = PressureBoard()
        previous.calibration = WorkCalibration(operationCount: 42)
        previous.telemetryEvents = []
        let folded = PressureBoard(composite: env, previous: previous, binaryPath: "/usr/bin/ndev", live: false)
        #expect(folded.work?.used == 3)
        #expect(folded.admission?.allowed == true)
        #expect(folded.health?.monitorHealthy == true)
        #expect(folded.binaryPath == "/usr/bin/ndev")
        // Unrequested section keeps its prior value instead of blanking a pane.
        #expect(folded.calibration?.operationCount == 42)
    }

    /// The board emits one shape per key. `status` also has compact projections
    /// of coverage and launchd, but compact coverage types `limitations` as a
    /// count while the full one is a list — decoding both into one model threw
    /// and took the entire read down with it. Lock the real shapes.
    @Test("board decodes full coverage, launchd, and every opt-in section")
    func compositeBoardDecodesFullSections() throws {
        let env = try PressureJSON.decode(BoardEnvelope.self, from: Data("""
        {
          "ok": true,
          "action": "board",
          "coverage": {
            "status": "ready-with-explicit-boundaries",
            "repo_root": "/Users/x/dev/nicos-tools",
            "surfaces": [{"id": "work", "label": "Heavy work", "state": "enforced",
                          "scope": "coordinated", "detail": "weighted capacity"}],
            "limitations": ["io attribution is best effort"]
          },
          "launchd": {"ok": true, "label": "com.nicos.session-pressure",
                      "installed": true, "loaded": true, "pid": 961,
                      "artifact_present": true, "artifact_verified": true},
          "policy": {"enabled": true, "enforce_admission": true, "auto_shed_critical": true},
          "doctor": {"ok": false, "protection_mode": "full"},
          "calibration": {"operation_count": 7, "wrapper_interrupt_operations": 2},
          "inventory": {"candidates": [], "candidate_count": 0, "returned_count": 0, "truncated": false},
          "idle_source": "resident",
          "events": [],
          "actions": [],
          "work": {"capacity": 8, "used": 0, "available": 8, "leases": [], "waiters": [], "queue_depth": 0},
          "admission": {"allowed": false, "level": "red", "source": "resident"}
        }
        """.utf8))
        #expect(env.coverage?.limitations.count == 1)
        #expect(env.coverage?.status == "ready-with-explicit-boundaries")
        #expect(env.effectiveLaunchd?.loaded == true)
        #expect(env.policy?.enforceAdmission == true)
        #expect(env.doctor?.protectionMode == "full")
        #expect(env.calibration?.operationCount == 7)
        #expect(env.inventory?.candidateCount == 0)
        #expect(env.idleSource == "resident")
        #expect(env.admission?.allowed == false)
    }

    /// Only an unknown-subcommand rejection may downgrade to the per-contract
    /// fan-out. Any other failure must surface, not silently halve fidelity.
    @Test("board fallback triggers only on an unknown subcommand")
    func boardFallbackIsNarrow() {
        #expect(NDevPressureClient.indicatesMissingBoardVerb(
            .nonZeroExit(code: 2, stderr: "ndev session pressure: unknown subcommand \"board\"", stdout: "")) == true)
        #expect(NDevPressureClient.indicatesMissingBoardVerb(
            .nonZeroExit(code: 2, stderr: "unknown board argument \"--nope\"", stdout: "")) == false)
        #expect(NDevPressureClient.indicatesMissingBoardVerb(
            .nonZeroExit(code: 1, stderr: "sample failed", stdout: "")) == false)
        #expect(NDevPressureClient.indicatesMissingBoardVerb(.timedOut(seconds: 30)) == false)
        #expect(NDevPressureClient.indicatesMissingBoardVerb(.binaryNotFound) == false)
    }

    private func decodeWork(_ json: String) throws -> WorkEnvelope {
        try PressureJSON.decode(WorkEnvelope.self, from: Data(json.utf8))
    }

    private func decodeWorkStatus(_ json: String) -> WorkStatus? {
        try? PressureJSON.decode(WorkEnvelope.self, from: Data(json.utf8)).work
    }
}
