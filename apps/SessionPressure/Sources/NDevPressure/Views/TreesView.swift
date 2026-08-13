import SwiftUI
import NDevPressureCore

struct TreesView: View {
    @EnvironmentObject private var store: PressureStore
    @State private var query = ""
    @State private var sort: Sort = .rss

    enum Sort: String, CaseIterable, Identifiable {
        case rss = "RSS"
        case age = "Age"
        case cpu = "CPU"
        case procs = "Procs"
        var id: String { rawValue }
    }

    private var trees: [AgentTree] {
        var items = store.board.snapshot.topAgentTrees
        let q = query.trimmingCharacters(in: .whitespacesAndNewlines).lowercased()
        if !q.isEmpty {
            items = items.filter {
                $0.agent.lowercased().contains(q)
                    || $0.executable.lowercased().contains(q)
                    || String($0.rootPID).contains(q)
                    || ($0.sessionID?.lowercased().contains(q) ?? false)
            }
        }
        switch sort {
        case .rss: items.sort { $0.rssSumMB > $1.rssSumMB }
        case .age: items.sort { ($0.elapsedSeconds ?? 0) > ($1.elapsedSeconds ?? 0) }
        case .cpu: items.sort { $0.cpuPercentSum > $1.cpuPercentSum }
        case .procs: items.sort { $0.processCount > $1.processCount }
        }
        return items
    }

    var body: some View {
        VStack(spacing: 0) {
            toolbar
            Divider()
            if trees.isEmpty {
                EmptyHint(
                    title: "No matching trees",
                    systemImage: "tree",
                    detail: "Live inventory comes from the resident monitor or a fresh snapshot."
                )
                .frame(maxHeight: .infinity)
            } else {
                List {
                    ForEach(Array(trees.enumerated()), id: \.element.id) { index, tree in
                        TreeRow(tree: tree, rank: index + 1)
                            .listRowInsets(EdgeInsets(top: 4, leading: 16, bottom: 4, trailing: 16))
                    }
                }
                .listStyle(.inset)
            }
        }
        .background(PressureTheme.bg)
    }

    private var toolbar: some View {
        HStack(spacing: 12) {
            TextField("Filter agent, pid, session…", text: $query)
                .textFieldStyle(.roundedBorder)
                .frame(maxWidth: 320)

            Picker("Sort", selection: $sort) {
                ForEach(Sort.allCases) { s in
                    Text(s.rawValue).tag(s)
                }
            }
            .pickerStyle(.segmented)
            .frame(maxWidth: 280)

            Spacer()

            Text("\(trees.count) trees · \(PressureFormat.mb(store.board.snapshot.agentRSSSumMB)) sum")
                .font(PressureTheme.monoCaption)
                .foregroundStyle(.secondary)

            Button {
                Task { await store.refresh(live: true, light: false) }
            } label: {
                Label("Live sample", systemImage: "bolt.horizontal.circle")
            }
            .disabled(store.isRefreshing)
            .help(PressureHelp.liveSample)
        }
        .padding(14)
    }
}
