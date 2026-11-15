import SwiftUI
import Charts
import NDevPressureCore

/// Fleet capacity and usage: is each runner earning its keep, and did
/// anything (CPU, heavy slots, memory, disk, I/O, network, traffic terms)
/// limit or threaten its capacity in the window. Read-only.
struct RunnerCapacityCard: View {
    @ObservedObject var store: RunnerCapacityStore
    /// Selecting a runner here also points the job history at it, so the
    /// page has one notion of "the selected runner".
    var onSelectHost: (String) -> Void = { _ in }
    /// Shared across charts: hovering one shows the same instant in all.
    @State private var hoverDate: Date?

    private static let critical = Color(red: 0.92, green: 0.22, blue: 0.28)
    private static let warning = Color(red: 0.95, green: 0.62, blue: 0.18)
    private static let info = Color(red: 0.35, green: 0.62, blue: 0.95)
    private static let good = Color(red: 0.23, green: 0.72, blue: 0.45)

    var body: some View {
        SectionCard(
            title: "Fleet capacity & usage",
            systemImage: "gauge.with.dots.needle.67percent",
            trailing: trailing,
            help: "Use and limits per runner from the runner's own 15-second samples, jobs.log, and kernel counters. Bandwidth history is recorded on this Mac while this tab is open."
        ) {
            VStack(alignment: .leading, spacing: 14) {
                controls
                if let error = store.error {
                    InlineError(message: error)
                }
                if let report = store.report {
                    if store.reportIsForOtherWindow {
                        HStack(spacing: 6) {
                            ProgressView().controlSize(.mini)
                            Text("Reading the last \(store.window.label); showing the \(windowLabel(report)) report until it arrives.")
                                .font(.caption).foregroundStyle(.secondary)
                        }
                    }
                    alerts(report)
                    hostTiles(report)
                    if let host = store.selected {
                        Divider().opacity(0.5)
                        hostDetail(host, report: report)
                    }
                } else if store.loading {
                    ProgressView("Reading the fleet…")
                        .controlSize(.small)
                } else if store.error == nil {
                    EmptyHint(title: "No capacity report yet", systemImage: "gauge.with.dots.needle.0percent")
                }
            }
        }
        .onAppear { store.setCardVisible(true) }
        .onDisappear { store.setCardVisible(false) }
    }

    private var trailing: String {
        guard let at = store.fetchedAt else { return store.loading ? "reading…" : "not read yet" }
        return (store.loading ? "refreshing · " : "updated ") + PressureFormat.relative(min(at, Date().addingTimeInterval(-1)))
    }

    private var controls: some View {
        HStack(spacing: 10) {
            Picker("Window", selection: Binding(
                get: { store.window },
                set: { value in Task { await store.setWindow(value) } }
            )) {
                ForEach(RunnerCapacityWindow.allCases) { w in
                    Text(w.label).tag(w)
                }
            }
            .pickerStyle(.segmented)
            .labelsHidden()
            .frame(maxWidth: 360)
            Button {
                Task { await store.refresh() }
            } label: {
                Image(systemName: "arrow.clockwise")
            }
            .buttonStyle(.borderless)
            .disabled(store.loading)
            .help("Read every runner now")
            Spacer()
            if let gen = store.report?.generation {
                Text("inventory \(gen)")
                    .font(PressureTheme.monoCaption)
                    .foregroundStyle(.tertiary)
                    .lineLimit(1)
                    .truncationMode(.middle)
            }
        }
    }

    // MARK: Alerts

    @ViewBuilder
    private func alerts(_ report: RunnerCapacityReport) -> some View {
        if report.alerts.isEmpty {
            Label("No capacity alerts in the last \(windowLabel(report)).", systemImage: "checkmark.seal")
                .font(.callout)
                .foregroundStyle(Self.good)
        } else {
            VStack(alignment: .leading, spacing: 6) {
                ForEach(report.alerts) { alert in
                    alertRow(alert)
                }
            }
        }
        if let history = report.historyError {
            Text(RunnerCapacityStore.describe(history))
                .font(.caption)
                .foregroundStyle(.secondary)
        }
    }

