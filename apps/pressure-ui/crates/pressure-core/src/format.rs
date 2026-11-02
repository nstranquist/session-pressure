pub fn percent(value: impl Into<f64>) -> String {
    format!("{:.0}%", value.into())
}

pub fn bytes(value: i64) -> String {
    const KIB: f64 = 1024.0;
    const MIB: f64 = 1024.0 * 1024.0;
    const GIB: f64 = 1024.0 * 1024.0 * 1024.0;
    let v = value as f64;
    if v.abs() >= GIB {
        format!("{:.1} GiB", v / GIB)
    } else if v.abs() >= MIB {
        format!("{:.1} MiB", v / MIB)
    } else if v.abs() >= KIB {
        format!("{:.0} KiB", v / KIB)
    } else {
        format!("{value} B")
    }
}

pub fn duration_ms(ms: i64) -> String {
    if ms < 1000 {
        return format!("{ms}ms");
    }
    let s = ms / 1000;
    if s < 60 {
        return format!("{s}s");
    }
    if s < 3600 {
        return format!("{}m", s / 60);
    }
    if s < 86_400 {
        let h = s / 3600;
        let m = (s % 3600) / 60;
        if m == 0 {
            format!("{h}h")
        } else {
            format!("{h}h {m}m")
        }
    } else {
        format!("{}d", s / 86_400)
    }
}

pub fn short_id(id: &str) -> String {
    if id.len() <= 10 {
        id.to_string()
    } else {
        format!("{}…", &id[..8])
    }
}

pub fn relative_hint(ts: Option<&str>) -> String {
    ts.filter(|s| !s.is_empty()).unwrap_or("—").to_string()
}

pub fn rate_bps(bps: f64) -> String {
    if bps >= 1_000_000_000.0 {
        format!("{:.1} GB/s", bps / 1_000_000_000.0)
    } else if bps >= 1_000_000.0 {
        format!("{:.1} MB/s", bps / 1_000_000.0)
    } else if bps >= 1_000.0 {
        format!("{:.0} KB/s", bps / 1_000.0)
    } else {
        format!("{:.0} B/s", bps)
    }
}

pub fn mb(value: f64) -> String {
    if value >= 1024.0 {
        format!("{:.1} GiB", value / 1024.0)
    } else {
        format!("{:.0} MiB", value)
    }
}
