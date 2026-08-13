import Foundation
import Darwin

public enum NDevPressureClientError: Error, LocalizedError, Sendable {
    case binaryNotFound
    case launchFailed(String)
    case nonZeroExit(code: Int32, stderr: String, stdout: String)
    case malformedJSON(String)
    case emptyResponse
    case timedOut(seconds: Double)
    case responseTooLarge(limit: Int)

    public var errorDescription: String? {
        switch self {
        case .binaryNotFound:
            return "ndev not found. Install nicos-dev or set NDEV_BIN / NDEV_PATH."
        case .launchFailed(let detail):
            return "Failed to launch ndev: \(detail)"
        case .nonZeroExit(let code, let stderr, _):
            let detail = stderr.trimmingCharacters(in: .whitespacesAndNewlines)
            return detail.isEmpty ? "ndev exited \(code)" : detail
        case .malformedJSON(let message):
            return "Could not decode ndev JSON: \(message)"
        case .emptyResponse:
            return "ndev returned empty output"
        case .timedOut(let seconds):
            return "ndev did not finish within \(String(format: "%.0f", seconds)) seconds"
        case .responseTooLarge(let limit):
            return "ndev response exceeded the \(limit)-byte UI contract"
        }
    }
}

/// Thread-safe, fixed-retention drain for a child pipe. The reader always
/// consumes the complete stream so the child cannot deadlock, while only the
/// configured prefix remains in memory.
private final class BoundedCaptureBuffer: @unchecked Sendable {
    private let lock = NSLock()
    private let limit: Int
    private var bytes = Data()
    private var overflowed = false

    init(limit: Int) {
        self.limit = max(1, limit)
    }

    func append(_ chunk: Data) {
        lock.lock()
        defer { lock.unlock() }
        let remaining = max(0, limit - bytes.count)
        if remaining > 0 {
            bytes.append(chunk.prefix(remaining))
        }
        if chunk.count > remaining {
            overflowed = true
        }
    }

    func snapshot() -> (data: Data, overflowed: Bool) {
        lock.lock()
        defer { lock.unlock() }
        return (bytes, overflowed)
    }
}

/// Thin, process-isolated client over `ndev session pressure` JSON contracts.
/// The app never reimplements sampling; the CLI remains the control plane.
public struct NDevPressureClient: Sendable {
    public let binaryPath: String
    private let environment: [String: String]
    private let apiClient: NDevPressureAPIClient?
    private let maxOutputBytes: Int
    private let commandTimeoutSeconds: Double

    public init(
        binaryPath: String? = nil,
        environment: [String: String] = ProcessInfo.processInfo.environment,
        maxOutputBytes: Int = 8 * 1024 * 1024,
        commandTimeoutSeconds: Double = 30
    ) throws {
        guard let path = binaryPath ?? Self.resolveBinary(environment: environment) else {
            throw NDevPressureClientError.binaryNotFound
        }
        self.binaryPath = path
        self.environment = Self.sanitizedEnvironment(environment)
        self.apiClient = NDevPressureAPIClient(environment: environment)
        self.maxOutputBytes = maxOutputBytes
        self.commandTimeoutSeconds = max(1, commandTimeoutSeconds)
    }

    public static func resolveBinary(
        environment: [String: String] = ProcessInfo.processInfo.environment
    ) -> String? {
        let home = FileManager.default.homeDirectoryForCurrentUser.path
        let candidates: [String?] = [
            environment["NDEV_PRESSURE_BIN"],
            environment["SESSION_PRESSURE_BIN"],
            "\(home)/.local/bin/ndev-pressure",
            "\(home)/.local/bin/session-pressure",
            environment["NICOS_DEV_PATH"].map { "\($0)/bin/ndev-pressure" },
            environment["NICOS_TOOLS_PATH"].map { "\($0)/nicos-dev/bin/ndev-pressure" },
            "\(home)/dev/nicos-tools/nicos-dev/bin/ndev-pressure",
            "/opt/homebrew/bin/ndev-pressure",
            "/usr/local/bin/ndev-pressure",
            // Compatibility fallback while ndev still wraps the product CLI.
            environment["NDEV_BIN"],
            environment["NDEV_PATH"],
            environment["NICOS_DEV_PATH"].map { "\($0)/bin/ndev" },
            environment["NICOS_TOOLS_PATH"].map { "\($0)/nicos-dev/bin/ndev" },
            "\(home)/.local/bin/ndev",
            "\(home)/dev/nicos-tools/nicos-dev/bin/ndev",
            "\(home)/dev/nicos-tools/nicos-dev/bin/ndev-go",
            "/opt/homebrew/bin/ndev",
            "/usr/local/bin/ndev",
        ]
        for candidate in candidates.compactMap({ $0 }).filter({ !$0.isEmpty }) {
            if FileManager.default.isExecutableFile(atPath: candidate) {
                return candidate
            }
        }
        return nil
    }

