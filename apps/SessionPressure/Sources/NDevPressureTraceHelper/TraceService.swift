import Darwin
import Foundation
import NDevPressureCore
import Security

final class TraceListenerDelegate: NSObject, NSXPCListenerDelegate {
    func listener(_ listener: NSXPCListener, shouldAcceptNewConnection connection: NSXPCConnection) -> Bool {
        guard Self.isTrustedClient(pid: connection.processIdentifier) else {
            return false
        }
        connection.exportedInterface = NSXPCInterface(with: NDevPressureTraceXPC.self)
        connection.exportedObject = TraceService()
        connection.resume()
        return true
    }

    private static func isTrustedClient(pid: pid_t) -> Bool {
        guard pid > 1 else { return false }
        var code: SecCode?
        let attributes = [kSecGuestAttributePid: NSNumber(value: pid)] as CFDictionary
        guard SecCodeCopyGuestWithAttributes(nil, attributes, SecCSFlags(), &code) == errSecSuccess,
              let code
        else {
            return false
        }
        var requirement: SecRequirement?
        let text = "identifier \"com.nstranquist.ndev-pressure\"" as CFString
        guard SecRequirementCreateWithString(text, SecCSFlags(), &requirement) == errSecSuccess,
              let requirement
        else {
            return false
        }
        guard SecCodeCheckValidity(code, SecCSFlags(), requirement) == errSecSuccess,
              let helperTeam = signingTeamForCurrentProcess(),
              let clientTeam = signingTeam(for: code),
              !helperTeam.isEmpty,
              clientTeam == helperTeam
        else {
            // A root helper must never trust an ad-hoc same-identifier client.
            // Development builds can validate packaging, but interactive trace
            // remains unavailable until app and helper share a real Team ID.
            return false
        }
        return true
    }

    private static func signingTeamForCurrentProcess() -> String? {
        var code: SecCode?
        guard SecCodeCopySelf(SecCSFlags(), &code) == errSecSuccess, let code else {
            return nil
        }
        return signingTeam(for: code)
    }

    private static func signingTeam(for code: SecCode) -> String? {
        var staticCode: SecStaticCode?
        guard SecCodeCopyStaticCode(code, SecCSFlags(), &staticCode) == errSecSuccess,
              let staticCode
        else {
            return nil
        }
        var information: CFDictionary?
        guard SecCodeCopySigningInformation(staticCode, SecCSFlags(rawValue: kSecCSSigningInformation), &information) == errSecSuccess,
              let values = information as? [CFString: Any],
              let team = values[kSecCodeInfoTeamIdentifier] as? String
        else {
            return nil
        }
        return team
    }
}

final class TraceService: NSObject, NDevPressureTraceXPC {
    func trace(
        pid: NSNumber,
        processStartIdentity: NSString,
        durationSeconds: NSNumber,
        authorizationExternalForm: NSData,
        reply: @escaping @Sendable ([NSString], NSNumber, NSString?) -> Void
    ) {
        let requestedPID = pid.intValue
        let requestedDuration = durationSeconds.intValue
        let expectedIdentity = processStartIdentity as String
        guard requestedPID > 1,
              (5...30).contains(requestedDuration),
              expectedIdentity.utf8.count <= 96
        else {
            reply([], false, "invalid bounded trace request")
            return
        }
        guard Self.authorizationValid(authorizationExternalForm as Data) else {
            reply([], false, "administrator authorization was not valid")
            return
        }

        DispatchQueue.global(qos: .userInitiated).async {
            do {
                let currentIdentity = try Self.processStartIdentity(pid: Int32(requestedPID))
                guard currentIdentity == expectedIdentity else {
                    throw TraceError.processIdentityChanged
                }
                let result = try Self.capturePaths(pid: requestedPID, durationSeconds: requestedDuration, expectedIdentity: expectedIdentity)
                reply(result.paths.map { $0 as NSString }, NSNumber(value: result.truncated), nil)
            } catch {
                reply([], false, error.localizedDescription as NSString)
            }
        }
    }

