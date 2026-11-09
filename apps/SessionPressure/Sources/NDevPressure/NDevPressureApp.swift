import AppKit
import SwiftUI
import NDevPressureCore

@main
struct NDevPressureApp: App {
    @NSApplicationDelegateAdaptor(AppDelegate.self) private var appDelegate
    @StateObject private var store = PressureStore()
    @AppStorage(AppPreferenceKey.showMenuBarExtra) private var showMenuBarExtra = true
    @AppStorage(AppPreferenceKey.showDayGate) private var showDayGate = true
    @AppStorage(AppPreferenceKey.showHetzner) private var showHetzner = true

    private func visibleSection(_ section: PressureStore.Section) -> Bool {
        switch section {
        case .dayGate: showDayGate
        case .hetzner: showHetzner
        default: true
        }
    }

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

                ForEach(PressureStore.Section.allCases.filter { visibleSection($0) }) { section in
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
            SettingsWindowView()
                .environmentObject(store)
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
