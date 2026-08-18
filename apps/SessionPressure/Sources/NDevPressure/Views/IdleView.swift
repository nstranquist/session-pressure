import SwiftUI
import NDevPressureCore

struct IdleView: View {
    @EnvironmentObject private var store: PressureStore
    var embedded: Bool = false
    @State private var pending: AgentTree?

    private var candidates: [AgentTree] { store.board.idleCandidates }

    var body: some View {
        VStack(spacing: 0) {
            if !embedded {
                header
                Divider()
            } else {
                idleToolbar
                Divider()
            }
            if candidates.isEmpty {
                EmptyHint(
                    title: "No idle candidates",
                    systemImage: "moon.zzz",
                    detail: "Default criteria: age ≥ 12h and CPU ≤ 0.25%. Apply is exact PID+session and revalidated."
                )
                .frame(maxHeight: .infinity)
            } else {
                List {
                    ForEach(candidates) { tree in
                        TreeRow(tree: tree, showActions: true) {
                            pending = tree
                        }
                        .listRowInsets(EdgeInsets(top: 4, leading: 16, bottom: 4, trailing: 16))
                    }
                }
                .listStyle(.inset)
            }
        }
        .background(PressureTheme.bg)
        .confirmationDialog(
            "Gracefully stop idle tree?",
            isPresented: Binding(
                get: { pending != nil },
                set: { if !$0 { pending = nil } }
            ),
            titleVisibility: .visible
        ) {
            if let tree = pending {
                Button("SIGTERM pid \(tree.rootPID)", role: .destructive) {
                    Task {
                        await store.applyIdle(tree: tree)
                        pending = nil
                    }
                }
            }
            Button("Cancel", role: .cancel) { pending = nil }
        } message: {
            if let tree = pending {
                Text("Exact apply for \(tree.agent) pid \(tree.rootPID) session \(tree.sessionID ?? "—"). The CLI re-samples and rejects identity/activity drift before signaling.")
            }
        }
    }

    private var header: some View {
        HStack {
            VStack(alignment: .leading, spacing: 4) {
                Text("Idle cleanup")
                    .font(.title2.weight(.semibold))
                Text("Operator-confirmed only. Automatic relief remains policy-gated at critical.")
                    .font(.caption)
                    .foregroundStyle(.secondary)
            }
            Spacer()
            Text("\(candidates.count) candidate(s)")
                .font(PressureTheme.monoCaption)
                .foregroundStyle(.secondary)
            Button {
                Task { await store.refresh(live: false, light: false) }
            } label: {
                Label("Refresh", systemImage: "arrow.clockwise")
            }
            .disabled(store.isRefreshing)
        }
        .padding(14)
    }

    private var idleToolbar: some View {
        HStack {
            Text("\(candidates.count) idle candidate(s)")
                .font(PressureTheme.monoCaption)
                .foregroundStyle(.secondary)
            Spacer()
            Button {
                Task { await store.refresh(live: false, light: false) }
            } label: {
                Label("Refresh", systemImage: "arrow.clockwise")
            }
            .disabled(store.isRefreshing)
        }
        .padding(.horizontal, 14)
        .padding(.vertical, 8)
    }
}
