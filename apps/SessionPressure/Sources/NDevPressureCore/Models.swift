import Foundation

// MARK: - Pressure level

public enum PressureLevel: String, Codable, Sendable, CaseIterable, Comparable {
    case normal
    case warning
    case red
    case critical
    case unknown

    public init(raw: String?) {
        self = PressureLevel(rawValue: (raw ?? "").lowercased()) ?? .unknown
    }

    private var rank: Int {
        switch self {
        case .normal: 0
        case .warning: 1
        case .red: 2
        case .critical: 3
        case .unknown: -1
        }
    }

    public static func < (lhs: PressureLevel, rhs: PressureLevel) -> Bool {
        lhs.rank < rhs.rank
    }

    public var displayName: String {
        switch self {
        case .normal: "Normal"
        case .warning: "Warning"
        case .red: "Red"
        case .critical: "Critical"
        case .unknown: "Unknown"
        }
    }

    public var shortLabel: String {
        switch self {
        case .normal: "OK"
        case .warning: "WARN"
        case .red: "RED"
        case .critical: "CRIT"
        case .unknown: "—"
        }
    }
}

public enum MemoryMomentum: String, Codable, Sendable, CaseIterable {
    case unknown
    case steady
    case declining
    case rapidDecline = "rapid_decline"
    case recovering

    public init(from decoder: Decoder) throws {
        let container = try decoder.singleValueContainer()
        self = MemoryMomentum(rawValue: (try? container.decode(String.self)) ?? "") ?? .unknown
    }

    public func encode(to encoder: Encoder) throws {
        var container = encoder.singleValueContainer()
        try container.encode(rawValue)
    }

    public var displayName: String {
        switch self {
        case .unknown: "Learning"
        case .steady: "Steady"
        case .declining: "Declining"
        case .rapidDecline: "Rapid decline"
        case .recovering: "Recovering"
        }
    }
}

// MARK: - Agent tree

public enum SemanticState: String, Codable, Sendable, CaseIterable {
    case ready
    case busy
    case unknown

    public var displayName: String {
        switch self {
        case .ready: "Ready"
        case .busy: "Busy"
        case .unknown: "Unknown"
        }
    }
}

public struct AgentTree: Codable, Sendable, Identifiable, Hashable {
    public var agent: String
    public var rootPID: Int
    public var sessionID: String?
    public var executable: String
    public var processCount: Int
    public var rssSumMB: Double
    public var cpuPercentSum: Double
    public var cpuAvailable: Bool
    public var elapsedSeconds: Int64?
    public var quiescentSamples: Int?
    public var semanticState: SemanticState?
    public var semanticStateAt: Date?

    public var id: String { "\(agent)-\(rootPID)" }

    enum CodingKeys: String, CodingKey {
        case agent
        case rootPID = "root_pid"
        case sessionID = "session_id"
        case executable
        case processCount = "process_count"
        case rssSumMB = "rss_sum_mb"
        case cpuPercentSum = "cpu_percent_sum"
        case cpuAvailable = "cpu_available"
        case elapsedSeconds = "elapsed_seconds"
        case quiescentSamples = "quiescent_samples"
        case semanticState = "semantic_state"
        case semanticStateAt = "semantic_state_at"
    }

    public init(
        agent: String,
        rootPID: Int,
        sessionID: String? = nil,
        executable: String,
        processCount: Int,
        rssSumMB: Double,
        cpuPercentSum: Double,
        cpuAvailable: Bool = false,
        elapsedSeconds: Int64? = nil,
        quiescentSamples: Int? = nil,
        semanticState: SemanticState? = nil,
        semanticStateAt: Date? = nil
    ) {
        self.agent = agent
        self.rootPID = rootPID
        self.sessionID = sessionID
        self.executable = executable
        self.processCount = processCount
        self.rssSumMB = rssSumMB
        self.cpuPercentSum = cpuPercentSum
        self.cpuAvailable = cpuAvailable
        self.elapsedSeconds = elapsedSeconds
        self.quiescentSamples = quiescentSamples
        self.semanticState = semanticState
        self.semanticStateAt = semanticStateAt
    }

    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        agent = try c.decode(String.self, forKey: .agent)
        rootPID = try c.decode(Int.self, forKey: .rootPID)
        sessionID = try c.decodeIfPresent(String.self, forKey: .sessionID)
        executable = try c.decode(String.self, forKey: .executable)
        processCount = try c.decode(Int.self, forKey: .processCount)
        rssSumMB = try c.decode(Double.self, forKey: .rssSumMB)
        cpuPercentSum = try c.decode(Double.self, forKey: .cpuPercentSum)
        cpuAvailable = try c.decodeIfPresent(Bool.self, forKey: .cpuAvailable) ?? false
        elapsedSeconds = try c.decodeIfPresent(Int64.self, forKey: .elapsedSeconds)
        quiescentSamples = try c.decodeIfPresent(Int.self, forKey: .quiescentSamples)
        semanticState = try c.decodeIfPresent(SemanticState.self, forKey: .semanticState)
        semanticStateAt = try c.decodeIfPresent(Date.self, forKey: .semanticStateAt)
    }
}

public struct HostConsumer: Codable, Sendable, Identifiable, Hashable {
    public var executable: String
    public var category: String
    public var processCount: Int
    public var rssSumMB: Double
    public var cpuPercentSum: Double
    public var cpuAvailable: Bool
    public var agentProcessCount: Int

    public var id: String { "\(category)-\(executable)" }

    enum CodingKeys: String, CodingKey {
        case executable, category
        case processCount = "process_count"
        case rssSumMB = "rss_sum_mb"
        case cpuPercentSum = "cpu_percent_sum"
        case cpuAvailable = "cpu_available"
        case agentProcessCount = "agent_process_count"
    }

    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        executable = try c.decodeIfPresent(String.self, forKey: .executable) ?? "unknown"
        category = try c.decodeIfPresent(String.self, forKey: .category) ?? "other"
        processCount = try c.decodeIfPresent(Int.self, forKey: .processCount) ?? 0
        rssSumMB = try c.decodeIfPresent(Double.self, forKey: .rssSumMB) ?? 0
        cpuPercentSum = try c.decodeIfPresent(Double.self, forKey: .cpuPercentSum) ?? 0
        cpuAvailable = try c.decodeIfPresent(Bool.self, forKey: .cpuAvailable) ?? false
        agentProcessCount = try c.decodeIfPresent(Int.self, forKey: .agentProcessCount) ?? 0
    }
}

// MARK: - Snapshot

public struct StorageSnapshot: Codable, Sendable, Hashable {
    public var available: Bool
    public var volumePath: String
    public var source: String?
    public var capturedAt: Date?
    public var totalBytes: Int64
    public var freeBytes: Int64
    public var availableBytes: Int64
    public var freePercent: Double
    public var level: PressureLevel
    public var reasons: [String]
    public var error: String?

    enum CodingKeys: String, CodingKey {
        case available
        case volumePath = "volume_path"
        case source
        case capturedAt = "captured_at"
        case totalBytes = "total_bytes"
        case freeBytes = "free_bytes"
        case availableBytes = "available_bytes"
        case freePercent = "free_percent"
        case level
        case reasons
        case error
    }

    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        available = try c.decodeIfPresent(Bool.self, forKey: .available) ?? false
        volumePath = try c.decodeIfPresent(String.self, forKey: .volumePath) ?? ""
        source = try c.decodeIfPresent(String.self, forKey: .source)
        capturedAt = try c.decodeIfPresent(Date.self, forKey: .capturedAt)
        totalBytes = try c.decodeIfPresent(Int64.self, forKey: .totalBytes) ?? 0
        freeBytes = try c.decodeIfPresent(Int64.self, forKey: .freeBytes) ?? 0
        availableBytes = try c.decodeIfPresent(Int64.self, forKey: .availableBytes) ?? 0
        freePercent = try c.decodeIfPresent(Double.self, forKey: .freePercent) ?? 0
        level = PressureLevel(raw: try c.decodeIfPresent(String.self, forKey: .level))
        reasons = try c.decodeIfPresent([String].self, forKey: .reasons) ?? []
        error = try c.decodeIfPresent(String.self, forKey: .error)
    }

    public init(available: Bool = false, volumePath: String = "", availableBytes: Int64 = 0, totalBytes: Int64 = 0, level: PressureLevel = .unknown) {
        self.available = available
        self.volumePath = volumePath
        self.totalBytes = totalBytes
        self.freeBytes = 0
        self.availableBytes = availableBytes
        self.freePercent = totalBytes > 0 ? 100 * Double(availableBytes) / Double(totalBytes) : 0
        self.level = level
        self.reasons = []
    }
}

public struct PressureSnapshot: Codable, Sendable, Hashable {
    public var schemaVersion: Int?
    public var timestamp: Date?
    public var level: PressureLevel
    public var reasons: [String]
    public var freePercent: Int
    public var physicalMemoryMB: Double
    public var swapUsedMB: Double
    public var logicalCPUCount: Int
    public var hostCPUPercent: Double
    public var hostCPUAvailable: Bool
    public var hostCPUSource: String?
    public var thermalState: String?
    public var thermalAvailable: Bool
    public var lowPowerMode: Bool
    public var lowPowerModeAvailable: Bool
    public var agentCPUPercent: Double
    public var agentCPUAvailable: Bool
    public var processCount: Int
    public var processRSSSumMB: Double
    public var agentTreeCount: Int
    public var agentRSSSumMB: Double
    public var memoryMomentum: MemoryMomentum
    public var freePercentSlopePerMinute: Double
    public var minutesToMemoryRed: Double?
    public var memoryMomentumSampleCount: Int
    public var processInventoryAvailable: Bool
    public var processInventoryFresh: Bool
    public var processInventoryCapturedAt: Date?
    public var processInventoryAgeSeconds: Double?
    public var processInventorySource: String?
    public var processInventoryError: String?
    public var guardPID: Int
    public var guardBinarySHA256: String?
    public var guardRSSMB: Double
    public var guardPeakRSSMB: Double?
    public var guardCPUPercent: Double
    public var guardRole: String?
    public var guardBudgetApplicable: Bool
    public var guardBaselineProven: Bool
    public var monitorSamples: Int?
    public var normalMonitorSamples: Int?
    public var sampleDurationMS: Double
    public var sampleDurationP95MS: Double?
    public var sampleCPUTimeMS: Double
    public var sampleCPUTimeP95MS: Double?
    public var observedIntervalSeconds: Double?
    public var guardCPUDutyPercent: Double?
    public var guardIdleCPUDutyPercent: Double?
    public var telemetryBytesToday: Int64?
    public var telemetryProjectedBytesPerDay: Int64?
    public var guardBudgetOK: Bool
    public var guardBudgetReasons: [String]
    public var topHostConsumers: [HostConsumer]
    public var topAgentTrees: [AgentTree]
    public var policySource: String?
    public var storage: StorageSnapshot
    public var diskWrite: DiskWriteSummary?

