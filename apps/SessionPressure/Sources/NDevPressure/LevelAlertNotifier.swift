import AppKit
import Foundation
import NDevPressureCore
import UserNotifications

/// Opt-in macOS notifications when host pressure rises to Red or Critical.
///
/// Off by default. Permission is requested only when the operator turns
/// alerts on. The notification center is touched lazily and only from a real
/// app bundle, so tests and `swift run` never crash on a missing bundle.
@MainActor
final class LevelAlertNotifier: NSObject, ObservableObject {
    @Published private(set) var authorization: UNAuthorizationStatus = .notDetermined
    @Published private(set) var lastAlertAt: Date?

    private var gate = LevelAlertGate()
    private var delegateInstalled = false

    private var center: UNUserNotificationCenter? {
        guard Bundle.main.bundleURL.pathExtension == "app",
              Bundle.main.bundleIdentifier != nil else { return nil }
        let center = UNUserNotificationCenter.current()
        if !delegateInstalled {
            center.delegate = self
            delegateInstalled = true
        }
        return center
    }

    var isAvailable: Bool { center != nil }

    func refreshAuthorization() async {
        guard let center else { return }
        authorization = await center.notificationSettings().authorizationStatus
    }

    /// Returns true when alerts can be delivered.
    @discardableResult
    func requestAuthorization() async -> Bool {
        guard let center else { return false }
        let granted = (try? await center.requestAuthorization(options: [.alert, .sound])) ?? false
        await refreshAuthorization()
        return granted
    }

    /// Feed every board level. The gate tracks transitions even while alerts
    /// are off, so turning them on never fires for an old transition.
    func observe(level: PressureLevel, detail: String) {
        let threshold = AppPreferences.alertThreshold()
        guard let fired = gate.observe(level, threshold: threshold) else { return }
        post(
            title: "Host pressure is \(fired.displayName)",
            body: detail,
            identifier: "level-\(fired.rawValue)"
        )
    }

    func sendTest() {
        post(
            title: "NDev Pressure test alert",
            body: "Alerts are on. You will be notified when pressure rises to the chosen level.",
            identifier: "test"
        )
    }

    private func post(title: String, body: String, identifier: String) {
        guard let center else { return }
        let content = UNMutableNotificationContent()
        content.title = title
        content.body = body
        content.sound = .default
        content.userInfo = ["url": "ndev-pressure://overview"]
        let request = UNNotificationRequest(identifier: identifier, content: content, trigger: nil)
        // Record an alert only when macOS will actually show it, so "Last
        // alert" never claims a delivery that permission denied.
        Task { @MainActor in
            let status = await center.notificationSettings().authorizationStatus
            authorization = status
            guard status == .authorized || status == .provisional else { return }
            do {
                try await center.add(request)
                lastAlertAt = Date()
            } catch {}
        }
    }

    static func openNotificationSettings() {
        if let url = URL(string: "x-apple.systempreferences:com.apple.Notifications-Settings.extension") {
            NSWorkspace.shared.open(url)
        }
    }

    var authorizationLabel: String {
        switch authorization {
        case .authorized, .provisional, .ephemeral: "Allowed"
        case .denied: "Denied in System Settings"
        case .notDetermined: "Not requested yet"
        @unknown default: "Unknown"
        }
    }
}

extension LevelAlertNotifier: UNUserNotificationCenterDelegate {
    nonisolated func userNotificationCenter(
        _ center: UNUserNotificationCenter,
        willPresent notification: UNNotification
    ) async -> UNNotificationPresentationOptions {
        [.banner, .sound]
    }

    nonisolated func userNotificationCenter(
        _ center: UNUserNotificationCenter,
        didReceive response: UNNotificationResponse
    ) async {
        let raw = response.notification.request.content.userInfo["url"] as? String
        await MainActor.run {
            NSApp.activate(ignoringOtherApps: true)
            if let raw, let url = URL(string: raw) { NSWorkspace.shared.open(url) }
        }
    }
}
