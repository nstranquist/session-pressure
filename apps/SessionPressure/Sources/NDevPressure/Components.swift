import SwiftUI
import NDevPressureCore

// MARK: - Level badge

struct LevelBadge: View {
    let level: PressureLevel
    var compact: Bool = false

    var body: some View {
        Text(compact ? level.shortLabel : level.displayName.uppercased())
            .font(compact ? .caption2.weight(.bold) : .caption.weight(.bold))
            .tracking(compact ? 0.4 : 0.8)
            .padding(.horizontal, compact ? 6 : 10)
            .padding(.vertical, compact ? 3 : 5)
            .foregroundStyle(PressureTheme.levelColor(level))
            .background(PressureTheme.levelFill(level), in: Capsule())
            .overlay(Capsule().stroke(PressureTheme.levelColor(level).opacity(0.35), lineWidth: 1))
            .help(PressureHelp.level(level))
    }
}

// MARK: - Metric card

struct MetricCard: View {
    let title: String
    let value: String
    var subtitle: String? = nil
    var accent: Color = .secondary
    var progress: Double? = nil // 0...1
    var help: String? = nil
    var action: (() -> Void)? = nil

    var body: some View {
        Group {
            if let action {
                Button(action: action) { card }
                    .buttonStyle(.plain)
            } else {
                card
            }
        }
        .help(help ?? PressureHelp.metricTitle(title))
    }

    private var card: some View {
        VStack(alignment: .leading, spacing: 8) {
            Text(title.uppercased())
                .font(.caption2.weight(.semibold))
                .tracking(0.6)
                .foregroundStyle(.secondary)

            Text(value)
                .font(.system(.title2, design: .monospaced).weight(.semibold))
                .foregroundStyle(accent)
                .lineLimit(1)
                .minimumScaleFactor(0.7)

            if let subtitle {
                Text(subtitle)
                    .font(.caption)
                    .foregroundStyle(.secondary)
                    .lineLimit(2)
            }

            if let progress {
                GeometryReader { geo in
                    ZStack(alignment: .leading) {
                        Capsule().fill(Color.primary.opacity(0.08))
                        Capsule()
                            .fill(accent.opacity(0.85))
                            .frame(width: max(4, geo.size.width * min(max(progress, 0), 1)))
                    }
                }
                .frame(height: 5)
            }
        }
        .padding(14)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(PressureTheme.card, in: RoundedRectangle(cornerRadius: 12, style: .continuous))
        .overlay(
            RoundedRectangle(cornerRadius: 12, style: .continuous)
                .stroke(PressureTheme.hairline, lineWidth: 1)
        )
        .contentShape(RoundedRectangle(cornerRadius: 12, style: .continuous))
    }
}

// MARK: - Section card

struct SectionCard<Content: View>: View {
    let title: String
    var systemImage: String? = nil
    var trailing: String? = nil
    var help: String? = nil
    @ViewBuilder var content: () -> Content

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            HStack(spacing: 8) {
                if let systemImage {
                    Image(systemName: systemImage)
                        .foregroundStyle(.secondary)
                        .help(help ?? title)
                }
                Text(title)
                    .font(.headline)
                    .help(help ?? title)
                Spacer()
                if let trailing {
                    Text(trailing)
                        .font(PressureTheme.monoCaption)
                        .foregroundStyle(.secondary)
                }
            }
            content()
        }
        .padding(16)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(PressureTheme.card, in: RoundedRectangle(cornerRadius: 14, style: .continuous))
        .overlay(
            RoundedRectangle(cornerRadius: 14, style: .continuous)
                .stroke(PressureTheme.hairline, lineWidth: 1)
        )
        .help(help ?? title)
    }
}

// MARK: - Status chip

struct StatusChip: View {
    let label: String
    let ok: Bool
    var detail: String? = nil
    var help: String? = nil