    public static func sanitizedEnvironment(
        _ ambient: [String: String] = ProcessInfo.processInfo.environment
    ) -> [String: String] {
        var result: [String: String] = [
            "HOME": ambient["HOME"] ?? FileManager.default.homeDirectoryForCurrentUser.path,
            "PATH": "/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:\(ambient["HOME"] ?? "")/.local/bin",
            "TMPDIR": ambient["TMPDIR"] ?? FileManager.default.temporaryDirectory.path,
        ]
        for name in ["USER", "LOGNAME", "LANG", "LC_ALL", "LC_CTYPE", "TERM", "NDEV_PRESSURE_BIN", "NDEV_BIN", "NDEV_PATH", "NICOS_DEV_PATH", "NICOS_TOOLS_PATH", "NDEV_SESSION_PRESSURE_HOME", "NDEV_PRESSURE_API_URL", "NDEV_PRESSURE_API_TOKEN", "NDEV_PRESSURE_API_TOKEN_FILE", "NDEV_PRESSURE_API_TIMEOUT_SECONDS"] {
            if let value = ambient[name], !value.isEmpty {
                result[name] = value
            }
        }
        return result
    }

    // MARK: - Reads

    public func status(live: Bool = false, full: Bool = false) async throws -> StatusEnvelope {
        var query: [String: String] = [:]
        if live { query["live"] = "true" }
        if full { query["full"] = "true" }
        if let api: StatusEnvelope = try await apiProjection(StatusEnvelope.self, route: "/v1/pressure/status", query: query) {
            return api
        }
        var args = ["--json", "session", "pressure", "status"]
        if live { args.append("--live") }
        if full { args.append("--full") }
        return try await runJSON(StatusEnvelope.self, arguments: args)
    }

    /// One composite read replacing the status + work + check (+ doctor +
    /// calibration) fan-out. Sections are opt-in so a menu-bar-only refresh
    /// stays at the cheapest possible cost.
    public func board(
        live: Bool = false,
        full: Bool = false,
        include: [String] = []
    ) async throws -> BoardEnvelope {
        var query: [String: String] = [:]
        if live { query["live"] = "true" }
        if full { query["full"] = "true" }
        if !include.isEmpty { query["include"] = include.joined(separator: ",") }
        if let api: BoardEnvelope = try await apiProjection(BoardEnvelope.self, route: "/v1/pressure/board", query: query) {
            return api
        }
        var args = ["--json", "session", "pressure", "board"]
        if live { args.append("--live") }
        if full { args.append("--full") }
        if !include.isEmpty {
            args.append(contentsOf: ["--include", include.joined(separator: ",")])
        }
        // A blocked admission is a policy decision the board still reports.
        return try await runJSON(BoardEnvelope.self, arguments: args, acceptNonZero: true)
    }

    public func diskWriteStatus(live: Bool = false, full: Bool = false) async throws -> DiskWriteStatusEnvelope {
        var query = ["view": "status"]
        if live { query["live"] = "true" }
        if full { query["full"] = "true" }
        if let api: DiskWriteStatusEnvelope = try await apiProjection(DiskWriteStatusEnvelope.self, route: "/v1/pressure/io", query: query) {
            return api
        }
        var args = ["--json", "session", "pressure", "io", "status"]
        if live { args.append("--live") }
        if full { args.append("--full") }
        return try await runJSON(
            DiskWriteStatusEnvelope.self,
            arguments: args,
            maximumResponseBytes: full ? 32 * 1024 : 4 * 1024
        )
    }

