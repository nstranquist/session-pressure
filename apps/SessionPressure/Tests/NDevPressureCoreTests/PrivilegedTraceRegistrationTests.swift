import Foundation
import ServiceManagement
import Testing
@testable import NDevPressure

@Suite("Privileged trace service registration")
@MainActor
struct PrivilegedTraceRegistrationTests {
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
