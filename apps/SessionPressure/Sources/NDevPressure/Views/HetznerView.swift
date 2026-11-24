import SwiftUI
import AppKit
import NDevPressureCore

/// Hetzner gate-runner tab, top to bottom: fleet capacity and alerts, the
/// runners' scheduler and live jobs, the delivery-gate delegate, and the
/// selected runner's job receipts with inline logs. Read-only throughout.
///
/// Performance: the page is a lazy stack, so cards below the fold are not
/// built until scrolled to. Each card is its own view; filtered lists are
/// cached by the store's revision counters and rows are Equatable, so the
/// 15-second refresh does not re-filter 1,000 receipts or rebuild rows whose
/// data did not change.
struct HetznerView: View {
    @ObservedObject var hetzner: HetznerStore
    @ObservedObject var capacity: RunnerCapacityStore

    var body: some View {
        ScrollView {
            LazyVStack(alignment: .leading, spacing: 16) {
                RunnerCapacityCard(store: capacity, onSelectHost: { host in
                    Task { await hetzner.selectHost(host) }
                })
                RunnersCard(hetzner: hetzner)
                DelegateStatusCard(hetzner: hetzner)
                DelegateRunsCard(hetzner: hetzner)
                BoxJobsCard(hetzner: hetzner)
            }
            .padding(16)
        }
    }
}

// MARK: - Shared

enum HetznerUI {
    static func chip(_ text: String, color: Color) -> some View {
        Text(text)
            .font(.caption2.weight(.semibold))
            .padding(.horizontal, 6)
            .padding(.vertical, 2)
            .background(color.opacity(0.15), in: Capsule())
            .foregroundStyle(color)
    }

    static func filterLabel(_ text: String) -> some View {
        Text(text)
            .font(.caption.weight(.semibold))
            .foregroundStyle(.secondary)
    }

    static func copy(_ value: String) {
        NSPasteboard.general.clearContents()
        NSPasteboard.general.setString(value, forType: .string)
    }

    /// Operator text for the CLI's fixed error codes.
    static func describeBox(_ code: String, failure: String?) -> String {
        let cause: String
        switch failure {
        case "timeout": cause = "SSH timed out (12 s)"
        case "connect_failed": cause = "SSH could not connect (host down, DNS, tailnet or key)"
        case "remote_failed": cause = "the read command failed on the runner"
        case "output_too_large": cause = "the runner's output exceeded the read bound"
        default: cause = ""
        }
        switch code {
        case "gate_runner_read_failed": return cause.isEmpty ? "Runner read failed" : "Runner read failed: \(cause)"
        case "gate_runner_payload_invalid": return "The runner returned an unreadable job list"
        case "gate_runner_health_invalid": return "The runner returned incomplete health (load, memory or disk)"
        default: return code
        }
    }

    static func describeLocal(_ code: String) -> String {
        code.split(separator: " ").map { part -> String in
            switch part {
            case "delegate_ledger_read_failed": "the delegate ledger could not be read"
            case "delegate_config_read_failed": "gate-delegate.json could not be read"
            default: String(part)
            }
        }.joined(separator: "; ")
    }
}

private func chip(_ text: String, _ color: Color) -> some View {
    HetznerUI.chip(text, color: color)
}

// MARK: - Runners (scheduler + live jobs)

private struct RunnersCard: View {
    @ObservedObject var hetzner: HetznerStore

    var body: some View {
        SectionCard(title: "Runners", systemImage: "server.rack",
                    trailing: hetzner.fleet.map { "\($0.hosts.count) configured · generation \($0.generation)" }) {
            VStack(alignment: .leading, spacing: 12) {
                if let error = hetzner.fleetError {
                    Label(error, systemImage: "exclamationmark.triangle.fill")
                        .font(.caption).foregroundStyle(.orange)
                }
                if let fleet = hetzner.fleet {
                    ForEach(fleet.hosts) { host in
                        runnerSection(host)
                        if host.id != fleet.hosts.last?.id { Divider() }
                    }
                } else {
                    Text("Fleet inventory not loaded. Runner roles and capacity are unknown.")
                        .foregroundStyle(.secondary)
                }
                if let error = hetzner.liveError {
                    Label("Job feed: \(error)", systemImage: "exclamationmark.triangle.fill")
                        .font(.caption).foregroundStyle(.orange)
                }
                Text("Active hosts receive new work; reserve hosts need an explicit choice. Scheduler checks run about once a minute and job samples every 15 seconds while this pane is visible.")
                    .font(.caption2).foregroundStyle(.tertiary)
            }
        }
    }

