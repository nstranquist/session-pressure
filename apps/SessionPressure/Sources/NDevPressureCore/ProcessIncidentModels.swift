import Foundation

public struct ProcessIncidentEnvelope: Decodable, Sendable {
    public let ok: Bool
    public let report: ProcessIncidentReport
}

public struct ProcessIncidentStopEnvelope: Decodable, Sendable {
    public let ok: Bool
    public let incident: ProcessIncident
}

public struct ProcessIncidentReport: Decodable, Sendable {
    public let schemaVersion: Int
    public let updatedAt: Date?
    public let incidents: [ProcessIncident]
    enum CodingKeys: String, CodingKey { case schemaVersion = "schema_version", updatedAt = "updated_at", incidents }
}

public struct ProcessIncident: Decodable, Sendable, Identifiable {
    public let id: String
    public let pid: Int
    public let ppid: Int?
    public let startIdentity: String
    public let source: String?
    public let executable: String
    public let parentExecutable: String?
    public let sessionID: String?
    public let uid: Int
    public let cpuPercent: Double?
    public let rssMb: Double?
    public let ageSeconds: Int64?
    public let reason: String
    public let firstSeen: Date
    public let lastSeen: Date
    public let sustainedSeconds: Int64?
    public let state: String
    public let actions: [ProcessIncidentAction]?

    enum CodingKeys: String, CodingKey {
        case id, pid, ppid, executable, uid, reason, state, actions, source
        case startIdentity = "start_identity", parentExecutable = "parent_executable"
        case sessionID = "session_id", cpuPercent = "cpu_percent", rssMb = "rss_mb"
        case ageSeconds = "age_seconds", firstSeen = "first_seen", lastSeen = "last_seen"
        case sustainedSeconds = "sustained_seconds"
    }

    public var canStop: Bool { source != "operator_record" && pid > 1 && !startIdentity.isEmpty && (state == "active" || state == "stop_requested") }
    public var canForceStop: Bool { canStop && state == "stop_requested" }

    public var reasonLabel: String {
        switch reason {
        case "orphaned_sustained_cpu", "init_parent_sustained_cpu": return "High CPU in a command with an init-owned parent"
        case "sustained_cpu": return "Sustained high CPU"
        default: return reason.replacingOccurrences(of: "_", with: " ")
        }
    }
}

public struct ProcessIncidentAction: Decodable, Sendable {
    public let at: Date
    public let signal: String
    public let result: String
    public let error: String?
    public var signalLabel: String { signal.isEmpty || signal == "NONE" ? "No signal" : signal }
}

