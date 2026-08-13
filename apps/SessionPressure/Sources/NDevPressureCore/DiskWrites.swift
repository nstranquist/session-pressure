import Foundation

public enum DiskWriteState: String, Codable, Sendable, CaseIterable, Comparable {
    case disabled
    case unavailable
    case learning
    case normal
    case unusual
    case high
    case unknown

    public init(from decoder: Decoder) throws {
        let container = try decoder.singleValueContainer()
        self = DiskWriteState(rawValue: (try? container.decode(String.self)) ?? "") ?? .unknown
    }

    public func encode(to encoder: Encoder) throws {
        var container = encoder.singleValueContainer()
        try container.encode(rawValue)
    }

    private var rank: Int {
        switch self {
        case .disabled, .unavailable, .unknown: -1
        case .learning: 0
        case .normal: 1
        case .unusual: 2
        case .high: 3
        }
    }

    public static func < (lhs: DiskWriteState, rhs: DiskWriteState) -> Bool {
        lhs.rank < rhs.rank
    }

    public var displayName: String {
        switch self {
        case .disabled: "Disabled"
        case .unavailable: "Unavailable"
        case .learning: "Learning"
        case .normal: "Normal"
        case .unusual: "Unusual"
        case .high: "High"
        case .unknown: "Unknown"
        }
    }
}

public enum DiskWriteConfidence: String, Codable, Sendable, CaseIterable {
    case none
    case learning
    case provisional
    case confident
    case unknown

    public init(from decoder: Decoder) throws {
        let container = try decoder.singleValueContainer()
        self = DiskWriteConfidence(rawValue: (try? container.decode(String.self)) ?? "") ?? .unknown
    }

    public func encode(to encoder: Encoder) throws {
        var container = encoder.singleValueContainer()
        try container.encode(rawValue)
    }

    public var displayName: String {
        switch self {
        case .none: "No baseline"
        case .learning: "Learning"
        case .provisional: "Provisional"
        case .confident: "Confident"
        case .unknown: "Unknown"
        }
    }
}

public struct DiskWriter: Codable, Sendable, Identifiable, Hashable {
    public var executable: String
    public var category: String
    public var processCount: Int
    public var agentProcessCount: Int
    public var windowBytes: UInt64
    public var bytesPerSecond: Double
    public var baselineRatio: Double?
    public var pid: Int?
    public var processStartID: UInt64?
    public var ownerKind: String?
    public var workClass: String?

    public var id: String { "\(executable):\(pid ?? 0):\(processStartID ?? 0)" }

    enum CodingKeys: String, CodingKey {
        case executable, category, pid
        case processCount = "process_count"
        case agentProcessCount = "agent_process_count"
        case windowBytes = "window_bytes"
        case bytesPerSecond = "bytes_per_second"
        case baselineRatio = "baseline_ratio"
        case processStartID = "process_start_id"
        case ownerKind = "owner_kind"
        case workClass = "work_class"
    }

    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        executable = try c.decodeIfPresent(String.self, forKey: .executable) ?? "unknown"
        category = try c.decodeIfPresent(String.self, forKey: .category) ?? "other"
        processCount = try c.decodeIfPresent(Int.self, forKey: .processCount) ?? 0
        agentProcessCount = try c.decodeIfPresent(Int.self, forKey: .agentProcessCount) ?? 0
        windowBytes = try c.decodeIfPresent(UInt64.self, forKey: .windowBytes) ?? 0
        bytesPerSecond = try c.decodeIfPresent(Double.self, forKey: .bytesPerSecond) ?? 0
        baselineRatio = try c.decodeIfPresent(Double.self, forKey: .baselineRatio)
        pid = try c.decodeIfPresent(Int.self, forKey: .pid)
        processStartID = try c.decodeIfPresent(UInt64.self, forKey: .processStartID)
        ownerKind = try c.decodeIfPresent(String.self, forKey: .ownerKind)
        workClass = try c.decodeIfPresent(String.self, forKey: .workClass)
    }
}