    enum CodingKeys: String, CodingKey {
        case schemaVersion = "schema_version"
        case timestamp
        case level
        case reasons
        case freePercent = "free_percent"
        case physicalMemoryMB = "physical_memory_mb"
        case swapUsedMB = "swap_used_mb"
        case logicalCPUCount = "logical_cpu_count"
        case hostCPUPercent = "host_cpu_percent"
        case hostCPUAvailable = "host_cpu_available"
        case hostCPUSource = "host_cpu_source"
        case thermalState = "thermal_state"
        case thermalAvailable = "thermal_available"
        case lowPowerMode = "low_power_mode"
        case lowPowerModeAvailable = "low_power_mode_available"
        case agentCPUPercent = "agent_cpu_percent"
        case agentCPUAvailable = "agent_cpu_available"
        case processCount = "process_count"
        case processRSSSumMB = "process_rss_sum_mb"
        case agentTreeCount = "agent_tree_count"
        case agentRSSSumMB = "agent_rss_sum_mb"
        case memoryMomentum = "memory_momentum"
        case freePercentSlopePerMinute = "free_percent_slope_per_minute"
        case minutesToMemoryRed = "minutes_to_memory_red"
        case memoryMomentumSampleCount = "memory_momentum_sample_count"
        case processInventoryAvailable = "process_inventory_available"
        case processInventoryFresh = "process_inventory_fresh"
        case processInventoryCapturedAt = "process_inventory_captured_at"
        case processInventoryAgeSeconds = "process_inventory_age_seconds"
        case processInventorySource = "process_inventory_source"
        case processInventoryError = "process_inventory_error"
        case guardPID = "guard_pid"
        case guardBinarySHA256 = "guard_binary_sha256"
        case guardRSSMB = "guard_rss_mb"
        case guardPeakRSSMB = "guard_peak_rss_mb"
        case guardCPUPercent = "guard_cpu_percent"
        case guardRole = "guard_role"
        case guardBudgetApplicable = "guard_budget_applicable"
        case guardBaselineProven = "guard_baseline_proven"
        case monitorSamples = "monitor_samples"
        case normalMonitorSamples = "normal_monitor_samples"
        case sampleDurationMS = "sample_duration_ms"
        case sampleDurationP95MS = "sample_duration_p95_ms"
        case sampleCPUTimeMS = "sample_cpu_time_ms"
        case sampleCPUTimeP95MS = "sample_cpu_time_p95_ms"
        case observedIntervalSeconds = "observed_interval_seconds"
        case guardCPUDutyPercent = "guard_cpu_duty_percent"
        case guardIdleCPUDutyPercent = "guard_idle_cpu_duty_percent"
        case telemetryBytesToday = "telemetry_bytes_today"
        case telemetryProjectedBytesPerDay = "telemetry_projected_bytes_per_day"
        case guardBudgetOK = "guard_budget_ok"
        case guardBudgetReasons = "guard_budget_reasons"
        case topHostConsumers = "top_host_consumers"
        case topAgentTrees = "top_agent_trees"
        case policySource = "policy_source"
        case storage
        case diskWrite = "disk_write"
    }

    private enum CompactCodingKeys: String, CodingKey {
        case sampleRole = "sample_role"
        case selfRSSMB = "self_rss_mb"
        case selfCPUPercent = "self_cpu_percent"
        case selfBudgetApplicable = "self_budget_applicable"
    }

    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        let compact = try decoder.container(keyedBy: CompactCodingKeys.self)
        schemaVersion = try c.decodeIfPresent(Int.self, forKey: .schemaVersion)
        timestamp = try c.decodeIfPresent(Date.self, forKey: .timestamp)
        level = PressureLevel(raw: try c.decodeIfPresent(String.self, forKey: .level))
        reasons = try c.decodeIfPresent([String].self, forKey: .reasons) ?? []
        freePercent = try c.decodeIfPresent(Int.self, forKey: .freePercent) ?? 0
        physicalMemoryMB = try c.decodeIfPresent(Double.self, forKey: .physicalMemoryMB) ?? 0
        swapUsedMB = try c.decodeIfPresent(Double.self, forKey: .swapUsedMB) ?? 0
        logicalCPUCount = try c.decodeIfPresent(Int.self, forKey: .logicalCPUCount) ?? 0
        hostCPUPercent = try c.decodeIfPresent(Double.self, forKey: .hostCPUPercent) ?? 0
        hostCPUAvailable = try c.decodeIfPresent(Bool.self, forKey: .hostCPUAvailable) ?? false
        hostCPUSource = try c.decodeIfPresent(String.self, forKey: .hostCPUSource)
        thermalState = try c.decodeIfPresent(String.self, forKey: .thermalState)
        thermalAvailable = try c.decodeIfPresent(Bool.self, forKey: .thermalAvailable) ?? false
        lowPowerMode = try c.decodeIfPresent(Bool.self, forKey: .lowPowerMode) ?? false
        lowPowerModeAvailable = try c.decodeIfPresent(Bool.self, forKey: .lowPowerModeAvailable) ?? false
        agentCPUPercent = try c.decodeIfPresent(Double.self, forKey: .agentCPUPercent) ?? 0
        agentCPUAvailable = try c.decodeIfPresent(Bool.self, forKey: .agentCPUAvailable) ?? false
        processCount = try c.decodeIfPresent(Int.self, forKey: .processCount) ?? 0
        processRSSSumMB = try c.decodeIfPresent(Double.self, forKey: .processRSSSumMB) ?? 0
        agentTreeCount = try c.decodeIfPresent(Int.self, forKey: .agentTreeCount) ?? 0
        agentRSSSumMB = try c.decodeIfPresent(Double.self, forKey: .agentRSSSumMB) ?? 0
        memoryMomentum = try c.decodeIfPresent(MemoryMomentum.self, forKey: .memoryMomentum) ?? .unknown
        freePercentSlopePerMinute = try c.decodeIfPresent(Double.self, forKey: .freePercentSlopePerMinute) ?? 0
        minutesToMemoryRed = try c.decodeIfPresent(Double.self, forKey: .minutesToMemoryRed)
        memoryMomentumSampleCount = try c.decodeIfPresent(Int.self, forKey: .memoryMomentumSampleCount) ?? 0
        processInventoryAvailable = try c.decodeIfPresent(Bool.self, forKey: .processInventoryAvailable) ?? false
        processInventoryFresh = try c.decodeIfPresent(Bool.self, forKey: .processInventoryFresh) ?? false
        processInventoryCapturedAt = try c.decodeIfPresent(Date.self, forKey: .processInventoryCapturedAt)
        processInventoryAgeSeconds = try c.decodeIfPresent(Double.self, forKey: .processInventoryAgeSeconds)
        processInventorySource = try c.decodeIfPresent(String.self, forKey: .processInventorySource)
        processInventoryError = try c.decodeIfPresent(String.self, forKey: .processInventoryError)
        guardPID = try c.decodeIfPresent(Int.self, forKey: .guardPID) ?? 0
        guardBinarySHA256 = try c.decodeIfPresent(String.self, forKey: .guardBinarySHA256)
        guardRSSMB = try c.decodeIfPresent(Double.self, forKey: .guardRSSMB) ?? compact.decodeIfPresent(Double.self, forKey: .selfRSSMB) ?? 0
        guardPeakRSSMB = try c.decodeIfPresent(Double.self, forKey: .guardPeakRSSMB)
        guardCPUPercent = try c.decodeIfPresent(Double.self, forKey: .guardCPUPercent) ?? compact.decodeIfPresent(Double.self, forKey: .selfCPUPercent) ?? 0
        guardRole = try c.decodeIfPresent(String.self, forKey: .guardRole) ?? compact.decodeIfPresent(String.self, forKey: .sampleRole)
        guardBudgetApplicable = try c.decodeIfPresent(Bool.self, forKey: .guardBudgetApplicable) ?? compact.decodeIfPresent(Bool.self, forKey: .selfBudgetApplicable) ?? false
        guardBaselineProven = try c.decodeIfPresent(Bool.self, forKey: .guardBaselineProven) ?? false
        monitorSamples = try c.decodeIfPresent(Int.self, forKey: .monitorSamples)
        normalMonitorSamples = try c.decodeIfPresent(Int.self, forKey: .normalMonitorSamples)
        sampleDurationMS = try c.decodeIfPresent(Double.self, forKey: .sampleDurationMS) ?? 0
        sampleDurationP95MS = try c.decodeIfPresent(Double.self, forKey: .sampleDurationP95MS)
        sampleCPUTimeMS = try c.decodeIfPresent(Double.self, forKey: .sampleCPUTimeMS) ?? 0
        sampleCPUTimeP95MS = try c.decodeIfPresent(Double.self, forKey: .sampleCPUTimeP95MS)
        observedIntervalSeconds = try c.decodeIfPresent(Double.self, forKey: .observedIntervalSeconds)
        guardCPUDutyPercent = try c.decodeIfPresent(Double.self, forKey: .guardCPUDutyPercent)
        guardIdleCPUDutyPercent = try c.decodeIfPresent(Double.self, forKey: .guardIdleCPUDutyPercent)
        telemetryBytesToday = try c.decodeIfPresent(Int64.self, forKey: .telemetryBytesToday)
        telemetryProjectedBytesPerDay = try c.decodeIfPresent(Int64.self, forKey: .telemetryProjectedBytesPerDay)
        guardBudgetOK = try c.decodeIfPresent(Bool.self, forKey: .guardBudgetOK) ?? true
        guardBudgetReasons = try c.decodeIfPresent([String].self, forKey: .guardBudgetReasons) ?? []
        topHostConsumers = try c.decodeIfPresent([HostConsumer].self, forKey: .topHostConsumers) ?? []
        topAgentTrees = try c.decodeIfPresent([AgentTree].self, forKey: .topAgentTrees) ?? []
        policySource = try c.decodeIfPresent(String.self, forKey: .policySource)
        storage = try c.decodeIfPresent(StorageSnapshot.self, forKey: .storage) ?? StorageSnapshot()
        diskWrite = try c.decodeIfPresent(DiskWriteSummary.self, forKey: .diskWrite)
    }

    public init(
        level: PressureLevel = .unknown,
        freePercent: Int = 0,
        physicalMemoryMB: Double = 0,
        swapUsedMB: Double = 0,
        hostCPUPercent: Double = 0,
        agentTreeCount: Int = 0,
        agentRSSSumMB: Double = 0,
        topAgentTrees: [AgentTree] = [],
        reasons: [String] = [],
        guardBudgetOK: Bool = true
    ) {
        self.level = level
        self.reasons = reasons
        self.freePercent = freePercent
        self.physicalMemoryMB = physicalMemoryMB
        self.swapUsedMB = swapUsedMB
        self.logicalCPUCount = 0
        self.hostCPUPercent = hostCPUPercent
        self.hostCPUAvailable = true
        self.thermalState = nil
        self.thermalAvailable = false
        self.lowPowerMode = false
        self.lowPowerModeAvailable = false
        self.agentCPUPercent = 0
        self.agentCPUAvailable = false
        self.processCount = 0
        self.processRSSSumMB = 0
        self.agentTreeCount = agentTreeCount
        self.agentRSSSumMB = agentRSSSumMB
        self.memoryMomentum = .unknown
        self.freePercentSlopePerMinute = 0
        self.memoryMomentumSampleCount = 0
        self.processInventoryAvailable = false
        self.processInventoryFresh = false
        self.guardPID = 0
        self.guardRSSMB = 0
        self.guardCPUPercent = 0
        self.guardBudgetApplicable = false
        self.guardBaselineProven = false
        self.sampleDurationMS = 0
        self.sampleCPUTimeMS = 0
        self.guardBudgetOK = guardBudgetOK
        self.guardBudgetReasons = []
        self.topHostConsumers = []
        self.topAgentTrees = topAgentTrees
        self.storage = StorageSnapshot()
        self.diskWrite = nil
    }
}

