import SwiftUI
import NDevPressureCore

struct ProcessIncidentsView: View {
    @EnvironmentObject private var store: PressureStore
    @State private var report: ProcessIncidentReport?
    @State private var selectedID: String?
    @State private var pending: ProcessIncident?
    @State private var force = false
    @State private var busy = false
    @State private var refreshing = false
    @State private var error: String?
    @State private var refreshedAt: Date?

    private var incidents: [ProcessIncident] { report?.incidents ?? [] }
    private var selected: ProcessIncident? { incidents.first { $0.id == selectedID } }

    var body: some View {
        VStack(alignment: .leading, spacing: 14) {
            HStack {
                Text("Process Incidents").font(.largeTitle.bold())
                Spacer()
                if busy || refreshing { ProgressView().controlSize(.small) }
                Button("Refresh") { Task { await refresh() } }.disabled(busy || refreshing)
            }
            Text("Inspect runaway work, stop a selected process, and keep its incident history.")
                .foregroundStyle(.secondary)
            if let error { Text(error).foregroundStyle(.red).textSelection(.enabled) }
            HStack {
                Label("Detection sends alerts. Stop requires your confirmation.", systemImage: "checkmark.shield")
                Spacer()
                if let at = refreshedAt { Text("Read \(at, style: .relative) ago").font(.caption) }
            }.help(PressureHelp.processIncidents)
            HSplitView {
                List(selection: $selectedID) {
                    ForEach(incidents) { incident in
                        VStack(alignment: .leading, spacing: 5) {
                            HStack {
                                Text(incident.executable).font(.headline)
                                Spacer()
                                Text(incident.state.replacingOccurrences(of: "_", with: " ")).font(.caption)
                                    .foregroundStyle(incident.canStop ? Color.orange : Color.secondary)
                            }
                            Text("PID \(incident.pid) · \(incident.reasonLabel)")
                                .font(.caption).foregroundStyle(.secondary)
                            Text(incident.lastSeen, style: .date).font(.caption2)
                        }.padding(.vertical, 5).tag(incident.id)
                    }
                }.frame(minWidth: 290, idealWidth: 360)
                ScrollView {
                    if let incident = selected {
                        details(incident).padding(18)
                    } else {
                        ContentUnavailableView(incidents.isEmpty ? "No process incidents" : "Select an incident", systemImage: "waveform.path.ecg", description: Text("The resident monitor records sustained CPU incidents here. Short CPU spikes do not create incidents."))
                    }
                }.frame(minWidth: 400, maxWidth: .infinity, maxHeight: .infinity)
            }
            DisclosureGroup("Detection and scan guard") {
                VStack(alignment: .leading, spacing: 8) {
                    Text(PressureHelp.processIncidents)
                    Text("Bound new scans to a narrow root and a time limit:")
                    Text("session-pressure processes run --timeout 2m -- rg --files /path/to/project")
                        .font(.system(.caption, design: .monospaced)).textSelection(.enabled)
                    Text("History retains the latest 200 incidents. Command arguments and output are not stored.")
                }.font(.caption).foregroundStyle(.secondary).padding(.top, 6)
            }
        }
        .padding(20)
        .background(PressureTheme.bg)
        .task {
            while !Task.isCancelled {
                if store.processIncidentsRefreshAllowed { await refresh() }
                do { try await Task.sleep(for: .seconds(10)) } catch { return }
            }
        }
        .confirmationDialog(force ? "Force stop this process?" : "Stop this process?", isPresented: Binding(get: { pending != nil }, set: { if !$0 { pending = nil } }), titleVisibility: .visible) {
            if let incident = pending {
                Button(force ? "Force Stop PID \(incident.pid)" : "Stop PID \(incident.pid)", role: .destructive) {
                    Task { await stop(incident, force: force) }
                }
            }
            Button("Cancel", role: .cancel) { pending = nil }
        } message: {
            if let incident = pending {
                Text("\(incident.executable), PID \(incident.pid). \(force ? "KILL ends it immediately and can lose unsaved work." : "TERM asks it to exit and can interrupt its work.") The control checks its kernel identity and records the result.")
            }
        }
    }