    var body: some View {
        HStack(spacing: 6) {
            Circle()
                .fill(ok ? PressureTheme.levelColor(.normal) : PressureTheme.levelColor(.red))
                .frame(width: 7, height: 7)
            Text(label)
                .font(.caption.weight(.medium))
            if let detail {
                Text(detail)
                    .font(PressureTheme.monoCaption)
                    .foregroundStyle(.secondary)
            }
        }
        .padding(.horizontal, 10)
        .padding(.vertical, 6)
        .background(Color.primary.opacity(0.05), in: Capsule())
        .help(help ?? "\(label): \(ok ? "ok" : "attention")\(detail.map { " (\($0))" } ?? "")")
    }
}

// MARK: - Work-style suggestion

/// Collapsed by default: current vs suggested stay visible; details sit behind the chevron.
struct WorkStyleSuggestionCard: View {
    @EnvironmentObject private var store: PressureStore
    let suggestion: PolicySuggestion
    @Binding var expanded: Bool
    var onApply: () -> Void

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            HStack(alignment: .center, spacing: 10) {
                Button {
                    withAnimation(.easeInOut(duration: 0.18)) { expanded.toggle() }
                } label: {
                    HStack(alignment: .center, spacing: 10) {
                        Image(systemName: "lightbulb")
                            .foregroundStyle(.secondary)
                            .help(PressureHelp.policySuggestion)
                        VStack(alignment: .leading, spacing: 4) {
                            Text("Work style")
                                .font(.headline)
                            HStack(spacing: 8) {
                                Text("Current \(suggestion.currentTitle)")
                                Image(systemName: "arrow.right")
                                    .font(.caption2.weight(.semibold))
                                    .foregroundStyle(.tertiary)
                                Text(suggestion.headline)
                                    .foregroundStyle(suggestion.kind == .restoreDefault ? PressureTheme.levelColor(.normal) : .orange)
                            }
                            .font(.callout)
                            if !suggestion.currentFlags.isEmpty {
                                Text(suggestion.currentFlags)
                                    .font(PressureTheme.monoCaption)
                                    .foregroundStyle(.secondary)
                            }
                        }
                        Spacer(minLength: 0)
                        Image(systemName: "chevron.down")
                            .font(.caption.weight(.semibold))
                            .foregroundStyle(.secondary)
                            .rotationEffect(.degrees(expanded ? 180 : 0))
                            .accessibilityHidden(true)
                    }
                    .contentShape(Rectangle())
                }
                .buttonStyle(.plain)
                .help(expanded ? "Hide suggestion details" : "Show why this style is suggested")
                .accessibilityLabel("Work style suggestion")
                .accessibilityValue("Current \(suggestion.currentTitle), \(suggestion.headline)")
                .accessibilityHint(expanded ? "Collapse details" : "Expand details")

                if suggestion.showsApplyWhenCollapsed {
                    Button(suggestion.kind == .restoreDefault ? "Apply Balanced" : "Apply \(suggestion.title)") {
                        onApply()
                    }
                    .buttonStyle(.borderedProminent)
                    .controlSize(.small)
                    .disabled(store.busyAction != nil)
                    .help("Runs \(suggestion.applyCommand) after a confirmation. Never silent.")
                }
            }

            if expanded {
                CopyableOperatorText(
                    text: [suggestion.explanation, suggestion.tradeoff].joined(separator: "\n\n"),
                    agentPaste: suggestion.agentPaste,
                    resolveTitle: suggestion.showsApplyWhenCollapsed ? nil : (suggestion.weakensProtection ? "Apply…" : "Apply \(suggestion.title)"),
                    resolveHelp: "Runs \(suggestion.applyCommand) after a confirmation. Never silent.",
                    destructive: suggestion.weakensProtection,
                    onResolve: suggestion.showsApplyWhenCollapsed ? nil : onApply
                )
            }
        }
        .padding(16)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(PressureTheme.card, in: RoundedRectangle(cornerRadius: 14, style: .continuous))
        .overlay(
            RoundedRectangle(cornerRadius: 14, style: .continuous)
                .stroke(PressureTheme.hairline, lineWidth: 1)
        )
    }
}

// MARK: - Copyable operator text