public extension PressureSnapshot {
    /// Overlay the fields present in `pressure status` compact projections while
    /// preserving inactive-pane inventories from the last full hydration.
    func applyingCompact(_ compact: PressureSnapshot) -> PressureSnapshot {
        var result = self
        result.timestamp = compact.timestamp
        result.level = compact.level
        result.reasons = compact.reasons
        result.freePercent = compact.freePercent
        result.swapUsedMB = compact.swapUsedMB
        result.hostCPUPercent = compact.hostCPUPercent
        result.agentTreeCount = compact.agentTreeCount
        result.agentRSSSumMB = compact.agentRSSSumMB
        result.memoryMomentum = compact.memoryMomentum
        result.guardRSSMB = compact.guardRSSMB
        result.guardCPUPercent = compact.guardCPUPercent
        result.guardRole = compact.guardRole
        result.guardBudgetApplicable = compact.guardBudgetApplicable
        result.guardBudgetOK = compact.guardBudgetOK
        result.guardBudgetReasons = compact.guardBudgetReasons
        result.monitorSamples = compact.monitorSamples
        result.normalMonitorSamples = compact.normalMonitorSamples
        result.telemetryBytesToday = compact.telemetryBytesToday
        result.telemetryProjectedBytesPerDay = compact.telemetryProjectedBytesPerDay
        if let diskWrite = compact.diskWrite {
            result.diskWrite = diskWrite
        }
        return result
    }
}

// MARK: - Envelopes

public struct StatusHealth: Codable, Sendable, Hashable {
    public var monitorHealthy: Bool
    public var dailyDriverReady: Bool
    public var operatorReady: Bool
    public var protectionMode: String
    public var latestMonitorFresh: Bool
    public var latestMonitorAgeSeconds: Double?
    public var latestMonitorMaxAgeSeconds: Double?
    public var residentSamples: Int?
    public var residentNormalSamples: Int?
    public var requiredNormalSamples: Int?
    public var healthReasons: [String]
    public var dailyDriverReasons: [String]
    public var operatorReasons: [String]

    enum CodingKeys: String, CodingKey {
        case monitorHealthy = "monitor_healthy"
        case dailyDriverReady = "daily_driver_ready"
        case operatorReady = "operator_ready"
        case protectionMode = "protection_mode"
        case latestMonitorFresh = "latest_monitor_fresh"
        case latestMonitorAgeSeconds = "latest_monitor_age_seconds"
        case latestMonitorMaxAgeSeconds = "latest_monitor_max_age_seconds"
        case residentSamples = "resident_samples"
        case residentNormalSamples = "resident_normal_samples"
        case requiredNormalSamples = "required_normal_samples"
        case healthReasons = "health_reasons"
        case dailyDriverReasons = "daily_driver_reasons"
        case operatorReasons = "operator_reasons"
    }

    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        monitorHealthy = try c.decodeIfPresent(Bool.self, forKey: .monitorHealthy) ?? false
        dailyDriverReady = try c.decodeIfPresent(Bool.self, forKey: .dailyDriverReady) ?? false
        operatorReady = try c.decodeIfPresent(Bool.self, forKey: .operatorReady) ?? dailyDriverReady
        protectionMode = try c.decodeIfPresent(String.self, forKey: .protectionMode) ?? "unknown"
        latestMonitorFresh = try c.decodeIfPresent(Bool.self, forKey: .latestMonitorFresh) ?? false
        latestMonitorAgeSeconds = try c.decodeIfPresent(Double.self, forKey: .latestMonitorAgeSeconds)
        latestMonitorMaxAgeSeconds = try c.decodeIfPresent(Double.self, forKey: .latestMonitorMaxAgeSeconds)
        residentSamples = try c.decodeIfPresent(Int.self, forKey: .residentSamples)
        residentNormalSamples = try c.decodeIfPresent(Int.self, forKey: .residentNormalSamples)
        requiredNormalSamples = try c.decodeIfPresent(Int.self, forKey: .requiredNormalSamples)
        healthReasons = try c.decodeIfPresent([String].self, forKey: .healthReasons) ?? []
        dailyDriverReasons = try c.decodeIfPresent([String].self, forKey: .dailyDriverReasons) ?? []
        operatorReasons = try c.decodeIfPresent([String].self, forKey: .operatorReasons) ?? []
    }

    public init(
        monitorHealthy: Bool = false,
        dailyDriverReady: Bool = false,
        protectionMode: String = "unknown",
        latestMonitorFresh: Bool = false
    ) {
        self.monitorHealthy = monitorHealthy
        self.dailyDriverReady = dailyDriverReady
        self.operatorReady = dailyDriverReady
        self.protectionMode = protectionMode
        self.latestMonitorFresh = latestMonitorFresh
        self.healthReasons = []
        self.dailyDriverReasons = []
        self.operatorReasons = []
    }
}

public struct CoverageSurface: Codable, Sendable, Identifiable, Hashable {
    public var id: String
    public var label: String
    public var state: String
    public var scope: String
    public var detail: String
}

public struct CoverageReport: Codable, Sendable, Hashable {
    public var status: String
    public var repoRoot: String?
    public var surfaces: [CoverageSurface]
    public var limitations: [String]

    enum CodingKeys: String, CodingKey {
        case status
        case repoRoot = "repo_root"
        case surfaces, limitations
    }

    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        status = try c.decodeIfPresent(String.self, forKey: .status) ?? "attention"
        repoRoot = try c.decodeIfPresent(String.self, forKey: .repoRoot)
        surfaces = try c.decodeIfPresent([CoverageSurface].self, forKey: .surfaces) ?? []
        limitations = try c.decodeIfPresent([String].self, forKey: .limitations) ?? []
    }
}

public struct StatusEnvelope: Codable, Sendable {
    public var action: String?
    public var hasLatestMonitor: Bool?
    public var hasRecoveryHint: Bool?
    public var health: StatusHealth?
    public var coverage: CoverageReport?
    public var snapshot: PressureSnapshot?
    public var snapshotSummary: PressureSnapshot?
    public var latestMonitor: PressureSnapshot?
    public var latestMonitorSummary: PressureSnapshot?
    public var recoveryHint: RecoveryHint?

    enum CodingKeys: String, CodingKey {
        case action
        case hasLatestMonitor = "has_latest_monitor"
        case hasRecoveryHint = "has_recovery_hint"
        case health
        case coverage
        case snapshot
        case snapshotSummary = "snapshot_summary"
        case latestMonitor = "latest_monitor"
        case latestMonitorSummary = "latest_monitor_summary"
        case recoveryHint = "recovery_hint"
    }
}

public struct RecoveryHint: Codable, Sendable, Hashable {
    public var schemaVersion: Int?
    public var detectedAt: Date?
    public var previousStartedAt: Date?
    public var lastSampleAt: Date?
    public var lastLevel: PressureLevel?
    public var reason: String
    public var recoveryCommand: String

    enum CodingKeys: String, CodingKey {
        case schemaVersion = "schema_version"
        case detectedAt = "detected_at"
        case previousStartedAt = "previous_started_at"
        case lastSampleAt = "last_sample_at"
        case lastLevel = "last_level"
        case reason
        case recoveryCommand = "recovery_command"
    }

    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        schemaVersion = try c.decodeIfPresent(Int.self, forKey: .schemaVersion)
        detectedAt = try c.decodeIfPresent(Date.self, forKey: .detectedAt)
        previousStartedAt = try c.decodeIfPresent(Date.self, forKey: .previousStartedAt)
        lastSampleAt = try c.decodeIfPresent(Date.self, forKey: .lastSampleAt)
        if let raw = try c.decodeIfPresent(String.self, forKey: .lastLevel) {
            lastLevel = PressureLevel(raw: raw)
        }
        reason = try c.decodeIfPresent(String.self, forKey: .reason) ?? ""
        recoveryCommand = try c.decodeIfPresent(String.self, forKey: .recoveryCommand) ?? ""
    }
}

public struct SnapshotEnvelope: Codable, Sendable {
    public var action: String?
    public var ok: Bool?
    public var snapshot: PressureSnapshot?
}

public struct Admission: Codable, Sendable, Hashable {
    public var allowed: Bool
    public var level: PressureLevel
    public var source: String?
    public var dimension: String?
    public var reasons: [String]
    public var warning: String?
    public var snapshot: PressureSnapshot?
    public var workQueue: WorkQueueAdmission?
    public var controller: ControllerDecision?

    enum CodingKeys: String, CodingKey {
        case allowed, level, source, dimension, reasons, warning, snapshot
        case workQueue = "work_queue"
        case controller
    }

    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        allowed = try c.decodeIfPresent(Bool.self, forKey: .allowed) ?? true
        level = PressureLevel(raw: try c.decodeIfPresent(String.self, forKey: .level))
        source = try c.decodeIfPresent(String.self, forKey: .source)
        dimension = try c.decodeIfPresent(String.self, forKey: .dimension)
        reasons = try c.decodeIfPresent([String].self, forKey: .reasons) ?? []
        warning = try c.decodeIfPresent(String.self, forKey: .warning)
        snapshot = try c.decodeIfPresent(PressureSnapshot.self, forKey: .snapshot)
        workQueue = try c.decodeIfPresent(WorkQueueAdmission.self, forKey: .workQueue)
        controller = try c.decodeIfPresent(ControllerDecision.self, forKey: .controller)
    }
}

public struct ControllerDecision: Codable, Sendable, Hashable {
    public var mode: String
    public var cpuStress: Bool
    public var thermalState: String
    public var lowPowerMode: Bool
    public var blockWork: Bool
    public var blockAgentLaunch: Bool
    public var dimension: String?
    public var reasons: [String]

    enum CodingKeys: String, CodingKey {
        case mode
        case cpuStress = "cpu_stress"
        case thermalState = "thermal_state"
        case lowPowerMode = "low_power_mode"
        case blockWork = "block_work"
        case blockAgentLaunch = "block_agent_launch"
        case dimension, reasons
    }

    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        mode = try c.decodeIfPresent(String.self, forKey: .mode) ?? "unknown"
        cpuStress = try c.decodeIfPresent(Bool.self, forKey: .cpuStress) ?? false
        thermalState = try c.decodeIfPresent(String.self, forKey: .thermalState) ?? "unknown"
        lowPowerMode = try c.decodeIfPresent(Bool.self, forKey: .lowPowerMode) ?? false
        blockWork = try c.decodeIfPresent(Bool.self, forKey: .blockWork) ?? false
        blockAgentLaunch = try c.decodeIfPresent(Bool.self, forKey: .blockAgentLaunch) ?? false
        dimension = try c.decodeIfPresent(String.self, forKey: .dimension)
        reasons = try c.decodeIfPresent([String].self, forKey: .reasons) ?? []
    }
}

public struct WorkQueueAdmission: Codable, Sendable, Hashable {
    public var capacity: Int
    public var used: Int
    public var queueDepth: Int
    public var oldestWaitMS: Int64
    public var queueDepthBlock: Int
    public var oldestWaitBlockMS: Int64
    public var wouldBlock: Bool
    public var enforced: Bool

    enum CodingKeys: String, CodingKey {
        case capacity, used
        case queueDepth = "queue_depth"
        case oldestWaitMS = "oldest_wait_ms"
        case queueDepthBlock = "queue_depth_block"
        case oldestWaitBlockMS = "oldest_wait_block_ms"
        case wouldBlock = "would_block"
        case enforced
    }
}