    private func alertRow(_ alert: CapacityAlert) -> some View {
        let focused = store.focusedAlert == alert.id
        return HStack(alignment: .top, spacing: 10) {
            Image(systemName: severityIcon(alert.severity))
                .foregroundStyle(severityColor(alert.severity))
                .frame(width: 16)
            VStack(alignment: .leading, spacing: 2) {
                HStack(spacing: 6) {
                    Text(alert.title).font(.callout.weight(.semibold))
                    Text(alert.host)
                        .font(PressureTheme.monoCaption)
                        .foregroundStyle(.secondary)
                    if let since = alert.since {
                        Text(rangeLabel(since, alert.until))
                            .font(PressureTheme.monoCaption)
                            .foregroundStyle(.tertiary)
                    }
                }
                Text(alert.detail)
                    .font(.caption)
                    .foregroundStyle(.secondary)
                    .fixedSize(horizontal: false, vertical: true)
            }
            Spacer(minLength: 0)
        }
        .padding(8)
        .background(severityColor(alert.severity).opacity(focused ? 0.18 : 0.08), in: RoundedRectangle(cornerRadius: 8, style: .continuous))
        .overlay(RoundedRectangle(cornerRadius: 8, style: .continuous)
            .stroke(focused ? severityColor(alert.severity).opacity(0.6) : .clear, lineWidth: 1))
        .contentShape(Rectangle())
        .onTapGesture {
            store.focusedAlert = focused ? nil : alert.id
            select(alert.host)
        }
        .help(focused ? "Clear the highlight" : "Show \(alert.host) below with this episode highlighted on its charts")
    }

    // MARK: Host tiles

    private func hostTiles(_ report: RunnerCapacityReport) -> some View {
        LazyVGrid(columns: [GridItem(.adaptive(minimum: 300), spacing: 12)], spacing: 12) {
            ForEach(store.hosts) { display in
                hostTile(display, alerts: report.alerts.filter { $0.host == display.id })
            }
        }
    }

    private func hostTile(_ display: RunnerCapacityStore.DisplayHost, alerts: [CapacityAlert]) -> some View {
        let host = display.capacity
        let selected = store.selected?.id == display.id
        return Button {
            select(host.host)
        } label: {
            VStack(alignment: .leading, spacing: 8) {
                HStack(spacing: 6) {
                    Circle()
                        .fill(display.isStale || !host.reachable ? Self.critical : tileStatusColor(alerts))
                        .frame(width: 8, height: 8)
                    Text(host.host).font(.headline)
                    Text(host.state.uppercased())
                        .font(.caption2.weight(.bold))
                        .padding(.horizontal, 6).padding(.vertical, 2)
                        .background((host.isActive ? Self.good : Color.secondary).opacity(0.15), in: Capsule())
                    if display.isStale {
                        Text("STALE")
                            .font(.caption2.weight(.bold))
                            .padding(.horizontal, 6).padding(.vertical, 2)
                            .background(Self.warning.opacity(0.18), in: Capsule())
                            .foregroundStyle(Self.warning)
                    }
                    Spacer()
                    Text(host.platform)
                        .font(PressureTheme.monoCaption)
                        .foregroundStyle(.tertiary)
                }
                if let staleSince = display.staleSince {
                    Label("Latest read failed: \(RunnerCapacityStore.describe(display.failure ?? "unreachable")) Showing data from \(PressureFormat.relative(staleSince)).",
                          systemImage: "exclamationmark.triangle.fill")
                        .font(.caption)
                        .foregroundStyle(Self.warning)
                        .fixedSize(horizontal: false, vertical: true)
                }
                if !host.reachable {
                    Text(RunnerCapacityStore.describe(host.error ?? "unreachable"))
                        .font(.caption)
                        .foregroundStyle(.secondary)
                        .fixedSize(horizontal: false, vertical: true)
                } else {
                    let u = host.usage
                    tileRow("CPU", value: u.cpuAvgPct.map { String(format: "%.1f%% avg · %.0f%% peak", $0, u.cpuPeakPct ?? 0) } ?? String(format: "load %.2f (no samples)", host.load1),
                            fraction: (u.cpuAvgPct ?? 0) / 100)
                    tileRow("Heavy slots", value: u.heavyOccupancyPct.map { String(format: "%.1f%% full · peak %d/%d", $0, u.heavyPeak ?? 0, host.heavySlots) } ?? "\(host.heavySlots) slots (no samples)",
                            fraction: (u.heavyOccupancyPct ?? 0) / 100)
                    tileRow("Memory", value: "\(CapacityFormat.gib(host.memAvailableBytes)) free of \(CapacityFormat.gib(host.memTotalBytes))",
                            fraction: host.memUsedFraction)
                    if let disk = host.disk {
                        tileRow("Disk \(disk.mount)", value: String(format: "%.0f%% · %@ free", disk.usedPct, CapacityFormat.bytes(Double(disk.availableBytes))),
                                fraction: disk.usedPct / 100)
                    }
                    HStack(spacing: 12) {
                        stat("\(u.jobs)", "jobs")
                        stat(CapacityFormat.duration(Double(u.busySeconds)), "busy")
                        stat("\(u.failedJobs)", "non-zero", warn: u.failedJobs > 0)
                        stat("\(u.setupFailures)", "setup fail", warn: u.setupFailures > 0)
                        stat(CapacityFormat.duration(u.saturatedSeconds), "saturated", warn: u.saturatedSeconds > 0)
                    }
                    if let net = host.network {
                        Text("\(net.interface): \(CapacityFormat.bytes(net.rxBytesPerDay))/day in · \(CapacityFormat.bytes(net.txBytesPerDay))/day out · \(allowanceLabel(net))")
                            .font(PressureTheme.monoCaption)
                            .foregroundStyle(.secondary)
                            .lineLimit(2)
                    }
                }
            }
            .padding(12)
            .frame(maxWidth: .infinity, alignment: .leading)
            .background(PressureTheme.card, in: RoundedRectangle(cornerRadius: 12, style: .continuous))
            .overlay(
                RoundedRectangle(cornerRadius: 12, style: .continuous)
                    .stroke(selected ? Color.accentColor : PressureTheme.hairline, lineWidth: selected ? 2 : 1)
            )
            .contentShape(RoundedRectangle(cornerRadius: 12, style: .continuous))
        }
        .buttonStyle(.plain)
        .help("Show charts for \(host.host) and its job receipts")
    }

