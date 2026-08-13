import Foundation
import Testing
@testable import NDevPressureCore

@Suite("Queue admission")
struct QueueAdmissionTests {
    @Test("decode protected scheduler status")
    func decodeProtectedSchedulerStatus() throws {
        let json = """
        {
          "action": "work.status",
          "ok": true,
          "work": {
            "schema_version": 4,
            "capacity": 8,
            "used": 6,
            "available": 2,
            "leases": [],
            "waiters": [{
              "operation_id": "op1",
              "class": "heavy",
              "weight": 6,
              "pid": 456,
              "queued_at": "2026-07-14T20:29:08Z",
              "position": 1,
              "wait_ms": 121000,
              "bypass_count": 4,
              "protected": true,
              "protection_reason": "bypass-bound"
            }],
            "queue_depth": 1,
            "scheduling_policy": "bounded-lookahead-v1",
            "selector_schema_version": 4,
            "protected_operation_id": "op1",
            "decision_reason": "protected_bounded_drain"
          }
        }
        """
        let envelope = try PressureJSON.decode(WorkEnvelope.self, from: Data(json.utf8))
        #expect(envelope.work?.waiters.first?.protected == true)
        #expect(envelope.work?.waiters.first?.waitMS == 121_000)
        #expect(envelope.work?.decisionReason == "protected_bounded_drain")
        #expect(PressureFormat.durationMS(121_000) == "2m")
    }

    @Test("decode queue-aware launch admission and policy")
    func decodeQueueAdmission() throws {
        let checkJSON = """
        {
          "action": "check",
          "admission": {
            "allowed": false,
            "level": "red",
            "source": "live-host-probe+work-queue",
            "dimension": "work_queue",
            "reasons": ["oldest work waiter exceeded new-launch threshold"],
            "work_queue": {
              "capacity": 8,
              "used": 6,
              "queue_depth": 4,
              "oldest_wait_ms": 121000,
              "queue_depth_block": 8,
              "oldest_wait_block_ms": 120000,
              "would_block": true,
              "enforced": true
            }
          }
        }
        """
        let check = try PressureJSON.decode(CheckEnvelope.self, from: Data(checkJSON.utf8))
        #expect(check.admission?.dimension == "work_queue")
        #expect(check.admission?.workQueue?.wouldBlock == true)
        #expect(check.admission?.workQueue?.oldestWaitMS == 121_000)

        let policyJSON = """
        {
          "policy": {
            "enabled": true,
            "enforce_admission": true,
            "auto_shed_critical": false,
            "launch_admission": {
              "mode": "soft",
              "queue_depth_block": 8,
              "oldest_wait_block_seconds": 120,
              "resume_behavior": "warn"
            }
          }
        }
        """
        let envelope = try PressureJSON.decode(PolicyEnvelope.self, from: Data(policyJSON.utf8))
        #expect(envelope.policy?.launchAdmission?.queueDepthBlock == 8)
        #expect(envelope.policy?.launchAdmission?.resumeBehavior == "warn")
    }

    @Test("decode operator queue override")
    func decodeQueueOverride() throws {
        let json = """
        {
          "action": "work.override",
          "ok": true,
          "override": {
            "operation_id": "00000000000000000000000000000003",
            "class": "test",
            "weight": 3,
            "pid": 456,
            "previous_position": 4,
            "requested_at": "2026-07-16T20:00:00Z",
            "already_requested": false
          },
          "work": {
            "schema_version": 4,
            "capacity": 8,
            "used": 6,
            "available": 2,
            "leases": [],
            "waiters": [],
            "queue_depth": 4,
            "scheduling_policy": "bounded-lookahead-v1",
            "selector_schema_version": 5,
            "protected_operation_id": "00000000000000000000000000000003",
            "decision_reason": "priority_override_bounded_drain",
            "override_operation_id": "00000000000000000000000000000003",
            "override_requested_at": "2026-07-16T20:00:00Z"
          }
        }
        """
        let envelope = try PressureJSON.decode(WorkOverrideEnvelope.self, from: Data(json.utf8))
        #expect(envelope.receipt?.previousPosition == 4)
        #expect(envelope.work?.selectorSchemaVersion == 5)
        #expect(envelope.work?.overrideOperationID == envelope.receipt?.operationID)
        #expect(envelope.work?.decisionReason == "priority_override_bounded_drain")
    }

    @Test("decode work lifecycle history envelope")
    func decodeWorkHistory() throws {
        let json = """
        {
          "action": "work.history",
          "ok": true,
          "since": "2026-07-16T02:14:06Z",
          "work_event_count": 2,
          "work_events": [
            {
              "schema_version": 3,
              "event_id": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
              "request_id": "cccccccccccccccccccccccccccccccc",
              "timestamp": "2026-07-17T02:13:05.775594Z",
              "event": "acquired",
              "operation_id": "3591da170ceb5c7d6c16d88b041f12b9",
              "lease_id": "a7761747fdf0409c1da3747cb988b8f3",
              "class": "test",
              "weight": 3,
              "pid": 47623,
              "command_digest": "sha256:d1119227a47498786bab382f0c3011f82bf9d13fa52fe6e455b6b3ba232941aa",
              "blocker": "capacity",
              "queue_position": 1,
              "queue_depth": 1,
              "wait_ms": 19011,
              "outcome": "lease_acquired"
            },
            {
              "schema_version": 3,
              "event_id": "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
              "timestamp": "2026-07-17T02:13:05.935728Z",
              "event": "started",
              "operation_id": "3591da170ceb5c7d6c16d88b041f12b9",
              "lease_id": "a7761747fdf0409c1da3747cb988b8f3",
              "class": "test",
              "weight": 3,
              "pid": 48458,
              "outcome": "child_bound"
            }
          ]
        }
        """
        let envelope = try PressureJSON.decode(WorkHistoryEnvelope.self, from: Data(json.utf8))
        #expect(envelope.workEventCount == 2)
        #expect(envelope.workEvents.count == 2)
        #expect(envelope.workEvents[0].event == "acquired")
        #expect(envelope.workEvents[0].requestID == "cccccccccccccccccccccccccccccccc")
        #expect(envelope.workEvents[0].waitMS == 19_011)
        #expect(envelope.workEvents[1].event == "started")
        #expect(envelope.workEvents[1].operationID == "3591da170ceb5c7d6c16d88b041f12b9")
    }
}
