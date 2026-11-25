use pressure_core::{
    BoardEnvelope, DoctorWork, PressureBoard, PressureClient, PressureLevel, RecoveryClearEnvelope,
    Section, WorkHeadroomEnvelope, WorkPlacementEnvelope, commands, filter_commands, percent,
    short_id, swap_out_rate,
};

#[test]
fn board_full_fixture_decodes() {
    let raw = include_str!("fixtures/board-full.json");
    let env: BoardEnvelope = serde_json::from_str(raw).expect("board fixture");
    assert!(env.ok || env.snapshot.is_some() || env.work.is_some());
    let board = PressureBoard::from_envelope(env, "session-pressure".into(), true);
    assert!(board.snapshot.physical_memory_mb > 0.0 || board.snapshot.free_percent >= 0);
    assert!(board.work.is_some(), "fixture must carry work");
    let work = board.work.as_ref().unwrap();
    assert!(work.capacity > 0);
}

#[test]
fn command_palette_covers_all_panes() {
    let cmds = commands();
    for title in [
        "Overview",
        "Agent Trees",
        "Disk Writes",
        "Storage",
        "Work Queue",
        "Policy",
        "Monitor",
        "Telemetry",
    ] {
        assert!(
            cmds.iter().any(|c| c.title.contains(title)),
            "missing command for {title}"
        );
    }
    let filtered = filter_commands("work");
    assert!(filtered.iter().any(|c| c.title.to_lowercase().contains("work")));
}

#[test]
fn compact_board_uses_latest_monitor_when_snapshot_omitted() {
    let raw = r#"{
      "ok": true,
      "action": "board",
      "latest_monitor": {
        "level": "warning",
        "free_percent": 49,
        "physical_memory_mb": 16384,
        "host_cpu_percent": 22.5,
        "top_agent_trees": [{"agent": "grok", "root_pid": 1, "rss_sum_mb": 100}]
      },
      "work": {"capacity": 8, "used": 1, "available": 7, "leases": [], "waiters": [], "queue_depth": 0}
    }"#;
    let env: BoardEnvelope = serde_json::from_str(raw).expect("compact board");
    let board = PressureBoard::from_envelope(env, "session-pressure".into(), false);
    assert_eq!(board.snapshot.free_percent, 49);
    assert_eq!(board.snapshot.top_agent_trees.len(), 1);
    assert_eq!(board.work.as_ref().unwrap().capacity, 8);
}

#[test]
fn live_aliases_decode_disk_and_consumer_cpu() {
    let raw = r#"{
      "ok": true,
      "latest_monitor": {
        "level": "normal",
        "free_percent": 44,
        "physical_memory_mb": 16384,
        "disk_write": {
          "state": "normal",
          "current_bytes_per_second": 47893170.7,
          "writer_available_count": 65,
          "confidence": "provisional",
          "bytes_24h": 1122719621120,
          "top_writer": {"executable": "precommithook.te", "bytes_per_second": 0}
        },
        "top_host_consumers": [
          {"executable": "Google_Chrome_He", "rss_sum_mb": 1241, "cpu_percent_sum": 12.5, "process_count": 18}
        ]
      }
    }"#;
    let env: BoardEnvelope = serde_json::from_str(raw).unwrap();
    let board = PressureBoard::from_envelope(env, "x".into(), false);
    let disk = board.snapshot.disk_write.unwrap();
    assert_eq!(disk.level.as_deref(), Some("normal"));
    assert!(disk.bytes_per_second > 1_000_000.0);
    assert_eq!(disk.writer_count, 65);
    assert_eq!(board.snapshot.top_host_consumers[0].cpu_percent, 12.5);
}

#[test]
fn live_cli_board_drains_full_payload() {
    let Ok(client) = PressureClient::resolve() else {
        return;
    };
    let env = client
        .board(false, true, &["doctor", "calibration", "policy", "monitor", "idle", "telemetry"])
        .expect("live board");
    let board = PressureBoard::from_envelope(env, client.binary_path.display().to_string(), false);
    assert!(
        board.snapshot.physical_memory_mb > 0.0 || board.snapshot.free_percent > 0,
        "expected host snapshot via snapshot or latest_monitor"
    );
    let headroom = client.work_headroom().expect("live work headroom");
    let h = headroom.headroom.expect("live headroom payload");
    assert!(h.physical_mb > 0 || h.available_mb > 0);
    let placement = client.work_placement("6h").expect("live work placement");
    let _ = board.alerts(placement.placement.as_ref(), None);
}

#[test]
fn formatters() {
    assert_eq!(percent(51.0), "51%");
    assert_eq!(short_id("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"), "aaaaaaaa…");
    assert_eq!(swap_out_rate(0.5), "0.5 MB/s");
    assert_eq!(swap_out_rate(-2.0), "0.0 MB/s");
}

