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
}
public struct DiskHealthEnvelope: Codable, Sendable {
    public var ok: Bool
    public var snapshot: DiskHealthSnapshot
}
