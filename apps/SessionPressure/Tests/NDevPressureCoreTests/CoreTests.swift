import Foundation
import Testing
@testable import NDevPressureCore

@Suite("NDevPressureCore")
struct CoreTests {
    @Test("utilization is memory used, not a discrete policy-level stub")
    func utilizationMatchesUsedPercent() {
        #expect(PressureUtilization.memoryUsedPercent(freePercent: 49) == 51)
        #expect(PressureUtilization.fraction(freePercent: 49) == Double(51) / 100)
        #expect(PressureUtilization.memoryUsedPercent(freePercent: 100) == 0)
        #expect(PressureUtilization.fraction(freePercent: 100) == 0)
        #expect(PressureUtilization.memoryUsedPercent(freePercent: 0) == 100)
        #expect(PressureUtilization.fraction(freePercent: 0) == 1)
        #expect(PressureUtilization.memoryUsedPercent(freePercent: -4) == 100)
        #expect(PressureUtilization.memoryUsedPercent(freePercent: 140) == 0)

        let warningButLotsFree = PressureUtilization(freePercent: 80, hostCPUPercent: 90)
        #expect(warningButLotsFree.memoryUsedPercent == 20)
        #expect(warningButLotsFree.fraction == Double(20) / 100)
        #expect(warningButLotsFree.menuBarGaugeSymbol == "gauge.with.dots.needle.33percent")

        let halfUsed = PressureUtilization(freePercent: 49)
        #expect(halfUsed.menuBarGaugeSymbol == "gauge.with.dots.needle.67percent")

        let nearlyFull = PressureUtilization(freePercent: 8)
        #expect(nearlyFull.memoryUsedPercent == 92)
        #expect(nearlyFull.menuBarGaugeSymbol == "gauge.with.dots.needle.100percent")

        let snap = PressureSnapshot(freePercent: 42, hostCPUPercent: 88)
        #expect(snap.utilization.memoryUsedPercent == 58)
        #expect(snap.utilization.fraction == Double(58) / 100)
    }

    @Test("pressure level ordering")
    func levelOrdering() {
        #expect(PressureLevel.normal < PressureLevel.warning)
        #expect(PressureLevel.warning < PressureLevel.red)
        #expect(PressureLevel.red < PressureLevel.critical)
        #expect(PressureLevel(raw: "RED") == .red)
        #expect(PressureLevel(raw: "nope") == .unknown)
    }

    @Test("decode work calibration interrupt count and optional suggestion")
    func decodeWorkCalibrationInterruptAndSuggestion() throws {
        let withCount = """
        {
          "ok": true,
          "action": "work.report",
          "calibration": {
            "schema_version": 1,
            "operation_count": 40,
            "wrapper_interrupt_operations": 3,
            "suggested_policy_profile": "multi-agent-soft",
            "suggested_policy_profile_reason": "high_cancel_rate",
            "review_signals": {
              "cancelled_operations": 8,
              "wrapper_interrupt_operations": 3,
              "long_wait_operations": 10,
              "reservation_deferrals": 12
            },
            "interrupt_projection": {
              "schema_version": 1,
              "wrapper_interrupt_operations": 3,
              "wrapper_interrupt_rate_per_hour": 0.125,
              "window_hours": 24
            }
          }
        }
        """
        let env = try PressureJSON.decode(WorkReportEnvelope.self, from: Data(withCount.utf8))
        #expect(env.calibration?.interruptCount == 3)
        #expect(env.calibration?.suggestedPolicyProfile == "multi-agent-soft")
        #expect(env.calibration?.reviewSignals?.longWaitOperations == 10)
        #expect(env.calibration?.interruptProjection?.wrapperInterruptOperations == 3)

        var livePolicy = PressurePolicy(enabled: true, enforceAdmission: true, autoShedCritical: true)
        livePolicy.profile = "balanced"
        let suggestion = PolicySuggestionFactory.current(from: env.calibration, policy: livePolicy)
        #expect(suggestion?.profile == "multi-agent-soft")
        #expect(suggestion?.currentTitle == "Balanced")
        #expect(suggestion?.currentFlags.contains("launch blocking on") == true)
        #expect(suggestion?.currentFlags.contains("auto-shed on") == true)
        #expect(suggestion?.weakensProtection == true)
        #expect(suggestion?.agentPaste.contains("Current profile: Balanced") == true)
        #expect(suggestion?.agentPaste.contains("Copy") == false)
        #expect(suggestion?.agentPaste.contains("ndev session pressure policy profile apply multi-agent-soft --dry-run") == true)
        #expect(suggestion?.explanation.contains("10 long waits") == true)
        #expect(suggestion?.explanation.contains("Current work style") == false)

        var alreadySoft = PressurePolicy(enabled: true, enforceAdmission: false, autoShedCritical: false)
        alreadySoft.profile = "multi-agent-soft"
        let restore = PolicySuggestionFactory.current(from: env.calibration, policy: alreadySoft)
        #expect(restore?.kind == .restoreDefault)
        #expect(restore?.profile == "balanced")
        #expect(restore?.currentTitle == "Multi Agent Soft")
        #expect(restore?.headline == "Default Balanced")
        #expect(restore?.showsApplyWhenCollapsed == true)
        #expect(restore?.weakensProtection == false)

        var alreadyBalanced = PressurePolicy(enabled: true, enforceAdmission: true, autoShedCritical: true)
        alreadyBalanced.profile = "balanced"
        let noCal = WorkCalibration(schemaVersion: 1, operationCount: 2)
        #expect(PolicySuggestionFactory.current(from: noCal, policy: alreadyBalanced) == nil)

        let absent = """
        {"ok":true,"action":"work.report","calibration":{"schema_version":1,"operation_count":2}}
        """
        let env2 = try PressureJSON.decode(WorkReportEnvelope.self, from: Data(absent.utf8))
        #expect(env2.calibration?.interruptCount == 0)
        #expect(env2.calibration?.suggestedPolicyProfile == nil)

        var board = PressureBoard(calibration: env.calibration)
        #expect(board.wrapperInterruptOperations == 3)
        board = PressureBoard(calibration: env2.calibration)
        #expect(board.wrapperInterruptOperations == 0)
    }