    private func runnerSection(_ runner: RunnerHost) -> some View {
        VStack(alignment: .leading, spacing: 6) {
            HStack {
                Text(runner.name).font(.headline)
                chip(runner.roleLabel.uppercased(), runner.state == "active" ? .blue : .secondary)
                Spacer()
                if let observation = hetzner.observations[runner.name] {
                    if !observation.isFresh() {
                        chip("CHECK STALE", .orange)
                    } else if observation.healthy && !observation.drained {
                        chip("SCHEDULER HEALTHY", .green)
                    } else {
                        chip(observation.drained ? "DRAINED" : "UNAVAILABLE", .orange)
                    }
                } else {
                    chip("NOT CHECKED", .secondary)
                }
            }
            Text("\(runner.providerLabel) · \(runner.platform) · \(runner.user)@\(runner.name) · \(runner.heavySlots) heavy / \(runner.lightSlots) light slots · weight \(runner.weight)")
                .font(PressureTheme.monoCaption).foregroundStyle(.secondary)
            if let observation = hetzner.observations[runner.name] {
                Text("Scheduler now: \(observation.heavyJobs)/\(observation.heavySlots) heavy · \(observation.lightJobs)/\(observation.lightSlots) light · revision \(PressureFormat.shortDigest(observation.sourceCommit)) · checked \(PressureFormat.relative(observation.observedAt))")
                    .font(PressureTheme.monoCaption).foregroundStyle(.secondary)
                if let error = observation.error {
                    Text(error).font(.caption).foregroundStyle(.orange)
                }
            }
            DisclosureGroup("Profiles (\(runner.profiles.count))") {
                Text(runner.profiles.joined(separator: ", "))
                    .font(.caption).foregroundStyle(.secondary)
                    .frame(maxWidth: .infinity, alignment: .leading)
            }
            .font(.caption)
            if let live = hetzner.liveHosts.first(where: { $0.host == runner.name }) {
                liveHostSection(live)
            } else {
                Text("No job samples recorded for this runner. Scheduler checks above show reachability, not job history.")
                    .font(.caption).foregroundStyle(.secondary)
            }
        }
    }

    private func feedStale(_ host: LiveHost) -> Bool {
        host.feedStale || !hetzner.livePresent || hetzner.liveError != nil || host.pollError != nil
    }

    private func liveHostSection(_ host: LiveHost) -> some View {
        let stale = feedStale(host)
        return VStack(alignment: .leading, spacing: 6) {
            HStack(spacing: 8) {
                Text("Job feed").font(.subheadline.weight(.semibold))
                chip(stale ? "FEED STALE" : "LIVE", stale ? .secondary : .green)
                if host.pollError != nil { chip("POLL ERROR", .orange) }
                Spacer()
                Text("polled \(PressureFormat.relative(host.lastPollAt))")
                    .font(PressureTheme.monoCaption).foregroundStyle(.secondary)
            }
            if let vitals = liveVitalsLine(host) {
                Text(vitals).font(PressureTheme.monoCaption).foregroundStyle(.secondary)
            }
            if stale {
                Label("Live feed stale — the poller may be down; showing the last samples, not a live fleet.", systemImage: "exclamationmark.triangle.fill")
                    .font(.caption)
                    .foregroundStyle(PressureTheme.levelColor(.warning))
            }
            if let error = host.pollError {
                Text("Last poll error: \(error)").font(PressureTheme.monoCaption).foregroundStyle(.orange)
            }
            if host.liveJobs.isEmpty {
                Text(stale ? "No jobs in the last sample." : "No jobs running in the last sample.")
                    .font(PressureTheme.monoCaption).foregroundStyle(.secondary)
            } else {
                ForEach(host.liveJobs) { job in
                    liveJobRow(job)
                    if hetzner.selectedJobID == job.jobID { JobLogPanel(hetzner: hetzner) }
                }
            }
            if !host.recentRuns.isEmpty {
                DisclosureGroup("Recent runs (\(host.recentRuns.count))") {
                    ForEach(host.recentRuns) { job in
                        recentRunRow(job)
                        if hetzner.selectedJobID == job.jobID { JobLogPanel(hetzner: hetzner) }
                    }
                }
                .font(.caption)
            }
            if !host.kills.isEmpty {
                DisclosureGroup {
                    ForEach(host.kills) { kill in killRow(kill) }
                } label: {
                    Text("Stall kills (\(host.kills.count)) · latest \(PressureFormat.relative(host.kills.compactMap(\.at).max()))")
                        .font(.caption.weight(.semibold)).foregroundStyle(.red)
                }
            }
        }
    }

    private func liveVitalsLine(_ host: LiveHost) -> String? {
        var parts: [String] = []
        if let version = host.runnerVersion { parts.append("runner \(version)") }
        if let load = host.load1 { parts.append(String(format: "load %.2f", load)) }
        if let disk = host.diskFreeGB { parts.append(String(format: "disk %.0f GB free", disk)) }
        if let cache = host.cacheGB { parts.append(String(format: "cache %.0f GB", cache)) }
        return parts.isEmpty ? nil : parts.joined(separator: " · ")
    }

    private func verdictChip(_ job: LiveJob) -> some View {
        switch job.verdict {
        case "HEALTHY": chip("HEALTHY", .green)
        case "SUSPECT": chip("SUSPECT", .orange)
        case "STALLED": chip("STALLED", .red)
        default: chip(job.phase.uppercased(), .secondary)
        }
    }

