import SwiftUI
import NDevPressureCore

// MARK: - Help / Docs Model

struct HelpArticle: Identifiable {
    let id: String
    let title: String
    let icon: String
    let summary: String
    let body: String
    let section: PressureStore.Section?
    let keyboard: String?
    var docsURLs: [String]? = nil
}

enum HelpWiki {
    static let articles: [HelpArticle] = [
        HelpArticle(id: "getting-started", title: "Getting Started", icon: "play.circle.fill",
                    summary: "Launch, first refresh and reading the main window.",
                    body: """
                    NDev Pressure opens on Overview. The sidebar on the left is the map; every pane is a view onto the same composite board. The bell and back button in the toolbar are always there: the back stack returns to the previous pane after you dive from a card, the bell lists active pressure, admission, or Day Gate alerts and dismisses when you view or click them.

                    Press ⌘1 … ⌘9 + ⌘R + ⌘L for panes, ⌘[ for back, and ⌘, for the Settings window. The three footer lines (free / cpu / trees) mirror the header. Everything is read-only — the app never kills or deletes for you; it tells you what to do next.
                    """,
                    section: .overview, keyboard: "⌘1 – ⌘9, ⌘R, ⌘L, ⌘[", docsURLs: ["https://github.com/nicos/session-pressure"]),

        HelpArticle(id: "overview", title: "Overview", icon: "gauge.with.dots.needle.67percent",
                    summary: "Host pressure at a glance — every little card jumps to details.",
                    body: """
                    The header ring shows host pressure (Normal/Warning/Red/Critical) and the level chip is colored. Click any metric card to jump:

                    • Free memory → Policy thresholds
                    • Swap → Policy thresholds
                    • Host CPU → Monitor health
                    • Agent RSS sum → Agent Trees
                    • Guard RSS / Sample cost → Monitor
                    • Memory momentum → Telemetry
                    • Storage available → Storage (disk) reclaim

                    Below the cards, Pressure reasons + Admission tell you whether launches are admitted and, if blocked, the source reason. “Top host consumers” and “Top agent trees” are ranked previews; the “Open full ranking” buttons open the scored list. Interruptions & timeouts (when present) jump to Telemetry, not a fabricated count.
                    """,
                    section: .overview, keyboard: "⌘1"),

        HelpArticle(id: "agent-trees", title: "Agent Trees", icon: "tree",
                    summary: "Ranked inventory of agent process trees — every entry is a launch tree, not a ps line.",
                    body: """
                    Sorted by RSS (or Age/CPU/Procs). The count and total (10 trees • 2.8 GiB) are the resident monitor’s canonical tree count and sum — the blue sparkline top is “most RSS” and the yellow bottom is “highest CPU.” Expand a tree to see its processes, thread and stack details (on-device only). Empty here means no qualifying agent tree exists, not that the probe failed. Owner paths use the actual cwd.
                    """,
                    section: .trees, keyboard: "⌘2"),

        HelpArticle(id: "day-gate", title: "Day Gate", icon: "calendar.badge.exclamationmark",
                    summary: "One day window gate. The bell surfaces its residue so you handle it before tomorrow.",
                    body: """
                    The Day Gate is the local-state concurrency gate. Day residency is 79 items (admitted=30, limit=0), queue 0/8, oldest 0s/1m (enforced). The 3 residue items are “needs review” — the bell’s residue row dismisses only after you open Day Gate or dismiss from the bell; it reappears if the count changes.
                    """,
                    section: .dayGate, keyboard: "⇧⌘1"),

        HelpArticle(id: "work-queue", title: "Work Queue", icon: "rectangle.stack",
                    summary: "Heavy work coordinator — one heavy process at a time.",
                    body: """
                    Host process trees and durable queue. The sweep is at 7.2s. Queue depth is paced by pressure. Same-queue starvation is marked. Hetzner runner states (host is ubuntu/hetzner, provider=null) are shown when present; the on-box state stays local. Clicking a leaked process name filters the table — always filter-local, pagination stays.
                    """,
                    section: .work, keyboard: "⌘5"),

        HelpArticle(id: "policy", title: "Policy", icon: "slider.horizontal.3",
                    summary: "Thresholds, pressure levels and protection mode.",
                    body: """
                    Policy is the single source: free warning/red/critical, swap, storage, cpu. The sliders in the pane edit them; Reset restores defaults. Protection mode is Balanced (enabled, admission=true). The disk-write host components include the lru writer and cache-trim policy so you see the host sampler and writer share the same clock without double-counting.
                    """,
                    section: .policy, keyboard: "⌘6"),

        HelpArticle(id: "monitor", title: "Monitor", icon: "heart.text.square",
                    summary: "LaunchAgent lifecycle + helper health vs. budget.",
                    body: """
                    Shows LaunchAgent installed/loaded/artifact verified, PID, and Resident health: protection, healthy, duty, peak RSS, sample wall/cpu, telemetry today. The upgrade/replace path is: install new helper → ensure it runs → checksum → rdev (no manual rename kills). Yellow indicates enough evidence to bring up.
                    """,
                    section: .monitor, keyboard: "⌘7"),

        HelpArticle(id: "telemetry", title: "Telemetry", icon: "waveform.path.ecg",
                    summary: "Pressure transitions and heartbeats.",
                    body: """
                    Lists monitor heartbeats (Normal/Critical etc.), disk-write-state transitions, interruptions & wait timeouts. Signal cancellations and unspecified interruptions are separate. The evidence is bounded — 48 entries.
                    """,
                    section: .telemetry, keyboard: "⌘8"),

        HelpArticle(id: "storage", title: "Storage", icon: "externaldrive.badge.checkmark",
                    summary: "Disk reclaim and idle trees.",
                    body: """
                    Auto-safe and operator tiers with per-target status + native capacity. Idle trees show Nx count, total bytes, enclosure, transfer until reaped. “Clean” via menu changes the binary only; no bulk delete.
                    """,
                    section: .storage, keyboard: "⌘4"),

        HelpArticle(id: "disk-writes", title: "Disk Writes", icon: "internaldrive",
                    summary: "Live disk-write attribution.",
                    body: """
                    Host writes total, kernel_swap, hot paths (pnpm-store, session-pressure, Xcode cache). The attribution is best-effort correlation — a row is a lead, not proof of exact bytes.
                    """,
                    section: .diskWrites, keyboard: "⌘3"),

        HelpArticle(id: "process-incidents", title: "Process Incidents", icon: "waveform.path.ecg",
                    summary: "Unmanaged/idle incidents and their recovery.",
                    body: """
                    Shows publisher/consumer groups, incidents by app, and recovery details. Up to 100 incident files, 30d retention, tombstone policy bound.
                    """,
                    section: .processIncidents, keyboard: "⌘0"),

        HelpArticle(id: "settings", title: "Settings", icon: "gearshape",
                    summary: "General, menu bar/dock, sidebar, keyboard, alerts, refresh, control plane, guard, diagnostics.",
                    body: """
                    The left rail is the jump list — click to jump, scroll the page and the highlighted rail item follows (scroll-spy). At the bottom: Binary path, Display version, ndev version, helper health, record integrity, telemetry projection. ⌘, opens the standard macOS Settings window; ⌘9 opens this pane. Dock badge can be set from Menu bar & Dock. All settings persist locally; no cloud.
                    """,
                    section: .settings, keyboard: "⌘9 (in app), ⌘, (window)"),

        HelpArticle(id: "notifications", title: "Notifications (Bell)", icon: "bell.badge.fill",
                    summary: "When the bell dims, you have seen it.",
                    body: """
                    The bell in the top-right toolbar shows its worst severity color. Clicking it opens the panel; closing the panel (or clicking a row) dismisses that exact notification even though the host condition persists — it will not re-alert until the ID changes (pressure-red → critical, residue-3 → residue-4). To confirm dismissal, reopen the bell: it shows “All clear” until a new ID appears.
                    """,
                    section: nil, keyboard: "—"),

        HelpArticle(id: "back", title: "Back Navigation", icon: "chevron.left",
                    summary: "Fixed slot, no layout jump.",
                    body: """
                    The back chevron lives at the leading edge (left of the toolbar, next to Hide Sidebar) and always reserves its 28×28 slot (opacity 0 at root). Every pane change (card → detail, row → detail) pushes the previous pane; Back (⌘[) pops it. It never appears/disappears and shifts neighbours.
                    """,
                    section: nil, keyboard: "⌘["),

        HelpArticle(id: "docs", title: "Docs & Wiki", icon: "book",
                    summary: "Where the on-device copy lives and how it stays current.",
                    body: """
                    This help is built in. The canonical documentation mirror is ~/code/docs (docs-puller). Local docs for reference images, the session-pressure repo and the ndev CLI live under docs/. The binary version shown in Diagnostics is the embedded info; the ndev --json session pressure board version is the runtime version — if they differ after rdev, the app prefers the latest and flags it.
                    """,
                    section: nil, keyboard: "—"),
    ]
}

