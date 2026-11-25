import ServiceManagement
import Testing
@testable import NDevPressure

/// `ndev mac menubar` parses this line (`parseAppSwitch` in nicos-tools
/// nicos-dev/internal/mac/menubar.go). The words must not change.
@Suite("launch-at-login switch")
struct LaunchAtLoginSwitchTests {
    @Test("status names match the ndev contract", arguments: [
        (SMAppService.Status.enabled, "enabled"),
        (.notRegistered, "not-registered"),
        (.requiresApproval, "requires-approval"),
        (.notFound, "not-found"),
    ])
    func names(status: SMAppService.Status, want: String) {
        #expect(LaunchAtLoginSwitch.name(status) == want)
    }

    @Test("state line carries the bundle path")
    func line() {
        #expect(LaunchAtLoginSwitch.line(.enabled, bundlePath: "/Applications/NDev Pressure.app")
            == "launch-at-login: enabled (/Applications/NDev Pressure.app)")
    }

    @Test("bad mode exits 2 without touching the service")
    func usage() {
        #expect(LaunchAtLoginSwitch.run("maybe") == 2)
    }
}