/// Selectable operator copy with an explicit clipboard action, and an optional
/// confirmed resolve button. Used for advisory suggestions and doctor fixes.
struct CopyableOperatorText: View {
    @EnvironmentObject private var store: PressureStore
    let text: String
    var agentPaste: String? = nil
    var resolveTitle: String? = nil
    var resolveHelp: String? = nil
    var resolveDisabled: Bool = false
    var destructive: Bool = false
    var onResolve: (() -> Void)? = nil

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            Text(text)
                .font(.callout)
                .textSelection(.enabled)
                .fixedSize(horizontal: false, vertical: true)
            HStack(spacing: 10) {
                Button {
                    store.copyToPasteboard(agentPaste ?? text)
                } label: {
                    Label("Copy for agent", systemImage: "doc.on.doc")
                }
                .buttonStyle(.borderless)
                .controlSize(.small)
                .help(PressureHelp.copyForAgent)
                if let resolveTitle, let onResolve {
                    Button(resolveTitle, role: destructive ? .destructive : nil, action: onResolve)
                        .buttonStyle(.borderedProminent)
                        .controlSize(.small)
                        .disabled(resolveDisabled || store.busyAction != nil)
                        .help(resolveHelp ?? "")
                }
            }
        }
    }
}

// MARK: - Gauge ring

struct PressureRing: View {
    let level: PressureLevel
    let freePercent: Int
    let hostCPU: Double
    var hostCPUAvailable: Bool = true
    var size: CGFloat = 148

    private var utilization: PressureUtilization {
        PressureUtilization(freePercent: freePercent, hostCPUPercent: hostCPU, hostCPUAvailable: hostCPUAvailable)
    }

    var body: some View {
        ZStack {
            Circle()
                .stroke(Color.primary.opacity(0.08), lineWidth: 12)
            Circle()
                .trim(from: 0, to: utilization.fraction)
                .stroke(
                    PressureTheme.levelColor(level),
                    style: StrokeStyle(lineWidth: 12, lineCap: .round)
                )
                .rotationEffect(.degrees(-90))
                .animation(.easeInOut(duration: 0.35), value: utilization.fraction)

            VStack(spacing: 4) {
                Text(level.displayName)
                    .font(.headline)
                    .foregroundStyle(PressureTheme.levelColor(level))
                Text("used \(utilization.memoryUsedPercent)%")
                    .font(PressureTheme.monoCaption)
                    .foregroundStyle(.secondary)
                Text("free \(freePercent)%")
                    .font(PressureTheme.monoCaption)
                    .foregroundStyle(.secondary)
            }
        }
        .frame(width: size, height: size)
        .help(PressureHelp.ring(level: level, utilization: utilization))
    }
}

// MARK: - Empty / error

struct InlineError: View {
    let message: String

    var body: some View {
        HStack(alignment: .top, spacing: 8) {
            Image(systemName: "exclamationmark.triangle.fill")
                .foregroundStyle(PressureTheme.levelColor(.warning))
            Text(message)
                .font(.caption)
                .textSelection(.enabled)
                .foregroundStyle(.primary)
            Spacer(minLength: 0)
        }
        .padding(10)
        .background(PressureTheme.levelFill(.warning), in: RoundedRectangle(cornerRadius: 8, style: .continuous))
    }
}

struct EmptyHint: View {
    let title: String
    let systemImage: String
    var detail: String? = nil

    var body: some View {
        ContentUnavailableView {
            Label(title, systemImage: systemImage)
        } description: {
            if let detail {
                Text(detail)
            }
        }
        .frame(maxWidth: .infinity, minHeight: 120)
    }
}

// MARK: - Back navigation (fixed slot, no layout jump)

/// Always reserves 28×28 so enabling/disabling never shifts neighbours.
/// Placed in the toolbar's trailing group before the bell; opacity 0 keeps
/// the slot when at the root (Overview).
struct BackButton: View {
    @EnvironmentObject private var store: PressureStore

