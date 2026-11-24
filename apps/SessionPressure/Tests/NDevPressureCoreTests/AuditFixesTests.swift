import Foundation
import Testing
@testable import NDevPressureCore

@Suite("Audit fixes model decoding")
struct AuditFixesTests {
    @Test("decode resource admission work status and headroom fields")
    func decodeResourceAdmissionWorkStatus() throws {
        let json = """
        {
          "action": "work.status",
          "ok": true,
          "work": {
            "schema_version": 8,
            "admission": "resource",
            "capacity": 1048576,
            "used": 10,
            "available": 1048566,
            "parked_weight": 6,
            "leases": [
              {
                "id": "lease-1",
                "operation_id": "op-1",
                "class": "build",
                "weight": 4,
                "pid": 101,
                "age_ms": 5000,
                "parked": false,
                "predicted_peak_mb": 1400,
                "predicted_median_mb": 900
              },
              {
                "id": "lease-2",
                "operation_id": "op-2",
                "class": "test",
                "weight": 6,
                "pid": 102,
                "age_ms": 90000,
                "parked": true,
                "predicted_peak_mb": 800
              }
            ],
            "waiters": [
              {
                "operation_id": "op-3",
                "class": "build",
                "weight": 5,
                "pid": 103,
                "position": 1,
                "wait_ms": 25000,
                "start_expired": true,
                "predicted_peak_mb": 2048
              }
            ],
            "queue_depth": 1
          }
        }
        """
        let envelope = try PressureJSON.decode(WorkEnvelope.self, from: Data(json.utf8))
        let work = try #require(envelope.work)
        #expect(work.isResourceAdmission)
        #expect(work.admission == "resource")
        #expect(work.parkedWeight == 6)
        #expect(work.activeLeaseCount == 1)
        #expect(work.parkedLeaseCount == 1)
        #expect(work.leases.first?.predictedPeakMB == 1400)
        #expect(work.leases.first?.predictedMedianMB == 900)
        #expect(work.waiters.first?.startExpired == true)
        #expect(work.waiters.first?.predictedPeakMB == 2048)
    }

    @Test("decode memory headroom and recovery clear envelopes")
    func decodeHeadroomAndRecoveryClear() throws {
        let headroomJSON = """
        {
          "action": "work.headroom",
          "ok": true,
          "memory_admission": "enforce",
          "headroom": {
            "rule": "mac-headroom-tree-p90-scope-p95-ramp-pooled-v5",
            "schema_version": 1,
            "sample_fresh": true,
            "level": "normal",
            "physical_mb": 49152,
            "available_mb": 34000,
            "reserve_mb": 6144,
            "lease_count": 2,
            "lease_predicted_mb": 1600,
            "ramping_lease_count": 1,
            "leased_rss_mb": 400,
            "leased_rss_known": true,
            "inflight_mb": 1200,
            "headroom_mb": 26656,
            "swap_used_mb": 128,
            "swap_out_mb_per_s": 0.5,
            "swap_thrash_latched": false
          }
        }
        """
        let headroomEnv = try PressureJSON.decode(WorkHeadroomEnvelope.self, from: Data(headroomJSON.utf8))
        #expect(headroomEnv.memoryAdmission == "enforce")
        #expect(headroomEnv.headroom?.headroomMB == 26_656)
        #expect(headroomEnv.headroom?.sampleFresh == true)
        #expect(headroomEnv.headroom?.swapOutMBPerSecond == 0.5)

        let clearJSON = """
        {"action":"recovery.clear","ok":true,"cleared":true}
        """
        let clearEnv = try PressureJSON.decode(RecoveryClearEnvelope.self, from: Data(clearJSON.utf8))
        #expect(clearEnv.ok == true)
        #expect(clearEnv.cleared == true)
    }

    @Test("doctor work recognizes resource admission sentinel")
    func doctorWorkResourceSentinel() throws {
        let json = """
        {"capacity": 1048576, "used": 10, "queue_depth": 2}
        """
        let docWork = try PressureJSON.decode(DoctorWork.self, from: Data(json.utf8))
        #expect(docWork.isResourceAdmission)
    }
}