public struct CheckEnvelope: Codable, Sendable {
    public var action: String?
    public var admission: Admission?
}

/// One composite read from `session pressure board`. It carries the same typed
/// sections the individual contracts emit, so the app pays one `ndev` cold
/// start per refresh instead of one per contract. Every section is optional:
/// the CLI reports a failed section as `<section>_error` and still returns the
/// rest, and an n-1 helper without the verb is handled by the caller falling
/// back to the per-contract path.
public struct BoardEnvelope: Codable, Sendable {
    public var ok: Bool?
    public var action: String?
    public var outputScope: String?
    public var health: StatusHealth?
    /// Full coverage list (surfaces + limitations). Present under `--full`.
    /// Compact board emits `coverage_summary` counts instead for agent/menu-bar
    /// polls; overview panes request `--full` so this stays populated for UI.
    public var coverage: CoverageReport?
    public var snapshot: PressureSnapshot?
    public var snapshotSummary: PressureSnapshot?
    public var latestMonitor: PressureSnapshot?
    public var latestMonitorSummary: PressureSnapshot?
    public var hasLatestMonitor: Bool?
    public var hasRecoveryHint: Bool?
    public var recoveryHint: RecoveryHint?
    public var work: WorkStatus?
    /// Launch decision. Compact board omits nested `snapshot` (use
    /// `latest_monitor_summary`); `--full` restores the diagnostic shape.
    public var admission: Admission?
    public var policy: PressurePolicy?
    /// Full launchd detail. Compact board may omit this unless `--include monitor`
    /// or `--full`; light menu-bar polls do not need paths/hashes every tick.
    public var launchd: LaunchdStatus?
    public var doctor: DoctorEnvelope?
    public var calibration: WorkCalibration?
    public var inventory: IdleInventory?
    /// Which snapshot the idle candidates came from — a board read never
    /// authorizes a signal, so this must not be mistaken for `idle --apply`.
    public var idleSource: String?
    public var events: [TelemetryEvent]?
    public var actions: [PressureAction]?
    public var workError: String?

    enum CodingKeys: String, CodingKey {
        case ok, action, health, coverage, snapshot, work, admission, policy, launchd
        case doctor, calibration, inventory, events, actions
        case outputScope = "output_scope"
        case snapshotSummary = "snapshot_summary"
        case latestMonitor = "latest_monitor"
        case latestMonitorSummary = "latest_monitor_summary"
        case hasLatestMonitor = "has_latest_monitor"
        case hasRecoveryHint = "has_recovery_hint"
        case recoveryHint = "recovery_hint"
        case idleSource = "idle_source"
        case workError = "work_error"
    }

    /// The richest snapshot this read carries, preferring a fresh sample over
    /// the resident one.
    public var effectiveSnapshot: PressureSnapshot? {
        snapshot ?? snapshotSummary ?? latestMonitor ?? latestMonitorSummary
    }

    public var effectiveCoverage: CoverageReport? { coverage }
    public var effectiveLaunchd: LaunchdStatus? { launchd }
}

// MARK: - Policy

public struct PolicyThresholds: Codable, Sendable, Hashable {
    public var hostCPUWarningPercent: Double?
    public var hostCPURedPercent: Double?
    public var freeWarningPercent: Int?
    public var freeRedPercent: Int?
    public var freeCriticalPercent: Int?
    public var swapWarningMB: Double?
    public var swapRedMB: Double?
    public var swapCriticalMB: Double?
    public var treeWarningMB: Double?
    public var treeRedMB: Double?
    public var treeCriticalMB: Double?
    public var agentTotalWarningMB: Double?
    public var agentTotalRedMB: Double?
    public var agentTotalCriticalMB: Double?

    enum CodingKeys: String, CodingKey {
        case hostCPUWarningPercent = "host_cpu_warning_percent"
        case hostCPURedPercent = "host_cpu_red_percent"
        case freeWarningPercent = "free_warning_percent"
        case freeRedPercent = "free_red_percent"
        case freeCriticalPercent = "free_critical_percent"
        case swapWarningMB = "swap_warning_mb"
        case swapRedMB = "swap_red_mb"
        case swapCriticalMB = "swap_critical_mb"
        case treeWarningMB = "tree_warning_mb"
        case treeRedMB = "tree_red_mb"
        case treeCriticalMB = "tree_critical_mb"
        case agentTotalWarningMB = "agent_total_warning_mb"
        case agentTotalRedMB = "agent_total_red_mb"
        case agentTotalCriticalMB = "agent_total_critical_mb"
    }
}

public struct PolicyResourceBudgets: Codable, Sendable, Hashable {
    public var maxSelfRSSMB: Double?
    public var maxIdleCPUPercent: Double?
    public var maxPressureCPUPercent: Double?
    public var maxSampleDurationMS: Double?
    public var maxSampleCPUTimeMS: Double?
    public var maxTelemetryBytesPerDay: Int64?

    enum CodingKeys: String, CodingKey {
        case maxSelfRSSMB = "max_self_rss_mb"
        case maxIdleCPUPercent = "max_idle_cpu_percent"
        case maxPressureCPUPercent = "max_pressure_cpu_percent"
        case maxSampleDurationMS = "max_sample_duration_ms"
        case maxSampleCPUTimeMS = "max_sample_cpu_time_ms"
        case maxTelemetryBytesPerDay = "max_telemetry_bytes_per_day"
    }
}

public struct PolicyWorkLimits: Codable, Sendable, Hashable {
    public var capacity: Int?
    public var warningCapacity: Int?
    public var warningCapacityEnabled: Bool?
    public var testWeight: Int?
    public var buildWeight: Int?
    public var expressTestWeight: Int?
    public var expressBuildWeight: Int?
    public var emulatorWeight: Int?
    public var browserWeight: Int?
    public var heavyWeight: Int?
    public var benchmarkWeight: Int?
    public var reclaimWeight: Int?

    enum CodingKeys: String, CodingKey {
        case capacity
        case warningCapacity = "warning_capacity"
        case warningCapacityEnabled = "warning_capacity_enabled"
        case testWeight = "test_weight"
        case buildWeight = "build_weight"
        case expressTestWeight = "express_test_weight"
        case expressBuildWeight = "express_build_weight"
        case emulatorWeight = "emulator_weight"
        case browserWeight = "browser_weight"
        case heavyWeight = "heavy_weight"
        case benchmarkWeight = "benchmark_weight"
        case reclaimWeight = "reclaim_weight"
    }
}

public struct PolicyLaunchAdmission: Codable, Sendable, Hashable {
    public var mode: String
    public var queueDepthBlock: Int
    public var oldestWaitBlockSeconds: Int
    public var resumeBehavior: String

    enum CodingKeys: String, CodingKey {
        case mode
        case queueDepthBlock = "queue_depth_block"
        case oldestWaitBlockSeconds = "oldest_wait_block_seconds"
        case resumeBehavior = "resume_behavior"
    }
}

public struct PressurePolicy: Codable, Sendable, Hashable {
    public var schemaVersion: Int?
    public var profile: String?
    public var enabled: Bool
    public var enforceAdmission: Bool
    public var autoShedCritical: Bool
    public var sampleIntervalSeconds: Int?
    public var pressureSampleIntervalSeconds: Int?
    public var criticalSampleIntervalSeconds: Int?
    public var blockNewAt: String?
    public var thresholds: PolicyThresholds?
    public var resourceBudgets: PolicyResourceBudgets?
    public var workLimits: PolicyWorkLimits?
    public var launchAdmission: PolicyLaunchAdmission?
    public var diskWrite: DiskWritePolicy?

    enum CodingKeys: String, CodingKey {
        case schemaVersion = "schema_version"
        case profile
        case enabled
        case enforceAdmission = "enforce_admission"
        case autoShedCritical = "auto_shed_critical"
        case sampleIntervalSeconds = "sample_interval_seconds"
        case pressureSampleIntervalSeconds = "pressure_sample_interval_seconds"
        case criticalSampleIntervalSeconds = "critical_sample_interval_seconds"
        case blockNewAt = "block_new_at"
        case thresholds
        case resourceBudgets = "resource_budgets"
        case workLimits = "work_limits"
        case launchAdmission = "launch_admission"
        case diskWrite = "disk_write"
    }

    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        schemaVersion = try c.decodeIfPresent(Int.self, forKey: .schemaVersion)
        profile = try c.decodeIfPresent(String.self, forKey: .profile)
        enabled = try c.decodeIfPresent(Bool.self, forKey: .enabled) ?? false
        enforceAdmission = try c.decodeIfPresent(Bool.self, forKey: .enforceAdmission) ?? false
        autoShedCritical = try c.decodeIfPresent(Bool.self, forKey: .autoShedCritical) ?? false
        sampleIntervalSeconds = try c.decodeIfPresent(Int.self, forKey: .sampleIntervalSeconds)
        pressureSampleIntervalSeconds = try c.decodeIfPresent(Int.self, forKey: .pressureSampleIntervalSeconds)
        criticalSampleIntervalSeconds = try c.decodeIfPresent(Int.self, forKey: .criticalSampleIntervalSeconds)
        blockNewAt = try c.decodeIfPresent(String.self, forKey: .blockNewAt)
        thresholds = try c.decodeIfPresent(PolicyThresholds.self, forKey: .thresholds)
        resourceBudgets = try c.decodeIfPresent(PolicyResourceBudgets.self, forKey: .resourceBudgets)
        workLimits = try c.decodeIfPresent(PolicyWorkLimits.self, forKey: .workLimits)
        launchAdmission = try c.decodeIfPresent(PolicyLaunchAdmission.self, forKey: .launchAdmission)
        diskWrite = try c.decodeIfPresent(DiskWritePolicy.self, forKey: .diskWrite)
    }

    public init(
        enabled: Bool = false,
        enforceAdmission: Bool = false,
        autoShedCritical: Bool = false
    ) {
        self.enabled = enabled
        self.profile = nil
        self.enforceAdmission = enforceAdmission
        self.autoShedCritical = autoShedCritical
        self.launchAdmission = nil
        self.diskWrite = nil
    }

    public var modeLabel: String {
        if !enabled { return "Disabled" }
        if let profile, !profile.isEmpty {
            return profile.replacingOccurrences(of: "-", with: " ").capitalized
        }
        if enforceAdmission && autoShedCritical { return "Full protection" }
        if enforceAdmission { return "Admission only" }
        return "Observe only"
    }
}

public struct PolicyEnvelope: Codable, Sendable {
    public var action: String?
    public var ok: Bool?
    public var path: String?
    public var persisted: Bool?
    public var policy: PressurePolicy?
}

public struct PolicyProfile: Codable, Sendable, Hashable, Identifiable {
    public var name: String
    public var description: String
    public var id: String { name }
}

public struct PolicyProfilesEnvelope: Codable, Sendable {
    public var action: String?
    public var ok: Bool?
    public var profiles: [PolicyProfile]
    public var defaults: PolicyProfileDefaults?

    enum CodingKeys: String, CodingKey {
        case action, ok, profiles, defaults
    }

    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        action = try c.decodeIfPresent(String.self, forKey: .action)
        ok = try c.decodeIfPresent(Bool.self, forKey: .ok)
        profiles = try c.decodeIfPresent([PolicyProfile].self, forKey: .profiles) ?? []
        defaults = try c.decodeIfPresent(PolicyProfileDefaults.self, forKey: .defaults)
    }
}

