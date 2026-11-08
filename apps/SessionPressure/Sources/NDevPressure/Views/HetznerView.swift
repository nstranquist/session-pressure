import SwiftUI
import AppKit
import NDevPressureCore

/// Hetzner gate-runner tab: is the delegate offload on, is the box taking
/// runs, are the verdicts agreeing — plus the full delegate ledger and the
/// box's job receipts, both filterable, with per-run detail and per-job
/// logs. Read-only: the delegate config is displayed, never written, and
/// running or killing jobs stays in the existing scripts.
struct HetznerView: View {
    @ObservedObject var hetzner: HetznerStore

    // Delegate-run filters (view state, so data refreshes never reset them)
    @State private var runRepo = "All"
    @State private var runDecision = "All"
    @State private var runAgreement = "All"
    @State private var runRange = "All"
    @State private var expandedRuns: Set<String> = []
    @State private var visibleRunLimit = 100

    // Box-job filters
    @State private var jobRepo = "All"
    @State private var jobCaller = "All"
    @State private var jobExit = "All"
    @State private var jobSort = "Newest"
    @State private var jobSearch = ""
    @State private var visibleJobLimit = 120

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 16) {
                statusCard
                delegateCard
                jobsCard
                if hetzner.selectedJobID != nil {
                    jobLogCard
                }
            }
            .padding(16)
        }

    }

    // MARK: Status

    private var statusCard: some View {
        SectionCard(
            title: "Hetzner offload · gaterunner-1",
            systemImage: "server.rack",
            trailing: boxStatusTrailing
        ) {
            VStack(alignment: .leading, spacing: 8) {
                HStack(spacing: 8) {
                    Text("Delegate mode")
                        .font(.subheadline.weight(.semibold))
                    if let config = hetzner.delegateConfig {
                        chip(config.mode.uppercased(), color: modeColor(config.mode))
                        Text(configSummary(config))
                            .font(PressureTheme.monoCaption)
                            .foregroundStyle(.secondary)
                            .lineLimit(1)
                            .truncationMode(.middle)
                    } else {
                        Text("config not read")
                            .font(PressureTheme.monoCaption)
                            .foregroundStyle(.secondary)
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
                    Text("Loaded runs: \(stats.total) · \(stats.shadow) shadow · \(stats.remotePass) substituted · \(stats.fallbacks) fallbacks")
                        .font(PressureTheme.monoCaption)
                        .foregroundStyle(.secondary)
                    Text("Loaded rows with mode on: \(stats.onEraTotal) runs · \(stats.onEraRemotePass) substituted · \(stats.onEraFallbacks) fallbacks · box time \(PressureFormat.durationMS(stats.onEraRemoteRunMS))")
                        .font(PressureTheme.monoCaption)
                        .foregroundStyle(.secondary)
                    HStack(spacing: 8) {
                        Text("Where both sides ran: \(stats.bothVerdict) · agree \(stats.agree) · diverged \(stats.diverged)")
                            .font(PressureTheme.monoCaption)
                            .foregroundStyle(.secondary)
                        if stats.diverged > 0 {
                            Button("Show diverged") { showDiverged() }
                                .controlSize(.small)
                        }
                    }
                    Text("Recorded check time — box \(PressureFormat.durationMS(stats.remoteRunMS)) · mac \(PressureFormat.durationMS(stats.localMS))")
                        .font(PressureTheme.monoCaption)
                        .foregroundStyle(.secondary)
                    if stats.onEraDiverged > 0 {
                        Label("\(stats.onEraDiverged) divergence(s) in loaded mode-on rows — set the runs filter to Agreement: Diverged to inspect.",
                              systemImage: "exclamationmark.triangle.fill")
                            .font(.caption.weight(.semibold))
                            .foregroundStyle(.red)
                    } else if stats.onEraBothVerdict > 0 {
                        Label("No recorded divergence in loaded mode-on comparisons.", systemImage: "checkmark.circle.fill")
                            .font(.caption)
                            .foregroundStyle(.green)
                    }
                } else if hetzner.ledgerPresent {
                    Text("No delegate runs recorded yet.")
                        .font(PressureTheme.monoCaption)
                        .foregroundStyle(.secondary)
                }

                if let error = hetzner.localError {
                    Label("Local read failed: \(error). Showing last good data.", systemImage: "exclamationmark.triangle.fill")
                        .font(.caption).foregroundStyle(.orange)
                }
                if let error = hetzner.boxError {
                    Label("Box read failed: \(error).", systemImage: "exclamationmark.triangle.fill")
                        .font(.caption).foregroundStyle(.orange)
                }
                if hetzner.historyTruncated || hetzner.malformedRows > 0 {
                    Text("Bounded history: \(hetzner.delegateRuns.count) recent rows loaded; \(hetzner.malformedRows) malformed rows skipped. Counts describe this window.")
                        .font(.caption).foregroundStyle(.secondary)
                }
                if let health = hetzner.boxHealth {
                    Text(boxHealthLine(health))
                        .font(PressureTheme.monoCaption)
                        .foregroundStyle(.secondary)
                }
                HStack(spacing: 12) {
                    Text("Local read:")
                    if let date = hetzner.ledgerCheckedAt { ReadAgeView(date: date) } else { Text("not read") }
                    Text("Box read:")
                    if let date = hetzner.boxFetchedAt { ReadAgeView(date: date) } else { Text("not read") }
                }
                Text("Recent history only: up to 1000 delegate rows and 1000 box jobs, subject to size limits.")
                    .font(.caption2).foregroundStyle(.tertiary)
                if !hetzner.boxReachable, hetzner.boxFetchedAt != nil {
                    Label("Box unreachable — showing last known data.", systemImage: "exclamationmark.triangle.fill")
                        .font(.caption)
                        .foregroundStyle(PressureTheme.levelColor(.warning))
                } else if !hetzner.boxReachable {
                    Label("Box not reached yet — first fetch may take a few seconds.", systemImage: "hourglass")
                        .font(.caption)
                        .foregroundStyle(.secondary)
                }
                Text("Mode and repos are read from ~/.nicos-dev/commit-delivery/v1/gate-delegate.json — display only; this tab never changes worker config.")
                    .font(.caption2)
                    .foregroundStyle(.tertiary)
            }
        }
    }

    private var boxStatusTrailing: String {
        if let at = hetzner.boxFetchedAt {
            return (hetzner.boxReachable ? "reachable · " : "last fetched ") + PressureFormat.relative(at)
        }
        return "not fetched yet"
    }

    private var freshnessLine: String {
        let ledger = hetzner.ledgerPresent
            ? "ledger \(hetzner.delegateRuns.count) rows · checked \(PressureFormat.relative(hetzner.ledgerCheckedAt))"
            : "ledger not found · checked \(PressureFormat.relative(hetzner.ledgerCheckedAt))"
        return ledger + " · box fetched \(PressureFormat.relative(hetzner.boxFetchedAt))"
    }

    private func modeColor(_ mode: String) -> Color {
        switch mode {
        case "on": return .green
        case "shadow": return .orange
        default: return .secondary
        }
    }

    private func configSummary(_ config: DelegateConfig) -> String {
        let repos = config.repos.map(\.box).joined(separator: ", ")
        return "repos: \(repos.isEmpty ? "none" : repos) · \(config.allowCount) portable prefixes"
    }

    private func boxHealthLine(_ health: BoxHealth) -> String {
        var parts: [String] = []
        if let l1 = health.load1, let l5 = health.load5, let l15 = health.load15 {
            parts.append(String(format: "load %.2f / %.2f / %.2f", l1, l5, l15))
        }
        if let avail = health.memAvailableMB, let total = health.memTotalMB {
            parts.append("mem \(avail) of \(total) MB available")
        }
        if let disk = health.diskUsedPercent {
            parts.append("disk \(disk)% used")
        }
        if let uptime = health.uptime {
            parts.append("up \(uptime)")
        }
        return parts.isEmpty ? "" : "Box — " + parts.joined(separator: " · ")
    }

    private func showDiverged() {
        runRepo = "All"
        runDecision = "All"
        runAgreement = "Diverged"
        runRange = "All"
    }

    // MARK: Delegate runs

    private var runRepoOptions: [String] {
        ["All"] + Array(Set(hetzner.delegateRuns.map(\.repoName))).sorted()
    }

    private var filteredRuns: [DelegateRun] {
        let cutoff: Date?
        switch runRange {
        case "1h": cutoff = Date().addingTimeInterval(-3600)
        case "24h": cutoff = Date().addingTimeInterval(-86400)
        default: cutoff = nil
        }
        return hetzner.delegateRuns.filter { run in
            if runRepo != "All", run.repoName != runRepo { return false }
            switch runDecision {
            case "Substituted": if run.decision != "remote_pass" { return false }
            case "Fallback": if run.decision != "local_fallback" { return false }
            case "Shadow": if run.decision != "shadow" { return false }
            default: break
            }
            switch runAgreement {
            case "Agree": if run.agreement != true { return false }
            case "Diverged": if run.agreement != false { return false }
            default: break
            }
            if let cutoff, let at = run.at, at < cutoff { return false }
            return true
        }
    }

    private var delegateCard: some View {
        SectionCard(
            title: "Gate-delegate runs",
            systemImage: "arrow.triangle.swap",
            trailing: "\(filteredRuns.count) of \(hetzner.delegateRuns.count)"
        ) {
            VStack(alignment: .leading, spacing: 8) {
                HStack(spacing: 12) {
                    filterLabel("Repo")
                    Picker("", selection: $runRepo) {
                        ForEach(runRepoOptions, id: \.self) { Text($0).tag($0) }
                    }
                    .pickerStyle(.menu)
                    .labelsHidden()
                    .fixedSize()
                    filterLabel("Decision")
                    Picker("", selection: $runDecision) {
                        ForEach(["All", "Substituted", "Fallback", "Shadow"], id: \.self) { Text($0).tag($0) }
                    }
                    .pickerStyle(.segmented)
                    .labelsHidden()
                }
                HStack(spacing: 12) {
                    filterLabel("Agreement")
                    Picker("", selection: $runAgreement) {
                        ForEach(["All", "Agree", "Diverged"], id: \.self) { Text($0).tag($0) }
                    }
                    .pickerStyle(.segmented)
                    .labelsHidden()
                    filterLabel("Range")
                    Picker("", selection: $runRange) {
                        ForEach(["All", "24h", "1h"], id: \.self) { Text($0).tag($0) }
                    }
                    .pickerStyle(.segmented)
                    .labelsHidden()
                }
            }
            .padding(.bottom, 4)

            if !hetzner.ledgerPresent {
                Text("The delegate ledger is not available at its expected path.")
                    .foregroundStyle(.secondary)
            } else if hetzner.delegateRuns.isEmpty {
                Text("No valid delegate runs were loaded from this ledger.")
                    .foregroundStyle(.secondary)
            } else if filteredRuns.isEmpty {
                Text("No runs match the current filters.")
                    .foregroundStyle(.secondary)
            } else {
                LazyVStack(spacing: 0) {
                    ForEach(filteredRuns.prefix(visibleRunLimit)) { run in
                        delegateRow(run)
                        Divider().opacity(0.4)
                    }
                }
                if filteredRuns.count > visibleRunLimit {
                    Button("Show more runs (\(visibleRunLimit) of \(filteredRuns.count) shown)") {
                        visibleRunLimit += 100
                    }
                    .padding(.top, 6)
                }
            }
            Text("Source: ~/.nicos-dev/commit-delivery/v1/gate-delegate/runs.jsonl — re-checked every 15 seconds while this pane is visible and active. Select a run for full detail.")
                .font(.caption2)
                .foregroundStyle(.tertiary)
                .padding(.top, 6)
        }
    }

    private func delegateRow(_ run: DelegateRun) -> some View {
        let expanded = expandedRuns.contains(run.id)
        return VStack(alignment: .leading, spacing: 3) {
            Button {
                if expanded { expandedRuns.remove(run.id) } else { expandedRuns.insert(run.id) }
            } label: {
                VStack(alignment: .leading, spacing: 3) {
                    HStack(spacing: 8) {
                        Text(run.repoName)
                            .font(.subheadline.weight(.semibold))
                        chip(run.mode, color: run.mode == "on" ? .green : .secondary)
                        decisionChip(run)
                        if let agreement = run.agreement {
                            chip(agreement ? "agree" : "DIVERGED", color: agreement ? .secondary : .red)
                            if !agreement, run.localEnvFailure {
                                chip("local environment hint", color: .orange)
                            }
                        }
                        Spacer()
                        Image(systemName: expanded ? "chevron.down" : "chevron.right")
                            .font(.caption2)
                            .foregroundStyle(.secondary)
                        Text(PressureFormat.shortTime(run.at))
                            .font(PressureTheme.monoCaption)
                            .foregroundStyle(.secondary)
                    }
                    Text(delegateDetail(run))
                        .font(PressureTheme.monoCaption)
                        .foregroundStyle(.secondary)
                        .lineLimit(2)
                    Text(run.command)
                        .font(.caption)
                        .foregroundStyle(.tertiary)
                        .lineLimit(1)
                        .truncationMode(.middle)
                }
                .contentShape(Rectangle())
            }
            .buttonStyle(.plain)
            if expanded {
                delegateExpanded(run)
            }
        }
        .padding(.vertical, 6)
    }

    @ViewBuilder
    private func decisionChip(_ run: DelegateRun) -> some View {
        switch run.decision {
        case "remote_pass":
            chip("VERDICT USED", color: .green)
        case "local_fallback":
            chip("FALLBACK · local kept", color: .orange)
        case "shadow":
            chip("shadow compare", color: .secondary)
        default:
            chip(run.decision.isEmpty ? "unknown decision" : run.decision, color: .secondary)
        }
    }

    private func delegateExpanded(_ run: DelegateRun) -> some View {
        VStack(alignment: .leading, spacing: 4) {
            detailRow("Delivery", run.deliveryID, copyable: true)
            detailRow("Command", run.command, copyable: true)
            if !run.repoPath.isEmpty {
                detailRow("Repo path", run.repoPath)
            }
            if let detail = run.localDetail, !detail.isEmpty {
                detailRow("Mac detail", detail)
            }
            if let boxSummary = boxRunSummary(run) {
                detailRow("Box run", boxSummary)
            }
            if let treeSummary = treeSummary(run) {
                detailRow("Tree", treeSummary)
            }
            if let digest = run.policyDigest {
                detailRow("Policy", String(digest.prefix(24)))
            }
            if let allowed = run.remotePassAllowed {
                detailRow("Remote pass", allowed ? "allowed for this check" : "not allowed for this check")
            }
        }
        .padding(.leading, 12)
        .padding(.top, 2)
    }

    private func detailRow(_ label: String, _ value: String, copyable: Bool = false) -> some View {
        HStack(alignment: .top, spacing: 8) {
            Text(label)
                .font(.caption.weight(.semibold))
                .foregroundStyle(.secondary)
                .frame(width: 86, alignment: .leading)
            Text(value)
                .font(PressureTheme.monoCaption)
                .textSelection(.enabled)
            if copyable {
                Button {
                    copyText(value)
                } label: {
                    Image(systemName: "doc.on.doc")
                }
                .buttonStyle(.plain)
                .foregroundStyle(.secondary)
                .help("Copy")
            }
            Spacer()
        }
    }

    private func treeSummary(_ run: DelegateRun) -> String? {
        guard let verified = run.treeVerified else { return nil }
        var text = verified ? "verified" : "NOT verified"
        if let hash = run.treeHash { text += " · " + String(hash.prefix(12)) }
        return text
    }

    private func boxRunSummary(_ run: DelegateRun) -> String? {
        guard run.remoteRan else { return nil }
        var parts: [String] = []
        if let stage = run.remoteStage { parts.append("stage \(stage)") }
        if let exit = run.remoteExit { parts.append("exit \(exit)") }
        return parts.isEmpty ? nil : parts.joined(separator: " · ")
    }

    private func delegateDetail(_ run: DelegateRun) -> String {
        var parts: [String] = ["delivery \(PressureFormat.shortSession(run.deliveryID))"]
        if let passed = run.localPassed {
            var local = "mac \(passed ? "pass" : "FAIL")"
            if let ms = run.localMS { local += " \(PressureFormat.durationMS(ms))" }
            parts.append(local)
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
        if let verified = run.treeVerified {
            parts.append(verified ? "tree verified" : "tree NOT verified")
        }
        if let allowed = run.remotePassAllowed {
            parts.append(allowed ? "remote-pass allowed" : "remote-pass not allowed")
        }
        return parts.joined(separator: " · ")
    }

    // MARK: Box jobs

    private var jobRepoOptions: [String] {
        ["All"] + Array(Set(hetzner.jobs.map(\.repo))).sorted()
    }

    private var jobCallerOptions: [String] {
        ["All"] + Array(Set(hetzner.jobs.map(\.caller))).sorted()
    }

    private var filteredJobs: [BoxJob] {
        var list = hetzner.jobs.filter { job in
            if jobRepo != "All", job.repo != jobRepo { return false }
            if jobCaller != "All", job.caller != jobCaller { return false }
            switch jobExit {
            case "OK": if !job.isSuccess { return false }
            case "Failed": if job.isSuccess || job.isSetupFailure { return false }
            case "Setup": if !job.isSetupFailure { return false }
            default: break
            }
            if !jobSearch.isEmpty {
                let hay = (job.repo + " " + job.caller + " " + job.command + " " + job.id).lowercased()
                if !hay.contains(jobSearch.lowercased()) { return false }
            }
            return true
        }
        if jobSort == "Longest" {
            list.sort { ($0.durationSeconds ?? -1) > ($1.durationSeconds ?? -1) }
        }
        return list
    }

    private var jobsCard: some View {
        SectionCard(
            title: "Gate-runner jobs",
            systemImage: "terminal",
            trailing: "\(filteredJobs.count) of \(hetzner.jobs.count)"
        ) {
            VStack(alignment: .leading, spacing: 8) {
                HStack(spacing: 12) {
                    filterLabel("Repo")
                    Picker("", selection: $jobRepo) {
                        ForEach(jobRepoOptions, id: \.self) { Text($0).tag($0) }
                    }
                    .pickerStyle(.menu)
                    .labelsHidden()
                    .fixedSize()
                    filterLabel("Caller")
                    Picker("", selection: $jobCaller) {
                        ForEach(jobCallerOptions, id: \.self) { Text($0).tag($0) }
                    }
                    .pickerStyle(.menu)
                    .labelsHidden()
                    .fixedSize()
                }
                HStack(spacing: 12) {
                    filterLabel("Exit")
                    Picker("", selection: $jobExit) {
                        ForEach(["All", "OK", "Failed", "Setup"], id: \.self) { Text($0).tag($0) }
                    }
                    .pickerStyle(.segmented)
                    .labelsHidden()
                    filterLabel("Sort")
                    Picker("", selection: $jobSort) {
                        ForEach(["Newest", "Longest"], id: \.self) { Text($0).tag($0) }
                    }
                    .pickerStyle(.segmented)
                    .labelsHidden()
                }
                HStack(spacing: 6) {
                    Image(systemName: "magnifyingglass")
                        .foregroundStyle(.secondary)
                    TextField("Search command, repo, caller, job id", text: $jobSearch)
                        .textFieldStyle(.roundedBorder)
                }
            }
            .padding(.bottom, 4)

            if hetzner.jobs.isEmpty {
                Text(hetzner.boxReachable
                    ? "No jobs in the fetched window."
                    : "No box jobs loaded yet — the box may be unreachable.")
                    .foregroundStyle(.secondary)
            } else if filteredJobs.isEmpty {
                Text("No jobs match the current filters.")
                    .foregroundStyle(.secondary)
            } else {
                LazyVStack(spacing: 0) {
                    ForEach(filteredJobs.prefix(visibleJobLimit)) { job in
                        jobRow(job)
                        Divider().opacity(0.4)
                    }
                }
                if filteredJobs.count > visibleJobLimit {
                    Button("Show more jobs (\(visibleJobLimit) of \(filteredJobs.count) shown)") {
                        visibleJobLimit += 120
                    }
                    .padding(.top, 6)
                }
            }
            Text("Source: /srv/build/logs/jobs.log on gaterunner-1 via ssh (tail of 1000) — refreshed about once a minute while this pane is visible and active. Select a job to view its captured log.")
                .font(.caption2)
                .foregroundStyle(.tertiary)
                .padding(.top, 6)
        }
    }

    private func jobRow(_ job: BoxJob) -> some View {
        Button {
            hetzner.selectJob(job)
        } label: {
            VStack(alignment: .leading, spacing: 3) {
                HStack(spacing: 8) {
                    Text(job.repo)
                        .font(.subheadline.weight(.semibold))
                    if let jobClass = job.jobClass {
                        chip(jobClass, color: .secondary)
                    }
                    chip(exitLabel(job), color: exitColor(job))
                    if let dur = job.durationSeconds {
                        Text(PressureFormat.duration(seconds: Int64(dur)))
                            .font(PressureTheme.monoCaption)
                            .foregroundStyle(.secondary)
                    }
                    Spacer()
                    if hetzner.selectedJobID == job.id {
                        Image(systemName: "chevron.down")
                            .font(.caption2)
                            .foregroundStyle(.secondary)
                    }
                    Text(PressureFormat.shortTime(job.at))
                        .font(PressureTheme.monoCaption)
                        .foregroundStyle(.secondary)
                }
                Text(jobMetaLine(job))
                    .font(PressureTheme.monoCaption)
                    .foregroundStyle(.secondary)
                    .lineLimit(1)
                    .truncationMode(.middle)
                if !job.command.isEmpty {
                    Text(job.command)
                        .font(.caption)
                        .foregroundStyle(.tertiary)
                        .lineLimit(1)
                        .truncationMode(.middle)
                }
            }
            .padding(.vertical, 6)
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
    }

    private func jobMetaLine(_ job: BoxJob) -> String {
        var line = "\(job.caller) · job \(job.id)"
        if let cand = job.cand {
            line += " · cand \(String(cand.prefix(8)))"
        }
        if let sha = job.sha {
            line += " · sha \(String(sha.prefix(8)))"
        }
        return line
    }

    private func exitLabel(_ job: BoxJob) -> String {
        job.isSetupFailure ? "SETUP" : "exit \(job.exitCode)"
    }

    private func exitColor(_ job: BoxJob) -> Color {
        if job.isSuccess { return .green }
        if job.isSetupFailure { return .orange }
        return .red
    }

    // MARK: Job log

    private var jobLogCard: some View {
        SectionCard(
            title: "Job log — \(hetzner.selectedJobID ?? "")",
            systemImage: "doc.text",
            trailing: logTrailing
        ) {
            if hetzner.jobLogLoading {
                HStack(spacing: 8) {
                    ProgressView().controlSize(.small)
                    Text("Fetching log from the box…")
                        .foregroundStyle(.secondary)
                }
            } else if let log = hetzner.jobLog {
                VStack(alignment: .leading, spacing: 6) {
                    HStack {
                        Spacer()
                        Button {
                            copyText(log)
                        } label: {
                            Label("Copy log (first 100 KB)", systemImage: "doc.on.doc")
                        }
                        .controlSize(.small)
                    }
                    ScrollView {
                        Text(log)
                            .font(.system(.caption, design: .monospaced))
                            .frame(maxWidth: .infinity, alignment: .leading)
                            .textSelection(.enabled)
                    }
                    .frame(maxHeight: 280)
                }
            } else if let error = hetzner.jobLogError {
                Label("Log read failed: \(error)", systemImage: "exclamationmark.triangle")
                    .foregroundStyle(.orange)
            } else {
                Label("No log loaded.", systemImage: "info.circle")
                    .foregroundStyle(.secondary)
            }
        }
    }

    private var logTrailing: String? {
        hetzner.jobLogLoading ? "loading…" : nil
    }

    // MARK: Shared

    private func filterLabel(_ text: String) -> some View {
        Text(text)
            .font(.caption.weight(.semibold))
            .foregroundStyle(.secondary)
    }

    private func chip(_ text: String, color: Color) -> some View {
        Text(text)
            .font(.caption2.weight(.semibold))
            .padding(.horizontal, 6)
            .padding(.vertical, 2)
            .background(color.opacity(0.15), in: Capsule())
            .foregroundStyle(color)
    }

    private func copyText(_ value: String) {
        NSPasteboard.general.clearContents()
        NSPasteboard.general.setString(value, forType: .string)
    }
}
