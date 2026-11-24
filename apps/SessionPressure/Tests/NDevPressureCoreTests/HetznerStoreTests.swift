import Foundation
import Testing
@testable import NDevPressure
@testable import NDevPressureCore

private actor KnownJobsLog {
    var values: [String?] = []
    func append(_ value: String?) { values.append(value) }
}

private actor LogReads {
    private(set) var count = 0
    func next(_ id: String) -> Int {
        count += 1
        return count
    }
}

private actor RunnerProbe {
    var calls = 0
    var active = 0
    var peak = 0
    var fail = false
    var receivedKnown: String?
    var live: LiveSection?
    func setLive(_ section: LiveSection?) { live = section }
    private var holdResponse = false
    private var responseRelease: CheckedContinuation<Void, Never>?
    func hold() { holdResponse = true }
    func releaseResponse() {
        holdResponse = false
        responseRelease?.resume()
        responseRelease = nil
    }
    func setFailure() { fail = true }
    func read(_ includeBox: Bool, known: String?) async throws -> GateRunnerEnvelope {
        calls += 1
        receivedKnown = known
        active += 1
        peak = max(peak, active)
        defer { active -= 1 }
        if holdResponse {
            await withCheckedContinuation { responseRelease = $0 }
        }
        try await Task.sleep(for: .milliseconds(30))
        if fail { throw URLError(.cannotConnectToHost) }
        return Self.envelope(includeBox: includeBox, unchanged: known != nil, live: live)
    }
    static var sampleFleet: RunnerFleet {
        RunnerFleet(generation: "test", digest: "digest", hosts: [
            RunnerHost(name: "runner-2", user: "builder", state: "active", provider: "hetzner-dedicated", platform: "linux/amd64", sourceCommit: "rev", weight: 4, heavySlots: 4, lightSlots: 2, profiles: ["linux-native"]),
            RunnerHost(name: "runner-1", user: "reserve-user", state: "reserve", provider: "hetzner-cloud", platform: "linux/arm64", sourceCommit: "rev", weight: 0, heavySlots: 1, lightSlots: 1, profiles: ["linux-native"])
        ], observations: nil, error: nil)
    }
    static var sampleLive: LiveSection {
        LiveSection(present: true, error: nil, hosts: [
            LiveHost(host: "runner-2", runnerVersion: "6797d410", load1: 1.2,
                     diskFreeGB: 640, cacheGB: 12, lastPollAt: nil, pollError: nil,
                     feedStale: false,
                     liveJobs: [LiveJob(jobID: "job-s1", deliveryID: "del-4", repo: "nicos-tools",
                                        sha: nil, jobClass: "pure", phase: "running", state: nil,
                                        verdict: "STALLED", enqueuedAt: nil, startedAt: nil,
                                        finishedAt: nil, updatedAt: nil, elapsedS: 731,
                                        queueWaitS: nil, lastOutputAgeS: 342, cpuPct: 0.4,
                                        rssMB: 128, exitCode: nil, detail: nil)],
                     recentRuns: [],
                     kills: [KillEvent(jobID: "job-k1", deliveryID: "del-5", jobClass: "pure",
                                       at: nil, elapsedS: 731, lastOutputAgeS: 342, cpuPct: 0.4,
                                       detail: "stall_killed: silent=342")])
        ])
    }
    static var sampleRun: DelegateRun {
        DelegateRun(id: "run-1", at: nil, mode: "on", decision: "local_fallback", deliveryID: "delivery-1",
            repoName: "sample", repoPath: "/work/sample", command: "go test ./...", localPassed: false,
            localMS: 5, localDetail: nil, localEnvironment: "killed", recordedAgreement: nil,
            remoteRan: true, remotePassed: true, remoteExit: 0, remoteRunMS: 3, remoteWallMS: 4,
            remoteStage: nil, treeVerified: true, treeHash: nil, policyDigest: nil, remotePassAllowed: false)
    }
    static func envelope(includeBox: Bool, unchanged: Bool, live: LiveSection? = nil, host: String = "runner-2") -> GateRunnerEnvelope {
        GateRunnerEnvelope(ok: true, inspection: GateRunnerInspection(
            schemaVersion: 1, capturedAt: Date(timeIntervalSince1970: 100), runs: unchanged ? [] : [sampleRun],
            config: DelegateConfig(mode: "shadow", repos: [], allowCount: 1), ledgerSignature: String(repeating: "a", count: 64), ledgerUnchanged: unchanged, ledgerPresent: true,
            historyTruncated: false, malformedRows: 0, localError: nil,
            boxRequested: includeBox, boxReachable: includeBox, boxError: nil,
            jobs: [BoxJob(id: "20261108T210000Z-1", at: nil, caller: "test", repo: "sample",
                          sha: nil, cand: nil, jobClass: "test", exitCode: "0", durationSeconds: 3,
                          macEst: nil, command: "go test ./...")], health: nil, live: live, host: "builder@\(host)", fleet: sampleFleet))
    }
}