    private func tileRow(_ label: String, value: String, fraction: Double) -> some View {
        VStack(alignment: .leading, spacing: 3) {
            HStack {
                Text(label).font(.caption.weight(.semibold)).foregroundStyle(.secondary)
                Spacer()
                Text(value).font(PressureTheme.monoCaption)
            }
            GeometryReader { geo in
                ZStack(alignment: .leading) {
                    Capsule().fill(Color.primary.opacity(0.08))
                    Capsule().fill(fillColor(fraction))
                        .frame(width: max(3, geo.size.width * min(max(fraction, 0), 1)))
                }
            }
            .frame(height: 4)
        }
    }

    private func stat(_ value: String, _ label: String, warn: Bool = false) -> some View {
        VStack(alignment: .leading, spacing: 0) {
            Text(value)
                .font(.system(.callout, design: .monospaced).weight(.semibold))
                .foregroundStyle(warn ? Self.warning : .primary)
            Text(label).font(.caption2).foregroundStyle(.secondary)
        }
    }

    // MARK: Host detail charts

    @ViewBuilder
    private func hostDetail(_ display: RunnerCapacityStore.DisplayHost, report: RunnerCapacityReport) -> some View {
        let host = display.capacity
        VStack(alignment: .leading, spacing: 16) {
            HStack(alignment: .firstTextBaseline) {
                Text("\(host.host) over the last \(windowLabel(report))")
                    .font(.subheadline.weight(.semibold))
                Spacer()
                if let alert = focusedAlert(for: host) {
                    Button {
                        store.focusedAlert = nil
                    } label: {
                        Label("Highlighting: \(alert.title)", systemImage: "xmark.circle.fill")
                    }
                    .buttonStyle(.borderless)
                    .font(.caption)
                    .foregroundStyle(severityColor(alert.severity))
                    .help("Clear the highlighted alert episode")
                }
            }
            hoverReadout(host)
            if !host.samplesPresent {
                Text("This runner does not record utilization samples (older runner build), so CPU, slot and memory history are not available. Jobs and network still show.")
                    .font(.caption)
                    .foregroundStyle(.secondary)
            } else if host.buckets.isEmpty {
                Text("No samples in this window.")
                    .font(.caption)
                    .foregroundStyle(.secondary)
            } else {
                cpuChart(host)
                slotsChart(host)
                memoryChart(host)
            }
            jobsChart(host, report: report)
            networkSection(host, report: report)
            useTables(host)
        }
    }