    public func diskWriteTop(live: Bool = false, limit: Int = 5) async throws -> DiskWriteTopEnvelope {
        let boundedLimit = min(max(limit, 1), 20)
        var query = ["view": "top", "limit": String(boundedLimit)]
        if live { query["live"] = "true" }
        if let api: DiskWriteTopEnvelope = try await apiProjection(DiskWriteTopEnvelope.self, route: "/v1/pressure/io", query: query) {
            return api
        }
        var args = ["--json", "session", "pressure", "io", "top"]
        if live { args.append("--live") }
        args.append(contentsOf: ["--limit", String(boundedLimit)])
        return try await runJSON(
            DiskWriteTopEnvelope.self,
            arguments: args,
            maximumResponseBytes: boundedLimit <= 5 ? 6 * 1024 : 24 * 1024
        )
    }

    public func diskWriteHistory(since: String = "24h", limit: Int = 20) async throws -> DiskWriteHistoryEnvelope {
        let boundedLimit = min(max(limit, 1), 200)
        if let api: DiskWriteHistoryEnvelope = try await apiProjection(
            DiskWriteHistoryEnvelope.self,
            route: "/v1/pressure/io",
            query: ["view": "history", "since": since, "limit": String(boundedLimit)]
        ) {
            return api
        }
        return try await runJSON(
            DiskWriteHistoryEnvelope.self,
            arguments: [
                "--json", "session", "pressure", "io", "history",
                "--since", since,
                "--limit", String(boundedLimit),
            ],
            maximumResponseBytes: boundedLimit <= 20 ? 12 * 1024 : 96 * 1024
        )
    }

    public func diskWritePolicyShow() async throws -> DiskWritePolicyEnvelope {
        if let api: DiskWritePolicyEnvelope = try await apiProjection(DiskWritePolicyEnvelope.self, route: "/v1/pressure/io", query: ["view": "policy"]) {
            return api
        }
        return try await runJSON(
            DiskWritePolicyEnvelope.self,
            arguments: ["--json", "session", "pressure", "io", "policy", "show"],
            maximumResponseBytes: 4 * 1024
        )
    }

    public func diskWritePolicyObserve() async throws -> DiskWritePolicyEnvelope {
        try await diskWritePolicyMutation("observe")
    }

    public func diskWritePolicyEnableAlerts() async throws -> DiskWritePolicyEnvelope {
        try await diskWritePolicyMutation("enable-alerts")
    }

    public func diskWritePolicyDisable() async throws -> DiskWritePolicyEnvelope {
        try await diskWritePolicyMutation("disable")
    }

    public func diskWriteTraceHandoff(pid: Int, durationSeconds: Int = 15) async throws -> DiskWriteTraceEnvelope {
        let duration = min(max(durationSeconds, 5), 30)
        return try await runJSON(
            DiskWriteTraceEnvelope.self,
            arguments: [
                "--json", "session", "pressure", "io", "trace",
                "--pid", String(pid),
                "--duration", "\(duration)s",
            ],
            maximumResponseBytes: 4 * 1024
        )
    }

    private func diskWritePolicyMutation(_ action: String) async throws -> DiskWritePolicyEnvelope {
        try await runJSON(
            DiskWritePolicyEnvelope.self,
            arguments: ["--json", "session", "pressure", "io", "policy", action],
            maximumResponseBytes: 4 * 1024
        )
    }

    public func snapshot() async throws -> SnapshotEnvelope {
        try await runJSON(SnapshotEnvelope.self, arguments: ["--json", "session", "pressure", "snapshot"])
    }

    public func check() async throws -> CheckEnvelope {
        // Admission may return non-zero when blocked; still decode stdout.
        try await runJSON(CheckEnvelope.self, arguments: ["--json", "session", "pressure", "check"], acceptNonZero: true)
    }

    public func policyShow() async throws -> PolicyEnvelope {
        if let api: PolicyEnvelope = try await apiProjection(PolicyEnvelope.self, route: "/v1/pressure/policy") {
            return api
        }
        return try await runJSON(PolicyEnvelope.self, arguments: ["--json", "session", "pressure", "policy", "show"])
    }

    public func policyProfileShow() async throws -> PolicyProfilesEnvelope {
        try await runJSON(PolicyProfilesEnvelope.self, arguments: ["--json", "session", "pressure", "policy", "profile", "show"])
    }

    public func policyProfileApply(_ profile: String, withAutoShed: Bool = false) async throws -> PolicyEnvelope {
        var args = ["--json", "session", "pressure", "policy", "profile", "apply", profile]
        if withAutoShed { args.append("--with-auto-shed") }
        return try await runJSON(PolicyEnvelope.self, arguments: args)
    }

