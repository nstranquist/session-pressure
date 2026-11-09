import Foundation
import NDevPressureCore

struct DelegateStats: Sendable {
    var total = 0
    var shadow = 0
    var remotePass = 0
    var fallbacks = 0
    var bothVerdict = 0
    var agree = 0
    var diverged = 0
    var remoteRunMS: Int64 = 0
    var localMS: Int64 = 0
    var onEraTotal = 0
    var onEraRemotePass = 0
    var onEraFallbacks = 0
    var onEraDiverged = 0
    var onEraBothVerdict = 0
    var onEraRemoteRunMS: Int64 = 0

    private static func addingDuration(_ a: Int64, _ b: Int64) -> Int64 {
        let (value, overflow) = a.addingReportingOverflow(max(0, b))
        return overflow ? Int64.max : value
    }

    static func compute(_ runs: [DelegateRun]) -> DelegateStats {
        var s = DelegateStats()
        s.total = runs.count
        for r in runs {
            if r.decision == "shadow" { s.shadow += 1 }
            if r.decision == "remote_pass" { s.remotePass += 1 }
            if r.decision == "local_fallback" { s.fallbacks += 1 }
            if let a = r.agreement {
                s.bothVerdict += 1
                if a { s.agree += 1 } else { s.diverged += 1 }
            }
            if r.remoteRan, let ms = r.remoteRunMS { s.remoteRunMS = addingDuration(s.remoteRunMS, ms) }
            if let ms = r.localMS { s.localMS = addingDuration(s.localMS, ms) }
            if r.mode == "on" {
                s.onEraTotal += 1
                if r.decision == "remote_pass" { s.onEraRemotePass += 1 }
                if r.decision == "local_fallback" { s.onEraFallbacks += 1 }
                if let agreement = r.agreement {
                    s.onEraBothVerdict += 1
                    if !agreement { s.onEraDiverged += 1 }
                }
                if r.remoteRan, let ms = r.remoteRunMS { s.onEraRemoteRunMS = addingDuration(s.onEraRemoteRunMS, ms) }
            }
        }
        return s
    }
}

// The Go CLI owns all file and SSH reads. This store only keeps view state.
@MainActor
final class HetznerStore: ObservableObject {
    typealias Inspect = @Sendable (Bool, String?, String?, String?) async throws -> GateRunnerEnvelope
    typealias ReadLog = @Sendable (String, String?, String?) async throws -> String
    @Published private(set) var delegateRuns: [DelegateRun] = []
    @Published private(set) var delegateStats = DelegateStats()
    @Published private(set) var delegateConfig: DelegateConfig?
    @Published private(set) var ledgerPresent = false
    @Published private(set) var ledgerCheckedAt: Date?
    @Published private(set) var historyTruncated = false
    @Published private(set) var malformedRows = 0
    @Published private(set) var localError: String?
    @Published private(set) var jobs: [BoxJob] = []
    @Published private(set) var boxHealth: BoxHealth?
    @Published private(set) var boxFetchedAt: Date?
    @Published private(set) var boxReachable = false
    @Published private(set) var boxError: String?
    @Published private(set) var liveHosts: [LiveHost] = []
    @Published private(set) var fleet: RunnerFleet?
    @Published private(set) var fleetError: String?
    @Published private(set) var observations: [String: RunnerObservation] = [:]
    @Published private(set) var selectedHost: String?
    @Published private(set) var livePresent = false
    @Published private(set) var liveError: String?
    @Published private(set) var liveCheckedAt: Date?
    @Published private(set) var refreshing = false
    @Published var selectedJobID: String?
    @Published private(set) var jobLog: String?
    @Published private(set) var jobLogError: String?
    @Published private(set) var jobLogLoading = false
    private let inspect: Inspect
    private let readLog: ReadLog
    private var loopTask: Task<Void, Never>?
    private var refreshTask: Task<Void, Never>?
    private var logTask: Task<Void, Never>?
    private var generation = UUID()
    private var refreshIncludesBox = false
    private var ledgerSignature: String?

    init(inspect: @escaping Inspect = { includeBox, known, host, user in
        try await ClientFactory.make().gateRunnerInspection(includeBox: includeBox, knownLedger: known, host: host, user: user)
    }, readLog: @escaping ReadLog = { id, host, user in
        try await ClientFactory.make().gateRunnerJobLog(id: id, host: host, user: user)
    }) {
        self.inspect = inspect
        self.readLog = readLog
    }

    func start() {
        guard loopTask == nil else { return }
        loopTask = Task { [weak self] in
            await self?.refreshNow()
            var tick = 0
            while !Task.isCancelled {
                if self == nil { return }
                do { try await Task.sleep(for: .seconds(15)) } catch { break }
                tick += 1
                await self?.refresh(includeBox: tick % 4 == 0)
            }
        }
    }

