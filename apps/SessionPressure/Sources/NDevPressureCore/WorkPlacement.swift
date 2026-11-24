import Foundation

/// `session-pressure --json work placement` (work queue v2 slice 6): what the
/// placer decided, what the runner did with offloaded work, the stall guard's
/// latest episode, waiter ETAs and live runner slots. Every field beyond the
/// envelope decodes leniently so an n-1 helper never fails the Work view.
public struct WorkPlacementEnvelope: Decodable, Sendable {
    public var action: String?
    public var ok: Bool?
    public var placement: WorkPlacementReport?
}

public struct WorkPlacementReport: Decodable, Sendable, Hashable {
    public var generatedAt: Date?
    public var windowSeconds: Int
    public var remote: PlacementRemoteConfig
    public var decisions: PlacementDecisionSummary
    public var remoteRuns: RemoteRunSummary
    public var localFallbacks: [String: Int]
    public var activeStall: WorkQueueStall?
    public var lastStall: WorkQueueStall?
    public var parkedStalls: [ParkedLeaseStall]
    public var waiters: [WaiterETA]
    public var etaBasis: String?
    public var runners: [RunnerSlots]
    public var runnersError: String?

    enum CodingKeys: String, CodingKey {
        case generatedAt = "generated_at"
        case windowSeconds = "window_seconds"
        case remote, decisions
        case remoteRuns = "remote_runs"
        case localFallbacks = "local_fallbacks"
        case activeStall = "active_stall"
        case lastStall = "last_stall"
        case parkedStalls = "parked_stalls"
        case waiters
        case etaBasis = "eta_basis"
        case runners
        case runnersError = "runners_error"
    }

    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        generatedAt = try c.decodeIfPresent(Date.self, forKey: .generatedAt)
        windowSeconds = try c.decodeIfPresent(Int.self, forKey: .windowSeconds) ?? 0
        remote = try c.decodeIfPresent(PlacementRemoteConfig.self, forKey: .remote) ?? PlacementRemoteConfig()
        decisions = try c.decodeIfPresent(PlacementDecisionSummary.self, forKey: .decisions) ?? PlacementDecisionSummary()
        remoteRuns = try c.decodeIfPresent(RemoteRunSummary.self, forKey: .remoteRuns) ?? RemoteRunSummary()
        localFallbacks = try c.decodeIfPresent([String: Int].self, forKey: .localFallbacks) ?? [:]
        activeStall = try c.decodeIfPresent(WorkQueueStall.self, forKey: .activeStall)
        lastStall = try c.decodeIfPresent(WorkQueueStall.self, forKey: .lastStall)
        parkedStalls = try c.decodeIfPresent([ParkedLeaseStall].self, forKey: .parkedStalls) ?? []
        waiters = try c.decodeIfPresent([WaiterETA].self, forKey: .waiters) ?? []
        etaBasis = try c.decodeIfPresent(String.self, forKey: .etaBasis)
        runners = try c.decodeIfPresent([RunnerSlots].self, forKey: .runners) ?? []
        runnersError = try c.decodeIfPresent(String.self, forKey: .runnersError)
    }

    public func eta(for operationID: String) -> WaiterETA? {
        waiters.first { $0.operationID == operationID }
    }
}

public struct PlacementRemoteConfig: Decodable, Sendable, Hashable {
    public var enabled = false
    public var placer = false
    public var node = false
    public var host: String?
    public init() {}
}

public struct PlacementDecisionRow: Decodable, Sendable, Hashable {
    public var at: Date?
    public var placement: String
    public var reason: String
    public var `class`: String?
    public var toolFamily: String?
    public var localETAMS: Int64?
    public var remoteETAMS: Int64?

    enum CodingKeys: String, CodingKey {
        case at, placement, reason
        case `class` = "class"
        case toolFamily = "tool_family"
        case localETAMS = "local_eta_ms"
        case remoteETAMS = "remote_eta_ms"
    }
}

public struct PlacementDecisionSummary: Decodable, Sendable, Hashable {
    public var total = 0
    public var local = 0
    public var remote = 0
    public var byReason: [String: Int] = [:]
    public var recent: [PlacementDecisionRow] = []

