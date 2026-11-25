import AppKit
import ServiceManagement

/// Process entry. `--launch-at-login on|off|status` runs the login item switch
/// and exits before any UI starts. Every other launch starts the app.
@main
enum NDevPressureEntry {
    @MainActor
    static func main() {
        let args = CommandLine.arguments
        if let i = args.firstIndex(of: LaunchAtLoginSwitch.flag) {
            exit(LaunchAtLoginSwitch.run(i + 1 < args.count ? args[i + 1] : "status"))
        }
        NDevPressureApp.main()
    }
}

/// The login switch every nicos menu bar app shares (`ndev mac menubar login`).
/// It acts on the same `SMAppService.mainApp` item as Settings > Launch at
/// login, so both stay in sync. Run it from the installed bundle's binary:
/// mainApp registers the bundle that contains the running executable.
enum LaunchAtLoginSwitch {
    static let flag = "--launch-at-login"

    /// Applies `on`, `off` or `status`, prints the resulting state line, and
    /// returns the exit code: 0 ok, 1 the service call failed, 2 bad usage.
    static func run(_ mode: String) -> Int32 {
        let service = SMAppService.mainApp
        do {
            switch mode {
            case "on":
                if service.status != .enabled { try service.register() }
            case "off":
                if service.status == .enabled || service.status == .requiresApproval { try service.unregister() }
            case "status":
                break
            default:
                FileHandle.standardError.write(Data("usage: \(flag) on|off|status\n".utf8))
                return 2
            }
        } catch {
            FileHandle.standardError.write(Data("login item: \(error.localizedDescription)\n".utf8))
            return 1
        }
        print(line(service.status, bundlePath: Bundle.main.bundlePath))
        return 0
    }

    /// `launch-at-login: <state> (<bundle path>)`, the line `ndev` parses.
    static func line(_ status: SMAppService.Status, bundlePath: String) -> String {
        "launch-at-login: \(name(status)) (\(bundlePath))"
    }

    static func name(_ status: SMAppService.Status) -> String {
        switch status {
        case .enabled: "enabled"
        case .notRegistered: "not-registered"
        case .requiresApproval: "requires-approval"
        case .notFound: "not-found"
        @unknown default: "unknown"
        }
    }
}