@Suite("Hetzner thin-client lifecycle")
@MainActor
struct HetznerStoreTests {
    @Test("unchanged box receipts are not re-sent or re-published")
    func jobsCursor() async {
        let signature = String(repeating: "b", count: 64)
        let known = KnownJobsLog()
        let store = HetznerStore(inspect: { box, _, _, _, knownJobs in
            await known.append(knownJobs)
            var inspection = RunnerProbe.envelope(includeBox: box, unchanged: false).inspection
            if box {
                inspection.jobsSignature = signature
                inspection.jobsUnchanged = knownJobs == signature
            }
            return GateRunnerEnvelope(ok: true, inspection: inspection)
        })
        await store.refreshNow()
        let revision = store.jobsRevision
        #expect(store.jobs.count == 1)
        await store.refreshNow()
        #expect(await known.values == [nil, signature])
        #expect(store.jobsRevision == revision)
        #expect(store.jobs.count == 1)
        // A new runner starts without a cursor.
        await store.selectHost("runner-1")
        #expect(await known.values.last == .some(nil))
        store.stop()
    }

    @Test("concurrent refresh requests share one CLI read")
    func coalescesRefreshes() async {
        let probe = RunnerProbe()
        let store = HetznerStore(inspect: { box, known, _, _, _ in try await probe.read(box, known: known) })
        async let a: Void = store.refreshNow()
        async let b: Void = store.refreshNow()
        async let c: Void = store.refreshNow()
        _ = await (a, b, c)
        #expect(await probe.calls == 1)
        #expect(await probe.peak == 1)
        #expect(!store.refreshing)
        #expect(store.jobs.count == 1)
        store.stop()
    }

    @Test("failed reads retain successful data and its original age")
    func retainsStaleEvidence() async {
        let probe = RunnerProbe()
        let store = HetznerStore(inspect: { box, known, _, _, _ in try await probe.read(box, known: known) })
        await store.refreshNow()
        let age = store.boxFetchedAt
        await probe.setFailure()
        await store.refreshNow()
        #expect(store.jobs.count == 1)
        #expect(store.delegateConfig?.mode == "shadow")
        #expect(store.boxFetchedAt == age)
        #expect(store.ledgerCheckedAt == age)
        #expect(!store.boxReachable)
        #expect(store.localError != nil)
        #expect(store.boxError != nil)
        store.stop()
    }

    @Test("closing the pane prevents a late result from updating it")
    func ignoresStoppedGeneration() async throws {
        let probe = RunnerProbe()
        await probe.hold()
        let store = HetznerStore(inspect: { box, known, _, _, _ in try await probe.read(box, known: known) })
        let read = Task { await store.refreshNow() }
        while await probe.calls == 0 { await Task.yield() }
        store.stop()
        await probe.releaseResponse()
        await read.value
        #expect(store.jobs.isEmpty)
        #expect(store.boxFetchedAt == nil)
        #expect(!store.refreshing)
    }

