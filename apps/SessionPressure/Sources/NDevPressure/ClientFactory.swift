import Foundation
import NDevPressureCore

/// The one place the app builds a control-plane client. Saved Settings
/// overrides are applied here, so every pane (board, Hetzner, incidents,
/// browser control) resolves the same binaries and API transport.
enum ClientFactory {
    static func environment() -> [String: String] {
        AppPreferences.clientEnvironment().environment
    }

    static func make(binaryPath: String? = nil) throws -> NDevPressureClient {
        try NDevPressureClient(binaryPath: binaryPath, environment: environment())
    }
}
