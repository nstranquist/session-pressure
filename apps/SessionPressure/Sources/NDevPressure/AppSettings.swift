import SwiftUI
import NDevPressureCore

enum AppSettings {
    static let showMenuBarExtraKey = "showMenuBarExtra"
}

struct AppSettingsView: View {
    @EnvironmentObject private var store: PressureStore
    @AppStorage(AppSettings.showMenuBarExtraKey) private var showMenuBarExtra = true

    var body: some View {
        Form {
            Section("Executing product") {
                if let identity = store.capabilities {
                    LabeledContent("Version", value: identity.version)
                    LabeledContent("Source revision", value: identity.sourceRevision ?? "Unknown")
                    LabeledContent("Executable", value: identity.executable)
                    LabeledContent("Platform", value: "\(identity.os)/\(identity.arch)")
                    Text("Supported operations: " + identity.operations.joined(separator: ", "))
                        .font(.caption).foregroundStyle(.secondary)
                } else { Text("Product identity has not been read.") }
                if let error = store.identityError { Text(error).foregroundStyle(.red) }
                Button("Refresh product identity") { Task { await store.refreshIdentity() } }
            }

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