// MARK: - About & Help View

struct HelpView: View {
    @EnvironmentObject private var store: PressureStore
    @State private var query = ""

    private var filtered: [HelpArticle] {
        let q = query.trimmingCharacters(in: .whitespacesAndNewlines).lowercased()
        guard !q.isEmpty else { return HelpWiki.articles }
        return HelpWiki.articles.filter { art in
            art.title.lowercased().contains(q)
            || art.summary.lowercased().contains(q)
            || art.body.lowercased().contains(q)
            || (art.section?.rawValue.lowercased().contains(q) ?? false)
        }
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            header
            searchBar
            if filtered.isEmpty {
                emptyState
            } else {
                articleList
            }
            footer
        }
        .background(Color(nsColor: .windowBackgroundColor))
    }

    private var header: some View {
        VStack(alignment: .leading, spacing: 8) {
            HStack(spacing: 10) {
                ZStack {
                    RoundedRectangle(cornerRadius: 10, style: .continuous)
                        .fill(PressureTheme.levelColor(.normal).opacity(0.14))
                        .frame(width: 36, height: 36)
                    Image(systemName: "gauge.with.dots.needle.67percent")
                        .font(.title2)
                        .foregroundStyle(PressureTheme.levelColor(.normal))
                }
                VStack(alignment: .leading, spacing: 2) {
                    Text("NDev Pressure — About & Help")
                        .font(.title2.weight(.semibold))
                    Text(aboutSubtitle)
                        .font(.caption)
                        .foregroundStyle(.secondary)
                }
                Spacer()
                Label("Docs", systemImage: "book")
                    .labelStyle(.iconOnly)
                    .help("Open documentation mirror")
                    .onTapGesture {
                        if let url = URL(string: "file:///Users/nicostran/code/docs") {
                            NSWorkspace.shared.open(url)
                        }
                    }
            }
            .padding(.horizontal, 18)
            .padding(.top, 16)
        }
    }

    private var aboutSubtitle: String {
        let version = Bundle.main.object(forInfoDictionaryKey: "CFBundleShortVersionString") as? String ?? "dev"
        let build = Bundle.main.object(forInfoDictionaryKey: "CFBundleVersion") as? String ?? "local"
        return "\(version) (\(build)) · read-only · no data leaves the device"
    }

    private var searchBar: some View {
        HStack(spacing: 8) {
            Image(systemName: "magnifyingglass")
                .foregroundStyle(.secondary)
            TextField("Search guide — try 'policy', 'disk', 'back', 'bell' …", text: $query)
                .textFieldStyle(.plain)
                .font(.body)
            if !query.isEmpty {
                Button { query = "" } label: {
                    Image(systemName: "xmark.circle.fill")
                        .foregroundStyle(.secondary)
                }
                .buttonStyle(.plain)
                .help("Clear search")
            }
            if !query.isEmpty {
                Text("\(filtered.count) of \(HelpWiki.articles.count)")
                    .font(.caption)
                    .foregroundStyle(.secondary)
                    .monospacedDigit()
            }
        }
        .padding(.horizontal, 12)
        .padding(.vertical, 8)
        .background(Color(nsColor: .controlBackgroundColor), in: RoundedRectangle(cornerRadius: 9, style: .continuous))
        .overlay(RoundedRectangle(cornerRadius: 9, style: .continuous).stroke(PressureTheme.hairline, lineWidth: 1))
        .padding(.horizontal, 18)
        .padding(.top, 10)
    }

    private var articleList: some View {
        ScrollView(.vertical, showsIndicators: true) {
            LazyVStack(alignment: .leading, spacing: 10) {
                ForEach(filtered) { art in
                    HelpArticleCard(article: art) {
                        if let sec = art.section {
                            store.selectedSection = sec
                            // Help pane dismissal is via back chevron / sidebar; not auto-dismissed
                        }
                    }
                }
            }
            .padding(.horizontal, 18)
            .padding(.top, 12)
            .padding(.bottom, 20)
        }
    }

    private var emptyState: some View {
        VStack(spacing: 10) {
            Image(systemName: "magnifyingglass")
                .font(.title2)
                .foregroundStyle(.secondary)
            Text("No matches")
                .font(.headline)
            Text("Try a different term — every article is local, no network needed.")
                .font(.caption)
                .foregroundStyle(.secondary)
            Button("Clear") { query = "" }
                .buttonStyle(.bordered)
        }
        .frame(maxWidth: .infinity, minHeight: 180)
        .padding(20)
    }

    private var footer: some View {
        VStack(alignment: .leading, spacing: 6) {
            Divider()
            HStack(spacing: 12) {
                Text("Read-only — this app never deletes or kills for you.")
                    .font(.caption)
                    .foregroundStyle(.secondary)
                Spacer()
                Button("Open Settings") { store.selectedSection = .settings }
                    .font(.caption)
                Button("Overview") { store.selectedSection = .overview }
                    .font(.caption)
            }
            .padding(.horizontal, 18)
            .padding(.vertical, 8)
        }
    }
}