public struct PolicyProfileDefaults: Codable, Sendable, Hashable {
    public var profile: String?
    public var enforceAdmission: Bool?
    public var autoShedCritical: Bool?
    public var warningDerating: String?

    enum CodingKeys: String, CodingKey {
        case profile
        case enforceAdmission = "enforce_admission"
        case autoShedCritical = "auto_shed_critical"
        case warningDerating = "warning_derating"
    }
}

// MARK: - Work coordinator

public struct WorkLease: Codable, Sendable, Identifiable, Hashable {
    public var id: String
    public var operationID: String?
    public var className: String
    public var weight: Int
    public var pid: Int?
    public var startedAt: Date?
    public var ageMS: Int64
    public var review: Bool
    public var reviewReason: String?

    enum CodingKeys: String, CodingKey {
        case id
        case operationID = "operation_id"
        case className = "class"
        case weight, pid
        case startedAt = "started_at"
        case ageMS = "age_ms"
        case review
        case reviewReason = "review_reason"
    }

    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        id = try c.decodeIfPresent(String.self, forKey: .id) ?? UUID().uuidString
        operationID = try c.decodeIfPresent(String.self, forKey: .operationID)
        className = try c.decodeIfPresent(String.self, forKey: .className) ?? "unknown"
        weight = try c.decodeIfPresent(Int.self, forKey: .weight) ?? 0
        pid = try c.decodeIfPresent(Int.self, forKey: .pid)
        startedAt = try c.decodeIfPresent(Date.self, forKey: .startedAt)
        ageMS = try c.decodeIfPresent(Int64.self, forKey: .ageMS) ?? 0
        review = try c.decodeIfPresent(Bool.self, forKey: .review) ?? false
        reviewReason = try c.decodeIfPresent(String.self, forKey: .reviewReason)
    }
}

public struct WorkWaiter: Codable, Sendable, Identifiable, Hashable {
    public var operationID: String
    public var className: String
    public var weight: Int
    public var pid: Int?
    public var queuedAt: Date?
    public var heartbeatAt: Date?
    public var position: Int?
    public var waitMS: Int64
    public var bypassCount: Int
    public var protected: Bool
    public var protectionReason: String?
    public var reservationKind: String?
    public var reservedAt: Date?
    /// 1-based place in the operator promotion sequence; 1 is the active head.
    /// Zero means this waiter is not pinned.
    public var overridePosition: Int

    public var id: String { operationID }

    enum CodingKeys: String, CodingKey {
        case operationID = "operation_id"
        case className = "class"
        case weight, pid
        case queuedAt = "queued_at"
        case heartbeatAt = "heartbeat_at"
        case position
        case waitMS = "wait_ms"
        case bypassCount = "bypass_count"
        case protected
        case protectionReason = "protection_reason"
        case reservationKind = "reservation_kind"
        case reservedAt = "reserved_at"
        case overridePosition = "override_position"
    }

    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        operationID = try c.decodeIfPresent(String.self, forKey: .operationID) ?? UUID().uuidString
        className = try c.decodeIfPresent(String.self, forKey: .className) ?? "unknown"
        weight = try c.decodeIfPresent(Int.self, forKey: .weight) ?? 0
        pid = try c.decodeIfPresent(Int.self, forKey: .pid)
        queuedAt = try c.decodeIfPresent(Date.self, forKey: .queuedAt)
        heartbeatAt = try c.decodeIfPresent(Date.self, forKey: .heartbeatAt)
        position = try c.decodeIfPresent(Int.self, forKey: .position)
        waitMS = try c.decodeIfPresent(Int64.self, forKey: .waitMS) ?? 0
        bypassCount = try c.decodeIfPresent(Int.self, forKey: .bypassCount) ?? 0
        protected = try c.decodeIfPresent(Bool.self, forKey: .protected) ?? false
        protectionReason = try c.decodeIfPresent(String.self, forKey: .protectionReason)
        reservationKind = try c.decodeIfPresent(String.self, forKey: .reservationKind)
        reservedAt = try c.decodeIfPresent(Date.self, forKey: .reservedAt)
        // Absent on an n-1 helper: unpinned, never a decode failure.
        overridePosition = try c.decodeIfPresent(Int.self, forKey: .overridePosition) ?? 0
    }

    /// This waiter is pinned behind the active head by an operator sequence. It
    /// is already going to run in order, so offering "Run now" on it would only
    /// let one click silently discard the rest of that sequence.
    public var isOverrideQueued: Bool { overridePosition > 1 }

    /// A pressure-reserved waiter already won its turn in the queue and is parked
    /// waiting for the host to recover. Promoting it changes nothing, so the UI
    /// must not offer "Run now" as though it were an ordinary queued operation.
    public var isPressureReserved: Bool { reservationKind == "pressure" }
}

/// A process parked at the host-pressure admission gate, before it has registered
/// with the weighted coordinator. Holds charge no capacity, so without this the
/// app showed an empty queue and idle capacity while work was blocked.
public struct WorkAdmissionHold: Codable, Sendable, Identifiable, Hashable {
    public var operationID: String
    public var className: String
    public var weight: Int
    public var pid: Int?
    public var heldSince: Date?
    public var heldForMS: Int64
    public var dimension: String?
    public var reason: String?

    public var id: String { operationID }

    enum CodingKeys: String, CodingKey {
        case operationID = "operation_id"
        case className = "class"
        case weight, pid, dimension, reason
        case heldSince = "held_since"
        case heldForMS = "held_for_ms"
    }

    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        operationID = try c.decodeIfPresent(String.self, forKey: .operationID) ?? UUID().uuidString
        className = try c.decodeIfPresent(String.self, forKey: .className) ?? "unknown"
        weight = try c.decodeIfPresent(Int.self, forKey: .weight) ?? 0
        pid = try c.decodeIfPresent(Int.self, forKey: .pid)
        heldSince = try c.decodeIfPresent(Date.self, forKey: .heldSince)
        heldForMS = try c.decodeIfPresent(Int64.self, forKey: .heldForMS) ?? 0
        dimension = try c.decodeIfPresent(String.self, forKey: .dimension)
        reason = try c.decodeIfPresent(String.self, forKey: .reason)
    }
}

/// The shared CPU latch. One host, one latch: this is what every waiting process
/// is actually waiting on once CPU-only pressure has engaged.
public struct WorkAdmissionLatch: Codable, Sendable, Hashable {
    public var latched: Bool
    public var latchedAt: Date?
    public var dimension: String?
    public var reason: String?
    public var redSamples: Int
    public var recoverySamples: Int
    public var blockRequired: Int
    public var releaseRequired: Int

    enum CodingKeys: String, CodingKey {
        case latched, dimension, reason
        case latchedAt = "latched_at"
        case redSamples = "red_samples"
        case recoverySamples = "recovery_samples"
        case blockRequired = "block_required"
        case releaseRequired = "release_required"
    }

    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        latched = try c.decodeIfPresent(Bool.self, forKey: .latched) ?? false
        latchedAt = try c.decodeIfPresent(Date.self, forKey: .latchedAt)
        dimension = try c.decodeIfPresent(String.self, forKey: .dimension)
        reason = try c.decodeIfPresent(String.self, forKey: .reason)
        redSamples = try c.decodeIfPresent(Int.self, forKey: .redSamples) ?? 0
        recoverySamples = try c.decodeIfPresent(Int.self, forKey: .recoverySamples) ?? 0
        blockRequired = try c.decodeIfPresent(Int.self, forKey: .blockRequired) ?? 0
        releaseRequired = try c.decodeIfPresent(Int.self, forKey: .releaseRequired) ?? 0
    }
}

public struct WorkStatus: Codable, Sendable, Hashable {
    public var schemaVersion: Int?
    public var capacity: Int
    public var used: Int
    public var available: Int
    public var leases: [WorkLease]
    public var waiters: [WorkWaiter]
    public var queueDepth: Int
    public var statePath: String?
    public var schedulingPolicy: String?
    public var selectorSchemaVersion: Int?
    public var selectedOperationID: String?
    public var protectedOperationID: String?
    public var decisionReason: String?
    public var bypassedCount: Int
    public var overrideOperationID: String?
    public var overrideRequestedAt: Date?
    /// Pending tail of the operator promotion sequence, excluding the active
    /// head; `overrideQueueDepth` counts the whole pinned set including it.
    public var overrideQueue: [String]
    public var overrideQueueDepth: Int
    public var admissionHolds: [WorkAdmissionHold]
    public var admissionHoldCount: Int
    public var longestAdmissionHoldMS: Int64
    public var admissionLatch: WorkAdmissionLatch?

    enum CodingKeys: String, CodingKey {
        case schemaVersion = "schema_version"
        case capacity, used, available, leases, waiters
        case queueDepth = "queue_depth"
        case admissionHolds = "admission_holds"
        case admissionHoldCount = "admission_hold_count"
        case longestAdmissionHoldMS = "longest_admission_hold_ms"
        case admissionLatch = "admission_latch"
        case statePath = "state_path"
        case schedulingPolicy = "scheduling_policy"
        case selectorSchemaVersion = "selector_schema_version"
        case selectedOperationID = "selected_operation_id"
        case protectedOperationID = "protected_operation_id"
        case decisionReason = "decision_reason"
        case bypassedCount = "bypassed_count"
        case overrideOperationID = "override_operation_id"
        case overrideRequestedAt = "override_requested_at"
        case overrideQueue = "override_queue"
        case overrideQueueDepth = "override_queue_depth"
    }

    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        schemaVersion = try c.decodeIfPresent(Int.self, forKey: .schemaVersion)
        capacity = try c.decodeIfPresent(Int.self, forKey: .capacity) ?? 0
        used = try c.decodeIfPresent(Int.self, forKey: .used) ?? 0
        available = try c.decodeIfPresent(Int.self, forKey: .available) ?? 0
        leases = try c.decodeIfPresent([WorkLease].self, forKey: .leases) ?? []
        waiters = try c.decodeIfPresent([WorkWaiter].self, forKey: .waiters) ?? []
        queueDepth = try c.decodeIfPresent(Int.self, forKey: .queueDepth) ?? 0
        statePath = try c.decodeIfPresent(String.self, forKey: .statePath)
        schedulingPolicy = try c.decodeIfPresent(String.self, forKey: .schedulingPolicy)
        selectorSchemaVersion = try c.decodeIfPresent(Int.self, forKey: .selectorSchemaVersion)
        selectedOperationID = try c.decodeIfPresent(String.self, forKey: .selectedOperationID)
        protectedOperationID = try c.decodeIfPresent(String.self, forKey: .protectedOperationID)
        decisionReason = try c.decodeIfPresent(String.self, forKey: .decisionReason)
        bypassedCount = try c.decodeIfPresent(Int.self, forKey: .bypassedCount) ?? 0
        overrideOperationID = try c.decodeIfPresent(String.self, forKey: .overrideOperationID)
        overrideRequestedAt = try c.decodeIfPresent(Date.self, forKey: .overrideRequestedAt)
        overrideQueue = try c.decodeIfPresent([String].self, forKey: .overrideQueue) ?? []
        // A schema-7 helper knows only the single head. Derive the depth from it
        // rather than reporting zero pinned while a promotion is actually live.
        overrideQueueDepth = try c.decodeIfPresent(Int.self, forKey: .overrideQueueDepth)
            ?? ((overrideOperationID?.isEmpty == false) ? 1 + overrideQueue.count : 0)
        // An older helper omits these entirely; absent must read as "nothing held",
        // never as a decode failure that blanks the whole work view.
        admissionHolds = try c.decodeIfPresent([WorkAdmissionHold].self, forKey: .admissionHolds) ?? []
        admissionHoldCount = try c.decodeIfPresent(Int.self, forKey: .admissionHoldCount) ?? 0
        longestAdmissionHoldMS = try c.decodeIfPresent(Int64.self, forKey: .longestAdmissionHoldMS) ?? 0
        admissionLatch = try c.decodeIfPresent(WorkAdmissionLatch.self, forKey: .admissionLatch)
    }

    /// The first persisted work-state shape with an ordered override tail.
    /// Below it a write keeps only the single override head.
    public static let overrideSequenceMinimumSchema = 8

    /// Whether this host's persisted work state can carry a multi-operation
    /// promotion sequence. While a legacy cohort still holds leases the document
    /// stays pinned at its own schema, so `--all` fails closed rather than
    /// persisting one entry and reporting several.
    public var supportsOverrideSequence: Bool {
        (schemaVersion ?? WorkStatus.overrideSequenceMinimumSchema) >= WorkStatus.overrideSequenceMinimumSchema
    }

    public init(capacity: Int = 0, used: Int = 0, available: Int = 0, leases: [WorkLease] = [], waiters: [WorkWaiter] = []) {
        self.capacity = capacity
        self.used = used
        self.available = available
        self.leases = leases
        self.waiters = waiters
        self.queueDepth = waiters.count
        self.bypassedCount = 0
        self.schedulingPolicy = nil
        self.selectorSchemaVersion = nil
        self.selectedOperationID = nil
        self.protectedOperationID = nil
        self.decisionReason = nil
        self.overrideOperationID = nil
        self.overrideRequestedAt = nil
        self.overrideQueue = []
        self.overrideQueueDepth = 0
        self.statePath = nil
        self.admissionHolds = []
        self.admissionHoldCount = 0
        self.longestAdmissionHoldMS = 0
        self.admissionLatch = nil
    }
}