    @Test("decode doctor envelope from session pressure doctor JSON")
    func decodeDoctorEnvelope() throws {
        let json = """
        {
          "ok": true,
          "action": "doctor",
          "schema_version": 1,
          "protection_mode": "observe-only",
          "policy_persisted": true,
          "enforce_admission": false,
          "auto_shed_critical": false,
          "monitor": {"healthy": true, "fresh": true, "age_seconds": 12, "pid": 99, "loaded": true, "running": true},
          "host": {"level": "normal", "source": "resident"},
          "work": {"capacity": 8, "used": 2, "queue_depth": 0, "express_green": true},
          "launch_soft_pressure": {"would_block": false, "noise_suppressed": true, "enforced": false},
          "coverage_status": "ready-with-explicit-boundaries",
          "fixes": [],
          "warnings": []
        }
        """
        let data = Data(json.utf8)
        let doc = try JSONDecoder().decode(DoctorEnvelope.self, from: data)
        #expect(doc.ok == true)
        #expect(doc.schemaVersion == 1)
        #expect(doc.protectionMode == "observe-only")
        #expect(doc.enforceAdmission == false)
        #expect(doc.autoShedCritical == false)
        #expect(doc.monitor?.healthy == true)
        #expect(doc.work?.expressGreen == true)
        #expect(doc.launchSoftPressure?.noiseSuppressed == true)
        #expect(doc.host?.level == .normal)
    }

    @Test("format helpers")
    func formats() {
        #expect(PressureFormat.mb(512).contains("MiB"))
        #expect(PressureFormat.mb(2048).contains("GiB"))
        #expect(PressureFormat.bytes(50 << 30) == "50.0 GiB")
        #expect(PressureFormat.percent(12.5) == "12.5%")
        #expect(PressureFormat.duration(seconds: 90) == "1m")
        #expect(PressureFormat.duration(seconds: 3700).contains("h"))
        #expect(PressureFormat.shortSession("019f5946-2c91-7653-99a4-ef51389e84b3").hasSuffix("…"))
        #expect(PressureFormat.pollInterval(for: .critical) < PressureFormat.pollInterval(for: .normal))
        #expect(PressureFormat.workFocusPollInterval(queueDepth: 1, leaseCount: 0, interfaceActive: true) == 2.5)
        #expect(PressureFormat.workFocusPollInterval(queueDepth: 0, leaseCount: 0, interfaceActive: true) == 10)
        #expect(PressureFormat.workFocusPollInterval(queueDepth: 4, leaseCount: 1, interfaceActive: false) == 30)
        #expect(PressureFormat.shortOperationID("abcdef0123456789") == "abcdef01…")
        #expect(PressureFormat.shortDigest("sha256:d1119227a47498786bab382f0c3011f82bf9d13fa52fe6e455b6b3ba232941aa").hasPrefix("sha256:"))
    }