    public func workStatus() async throws -> WorkEnvelope {
        if let api: WorkEnvelope = try await apiProjection(WorkEnvelope.self, route: "/v1/pressure/work", query: ["view": "status"]) {
            return api
        }
        return try await runJSON(WorkEnvelope.self, arguments: ["--json", "session", "pressure", "work", "status"])
    }

    public func workReport(since: String = "24h") async throws -> WorkReportEnvelope {
        if let api: WorkReportEnvelope = try await apiProjection(WorkReportEnvelope.self, route: "/v1/pressure/work", query: ["view": "report", "since": since]) {
            return api
        }
        return try await runJSON(
            WorkReportEnvelope.self,
            arguments: ["--json", "session", "pressure", "work", "report", "--since", since]
        )
    }

    public func workStats(since: String = "24h") async throws -> WorkStatsEnvelope {
        if let api: WorkStatsEnvelope = try await apiProjection(WorkStatsEnvelope.self, route: "/v1/pressure/work", query: ["view": "stats", "since": since]) {
            return api
        }
        return try await runJSON(
            WorkStatsEnvelope.self,
            arguments: ["--json", "session", "pressure", "work", "stats", "--since", since]
        )
    }

    public func doctor() async throws -> DoctorEnvelope {
        if let api: DoctorEnvelope = try await apiProjection(DoctorEnvelope.self, route: "/v1/pressure/doctor") {
            return api
        }
        return try await runJSON(DoctorEnvelope.self, arguments: ["--json", "session", "pressure", "doctor"])
    }

    /// `operationID` narrows the ledger read to one operation's lifecycle. The
    /// inspector previously pulled the whole window and discarded nearly all of
    /// it on every row click.
    public func workHistory(
        since: String = "24h",
        limit: Int = 400,
        operationID: String? = nil
    ) async throws -> WorkHistoryEnvelope {
        var args = [
            "--json", "session", "pressure", "work", "history",
            "--since", since,
            "--limit", String(limit),
        ]
        if let operationID, !operationID.isEmpty {
            args.append(contentsOf: ["--operation-id", operationID])
        }
        return try await runJSON(WorkHistoryEnvelope.self, arguments: args)
    }

    public func monitorStatus() async throws -> MonitorStatusEnvelope {
        try await runJSON(MonitorStatusEnvelope.self, arguments: ["--json", "session", "pressure", "monitor", "status"])
    }

    public func idle(limit: Int = 20, minAge: String = "12h") async throws -> IdleEnvelope {
        return try await runJSON(
            IdleEnvelope.self,
            arguments: [
                "--json", "session", "pressure", "idle",
                "--limit", String(limit),
                "--min-age", minAge,
            ]
        )
    }

    public func telemetry(limit: Int = 40, since: String = "24h") async throws -> TelemetryEnvelope {
        let boundedLimit = min(max(limit, 1), 200)
        if let api: TelemetryEnvelope = try await apiProjection(TelemetryEnvelope.self, route: "/v1/pressure/telemetry", query: ["limit": String(boundedLimit), "since": since]) {
            return api
        }
        return try await runJSON(
            TelemetryEnvelope.self,
            arguments: [
                "--json", "session", "pressure", "telemetry",
                "--limit", String(limit),
                "--since", since,
            ]
        )
    }

    /// Pane-aware composite read. Compact status, work, and admission are the
    /// steady-state path; larger diagnostics are parallel and opt-in. A prior
    /// board preserves inactive-pane detail without refetching it.
    public func refreshBoard(
        live: Bool = false,
        includeIdle: Bool = true,
        includeTelemetry: Bool = true,
        fullStatus: Bool = true,
        includePolicy: Bool = true,
        includeMonitor: Bool = true,
        includeDoctor: Bool = true,
        includeCalibration: Bool = true,
        previous: PressureBoard? = nil
    ) async throws -> PressureBoard {
        // One process beats five. Only fall back to the per-contract fan-out
        // when the installed ndev predates the composite verb — a decode or
        // transport failure must not silently halve the board's fidelity.
        do {
            var include: [String] = []
            if includeDoctor { include.append("doctor") }
            if includeCalibration { include.append("calibration") }
            if includePolicy { include.append("policy") }
            if includeMonitor { include.append("monitor") }
            if includeIdle { include.append("idle") }
            if includeTelemetry { include.append("telemetry") }
            let env = try await board(live: live, full: fullStatus, include: include)
            return PressureBoard(composite: env, previous: previous, binaryPath: binaryPath, live: live)
        } catch let error as NDevPressureClientError {
            guard Self.indicatesMissingBoardVerb(error) else { throw error }
        }
        return try await refreshBoardFanOut(
            live: live, includeIdle: includeIdle, includeTelemetry: includeTelemetry,
            fullStatus: fullStatus, includePolicy: includePolicy, includeMonitor: includeMonitor,
            includeDoctor: includeDoctor, includeCalibration: includeCalibration, previous: previous
        )
    }