public struct DiskWriteSummary: Codable, Sendable, Hashable {
    public var schemaVersion: Int?
    public var modelVersion: String
    public var capturedAt: Date?
    public var state: DiskWriteState
    public var confidence: DiskWriteConfidence
    public var source: String
    public var deviceScope: String
    public var attributionScope: String
    public var context: String
    public var measurementWindowSeconds: Double?
    public var currentBytesPerSecond: Double
    public var window15mBytes: UInt64
    public var bytes24h: UInt64
    public var unscoredGapBytes: UInt64
    public var baselineP99Bytes15m: UInt64
    public var baselineRatio: Double
    public var baselineSamples: UInt64
    public var baselineAgeSeconds: Double?
    public var deviceCount: Int
    public var totalPIDCount: Int
    public var accessiblePIDCount: Int
    public var attributionAvailable: Bool
    public var writerAvailableCount: Int
    public var topWriter: DiskWriter?
    public var reasonCodes: [String]

    public var rollingWindowIncomplete: Bool {
        reasonCodes.contains("rolling_window_incomplete")
    }

    enum CodingKeys: String, CodingKey {
        case schemaVersion = "schema_version"
        case modelVersion = "model_version"
        case capturedAt = "captured_at"
        case state, confidence, source, context
        case deviceScope = "device_scope"
        case attributionScope = "attribution_scope"
        case measurementWindowSeconds = "measurement_window_seconds"
        case currentBytesPerSecond = "current_bytes_per_second"
        case window15mBytes = "window_15m_bytes"
        case bytes24h = "bytes_24h"
        case unscoredGapBytes = "unscored_gap_bytes"
        case baselineP99Bytes15m = "baseline_p99_bytes_15m"
        case baselineRatio = "baseline_ratio"
        case baselineSamples = "baseline_samples"
        case baselineAgeSeconds = "baseline_age_seconds"
        case deviceCount = "device_count"
        case totalPIDCount = "total_pid_count"
        case accessiblePIDCount = "accessible_pid_count"
        case attributionAvailable = "attribution_available"
        case writerAvailableCount = "writer_available_count"
        case topWriter = "top_writer"
        case reasonCodes = "reason_codes"
    }

    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        schemaVersion = try c.decodeIfPresent(Int.self, forKey: .schemaVersion)
        modelVersion = try c.decodeIfPresent(String.self, forKey: .modelVersion) ?? "unknown"
        capturedAt = try c.decodeIfPresent(Date.self, forKey: .capturedAt)
        state = try c.decodeIfPresent(DiskWriteState.self, forKey: .state) ?? .unknown
        confidence = try c.decodeIfPresent(DiskWriteConfidence.self, forKey: .confidence) ?? .unknown
        source = try c.decodeIfPresent(String.self, forKey: .source) ?? "unknown"
        deviceScope = try c.decodeIfPresent(String.self, forKey: .deviceScope) ?? "unknown"
        attributionScope = try c.decodeIfPresent(String.self, forKey: .attributionScope) ?? "unknown"
        context = try c.decodeIfPresent(String.self, forKey: .context) ?? "uncoordinated"
        measurementWindowSeconds = try c.decodeIfPresent(Double.self, forKey: .measurementWindowSeconds)
        currentBytesPerSecond = try c.decodeIfPresent(Double.self, forKey: .currentBytesPerSecond) ?? 0
        window15mBytes = try c.decodeIfPresent(UInt64.self, forKey: .window15mBytes) ?? 0
        bytes24h = try c.decodeIfPresent(UInt64.self, forKey: .bytes24h) ?? 0
        unscoredGapBytes = try c.decodeIfPresent(UInt64.self, forKey: .unscoredGapBytes) ?? 0
        baselineP99Bytes15m = try c.decodeIfPresent(UInt64.self, forKey: .baselineP99Bytes15m) ?? 0
        baselineRatio = try c.decodeIfPresent(Double.self, forKey: .baselineRatio) ?? 0
        baselineSamples = try c.decodeIfPresent(UInt64.self, forKey: .baselineSamples) ?? 0
        baselineAgeSeconds = try c.decodeIfPresent(Double.self, forKey: .baselineAgeSeconds)
        deviceCount = try c.decodeIfPresent(Int.self, forKey: .deviceCount) ?? 0
        totalPIDCount = try c.decodeIfPresent(Int.self, forKey: .totalPIDCount) ?? 0
        accessiblePIDCount = try c.decodeIfPresent(Int.self, forKey: .accessiblePIDCount) ?? 0
        attributionAvailable = try c.decodeIfPresent(Bool.self, forKey: .attributionAvailable) ?? false
        writerAvailableCount = try c.decodeIfPresent(Int.self, forKey: .writerAvailableCount) ?? 0
        topWriter = try c.decodeIfPresent(DiskWriter.self, forKey: .topWriter)
        reasonCodes = try c.decodeIfPresent([String].self, forKey: .reasonCodes) ?? []
    }
}

