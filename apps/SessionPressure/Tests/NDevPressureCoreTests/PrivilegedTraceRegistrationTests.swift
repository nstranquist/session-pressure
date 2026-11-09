import Foundation
import ServiceManagement
import Testing
@testable import NDevPressure

@Suite("Privileged trace service registration")
@MainActor
struct PrivilegedTraceRegistrationTests {
    @Test("post-update SM transition retries and succeeds without manual retry")
    func recoversRefreshTransition() async throws {
        var current = SMAppService.Status.notRegistered
        var registrations = 0
        var delays: [Int] = []
        try await TraceHelperRegistration.ensureAfterRefresh(
            helperIsEmbedded: true, refreshed: true, status: { current },
            register: {
                registrations += 1
                if registrations < 3 { throw NSError(domain: "SMAppServiceErrorDomain", code: 1) }
                current = .enabled
            }, pause: { delays.append($0) }
        )
        #expect(registrations == 3)
        #expect(delays == [100, 200])
    }

    @Test("persistent post-update permission failure has a fixed retry bound")
    func boundsRefreshRetries() async {
        var registrations = 0
        var delays: [Int] = []
        do {
            try await TraceHelperRegistration.ensureAfterRefresh(
                helperIsEmbedded: true, refreshed: true, status: { .notRegistered },
                register: {
                    registrations += 1
                    throw NSError(domain: "SMAppServiceErrorDomain", code: 1)
                }, pause: { delays.append($0) }
            )
            Issue.record("Persistent failure must fail")
        } catch {
            #expect(error.localizedDescription.contains("SMAppServiceErrorDomain:1"))
        }
        #expect(registrations == 6)
        #expect(delays.reduce(0, +) == 3100)
    }

    @Test("ordinary registration and unrelated errors never retry", arguments: [false, true])
    func doesNotRetryOtherFailures(refreshed: Bool) async {
        var registrations = 0
        var pauses = 0
        do {
            try await TraceHelperRegistration.ensureAfterRefresh(
                helperIsEmbedded: true, refreshed: refreshed, status: { .notRegistered },
                register: {
                    registrations += 1
                    throw NSError(domain: refreshed ? "OtherDomain" : "SMAppServiceErrorDomain", code: 1)
                }, pause: { _ in pauses += 1 }
            )
            Issue.record("Registration must fail")
        } catch {}
        #expect(registrations == 1)
        #expect(pauses == 0)
    }

    @Test("approval required during refresh stops before retry")
    func retryPreservesApproval() async {
        var current = SMAppService.Status.notRegistered
        var registrations = 0
        var pauses = 0
        do {
            try await TraceHelperRegistration.ensureAfterRefresh(
                helperIsEmbedded: true, refreshed: true, status: { current },
                register: {
                    registrations += 1
                    current = .requiresApproval
                    throw NSError(domain: "SMAppServiceErrorDomain", code: 1)
                }, pause: { _ in pauses += 1 }
            )
            Issue.record("Approval must remain required")
        } catch {
            guard case .helperApprovalRequired = error as? PrivilegedTraceError else {
                Issue.record("Wrong approval error: \(error)")
                return
            }
        }
        #expect(registrations == 1)
        #expect(pauses == 0)
    }

    @Test("cancelled refresh backoff stops without another registration")
    func cancellationStopsRetries() async {
        var registrations = 0
        do {
            try await TraceHelperRegistration.ensureAfterRefresh(
                helperIsEmbedded: true, refreshed: true, status: { .notRegistered },
                register: {
                    registrations += 1
                    throw NSError(domain: "SMAppServiceErrorDomain", code: 1)
                }, pause: { _ in throw CancellationError() }
            )
            Issue.record("Cancellation must stop registration")
        } catch {
            #expect(error is CancellationError)
        }
        #expect(registrations == 1)
    }

    @Test("updated enabled helper is unregistered before registration", arguments: [Optional<String>.none, "14"])
    func refreshesUpdatedHelper(previous: String?) async throws {
        var unregistrations = 0
        try await TraceHelperRegistration.refreshIfNeeded(
            helperIsEmbedded: true, status: .enabled,
            installedBuild: "15", registeredBuild: previous
        ) { unregistrations += 1 }
        #expect(unregistrations == 1)
    }

