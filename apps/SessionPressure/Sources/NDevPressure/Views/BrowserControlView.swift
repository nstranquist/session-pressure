import SwiftUI
import NDevPressureCore

struct BrowserControlView: View {
    @State private var status: BrowserControlStatus?
    @State private var error: String?
    @State private var changing = false
    @Environment(\.scenePhase) private var scenePhase

    var body: some View {
        SectionCard(title: "Agent browser control", systemImage: "hand.raised") {
            VStack(alignment: .leading, spacing: 8) {
                if let status {
                    Label(status.blocked ? "Browser work blocked" : "Browser work permitted",
                          systemImage: status.blocked ? "pause.circle.fill" : "checkmark.circle")
                    Text(status.reason).font(.caption).foregroundStyle(.secondary)
                    Text("One browser job at a time. Each job has a \(status.maxRuntimeSeconds)-second limit.")
                        .font(.caption).foregroundStyle(.secondary)
                    HStack {
                        Button("Pause browser work") { update("pause") }.disabled(changing || status.mode == "paused")
                        Button("Automatic recovery") { update("auto") }.disabled(changing || status.mode == "auto")
                    }
                    Text("Automatic mode resumes after two fresh samples below the CPU recovery limit. Cleanup stays available.")
                        .font(.caption2).foregroundStyle(.secondary)
                }
                if let error { Text(error).font(.caption).foregroundStyle(.red) }
            }
        }
        .task(id: scenePhase) {
            guard scenePhase == .active else { return }
            while !Task.isCancelled {
                await refresh("status")
                try? await Task.sleep(for: .seconds(10))
            }
        }
    }
    private func update(_ action: String) {
        changing = true
        Task { await refresh(action); changing = false }
    }
    @MainActor private func refresh(_ action: String) async {
        do { status = try await NDevPressureClient().browserControl(action).browser; error = nil }
        catch { self.error = error.localizedDescription }
    }
}
