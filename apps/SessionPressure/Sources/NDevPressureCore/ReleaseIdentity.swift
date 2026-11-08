import Foundation

public struct ProductCapabilities: Codable, Sendable {
    public var version: String
    public var sourceRevision: String?
    public var executable: String
    public var os: String
    public var arch: String
    public var capturedAt: Date
    public var operations: [String]
    public var nativeSampling: Bool
    enum CodingKeys: String, CodingKey {
        case version, executable, os, arch, operations
        case sourceRevision = "source_revision"
        case capturedAt = "captured_at"
        case nativeSampling = "native_sampling"
    }
}
public struct CapabilitiesEnvelope: Codable, Sendable {
    public var ok: Bool
    public var capabilities: ProductCapabilities
}
public struct DiskHealthSnapshot: Codable, Sendable {
    public var capturedAt: Date
    public var available: Bool
    public var reason: String?
    public var percentageUsed: Int
    public var hostBytesWritten: UInt64
    public var mediaErrors: UInt64
    public var criticalWarning: Int
    enum CodingKeys: String, CodingKey {
        case available, reason
        case capturedAt = "captured_at"
        case percentageUsed = "percentage_used"
        case hostBytesWritten = "host_bytes_written"
        case mediaErrors = "media_errors"
        case criticalWarning = "critical_warning"
    }
}
public struct DiskHealthEnvelope: Codable, Sendable {
    public var ok: Bool
    public var snapshot: DiskHealthSnapshot
}