    private func chartTitle(_ title: String, _ caption: String) -> some View {
        HStack(alignment: .firstTextBaseline) {
            Text(title).font(.callout.weight(.semibold))
            Text(caption).font(.caption).foregroundStyle(.secondary)
        }
    }

    private func cpuChart(_ host: HostCapacity) -> some View {
        VStack(alignment: .leading, spacing: 6) {
            chartTitle("CPU", "bucket average (area) and 15-second peak (line); dashed rule = 85% spike threshold")
            Chart {
                ForEach(host.buckets) { b in
                    AreaMark(x: .value("Time", b.at), y: .value("Average %", b.cpuAvgPct))
                        .foregroundStyle(Self.info.opacity(0.35))
                    LineMark(x: .value("Time", b.at), y: .value("Peak %", b.cpuPeakPct), series: .value("Series", "peak"))
                        .foregroundStyle(Self.info)
                        .lineStyle(StrokeStyle(lineWidth: 1))
                }
                RuleMark(y: .value("Spike", 85))
                    .foregroundStyle(Self.warning.opacity(0.7))
                    .lineStyle(StrokeStyle(lineWidth: 1, dash: [4, 3]))
                overlays(host)
            }
            .chartYScale(domain: 0...100)
            .chartXScale(domain: xDomain)
            .chartXSelection(value: $hoverDate)
            .chartPlotStyle { $0.clipped() }
            .chartYAxis { AxisMarks(values: [0, 25, 50, 75, 100]) { v in AxisGridLine(); AxisValueLabel { Text("\(v.as(Int.self) ?? 0)%") } } }
            .frame(height: 150)
        }
    }

    private func slotsChart(_ host: HostCapacity) -> some View {
        VStack(alignment: .leading, spacing: 6) {
            chartTitle("Heavy slots", "average busy (area), peak (bars); top rule = \(host.heavySlots) slot capacity")
            Chart {
                ForEach(host.buckets) { b in
                    BarMark(x: .value("Time", b.at, unit: barUnit), y: .value("Peak", b.heavyMax))
                        .foregroundStyle(b.heavyMax >= host.heavySlots ? Self.warning.opacity(0.55) : Color.secondary.opacity(0.25))
                    AreaMark(x: .value("Time", b.at), y: .value("Average", b.heavyAvg))
                        .foregroundStyle(Self.good.opacity(0.5))
                }
                RuleMark(y: .value("Capacity", host.heavySlots))
                    .foregroundStyle(Self.critical.opacity(0.6))
                    .lineStyle(StrokeStyle(lineWidth: 1, dash: [4, 3]))
                overlays(host)
            }
            .chartYScale(domain: 0...Double(max(host.heavySlots, 1)) + 0.5)
            .chartXScale(domain: xDomain)
            .chartXSelection(value: $hoverDate)
            .chartPlotStyle { $0.clipped() }
            .frame(height: 120)
        }
    }

    private func memoryChart(_ host: HostCapacity) -> some View {
        let total = Double(host.memTotalBytes) / 1_073_741_824
        return VStack(alignment: .leading, spacing: 6) {
            chartTitle("Memory available", "minimum per bucket, GiB; dashed rule = 10% pressure line")
            Chart {
                ForEach(host.buckets) { b in
                    LineMark(x: .value("Time", b.at), y: .value("GiB", Double(b.memAvailableMinBytes) / 1_073_741_824))
                        .foregroundStyle(Self.good)
                }
                RuleMark(y: .value("Pressure", total * 0.10))
                    .foregroundStyle(Self.critical.opacity(0.6))
                    .lineStyle(StrokeStyle(lineWidth: 1, dash: [4, 3]))
                overlays(host)
            }
            .chartYScale(domain: 0...max(total, 1))
            .chartXScale(domain: xDomain)
            .chartXSelection(value: $hoverDate)
            .chartPlotStyle { $0.clipped() }
            .frame(height: 110)
        }
    }

