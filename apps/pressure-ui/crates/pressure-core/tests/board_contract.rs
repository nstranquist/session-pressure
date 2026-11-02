use pressure_core::{
    BoardEnvelope, PressureBoard, PressureClient, commands, filter_commands, percent, short_id,
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
}

#[test]
fn formatters() {
    assert_eq!(percent(51.0), "51%");
    assert_eq!(short_id("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"), "aaaaaaaa…");
}