    var body: some View {
        Button {
            store.goBack()
        } label: {
            Image(systemName: "chevron.left")
                .font(.system(size: 13, weight: .semibold))
                .foregroundStyle(store.canGoBack ? Color.primary : Color.secondary.opacity(0.35))
                .frame(width: 28, height: 28)
                .background(Color.primary.opacity(store.canGoBack ? 0.06 : 0), in: RoundedRectangle(cornerRadius: 7, style: .continuous))
                .overlay(RoundedRectangle(cornerRadius: 7, style: .continuous).stroke(store.canGoBack ? PressureTheme.hairline : Color.clear, lineWidth: 1))
        }
        .buttonStyle(.plain)
        .disabled(!store.canGoBack)
        .opacity(store.canGoBack ? 1 : 0)
        .animation(.easeInOut(duration: 0.15), value: store.canGoBack)
        .help(backHelp)
        .accessibilityLabel("Back")
        .accessibilityValue(store.backDestination?.rawValue ?? "Root")
        .accessibilityHint(store.canGoBack ? "Go back to \(store.backDestination?.rawValue ?? "")" : "Root of navigation")
        .keyboardShortcut("[", modifiers: [.command])
    }

    private var backHelp: String {
        if let dest = store.backDestination {
            return "Back to \(dest.rawValue) (⌘[)"
        }
        return "At root — no back history"
    }
}

// MARK: - Notification bell (top-right)

struct NotificationBell: View {
    @EnvironmentObject private var store: PressureStore
    @State private var showingPopover = false
    @State private var popoverIDs: Set<String> = []

    var body: some View {
        Button {
            if !showingPopover {
                popoverIDs = Set(store.notifications.map(\.id))
            }
            showingPopover.toggle()
            if showingPopover {
                // Marking viewed happens on dismiss so the list stays visible while open.
            } else {
                // Dismiss after viewing — clears badge even though the host condition persists.
                if !popoverIDs.isEmpty {
                    for id in popoverIDs { store.dismissNotification(id: id) }
                    popoverIDs.removeAll()
                }
            }
        } label: {
            ZStack(alignment: .topTrailing) {
                Image(systemName: store.notificationCount > 0 ? "bell.badge.fill" : "bell")
                    .font(.system(size: 15, weight: .regular))
                    .foregroundStyle(store.notificationCount > 0 ? PressureTheme.levelColor(worstSeverity) : .primary)
                    .symbolRenderingMode(store.notificationCount > 0 ? .multicolor : .monochrome)

                if store.notificationCount > 0 {
                    Text("\(store.notificationCount)")
                        .font(.system(size: 9, weight: .bold, design: .rounded))
                        .foregroundStyle(.white)
                        .frame(minWidth: 14, minHeight: 14)
                        .background(PressureTheme.levelColor(worstSeverity), in: Capsule())
                        .offset(x: 7, y: -6)
                        .transition(.scale.combined(with: .opacity))
                }
            }
            .frame(width: 28, height: 28)
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .help(helpText)
        .accessibilityLabel("Notifications")
        .accessibilityValue(store.notificationCount > 0 ? "\(store.notificationCount) active" : "No notifications")
        .popover(isPresented: $showingPopover) {
            NotificationPopoverBody()
                .environmentObject(store)
                .frame(width: 320)
        }
        .onChange(of: showingPopover) { _, isShowing in
            if !isShowing && !popoverIDs.isEmpty {
                for id in popoverIDs { store.dismissNotification(id: id) }
                popoverIDs.removeAll()
            }
        }
        .onAppear {
            // Ensure dismissed set reflects current store; no-op.
        }
        .animation(.easeInOut(duration: 0.18), value: store.notificationCount)
    }

    private var worstSeverity: PressureLevel {
        store.notifications.map(\.severity).max() ?? .normal
    }

    private var helpText: String {
        if store.notificationCount == 0 {
            return "No active alerts. Pressure is normal."
        }
        let titles = store.notifications.map(\.title).joined(separator: " · ")
        return "\(store.notificationCount) active \(store.notificationCount == 1 ? "alert" : "alerts")\n\(titles)\n\nClick for details."
    }
}

struct NotificationPopoverBody: View {
    @EnvironmentObject private var store: PressureStore
    @Environment(\.dismiss) private var dismiss

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            HStack {
                Text("Notifications")
                    .font(.headline)
                Spacer()
                if store.notificationCount > 0 {
                    Text("\(store.notificationCount)")
                        .font(.caption2.weight(.bold))
                        .padding(.horizontal, 6)
                        .padding(.vertical, 2)
                        .background(PressureTheme.levelColor(maxSeverity), in: Capsule())
                        .foregroundStyle(.white)
                }
                Button { dismiss() } label: {
                    Image(systemName: "xmark")
                        .font(.caption.weight(.medium))
                        .foregroundStyle(.secondary)
                }
                .buttonStyle(.plain)
                .help("Close")
            }
            .padding(.horizontal, 12)
            .padding(.vertical, 10)