    private struct JobSlice: Identifiable {
        let at: Date
        let kind: String
        let count: Int
        var id: String { "\(at.timeIntervalSince1970)-\(kind)" }
    }

    private func jobsChart(_ host: HostCapacity, report: RunnerCapacityReport) -> some View {
        let slices = host.jobHours.flatMap { h in
            [JobSlice(at: h.at, kind: "exit 0", count: max(0, h.jobs - h.failed - h.setup)),
             JobSlice(at: h.at, kind: "non-zero", count: h.failed),
             JobSlice(at: h.at, kind: "setup failure", count: h.setup)]
        }
        return VStack(alignment: .leading, spacing: 6) {
            chartTitle("Jobs per hour", "\(host.usage.jobs) jobs, \(CapacityFormat.duration(Double(host.usage.busySeconds))) of job time")
            if host.jobHours.isEmpty {
                Text("No jobs in this window.").font(.caption).foregroundStyle(.secondary)
            } else {
                Chart {
                    ForEach(slices) { s in
                        BarMark(x: .value("Hour", s.at, unit: .hour), y: .value("Jobs", s.count))
                            .foregroundStyle(by: .value("Result", s.kind))
                    }
                    overlays(host)
                }
                .chartForegroundStyleScale(["exit 0": Self.good.opacity(0.7), "non-zero": Self.warning, "setup failure": Self.critical])
                .chartXScale(domain: xDomain)
                .chartXSelection(value: $hoverDate)
            .chartPlotStyle { $0.clipped() }
                .frame(height: 130)
            }
        }
    }

    @ViewBuilder
    private func networkSection(_ host: HostCapacity, report: RunnerCapacityReport) -> some View {
        if let net = host.network {
            VStack(alignment: .leading, spacing: 6) {
                chartTitle("Network \(net.interface)", net.linkMbps.map { "\($0) Mbit/s port" } ?? "port speed not reported")
                HStack(spacing: 16) {
                    stat(CapacityFormat.bytes(Double(net.rxBytesSinceBoot)), "in since boot")
                    stat(CapacityFormat.bytes(Double(net.txBytesSinceBoot)), "out since boot")
                    if let rx = net.tailnetRxBytes {
                        stat(CapacityFormat.bytes(Double(rx)), "of it via tailnet")
                    }
                    stat(CapacityFormat.bytes(net.projectedMonthBytes), "projected/month")
                    if let pct = net.projectedAllowancePct {
                        stat(String(format: "%.0f%%", pct), "of allowance", warn: pct >= 70)
                    }
                    if let peak = net.peakRxBps {
                        stat(CapacityFormat.rate(max(peak, net.peakTxBps ?? 0)), "peak rate")
                    }
                }
                Text(net.allowance.note)
                    .font(.caption)
                    .foregroundStyle(.secondary)
                if net.series.isEmpty {
                    Text("Bandwidth history builds while this tab is open (one sample a minute, kept 35 days).")
                        .font(.caption)
                        .foregroundStyle(.tertiary)
                } else {
                    Chart {
                        ForEach(net.series) { p in
                            LineMark(x: .value("Time", p.at), y: .value("Mbit/s", p.rxBps * 8 / 1e6), series: .value("Dir", "in"))
                                .foregroundStyle(by: .value("Direction", "in"))
                            LineMark(x: .value("Time", p.at), y: .value("Mbit/s", p.txBps * 8 / 1e6), series: .value("Dir", "out"))
                                .foregroundStyle(by: .value("Direction", "out"))
                        }
                        if let link = net.linkMbps, peakMbps(net) >= Double(link) * 0.25 {
                            RuleMark(y: .value("70% of port", Double(link) * 0.7))
                                .foregroundStyle(Self.warning.opacity(0.6))
                                .lineStyle(StrokeStyle(lineWidth: 1, dash: [4, 3]))
                        }
                        overlays(host)
                    }
                    .chartForegroundStyleScale(["in": Self.info, "out": Self.good])
                    .chartXScale(domain: xDomain)
                    .chartXSelection(value: $hoverDate)
            .chartPlotStyle { $0.clipped() }
                    .chartYAxisLabel("Mbit/s")
                    .frame(height: 130)
                    Text("Covers \(CapacityFormat.duration(Double(net.seriesCoverageSeconds))) of the window (sampled while the app polls).\(net.linkMbps.map { link in peakMbps(net) < Double(link) * 0.25 ? String(format: " Peak is %.1f%% of the %d Mbit/s port, so the port line is hidden.", peakMbps(net) / Double(link) * 100, link) : "" } ?? "")")
                        .font(.caption2)
                        .foregroundStyle(.tertiary)
                }
            }
        }
    }