    @Test("decode snapshot envelope from realistic JSON")
    func decodeSnapshot() throws {
        let json = """
        {
          "action": "snapshot",
          "ok": true,
          "snapshot": {
            "schema_version": 1,
            "timestamp": "2026-07-14T20:30:34.446213Z",
            "level": "red",
            "reasons": ["host CPU 100.0% >= red 95.0%"],
            "free_percent": 44,
            "physical_memory_mb": 16384,
            "swap_used_mb": 9967.5625,
            "logical_cpu_count": 10,
            "host_cpu_percent": 100,
            "host_cpu_available": true,
            "agent_cpu_percent": 0,
            "agent_cpu_available": true,
            "process_count": 776,
            "process_rss_sum_mb": 10033.95,
            "agent_tree_count": 2,
            "agent_rss_sum_mb": 700.5,
            "memory_momentum": "declining",
            "free_percent_slope_per_minute": -2.5,
            "minutes_to_memory_red": 11.6,
            "memory_momentum_sample_count": 5,
            "process_inventory_available": true,
            "process_inventory_fresh": true,
            "process_inventory_captured_at": "2026-07-14T20:30:30Z",
            "process_inventory_age_seconds": 4.4,
            "process_inventory_source": "libproc",
            "guard_pid": 39107,
            "guard_rss_mb": 7.25,
            "guard_cpu_percent": 0,
            "guard_role": "operator",
            "guard_budget_applicable": false,
            "guard_baseline_proven": false,
            "sample_duration_ms": 73.7,
            "sample_cpu_time_ms": 15.5,
            "guard_budget_ok": true,
            "top_host_consumers": [
              {
                "executable": "Google_Chrome_Helper",
                "category": "browser",
                "process_count": 12,
                "rss_sum_mb": 1200.5,
                "cpu_percent_sum": 15.2,
                "cpu_available": true,
                "agent_process_count": 0
              }
            ],
            "storage": {
              "available": true,
              "volume_path": "/System/Volumes/Data",
              "source": "statfs",
              "total_bytes": 494384795648,
              "free_bytes": 28991029248,
              "available_bytes": 28454281216,
              "free_percent": 5.8,
              "level": "warning",
              "reasons": ["available storage below warning threshold"]
            },
            "top_agent_trees": [
              {
                "agent": "codex",
                "root_pid": 41835,
                "executable": "codex",
                "process_count": 26,
                "rss_sum_mb": 444.3,
                "cpu_percent_sum": 0,
                "cpu_available": true,
                "elapsed_seconds": 3677,
                "semantic_state": "ready",
                "semantic_state_at": "2026-07-14T20:30:30Z",
                "session_id": "019f5946-2c91-7653-99a4-ef51389e84b3"
              }
            ]
          }
        }
        """
        let data = Data(json.utf8)
        let env = try PressureJSON.decode(SnapshotEnvelope.self, from: data)
        #expect(env.ok == true)
        let snap = try #require(env.snapshot)
        #expect(snap.level == .red)
        #expect(snap.freePercent == 44)
        #expect(snap.topAgentTrees.count == 1)
        #expect(snap.topAgentTrees[0].rootPID == 41835)
        #expect(snap.topAgentTrees[0].sessionID != nil)
        #expect(snap.topAgentTrees[0].cpuAvailable == true)
        #expect(snap.agentCPUAvailable == true)
        #expect(snap.topAgentTrees[0].semanticState == .ready)
        #expect(snap.reasons.first?.contains("host CPU") == true)
        #expect(snap.storage.available == true)
        #expect(snap.storage.level == .warning)
        #expect(snap.storage.availableBytes == 28_454_281_216)
        #expect(snap.storage.volumePath == "/System/Volumes/Data")
        #expect(snap.memoryMomentum == .declining)
        #expect(snap.minutesToMemoryRed == 11.6)
        #expect(snap.processInventorySource == "libproc")
        #expect(snap.topHostConsumers.first?.executable == "Google_Chrome_Helper")
        #expect(snap.topHostConsumers.first?.cpuAvailable == true)
    }