    /// `session pressure board` is rejected as an unknown subcommand (exit 2) by
    /// any helper older than it. That exact shape is the only fallback trigger.
    static func indicatesMissingBoardVerb(_ error: NDevPressureClientError) -> Bool {
        guard case .nonZeroExit(let code, let stderr, _) = error, code == 2 else { return false }
        return stderr.contains("unknown subcommand") && stderr.contains("board")
    }

    private func refreshBoardFanOut(
        live: Bool,
        includeIdle: Bool,
        includeTelemetry: Bool,
        fullStatus: Bool,
        includePolicy: Bool,
        includeMonitor: Bool,
        includeDoctor: Bool,
        includeCalibration: Bool,
        previous: PressureBoard?
    ) async throws -> PressureBoard {
        async let statusTask = status(live: live, full: fullStatus)
        async let workTask = workStatus()
        async let checkTask = check()

        let policyTask: Task<PolicyEnvelope?, Never>? = includePolicy ? Task { try? await policyShow() } : nil
        let monitorTask: Task<MonitorStatusEnvelope?, Never>? = includeMonitor ? Task { try? await monitorStatus() } : nil
        let doctorTask: Task<DoctorEnvelope?, Never>? = includeDoctor ? Task { try? await doctor() } : nil
        let reportTask: Task<WorkReportEnvelope?, Never>? = includeCalibration ? Task { try? await workReport(since: "24h") } : nil
        let idleTask: Task<IdleEnvelope?, Never>? = includeIdle ? Task { try? await idle() } : nil
        let telemetryTask: Task<TelemetryEnvelope?, Never>? = includeTelemetry ? Task { try? await telemetry() } : nil

        let statusEnv = try await statusTask
        let workEnv = try await workTask
        let checkEnv = try await checkTask
        let policyEnv = await policyTask?.value
        let monitorEnv = await monitorTask?.value
        let doctorEnv = await doctorTask?.value
        let reportEnv = await reportTask?.value
        let idleEnv = await idleTask?.value
        let telemetryEnv = await telemetryTask?.value

        let sampled = statusEnv.snapshot ?? statusEnv.snapshotSummary ?? statusEnv.latestMonitor ?? statusEnv.latestMonitorSummary
        let boardSnapshot: PressureSnapshot
        if let sampled, sampled.schemaVersion == nil, let previous {
            boardSnapshot = previous.snapshot.applyingCompact(sampled)
        } else {
            boardSnapshot = sampled ?? previous?.snapshot ?? PressureSnapshot()
        }

        return PressureBoard(
            snapshot: boardSnapshot,
            health: statusEnv.health,
            coverage: statusEnv.coverage ?? previous?.coverage,
            admission: checkEnv.admission,
            policy: policyEnv?.policy ?? previous?.policy,
            work: workEnv.work,
            launchd: monitorEnv?.launchd ?? previous?.launchd,
            doctor: doctorEnv ?? previous?.doctor,
            calibration: reportEnv?.calibration ?? previous?.calibration,
            hasRecoveryHint: statusEnv.hasRecoveryHint ?? false,
            recoveryHint: statusEnv.recoveryHint,
            idleCandidates: idleEnv?.inventory?.candidates ?? previous?.idleCandidates ?? [],
            telemetryEvents: telemetryEnv?.events ?? previous?.telemetryEvents ?? [],
            reliefActions: telemetryEnv?.actions ?? previous?.reliefActions ?? [],
            binaryPath: binaryPath,
            refreshedAt: Date(),
            liveSample: live
        )
    }

    // MARK: - Mutations

    public func policyEnable(autoShed: Bool = true) async throws -> PolicyEnvelope {
        var args = ["--json", "session", "pressure", "policy", "enable"]
        if !autoShed { args.append("--no-auto-shed") }
        return try await runJSON(PolicyEnvelope.self, arguments: args)
    }

