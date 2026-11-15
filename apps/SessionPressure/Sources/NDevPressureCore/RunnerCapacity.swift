import Foundation

/// `session-pressure remote capacity` (schema 1): per-runner use over a
/// window, kernel state now, NIC history, and typed capacity alerts. The Go
/// CLI owns every SSH and file read; these types only decode its JSON.
public struct RunnerCapacityEnvelope: Decodable, Sendable {
    public let ok: Bool
    public let capacity: RunnerCapacityReport
}

public enum RunnerCapacityWindow: String, CaseIterable, Identifiable, Sendable {
    case sixHours = "6h"
    case day = "24h"
    case week = "7d"
    case fortnight = "14d"
    public var id: String { rawValue }
    public var label: String {
        switch self {
        case .sixHours: "6 hours"
        case .day: "24 hours"
        case .week: "7 days"
        case .fortnight: "14 days"
        }
    }
}

public struct RunnerCapacityReport: Decodable, Sendable {
    public let schemaVersion: Int
    public let capturedAt: Date
    public let window: String
    public let since: Date
    public let bucketSeconds: Int
    public let generation: String?
    public let hosts: [HostCapacity]
    public let alerts: [CapacityAlert]
    public let historyError: String?
    public let error: String?

    enum CodingKeys: String, CodingKey {
        case window, since, generation, hosts, alerts, error
        case schemaVersion = "schema_version"
        case capturedAt = "captured_at"
        case bucketSeconds = "bucket_seconds"
        case historyError = "history_error"
    }
}

public struct HostCapacity: Decodable, Identifiable, Sendable {
    public let host: String
    public let state: String
    public let provider: String
    public let platform: String
    public let heavySlots: Int
    public let lightSlots: Int
    public let reachable: Bool
    public let error: String?
    public let cpus: Int
    public let uptimeSeconds: Double
    public let load1: Double
    public let load5: Double
    public let load15: Double
    public let memTotalBytes: UInt64
    public let memAvailableBytes: UInt64
    public let disk: DiskUse?
    public let network: NetworkUse?
    public let samplesPresent: Bool
    public let usage: UsageSummary
    public let buckets: [UsageBucket]
    public let jobHours: [JobBucket]
    public let topCallers: [UseRow]
    public let topRepos: [UseRow]

    public var id: String { host }
    public var isActive: Bool { state == "active" }
    public var memUsedFraction: Double {
        memTotalBytes == 0 ? 0 : Double(memTotalBytes - min(memAvailableBytes, memTotalBytes)) / Double(memTotalBytes)
    }

    enum CodingKeys: String, CodingKey {
        case host, state, provider, platform, reachable, error, cpus, load1, load5, load15, disk, network, usage, buckets
        case heavySlots = "heavy_slots"
        case lightSlots = "light_slots"
        case uptimeSeconds = "uptime_seconds"
        case memTotalBytes = "mem_total_bytes"
        case memAvailableBytes = "mem_available_bytes"
        case samplesPresent = "samples_present"
        case jobHours = "job_hours"
        case topCallers = "top_callers"
        case topRepos = "top_repos"
    }
}

public struct DiskUse: Decodable, Sendable {
    public let mount: String
    public let totalBytes: UInt64
    public let usedBytes: UInt64
    public let availableBytes: UInt64
    public let usedPct: Double
    enum CodingKeys: String, CodingKey {
        case mount
        case totalBytes = "total_bytes"
        case usedBytes = "used_bytes"
        case availableBytes = "available_bytes"
        case usedPct = "used_pct"
    }
}

public struct TrafficAllowance: Decodable, Sendable {
    /// unmetered, monthly_included, or unknown.
    public let kind: String
    public let basis: String?
    public let includedBytesPerMonth: UInt64?
    public let note: String
    enum CodingKeys: String, CodingKey {
        case kind, basis, note
        case includedBytesPerMonth = "included_bytes_per_month"
    }
}

public struct NetPoint: Decodable, Identifiable, Sendable {
    public let at: Date
    public let rxBps: Double
    public let txBps: Double
    public var id: Date { at }
    enum CodingKeys: String, CodingKey {
        case at
        case rxBps = "rx_bps"
        case txBps = "tx_bps"
    }
}

public struct NetworkUse: Decodable, Sendable {
    public let interface: String
    public let linkMbps: Int?
    public let rxBytesSinceBoot: UInt64
    public let txBytesSinceBoot: UInt64
    public let tailnetRxBytes: UInt64?
    public let tailnetTxBytes: UInt64?
    public let rxBytesPerDay: Double
    public let txBytesPerDay: Double
    public let allowance: TrafficAllowance
    public let projectedMonthBytes: Double
    public let projectedAllowancePct: Double?
    public let peakRxBps: Double?
    public let peakTxBps: Double?
    public let series: [NetPoint]
    public let seriesCoverageSeconds: Int