    enum CodingKeys: String, CodingKey {
        case total, local, remote
        case byReason = "by_reason"
        case recent
    }

    public init() {}

    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        total = try c.decodeIfPresent(Int.self, forKey: .total) ?? 0
        local = try c.decodeIfPresent(Int.self, forKey: .local) ?? 0
        remote = try c.decodeIfPresent(Int.self, forKey: .remote) ?? 0
        byReason = try c.decodeIfPresent([String: Int].self, forKey: .byReason) ?? [:]
        recent = try c.decodeIfPresent([PlacementDecisionRow].self, forKey: .recent) ?? []
    }
}

public struct RemoteRunRow: Decodable, Sendable, Hashable {
    public var at: Date?
    public var operation: String?
    public var profile: String?
    public var runnerHost: String?
    public var durationMS: Int64
    public var exitCode: Int?
    public var failed: Bool

    enum CodingKeys: String, CodingKey {
        case at, operation, profile
        case runnerHost = "runner_host"
        case durationMS = "duration_ms"
        case exitCode = "exit_code"
        case failed
    }
}

public struct RemoteRunSummary: Decodable, Sendable, Hashable {
    public var total = 0
    public var failed = 0
    public var p50MS: Int64 = 0
    public var p90MS: Int64 = 0
    public var recent: [RemoteRunRow] = []

    enum CodingKeys: String, CodingKey {
        case total, failed
        case p50MS = "p50_ms"
        case p90MS = "p90_ms"
        case recent
    }

    public init() {}

    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        total = try c.decodeIfPresent(Int.self, forKey: .total) ?? 0
        failed = try c.decodeIfPresent(Int.self, forKey: .failed) ?? 0
        p50MS = try c.decodeIfPresent(Int64.self, forKey: .p50MS) ?? 0
        p90MS = try c.decodeIfPresent(Int64.self, forKey: .p90MS) ?? 0
        recent = try c.decodeIfPresent([RemoteRunRow].self, forKey: .recent) ?? []
    }
}

public struct WorkQueueStall: Decodable, Sendable, Hashable {
    public var since: Date?
    public var endedAt: Date?
    public var durationMS: Int64
    public var operationID: String
    public var `class`: String?
    public var weight: Int?
    public var used: Int?
    public var effectiveCapacity: Int?
    public var level: String?
    public var decisionReason: String?

    enum CodingKeys: String, CodingKey {
        case since
        case endedAt = "ended_at"
        case durationMS = "duration_ms"
        case operationID = "operation_id"
        case `class` = "class"
        case weight, used
        case effectiveCapacity = "effective_capacity"
        case level
        case decisionReason = "decision_reason"
    }
}

public struct ParkedLeaseStall: Decodable, Sendable, Hashable, Identifiable {
    public var operationID: String
    public var `class`: String?
    public var pid: Int?
    public var weight: Int?
    public var parkedAt: Date?
    public var parkedDurationMS: Int64
    public var ageMS: Int64

    public var id: String { operationID }

    enum CodingKeys: String, CodingKey {
        case operationID = "operation_id"
        case `class` = "class"
        case pid, weight
        case parkedAt = "parked_at"
        case parkedDurationMS = "parked_duration_ms"
        case ageMS = "age_ms"
    }
}

public struct WaiterETA: Decodable, Sendable, Hashable {
    public var operationID: String
    public var position: Int?
    /// Milliseconds until the estimated start; negative means unknown.
    public var etaMS: Int64
    public var basis: String?

    enum CodingKeys: String, CodingKey {
        case operationID = "operation_id"
        case position
        case etaMS = "eta_ms"
        case basis
    }

    public var isKnown: Bool { etaMS >= 0 }
}

public struct RunnerSlots: Decodable, Sendable, Hashable, Identifiable {
    public var name: String
    public var state: String?
    public var healthy: Bool
    public var heavySlots: Int
    public var heavyJobs: Int
    public var lightSlots: Int
    public var lightJobs: Int
    public var error: String?

    public var id: String { name }

    enum CodingKeys: String, CodingKey {
        case name, state, healthy
        case heavySlots = "heavy_slots"
        case heavyJobs = "heavy_jobs"
        case lightSlots = "light_slots"
        case lightJobs = "light_jobs"
        case error
    }
}