    public func policyObserve() async throws -> PolicyEnvelope {
        try await runJSON(PolicyEnvelope.self, arguments: ["--json", "session", "pressure", "policy", "observe"])
    }

    public func policyInit(force: Bool = false) async throws -> PolicyEnvelope {
        var args = ["--json", "session", "pressure", "policy", "init"]
        if force { args.append("--force") }
        return try await runJSON(PolicyEnvelope.self, arguments: args)
    }

    public func monitorInstall(enforce: Bool = false) async throws -> MonitorStatusEnvelope {
        var args = ["--json", "session", "pressure", "monitor", "install"]
        if enforce { args.append("--enforce") }
        return try await runJSON(MonitorStatusEnvelope.self, arguments: args)
    }

    public func monitorUninstall() async throws -> MonitorStatusEnvelope {
        try await runJSON(MonitorStatusEnvelope.self, arguments: ["--json", "session", "pressure", "monitor", "uninstall"])
    }

    public func monitorOnce() async throws -> SnapshotEnvelope {
        try await runJSON(SnapshotEnvelope.self, arguments: ["--json", "session", "pressure", "monitor", "once"])
    }

    public func idleApply(rootPID: Int, sessionID: String) async throws -> IdleEnvelope {
        try await runJSON(
            IdleEnvelope.self,
            arguments: [
                "--json", "session", "pressure", "idle",
                "--apply",
                "--root-pid", String(rootPID),
                "--session-id", sessionID,
            ],
            acceptNonZero: true
        )
    }

    public func workOverride(operationID: String) async throws -> WorkOverrideEnvelope {
        try await runJSON(
            WorkOverrideEnvelope.self,
            arguments: [
                "--json", "session", "pressure", "work", "override",
                "--operation-id", operationID,
                "--confirm",
            ]
        )
    }

    /// Releases the whole pinned sequence so the queue returns to ordinary
    /// policy. Active leases keep running.
    public func workOverrideClear() async throws -> WorkOverrideEnvelope {
        try await runJSON(
            WorkOverrideEnvelope.self,
            arguments: [
                "--json", "session", "pressure", "work", "override",
                "--clear",
                "--confirm",
            ]
        )
    }

    /// Pins every currently queued waiter as one ordered promotion sequence.
    /// The queue snapshot is resolved inside the coordinator's own state lock,
    /// so this cannot pin a stale list the way a read-then-write client would.
    public func workOverrideAll() async throws -> WorkOverrideEnvelope {
        try await runJSON(
            WorkOverrideEnvelope.self,
            arguments: [
                "--json", "session", "pressure", "work", "override",
                "--all",
                "--confirm",
            ]
        )
    }

    // MARK: - Process runner

    private func apiProjection<T: Decodable>(
        _ type: T.Type,
        route: String,
        query: [String: String] = [:]
    ) async throws -> T? {
        guard let apiClient else { return nil }
        do {
            return try await apiClient.projection(route, query: query, as: type)
        } catch let error as NDevPressureAPIError where error.permitsCLIFallback {
            return nil
        }
    }

    private func runJSON<T: Decodable>(
        _ type: T.Type,
        arguments: [String],
        acceptNonZero: Bool = false,
        maximumResponseBytes: Int? = nil
    ) async throws -> T {
        let responseLimit = min(maximumResponseBytes ?? maxOutputBytes, maxOutputBytes)
        let result = try await run(arguments: arguments, maximumOutputBytes: responseLimit)
        if let maximumResponseBytes, result.stdout.utf8.count > maximumResponseBytes {
            throw NDevPressureClientError.responseTooLarge(limit: maximumResponseBytes)
        }
        if result.code != 0 && !acceptNonZero {
            // Some pressure verbs still emit useful JSON on failure — try decode first.
            if let data = result.stdout.data(using: .utf8), !data.isEmpty,
               let decoded = try? PressureJSON.decode(type, from: data) {
                return decoded
            }
            throw NDevPressureClientError.nonZeroExit(code: result.code, stderr: result.stderr, stdout: result.stdout)
        }
        guard let data = result.stdout.data(using: .utf8), !data.isEmpty else {
            if acceptNonZero, result.code != 0 {
                throw NDevPressureClientError.nonZeroExit(code: result.code, stderr: result.stderr, stdout: result.stdout)
            }
            throw NDevPressureClientError.emptyResponse
        }
        do {
            return try PressureJSON.decode(type, from: data)
        } catch {
            if result.code != 0 {
                throw NDevPressureClientError.nonZeroExit(code: result.code, stderr: result.stderr, stdout: result.stdout)
            }
            throw NDevPressureClientError.malformedJSON(String(describing: error))
        }
    }

