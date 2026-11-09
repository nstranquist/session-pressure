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
        try await ensureRegistered()
        let authorization = try authorizationExternalForm()
        // External forms refer to live authorization state. Keep our reference
        // until the helper replies (or the bounded connection attempt ends).
        defer { AuthorizationFree(authorization.reference, []) }
        let connection = NSXPCConnection(
            machServiceName: NDevPressureTraceContract.helperLabel,
            options: .privileged
        )
        connection.remoteObjectInterface = NSXPCInterface(with: NDevPressureTraceXPC.self)
        connection.resume()
        defer { connection.invalidate() }

        return try await withCheckedThrowingContinuation { continuation in
            let completion = TraceCompletion(continuation)
            connection.interruptionHandler = { @Sendable in
                completion.fail(PrivilegedTraceError.connection("helper connection interrupted"))
            }
            connection.invalidationHandler = { @Sendable in
                completion.fail(PrivilegedTraceError.connection("helper connection invalidated"))
            }
            let deadline = completion.makeDeadline()
            DispatchQueue.global().asyncAfter(deadline: .now() + Double(min(max(durationSeconds, 5), 30) + 10), execute: deadline)
            guard let proxy = connection.remoteObjectProxyWithErrorHandler({ @Sendable error in
                completion.fail(PrivilegedTraceError.connection(error.localizedDescription))
            }) as? NDevPressureTraceXPC else {
                completion.fail(PrivilegedTraceError.invalidReply)
                return
            }
            proxy.trace(
                pid: NSNumber(value: pid),
                processStartIdentity: processStartIdentity as NSString,
                durationSeconds: NSNumber(value: min(max(durationSeconds, 5), 30)),
                authorizationExternalForm: authorization.data as NSData
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

    private func ensureRegistered() async throws {
        let contents = Bundle.main.bundleURL.appendingPathComponent("Contents/Library")
        let helper = contents.appendingPathComponent("LaunchServices/NDevPressureTraceHelper")
        let plist = contents.appendingPathComponent("LaunchDaemons/\(NDevPressureTraceContract.helperPlist)")
        let embedded = FileManager.default.isExecutableFile(atPath: helper.path)
            && FileManager.default.fileExists(atPath: plist.path)
        guard let build = Bundle.main.object(forInfoDictionaryKey: "CFBundleVersion") as? String,
              !build.isEmpty else {
            throw PrivilegedTraceError.helperRegistration("app build identity is missing")
        }
        let registrationKey = "traceHelperRegisteredBuild"
        let refreshed = try await TraceHelperRegistration.refreshIfNeeded(
            helperIsEmbedded: embedded,
            status: service.status,
            installedBuild: build,
            registeredBuild: UserDefaults.standard.string(forKey: registrationKey),
            unregister: { try await self.service.unregister() }
        )
        try await TraceHelperRegistration.ensureAfterRefresh(
            helperIsEmbedded: embedded,
            refreshed: refreshed,
            status: { self.service.status },
            register: { try self.service.register() },
            pause: { milliseconds in
                try await Task.sleep(for: .milliseconds(milliseconds))
            }
        )
        UserDefaults.standard.set(build, forKey: registrationKey)
    }

    private func authorizationExternalForm() throws -> (data: Data, reference: AuthorizationRef) {
        var authorization: AuthorizationRef?
        let create = AuthorizationCreate(nil, nil, [], &authorization)
        guard create == errAuthorizationSuccess, let authorization else {
            throw PrivilegedTraceError.authorization(create)
        }
        // Failed or cancelled requests still release their reference here.
        // A successful request transfers ownership to trace's defer.
        var transferred = false
        defer { if !transferred { AuthorizationFree(authorization, []) } }
        let status = kAuthorizationRightExecute.withCString { rightName -> OSStatus in
            var item = AuthorizationItem(name: rightName, valueLength: 0, value: nil, flags: 0)
            return withUnsafeMutablePointer(to: &item) { pointer -> OSStatus in
                var rights = AuthorizationRights(count: 1, items: pointer)
                let flags: AuthorizationFlags = [.interactionAllowed, .extendRights, .preAuthorize]
                return AuthorizationCopyRights(authorization, &rights, nil, flags, nil)
            }
        }
        guard status == errAuthorizationSuccess else {
            throw PrivilegedTraceError.authorization(status)
        }
        var external = AuthorizationExternalForm()
        let externalStatus = AuthorizationMakeExternalForm(authorization, &external)
        guard externalStatus == errAuthorizationSuccess else {
            throw PrivilegedTraceError.authorization(externalStatus)
        }
        let data = withUnsafeBytes(of: external) { Data($0) }
        transferred = true
        return (data, authorization)
    }
}

/// A framework lookup failure does not prove that the embedded files are missing.
/// Try normal registration once, then require an enabled service or user approval.
@MainActor
enum TraceHelperRegistration {
    /// Updated executables must be re-registered. Wait for the old service to
    /// stop before registering the current bundle. Only an explicit trace calls
    /// this path; a service awaiting user approval is never unregistered.
    @discardableResult
    static func refreshIfNeeded(
        helperIsEmbedded: Bool,
        status: SMAppService.Status,
        installedBuild: String,
        registeredBuild: String?,
        unregister: () async throws -> Void
    ) async throws -> Bool {
        guard helperIsEmbedded else { throw PrivilegedTraceError.helperMissing }
        guard status == .enabled, registeredBuild != installedBuild else { return false }
        do {
            try await unregister()
            // Service Management can finish bootout before its registration
            // state is ready. Apple's DTS recommends a main-run-loop turn.
            await withCheckedContinuation { continuation in
                DispatchQueue.main.async { continuation.resume() }
            }
        } catch {
            let detail = error as NSError
            throw PrivilegedTraceError.helperRegistration(
                "could not refresh the previous helper: \(detail.localizedDescription) [\(detail.domain):\(detail.code)]"
            )
        }
        return true
    }

    /// smd can acknowledge unregister before its old disabled entry settles.
    /// Retry only the observed SM operation-not-permitted transition after our
    /// own successful refresh. Each attempt still uses the normal approval gate.
    static func ensureAfterRefresh(
        helperIsEmbedded: Bool,
        refreshed: Bool,
        status: () -> SMAppService.Status,
        register: () throws -> Void,
        pause: (Int) async throws -> Void
    ) async throws {
        let delays = [100, 200, 400, 800, 1600] // 3.1 seconds total, only on failure.
        var attempt = 0
        while true {
            try Task.checkCancellation()
            var transientRefreshFailure = false
            do {
                try ensureRegistered(helperIsEmbedded: helperIsEmbedded, status: status) {
                    do {
                        try register()
                    } catch {
                        let detail = error as NSError
                        transientRefreshFailure = refreshed
                            && detail.domain == "SMAppServiceErrorDomain"
                            && detail.code == 1
                        throw error
                    }
                }
                return
            } catch {
                guard transientRefreshFailure, attempt < delays.count else { throw error }
                // Approval changes stop immediately, without another register.
                if status() == .requiresApproval {
                    throw PrivilegedTraceError.helperApprovalRequired
                }
                try await pause(delays[attempt])
                attempt += 1
            }
        }
    }

    static func ensureRegistered(
        helperIsEmbedded: Bool,
        status: () -> SMAppService.Status,
        register: () throws -> Void
    ) throws {
        guard helperIsEmbedded else { throw PrivilegedTraceError.helperMissing }
        switch status() {
        case .enabled:
            return
        case .notRegistered, .notFound:
            do {
                try register()
            } catch {
                let detail = error as NSError
                throw PrivilegedTraceError.helperRegistration(
                    "\(detail.localizedDescription) [\(detail.domain):\(detail.code)]"
                )
            }
            let registeredStatus = status()
            if registeredStatus == .requiresApproval {
                throw PrivilegedTraceError.helperApprovalRequired
            }
            guard registeredStatus == .enabled else {
                throw PrivilegedTraceError.helperRegistration(
                    "registration did not become enabled (status \(registeredStatus.rawValue))"
                )
            }
        case .requiresApproval:
            throw PrivilegedTraceError.helperApprovalRequired
        @unknown default:
            throw PrivilegedTraceError.helperRegistration("unknown service status \(status().rawValue)")
        }
    }
}

/// NSXPC can race an interruption with a reply. Resume the Swift continuation
/// exactly once without retaining the connection beyond the explicit request.
final class TraceCompletion: @unchecked Sendable {
    private let lock = NSLock()
    private var continuation: CheckedContinuation<DiskPathTraceResult, Error>?
    private var deadline: DispatchWorkItem?

    // Construct the dispatch callback outside MainActor isolation. Dispatch
    // executes it on a background queue; an inherited actor check traps there.
    func makeDeadline() -> DispatchWorkItem {
        let item = DispatchWorkItem { [self] in
            fail(PrivilegedTraceError.connection("bounded trace deadline exceeded"))
        }
        setDeadline(item)
        return item
    }

    func setDeadline(_ item: DispatchWorkItem) {
        lock.lock(); defer { lock.unlock() }
        if continuation == nil { item.cancel() } else { deadline = item }
    }

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
        deadline?.cancel()
        deadline = nil
        lock.unlock()
        current?.resume(with: result)
    }
}
