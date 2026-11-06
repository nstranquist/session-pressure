import SwiftUI

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
                    .help("Show or hide the NDev Pressure icon in the menu bar. This setting is saved and takes effect immediately.")
            } footer: {
                Text("To show the icon again, open NDev Pressure from the Dock or Applications, then choose Settings… (⌘,).")
            }
        }
        .formStyle(.grouped)
        .frame(width: 420)
        .fixedSize(horizontal: false, vertical: true)
    }
}