public struct WorkEnvelope: Codable, Sendable {
    public var action: String?
    public var ok: Bool?
    public var work: WorkStatus?
}

/// Privacy-safe calibration envelope from `ndev session pressure work report|stats`.
/// Counts only — never argv, cwd, or outcomes free text lists required for UI.
public struct WorkCalibration: Codable, Sendable, Hashable {
    public var schemaVersion: Int?
    public var operationCount: Int?
    public var wrapperInterruptOperations: Int?
    public var suggestedPolicyProfile: String?
    public var suggestedPolicyProfileReason: String?
    public var expressTestShare: Double?
    public var expressBuildShare: Double?
    public var thresholdRetuneHint: String?
    public var interruptProjection: InterruptProjection?

    enum CodingKeys: String, CodingKey {
        case schemaVersion = "schema_version"
        case operationCount = "operation_count"
        case wrapperInterruptOperations = "wrapper_interrupt_operations"
        case suggestedPolicyProfile = "suggested_policy_profile"
        case suggestedPolicyProfileReason = "suggested_policy_profile_reason"
        case expressTestShare = "express_test_share"
        case expressBuildShare = "express_build_share"
        case thresholdRetuneHint = "threshold_retune_hint"
        case interruptProjection = "interrupt_projection"
    }

    public init(
        schemaVersion: Int? = nil,
        operationCount: Int? = nil,
        wrapperInterruptOperations: Int? = nil,
        suggestedPolicyProfile: String? = nil,
        suggestedPolicyProfileReason: String? = nil,
        expressTestShare: Double? = nil,
        expressBuildShare: Double? = nil,
        thresholdRetuneHint: String? = nil,
        interruptProjection: InterruptProjection? = nil
    ) {
        self.schemaVersion = schemaVersion
        self.operationCount = operationCount
        self.wrapperInterruptOperations = wrapperInterruptOperations
        self.suggestedPolicyProfile = suggestedPolicyProfile
        self.suggestedPolicyProfileReason = suggestedPolicyProfileReason
        self.expressTestShare = expressTestShare
        self.expressBuildShare = expressBuildShare
        self.thresholdRetuneHint = thresholdRetuneHint
        self.interruptProjection = interruptProjection
    }

    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        schemaVersion = try c.decodeIfPresent(Int.self, forKey: .schemaVersion)
        operationCount = try c.decodeIfPresent(Int.self, forKey: .operationCount)
        wrapperInterruptOperations = try c.decodeIfPresent(Int.self, forKey: .wrapperInterruptOperations) ?? 0
        suggestedPolicyProfile = try c.decodeIfPresent(String.self, forKey: .suggestedPolicyProfile)
        suggestedPolicyProfileReason = try c.decodeIfPresent(String.self, forKey: .suggestedPolicyProfileReason)
        expressTestShare = try c.decodeIfPresent(Double.self, forKey: .expressTestShare)
        expressBuildShare = try c.decodeIfPresent(Double.self, forKey: .expressBuildShare)
        thresholdRetuneHint = try c.decodeIfPresent(String.self, forKey: .thresholdRetuneHint)
        interruptProjection = try c.decodeIfPresent(InterruptProjection.self, forKey: .interruptProjection)
    }

    /// Count for chip display; treats missing as 0.
    public var interruptCount: Int { wrapperInterruptOperations ?? 0 }
}

public struct InterruptProjection: Codable, Sendable, Hashable {
    public var schemaVersion: Int?
    public var wrapperInterruptOperations: Int?
    public var wrapperInterruptRatePerHour: Double?
    public var windowHours: Double?

    enum CodingKeys: String, CodingKey {
        case schemaVersion = "schema_version"
        case wrapperInterruptOperations = "wrapper_interrupt_operations"
        case wrapperInterruptRatePerHour = "wrapper_interrupt_rate_per_hour"
        case windowHours = "window_hours"
    }
}

public struct WorkReportEnvelope: Codable, Sendable {
    public var action: String?
    public var ok: Bool?
    public var calibration: WorkCalibration?

    enum CodingKeys: String, CodingKey {
        case action, ok, calibration
    }
}

public struct WorkStatsEnvelope: Codable, Sendable {
    public var action: String?
    public var ok: Bool?
    public var calibration: WorkCalibration?

    enum CodingKeys: String, CodingKey {
        case action, ok, calibration
    }
}

public struct WorkOverrideReceipt: Codable, Sendable, Hashable {
    public var operationID: String
    public var className: String
    public var weight: Int
    public var pid: Int
    public var previousPosition: Int
    public var requestedAt: Date
    public var alreadyRequested: Bool
    /// 1-based place in the confirmed promotion sequence.
    public var overridePosition: Int

    enum CodingKeys: String, CodingKey {
        case operationID = "operation_id"
        case className = "class"
        case weight, pid
        case previousPosition = "previous_position"
        case requestedAt = "requested_at"
        case alreadyRequested = "already_requested"
        case overridePosition = "override_position"
    }

    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        operationID = try c.decodeIfPresent(String.self, forKey: .operationID) ?? ""
        className = try c.decodeIfPresent(String.self, forKey: .className) ?? "unknown"
        weight = try c.decodeIfPresent(Int.self, forKey: .weight) ?? 0
        pid = try c.decodeIfPresent(Int.self, forKey: .pid) ?? 0
        previousPosition = try c.decodeIfPresent(Int.self, forKey: .previousPosition) ?? 0
        requestedAt = try c.decodeIfPresent(Date.self, forKey: .requestedAt) ?? Date()
        alreadyRequested = try c.decodeIfPresent(Bool.self, forKey: .alreadyRequested) ?? false
        // An n-1 helper reports a single-slot override; that is position 1.
        overridePosition = try c.decodeIfPresent(Int.self, forKey: .overridePosition) ?? 1
    }
}

public struct WorkOverrideEnvelope: Codable, Sendable {
    public var action: String?
    public var ok: Bool?
    /// The active head of the sequence, retained under the original `override`
    /// key so a single-override reader keeps working unchanged.
    public var receipt: WorkOverrideReceipt?
    /// Every operation pinned by this request, head first.
    public var receipts: [WorkOverrideReceipt]
    public var pinned: Int
    public var work: WorkStatus?

    enum CodingKeys: String, CodingKey {
        case action, ok, work, pinned
        case receipt = "override"
        case receipts = "overrides"
    }

    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        action = try c.decodeIfPresent(String.self, forKey: .action)
        ok = try c.decodeIfPresent(Bool.self, forKey: .ok)
        receipt = try c.decodeIfPresent(WorkOverrideReceipt.self, forKey: .receipt)
        receipts = try c.decodeIfPresent([WorkOverrideReceipt].self, forKey: .receipts)
            ?? [receipt].compactMap { $0 }
        pinned = try c.decodeIfPresent(Int.self, forKey: .pinned) ?? receipts.count
        work = try c.decodeIfPresent(WorkStatus.self, forKey: .work)
    }
}

/// Privacy-bounded heavy-work lifecycle row from `ndev session pressure work history`.
/// Never contains argv, cwd, env, or process stdout/stderr.
public struct WorkLifecycleEvent: Codable, Sendable, Identifiable, Hashable {
    public var schemaVersion: Int?
    public var eventID: String
    public var requestID: String?
    public var timestamp: Date?
    public var event: String
    public var operationID: String
    public var leaseID: String?
    public var className: String?
    public var weight: Int?
    public var pid: Int?
    public var sessionDigest: String?
    public var commandDigest: String?
    public var blocker: String?
    public var queuePosition: Int?
    public var queueDepth: Int?
    public var capacity: Int?
    public var used: Int?
    public var available: Int?
    public var waitMS: Int64?
    public var runtimeMS: Int64?
    public var pressureLevel: String?
    public var pressureDimension: String?
    public var pressureReason: String?
    public var exitCode: Int?
    public var outcome: String?
    public var decisionReason: String?
    public var schedulingPolicy: String?

    public var id: String { eventID }

