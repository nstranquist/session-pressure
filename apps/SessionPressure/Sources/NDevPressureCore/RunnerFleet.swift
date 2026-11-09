import Foundation

public struct RunnerFleet: Decodable, Sendable {
    public let generation: String
    public let digest: String
    public let hosts: [RunnerHost]
    public let observations: [RunnerObservation]?
    public let error: String?
}

public struct RunnerHost: Decodable, Identifiable, Sendable {
    public let name: String
    public let user: String
    public let state: String
    public let provider: String
    public let platform: String
    public let sourceCommit: String
    public let weight: Int
    public let heavySlots: Int
    public let lightSlots: Int
    public let profiles: [String]
    public var id: String { name }
    public var roleLabel: String { state == "reserve" ? "Explicit reserve" : state.capitalized }
    public var providerLabel: String {
        switch provider {
        case "hetzner-dedicated": return "Hetzner dedicated server"
        case "hetzner-cloud": return "Hetzner Cloud VM"
        default: return provider
        }
    }
    enum CodingKeys: String, CodingKey {
        case name, user, state, provider, platform, weight, profiles
        case sourceCommit = "source_commit"
        case heavySlots = "heavy_slots"
        case lightSlots = "light_slots"
    }
}

public struct RunnerObservation: Decodable, Sendable {
    public let host: RunnerHost
    public let observedAt: Date?
    public let healthy: Bool
    public let drained: Bool
    public let sourceCommit: String
    public let heavySlots: Int
    public let lightSlots: Int
    public let heavyJobs: Int
    public let lightJobs: Int
    public let error: String?
    public func isFresh(at now: Date = Date()) -> Bool {
        guard let observedAt else { return false }
        return now.timeIntervalSince(observedAt) < 90
    }
    enum CodingKeys: String, CodingKey {
        case host, healthy, drained, error
        case observedAt = "observed_at"
        case sourceCommit = "source_commit"
        case heavySlots = "heavy_slots"
        case lightSlots = "light_slots"
        case heavyJobs = "heavy_jobs"
        case lightJobs = "light_jobs"
    }
}