    private func useTables(_ host: HostCapacity) -> some View {
        HStack(alignment: .top, spacing: 20) {
            useTable("Top callers", host.topCallers)
            useTable("Top repositories", host.topRepos)
        }
    }

    private func useTable(_ title: String, _ rows: [UseRow]) -> some View {
        let maxBusy = Double(rows.map(\.busySeconds).max() ?? 1)
        return VStack(alignment: .leading, spacing: 4) {
            Text(title).font(.callout.weight(.semibold))
            if rows.isEmpty {
                Text("None in this window.").font(.caption).foregroundStyle(.secondary)
            }
            ForEach(rows) { row in
                HStack(spacing: 8) {
                    Text(row.name)
                        .font(PressureTheme.monoCaption)
                        .lineLimit(1)
                        .truncationMode(.middle)
                        .frame(width: 210, alignment: .leading)
                    GeometryReader { geo in
                        Capsule().fill(Self.info.opacity(0.5))
                            .frame(width: max(2, geo.size.width * Double(row.busySeconds) / max(maxBusy, 1)))
                    }
                    .frame(height: 5)
                    Text("\(row.jobs) · \(CapacityFormat.duration(Double(row.busySeconds)))")
                        .font(PressureTheme.monoCaption)
                        .foregroundStyle(.secondary)
                        .frame(width: 90, alignment: .trailing)
                }
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
    }

    // MARK: Helpers

    /// Every time chart shares the report window so their axes line up.
    private var xDomain: ClosedRange<Date> {
        guard let r = store.report, r.capturedAt > r.since else { return Date().addingTimeInterval(-86400)...Date() }
        return r.since...r.capturedAt
    }

    private func peakMbps(_ net: NetworkUse) -> Double {
        max(net.peakRxBps ?? 0, net.peakTxBps ?? 0) * 8 / 1e6
    }

    private var barUnit: Calendar.Component {
        (store.report?.bucketSeconds ?? 0) >= 3600 ? .hour : .minute
    }

    private func allowanceLabel(_ net: NetworkUse) -> String {
        switch net.allowance.kind {
        case "unmetered": return "unmetered"
        case "monthly_included":
            if let pct = net.projectedAllowancePct { return String(format: "%.0f%% of monthly allowance", pct) }
            return "monthly allowance"
        default: return "traffic terms unknown"
        }
    }

    private func tileStatusColor(_ alerts: [CapacityAlert]) -> Color {
        if alerts.contains(where: { $0.severity == "critical" }) { return Self.critical }
        if alerts.contains(where: { $0.severity == "warning" }) { return Self.warning }
        return Self.good
    }

    private func fillColor(_ fraction: Double) -> Color {
        switch fraction {
        case 0.9...: Self.critical
        case 0.75..<0.9: Self.warning
        default: Self.info.opacity(0.8)
        }
    }

    private func severityIcon(_ s: String) -> String {
        switch s {
        case "critical": "exclamationmark.octagon.fill"
        case "warning": "exclamationmark.triangle.fill"
        default: "info.circle.fill"
        }
    }

    private func severityColor(_ s: String) -> Color {
        switch s {
        case "critical": Self.critical
        case "warning": Self.warning
        default: Self.info
        }
    }

    private static let dayTime: DateFormatter = {
        let f = DateFormatter()
        f.dateFormat = "MMM d HH:mm"
        return f
    }()

    private static let timeOnly: DateFormatter = {
        let f = DateFormatter()
        f.dateFormat = "HH:mm"
        return f
    }()

    private func rangeLabel(_ since: Date, _ until: Date?) -> String {
        guard let until else { return Self.dayTime.string(from: since) }
        let sameDay = Calendar.current.isDate(since, inSameDayAs: until)
        return "\(Self.dayTime.string(from: since))–\(sameDay ? Self.timeOnly.string(from: until) : Self.dayTime.string(from: until))"
    }

    private func select(_ host: String) {
        store.selectedHost = host
        onSelectHost(host)
    }

    /// The window the shown report covers, which can lag the picker for the
    /// length of one read.
    private func windowLabel(_ report: RunnerCapacityReport) -> String {
        RunnerCapacityWindow(rawValue: report.window)?.label ?? report.window
    }

    private func focusedAlert(for host: HostCapacity) -> CapacityAlert? {
        guard let id = store.focusedAlert,
              let alert = store.report?.alerts.first(where: { $0.id == id }),
              alert.host == host.host, alert.since != nil else { return nil }
        return alert
    }

    /// Focused alert episode band and the shared hover rule.
    @ChartContentBuilder
    private func overlays(_ host: HostCapacity) -> some ChartContent {
        if let alert = focusedAlert(for: host), let since = alert.since {
            let end = max(alert.until ?? since, since.addingTimeInterval(Double(store.report?.bucketSeconds ?? 300)))
            RectangleMark(xStart: .value("From", since), xEnd: .value("To", end))
                .foregroundStyle(severityColor(alert.severity).opacity(0.14))
        }
        if let hoverDate {
            RuleMark(x: .value("Hover", hoverDate))
                .foregroundStyle(Color.primary.opacity(0.35))
                .lineStyle(StrokeStyle(lineWidth: 1))
        }
    }

    /// One line describing the hovered instant across every series, so a
    /// spike can be read without guessing from the axes.
    @ViewBuilder
    private func hoverReadout(_ host: HostCapacity) -> some View {
        let text = hoverText(host)
        Text(text ?? "Hover a chart to read exact values; tap an alert to highlight its episode.")
            .font(PressureTheme.monoCaption)
            .foregroundStyle(text == nil ? .tertiary : .secondary)
            .lineLimit(2)
            .frame(maxWidth: .infinity, minHeight: 30, alignment: .topLeading)
    }

    private func hoverText(_ host: HostCapacity) -> String? {
        guard let hoverDate else { return nil }
        var parts: [String] = []
        if let b = nearest(host.buckets, to: hoverDate, within: Double(store.report?.bucketSeconds ?? 300)) {
            parts.append(Self.dayTime.string(from: b.at))
            parts.append(String(format: "CPU %.0f%% avg / %.0f%% peak", b.cpuAvgPct, b.cpuPeakPct))
            parts.append(String(format: "iowait %.1f%%", b.iowaitAvgPct))
            parts.append(String(format: "heavy %.1f avg / %d of %d", b.heavyAvg, b.heavyMax, host.heavySlots))
            if b.saturatedSeconds > 0 { parts.append("full \(CapacityFormat.duration(b.saturatedSeconds))") }
            parts.append("mem min \(CapacityFormat.gib(b.memAvailableMinBytes)) free")
        } else {
            parts.append(Self.dayTime.string(from: hoverDate))
        }
        if let hour = host.jobHours.first(where: { $0.at <= hoverDate && hoverDate < $0.at.addingTimeInterval(3600) }) {
            parts.append("\(hour.jobs) jobs that hour (\(hour.failed) non-zero, \(hour.setup) setup)")
        }
        if let net = host.network, let p = nearest(net.series, to: hoverDate, within: 900) {
            parts.append(String(format: "net %.1f in / %.1f out Mbit/s", p.rxBps * 8 / 1e6, p.txBps * 8 / 1e6))
        }
        return parts.joined(separator: " · ")
    }

    private func nearest<T>(_ items: [T], to date: Date, within: TimeInterval) -> T? where T: Identifiable, T.ID == Date {
        // Series are time-ordered: binary search for the insertion point.
        var lo = 0, hi = items.count
        while lo < hi {
            let mid = (lo + hi) / 2
            if items[mid].id < date { lo = mid + 1 } else { hi = mid }
        }
        let candidates = [lo - 1, lo].filter { $0 >= 0 && $0 < items.count }
        guard let best = candidates.min(by: { abs(items[$0].id.timeIntervalSince(date)) < abs(items[$1].id.timeIntervalSince(date)) }),
              abs(items[best].id.timeIntervalSince(date)) <= within else { return nil }
        return items[best]
    }
}