    @Test("decode admission holds, latch, and pressure-reserved waiters")
    func decodeAdmissionHoldsAndReservations() throws {
        let workJSON = """
        {
          "action": "work.status",
          "ok": true,
          "work": {
            "schema_version": 7,
            "capacity": 8,
            "used": 2,
            "available": 6,
            "leases": [],
            "waiters": [
              {"operation_id": "a1", "class": "build", "weight": 5, "wait_ms": 1000, "reservation_kind": "pressure", "reserved_at": "2026-07-25T20:01:08Z"},
              {"operation_id": "a2", "class": "test", "weight": 3, "wait_ms": 500}
            ],
            "queue_depth": 2,
            "admission_holds": [
              {"operation_id": "b1", "class": "express-test", "weight": 1, "pid": 42,
               "held_since": "2026-07-25T20:00:00Z", "held_for_ms": 205000,
               "dimension": "cpu", "reason": "host CPU 100.0% >= red 95.0%"}
            ],
            "admission_hold_count": 1,
            "longest_admission_hold_ms": 205000,
            "admission_latch": {
              "latched": true, "dimension": "cpu", "red_samples": 2,
              "recovery_samples": 1, "block_required": 2, "release_required": 2
            }
          }
        }
        """
        let envelope = try PressureJSON.decode(WorkEnvelope.self, from: Data(workJSON.utf8))
        let work = try #require(envelope.work)
        #expect(work.admissionHoldCount == 1)
        #expect(work.admissionHolds.count == 1)
        #expect(work.admissionHolds[0].className == "express-test")
        #expect(work.admissionHolds[0].heldForMS == 205_000)
        #expect(work.admissionHolds[0].dimension == "cpu")
        #expect(work.longestAdmissionHoldMS == 205_000)
        // A hold must never be mistaken for consumed capacity.
        #expect(work.used == 2)
        #expect(work.available == 6)
        let latch = try #require(work.admissionLatch)
        #expect(latch.latched)
        #expect(latch.recoverySamples == 1)
        #expect(latch.releaseRequired == 2)
        // Only the pressure-reserved waiter suppresses the Run now affordance.
        #expect(work.waiters[0].isPressureReserved)
        #expect(work.waiters[0].reservedAt != nil)
        #expect(!work.waiters[1].isPressureReserved)
    }

    @Test("work status from an n-1 helper decodes with no holds")
    func decodeWorkStatusWithoutAdmissionFields() throws {
        let workJSON = """
        {"action":"work.status","ok":true,"work":{"schema_version":6,"capacity":8,"used":0,
         "available":8,"leases":[],"waiters":[],"queue_depth":0}}
        """
        let envelope = try PressureJSON.decode(WorkEnvelope.self, from: Data(workJSON.utf8))
        let work = try #require(envelope.work)
        // Absent must read as "nothing held", never as a decode failure that
        // blanks the whole work view against an older installed helper.
        #expect(work.admissionHolds.isEmpty)
        #expect(work.admissionHoldCount == 0)
        #expect(work.longestAdmissionHoldMS == 0)
        #expect(work.admissionLatch == nil)
    }

    @Test("decode status health + work")
    func decodeStatusAndWork() throws {
        let statusJSON = """
        {
          "action": "status",
          "has_latest_monitor": true,
          "has_recovery_hint": true,
          "recovery_hint": {
            "schema_version": 1,
            "detected_at": "2026-07-14T20:29:30Z",
            "previous_started_at": "2026-07-14T20:00:00Z",
            "last_sample_at": "2026-07-14T20:29:20Z",
            "last_level": "red",
            "reason": "previous monitor was unclean",
            "recovery_command": "ndev session recover --around 2026-07-14"
          },
          "health": {
            "monitor_healthy": true,
            "daily_driver_ready": true,
            "operator_ready": false,
            "operator_reasons": ["an unclean-shutdown recovery hint is pending review"],
            "protection_mode": "full",
            "latest_monitor_fresh": true,
            "latest_monitor_age_seconds": 12.5,
            "resident_samples": 10,
            "resident_normal_samples": 4,
            "required_normal_samples": 4
          },
          "coverage": {
            "status": "ready-with-explicit-boundaries",
            "repo_root": "/tmp/nicos-tools",
            "surfaces": [
              {"id":"direct_external_launch","label":"Direct external launches","state":"observed","scope":"machine","detail":"visible but not intercepted"}
            ],
            "limitations": ["Direct external launches are observed but not intercepted."]
          },
          "latest_monitor": {
            "schema_version": 1,
            "timestamp": "2026-07-14T20:29:20.658934Z",
            "level": "normal",
            "free_percent": 49,
            "physical_memory_mb": 16384,
            "swap_used_mb": 1000,
            "logical_cpu_count": 10,
            "host_cpu_percent": 40,
            "host_cpu_available": true,
            "agent_cpu_percent": 0,
            "process_count": 100,
            "process_rss_sum_mb": 1000,
            "agent_tree_count": 1,
            "agent_rss_sum_mb": 200,
            "process_inventory_available": true,
            "process_inventory_fresh": true,
            "guard_pid": 1,
            "guard_rss_mb": 7,
            "guard_cpu_percent": 0,
            "guard_role": "resident",
            "guard_budget_applicable": true,
            "guard_baseline_proven": true,
            "sample_duration_ms": 10,
            "sample_cpu_time_ms": 2,
            "guard_budget_ok": true,
            "top_agent_trees": []
          }
        }
        """
        let status = try PressureJSON.decode(StatusEnvelope.self, from: Data(statusJSON.utf8))
        #expect(status.health?.dailyDriverReady == true)
        #expect(status.health?.operatorReady == false)
        #expect(status.latestMonitor?.level == .normal)
        #expect(status.hasRecoveryHint == true)
        #expect(status.recoveryHint?.lastLevel == .red)
        #expect(status.recoveryHint?.recoveryCommand.hasPrefix("ndev session recover") == true)
        #expect(status.coverage?.surfaces.first?.state == "observed")

        let workJSON = """
        {
          "action": "work.status",
          "ok": true,
          "work": {
            "schema_version": 3,
            "capacity": 8,
            "used": 6,
            "available": 2,
            "leases": [
              {
                "id": "abc",
                "class": "build",
                "weight": 6,
                "pid": 123,
                "started_at": "2026-07-14T20:30:29.408076Z"
              }
            ],
            "waiters": [
              {
                "operation_id": "op1",
                "class": "test",
                "weight": 3,
                "pid": 456,
                "queued_at": "2026-07-14T20:29:08.753275Z",
                "position": 1
              }
            ],
            "queue_depth": 1
          }
        }
        """
        let work = try PressureJSON.decode(WorkEnvelope.self, from: Data(workJSON.utf8))
        #expect(work.work?.capacity == 8)
        #expect(work.work?.leases.count == 1)
        #expect(work.work?.waiters.first?.className == "test")
    }