    private static func authorizationValid(_ data: Data) -> Bool {
        guard data.count == MemoryLayout<AuthorizationExternalForm>.size else { return false }
        var external = AuthorizationExternalForm()
        _ = withUnsafeMutableBytes(of: &external) { data.copyBytes(to: $0) }
        var authorization: AuthorizationRef?
        guard AuthorizationCreateFromExternalForm(&external, &authorization) == errAuthorizationSuccess,
              let authorization
        else {
            return false
        }
        defer { AuthorizationFree(authorization, []) }
        return kAuthorizationRightExecute.withCString { rightName in
            var item = AuthorizationItem(name: rightName, valueLength: 0, value: nil, flags: 0)
            return withUnsafeMutablePointer(to: &item) { pointer in
                var rights = AuthorizationRights(count: 1, items: pointer)
                return AuthorizationCopyRights(authorization, &rights, nil, [], nil) == errAuthorizationSuccess
            }
        }
    }

    private static func processStartIdentity(pid: Int32) throws -> String {
        var row = kinfo_proc()
        var size = MemoryLayout<kinfo_proc>.stride
        var mib = [CTL_KERN, KERN_PROC, KERN_PROC_PID, pid]
        let result = mib.withUnsafeMutableBufferPointer { buffer in
            sysctl(buffer.baseAddress, u_int(buffer.count), &row, &size, nil, 0)
        }
        guard result == 0, size >= MemoryLayout<kinfo_proc>.stride, row.kp_proc.p_pid == pid else {
            throw TraceError.processNotLive
        }
        let started = row.kp_proc.p_starttime
        guard started.tv_sec != 0 || started.tv_usec != 0 else {
            throw TraceError.processNotLive
        }
        return "darwin:\(started.tv_sec):\(started.tv_usec)"
    }

    private static func capturePaths(pid: Int, durationSeconds: Int, expectedIdentity: String) throws -> DiskPathTraceResult {
        let pipe = Pipe()
        let process = Process()
        process.executableURL = URL(fileURLWithPath: "/usr/bin/fs_usage")
        process.arguments = ["-w", "-f", "pathname", "-t", String(durationSeconds), String(pid)]
        process.environment = ["PATH": "/usr/bin:/bin:/usr/sbin:/sbin", "LANG": "C"]
        process.standardOutput = pipe
        process.standardError = pipe
        process.standardInput = FileHandle.nullDevice
        try process.run()

        // Revalidate immediately after the tracer has attached. This prevents a
        // dead target or already-reused PID from becoming an observation target.
        guard try processStartIdentity(pid: Int32(pid)) == expectedIdentity else {
            process.terminate()
            throw TraceError.processIdentityChanged
        }

        var capture = Data()
        var truncated = false
        while let chunk = try pipe.fileHandleForReading.read(upToCount: 8 * 1024), !chunk.isEmpty {
            let remaining = NDevPressureTraceContract.maximumCaptureBytes - capture.count
            if remaining > 0 {
                capture.append(chunk.prefix(remaining))
            }
            if chunk.count > remaining {
                truncated = true
            }
        }
        process.waitUntilExit()
        if process.terminationStatus != 0 && capture.isEmpty {
            throw TraceError.tracerFailed(process.terminationStatus)
        }

        let parsed = DiskTraceParser.paths(fromFSUsage: String(decoding: capture, as: UTF8.self))
        return DiskPathTraceResult(paths: parsed.paths, truncated: truncated || parsed.truncated)
    }
}

private enum TraceError: LocalizedError {
    case processNotLive
    case processIdentityChanged
    case tracerFailed(Int32)

    var errorDescription: String? {
        switch self {
        case .processNotLive: "The selected process is no longer live."
        case .processIdentityChanged: "The selected PID changed identity before tracing began."
        case .tracerFailed(let code): "The bounded path tracer exited \(code)."
        }
    }
}