    private func details(_ incident: ProcessIncident) -> some View {
        VStack(alignment: .leading, spacing: 12) {
            Text(incident.executable).font(.title2.bold())
            LabeledContent("State", value: incident.state.replacingOccurrences(of: "_", with: " "))
            if incident.source == "operator_record" { Text("Historical exit reported by an operator. Kernel identity was not available for this record.").font(.caption).foregroundStyle(.secondary) }
            LabeledContent("Process", value: incident.source == "operator_record" ? "PID \(incident.pid) · owner not captured" : "PID \(incident.pid) · user \(incident.uid)")
            LabeledContent("Parent", value: "\(incident.parentExecutable ?? "unknown") · PID \(incident.ppid ?? 0)")
            LabeledContent("Incident", value: incident.id)
            if !incident.startIdentity.isEmpty { LabeledContent("Kernel identity", value: incident.startIdentity) }
            if incident.source != "operator_record" {
            if let age = incident.ageSeconds { LabeledContent("Age at observation", value: "\(age / 60) minutes") }
            if let cpu = incident.cpuPercent { LabeledContent("CPU at observation", value: String(format: "%.1f%% of one core", cpu)) }
            if let memory = incident.rssMb { LabeledContent("Memory at observation", value: String(format: "%.0f MiB", memory)) }
            if let hot = incident.sustainedSeconds { LabeledContent("Sustained CPU", value: "\(hot) seconds") }
            }
            LabeledContent("First evidence", value: incident.firstSeen.formatted(date: .abbreviated, time: .standard))
            LabeledContent("Last evidence", value: incident.lastSeen.formatted(date: .abbreviated, time: .standard))
            if let session = incident.sessionID { LabeledContent("Session", value: session).textSelection(.enabled) }
            HStack {
                Button("Stop", role: .destructive) { force = false; pending = incident }
                    .disabled(!incident.canStop || busy)
                if incident.canForceStop {
                    Button("Force Stop…", role: .destructive) { force = true; pending = incident }.disabled(busy)
                }
            }
            Divider()
            Text("Action history").font(.headline)
            if incident.actions?.isEmpty ?? true { Text("No stop action requested.").foregroundStyle(.secondary) }
            ForEach(Array((incident.actions ?? []).enumerated()), id: \.offset) { _, action in
                VStack(alignment: .leading, spacing: 3) {
                    Text("\(action.signalLabel) · \(action.result.replacingOccurrences(of: "_", with: " "))")
                    Text(action.at.formatted(date: .abbreviated, time: .standard)).font(.caption).foregroundStyle(.secondary)
                    if let error = action.error { Text(error).font(.caption).foregroundStyle(.red) }
                }
            }
        }.textSelection(.enabled)
    }

    @MainActor private func refresh() async {
        guard !busy && !refreshing else { return }
        refreshing = true
        defer { refreshing = false }
        do {
            let client = try NDevPressureClient(binaryPath: store.binaryPath.isEmpty ? nil : store.binaryPath)
            report = try await client.processIncidents().report
            refreshedAt = Date(); error = nil
            if !incidents.contains(where: { $0.id == selectedID }) { selectedID = incidents.first?.id }
        } catch { self.error = error.localizedDescription }
    }

    @MainActor private func stop(_ incident: ProcessIncident, force: Bool) async {
        guard !busy else { return }
        busy = true; pending = nil
        do {
            let client = try NDevPressureClient(binaryPath: store.binaryPath.isEmpty ? nil : store.binaryPath)
            let response = try await client.stopProcessIncident(id: incident.id, force: force)
            if !response.ok { throw NDevPressureClientError.launchFailed("Stop failed; refresh incident history.") }
            error = nil
        } catch { self.error = error.localizedDescription }
        busy = false
        // Preserve a mutation error while refreshing the history.
        let mutationError = error
        await refresh()
        if let mutationError { error = mutationError }
    }
}
