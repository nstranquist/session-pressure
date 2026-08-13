import AppKit
import SwiftUI
import NDevPressureCore

struct MonitorView: View {
    @EnvironmentObject private var store: PressureStore
    @State private var confirmInstall = false
    @State private var confirmUninstall = false

    private var launchd: LaunchdStatus? { store.board.launchd }
    private var health: StatusHealth? { store.board.health }
    private var snap: PressureSnapshot { store.board.snapshot }

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 16) {
                if store.board.hasRecoveryHint {
                    SectionCard(title: "Recovery hint", systemImage: "arrow.uturn.backward.circle") {
                        if let recovery = store.board.recoveryHint {
                            labeled("Detected", PressureFormat.relative(recovery.detectedAt))
                            labeled("Last sample", PressureFormat.relative(recovery.lastSampleAt))
                            if let level = recovery.lastLevel {
                                labeled("Last level", level.displayName)
                            }
                            if !recovery.reason.isEmpty {
                                Text(recovery.reason)
                                    .font(.caption)
                                    .foregroundStyle(.secondary)
                            }
                            if !recovery.recoveryCommand.isEmpty {
                                Text(recovery.recoveryCommand)
                                    .font(PressureTheme.monoCaption)
                                    .textSelection(.enabled)
                                Button("Copy recovery command") {
                                    NSPasteboard.general.clearContents()
                                    NSPasteboard.general.setString(recovery.recoveryCommand, forType: .string)
                                }
                                .buttonStyle(.bordered)
                                .help("Copy the evidence-centered command for manual review. The app never executes recovery automatically.")
                            }
                        } else {
                            Text("A recovery hint exists; refresh to load its details.")
                                .foregroundStyle(.secondary)
                        }
                    }
                }

                SectionCard(title: "LaunchAgent", systemImage: "launchpad") {
                    if let launchd {
                        LazyVGrid(columns: [GridItem(.adaptive(minimum: 180), spacing: 10)], spacing: 10) {
                            StatusChip(label: "Installed", ok: launchd.installed)
                            StatusChip(label: "Loaded", ok: launchd.loaded)
                            StatusChip(label: "Artifact verified", ok: launchd.artifactVerified ?? false)
                            StatusChip(label: "PID", ok: (launchd.pid ?? 0) > 0, detail: PressureFormat.pid(launchd.pid))
                        }

                        VStack(alignment: .leading, spacing: 6) {
                            labeled("Label", launchd.label ?? "—")
                            labeled("Plist", launchd.plistPath ?? "—")
                            labeled("Artifact", launchd.artifactPath ?? "—")
                            labeled("SHA-256", shortHash(launchd.artifactSHA256))
                            labeled("Installed", PressureFormat.relative(launchd.artifactInstalledAt))
                        }
                        .padding(.top, 8)

                        HStack(spacing: 10) {
                            Button("Install monitor") { confirmInstall = true }
                                .buttonStyle(.borderedProminent)
                                .disabled(store.busyAction != nil)
                                .help(PressureHelp.monitorInstall)
                            Button("Sample once") {
                                Task { await store.sampleOnce() }
                            }
                            .buttonStyle(.bordered)
                            .disabled(store.busyAction != nil)
                            .help(PressureHelp.monitorOnce)
                            Button("Uninstall", role: .destructive) { confirmUninstall = true }
                                .disabled(store.busyAction != nil)
                                .help(PressureHelp.monitorUninstall)
                        }
                        .padding(.top, 10)
                    } else {
                        Text("Monitor status unavailable.")
                            .foregroundStyle(.secondary)
                    }
                }

                SectionCard(title: "Resident health", systemImage: "waveform.path.ecg") {
                    if let health {
                        LazyVGrid(columns: [GridItem(.adaptive(minimum: 160), spacing: 10)], spacing: 10) {
                            metric("Protection", health.protectionMode.replacingOccurrences(of: "_", with: " "))
                            metric("Healthy", health.monitorHealthy ? "yes" : "no")
                            metric("Daily driver", health.dailyDriverReady ? "ready" : "not ready")
                            metric("Operator", health.operatorReady ? "ready" : "attention")
                            metric("Freshness", health.latestMonitorFresh ? "fresh" : "stale")
                            metric("Age", health.latestMonitorAgeSeconds.map { String(format: "%.0fs", $0) } ?? "—")
                            metric("Samples", "\(health.residentSamples ?? 0)")
                            metric("Normal samples", "\(health.residentNormalSamples ?? 0)/\(health.requiredNormalSamples ?? 4)")
                        }
                        if !health.operatorReasons.isEmpty {
                            VStack(alignment: .leading, spacing: 4) {
                                ForEach(health.operatorReasons, id: \.self) { reason in
                                    InlineError(message: reason)
                                }
                            }
                            .padding(.top, 8)
                        }
                    }

                    Divider().padding(.vertical, 6)

                    LazyVGrid(columns: [GridItem(.adaptive(minimum: 160), spacing: 10)], spacing: 10) {
                        metric("Guard RSS", PressureFormat.mb(snap.guardRSSMB))
                        metric("Peak RSS", snap.guardPeakRSSMB.map { PressureFormat.mb($0) } ?? "—")
                        metric("Sample wall", String(format: "%.1f ms", snap.sampleDurationMS))
                        metric("Sample CPU", String(format: "%.1f ms", snap.sampleCPUTimeMS))
                        metric("Duty", snap.guardCPUDutyPercent.map { PressureFormat.percent($0) } ?? "—")
                        metric("Telemetry today", PressureFormat.bytes(snap.telemetryBytesToday))
                        metric("Projected / day", PressureFormat.bytes(snap.telemetryProjectedBytesPerDay))
                        metric("Budget", snap.guardBudgetOK ? "ok" : "breach")
                    }

                    if !snap.guardBudgetReasons.isEmpty {
                        VStack(alignment: .leading, spacing: 4) {
                            ForEach(snap.guardBudgetReasons, id: \.self) { reason in
                                InlineError(message: reason)
                            }
                        }
                        .padding(.top, 8)
                    }
                }

                SectionCard(title: "Binary", systemImage: "terminal") {
                    labeled("ndev", store.binaryPath.isEmpty ? "—" : store.binaryPath)
                    Text("This app shells out to ndev JSON contracts and never reimplements sampling.")
                        .font(.caption)
                        .foregroundStyle(.secondary)
                }
            }
            .padding(20)
        }
        .background(PressureTheme.bg)
        .confirmationDialog("Install session-pressure monitor?", isPresented: $confirmInstall, titleVisibility: .visible) {
            Button("Install (respect current policy)") {
                Task { await store.installMonitor(enforce: false) }
            }
            Button("Install with enforce") {
                Task { await store.installMonitor(enforce: true) }
            }
            Button("Cancel", role: .cancel) {}
        } message: {
            Text("Installs the low-priority user LaunchAgent com.nicos.session-pressure using the digest-verified helper artifact.")
        }
        .confirmationDialog("Uninstall monitor?", isPresented: $confirmUninstall, titleVisibility: .visible) {
            Button("Uninstall", role: .destructive) {
                Task { await store.uninstallMonitor() }
            }
            Button("Cancel", role: .cancel) {}
        } message: {
            Text("Stops and removes the LaunchAgent, forces observe-only policy on incomplete uninstall, and keeps telemetry.")
        }
    }

    private func labeled(_ title: String, _ value: String) -> some View {
        HStack(alignment: .top) {
            Text(title)
                .font(.caption.weight(.semibold))
                .foregroundStyle(.secondary)
                .frame(width: 88, alignment: .leading)
            Text(value)
                .font(PressureTheme.monoCaption)
                .textSelection(.enabled)
                .frame(maxWidth: .infinity, alignment: .leading)
        }
    }

    private func metric(_ title: String, _ value: String) -> some View {
        VStack(alignment: .leading, spacing: 4) {
            Text(title.uppercased())
                .font(.caption2.weight(.semibold))
                .foregroundStyle(.secondary)
            Text(value)
                .font(.callout.monospaced())
        }
        .padding(10)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(Color.primary.opacity(0.04), in: RoundedRectangle(cornerRadius: 8, style: .continuous))
    }

    private func shortHash(_ hash: String?) -> String {
        guard let hash, !hash.isEmpty else { return "—" }
        if hash.count <= 16 { return hash }
        return String(hash.prefix(12)) + "…"
    }
}
