import Foundation
import Testing
@testable import NDevPressure
@testable import NDevPressureCore

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
    static var sampleLive: LiveSection {
        LiveSection(present: true, error: nil, hosts: [
            LiveHost(host: "gaterunner-2", runnerVersion: "6797d410", load1: 1.2,
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
    static func envelope(includeBox: Bool, unchanged: Bool, live: LiveSection? = nil) -> GateRunnerEnvelope {
        GateRunnerEnvelope(ok: true, inspection: GateRunnerInspection(
            schemaVersion: 1, capturedAt: Date(timeIntervalSince1970: 100), runs: unchanged ? [] : [sampleRun],
            config: DelegateConfig(mode: "shadow", repos: [], allowCount: 1), ledgerSignature: String(repeating: "a", count: 64), ledgerUnchanged: unchanged, ledgerPresent: true,
            historyTruncated: false, malformedRows: 0, localError: nil,
            boxRequested: includeBox, boxReachable: includeBox, boxError: nil,
            jobs: [BoxJob(id: "20261108T210000Z-1", at: nil, caller: "test", repo: "sample",
                          sha: nil, cand: nil, jobClass: "test", exitCode: "0", durationSeconds: 3,
                          macEst: nil, command: "go test ./...")], health: nil, live: live))
    }
}

@Suite("Hetzner thin-client lifecycle")
@MainActor
struct HetznerStoreTests {
    @Test("concurrent refresh requests share one CLI read")
    func coalescesRefreshes() async {
        let probe = RunnerProbe()
        let store = HetznerStore(inspect: { box, known in try await probe.read(box, known: known) })
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
        let store = HetznerStore(inspect: { box, known in try await probe.read(box, known: known) })
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
        let store = HetznerStore(inspect: { box, known in try await probe.read(box, known: known) })
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
        let store = HetznerStore(inspect: { box, known in try await probe.read(box, known: known) }, readLog: { _ in throw URLError(.cannotConnectToHost) })
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
        let store = HetznerStore(inspect: { box, known in try await probe.read(box, known: known) })
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
        let store = HetznerStore(inspect: { box, known in try await probe.read(box, known: known) })
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

    @Test("absent live section keeps the legacy tab state")
    func absentLiveKeepsLegacy() async {
        let probe = RunnerProbe()
        let store = HetznerStore(inspect: { box, known in try await probe.read(box, known: known) })
        await store.refreshNow()
        #expect(!store.livePresent)
        #expect(store.liveHosts.isEmpty)
        #expect(store.liveError == nil)
        #expect(store.jobs.count == 1)
        store.stop()
    }

}