private struct HelpArticleCard: View {
    let article: HelpArticle
    var onOpen: () -> Void

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            HStack(spacing: 8) {
                Image(systemName: article.icon)
                    .font(.callout)
                    .foregroundStyle(PressureTheme.levelColor(.normal))
                    .frame(width: 22, alignment: .center)
                Text(article.title)
                    .font(.headline)
                Spacer()
                if let kb = article.keyboard {
                    Text(kb)
                        .font(.caption.monospaced())
                        .padding(.horizontal, 6).padding(.vertical, 2)
                        .background(Color.primary.opacity(0.06), in: Capsule())
                        .foregroundStyle(.secondary)
                }
                if article.section != nil {
                    Image(systemName: "chevron.right")
                        .font(.caption2.weight(.semibold))
                        .foregroundStyle(.tertiary)
                }
            }
            Text(article.summary)
                .font(.callout)
                .foregroundStyle(.secondary)
            Text(article.body)
                .font(.body)
                .lineSpacing(4)
                .fixedSize(horizontal: false, vertical: true)
            if let urls = article.docsURLs {
                ForEach(urls, id: \.self) { raw in
                    if let url = URL(string: raw) {
                        Link(raw, destination: url)
                            .font(.caption)
                    }
                }
            }
        }
        .padding(14)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(PressureTheme.card, in: RoundedRectangle(cornerRadius: 12, style: .continuous))
        .overlay(
            RoundedRectangle(cornerRadius: 12, style: .continuous)
                .stroke(PressureTheme.hairline, lineWidth: 1)
        )
        .onTapGesture {
            if article.section != nil { onOpen() }
        }
        .help(article.title)
    }
}
