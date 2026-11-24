import SwiftUI
import NDevPressureCore

/// Work queue v2 slice 6: where work runs and why. Shows the stall guard's
/// open episode, the placer's decisions, offloaded runs and live runner slots
/// from `session-pressure work placement`.
struct WorkStallBanner: View {
    let stall: WorkQueueStall

    var body: some View {
        HStack(alignment: .top, spacing: 10) {
            Image(systemName: "exclamationmark.octagon.fill")
                .foregroundStyle(PressureTheme.levelColor(.red))
            VStack(alignment: .leading, spacing: 4) {
                Text("Queue stalled for \(PressureFormat.durationMS(stall.durationMS))")
                    .font(.callout.weight(.semibold))
                Text("\(stall.class ?? "work") (weight \(stall.weight ?? 0)) fits in free capacity (\(stall.used ?? 0)/\(stall.effectiveCapacity ?? 0) used) but nothing starts. Operation \(String(stall.operationID.prefix(12))).")
                    .font(.caption)
                    .foregroundStyle(.secondary)
                if let reason = stall.decisionReason, !reason.isEmpty {
                    Text("Scheduler decision: \(reason)")
                        .font(PressureTheme.monoCaption)
                        .foregroundStyle(.secondary)
                }
                Text("ndev session pressure work inspect \(stall.operationID)")
                    .font(PressureTheme.monoCaption)
                    .foregroundStyle(.tertiary)
                    .textSelection(.enabled)
            }
            Spacer()
        }
        .padding(12)
        .background(PressureTheme.levelColor(.red).opacity(0.10), in: RoundedRectangle(cornerRadius: 10))
        .accessibilityElement(children: .combine)
    }
}

struct WorkPlacementCard: View {
    let report: WorkPlacementReport?
    let error: String?
    let loadedAt: Date?

    var body: some View {
        SectionCard(
            title: "Placement & offload",
            systemImage: "arrow.left.arrow.right",
            trailing: report.map { windowText($0.windowSeconds) },
            help: "Where auto-placed checks ran and why: the closed-loop placer keeps a check on an idle Mac only when its measured local time beats the runner."
        ) {
            if let report {
                content(report)
            } else if let error {
                Text(error).font(.caption).foregroundStyle(PressureTheme.levelColor(.warning))
            } else {
                Text("Loading placement…").font(.caption).foregroundStyle(.secondary)
            }
        }
    }

    @ViewBuilder
    private func content(_ report: WorkPlacementReport) -> some View {
        HStack(spacing: 8) {
            chip(report.remote.enabled ? "offload on" : "offload off", good: report.remote.enabled)
            chip(report.remote.placer ? "placer on" : "placer off", good: report.remote.placer)
            if let host = report.remote.host { chip(host, good: true) }
            Spacer()
            if let loadedAt {
                ReadAgeView(date: loadedAt).font(.caption2).foregroundStyle(.tertiary)
            }
        }

        ForEach(report.runners) { runner in
            runnerRow(runner)
        }
        if let runnersError = report.runnersError {
            Text("Runner slots unavailable: \(runnersError)").font(.caption).foregroundStyle(.secondary)
        }

        Divider().opacity(0.4)
        HStack(alignment: .firstTextBaseline, spacing: 16) {
            stat("decisions", "\(report.decisions.total)")
            stat("kept local", "\(report.decisions.local)")
            stat("sent remote", "\(report.decisions.remote)")
            stat("remote runs", "\(report.remoteRuns.total)")
            stat("remote exit ≠ 0", "\(report.remoteRuns.failed)", warn: report.remoteRuns.failed > 0)
                .help("Remote runs that ended with a non-zero exit. This includes real test failures, not only runner trouble.")
            stat("remote p50", report.remoteRuns.p50MS > 0 ? PressureFormat.durationMS(report.remoteRuns.p50MS) : "–")
            stat("remote p90", report.remoteRuns.p90MS > 0 ? PressureFormat.durationMS(report.remoteRuns.p90MS) : "–")
        }
        if !report.decisions.byReason.isEmpty {
            reasonList("Placer reasons", report.decisions.byReason)
        }
        if !report.localFallbacks.isEmpty {
            reasonList("Always local (Mac-only work)", report.localFallbacks)
        }
        if !report.decisions.recent.isEmpty {
            VStack(alignment: .leading, spacing: 2) {
                Text("Recent decisions").font(.caption.weight(.semibold)).foregroundStyle(.secondary)
                ForEach(Array(report.decisions.recent.prefix(5).enumerated()), id: \.offset) { _, row in
                    HStack(spacing: 8) {
                        Image(systemName: row.placement == "local" ? "laptopcomputer" : "server.rack")
                            .foregroundStyle(.secondary)
                            .frame(width: 16)
                        Text(row.class ?? "–").font(PressureTheme.monoCaption).frame(width: 96, alignment: .leading)
                        Text(row.reason).font(.caption).lineLimit(1)
                        Spacer()
                        Text(PressureFormat.relative(row.at)).font(.caption2).foregroundStyle(.tertiary)
                    }
                }
            }
        }
        if !report.parkedStalls.isEmpty {
            VStack(alignment: .leading, spacing: 4) {
                Label("\(report.parkedStalls.count) long-parked \(report.parkedStalls.count == 1 ? "lease" : "leases") (>5m)", systemImage: "lock.trianglebadge.exclamationmark")
                    .font(.caption.weight(.semibold))
                    .foregroundStyle(PressureTheme.levelColor(.warning))
                ForEach(report.parkedStalls.prefix(4)) { parked in
                    Text("\(parked.class ?? "work") pid \(PressureFormat.pid(parked.pid)) · parked \(PressureFormat.durationMS(parked.parkedDurationMS)) · op \(String(parked.operationID.prefix(12)))")
                        .font(PressureTheme.monoCaption)
                        .foregroundStyle(.secondary)
                }
            }
            .padding(8)
            .frame(maxWidth: .infinity, alignment: .leading)
            .background(PressureTheme.levelColor(.warning).opacity(0.10), in: RoundedRectangle(cornerRadius: 8))
        }
        if let stall = report.lastStall, report.activeStall == nil, let since = stall.since {
            Text("Last stall: \(stall.class ?? "work") idle \(PressureFormat.durationMS(stall.durationMS)), \(PressureFormat.relative(since)).")
                .font(.caption)
                .foregroundStyle(.secondary)
        }
    }

