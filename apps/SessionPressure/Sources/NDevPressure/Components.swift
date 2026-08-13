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

    var body: some View {
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
        .help(help ?? PressureHelp.metricTitle(title))
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
