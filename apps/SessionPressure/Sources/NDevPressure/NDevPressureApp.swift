import AppKit
import SwiftUI

@main
struct NDevPressureApp: App {
    @NSApplicationDelegateAdaptor(AppDelegate.self) private var appDelegate
    @StateObject private var store = PressureStore()

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

                Button("Overview") { store.selectedSection = .overview }
                    .keyboardShortcut("1", modifiers: [.command])
                Button("Agent Trees") { store.selectedSection = .trees }
                    .keyboardShortcut("2", modifiers: [.command])
                Button("Disk Writes") { store.selectedSection = .diskWrites }
                    .keyboardShortcut("3", modifiers: [.command])
                Button("Storage") { store.openStorage(tab: .disk) }
                    .keyboardShortcut("4", modifiers: [.command])
                Button("Work Queue") { store.selectedSection = .work }
                    .keyboardShortcut("5", modifiers: [.command])
                Button("Policy") { store.selectedSection = .policy }
                    .keyboardShortcut("6", modifiers: [.command])
                Button("Monitor") { store.selectedSection = .monitor }
                    .keyboardShortcut("7", modifiers: [.command])
                Button("Telemetry") { store.selectedSection = .telemetry }
                    .keyboardShortcut("8", modifiers: [.command])
            }
        }

        MenuBarExtra {
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