#[test]
fn resource_admission_work_status_headroom_and_doctor_decode() {
    let raw = r#"{
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
            "pressure_parked": true,
            "predicted_peak_mb": 2048
          }
        ],
        "queue_depth": 1
      }
    }"#;
    let env: BoardEnvelope = serde_json::from_str(raw).expect("resource admission board");
    let work = env.work.expect("work present");
    assert!(work.is_resource_admission());
    assert_eq!(work.admission.as_deref(), Some("resource"));
    assert_eq!(work.parked_weight, 6);
    assert_eq!(work.active_lease_count(), 1);
    assert_eq!(work.parked_lease_count(), 1);
    assert_eq!(work.leases[0].predicted_peak_mb, Some(1400));
    assert_eq!(work.leases[0].predicted_median_mb, Some(900));
    assert!(work.waiters[0].start_expired);
    assert!(work.waiters[0].is_pressure_reserved());
    assert_eq!(work.waiters[0].predicted_peak_mb, Some(2048));

    let headroom_raw = r#"{
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
    }"#;
    let h_env: WorkHeadroomEnvelope = serde_json::from_str(headroom_raw).expect("headroom env");
    assert_eq!(h_env.memory_admission.as_deref(), Some("enforce"));
    let h = h_env.headroom.expect("headroom");
    assert_eq!(h.headroom_mb, 26_656);
    assert!(h.sample_fresh);
    assert!((h.swap_out_mb_per_s - 0.5).abs() < 1e-6);
    assert!(h.used_fraction() > 0.0 && h.used_fraction() < 0.2);

    let clear_env: RecoveryClearEnvelope =
        serde_json::from_str(r#"{"action":"recovery.clear","ok":true,"cleared":true}"#).unwrap();
    assert_eq!(clear_env.ok, Some(true));
    assert_eq!(clear_env.cleared, Some(true));

    let doc_work: DoctorWork =
        serde_json::from_str(r#"{"capacity": 1048576, "used": 10, "queue_depth": 2}"#).unwrap();
    assert!(doc_work.is_resource_admission());
}

#[test]
fn alert_routes_are_not_swapped_and_work_stalls_route_to_work() {
    let board_raw = r#"{
      "ok": true,
      "has_recovery_hint": true,
      "recovery_hint": {"reason": "unclean helper shutdown"},
      "snapshot": {
        "level": "normal",
        "free_percent": 45,
        "physical_memory_mb": 49152,
        "storage": {
          "available": true,
          "level": "warning",
          "free_percent": 9.5,
          "available_bytes": 53687091200,
          "reasons": ["free space below 10%"]
        }
      }
    }"#;
    let placement_raw = r#"{
      "action": "work.placement",
      "ok": true,
      "placement": {
        "window_seconds": 21600,
        "active_stall": {
          "duration_ms": 185000,
          "operation_id": "op-stall-1",
          "class": "build",
          "decision_reason": "memory_headroom_wait"
        },
        "parked_stalls": [
          {
            "operation_id": "op-parked-9",
            "class": "test",
            "pid": 4242,
            "parked_duration_ms": 120000,
            "age_ms": 300000
          }
        ]
      }
    }"#;
    let board = PressureBoard::from_envelope(
        serde_json::from_str(board_raw).unwrap(),
        "session-pressure".into(),
        false,
    );
    let placement: WorkPlacementEnvelope = serde_json::from_str(placement_raw).unwrap();
    let alerts = board.alerts(placement.placement.as_ref(), Some("smartctl timeout"));

    let storage = alerts.iter().find(|a| a.id == "storage-warn").expect("storage alert");
    assert_eq!(storage.section, Section::Storage, "storage alert must route to Storage, not DiskWrites");

    let disk_err = alerts.iter().find(|a| a.id == "disk-health-error").expect("disk health alert");
    assert_eq!(disk_err.section, Section::DiskWrites, "disk health probe error must route to DiskWrites, not Storage");

    let stall = alerts.iter().find(|a| a.id == "work-stall-op-stall-1").expect("work stall alert");
    assert_eq!(stall.section, Section::Work);
    assert!(stall.detail.contains("build stalled 3m"));
    assert!(stall.detail.contains("memory_headroom_wait"));

    let parked = alerts.iter().find(|a| a.id == "work-parked-stall-op-parked-9").expect("parked stall alert");
    assert_eq!(parked.section, Section::Work);
    assert!(parked.detail.contains("pid 4242"));

    let recovery = alerts.iter().find(|a| a.id == "recovery-hint").expect("recovery alert");
    assert_eq!(recovery.section, Section::Monitor);
}

#[test]
fn policy_driven_thresholds_control_metric_levels() {
    let raw = r#"{
      "ok": true,
      "snapshot": {
        "level": "normal",
        "free_percent": 18,
        "host_cpu_percent": 72.0,
        "agent_rss_sum_mb": 6000.0,
        "swap_used_mb": 3000.0,
        "swap_out_mb_per_s": 1.0,
        "swap_thrash_latched": false
      },
      "policy": {
        "enabled": true,
        "enforce_admission": true,
        "profile": "balanced",
        "thresholds": {
          "free_warning_percent": 20,
          "free_red_percent": 12,
          "free_critical_percent": 6,
          "host_cpu_warning_percent": 70.0,
          "host_cpu_red_percent": 90.0,
          "agent_total_warning_mb": 5000.0,
          "agent_total_red_mb": 7000.0,
          "agent_total_critical_mb": 9000.0,
          "swap_warning_mb": 2048.0,
          "swap_red_mb": 4096.0
        },
        "work_limits": {
          "admission": "resource",
          "memory_admission": "enforce",
          "capacity": 1048576
        }
      }
    }"#;
    let mut board = PressureBoard::from_envelope(
        serde_json::from_str(raw).unwrap(),
        "session-pressure".into(),
        false,
    );
    assert_eq!(board.free_memory_level(), PressureLevel::Warning);
    assert_eq!(board.host_cpu_level(), PressureLevel::Warning);
    assert_eq!(board.agent_rss_level(), PressureLevel::Warning);
    assert_eq!(board.agent_rss_ceiling_mb(), 9000.0);
    assert_eq!(board.swap_level(), PressureLevel::Warning);

    // Swap thrash latch immediately escalates swap level to Red.
    board.snapshot.swap_thrash_latched = true;
    assert_eq!(board.swap_level(), PressureLevel::Red);
}