    @Test("policy mode labels")
    func policyModes() {
        #expect(PressurePolicy(enabled: false).modeLabel == "Disabled")
        #expect(PressurePolicy(enabled: true, enforceAdmission: false).modeLabel == "Observe only")
        #expect(PressurePolicy(enabled: true, enforceAdmission: true, autoShedCritical: false).modeLabel == "Admission only")
        #expect(PressurePolicy(enabled: true, enforceAdmission: true, autoShedCritical: true).modeLabel == "Full protection")
    }

    @Test("decode heartbeat summary wrapper interrupt fields")
    func decodeTelemetrySummaryWrapperInterrupts() throws {
        let json = """
        {
          "events": [
            {
              "event": "heartbeat",
              "timestamp": "2026-07-20T21:00:00Z",
              "summary": {
                "level": "normal",
                "free_percent": 40,
                "swap_used_mb": 1,
                "host_cpu_percent": 10,
                "agent_tree_count": 0,
                "agent_rss_sum_mb": 0,
                "memory_momentum": "steady",
                "free_percent_slope_per_minute": 0,
                "wrapper_interrupt_operations": 4,
                "wrapper_interrupt_rate_per_hour": 0.17
              }
            }
          ]
        }
        """
        let env = try PressureJSON.decode(TelemetryEnvelope.self, from: Data(json.utf8))
        #expect(env.events.first?.summary?.wrapperInterruptOperations == 4)
        #expect(env.events.first?.summary?.wrapperInterruptRatePerHour == 0.17)
    }

    @Test("decode compact telemetry momentum")
    func decodeTelemetryMomentum() throws {
        let json = """
        {
          "events": [{
            "schema_version": 1,
            "timestamp": "2026-07-16T12:00:00Z",
            "event": "heartbeat",
            "summary": {
              "schema_version": 1,
              "timestamp": "2026-07-16T12:00:00Z",
              "level": "warning",
              "free_percent": 23,
              "swap_used_mb": 1024,
              "host_cpu_percent": 44,
              "agent_tree_count": 3,
              "agent_rss_sum_mb": 1200,
              "memory_momentum": "rapid_decline",
              "free_percent_slope_per_minute": -4.2,
              "minutes_to_memory_red": 1.9,
              "guard_rss_mb": 8,
              "guard_budget_ok": true
            }
          }],
          "actions": []
        }
        """
        let telemetry = try PressureJSON.decode(TelemetryEnvelope.self, from: Data(json.utf8))
        #expect(telemetry.events.first?.summary?.memoryMomentum == .rapidDecline)
        #expect(telemetry.events.first?.summary?.minutesToMemoryRed == 1.9)
    }