public struct DiskWritePolicy: Codable, Sendable, Hashable {
    public var enabled: Bool
    public var notificationsEnabled: Bool
    public var sampleIntervalSeconds: Int
    public var baselineRetentionDays: Int
    public var profile: String
    public var traceMaxDurationSeconds: Int

    enum CodingKeys: String, CodingKey {
        case enabled, profile
        case notificationsEnabled = "notifications_enabled"
        case sampleIntervalSeconds = "sample_interval_seconds"
        case baselineRetentionDays = "baseline_retention_days"
        case traceMaxDurationSeconds = "trace_max_duration_seconds"
    }

    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        enabled = try c.decodeIfPresent(Bool.self, forKey: .enabled) ?? false
        notificationsEnabled = try c.decodeIfPresent(Bool.self, forKey: .notificationsEnabled) ?? false
        sampleIntervalSeconds = try c.decodeIfPresent(Int.self, forKey: .sampleIntervalSeconds) ?? 15
        baselineRetentionDays = try c.decodeIfPresent(Int.self, forKey: .baselineRetentionDays) ?? 14
        profile = try c.decodeIfPresent(String.self, forKey: .profile) ?? "quiet-adaptive-v1"
        traceMaxDurationSeconds = try c.decodeIfPresent(Int.self, forKey: .traceMaxDurationSeconds) ?? 30
    }
}

public struct DiskWriteReport: Codable, Sendable, Hashable {
    public var summary: DiskWriteSummary
    public var writers: [DiskWriter]
    public var availableCount: Int
    public var returnedCount: Int
    public var truncated: Bool

    enum CodingKeys: String, CodingKey {
        case summary, writers, truncated
        case availableCount = "available_count"
        case returnedCount = "returned_count"
    }
}

public struct DiskWriteHistoryPoint: Codable, Sendable, Identifiable, Hashable {
    public var hour: Date
    public var state: DiskWriteState?
    public var bytesWritten: UInt64
    public var unscoredGapBytes: UInt64
    public var baselineP99Bytes: UInt64
    public var sampleCount: UInt64

    public var id: Date { hour }

    enum CodingKeys: String, CodingKey {
        case hour, state
        case bytesWritten = "bytes_written"
        case unscoredGapBytes = "unscored_gap_bytes"
        case baselineP99Bytes = "baseline_p99_bytes"
        case sampleCount = "sample_count"
    }

    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        hour = try c.decode(Date.self, forKey: .hour)
        state = try c.decodeIfPresent(DiskWriteState.self, forKey: .state)
        bytesWritten = try c.decodeIfPresent(UInt64.self, forKey: .bytesWritten) ?? 0
        unscoredGapBytes = try c.decodeIfPresent(UInt64.self, forKey: .unscoredGapBytes) ?? 0
        baselineP99Bytes = try c.decodeIfPresent(UInt64.self, forKey: .baselineP99Bytes) ?? 0
        sampleCount = try c.decodeIfPresent(UInt64.self, forKey: .sampleCount) ?? 0
    }
}

