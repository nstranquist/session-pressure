import SwiftUI
import NDevPressureCore

/// Read-only view of the advisor's daily gate. Ranking and plan writes remain
/// exclusively owned by `ndev advisor`.
struct DayGateView: View {
    @EnvironmentObject private var store: PressureStore

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 16) {
                header
                if let brief = store.advisorBrief {
                    laneCounts(brief)
                    residue(brief)
                    contract(brief)
                } else {
                    unavailable
                }
            }
            .padding(20)
        }
        .background(PressureTheme.bg)
    }

    private var header: some View {
        HStack(alignment: .center, spacing: 16) {
            Image(systemName: "calendar.badge.exclamationmark")
                .font(.system(size: 34, weight: .semibold))
                .foregroundStyle(store.residueCount > 0 ? PressureTheme.levelColor(.warning) : PressureTheme.levelColor(.normal))
            VStack(alignment: .leading, spacing: 5) {
                Text("Day Gate")
                    .font(.largeTitle.weight(.semibold))
                Text("Read-only projection of the once-per-day advisor brief")
                    .foregroundStyle(.secondary)
            }
            Spacer()
            Button {
                Task { await store.refreshAdvisorBrief(force: true) }
            } label: {
                Label("Refresh brief", systemImage: "arrow.clockwise")
            }
            .help(PressureHelp.advisorBrief)
        }
        .padding(18)
        .background(PressureTheme.card, in: RoundedRectangle(cornerRadius: 16, style: .continuous))
    }

    private func laneCounts(_ brief: AdvisorBrief) -> some View {
        SectionCard(
            title: "Advisor decision",
            systemImage: brief.residueCount > 0 ? "exclamationmark.triangle.fill" : "checkmark.seal.fill",
            trailing: brief.day,
            help: PressureHelp.advisorBrief
        ) {
            HStack(spacing: 10) {
                StatusChip(label: "gate \(brief.gate)", ok: brief.gate == "none")
                StatusChip(label: "residue \(brief.residueCount)", ok: brief.residueCount == 0)
                StatusChip(label: "eligible \(brief.lanes.eligible.count)", ok: brief.gate == "none")
                Text("moved \(brief.lanes.moved.count) · spine \(brief.lanes.spine.count) · parked \(brief.lanes.parked.count) · protected \(brief.lanes.doNotTouch.count)")
                    .font(PressureTheme.monoCaption)
                    .foregroundStyle(.secondary)
                Spacer(minLength: 0)
            }
        }
    }

    @ViewBuilder
    private func residue(_ brief: AdvisorBrief) -> some View {
        SectionCard(
            title: "Residue",
            systemImage: "tray.full",
            trailing: "\(brief.residueCount)",
            help: PressureHelp.advisorResidue
        ) {
            if brief.lanes.residue.isEmpty {
                Text("No residue blocks product work today.")
                    .foregroundStyle(.secondary)
            } else {
                VStack(alignment: .leading, spacing: 10) {
                    ForEach(Array(brief.lanes.residue.enumerated()), id: \.offset) { _, item in
                        VStack(alignment: .leading, spacing: 3) {
                            Text(item.title?.isEmpty == false ? item.title! : item.id)
                                .font(.body.weight(.medium))
                                .textSelection(.enabled)
                            if item.title?.isEmpty == false {
                                Text(item.id)
                                    .font(PressureTheme.monoCaption)
                                    .foregroundStyle(.secondary)
                                    .textSelection(.enabled)
                            }
                        }
                    }
                }
            }
        }
    }

    private func contract(_ brief: AdvisorBrief) -> some View {
        SectionCard(
            title: "Canonical contract",
            systemImage: "terminal",
            trailing: brief.truncated ? "COMPACT" : nil,
            help: PressureHelp.advisorBrief
        ) {
            VStack(alignment: .leading, spacing: 8) {
                CopyableOperatorText(
                    text: "ndev --json advisor brief show",
                    agentPaste: "ndev --json advisor brief show"
                )
                Text("NDev Pressure reads this validated artifact. It does not gather sources, rank work, or write a plan.")
                    .font(.caption)
                    .foregroundStyle(.secondary)
                if let hint = brief.detailHint, !hint.isEmpty {
                    Text(hint)
                        .font(.caption)
                        .foregroundStyle(.tertiary)
                }
            }
        }
    }

    private var unavailable: some View {
        SectionCard(
            title: "Brief unavailable",
            systemImage: "questionmark.circle",
            help: PressureHelp.advisorBrief
        ) {
            VStack(alignment: .leading, spacing: 8) {
                Text("Pressure monitoring remains active. The day gate is hidden until today's validated brief exists.")
                    .foregroundStyle(.secondary)
                if let error = store.advisorBriefError {
                    Text(error)
                        .font(PressureTheme.monoCaption)
                        .foregroundStyle(.secondary)
                        .textSelection(.enabled)
                }
            }
        }
    }
}