    @Test("decode disk-write status, writers, policy, and hourly totals")
    func decodeDiskWriteContracts() throws {
        let statusJSON = """
        {
          "ok": true,
          "action": "io.status",
          "output_scope": "full",
          "summary": {
            "schema_version": 1,
            "model_version": "quiet-adaptive-v1",
            "captured_at": "2026-07-22T22:00:00Z",
            "state": "unusual",
            "confidence": "confident",
            "source": "iokit+libproc",
            "device_scope": "internal_ssd",
            "attribution_scope": "all_disk_io_best_effort",
            "context": "express-test",
            "current_bytes_per_second": 1048576,
            "window_15m_bytes": 943718400,
            "bytes_24h": 4294967296,
            "baseline_p99_bytes_15m": 104857600,
            "baseline_ratio": 9,
            "baseline_samples": 7000,
            "unscored_gap_bytes": 4096,
            "reason_codes": ["rolling_window_incomplete"],
            "attribution_available": true,
            "writer_available_count": 1,
            "top_writer": {
              "executable": "sqlite3",
              "category": "database",
              "process_count": 1,
              "agent_process_count": 0,
              "window_bytes": 524288000,
              "bytes_per_second": 524288
            }
          },
          "report": {
            "summary": {
              "schema_version": 1,
              "model_version": "quiet-adaptive-v1",
              "captured_at": "2026-07-22T22:00:00Z",
              "state": "unusual",
              "confidence": "confident",
              "source": "iokit+libproc",
              "device_scope": "internal_ssd",
              "attribution_scope": "all_disk_io_best_effort",
              "context": "express-test",
              "attribution_available": true
            },
            "writers": [{
              "executable": "sqlite3",
              "category": "database",
              "process_count": 1,
              "agent_process_count": 0,
              "window_bytes": 524288000,
              "bytes_per_second": 524288,
              "pid": 123,
              "process_start_id": 456
            }],
            "available_count": 1,
            "returned_count": 1,
            "truncated": false
          },
          "disk_write_policy": {
            "enabled": true,
            "notifications_enabled": false,
            "sample_interval_seconds": 15,
            "baseline_retention_days": 14,
            "profile": "quiet-adaptive-v1",
            "trace_max_duration_seconds": 30
          }
        }
        """
        let status = try PressureJSON.decode(DiskWriteStatusEnvelope.self, from: Data(statusJSON.utf8))
        #expect(status.summary?.state == .unusual)
        #expect(status.summary?.confidence == .confident)
        #expect(status.summary?.topWriter?.executable == "sqlite3")
        #expect(status.summary?.rollingWindowIncomplete == true)
        #expect(status.summary?.unscoredGapBytes == 4_096)
        #expect(status.report?.writers.first?.pid == 123)
        #expect(status.diskWritePolicy?.notificationsEnabled == false)

        let topJSON = """
        {
          "ok": true,
          "action": "io.top",
          "output_scope": "persisted_lead",
          "writers": [],
          "available_count": 3,
          "returned_count": 0,
          "truncated": true
        }
        """
        let top = try PressureJSON.decode(DiskWriteTopEnvelope.self, from: Data(topJSON.utf8))
        #expect(top.outputScope == "persisted_lead")

        let historyJSON = """
        {
          "ok": true,
          "history": [{
            "hour": "2026-07-22T21:00:00Z",
            "state": "normal",
            "bytes_written": 1073741824,
            "unscored_gap_bytes": 4096,
            "baseline_p99_bytes": 268435456,
            "sample_count": 240
          }],
          "available_count": 1,
          "returned_count": 1,
          "truncated": false
        }
        """
        let history = try PressureJSON.decode(DiskWriteHistoryEnvelope.self, from: Data(historyJSON.utf8))
        #expect(history.history.first?.bytesWritten == 1_073_741_824)
        #expect(history.history.first?.unscoredGapBytes == 4_096)
        #expect(history.history.first?.sampleCount == 240)
    }

