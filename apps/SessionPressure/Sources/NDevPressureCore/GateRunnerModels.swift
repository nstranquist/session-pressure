import Foundation

// MARK: - Models

/// One row of the commit-delivery gate-delegate ledger
/// (~/.nicos-dev/commit-delivery/v1/gate-delegate/runs.jsonl). The worker
/// writes one row per delegated gate check: the Hetzner box ran the check,
/// and the row records whether the box verdict was merely shadow-compared
/// against the local run or actually used in place of it.
public struct DelegateRun: Decodable, Identifiable, Sendable {
    public let id: String
    public let at: Date?
    public let mode: String
    public let decision: String
    public let deliveryID: String
    public let repoName: String
    public let repoPath: String
    public let command: String
    public let localPassed: Bool?
    public let localMS: Int64?
    public let localDetail: String?
    public let localEnvironment: String?
    public let recordedAgreement: Bool?
    public let remoteRan: Bool
    public let remotePassed: Bool?
    public let remoteExit: Int?
    public let remoteRunMS: Int64?
    public let remoteWallMS: Int64?
    public let remoteStage: String?
    public let treeVerified: Bool?
    public let treeHash: String?
    public let policyDigest: String?
    public let remotePassAllowed: Bool?

    /// True when the box verdict replaced the local run instead of only
    /// being compared against it. Shadow rows carry decision "shadow";
    /// a consumed verdict is "remote_pass" (box used) or "local_fallback"
    /// (box ran but the local verdict stayed authoritative).
    public var verdictUsed: Bool { decision == "remote_pass" }

    /// The delivery worker owns comparability. Equal booleans alone cannot
    /// establish agreement when a run failed because of its environment.
    public var agreement: Bool? { recordedAgreement }

    /// A recorded environment failure, or a display hint from the local
    /// failure detail. This hint does not establish verdict comparability.
    public var localEnvFailure: Bool {
        if let localEnvironment, !localEnvironment.isEmpty { return true }
        guard localPassed == false, let detail = localDetail?.lowercased() else { return false }
        return detail.contains("timeout") || detail.contains("killed") || detail.contains("memory")
    }
    enum CodingKeys: String, CodingKey {
        case id
        case at
        case mode
        case decision
        case deliveryID = "delivery_id"
        case repoName = "repo_name"
        case repoPath = "repo_path"
        case command
        case localPassed = "local_passed"
        case localMS = "local_ms"
        case localDetail = "local_detail"
        case localEnvironment = "local_environment"
        case recordedAgreement = "agreement"
        case remoteRan = "remote_ran"
        case remotePassed = "remote_passed"
        case remoteExit = "remote_exit"
        case remoteRunMS = "remote_run_ms"
        case remoteWallMS = "remote_wall_ms"
        case remoteStage = "remote_stage"
        case treeVerified = "tree_verified"
        case treeHash = "tree_hash"
        case policyDigest = "policy_digest"
        case remotePassAllowed = "remote_pass_allowed"
    }

}

/// One repository entry in the delegate map (Mac path -> box clone name).
public struct DelegateRepo: Decodable, Identifiable, Sendable {
    public let path: String
    public let box: String
    public var id: String { path }
}

/// The worker's delegate configuration, read display-only from
/// gate-delegate.json. This tab never writes it.
public struct DelegateConfig: Decodable, Sendable {
    public let mode: String
    public let repos: [DelegateRepo]
    public let allowCount: Int
    enum CodingKeys: String, CodingKey {
        case mode
        case repos
        case allowCount = "allow_count"
    }

}

/// Health snapshot of the gate-runner box, fetched in the same SSH round
/// trip as the job receipts (same fields the :8793 poller collects).
public struct BoxHealth: Decodable, Sendable {
    public let load1: Double?
    public let load5: Double?
    public let load15: Double?
    public let memTotalMB: Int64?
    public let memAvailableMB: Int64?
    public let diskUsedPercent: Int?
    public let uptime: String?
    enum CodingKeys: String, CodingKey {
        case load1
        case load5
        case load15
        case memTotalMB = "mem_total_mb"
        case memAvailableMB = "mem_available_mb"
        case diskUsedPercent = "disk_used_percent"
        case uptime
    }

}

/// One receipt line from the gate-runner box's /srv/build/logs/jobs.log
/// (receipts-v2; the earliest v1 lines lack sha/cand/class/mac_est and
/// parse with those fields nil).
public struct BoxJob: Decodable, Identifiable, Sendable {
    public let id: String
    public let at: Date?
    public let caller: String
    public let repo: String
    public let sha: String?
    public let cand: String?
    public let jobClass: String?
    public let exitCode: String
    public let durationSeconds: Int?
    public let macEst: String?
    public let command: String

    public var isSetupFailure: Bool { exitCode == "SETUP" }
    public var isSuccess: Bool { exitCode == "0" }
    enum CodingKeys: String, CodingKey {
        case id
        case at
        case caller
        case repo
        case sha
        case cand
        case jobClass = "job_class"
        case exitCode = "exit_code"
        case durationSeconds = "duration_seconds"
        case macEst = "mac_est"
        case command
    }

}


public struct GateRunnerEnvelope: Decodable, Sendable {
    public let ok: Bool
    public let inspection: GateRunnerInspection
}

public struct GateRunnerInspection: Decodable, Sendable {
    public let schemaVersion: Int
    public let capturedAt: Date
    public let runs: [DelegateRun]
    public let config: DelegateConfig?
    public let ledgerSignature: String
    public let ledgerUnchanged: Bool
    public let ledgerPresent: Bool
    public let historyTruncated: Bool
    public let malformedRows: Int
    public let localError: String?
    public let boxRequested: Bool
    public let boxReachable: Bool
    public let boxError: String?
    public let jobs: [BoxJob]
    public let health: BoxHealth?
    enum CodingKeys: String, CodingKey {
        case schemaVersion = "schema_version"
        case capturedAt = "captured_at"
        case runs, config, jobs, health
        case ledgerSignature = "ledger_signature"
        case ledgerUnchanged = "ledger_unchanged"
        case ledgerPresent = "ledger_present"
        case historyTruncated = "history_truncated"
        case malformedRows = "malformed_rows"
        case localError = "local_error"
        case boxRequested = "box_requested"
        case boxReachable = "box_reachable"
        case boxError = "box_error"
    }
}