    private func liveJobRow(_ job: LiveJob) -> some View {
        let open = hetzner.selectedJobID == job.jobID
        return Button { hetzner.selectLiveJob(job) } label: {
            VStack(alignment: .leading, spacing: 3) {
                HStack(spacing: 8) {
                    Text(job.repo.isEmpty ? job.jobID : job.repo).font(.subheadline.weight(.semibold))
                    if let jobClass = job.jobClass { chip(jobClass, .secondary) }
                    verdictChip(job)
                    Spacer()
                    if let elapsed = job.elapsedS {
                        Text(PressureFormat.duration(seconds: elapsed))
                            .font(PressureTheme.monoCaption).foregroundStyle(.secondary)
                    }
                    Image(systemName: open ? "chevron.down" : "chevron.right")
                        .font(.caption2).foregroundStyle(.secondary)
                }
                Text(liveJobMeta(job))
                    .font(PressureTheme.monoCaption)
                    .foregroundStyle(job.isStalled ? .red : .secondary)
                    .lineLimit(2)
            }
            .padding(.vertical, 3)
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .help(job.phase == "running" ? "Open this job's log; it refreshes while the job runs" : "Open this job's log")
    }

    private func liveJobMeta(_ job: LiveJob) -> String {
        var parts: [String] = []
        if let delivery = job.deliveryID { parts.append("delivery \(PressureFormat.shortSession(delivery))") }
        if job.phase == "queued" {
            if let wait = job.queueWaitS { parts.append("waiting \(PressureFormat.duration(seconds: wait))") }
        } else if job.phase == "running" {
            if let silent = job.lastOutputAgeS { parts.append("silent \(PressureFormat.duration(seconds: silent))") }
            if let cpu = job.cpuPct { parts.append(String(format: "cpu %.0f%%", cpu)) }
            if let rss = job.rssMB { parts.append(String(format: "rss %.0f MB", rss)) }
        }
        if let sha = job.sha { parts.append("sha \(String(sha.prefix(8)))") }
        return parts.joined(separator: " · ")
    }

    private func recentRunRow(_ job: LiveJob) -> some View {
        let open = hetzner.selectedJobID == job.jobID
        return Button { hetzner.selectLiveJob(job) } label: {
            HStack(spacing: 8) {
                Text(job.repo.isEmpty ? job.jobID : job.repo).font(PressureTheme.monoCaption)
                if let jobClass = job.jobClass { chip(jobClass, .secondary) }
                chip(job.state ?? "terminal", terminalStateColor(job.state))
                if let elapsed = job.elapsedS {
                    Text(PressureFormat.duration(seconds: elapsed)).font(PressureTheme.monoCaption).foregroundStyle(.secondary)
                }
                Spacer()
                Image(systemName: open ? "chevron.down" : "chevron.right")
                    .font(.caption2).foregroundStyle(.secondary)
                Text(PressureFormat.shortTime(job.finishedAt ?? job.updatedAt))
                    .font(PressureTheme.monoCaption).foregroundStyle(.secondary)
            }
            .padding(.vertical, 2)
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .help("Open this job's log")
    }

    private func terminalStateColor(_ state: String?) -> Color {
        switch state {
        case "passed": .green
        case "failed": .red
        case "expired": .orange
        default: .secondary
        }
    }

    private func killRow(_ kill: KillEvent) -> some View {
        VStack(alignment: .leading, spacing: 3) {
            HStack(spacing: 8) {
                chip("STALL-KILLED", .red)
                if let jobClass = kill.jobClass { chip(jobClass, .secondary) }
                if let delivery = kill.deliveryID {
                    Text("delivery \(PressureFormat.shortSession(delivery))")
                        .font(PressureTheme.monoCaption).foregroundStyle(.secondary)
                }
                Spacer()
                Text(PressureFormat.shortTime(kill.at)).font(PressureTheme.monoCaption).foregroundStyle(.secondary)
            }
            Text(killMeta(kill)).font(PressureTheme.monoCaption).foregroundStyle(.secondary)
            if let detail = kill.detail, !detail.isEmpty {
                Text(detail).font(.caption).foregroundStyle(.tertiary).lineLimit(2)
            }
        }
        .padding(.vertical, 3)
    }

    private func killMeta(_ kill: KillEvent) -> String {
        var parts: [String] = ["job \(kill.jobID)"]
        if let elapsed = kill.elapsedS { parts.append("elapsed \(PressureFormat.duration(seconds: elapsed))") }
        if let silent = kill.lastOutputAgeS { parts.append("silent \(PressureFormat.duration(seconds: silent))") }
        if let cpu = kill.cpuPct { parts.append(String(format: "cpu %.1f%%", cpu)) }
        return parts.joined(separator: " · ")
    }
}

// MARK: - Delegate status

private struct DelegateStatusCard: View {
    @ObservedObject var hetzner: HetznerStore

    var body: some View {
        SectionCard(title: "Gate delegate", systemImage: "arrow.triangle.swap", trailing: trailing) {
            VStack(alignment: .leading, spacing: 8) {
                HStack(spacing: 8) {
                    Text("Mode").font(.subheadline.weight(.semibold))
                    if let config = hetzner.delegateConfig {
                        chip(config.mode.uppercased(), modeColor(config.mode))
                        Text(configSummary(config))
                            .font(PressureTheme.monoCaption)
                            .foregroundStyle(.secondary)
                            .lineLimit(1)
                            .truncationMode(.middle)
                    } else {
                        Text("config not read").font(PressureTheme.monoCaption).foregroundStyle(.secondary)
                    }
                    Spacer()
                    Button {
                        Task { await hetzner.refreshNow() }
                    } label: {
                        Label("Refresh now", systemImage: "arrow.clockwise")
                    }
                    .controlSize(.small)
                    .disabled(hetzner.refreshing)
                }
                let stats = hetzner.delegateStats
                if stats.total > 0 {
                    Grid(alignment: .leading, horizontalSpacing: 18, verticalSpacing: 4) {
                        GridRow {
                            metric("\(stats.total)", "loaded runs")
                            metric("\(stats.remotePass)", "verdicts used")
                            metric("\(stats.fallbacks)", "fallbacks", warn: stats.fallbacks > 0)
                            metric("\(stats.shadow)", "shadow")
                            metric("\(stats.diverged)", "diverged", alert: stats.diverged > 0)
                            metric(PressureFormat.durationMS(stats.remoteRunMS), "box time")
                            metric(PressureFormat.durationMS(stats.localMS), "mac time")
                        }
                    }
                    if stats.onEraDiverged > 0 {
                        Label("\(stats.onEraDiverged) divergence(s) in loaded mode-on rows — filter Agreement: Diverged below.",
                              systemImage: "exclamationmark.triangle.fill")
                            .font(.caption.weight(.semibold)).foregroundStyle(.red)
                    } else if stats.onEraBothVerdict > 0 {
                        Label("No recorded divergence in \(stats.onEraBothVerdict) loaded mode-on comparisons.", systemImage: "checkmark.circle.fill")
                            .font(.caption).foregroundStyle(.green)
                    }
                } else if hetzner.ledgerPresent {
                    Text("No delegate runs recorded yet.").font(PressureTheme.monoCaption).foregroundStyle(.secondary)
                }
                if let error = hetzner.localError {
                    Label("Local read failed: \(HetznerUI.describeLocal(error)). Showing last good data.", systemImage: "exclamationmark.triangle.fill")
                        .font(.caption).foregroundStyle(.orange)
                }
                if hetzner.historyTruncated || hetzner.malformedRows > 0 {
                    Text("Bounded history: \(hetzner.delegateRuns.count) recent rows loaded; \(hetzner.malformedRows) malformed rows skipped. Counts describe this window.")
                        .font(.caption).foregroundStyle(.secondary)
                }
                Text("Read from ~/.nicos-dev/commit-delivery/v1/gate-delegate.json and the gate-delegate.runs stream in side-streams.sqlite (latest 1,000 runs) — display only.")
                    .font(.caption2).foregroundStyle(.tertiary)
            }
        }
    }

    private var trailing: String {
        guard let at = hetzner.ledgerCheckedAt else { return "not read" }
        return "ledger checked " + PressureFormat.relative(min(at, Date()))
    }

    private func metric(_ value: String, _ label: String, warn: Bool = false, alert: Bool = false) -> some View {
        VStack(alignment: .leading, spacing: 0) {
            Text(value)
                .font(.system(.callout, design: .monospaced).weight(.semibold))
                .foregroundStyle(alert ? .red : warn ? .orange : .primary)
            Text(label).font(.caption2).foregroundStyle(.secondary)
        }
    }

    private func modeColor(_ mode: String) -> Color {
        switch mode {
        case "on": .green
        case "shadow": .orange
        default: .secondary
        }
    }

    private func configSummary(_ config: DelegateConfig) -> String {
        let repos = config.repos.map(\.box).joined(separator: ", ")
        return "repos: \(repos.isEmpty ? "none" : repos) · \(config.allowCount) portable prefixes"
    }
}

// MARK: - Delegate runs

private struct RunFilter: Equatable {
    var repo = "All"
    var decision = "All"
    var agreement = "All"
    var range = "All"
}

private struct DelegateRunsCard: View {
    @ObservedObject var hetzner: HetznerStore
    @State private var filter = RunFilter()
    @State private var expanded: Set<String> = []
    @State private var visibleLimit = 100
    @State private var filtered: [DelegateRun] = []
    @State private var repoOptions: [String] = ["All"]

    private struct CacheKey: Equatable {
        let revision: Int
        let filter: RunFilter
    }

    var body: some View {
        SectionCard(title: "Gate-delegate runs", systemImage: "list.bullet.rectangle",
                    trailing: "\(filtered.count) of \(hetzner.delegateRuns.count)") {
            VStack(alignment: .leading, spacing: 8) {
                HStack(spacing: 12) {
                    HetznerUI.filterLabel("Repo")
                    Picker("", selection: $filter.repo) {
                        ForEach(repoOptions, id: \.self) { Text($0).tag($0) }
                    }
                    .pickerStyle(.menu).labelsHidden().fixedSize()
                    HetznerUI.filterLabel("Decision")
                    Picker("", selection: $filter.decision) {
                        ForEach(["All", "Substituted", "Fallback", "Shadow"], id: \.self) { Text($0).tag($0) }
                    }
                    .pickerStyle(.segmented).labelsHidden()
                }
                HStack(spacing: 12) {
                    HetznerUI.filterLabel("Agreement")
                    Picker("", selection: $filter.agreement) {
                        ForEach(["All", "Agree", "Diverged"], id: \.self) { Text($0).tag($0) }
                    }
                    .pickerStyle(.segmented).labelsHidden()
                    HetznerUI.filterLabel("Range")
                    Picker("", selection: $filter.range) {
                        ForEach(["All", "24h", "1h"], id: \.self) { Text($0).tag($0) }
                    }
                    .pickerStyle(.segmented).labelsHidden()
                    if filter != RunFilter() {
                        Button("Clear") { filter = RunFilter() }.controlSize(.small)
                    }
                }
            }
            .padding(.bottom, 4)

            if !hetzner.ledgerPresent {
                Text("The delegate ledger is not available at its expected path.").foregroundStyle(.secondary)
            } else if hetzner.delegateRuns.isEmpty {
                Text("No valid delegate runs were loaded from this ledger.").foregroundStyle(.secondary)
            } else if filtered.isEmpty {
                Text("No runs match the current filters.").foregroundStyle(.secondary)
            } else {
                LazyVStack(spacing: 0) {
                    ForEach(filtered.prefix(visibleLimit)) { run in
                        DelegateRunRow(run: run, expanded: expanded.contains(run.id)) {
                            if expanded.contains(run.id) { expanded.remove(run.id) } else { expanded.insert(run.id) }
                        }
                        .equatable()
                        Divider().opacity(0.4)
                    }
                }
                if filtered.count > visibleLimit {
                    Button("Show more runs (\(visibleLimit) of \(filtered.count) shown)") { visibleLimit += 100 }
                        .padding(.top, 6)
                }
            }
        }
        .onChange(of: CacheKey(revision: hetzner.runsRevision, filter: filter), initial: true) { _, key in
            recompute(key.filter)
        }
    }

    private func recompute(_ f: RunFilter) {
        let runs = hetzner.delegateRuns
        repoOptions = ["All"] + Array(Set(runs.map(\.repoName))).sorted()
        let cutoff: Date? = switch f.range {
        case "1h": Date().addingTimeInterval(-3600)
        case "24h": Date().addingTimeInterval(-86400)
        default: nil
        }
        filtered = runs.filter { run in
            if f.repo != "All", run.repoName != f.repo { return false }
            switch f.decision {
            case "Substituted": if run.decision != "remote_pass" { return false }
            case "Fallback": if run.decision != "local_fallback" { return false }
            case "Shadow": if run.decision != "shadow" { return false }
            default: break
            }
            switch f.agreement {
            case "Agree": if run.agreement != true { return false }
            case "Diverged": if run.agreement != false { return false }
            default: break
            }
            if let cutoff, let at = run.at, at < cutoff { return false }
            return true
        }
    }
}

private struct DelegateRunRow: View, Equatable {
    let run: DelegateRun
    let expanded: Bool
    let toggle: () -> Void

    nonisolated static func == (a: Self, b: Self) -> Bool { a.run.id == b.run.id && a.expanded == b.expanded }

    var body: some View {
        VStack(alignment: .leading, spacing: 3) {
            Button(action: toggle) {
                VStack(alignment: .leading, spacing: 3) {
                    HStack(spacing: 8) {
                        Text(run.repoName).font(.subheadline.weight(.semibold))
                        chip(run.mode, run.mode == "on" ? .green : .secondary)
                        decisionChip
                        if let agreement = run.agreement {
                            chip(agreement ? "agree" : "DIVERGED", agreement ? .secondary : .red)
                            if !agreement, run.localEnvFailure { chip("local environment hint", .orange) }
                        }
                        Spacer()
                        Image(systemName: expanded ? "chevron.down" : "chevron.right")
                            .font(.caption2).foregroundStyle(.secondary)
                        Text(PressureFormat.shortTime(run.at)).font(PressureTheme.monoCaption).foregroundStyle(.secondary)
                    }
                    Text(detail).font(PressureTheme.monoCaption).foregroundStyle(.secondary).lineLimit(2)
                    Text(run.command).font(.caption).foregroundStyle(.tertiary).lineLimit(1).truncationMode(.middle)
                }
                .contentShape(Rectangle())
            }
            .buttonStyle(.plain)
            if expanded { expandedDetail }
        }
        .padding(.vertical, 6)
    }

    @ViewBuilder private var decisionChip: some View {
        switch run.decision {
        case "remote_pass": chip("VERDICT USED", .green)
        case "local_fallback": chip("FALLBACK · local kept", .orange)
        case "shadow": chip("shadow compare", .secondary)
        default: chip(run.decision.isEmpty ? "unknown decision" : run.decision, .secondary)
        }
    }

    private var expandedDetail: some View {
        VStack(alignment: .leading, spacing: 4) {
            detailRow("Delivery", run.deliveryID, copyable: true)
            detailRow("Command", run.command, copyable: true)
            if !run.repoPath.isEmpty { detailRow("Repo path", run.repoPath) }
            if let detail = run.localDetail, !detail.isEmpty { detailRow("Mac detail", detail) }
            if run.remoteRan {
                let parts = [run.remoteStage.map { "stage \($0)" }, run.remoteExit.map { "exit \($0)" }].compactMap { $0 }
                if !parts.isEmpty { detailRow("Box run", parts.joined(separator: " · ")) }
            }
            if let verified = run.treeVerified {
                detailRow("Tree", (verified ? "verified" : "NOT verified") + (run.treeHash.map { " · " + String($0.prefix(12)) } ?? ""))
            }
            if let digest = run.policyDigest { detailRow("Policy", String(digest.prefix(24))) }
            if let allowed = run.remotePassAllowed {
                detailRow("Remote pass", allowed ? "allowed for this check" : "not allowed for this check")
            }
        }
        .padding(.leading, 12)
        .padding(.top, 2)
    }

    private func detailRow(_ label: String, _ value: String, copyable: Bool = false) -> some View {
        HStack(alignment: .top, spacing: 8) {
            Text(label).font(.caption.weight(.semibold)).foregroundStyle(.secondary).frame(width: 86, alignment: .leading)
            Text(value).font(PressureTheme.monoCaption).textSelection(.enabled)
            if copyable {
                Button { HetznerUI.copy(value) } label: { Image(systemName: "doc.on.doc") }
                    .buttonStyle(.plain).foregroundStyle(.secondary).help("Copy")
            }
            Spacer()
        }
    }

    private var detail: String {
        var parts: [String] = ["delivery \(PressureFormat.shortSession(run.deliveryID))"]
        if let passed = run.localPassed {
            parts.append("mac \(passed ? "pass" : "FAIL")" + (run.localMS.map { " \(PressureFormat.durationMS($0))" } ?? ""))
        }
        if run.remoteRan {
            var remote = "box"
            if let passed = run.remotePassed { remote += " \(passed ? "pass" : "FAIL")" }
            if let ms = run.remoteRunMS { remote += " \(PressureFormat.durationMS(ms))" }
            if let wall = run.remoteWallMS { remote += " (wall \(PressureFormat.durationMS(wall)))" }
            parts.append(remote)
        } else {
            parts.append("box did not run")
        }
        if let verified = run.treeVerified { parts.append(verified ? "tree verified" : "tree NOT verified") }
        if let allowed = run.remotePassAllowed { parts.append(allowed ? "remote-pass allowed" : "remote-pass not allowed") }
        return parts.joined(separator: " · ")
    }
}

// MARK: - Box jobs

private struct JobFilter: Equatable {
    var repo = "All"
    var caller = "All"
    var exit = "All"
    var sort = "Newest"
    var search = ""
}

private struct BoxJobsCard: View {
    @ObservedObject var hetzner: HetznerStore
    @State private var filter = JobFilter()
    @State private var searchText = ""
    @State private var visibleLimit = 120
    @State private var filtered: [BoxJob] = []
    @State private var repoOptions: [String] = ["All"]
    @State private var callerOptions: [String] = ["All"]

    private struct CacheKey: Equatable {
        let revision: Int
        let filter: JobFilter
    }

    var body: some View {
        SectionCard(title: "Job receipts", systemImage: "terminal", trailing: trailing) {
            VStack(alignment: .leading, spacing: 8) {
                HStack(spacing: 12) {
                    HetznerUI.filterLabel("Runner")
                    if let fleet = hetzner.fleet {
                        Picker("", selection: Binding(
                            get: { hetzner.selectedHost ?? fleet.hosts.first?.name ?? "" },
                            set: { host in Task { await hetzner.selectHost(host) } }
                        )) {
                            ForEach(fleet.hosts) { host in Text("\(host.name) · \(host.roleLabel)").tag(host.name) }
                        }
                        .pickerStyle(.menu).labelsHidden().fixedSize()
                        .help("The runner whose receipts and logs are read. Placement stays governed by the fleet inventory.")
                    } else {
                        Text(hetzner.selectedHost ?? "default runner").font(PressureTheme.monoCaption)
                    }
                    HetznerUI.filterLabel("Repo")
                    Picker("", selection: $filter.repo) {
                        ForEach(repoOptions, id: \.self) { Text($0).tag($0) }
                    }
                    .pickerStyle(.menu).labelsHidden().fixedSize()
                    HetznerUI.filterLabel("Caller")
                    Picker("", selection: $filter.caller) {
                        ForEach(callerOptions, id: \.self) { Text($0).tag($0) }
                    }
                    .pickerStyle(.menu).labelsHidden().fixedSize()
                }
                HStack(spacing: 12) {
                    HetznerUI.filterLabel("Exit")
                    Picker("", selection: $filter.exit) {
                        ForEach(["All", "OK", "Failed", "Setup"], id: \.self) { Text($0).tag($0) }
                    }
                    .pickerStyle(.segmented).labelsHidden()
                    HetznerUI.filterLabel("Sort")
                    Picker("", selection: $filter.sort) {
                        ForEach(["Newest", "Longest"], id: \.self) { Text($0).tag($0) }
                    }
                    .pickerStyle(.segmented).labelsHidden()
                }
                HStack(spacing: 6) {
                    Image(systemName: "magnifyingglass").foregroundStyle(.secondary)
                    TextField("Search command, repo, caller, job id", text: $searchText)
                        .textFieldStyle(.roundedBorder)
                    if filter != JobFilter() || !searchText.isEmpty {
                        Button("Clear") { filter = JobFilter(); searchText = "" }.controlSize(.small)
                    }
                }
                if let error = hetzner.boxError {
                    Label("\(HetznerUI.describeBox(error, failure: hetzner.boxFailure)). Showing the last good receipts.", systemImage: "exclamationmark.triangle.fill")
                        .font(.caption).foregroundStyle(.orange)
                }
                if let health = hetzner.boxHealth {
                    Text(healthLine(health)).font(PressureTheme.monoCaption).foregroundStyle(.secondary)
                }
            }
            .padding(.bottom, 4)

            if hetzner.jobs.isEmpty {
                if hetzner.boxFetchedAt == nil && hetzner.boxError == nil {
                    HStack(spacing: 8) {
                        ProgressView().controlSize(.small)
                        Text("Reading receipts from the runner…").foregroundStyle(.secondary)
                    }
                } else {
                    Text(hetzner.boxReachable ? "No receipts in the fetched window." : "No receipts loaded — the runner was not reached.")
                        .foregroundStyle(.secondary)
                }
            } else if filtered.isEmpty {
                Text("No jobs match the current filters.").foregroundStyle(.secondary)
            } else {
                LazyVStack(spacing: 0) {
                    ForEach(filtered.prefix(visibleLimit)) { job in
                        let selected = hetzner.selectedJobID == job.id
                        BoxJobRow(job: job, selected: selected) { hetzner.selectJob(job) }
                            .equatable()
                        if selected {
                            JobLogPanel(hetzner: hetzner)
                        }
                        Divider().opacity(0.4)
                    }
                }
                if filtered.count > visibleLimit {
                    Button("Show more jobs (\(visibleLimit) of \(filtered.count) shown)") { visibleLimit += 120 }
                        .padding(.top, 6)
                }
            }
            Text("Source: /srv/build/logs/jobs.log on \(hetzner.selectedHost ?? "the selected runner") via SSH (latest 1,000 receipts, \(receiptSpan)), about once a minute while visible; unchanged receipts are not re-sent. Long commands are cut in the list; select a job to open its log and full command.")
                .font(.caption2).foregroundStyle(.tertiary).padding(.top, 6)
        }
        .onChange(of: CacheKey(revision: hetzner.jobsRevision, filter: filter), initial: true) { _, key in
            recompute(key.filter)
        }
        // Debounce typing so each keystroke does not re-filter 1,000 rows.
        .task(id: searchText) {
            try? await Task.sleep(for: .milliseconds(180))
            if !Task.isCancelled { filter.search = searchText }
        }
    }

    /// How far back the loaded receipts reach, so a short window is visible.
    private var receiptSpan: String {
        guard let oldest = hetzner.jobs.compactMap(\.at).min() else { return "no receipts loaded" }
        return "back to " + PressureFormat.relative(oldest)
    }

    private var trailing: String {
        let base = "\(filtered.count) of \(hetzner.jobs.count)"
        guard let at = hetzner.boxFetchedAt else { return base }
        return base + " · " + (hetzner.boxReachable ? "read " : "last read ") + PressureFormat.relative(min(at, Date()))
    }

    private func healthLine(_ health: BoxHealth) -> String {
        var parts: [String] = []
        if let l1 = health.load1, let l5 = health.load5, let l15 = health.load15 {
            parts.append(String(format: "load %.2f / %.2f / %.2f", l1, l5, l15))
        }
        if let avail = health.memAvailableMB, let total = health.memTotalMB {
            parts.append("mem \(avail) of \(total) MB available")
        }
        if let disk = health.diskUsedPercent { parts.append("disk \(disk)% used") }
        if let uptime = health.uptime { parts.append("up \(uptime)") }
        return (hetzner.selectedHost ?? "Runner") + " now — " + parts.joined(separator: " · ")
    }

    private func recompute(_ f: JobFilter) {
        let jobs = hetzner.jobs
        repoOptions = ["All"] + Array(Set(jobs.map(\.repo))).sorted()
        callerOptions = ["All"] + Array(Set(jobs.map(\.caller))).sorted()
        let needle = f.search.lowercased()
        var list = jobs.filter { job in
            if f.repo != "All", job.repo != f.repo { return false }
            if f.caller != "All", job.caller != f.caller { return false }
            switch f.exit {
            case "OK": if !job.isSuccess { return false }
            case "Failed": if job.isSuccess || job.isSetupFailure { return false }
            case "Setup": if !job.isSetupFailure { return false }
            default: break
            }
            if !needle.isEmpty {
                return job.repo.localizedCaseInsensitiveContains(needle)
                    || job.caller.localizedCaseInsensitiveContains(needle)
                    || job.command.localizedCaseInsensitiveContains(needle)
                    || job.id.localizedCaseInsensitiveContains(needle)
            }
            return true
        }
        if f.sort == "Longest" {
            list.sort { ($0.durationSeconds ?? -1) > ($1.durationSeconds ?? -1) }
        }
        filtered = list
    }
}

private struct BoxJobRow: View, Equatable {
    let job: BoxJob
    let selected: Bool
    let select: () -> Void

    nonisolated static func == (a: Self, b: Self) -> Bool { a.job.id == b.job.id && a.selected == b.selected }

    var body: some View {
        Button(action: select) {
            VStack(alignment: .leading, spacing: 3) {
                HStack(spacing: 8) {
                    Text(job.repo).font(.subheadline.weight(.semibold))
                    if let jobClass = job.jobClass { chip(jobClass, .secondary) }
                    chip(job.isSetupFailure ? "SETUP" : "exit \(job.exitCode)", exitColor)
                    if let dur = job.durationSeconds {
                        Text(PressureFormat.duration(seconds: Int64(dur))).font(PressureTheme.monoCaption).foregroundStyle(.secondary)
                    }
                    Spacer()
                    Image(systemName: selected ? "chevron.down" : "chevron.right").font(.caption2).foregroundStyle(.secondary)
                    Text(PressureFormat.shortTime(job.at)).font(PressureTheme.monoCaption).foregroundStyle(.secondary)
                }
                Text(meta).font(PressureTheme.monoCaption).foregroundStyle(.secondary).lineLimit(1).truncationMode(.middle)
                if !job.command.isEmpty {
                    Text(job.command + (job.commandTruncated == true ? " …" : ""))
                        .font(.caption).foregroundStyle(.tertiary).lineLimit(1).truncationMode(.middle)
                }
            }
            .padding(.vertical, 6)
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
    }

    private var exitColor: Color {
        if job.isSuccess { return .green }
        if job.isSetupFailure { return .orange }
        return .red
    }

    private var meta: String {
        var line = "\(job.caller) · job \(job.id)"
        if let cand = job.cand { line += " · cand \(String(cand.prefix(8)))" }
        if let sha = job.sha { line += " · sha \(String(sha.prefix(8)))" }
        return line
    }
}

/// Inline log for the open job: a receipt, or a live or recent scheduler
/// job. Large logs render in a native text view so a 200 KB log does not lay
/// out as one SwiftUI Text.
private struct JobLogPanel: View {
    @ObservedObject var hetzner: HetznerStore

    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            if hetzner.jobLogLoading {
                HStack(spacing: 8) {
                    ProgressView().controlSize(.small)
                    Text("Fetching log from \(hetzner.selectedHost ?? "the runner")…").foregroundStyle(.secondary)
                }
            } else if let log = hetzner.jobLog {
                if let command = log.command, !command.isEmpty {
                    HStack(alignment: .top, spacing: 8) {
                        Text("Command").font(.caption.weight(.semibold)).foregroundStyle(.secondary)
                        Text(command)
                            .font(PressureTheme.monoCaption)
                            .textSelection(.enabled)
                            .lineLimit(6)
                            .truncationMode(.middle)
                        Spacer()
                        Button { HetznerUI.copy(command) } label: { Image(systemName: "doc.on.doc") }
                            .buttonStyle(.plain).foregroundStyle(.secondary).help("Copy the full command")
                    }
                }
                HStack(spacing: 8) {
                    Text(header(log)).font(.caption).foregroundStyle(.secondary)
                    if hetzner.jobLogFollowing { chip("FOLLOWING", .green) }
                    Spacer()
                    Button { hetzner.reloadLog() } label: { Label("Reload", systemImage: "arrow.clockwise") }
                        .controlSize(.small)
                    Button { HetznerUI.copy(log.log) } label: { Label("Copy", systemImage: "doc.on.doc") }
                        .controlSize(.small)
                }
                LogTextView(text: log.log, followTail: hetzner.jobLogFollowing || log.omittedBytes > 0)
                    .frame(height: 280)
                    .clipShape(RoundedRectangle(cornerRadius: 8, style: .continuous))
                    .overlay(RoundedRectangle(cornerRadius: 8, style: .continuous).stroke(PressureTheme.hairline))
            } else if let error = hetzner.jobLogError {
                Label("Log read failed: \(error)", systemImage: "exclamationmark.triangle")
                    .foregroundStyle(.orange)
            }
        }
        .padding(.leading, 12)
        .padding(.bottom, 8)
    }

    private func header(_ log: GateRunnerJobLog) -> String {
        let size = PressureFormat.bytes(log.bytes)
        var text = log.omittedBytes > 0
            ? "First 40 KB and last 160 KB of \(size) (\(PressureFormat.bytes(log.omittedBytes)) omitted)"
            : log.bytes == 0 ? "No output yet" : "Log · \(size)"
        if hetzner.jobLogFollowing {
            text += " · refreshes every 15 s while the job runs"
        } else if let at = hetzner.jobLogFetchedAt {
            text += " · read " + PressureFormat.relative(at)
        }
        return text
    }
}

private struct LogTextView: NSViewRepresentable {
    let text: String
    /// Scroll to the end on new text: the latest output of a running job,
    /// or the tail of a long log, is where a failure shows.
    var followTail = false

    func makeNSView(context: Context) -> NSScrollView {
        let scroll = NSTextView.scrollableTextView()
        if let view = scroll.documentView as? NSTextView {
            view.isEditable = false
            view.isSelectable = true
            view.font = .monospacedSystemFont(ofSize: NSFont.smallSystemFontSize, weight: .regular)
            view.textContainerInset = NSSize(width: 6, height: 6)
            view.drawsBackground = false
        }
        scroll.drawsBackground = false
        return scroll
    }

    func updateNSView(_ scroll: NSScrollView, context: Context) {
        guard let view = scroll.documentView as? NSTextView, view.string != text else { return }
        view.string = text
        view.textColor = .labelColor
        if followTail { view.scrollToEndOfDocument(nil) }
    }
}