    @Test("compact status carries disk-write summary without full hydration")
    func decodeCompactDiskWriteStatus() throws {
        let json = """
        {
          "action": "status",
          "latest_monitor_summary": {
            "timestamp": "2026-07-22T22:00:00Z",
            "level": "normal",
            "free_percent": 42,
            "swap_used_mb": 0,
            "host_cpu_percent": 12,
            "agent_tree_count": 2,
            "agent_rss_sum_mb": 512,
            "sample_role": "resident",
            "self_rss_mb": 9,
            "self_cpu_percent": 0.1,
            "self_budget_applicable": true,
            "memory_momentum": "steady",
            "guard_budget_ok": true,
            "monitor_samples": 20,
            "normal_monitor_samples": 20,
            "disk_write": {
              "schema_version": 1,
              "model_version": "quiet-adaptive-v1",
              "captured_at": "2026-07-22T22:00:00Z",
              "state": "learning",
              "confidence": "learning",
              "source": "iokit+libproc",
              "device_scope": "internal_ssd",
              "attribution_scope": "all_disk_io_best_effort",
              "context": "uncoordinated",
              "attribution_available": true
            }
          }
        }
        """
        let status = try PressureJSON.decode(StatusEnvelope.self, from: Data(json.utf8))
        #expect(status.latestMonitor == nil)
        #expect(status.latestMonitorSummary?.freePercent == 42)
        #expect(status.latestMonitorSummary?.diskWrite?.state == .learning)
        let tree = AgentTree(agent: "codex", rootPID: 7, executable: "codex", processCount: 1, rssSumMB: 100, cpuPercentSum: 0)
        let full = PressureSnapshot(freePercent: 10, topAgentTrees: [tree])
        let merged = full.applyingCompact(try #require(status.latestMonitorSummary))
        #expect(merged.freePercent == 42)
        #expect(merged.topAgentTrees.first?.rootPID == 7)
        #expect(merged.diskWrite?.state == .learning)
    }

    @Test("fs_usage path parsing is unique and bounded")
    func parseBoundedDiskTracePaths() {
        let output = """
        12:00:00  open              /tmp/example.sqlite                                  sqlite3.123
        12:00:01  write             /tmp/example.sqlite                                  sqlite3.123
        12:00:02  rename            /Users/nico/Library/Application Support/demo.db       demo.456
        no pathname on this line
        """
        let result = DiskTraceParser.paths(fromFSUsage: output, maximumPaths: 2)
        #expect(result.paths == ["/tmp/example.sqlite", "/Users/nico/Library/Application Support/demo.db"])
        #expect(result.truncated == true)
    }

    @Test("missing-binary copy names session-pressure, not only ndev")
    func missingBinaryCopyNamesProductCLI() {
        let message = NDevPressureClientError.binaryNotFound.errorDescription ?? ""
        #expect(message.contains("session-pressure"))
        #expect(!message.hasPrefix("ndev not found"))
    }

    @Test("resolveBinary uses SESSION_PRESSURE_BIN when that path is executable")
    func resolveBinaryUsesSessionPressureBin() throws {
        let dir = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString, isDirectory: true)
        try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        defer { try? FileManager.default.removeItem(at: dir) }
        let bin = dir.appendingPathComponent("session-pressure")
        #expect(FileManager.default.createFile(atPath: bin.path, contents: Data("#!/bin/sh\n".utf8), attributes: [.posixPermissions: 0o755]))
        #expect(NDevPressureClient.resolveBinary(environment: ["SESSION_PRESSURE_BIN": bin.path]) == bin.path)
    }

    @Test("extract binary lookup prefers public session-pressure over nicos-tools paths")
    func extractBinaryLookupPrefersPublicCLI() {
        let home = "/Users/demo"
        let ordered = NDevPressureClient.binaryCandidates(
            home: home,
            environment: [
                "SESSION_PRESSURE_BIN": "/opt/session-pressure",
                "NDEV_PRESSURE_BIN": "/opt/ndev-pressure",
            ]
        )
        let publicCLI = "\(home)/tools/session-pressure/bin/session-pressure"
        let localPublic = "\(home)/.local/bin/session-pressure"
        #expect(ordered.first == "/opt/session-pressure")
        #expect(ordered.contains(localPublic))
        #expect(ordered.contains(publicCLI))
        if let publicIndex = ordered.firstIndex(of: publicCLI),
           let nicosIndex = ordered.firstIndex(where: { $0.contains("nicos-tools") }) {
            #expect(publicIndex < nicosIndex)
        } else {
            #expect(!ordered.contains(where: { $0.contains("nicos-tools") }))
        }
        #expect(ordered.firstIndex(of: localPublic)! < ordered.firstIndex(of: "\(home)/.local/bin/ndev")!)
    }

    @Test("sanitized environment forwards SessionPressure home and bin")
    func sanitizedEnvironmentForwardsSessionPressureOverrides() {
        let sanitized = NDevPressureClient.sanitizedEnvironment([
            "HOME": "/tmp/home",
            "SESSION_PRESSURE_HOME": "/tmp/sp-home",
            "SESSION_PRESSURE_BIN": "/tmp/session-pressure",
            "NDEV_SESSION_PRESSURE_HOME": "/tmp/ndev-sp-home",
            "SECRET": "nope",
        ])
        #expect(sanitized["SESSION_PRESSURE_HOME"] == "/tmp/sp-home")
        #expect(sanitized["SESSION_PRESSURE_BIN"] == "/tmp/session-pressure")
        #expect(sanitized["NDEV_SESSION_PRESSURE_HOME"] == "/tmp/ndev-sp-home")
        #expect(sanitized["SECRET"] == nil)
    }