    func stop() {
        generation = UUID()
        loopTask?.cancel()
        refreshTask?.cancel()
        logTask?.cancel()
        loopTask = nil
        refreshTask = nil
        logTask = nil
        refreshing = false
        jobLogLoading = false
    }

    func refreshNow() async { await refresh(includeBox: true) }

    private func refresh(includeBox: Bool) async {
        let token = generation
        if let refreshTask {
            let needsBox = includeBox && !refreshIncludesBox
            await refreshTask.value
            if needsBox && generation == token && !Task.isCancelled { await refresh(includeBox: true) }
            return
        }
        refreshIncludesBox = includeBox
        let inspect = self.inspect
        let signature = ledgerSignature
        let host = selectedHost
        let user = fleet?.hosts.first(where: { $0.name == host })?.user
        refreshing = true
        let task = Task { [weak self] in
            do {
                let envelope = try await inspect(includeBox, signature, host, user)
                guard !Task.isCancelled, let self, self.generation == token else { return }
                guard envelope.ok, envelope.inspection.schemaVersion == 1 else {
                    self.localError = "Unsupported gate-runner response"
                    if includeBox {
                        self.boxReachable = false
                        self.boxError = "Unsupported gate-runner response"
                    }
                    return
                }
                let s = envelope.inspection
                if let endpoint = s.host {
                    let actual = String(endpoint.split(separator: "@").last ?? Substring(endpoint))
                    guard host == nil || host == actual else {
                        self.boxError = "Response belongs to a different runner"
                        self.boxReachable = false
                        return
                    }
                    self.selectedHost = actual
                }
                if let fleet = s.fleet {
                    self.fleetError = fleet.error
                    if fleet.error == nil {
                        self.fleet = fleet
                        if let observations = fleet.observations {
                            self.observations = Dictionary(uniqueKeysWithValues: observations.map { ($0.host.name, $0) })
                        }
                    }
                } else {
                    self.fleetError = "Runner inventory unavailable; update the CLI or install a fleet inventory."
                }
                self.localError = s.localError
                if s.localError == nil {
                    if !s.ledgerUnchanged {
                        self.delegateRuns = s.runs
                        self.delegateStats = DelegateStats.compute(s.runs)
                        self.historyTruncated = s.historyTruncated
                        self.malformedRows = s.malformedRows
                    }
                    self.ledgerSignature = s.ledgerSignature.isEmpty ? nil : s.ledgerSignature
                    self.delegateConfig = s.config
                    self.ledgerPresent = s.ledgerPresent
                    self.ledgerCheckedAt = s.capturedAt
                }
                if let live = s.live {
                    self.liveError = live.error
                    self.livePresent = live.present
                    if live.present && live.error == nil {
                        self.liveHosts = live.hosts
                        self.liveCheckedAt = s.capturedAt
                    }
                } else {
                    self.livePresent = false
                    self.liveError = "Live store unavailable; showing any last successful samples."
                }
                if s.boxRequested {
                    self.boxReachable = s.boxReachable
                    self.boxError = s.boxError
                    if s.boxReachable && s.boxError == nil {
                        self.jobs = s.jobs
                        self.boxHealth = s.health
                        self.boxFetchedAt = s.capturedAt
                    }
                }
            } catch {
                guard !Task.isCancelled, let self, self.generation == token else { return }
                self.localError = error.localizedDescription
                self.liveError = error.localizedDescription
                self.fleetError = error.localizedDescription
                if includeBox {
                    self.boxReachable = false
                    self.boxError = error.localizedDescription
                }
            }
        }
        refreshTask = task
        await task.value
        guard generation == token else { return }
        refreshTask = nil
        refreshing = false
    }

    func selectHost(_ host: String) async {
        guard host != selectedHost, fleet?.hosts.contains(where: { $0.name == host }) == true else { return }
        let wasPolling = loopTask != nil
        stop()
        selectedHost = host
        jobs = []
        boxHealth = nil
        boxFetchedAt = nil
        boxReachable = false
        boxError = nil
        selectedJobID = nil
        jobLog = nil
        jobLogError = nil
        if wasPolling { start() } else { await refreshNow() }
    }

    func selectJob(_ job: BoxJob) {
        logTask?.cancel()
        jobLog = nil
        jobLogError = nil
        if selectedJobID == job.id {
            selectedJobID = nil
            jobLogLoading = false
            return
        }
        selectedJobID = job.id
        jobLogLoading = true
        let readLog = self.readLog
        let host = selectedHost
        let user = fleet?.hosts.first(where: { $0.name == host })?.user
        let token = generation
        logTask = Task { [weak self] in
            do {
                let result = try await readLog(job.id, host, user)
                guard let self, !Task.isCancelled, self.generation == token, self.selectedJobID == job.id else { return }
                self.jobLog = result
                self.jobLogLoading = false
            } catch {
                guard let self, !Task.isCancelled, self.generation == token, self.selectedJobID == job.id else { return }
                self.jobLogError = error.localizedDescription
                self.jobLogLoading = false
            }
        }
    }
}
