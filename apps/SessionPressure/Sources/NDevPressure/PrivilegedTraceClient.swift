import Foundation
import NDevPressureCore
import Security
import ServiceManagement

enum PrivilegedTraceError: LocalizedError {
    case helperMissing
    case helperApprovalRequired
    case helperRegistration(String)
    case authorization(OSStatus)
    case connection(String)
    case invalidReply

    var errorDescription: String? {
        switch self {
        case .helperMissing:
            "The signed trace helper is not embedded in this NDev Pressure build. Install the packaged app, then retry."
        case .helperApprovalRequired:
            "macOS requires approval for the NDev Pressure trace helper in System Settings → General → Login Items."
        case .helperRegistration(let detail):
            "Could not register the trace helper: \(detail)"
        case .authorization(let status):
            "Administrator authorization failed (\(status))."
        case .connection(let detail):
            "Could not contact the trace helper: \(detail)"
        case .invalidReply:
            "The trace helper returned an invalid response."
        }
    }
}

@MainActor
final class PrivilegedTraceClient {
    private let service = SMAppService.daemon(plistName: NDevPressureTraceContract.helperPlist)

    func trace(pid: Int, processStartIdentity: String, durationSeconds: Int) async throws -> DiskPathTraceResult {
        try ensureRegistered()
        let authorization = try authorizationExternalForm()
        let connection = NSXPCConnection(
            machServiceName: NDevPressureTraceContract.helperLabel,
            options: .privileged
        )
        connection.remoteObjectInterface = NSXPCInterface(with: NDevPressureTraceXPC.self)
        connection.resume()
        defer { connection.invalidate() }

        return try await withCheckedThrowingContinuation { continuation in
            let completion = TraceCompletion(continuation)
            guard let proxy = connection.remoteObjectProxyWithErrorHandler({ error in
                completion.fail(PrivilegedTraceError.connection(error.localizedDescription))
            }) as? NDevPressureTraceXPC else {
                completion.fail(PrivilegedTraceError.invalidReply)
                return
            }
            proxy.trace(
                pid: NSNumber(value: pid),
                processStartIdentity: processStartIdentity as NSString,
                durationSeconds: NSNumber(value: min(max(durationSeconds, 5), 30)),
                authorizationExternalForm: authorization as NSData
            ) { paths, truncated, error in
                if let error {
                    completion.fail(PrivilegedTraceError.connection(error as String))
                    return
                }
                completion.succeed(DiskPathTraceResult(
                    paths: paths.prefix(NDevPressureTraceContract.maximumPaths).map { $0 as String },
                    truncated: truncated.boolValue
                ))
            }
        }
    }

    private func ensureRegistered() throws {
        switch service.status {
        case .enabled:
            return
        case .notRegistered:
            do {
                try service.register()
            } catch {
                throw PrivilegedTraceError.helperRegistration(error.localizedDescription)
            }
            if service.status == .requiresApproval {
                throw PrivilegedTraceError.helperApprovalRequired
            }
            guard service.status == .enabled else {
                throw PrivilegedTraceError.helperRegistration("registration did not become enabled")
            }
        case .requiresApproval:
            throw PrivilegedTraceError.helperApprovalRequired
        case .notFound:
            throw PrivilegedTraceError.helperMissing
        @unknown default:
            throw PrivilegedTraceError.helperRegistration("unknown service status \(service.status.rawValue)")
        }
    }

    private func authorizationExternalForm() throws -> Data {
        var authorization: AuthorizationRef?
        let status = kAuthorizationRightExecute.withCString { rightName -> OSStatus in
            var item = AuthorizationItem(name: rightName, valueLength: 0, value: nil, flags: 0)
            return withUnsafeMutablePointer(to: &item) { pointer -> OSStatus in
                var rights = AuthorizationRights(count: 1, items: pointer)
                let flags: AuthorizationFlags = [.interactionAllowed, .extendRights, .preAuthorize]
                let create = AuthorizationCreate(nil, nil, [], &authorization)
                guard create == errAuthorizationSuccess, let authorization else { return create }
                return AuthorizationCopyRights(authorization, &rights, nil, flags, nil)
            }
        }
        guard status == errAuthorizationSuccess, let authorization else {
            throw PrivilegedTraceError.authorization(status)
        }
        defer { AuthorizationFree(authorization, []) }
        var external = AuthorizationExternalForm()
        let externalStatus = AuthorizationMakeExternalForm(authorization, &external)
        guard externalStatus == errAuthorizationSuccess else {
            throw PrivilegedTraceError.authorization(externalStatus)
        }
        return withUnsafeBytes(of: external) { Data($0) }
    }
}

/// NSXPC can race an interruption with a reply. Resume the Swift continuation
/// exactly once without retaining the connection beyond the explicit request.
private final class TraceCompletion: @unchecked Sendable {
    private let lock = NSLock()
    private var continuation: CheckedContinuation<DiskPathTraceResult, Error>?

    init(_ continuation: CheckedContinuation<DiskPathTraceResult, Error>) {
        self.continuation = continuation
    }

    func succeed(_ result: DiskPathTraceResult) {
        finish(.success(result))
    }

    func fail(_ error: Error) {
        finish(.failure(error))
    }

    private func finish(_ result: Result<DiskPathTraceResult, Error>) {
        lock.lock()
        let current = continuation
        continuation = nil
        lock.unlock()
        current?.resume(with: result)
    }
}
