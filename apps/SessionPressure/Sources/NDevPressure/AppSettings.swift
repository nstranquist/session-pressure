import SwiftUI
import NDevPressureCore

enum AppSettings {
    static let showMenuBarExtraKey = "showMenuBarExtra"
}

struct AppSettingsView: View {
    @AppStorage(AppSettings.showMenuBarExtraKey) private var showMenuBarExtra = true

    var body: some View {
        Form {
            Section {
                Toggle("Show in menu bar", isOn: $showMenuBarExtra)
                    .toggleStyle(.checkbox)
                    .help(PressureHelp.showMenuBarExtra)
            } header: {
                Text("Appearance")
            } footer: {
                Text("To show the icon again, open NDev Pressure from the Dock or Applications, then select Settings in the sidebar or choose Settings… (⌘,).")
            }
        }
        .formStyle(.grouped)
    }
}