            Divider()

            if store.notifications.isEmpty {
                VStack(spacing: 8) {
                    Image(systemName: "checkmark.seal.fill")
                        .font(.title2)
                        .foregroundStyle(PressureTheme.levelColor(.normal))
                    Text("All clear")
                        .font(.callout.weight(.medium))
                    Text("No active pressure, admission, or Day Gate alerts.")
                        .font(.caption)
                        .foregroundStyle(.secondary)
                }
                .frame(maxWidth: .infinity, minHeight: 90)
                .padding(12)
            } else {
                ScrollView {
                    VStack(spacing: 0) {
                        ForEach(store.notifications) { item in
                            NotificationRow(item: item) {
                                if let section = item.section {
                                    store.selectedSection = section
                                }
                                // Explicit per-item dismissal on click; onDismiss also clears viewed set.
                                store.dismissNotification(id: item.id)
                                if item.id == "error" {
                                    store.lastError = nil
                                }
                                dismiss()
                            }
                            if item.id != store.notifications.last?.id {
                                Divider().opacity(0.4).padding(.horizontal, 12)
                            }
                        }
                    }
                }
                .frame(maxHeight: 340)
            }
        }
        .background(PressureTheme.card)
    }

    private var maxSeverity: PressureLevel {
        store.notifications.map(\.severity).max() ?? .normal
    }
}

private struct NotificationRow: View {
    let item: PressureStore.NotificationItem
    var onTap: () -> Void

    var body: some View {
        Button(action: onTap) {
            HStack(alignment: .top, spacing: 10) {
                Image(systemName: item.systemImage)
                    .font(.callout)
                    .foregroundStyle(PressureTheme.levelColor(item.severity))
                    .frame(width: 20, alignment: .center)
                    .padding(.top, 1)

                VStack(alignment: .leading, spacing: 3) {
                    Text(item.title)
                        .font(.callout.weight(.medium))
                        .foregroundStyle(.primary)
                        .multilineTextAlignment(.leading)
                        .lineLimit(2)
                    if let detail = item.detail, !detail.isEmpty {
                        Text(detail)
                            .font(.caption)
                            .foregroundStyle(.secondary)
                            .lineLimit(3)
                            .multilineTextAlignment(.leading)
                    }
                }

                Spacer(minLength: 0)

                if item.section != nil {
                    Image(systemName: "chevron.right")
                        .font(.caption2.weight(.semibold))
                        .foregroundStyle(.tertiary)
                        .padding(.top, 2)
                }
            }
            .padding(.horizontal, 12)
            .padding(.vertical, 9)
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .help(item.title)
    }
}

// MARK: - Capacity bar

struct CapacityBar: View {
    let used: Int
    let capacity: Int
    let available: Int

    private var fraction: Double {
        guard capacity > 0 else { return 0 }
        return min(1, Double(used) / Double(capacity))
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            HStack {
                Text("Capacity")
                    .font(.caption.weight(.semibold))
                    .foregroundStyle(.secondary)
                Spacer()
                Text("\(used) / \(capacity) used · \(available) free")
                    .font(PressureTheme.monoCaption)
                    .foregroundStyle(.secondary)
            }
            GeometryReader { geo in
                ZStack(alignment: .leading) {
                    Capsule().fill(Color.primary.opacity(0.08))
                    Capsule()
                        .fill(fraction > 0.85 ? PressureTheme.levelColor(.red) : PressureTheme.levelColor(.normal))
                        .frame(width: max(4, geo.size.width * fraction))
                }
            }
            .frame(height: 8)
        }
        .help(PressureHelp.workCapacity)
    }
}