    private func runnerRow(_ runner: RunnerSlots) -> some View {
        VStack(alignment: .leading, spacing: 4) {
            HStack {
                Image(systemName: runner.healthy ? "checkmark.circle.fill" : "xmark.octagon.fill")
                    .foregroundStyle(runner.healthy ? PressureTheme.levelColor(.normal) : PressureTheme.levelColor(.red))
                Text(runner.name).font(.callout.weight(.semibold))
                Spacer()
                Text("heavy \(runner.heavyJobs)/\(runner.heavySlots) · light \(runner.lightJobs)/\(runner.lightSlots)")
                    .font(PressureTheme.monoCaption)
                    .foregroundStyle(.secondary)
            }
            SlotBar(jobs: runner.heavyJobs, slots: runner.heavySlots)
                .help("Queued, preparing and running heavy jobs against the runner's heavy slots. Above the slot count, jobs wait in the runner queue.")
            if let error = runner.error, !error.isEmpty {
                Text(error).font(.caption).foregroundStyle(PressureTheme.levelColor(.warning))
            }
        }
    }

    private func chip(_ text: String, good: Bool) -> some View {
        Text(text)
            .font(.caption2.weight(.semibold))
            .padding(.horizontal, 6)
            .padding(.vertical, 2)
            .background((good ? PressureTheme.levelColor(.normal) : Color.secondary).opacity(0.15), in: Capsule())
    }

    private func stat(_ label: String, _ value: String, warn: Bool = false) -> some View {
        VStack(alignment: .leading, spacing: 1) {
            Text(value)
                .font(.callout.weight(.semibold).monospacedDigit())
                .foregroundStyle(warn ? PressureTheme.levelColor(.warning) : Color.primary)
            Text(label).font(.caption2).foregroundStyle(.secondary)
        }
    }

    private func reasonList(_ title: String, _ counts: [String: Int]) -> some View {
        VStack(alignment: .leading, spacing: 2) {
            Text(title).font(.caption.weight(.semibold)).foregroundStyle(.secondary)
            ForEach(counts.sorted { $0.value == $1.value ? $0.key < $1.key : $0.value > $1.value }.prefix(6), id: \.key) { reason, count in
                HStack {
                    Text(reason).font(PressureTheme.monoCaption).lineLimit(1)
                    Spacer()
                    Text("\(count)").font(PressureTheme.monoCaption).foregroundStyle(.secondary)
                }
            }
        }
    }

    private func windowText(_ seconds: Int) -> String {
        seconds >= 3600 ? "last \(seconds / 3600)h" : "last \(max(seconds / 60, 1))m"
    }
}

/// Runner slot use. Jobs beyond the slot count are shown as overflow.
struct SlotBar: View {
    let jobs: Int
    let slots: Int

    var body: some View {
        HStack(spacing: 3) {
            ForEach(0..<max(slots, 0), id: \.self) { index in
                RoundedRectangle(cornerRadius: 2)
                    .fill(index < jobs ? PressureTheme.levelColor(jobs >= slots ? .warning : .normal) : Color.primary.opacity(0.08))
                    .frame(height: 8)
            }
            if jobs > slots {
                Text("+\(jobs - slots) queued")
                    .font(.caption2.weight(.semibold))
                    .foregroundStyle(PressureTheme.levelColor(.warning))
                    .fixedSize()
            }
        }
        .accessibilityElement(children: .ignore)
        .accessibilityLabel("\(jobs) of \(slots) runner slots in use")
    }
}