    enum CodingKeys: String, CodingKey {
        case schemaVersion = "schema_version"
        case eventID = "event_id"
        case requestID = "request_id"
        case timestamp, event
        case operationID = "operation_id"
        case leaseID = "lease_id"
        case className = "class"
        case weight, pid
        case sessionDigest = "session_digest"
        case commandDigest = "command_digest"
        case blocker
        case queuePosition = "queue_position"
        case queueDepth = "queue_depth"
        case capacity, used, available
        case waitMS = "wait_ms"
        case runtimeMS = "runtime_ms"
        case pressureLevel = "pressure_level"
        case pressureDimension = "pressure_dimension"
        case pressureReason = "pressure_reason"
        case exitCode = "exit_code"
        case outcome
        case decisionReason = "decision_reason"
        case schedulingPolicy = "scheduling_policy"
    }

    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        schemaVersion = try c.decodeIfPresent(Int.self, forKey: .schemaVersion)
        eventID = try c.decodeIfPresent(String.self, forKey: .eventID) ?? UUID().uuidString
        requestID = try c.decodeIfPresent(String.self, forKey: .requestID)
        timestamp = try c.decodeIfPresent(Date.self, forKey: .timestamp)
        event = try c.decodeIfPresent(String.self, forKey: .event) ?? "unknown"
        operationID = try c.decodeIfPresent(String.self, forKey: .operationID) ?? ""
        leaseID = try c.decodeIfPresent(String.self, forKey: .leaseID)
        className = try c.decodeIfPresent(String.self, forKey: .className)
        weight = try c.decodeIfPresent(Int.self, forKey: .weight)
        pid = try c.decodeIfPresent(Int.self, forKey: .pid)
        sessionDigest = try c.decodeIfPresent(String.self, forKey: .sessionDigest)
        commandDigest = try c.decodeIfPresent(String.self, forKey: .commandDigest)
        blocker = try c.decodeIfPresent(String.self, forKey: .blocker)
        queuePosition = try c.decodeIfPresent(Int.self, forKey: .queuePosition)
        queueDepth = try c.decodeIfPresent(Int.self, forKey: .queueDepth)
        capacity = try c.decodeIfPresent(Int.self, forKey: .capacity)
        used = try c.decodeIfPresent(Int.self, forKey: .used)
        available = try c.decodeIfPresent(Int.self, forKey: .available)
        waitMS = try c.decodeIfPresent(Int64.self, forKey: .waitMS)
        runtimeMS = try c.decodeIfPresent(Int64.self, forKey: .runtimeMS)
        pressureLevel = try c.decodeIfPresent(String.self, forKey: .pressureLevel)
        pressureDimension = try c.decodeIfPresent(String.self, forKey: .pressureDimension)
        pressureReason = try c.decodeIfPresent(String.self, forKey: .pressureReason)
        exitCode = try c.decodeIfPresent(Int.self, forKey: .exitCode)
        outcome = try c.decodeIfPresent(String.self, forKey: .outcome)
        decisionReason = try c.decodeIfPresent(String.self, forKey: .decisionReason)
        schedulingPolicy = try c.decodeIfPresent(String.self, forKey: .schedulingPolicy)
    }
}

public struct WorkHistoryEnvelope: Codable, Sendable {
    public var action: String?
    public var ok: Bool?
    public var since: Date?
    public var workEventCount: Int?
    public var workEvents: [WorkLifecycleEvent]

    enum CodingKeys: String, CodingKey {
        case action, ok, since
        case workEventCount = "work_event_count"
        case workEvents = "work_events"
    }

    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        action = try c.decodeIfPresent(String.self, forKey: .action)
        ok = try c.decodeIfPresent(Bool.self, forKey: .ok)
        since = try c.decodeIfPresent(Date.self, forKey: .since)
        workEventCount = try c.decodeIfPresent(Int.self, forKey: .workEventCount)
        workEvents = try c.decodeIfPresent([WorkLifecycleEvent].self, forKey: .workEvents) ?? []
    }
}

// MARK: - Monitor / launchd

public struct LaunchdStatus: Codable, Sendable, Hashable {
    public var ok: Bool?
    public var label: String?
    public var installed: Bool
    public var loaded: Bool
    public var pid: Int?
    public var plistPath: String?
    public var artifactPresent: Bool?
    public var artifactVerified: Bool?
    public var artifactPath: String?
    public var artifactSHA256: String?
    public var artifactInstalledAt: Date?

    enum CodingKeys: String, CodingKey {
        case ok, label, installed, loaded, pid
        case plistPath = "plist_path"
        case artifactPresent = "artifact_present"
        case artifactVerified = "artifact_verified"
        case artifactPath = "artifact_path"
        case artifactSHA256 = "artifact_sha256"
        case artifactInstalledAt = "artifact_installed_at"
    }

    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        ok = try c.decodeIfPresent(Bool.self, forKey: .ok)
        label = try c.decodeIfPresent(String.self, forKey: .label)
        installed = try c.decodeIfPresent(Bool.self, forKey: .installed) ?? false
        loaded = try c.decodeIfPresent(Bool.self, forKey: .loaded) ?? false
        pid = try c.decodeIfPresent(Int.self, forKey: .pid)
        plistPath = try c.decodeIfPresent(String.self, forKey: .plistPath)
        artifactPresent = try c.decodeIfPresent(Bool.self, forKey: .artifactPresent)
        artifactVerified = try c.decodeIfPresent(Bool.self, forKey: .artifactVerified)
        artifactPath = try c.decodeIfPresent(String.self, forKey: .artifactPath)
        artifactSHA256 = try c.decodeIfPresent(String.self, forKey: .artifactSHA256)
        artifactInstalledAt = try c.decodeIfPresent(Date.self, forKey: .artifactInstalledAt)
    }

    public init(installed: Bool = false, loaded: Bool = false, pid: Int? = nil) {
        self.installed = installed
        self.loaded = loaded
        self.pid = pid
    }
}

public struct MonitorStatusEnvelope: Codable, Sendable {
    public var action: String?
    public var ok: Bool?
    public var launchd: LaunchdStatus?
}

// MARK: - Idle

public struct IdleCriteria: Codable, Sendable, Hashable {
    public var limit: Int?
    public var maxCPUPercent: Double?
    public var minAgeSeconds: Int64?

    enum CodingKeys: String, CodingKey {
        case limit
        case maxCPUPercent = "max_cpu_percent"
        case minAgeSeconds = "min_age_seconds"
    }
}

public struct IdleInventory: Codable, Sendable, Hashable {
    public var candidates: [AgentTree]
    public var candidateCount: Int
    public var returnedCount: Int
    public var truncated: Bool

    enum CodingKeys: String, CodingKey {
        case candidates
        case candidateCount = "candidate_count"
        case returnedCount = "returned_count"
        case truncated
    }

    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        candidates = try c.decodeIfPresent([AgentTree].self, forKey: .candidates) ?? []
        candidateCount = try c.decodeIfPresent(Int.self, forKey: .candidateCount) ?? candidates.count
        returnedCount = try c.decodeIfPresent(Int.self, forKey: .returnedCount) ?? candidates.count
        truncated = try c.decodeIfPresent(Bool.self, forKey: .truncated) ?? false
    }
}

public struct IdleEnvelope: Codable, Sendable {
    public var action: String?
    public var ok: Bool?
    public var apply: Bool?
    public var criteria: IdleCriteria?
    public var inventory: IdleInventory?
    public var level: PressureLevel?
    public var sampledAt: Date?

    enum CodingKeys: String, CodingKey {
        case action, ok, apply, criteria, inventory, level
        case sampledAt = "sampled_at"
    }

    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        action = try c.decodeIfPresent(String.self, forKey: .action)
        ok = try c.decodeIfPresent(Bool.self, forKey: .ok)
        apply = try c.decodeIfPresent(Bool.self, forKey: .apply)
        criteria = try c.decodeIfPresent(IdleCriteria.self, forKey: .criteria)
        inventory = try c.decodeIfPresent(IdleInventory.self, forKey: .inventory)
        if let raw = try c.decodeIfPresent(String.self, forKey: .level) {
            level = PressureLevel(raw: raw)
        }
        sampledAt = try c.decodeIfPresent(Date.self, forKey: .sampledAt)
    }
}

// MARK: - Telemetry

public struct TelemetrySummary: Codable, Sendable, Hashable {
    public var timestamp: Date?
    public var level: PressureLevel
    public var primaryReason: String?
    public var freePercent: Int
    public var swapUsedMB: Double
    public var hostCPUPercent: Double
    public var agentTreeCount: Int
    public var agentRSSSumMB: Double
    public var memoryMomentum: MemoryMomentum
    public var freePercentSlopePerMinute: Double
    public var minutesToMemoryRed: Double?
    /// Sparse main-plane interrupt forensics (M2 heartbeat projection).
    public var wrapperInterruptOperations: Int?
    public var wrapperInterruptRatePerHour: Double?

    enum CodingKeys: String, CodingKey {
        case timestamp, level
        case primaryReason = "primary_reason"
        case freePercent = "free_percent"
        case swapUsedMB = "swap_used_mb"
        case hostCPUPercent = "host_cpu_percent"
        case agentTreeCount = "agent_tree_count"
        case agentRSSSumMB = "agent_rss_sum_mb"
        case memoryMomentum = "memory_momentum"
        case freePercentSlopePerMinute = "free_percent_slope_per_minute"
        case minutesToMemoryRed = "minutes_to_memory_red"
        case wrapperInterruptOperations = "wrapper_interrupt_operations"
        case wrapperInterruptRatePerHour = "wrapper_interrupt_rate_per_hour"
    }

    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        timestamp = try c.decodeIfPresent(Date.self, forKey: .timestamp)
        level = PressureLevel(raw: try c.decodeIfPresent(String.self, forKey: .level))
        primaryReason = try c.decodeIfPresent(String.self, forKey: .primaryReason)
        freePercent = try c.decodeIfPresent(Int.self, forKey: .freePercent) ?? 0
        swapUsedMB = try c.decodeIfPresent(Double.self, forKey: .swapUsedMB) ?? 0
        hostCPUPercent = try c.decodeIfPresent(Double.self, forKey: .hostCPUPercent) ?? 0
        agentTreeCount = try c.decodeIfPresent(Int.self, forKey: .agentTreeCount) ?? 0
        agentRSSSumMB = try c.decodeIfPresent(Double.self, forKey: .agentRSSSumMB) ?? 0
        memoryMomentum = try c.decodeIfPresent(MemoryMomentum.self, forKey: .memoryMomentum) ?? .unknown
        freePercentSlopePerMinute = try c.decodeIfPresent(Double.self, forKey: .freePercentSlopePerMinute) ?? 0
        minutesToMemoryRed = try c.decodeIfPresent(Double.self, forKey: .minutesToMemoryRed)
        wrapperInterruptOperations = try c.decodeIfPresent(Int.self, forKey: .wrapperInterruptOperations)
        wrapperInterruptRatePerHour = try c.decodeIfPresent(Double.self, forKey: .wrapperInterruptRatePerHour)
    }
}

public struct TelemetryEvent: Codable, Sendable, Identifiable, Hashable {
    public var schemaVersion: Int?
    public var timestamp: Date?
    public var event: String?
    public var snapshot: PressureSnapshot?
    public var summary: TelemetrySummary?

    public var id: String {
        let ts = timestamp?.timeIntervalSince1970 ?? 0
        return "\(event ?? "event")-\(ts)-\(snapshot?.level.rawValue ?? summary?.level.rawValue ?? "")"
    }
}

public struct PressureAction: Codable, Sendable, Identifiable, Hashable {
    public var schemaVersion: Int?
    public var timestamp: Date?
    public var kind: String?
    public var level: PressureLevel?
    public var rootPID: Int?
    public var agent: String?
    public var sessionID: String?
    public var rssSumMB: Double?
    public var semanticState: SemanticState?
    public var reliefScope: String?
    public var primaryHostExecutable: String?
    public var primaryHostRSSSumMB: Double?
    public var agentRSSSharePercent: Double?
    public var signal: String?
    public var result: String?
    public var reason: String?
    public var revalidatedSemanticState: SemanticState?

    public var id: String {
        let ts = timestamp?.timeIntervalSince1970 ?? 0
        return "\(kind ?? "action")-\(rootPID ?? 0)-\(ts)"
    }

