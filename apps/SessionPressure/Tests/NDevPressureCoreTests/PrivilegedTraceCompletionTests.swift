import Foundation
import NDevPressureCore
import Testing
@testable import NDevPressure

@Suite("Privileged trace completion")
@MainActor
struct PrivilegedTraceCompletionTests {
    @Test("deadline created from MainActor completes on a background queue")
    func backgroundDeadline() async {
        do {
            let _: DiskPathTraceResult = try await withCheckedThrowingContinuation { continuation in
                let completion = TraceCompletion(continuation)
                let deadline = completion.makeDeadline()
                DispatchQueue.global().async(execute: deadline)
            }
            Issue.record("Deadline must fail the request")
        } catch {
            #expect(error.localizedDescription.contains("bounded trace deadline exceeded"))
        }
    }

    @Test("competing callbacks resume the request only once")
    func competingCallbacks() async throws {
        let result: DiskPathTraceResult = try await withCheckedThrowingContinuation { continuation in
            let completion = TraceCompletion(continuation)
            DispatchQueue.global().async {
                completion.succeed(DiskPathTraceResult(paths: [], truncated: false))
                completion.fail(PrivilegedTraceError.connection("late interruption"))
                completion.succeed(DiskPathTraceResult(paths: ["late reply"], truncated: true))
            }
        }
        #expect(result.paths.isEmpty)
        #expect(!result.truncated)
    }
}