    @Test("job log failures are errors instead of invented missing-history claims")
    func logFailureIsVisible() async throws {
        let probe = RunnerProbe()
        let store = HetznerStore(inspect: { box, known, _, _, _ in try await probe.read(box, known: known) }, readLog: { _, _, _ in throw URLError(.cannotConnectToHost) })
        await store.refreshNow()
        store.selectJob(try #require(store.jobs.first))
        while store.jobLogLoading { await Task.yield() }
        #expect(store.jobLog == nil)
        #expect(store.jobLogError != nil)
        store.stop()
    }
    @Test("unchanged history retains loaded rows and sends its content signature")
    func unchangedHistory() async {
        let probe = RunnerProbe()
        let store = HetznerStore(inspect: { box, known, _, _, _ in try await probe.read(box, known: known) })
        await store.refreshNow()
        #expect(store.delegateRuns.count == 1)
        await store.refreshNow()
        #expect(await probe.receivedKnown == String(repeating: "a", count: 64))
        #expect(store.delegateRuns.count == 1)
        #expect(store.delegateStats.bothVerdict == 0)
        #expect(store.delegateStats.diverged == 0)
        #expect(store.delegateRuns[0].localEnvFailure)
        store.stop()
    }

    @Test("live fleet surfaces hosts, verdicts and kills")
    func liveFleetSurfaces() async {
        let probe = RunnerProbe()
        await probe.setLive(RunnerProbe.sampleLive)
        let store = HetznerStore(inspect: { box, known, _, _, _ in try await probe.read(box, known: known) })
        await store.refreshNow()
        #expect(store.livePresent)
        #expect(store.liveError == nil)
        #expect(store.liveHosts.count == 1)
        #expect(store.liveHosts[0].liveJobs.first?.verdict == "STALLED")
        #expect(store.liveHosts[0].liveJobs.first?.isStalled == true)
        #expect(store.liveHosts[0].kills.count == 1)
        #expect(store.liveCheckedAt != nil)
        store.stop()
    }

    @Test("a running job's log follows the job, then gets one last read")
    func followsRunningJobLog() async throws {
        let probe = RunnerProbe()
        await probe.setLive(RunnerProbe.sampleLive)
        let reads = LogReads()
        let store = HetznerStore(inspect: { box, known, _, _, _ in try await probe.read(box, known: known) },
                                 readLog: { id, _, _ in
            let n = await reads.next(id)
            return GateRunnerJobLog(id: id, kind: "scheduler", log: "line \(n)\n", bytes: 7)
        })
        await store.refreshNow()
        let job = try #require(store.liveHosts.first?.liveJobs.first)
        store.selectLiveJob(job)
        #expect(store.selectedJobID == "job-s1")
        #expect(store.jobLogFollowing)
        while store.jobLogLoading { await Task.yield() }
        #expect(store.jobLog?.log == "line 1\n")

        // Still running: the tick reads the log again and keeps following.
        store.followRunningLog()
        while await reads.count < 2 { await Task.yield() }
        while store.jobLog?.log != "line 2\n" { await Task.yield() }
        #expect(store.jobLogFollowing)

        // The job left the running phase: one last read, then stop.
        await probe.setLive(LiveSection(present: true, error: nil, hosts: []))
        await store.refreshNow()
        store.followRunningLog()
        #expect(!store.jobLogFollowing)
        while store.jobLog?.log != "line 3\n" { await Task.yield() }

        // Selecting the same job again closes its log.
        store.selectLiveJob(job)
        #expect(store.selectedJobID == nil)
        #expect(store.jobLog == nil)
        store.stop()
    }

    @Test("a finished job's log opens without following")
    func finishedJobLogDoesNotFollow() async throws {
        let store = HetznerStore(inspect: { box, known, _, _, _ in
            RunnerProbe.envelope(includeBox: box, unchanged: known != nil)
        }, readLog: { id, _, _ in
            GateRunnerJobLog(id: id, kind: "receipt", log: "done", bytes: 4, command: "go test ./a ./b")
        })
        await store.refreshNow()
        store.selectJob(try #require(store.jobs.first))
        #expect(!store.jobLogFollowing)
        while store.jobLogLoading { await Task.yield() }
        #expect(store.jobLog?.command == "go test ./a ./b")
        store.stop()
    }

    @Test("absent live section reports unavailable without hiding history")
    func absentLiveIsUnavailable() async {
        let probe = RunnerProbe()
        let store = HetznerStore(inspect: { box, known, _, _, _ in try await probe.read(box, known: known) })
        await store.refreshNow()
        #expect(!store.livePresent)
        #expect(store.liveHosts.isEmpty)
        #expect(store.liveError != nil)
        #expect(store.jobs.count == 1)
        store.stop()
    }

    @Test("failed live reads preserve samples and their age")
    func retainsLiveEvidence() async {
        let probe = RunnerProbe()
        await probe.setLive(RunnerProbe.sampleLive)
        let store = HetznerStore(inspect: { box, known, _, _, _ in try await probe.read(box, known: known) })
        await store.refreshNow()
        let age = store.liveCheckedAt
        await probe.setLive(LiveSection(present: false, error: "runner_live_read_failed", hosts: []))
        await store.refreshNow()
        #expect(!store.livePresent)
        #expect(store.liveError == "runner_live_read_failed")
        #expect(store.liveHosts.count == 1)
        #expect(store.liveCheckedAt == age)
        store.stop()
    }

    @Test("runner switching binds job history and logs to the inventory endpoint")
    func bindsSelectedRunner() async throws {
        let store = HetznerStore(inspect: { box, known, host, user, _ in
            if host == "runner-1" { #expect(user == "reserve-user") }
            return RunnerProbe.envelope(includeBox: box, unchanged: known != nil, host: host ?? "runner-2")
        }, readLog: { _, host, user in
            #expect(host == "runner-1")
            #expect(user == "reserve-user")
            return GateRunnerJobLog(id: "r", kind: "receipt", log: "reserve log", bytes: 11)
        })
        await store.refreshNow()
        await store.selectHost("runner-1")
        #expect(store.selectedHost == "runner-1")
        #expect(store.jobLog == nil)
        store.selectJob(try #require(store.jobs.first))
        while store.jobLogLoading { await Task.yield() }
        #expect(store.jobLog?.log == "reserve log")
        store.stop()
    }

    @Test("a response for another runner cannot replace selected history")
    func rejectsWrongRunner() async {
        let store = HetznerStore(inspect: { box, known, _, _, _ in
            RunnerProbe.envelope(includeBox: box, unchanged: known != nil)
        })
        await store.refreshNow()
        await store.selectHost("runner-1")
        #expect(store.selectedHost == "runner-1")
        #expect(store.jobs.isEmpty)
        #expect(!store.boxReachable)
        #expect(store.boxError == "Response belongs to a different runner")
        store.stop()
    }

}
