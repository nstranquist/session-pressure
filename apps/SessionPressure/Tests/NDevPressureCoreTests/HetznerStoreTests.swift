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
    func setFailure() { fail = true }
    func read(_ includeBox: Bool, known: String?) async throws -> GateRunnerEnvelope {
        calls += 1
        receivedKnown = known
        active += 1
        peak = max(peak, active)
        defer { active -= 1 }
        try await Task.sleep(for: .milliseconds(30))
        if fail { throw URLError(.cannotConnectToHost) }
        return Self.envelope(includeBox: includeBox, unchanged: known != nil)
    }
    static var sampleRun: DelegateRun {
        DelegateRun(id: "run-1", at: nil, mode: "on", decision: "local_fallback", deliveryID: "delivery-1",
            repoName: "sample", repoPath: "/work/sample", command: "go test ./...", localPassed: false,
            localMS: 5, localDetail: nil, localEnvironment: "killed", recordedAgreement: nil,
            remoteRan: true, remotePassed: true, remoteExit: 0, remoteRunMS: 3, remoteWallMS: 4,
            remoteStage: nil, treeVerified: true, treeHash: nil, policyDigest: nil, remotePassAllowed: false)
    }
    static func envelope(includeBox: Bool, unchanged: Bool) -> GateRunnerEnvelope {
        GateRunnerEnvelope(ok: true, inspection: GateRunnerInspection(
            schemaVersion: 1, capturedAt: Date(timeIntervalSince1970: 100), runs: unchanged ? [] : [sampleRun],
            config: DelegateConfig(mode: "shadow", repos: [], allowCount: 1), ledgerSignature: String(repeating: "a", count: 64), ledgerUnchanged: unchanged, ledgerPresent: true,
            historyTruncated: false, malformedRows: 0, localError: nil,
            boxRequested: includeBox, boxReachable: includeBox, boxError: nil,
            jobs: [BoxJob(id: "20261108T210000Z-1", at: nil, caller: "test", repo: "sample",
                          sha: nil, cand: nil, jobClass: "test", exitCode: "0", durationSeconds: 3,
                          macEst: nil, command: "go test ./...")], health: nil))
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
        let store = HetznerStore(inspect: { box, known in try await probe.read(box, known: known) })
        let read = Task { await store.refreshNow() }
        while await probe.calls == 0 { await Task.yield() }
        store.stop()
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

}
