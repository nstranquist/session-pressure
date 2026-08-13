import Foundation

public enum NDevPressureTraceContract {
    public static let helperLabel = "com.nstranquist.ndev-pressure.trace-helper"
    public static let helperPlist = "com.nstranquist.ndev-pressure.trace-helper.plist"
    public static let maximumPaths = 100
    public static let maximumCaptureBytes = 256 * 1024
}

/// XPC values intentionally stay inside the Foundation property-list surface.
/// Paths exist only for the lifetime of one explicit response and are never
/// written by either side of this protocol.
@objc public protocol NDevPressureTraceXPC {
    func trace(
        pid: NSNumber,
        processStartIdentity: NSString,
        durationSeconds: NSNumber,
        authorizationExternalForm: NSData,
        reply: @escaping @Sendable ([NSString], NSNumber, NSString?) -> Void
    )
}

public struct DiskPathTraceResult: Sendable, Hashable {
    public var paths: [String]
    public var truncated: Bool

    public init(paths: [String], truncated: Bool) {
        self.paths = paths
        self.truncated = truncated
    }
}
