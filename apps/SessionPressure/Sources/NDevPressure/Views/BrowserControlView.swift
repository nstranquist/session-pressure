import SwiftUI
import NDevPressureCore

struct BrowserControlView: View {
    @EnvironmentObject private var store: PressureStore

    var body: some View {
        SectionCard(title: "Agent browser control", systemImage: "hand.raised") {
            VStack(alignment: .leading, spacing: 8) {
                if let status = store.browserControl {
                    Label(status.blocked ? "Browser work blocked" : "Browser work permitted",
                          systemImage: status.blocked ? "pause.circle.fill" : "checkmark.circle")
                    Text(status.reason).font(.caption).foregroundStyle(.secondary)
                    Text("Browser jobs run in parallel while memory fits one more session. Each job has a \(status.maxRuntimeSeconds)-second limit.")
                        .font(.caption).foregroundStyle(.secondary)
                    HStack {
                        Button("Pause browser work") {
                            Task { await store.updateBrowserControl("pause") }
                        }
                        .disabled(store.isBrowserControlUpdating || status.mode == "paused")
                        Button("Automatic recovery") {
                            Task { await store.updateBrowserControl("auto") }
                        }
                        .disabled(store.isBrowserControlUpdating || status.mode == "auto")
                    }
                    Text("Automatic mode resumes after two fresh samples below the CPU recovery limit. Cleanup stays available.")
                        .font(.caption2).foregroundStyle(.secondary)
                } else if store.browserControlError == nil {
                    Text("Loading browser control status…")
                        .font(.caption)
                        .foregroundStyle(.secondary)
                }
                if let error = store.browserControlError {
                    Text(error).font(.caption).foregroundStyle(.red)
                }
            }
        }
    }
}