    private func run(arguments: [String], maximumOutputBytes: Int? = nil) async throws -> (code: Int32, stdout: String, stderr: String) {
        let binaryPath = self.binaryPath
        let environment = self.environment
        let maxOutputBytes = min(maximumOutputBytes ?? self.maxOutputBytes, self.maxOutputBytes)
        let commandTimeoutSeconds = self.commandTimeoutSeconds
        return try await withCheckedThrowingContinuation { continuation in
            DispatchQueue.global(qos: .userInitiated).async {
                do {
                    let value = try Self.capture(
                        binaryPath: binaryPath,
                        environment: environment,
                        arguments: arguments,
                        maxBytes: maxOutputBytes,
                        timeoutSeconds: commandTimeoutSeconds
                    )
                    continuation.resume(returning: value)
                } catch {
                    continuation.resume(throwing: error)
                }
            }
        }
    }

    static func capture(
        binaryPath: String,
        environment: [String: String],
        arguments: [String],
        maxBytes: Int,
        timeoutSeconds: Double
    ) throws -> (code: Int32, stdout: String, stderr: String) {
        let stdoutPipe = Pipe()
        let stderrPipe = Pipe()
        let stdoutBuffer = BoundedCaptureBuffer(limit: maxBytes)
        let stderrBuffer = BoundedCaptureBuffer(limit: min(maxBytes, 64 * 1024))

        let process = Process()
        process.executableURL = URL(fileURLWithPath: binaryPath)
        process.arguments = arguments
        process.environment = environment
        process.standardOutput = stdoutPipe
        process.standardError = stderrPipe
        process.standardInput = FileHandle.nullDevice

        let completed = DispatchSemaphore(value: 0)
        process.terminationHandler = { _ in completed.signal() }
        do {
            try process.run()
        } catch {
            throw NDevPressureClientError.launchFailed(error.localizedDescription)
        }

        // Process.run has duplicated the descriptors into the child. Closing
        // the parent's write ends lets the drains observe EOF after exit.
        try? stdoutPipe.fileHandleForWriting.close()
        try? stderrPipe.fileHandleForWriting.close()
        let drains = DispatchGroup()
        Self.drain(stdoutPipe.fileHandleForReading, into: stdoutBuffer, group: drains)
        Self.drain(stderrPipe.fileHandleForReading, into: stderrBuffer, group: drains)

        if completed.wait(timeout: .now() + timeoutSeconds) == .timedOut {
            process.terminate()
            if completed.wait(timeout: .now() + 2) == .timedOut {
                Darwin.kill(process.processIdentifier, SIGKILL)
                _ = completed.wait(timeout: .now() + 2)
            }
            try? stdoutPipe.fileHandleForReading.close()
            try? stderrPipe.fileHandleForReading.close()
            _ = drains.wait(timeout: .now() + 2)
            throw NDevPressureClientError.timedOut(seconds: timeoutSeconds)
        }
        process.waitUntilExit()
        if drains.wait(timeout: .now() + 2) == .timedOut {
            try? stdoutPipe.fileHandleForReading.close()
            try? stderrPipe.fileHandleForReading.close()
            _ = drains.wait(timeout: .now() + 1)
        }

        let stdoutResult = stdoutBuffer.snapshot()
        let stderrResult = stderrBuffer.snapshot()
        if stdoutResult.overflowed {
            throw NDevPressureClientError.responseTooLarge(limit: maxBytes)
        }
        let stdout = String(decoding: stdoutResult.data, as: UTF8.self)
        var stderr = String(decoding: stderrResult.data, as: UTF8.self)
        if stderrResult.overflowed {
            stderr += "\n[stderr truncated by NDev Pressure]"
        }
        return (process.terminationStatus, stdout, stderr)
    }

    private static func drain(_ handle: FileHandle, into buffer: BoundedCaptureBuffer, group: DispatchGroup) {
        group.enter()
        DispatchQueue.global(qos: .userInitiated).async {
            defer { group.leave() }
            while true {
                do {
                    guard let chunk = try handle.read(upToCount: 8 * 1024), !chunk.isEmpty else { return }
                    buffer.append(chunk)
                } catch {
                    return
                }
            }
        }
    }
}