    @Test("desktop argv pairing is --json session pressure <leaf> for both binaries")
    func desktopProductCLIArgvPairing() {
        #expect(NDevPressureClient.pairedCLIArguments(["--json", "session", "pressure", "doctor"]) == ["--json", "session", "pressure", "doctor"])
        #expect(NDevPressureClient.pairedCLIArguments(["--json", "doctor"]) == ["--json", "session", "pressure", "doctor"])
        #expect(NDevPressureClient.pairedCLIArguments(["--json", "session", "pressure", "status", "--live"]) == ["--json", "session", "pressure", "status", "--live"])
        #expect(NDevPressureClient.pairedCLIArguments(["status", "--full"]) == ["--json", "session", "pressure", "status", "--full"])
    }

    @Test("resolve binary prefers existing path")
    func resolveBinary() {
        // At least one of the standard candidates usually exists in this environment.
        // The function must not crash and should return an absolute path when found.
        if let path = NDevPressureClient.resolveBinary() {
            #expect(path.hasPrefix("/"))
            #expect(FileManager.default.isExecutableFile(atPath: path))
        }
    }

    @Test("SessionPressure API client accepts loopback only")
    func pressureAPIEndpointScope() {
        #expect(NDevPressureAPIClient(environment: ["NDEV_PRESSURE_API_URL": "http://127.0.0.1:8765"]) != nil)
        #expect(NDevPressureAPIClient(environment: ["NDEV_PRESSURE_API_URL": "https://localhost:8765"]) != nil)
        #expect(NDevPressureAPIClient(environment: ["NDEV_PRESSURE_API_URL": "https://example.com"]) == nil)
    }

    @Test("tooltip copy covers levels and sections")
    func helpCopy() {
        for level in PressureLevel.allCases {
            #expect(!PressureHelp.level(level).isEmpty)
        }
        #expect(PressureHelp.section("Overview").contains("pressure"))
        #expect(PressureHelp.freeMemory.contains("free"))
        #expect(PressureHelp.policyFull.contains("auto-shed") || PressureHelp.policyFull.contains("critical"))
        #expect(PressureHelp.refresh.contains("ndev"))
        #expect(PressureHelp.storage.contains("separate from memory"))
        #expect(PressureHelp.workCapacity.contains("benchmark"))
        #expect(PressureHelp.memoryMomentum.contains("never raises pressure"))
        #expect(PressureHelp.hostConsumers.contains("no PID"))
        #expect(PressureHelp.section("Disk Writes").contains("SSD"))
        #expect(PressureHelp.section("Storage").contains("⌘4"))
        #expect(PressureHelp.section("Idle Cleanup").contains("Idle trees"))
        #expect(PressureHelp.storageBeginSafeReclaim.contains("--auto-safe"))
        #expect(PressureHelp.keyboardRemap.contains("App Shortcuts"))
        #expect(PressureHelp.diskWriterAttribution.contains("all mounted volumes"))
    }

    @Test("CLI capture is time bounded")
    func captureTimeout() {
        #expect(throws: NDevPressureClientError.self) {
            _ = try NDevPressureClient.capture(
                binaryPath: "/bin/sleep",
                environment: ["PATH": "/usr/bin:/bin"],
                arguments: ["5"],
                maxBytes: 1024,
                timeoutSeconds: 0.05
            )
        }
    }

    @Test("CLI capture drains in memory and enforces its byte ceiling")
    func captureByteCeiling() throws {
        let result = try NDevPressureClient.capture(
            binaryPath: "/bin/echo",
            environment: ["PATH": "/usr/bin:/bin"],
            arguments: ["ok"],
            maxBytes: 32,
            timeoutSeconds: 2
        )
        #expect(result.code == 0)
        #expect(result.stdout == "ok\n")

        #expect(throws: NDevPressureClientError.self) {
            _ = try NDevPressureClient.capture(
                binaryPath: "/bin/echo",
                environment: ["PATH": "/usr/bin:/bin"],
                arguments: [String(repeating: "x", count: 2_048)],
                maxBytes: 128,
                timeoutSeconds: 2
            )
        }
    }
}
