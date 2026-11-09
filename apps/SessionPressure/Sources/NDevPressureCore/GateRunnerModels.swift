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
    /// Runner-live fleet section; nil until the poller store exists on
    /// this Mac (the key is then absent from the inspection JSON).
    public let live: LiveSection?
    enum CodingKeys: String, CodingKey {
        case schemaVersion = "schema_version"
        case capturedAt = "captured_at"
        case runs, config, jobs, health, live
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


// MARK: - Runner-live fleet (runner-live.sqlite, RUNNER-LIVENESS-DESIGN §3)
//
// The Mac poller keeps one SQLite store of per-host liveness: latest
// heartbeat per job plus its evaluated verdict, host vitals, and poll
// health. The inspection carries it as an optional section — when the
// store does not exist yet the key is absent and the tab renders its
// legacy ledger + primary-box view unchanged.

/// One job's latest live record on a host. Terminal rows surface under
/// recent runs (or kills, when the runner stall-killed the job).
public struct LiveJob: Decodable, Identifiable, Sendable {
    public let jobID: String
    public let deliveryID: String?
    public let repo: String
    public let sha: String?
    public let jobClass: String?
    public let phase: String
    public let state: String?
    public let verdict: String
    public let enqueuedAt: Date?
    public let startedAt: Date?
    public let finishedAt: Date?
    public let updatedAt: Date?
    public let elapsedS: Int64?
    public let queueWaitS: Int64?
    public let lastOutputAgeS: Int64?
    public let cpuPct: Double?
    public let rssMB: Double?
    public let exitCode: Int64?
    public let detail: String?

    public var id: String { jobID }
    public var isStalled: Bool { verdict == "STALLED" }

    enum CodingKeys: String, CodingKey {
        case jobID = "job_id"
        case deliveryID = "delivery_id"
        case repo, sha
        case jobClass = "class"
        case phase, state, verdict
        case enqueuedAt = "enqueued_at"
        case startedAt = "started_at"
        case finishedAt = "finished_at"
        case updatedAt = "updated_at"
        case elapsedS = "elapsed_s"
        case queueWaitS = "queue_wait_s"
        case lastOutputAgeS = "last_output_age_s"
        case cpuPct = "cpu_pct"
        case rssMB = "rss_mb"
        case exitCode = "exit_code"
        case detail
    }
}

/// A stall auto-kill receipt (design §5): the runner stopped the job for
/// silence + idle CPU + elapsed beyond its class bound. Surfaced
/// distinctly from ordinary failures — a kill is the system working as
/// configured, and the first-week kill report audits exactly these rows.
public struct KillEvent: Decodable, Identifiable, Sendable {
    public let jobID: String
    public let deliveryID: String?
    public let jobClass: String?
    public let at: Date?
    public let elapsedS: Int64?
    public let lastOutputAgeS: Int64?
    public let cpuPct: Double?
    public let detail: String?

    public var id: String { jobID }

    enum CodingKeys: String, CodingKey {
        case jobID = "job_id"
        case deliveryID = "delivery_id"
        case jobClass = "class"
        case at
        case elapsedS = "elapsed_s"
        case lastOutputAgeS = "last_output_age_s"
        case cpuPct = "cpu_pct"
        case detail
    }
}

/// Per-host liveness: vitals and poll health from runner_hosts, the
/// in-flight jobs, the newest terminal receipts, and stall kills.
/// feedStale means the poller itself has gone quiet (>60 s) — the host
/// renders grey, never as an idle fleet.
public struct LiveHost: Decodable, Identifiable, Sendable {
    public let host: String
    public let runnerVersion: String?
    public let load1: Double?
    public let diskFreeGB: Double?
    public let cacheGB: Double?
    public let lastPollAt: Date?
    public let pollError: String?
    public let feedStale: Bool
    public let liveJobs: [LiveJob]
    public let recentRuns: [LiveJob]
    public let kills: [KillEvent]

    public var id: String { host }

    enum CodingKeys: String, CodingKey {
        case host
        case runnerVersion = "runner_version"
        case load1
        case diskFreeGB = "disk_free_gb"
        case cacheGB = "cache_gb"
        case lastPollAt = "last_poll_at"
        case pollError = "poll_error"
        case feedStale = "feed_stale"
        case liveJobs = "live_jobs"
        case recentRuns = "recent_runs"
        case kills
    }
}

public struct LiveSection: Decodable, Sendable {
    public let present: Bool
    public let error: String?
    public let hosts: [LiveHost]
}