public struct DiskWriteStatusEnvelope: Codable, Sendable {
    public var ok: Bool?
    public var action: String?
    public var outputScope: String?
    public var summary: DiskWriteSummary?
    public var report: DiskWriteReport?
    public var diskWritePolicy: DiskWritePolicy?

    enum CodingKeys: String, CodingKey {
        case ok, action, summary, report
        case outputScope = "output_scope"
        case diskWritePolicy = "disk_write_policy"
    }
}

public struct DiskWriteTopEnvelope: Codable, Sendable {
    public var ok: Bool?
    public var action: String?
    public var outputScope: String?
    public var capturedAt: Date?
    public var deviceScope: String?
    public var attributionScope: String?
    public var writers: [DiskWriter]
    public var availableCount: Int
    public var returnedCount: Int
    public var truncated: Bool

    enum CodingKeys: String, CodingKey {
        case ok, action, writers, truncated
        case outputScope = "output_scope"
        case capturedAt = "captured_at"
        case deviceScope = "device_scope"
        case attributionScope = "attribution_scope"
        case availableCount = "available_count"
        case returnedCount = "returned_count"
    }

    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        ok = try c.decodeIfPresent(Bool.self, forKey: .ok)
        action = try c.decodeIfPresent(String.self, forKey: .action)
        outputScope = try c.decodeIfPresent(String.self, forKey: .outputScope)
        capturedAt = try c.decodeIfPresent(Date.self, forKey: .capturedAt)
        deviceScope = try c.decodeIfPresent(String.self, forKey: .deviceScope)
        attributionScope = try c.decodeIfPresent(String.self, forKey: .attributionScope)
        writers = try c.decodeIfPresent([DiskWriter].self, forKey: .writers) ?? []
        availableCount = try c.decodeIfPresent(Int.self, forKey: .availableCount) ?? writers.count
        returnedCount = try c.decodeIfPresent(Int.self, forKey: .returnedCount) ?? writers.count
        truncated = try c.decodeIfPresent(Bool.self, forKey: .truncated) ?? false
    }
}

public struct DiskWriteHistoryEnvelope: Codable, Sendable {
    public var ok: Bool?
    public var action: String?
    public var since: Date?
    public var history: [DiskWriteHistoryPoint]
    public var availableCount: Int
    public var returnedCount: Int
    public var truncated: Bool

    enum CodingKeys: String, CodingKey {
        case ok, action, since, history, truncated
        case availableCount = "available_count"
        case returnedCount = "returned_count"
    }

    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        ok = try c.decodeIfPresent(Bool.self, forKey: .ok)
        action = try c.decodeIfPresent(String.self, forKey: .action)
        since = try c.decodeIfPresent(Date.self, forKey: .since)
        history = try c.decodeIfPresent([DiskWriteHistoryPoint].self, forKey: .history) ?? []
        availableCount = try c.decodeIfPresent(Int.self, forKey: .availableCount) ?? history.count
        returnedCount = try c.decodeIfPresent(Int.self, forKey: .returnedCount) ?? history.count
        truncated = try c.decodeIfPresent(Bool.self, forKey: .truncated) ?? false
    }
}

public struct DiskWritePolicyEnvelope: Codable, Sendable {
    public var ok: Bool?
    public var action: String?
    public var diskWritePolicy: DiskWritePolicy?
    public var path: String?
    public var persisted: Bool?

    enum CodingKeys: String, CodingKey {
        case ok, action, path, persisted
        case diskWritePolicy = "disk_write_policy"
    }
}

public struct DiskWriteTraceEnvelope: Codable, Sendable {
    public var ok: Bool?
    public var action: String?
    public var status: String?
    public var pid: Int?
    public var processStartIdentity: String?
    public var durationSeconds: Int?
    public var deepLink: String?
    public var opened: Bool?

    enum CodingKeys: String, CodingKey {
        case ok, action, status, pid, opened
        case processStartIdentity = "process_start_identity"
        case durationSeconds = "duration_seconds"
        case deepLink = "deep_link"
    }
}