    enum CodingKeys: String, CodingKey {
        case schemaVersion = "schema_version"
        case timestamp, kind, level
        case rootPID = "root_pid"
        case agent
        case sessionID = "session_id"
        case rssSumMB = "rss_sum_mb"
        case semanticState = "semantic_state"
        case reliefScope = "relief_scope"
        case primaryHostExecutable = "primary_host_executable"
        case primaryHostRSSSumMB = "primary_host_rss_sum_mb"
        case agentRSSSharePercent = "agent_rss_share_percent"
        case signal, result, reason
        case revalidatedSemanticState = "revalidated_semantic_state"
    }

    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        schemaVersion = try c.decodeIfPresent(Int.self, forKey: .schemaVersion)
        timestamp = try c.decodeIfPresent(Date.self, forKey: .timestamp)
        kind = try c.decodeIfPresent(String.self, forKey: .kind)
        if let raw = try c.decodeIfPresent(String.self, forKey: .level) {
            level = PressureLevel(raw: raw)
        }
        rootPID = try c.decodeIfPresent(Int.self, forKey: .rootPID)
        agent = try c.decodeIfPresent(String.self, forKey: .agent)
        sessionID = try c.decodeIfPresent(String.self, forKey: .sessionID)
        rssSumMB = try c.decodeIfPresent(Double.self, forKey: .rssSumMB)
        semanticState = try c.decodeIfPresent(SemanticState.self, forKey: .semanticState)
        reliefScope = try c.decodeIfPresent(String.self, forKey: .reliefScope)
        primaryHostExecutable = try c.decodeIfPresent(String.self, forKey: .primaryHostExecutable)
        primaryHostRSSSumMB = try c.decodeIfPresent(Double.self, forKey: .primaryHostRSSSumMB)
        agentRSSSharePercent = try c.decodeIfPresent(Double.self, forKey: .agentRSSSharePercent)
        signal = try c.decodeIfPresent(String.self, forKey: .signal)
        result = try c.decodeIfPresent(String.self, forKey: .result)
        reason = try c.decodeIfPresent(String.self, forKey: .reason)
        revalidatedSemanticState = try c.decodeIfPresent(SemanticState.self, forKey: .revalidatedSemanticState)
    }
}

public struct TelemetryEnvelope: Codable, Sendable {
    public var action: String?
    public var count: Int?
    public var actionCount: Int?
    public var directory: String?
    public var events: [TelemetryEvent]
    public var actions: [PressureAction]

    enum CodingKeys: String, CodingKey {
        case action, count, directory, events, actions
        case actionCount = "action_count"
    }

    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        action = try c.decodeIfPresent(String.self, forKey: .action)
        count = try c.decodeIfPresent(Int.self, forKey: .count)
        actionCount = try c.decodeIfPresent(Int.self, forKey: .actionCount)
        directory = try c.decodeIfPresent(String.self, forKey: .directory)
        events = try c.decodeIfPresent([TelemetryEvent].self, forKey: .events) ?? []
        actions = try c.decodeIfPresent([PressureAction].self, forKey: .actions) ?? []
    }
}

// MARK: - Doctor envelope (ndev session pressure doctor)

/// Compact agent/operator health from `ndev --json session pressure doctor`.
/// Desktop consumes this envelope rather than re-deriving health.
public struct DoctorEnvelope: Codable, Sendable, Hashable {
    public var ok: Bool?
    public var action: String?
    public var schemaVersion: Int?
    public var protectionMode: String?
    public var policyPersisted: Bool?
    public var enforceAdmission: Bool?
    public var autoShedCritical: Bool?
    public var monitor: DoctorMonitor?
    public var host: DoctorHost?
    public var work: DoctorWork?
    public var launchSoftPressure: DoctorLaunch?
    public var coverageStatus: String?
    public var fixes: [String]
    public var warnings: [String]

    enum CodingKeys: String, CodingKey {
        case ok, action
        case schemaVersion = "schema_version"
        case protectionMode = "protection_mode"
        case policyPersisted = "policy_persisted"
        case enforceAdmission = "enforce_admission"
        case autoShedCritical = "auto_shed_critical"
        case monitor, host, work
        case launchSoftPressure = "launch_soft_pressure"
        case coverageStatus = "coverage_status"
        case fixes, warnings
    }

    public init(
        ok: Bool? = nil,
        action: String? = nil,
        schemaVersion: Int? = nil,
        protectionMode: String? = nil,
        policyPersisted: Bool? = nil,
        enforceAdmission: Bool? = nil,
        autoShedCritical: Bool? = nil,
        monitor: DoctorMonitor? = nil,
        host: DoctorHost? = nil,
        work: DoctorWork? = nil,
        launchSoftPressure: DoctorLaunch? = nil,
        coverageStatus: String? = nil,
        fixes: [String] = [],
        warnings: [String] = []
    ) {
        self.ok = ok
        self.action = action
        self.schemaVersion = schemaVersion
        self.protectionMode = protectionMode
        self.policyPersisted = policyPersisted
        self.enforceAdmission = enforceAdmission
        self.autoShedCritical = autoShedCritical
        self.monitor = monitor
        self.host = host
        self.work = work
        self.launchSoftPressure = launchSoftPressure
        self.coverageStatus = coverageStatus
        self.fixes = fixes
        self.warnings = warnings
    }

    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        ok = try c.decodeIfPresent(Bool.self, forKey: .ok)
        action = try c.decodeIfPresent(String.self, forKey: .action)
        schemaVersion = try c.decodeIfPresent(Int.self, forKey: .schemaVersion)
        protectionMode = try c.decodeIfPresent(String.self, forKey: .protectionMode)
        policyPersisted = try c.decodeIfPresent(Bool.self, forKey: .policyPersisted)
        enforceAdmission = try c.decodeIfPresent(Bool.self, forKey: .enforceAdmission)
        autoShedCritical = try c.decodeIfPresent(Bool.self, forKey: .autoShedCritical)
        monitor = try c.decodeIfPresent(DoctorMonitor.self, forKey: .monitor)
        host = try c.decodeIfPresent(DoctorHost.self, forKey: .host)
        work = try c.decodeIfPresent(DoctorWork.self, forKey: .work)
        launchSoftPressure = try c.decodeIfPresent(DoctorLaunch.self, forKey: .launchSoftPressure)
        coverageStatus = try c.decodeIfPresent(String.self, forKey: .coverageStatus)
        fixes = try c.decodeIfPresent([String].self, forKey: .fixes) ?? []
        warnings = try c.decodeIfPresent([String].self, forKey: .warnings) ?? []
    }
}

public struct DoctorMonitor: Codable, Sendable, Hashable {
    public var healthy: Bool?
    public var fresh: Bool?
    public var ageSeconds: Double?
    public var pid: Int?
    public var loaded: Bool?
    public var running: Bool?

    enum CodingKeys: String, CodingKey {
        case healthy, fresh, pid, loaded, running
        case ageSeconds = "age_seconds"
    }
}

public struct DoctorHost: Codable, Sendable, Hashable {
    public var level: PressureLevel?
    public var source: String?

    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        if let raw = try c.decodeIfPresent(String.self, forKey: .level) {
            level = PressureLevel(raw: raw)
        }
        source = try c.decodeIfPresent(String.self, forKey: .source)
    }

    enum CodingKeys: String, CodingKey { case level, source }
}

public struct DoctorWork: Codable, Sendable, Hashable {
    public var capacity: Int?
    public var used: Int?
    public var queueDepth: Int?
    public var expressGreen: Bool?

    enum CodingKeys: String, CodingKey {
        case capacity, used
        case queueDepth = "queue_depth"
        case expressGreen = "express_green"
    }
}

public struct DoctorLaunch: Codable, Sendable, Hashable {
    public var wouldBlock: Bool?
    public var noiseSuppressed: Bool?
    public var enforced: Bool?

    enum CodingKeys: String, CodingKey {
        case wouldBlock = "would_block"
        case noiseSuppressed = "noise_suppressed"
        case enforced
    }
}

// MARK: - Aggregate board state

public extension PressureBoard {
    /// Fold one composite `session pressure board` read into the display board.
    /// Sections the caller did not request — or that the CLI reported as failed —
    /// keep their previous value rather than blanking a pane the user is looking
    /// at, which is the same reuse contract the per-contract fan-out had.
    init(composite env: BoardEnvelope, previous: PressureBoard?, binaryPath: String, live: Bool) {
        let sampled = env.effectiveSnapshot
        let resolvedSnapshot: PressureSnapshot
        if let sampled, sampled.schemaVersion == nil, let previous {
            resolvedSnapshot = previous.snapshot.applyingCompact(sampled)
        } else {
            resolvedSnapshot = sampled ?? previous?.snapshot ?? PressureSnapshot()
        }
        self.init(
            snapshot: resolvedSnapshot,
            health: env.health ?? previous?.health,
            coverage: env.effectiveCoverage ?? previous?.coverage,
            admission: env.admission ?? previous?.admission,
            policy: env.policy ?? previous?.policy,
            work: env.work ?? previous?.work,
            launchd: env.effectiveLaunchd ?? previous?.launchd,
            doctor: env.doctor ?? previous?.doctor,
            calibration: env.calibration ?? previous?.calibration,
            hasRecoveryHint: env.hasRecoveryHint ?? false,
            recoveryHint: env.recoveryHint,
            idleCandidates: env.inventory?.candidates ?? previous?.idleCandidates ?? [],
            telemetryEvents: env.events ?? previous?.telemetryEvents ?? [],
            reliefActions: env.actions ?? previous?.reliefActions ?? [],
            binaryPath: binaryPath,
            refreshedAt: Date(),
            liveSample: live
        )
    }
}

public struct PressureBoard: Sendable, Hashable {
    public var snapshot: PressureSnapshot
    public var health: StatusHealth?
    public var coverage: CoverageReport?
    public var admission: Admission?
    public var policy: PressurePolicy?
    public var work: WorkStatus?
    public var launchd: LaunchdStatus?
    public var doctor: DoctorEnvelope?
    public var calibration: WorkCalibration?
    public var hasRecoveryHint: Bool
    public var recoveryHint: RecoveryHint?
    public var idleCandidates: [AgentTree]
    public var telemetryEvents: [TelemetryEvent]
    public var reliefActions: [PressureAction]
    public var binaryPath: String
    public var refreshedAt: Date
    public var liveSample: Bool

    public init(
        snapshot: PressureSnapshot = PressureSnapshot(),
        health: StatusHealth? = nil,
        coverage: CoverageReport? = nil,
        admission: Admission? = nil,
        policy: PressurePolicy? = nil,
        work: WorkStatus? = nil,
        launchd: LaunchdStatus? = nil,
        doctor: DoctorEnvelope? = nil,
        calibration: WorkCalibration? = nil,
        hasRecoveryHint: Bool = false,
        recoveryHint: RecoveryHint? = nil,
        idleCandidates: [AgentTree] = [],
        telemetryEvents: [TelemetryEvent] = [],
        reliefActions: [PressureAction] = [],
        binaryPath: String = "",
        refreshedAt: Date = .distantPast,
        liveSample: Bool = false
    ) {
        self.snapshot = snapshot
        self.health = health
        self.coverage = coverage
        self.admission = admission
        self.policy = policy
        self.work = work
        self.launchd = launchd
        self.doctor = doctor
        self.calibration = calibration
        self.hasRecoveryHint = hasRecoveryHint
        self.recoveryHint = recoveryHint
        self.idleCandidates = idleCandidates
        self.telemetryEvents = telemetryEvents
        self.reliefActions = reliefActions
        self.binaryPath = binaryPath
        self.refreshedAt = refreshedAt
        self.liveSample = liveSample
    }

    public var level: PressureLevel { snapshot.level }

    /// Wrapper-interrupt count for UI chips; 0 when unknown.
    public var wrapperInterruptOperations: Int {
        calibration?.interruptCount ?? 0
    }
}
