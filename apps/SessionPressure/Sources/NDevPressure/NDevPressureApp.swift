import AppKit
import SwiftUI

@main
struct NDevPressureApp: App {
    @NSApplicationDelegateAdaptor(AppDelegate.self) private var appDelegate
    @StateObject private var store = PressureStore()
    @AppStorage(AppSettings.showMenuBarExtraKey) private var showMenuBarExtra = true

    var body: some Scene {
        Window("NDev Pressure", id: "main") {
            ContentView()
                .environmentObject(store)
        }
        .defaultSize(width: 1180, height: 780)
        .windowResizability(.contentMinSize)
        .commands {
            CommandGroup(replacing: .newItem) {}
            CommandMenu("Pressure") {
                Button("Refresh") {
                    Task { await store.refresh(live: false, light: false) }
                }
                .keyboardShortcut("r", modifiers: [.command])

                Button("Live Sample") {
                    Task { await store.refresh(live: true, light: false) }
                }
                .keyboardShortcut("r", modifiers: [.command, .shift])

                Divider()

                ForEach(PressureStore.Section.allCases) { section in
                    Button(section.rawValue) {
                        if section == .storage { store.openStorage(tab: .disk) }
                        else { store.selectedSection = section }
                    }
                    .keyboardShortcut(
                        KeyEquivalent(section.commandDigit.first ?? "1"),
                        modifiers: section.shortcutModifiers
                    )
                }
            }
        }

        Settings {
            AppSettingsView()
                .frame(width: 420)
                .fixedSize(horizontal: false, vertical: true)
        }

        MenuBarExtra(isInserted: $showMenuBarExtra) {
            MenuBarContent()
                .environmentObject(store)
        } label: {
            MenuBarLabel()
                .environmentObject(store)
        }
        .menuBarExtraStyle(.window)
    }
}

final class AppDelegate: NSObject, NSApplicationDelegate {
    func applicationDidFinishLaunching(_ notification: Notification) {
        NSApp.setActivationPolicy(.regular)
    }

    func applicationShouldTerminateAfterLastWindowClosed(_ sender: NSApplication) -> Bool {
        // Keep menu bar extra alive when the main window closes.
        false
    }
}
