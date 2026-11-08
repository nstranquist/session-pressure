import Foundation

public struct BrowserControlEnvelope: Decodable, Sendable {
    public let browser: BrowserControlStatus
}

public struct BrowserControlStatus: Decodable, Sendable {
    public let mode: String
    public let blocked: Bool
    public let reason: String
    public let recoverySamples: Int
    public let maxRuntimeSeconds: Int
    enum CodingKeys: String, CodingKey {
        case mode, blocked, reason
        case recoverySamples = "recovery_samples"
        case maxRuntimeSeconds = "max_runtime_seconds"
    }
}