    @Test("current or unapproved helper is not unregistered", arguments: [SMAppService.Status.enabled, .requiresApproval, .notRegistered, .notFound])
    func preservesCurrentOrUnapprovedHelper(status: SMAppService.Status) async throws {
        var unregistrations = 0
        try await TraceHelperRegistration.refreshIfNeeded(
            helperIsEmbedded: true, status: status,
            installedBuild: "15", registeredBuild: status == .enabled ? "15" : "14"
        ) { unregistrations += 1 }
        #expect(unregistrations == 0)
    }

    @Test("failed helper refresh retains the underlying error")
    func refreshFailureIsActionable() async {
        do {
            try await TraceHelperRegistration.refreshIfNeeded(
                helperIsEmbedded: true, status: .enabled,
                installedBuild: "15", registeredBuild: "14"
            ) { throw NSError(domain: "RefreshTest", code: 42) }
            Issue.record("Failed unregister must stop registration")
        } catch {
            #expect(error.localizedDescription.contains("RefreshTest:42"))
        }
    }

    @Test("embedded services register after a lookup failure", arguments: [SMAppService.Status.notFound, .notRegistered])
    func registersEmbeddedService(initial: SMAppService.Status) throws {
        var current = initial
        var registrations = 0
        try TraceHelperRegistration.ensureRegistered(helperIsEmbedded: true, status: { current }) {
            registrations += 1
            current = .enabled
        }
        #expect(registrations == 1)
        #expect(current == .enabled)
    }

    @Test("enabled services do not register again")
    func enabledServiceIsUnchanged() throws {
        var registrations = 0
        try TraceHelperRegistration.ensureRegistered(helperIsEmbedded: true, status: { .enabled }) {
            registrations += 1
        }
        #expect(registrations == 0)
    }

    @Test("missing bundle files fail before registration")
    func missingBundleFails() {
        var registrations = 0
        do {
            try TraceHelperRegistration.ensureRegistered(helperIsEmbedded: false, status: { .notFound }) {
                registrations += 1
            }
            Issue.record("Missing helper files must fail")
        } catch {
            guard case .helperMissing = error as? PrivilegedTraceError else {
                Issue.record("Wrong missing-helper error: \(error)")
                return
            }
        }
        #expect(registrations == 0)
    }

    @Test("approval is required before and after registration", arguments: [false, true])
    func approvalIsNotBypassed(afterRegistration: Bool) {
        var current: SMAppService.Status = afterRegistration ? .notFound : .requiresApproval
        var registrations = 0
        do {
            try TraceHelperRegistration.ensureRegistered(helperIsEmbedded: true, status: { current }) {
                registrations += 1
                current = .requiresApproval
            }
            Issue.record("Approval must remain required")
        } catch {
            guard case .helperApprovalRequired = error as? PrivilegedTraceError else {
                Issue.record("Wrong approval error: \(error)")
                return
            }
        }
        #expect(registrations == (afterRegistration ? 1 : 0))
    }

    @Test("registration failures retain their domain and code")
    func registrationErrorIsActionable() {
        do {
            try TraceHelperRegistration.ensureRegistered(helperIsEmbedded: true, status: { .notFound }) {
                throw NSError(domain: "RegistrationTest", code: 42)
            }
            Issue.record("Registration error must fail")
        } catch {
            #expect(error.localizedDescription.contains("RegistrationTest:42"))
        }
    }

    @Test("registration must result in an enabled service")
    func unsuccessfulRegistrationFails() {
        var registrations = 0
        do {
            try TraceHelperRegistration.ensureRegistered(helperIsEmbedded: true, status: { .notFound }) {
                registrations += 1
            }
            Issue.record("Unchanged lookup failure must fail")
        } catch {
            #expect(error.localizedDescription.contains("did not become enabled"))
        }
        #expect(registrations == 1)
    }
}