    enum CodingKeys: String, CodingKey {
        case interface, allowance, series
        case linkMbps = "link_mbps"
        case rxBytesSinceBoot = "rx_bytes_since_boot"
        case txBytesSinceBoot = "tx_bytes_since_boot"
        case tailnetRxBytes = "tailnet_rx_bytes"
        case tailnetTxBytes = "tailnet_tx_bytes"
        case rxBytesPerDay = "rx_bytes_per_day"
        case txBytesPerDay = "tx_bytes_per_day"
        case projectedMonthBytes = "projected_month_bytes"
        case projectedAllowancePct = "projected_allowance_pct"
        case peakRxBps = "peak_rx_bps"
        case peakTxBps = "peak_tx_bps"
        case seriesCoverageSeconds = "series_coverage_seconds"
    }
}

public struct UsageBucket: Decodable, Identifiable, Sendable {
    public let at: Date
    public let samples: Int
    public let cpuAvgPct: Double
    public let cpuPeakPct: Double
    public let iowaitAvgPct: Double
    public let memAvailableMinBytes: UInt64
    public let heavyAvg: Double
    public let heavyMax: Int
    public let lightMax: Int
    public let saturatedSeconds: Double
    public var id: Date { at }
    enum CodingKeys: String, CodingKey {
        case at, samples
        case cpuAvgPct = "cpu_avg_pct"
        case cpuPeakPct = "cpu_peak_pct"
        case iowaitAvgPct = "iowait_avg_pct"
        case memAvailableMinBytes = "mem_available_min_bytes"
        case heavyAvg = "heavy_avg"
        case heavyMax = "heavy_max"
        case lightMax = "light_max"
        case saturatedSeconds = "saturated_seconds"
    }
}

public struct UsageSummary: Decodable, Sendable {
    public let samples: Int
    public let cpuAvgPct: Double?
    public let cpuPeakPct: Double?
    public let iowaitAvgPct: Double?
    public let memAvailableMinBytes: UInt64?
    public let heavyOccupancyPct: Double?
    public let heavyPeak: Int?
    public let saturatedSeconds: Double
    public let jobs: Int
    public let failedJobs: Int
    public let setupFailures: Int
    public let busySeconds: Int64
    enum CodingKeys: String, CodingKey {
        case samples, jobs
        case cpuAvgPct = "cpu_avg_pct"
        case cpuPeakPct = "cpu_peak_pct"
        case iowaitAvgPct = "iowait_avg_pct"
        case memAvailableMinBytes = "mem_available_min_bytes"
        case heavyOccupancyPct = "heavy_occupancy_pct"
        case heavyPeak = "heavy_peak"
        case saturatedSeconds = "saturated_seconds"
        case failedJobs = "failed_jobs"
        case setupFailures = "setup_failures"
        case busySeconds = "busy_seconds"
    }
}

public struct JobBucket: Decodable, Identifiable, Sendable {
    public let at: Date
    public let jobs: Int
    public let failed: Int
    public let setup: Int
    public let busySeconds: Int64
    public var id: Date { at }
    enum CodingKeys: String, CodingKey {
        case at, jobs, failed, setup
        case busySeconds = "busy_seconds"
    }
}

public struct UseRow: Decodable, Identifiable, Sendable {
    public let name: String
    public let jobs: Int
    public let busySeconds: Int64
    public var id: String { name }
    enum CodingKeys: String, CodingKey {
        case name, jobs
        case busySeconds = "busy_seconds"
    }
}

public struct CapacityAlert: Decodable, Identifiable, Sendable {
    /// critical, warning, or info.
    public let severity: String
    public let code: String
    public let host: String
    public let title: String
    public let detail: String
    public let since: Date?
    public let until: Date?
    public var id: String { "\(host)/\(code)/\(title)" }
}

/// Byte formatting in decimal units, matching provider traffic terms.
public enum CapacityFormat {
    public static func bytes(_ value: Double) -> String {
        let units = ["B", "KB", "MB", "GB", "TB"]
        var v = max(0, value)
        var i = 0
        while v >= 1000 && i < units.count - 1 {
            v /= 1000
            i += 1
        }
        return i == 0 ? String(format: "%.0f %@", v, units[i]) : String(format: "%.1f %@", v, units[i])
    }

    public static func rate(_ bytesPerSecond: Double) -> String {
        let bits = max(0, bytesPerSecond) * 8
        if bits >= 1e9 { return String(format: "%.2f Gbit/s", bits / 1e9) }
        if bits >= 1e6 { return String(format: "%.1f Mbit/s", bits / 1e6) }
        if bits >= 1e3 { return String(format: "%.0f kbit/s", bits / 1e3) }
        return String(format: "%.0f bit/s", bits)
    }

    public static func gib(_ value: UInt64) -> String {
        String(format: "%.1f GiB", Double(value) / 1_073_741_824)
    }

    public static func duration(_ seconds: Double) -> String {
        let s = max(0, seconds)
        if s < 60 { return "\(Int(s))s" }
        if s < 3600 { return "\(Int(s / 60))m" }
        if s < 86400 { return String(format: "%.1fh", s / 3600) }
        return String(format: "%.1fd", s / 86400)
    }
}
