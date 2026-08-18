import SwiftUI
import NDevPressureCore

struct StorageView: View {
    @EnvironmentObject private var store: PressureStore
    @State private var confirmEnablePolicy = false
    @State private var confirmObservePolicy = false
    @State private var confirmApply = false
    @State private var applyRequest: PressureStore.StorageApplyRequest?

    private var snapshot: StorageSnapshot {
        store.board.snapshot.storage
    }

    var body: some View {
        VStack(spacing: 0) {
            header
            Divider()
            switch store.storageTab {
            case .disk:
                diskReclaim
            case .idle:
                IdleView(embedded: true)
            }
        }
        .background(PressureTheme.bg)
        .navigationTitle("Storage")
        .confirmationDialog(
            applyRequest?.autoSafe == true ? "Apply typed safe reclaim?" : "Apply named storage provider?",
            isPresented: $confirmApply,
            titleVisibility: .visible
        ) {
            if let request = applyRequest {
                Button(request.autoSafe ? "Apply --auto-safe" : "Apply --provider \(request.provider ?? "")", role: .destructive) {
                    applyRequest = nil
                    Task { await store.confirmStorageApply(request) }
                }
            }
            Button("Cancel", role: .cancel) { applyRequest = nil }
        } message: {
            if applyRequest?.autoSafe == true {
                Text(PressureHelp.storageBeginSafeReclaim)
            } else {
                Text("Runs the typed session-pressure storage apply for one named provider. This is not an agent session and cannot take arbitrary argv.")
            }
        }
        .confirmationDialog("Enable storage admission?", isPresented: $confirmEnablePolicy, titleVisibility: .visible) {
            Button("Enable storage policy") {
                Task { await store.enableStoragePolicy() }
            }
            Button("Cancel", role: .cancel) {}
        } message: {
            Text(PressureHelp.storagePolicyEnable)
        }
        .confirmationDialog("Observe storage only?", isPresented: $confirmObservePolicy, titleVisibility: .visible) {
            Button("Storage observe") {
                Task { await store.observeStoragePolicy() }
            }
            Button("Cancel", role: .cancel) {}
        } message: {
            Text("Keeps storage monitoring but turns off --auto-safe admission enforcement. Named provider apply remains available.")
        }
    }

    private var header: some View {
        VStack(alignment: .leading, spacing: 10) {
            HStack {
                VStack(alignment: .leading, spacing: 4) {
                    Text("Storage")
                        .font(.title2.weight(.semibold))
                    Text(store.storageTab == .disk
                         ? "Typed disk reclaim. No Grok, Codex, PTY, or free-text shell."
                         : "Operator-confirmed SIGTERM of old quiet trees. This is RAM, not disk.")
                        .font(.caption)
                        .foregroundStyle(.secondary)
                }
                Spacer()
                if store.storageTab == .disk {
                    Button {
                        Task { await store.refreshStorageInventory() }
                    } label: {
                        Label("Refresh inventory", systemImage: "arrow.clockwise")
                    }
                    .disabled(store.isStorageLoading || store.isStorageApplying)
                    Button {
                        Task { await store.previewSafeReclaim(autoSafe: true) }
                    } label: {
                        Label("Begin safe reclaim", systemImage: "internaldrive.badge.checkmark")
                    }
                    .buttonStyle(.borderedProminent)
                    .disabled(store.isStorageApplying || store.busyAction != nil || !store.canBeginAutoSafeReclaim)
                    .help(store.canBeginAutoSafeReclaim
                          ? PressureHelp.storageBeginSafeReclaim
                          : "No actionable auto_safe provider. Factory-only and blocked rows stay visible below.")
                }
            }
            Picker("Storage surface", selection: $store.storageTab) {
                ForEach(PressureStore.StorageTab.allCases) { tab in
                    Text(tab.rawValue).tag(tab)
                }
            }
            .pickerStyle(.segmented)
            .help("Disk reclaim is typed storage apply. Idle trees is operator-confirmed SIGTERM.")
        }
        .padding(14)
    }

    private var diskReclaim: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 16) {
                if let error = store.storageError {
                    InlineError(message: error)
                }
                capacityCard
                policyCard
                if store.pendingStorageApply != nil {
                    confirmBanner
                }
                tierCard("Tier A — auto_safe", providers: providers(in: .autoSafe), help: PressureHelp.storageAutoSafe)
                tierCard("Tier B — operator", providers: providers(in: .operatorProvider), help: PressureHelp.storageOperator)
                tierCard("Tier C — report only", providers: providers(in: .reportOnly), help: PressureHelp.storageReportOnly)
                receiptCard
            }
            .padding(20)
        }
    }

    private var confirmBanner: some View {
        HStack {
            Text("Preview ready. Read the receipt, then confirm the typed apply.")
                .font(.caption)
            Spacer()
            Button("Confirm apply") {
                applyRequest = store.pendingStorageApply
                confirmApply = true
            }
            .buttonStyle(.borderedProminent)
            .disabled(store.isStorageApplying)
        }
        .padding(12)
        .background(PressureTheme.card, in: RoundedRectangle(cornerRadius: 12, style: .continuous))
    }

    private var capacityCard: some View {
        SectionCard(title: "Capacity", systemImage: "externaldrive", help: PressureHelp.storage) {
            HStack(spacing: 16) {
                LevelBadge(level: snapshot.level)
                VStack(alignment: .leading, spacing: 4) {
                    Text(PressureFormat.bytes(snapshot.availableBytes))
                        .font(.title2.monospaced().weight(.semibold))
                    Text(snapshot.available
                         ? "\(PressureFormat.percent(snapshot.freePercent)) free · \(snapshot.volumePath)"
                         : (snapshot.error ?? "probe unavailable"))
                        .font(.caption)
                        .foregroundStyle(.secondary)
                }
                Spacer()
                if store.isStorageLoading {
                    ProgressView().controlSize(.small)
                }
            }
            if let reason = snapshot.reasons.first {
                Text(reason)
                    .font(.caption)
                    .foregroundStyle(.secondary)
            }
        }
    }

    private var policyCard: some View {
        SectionCard(title: "Storage policy", systemImage: "lock.shield", help: PressureHelp.storagePolicyEnable) {
            Text(storagePolicyCaption)
                .font(.caption)
                .foregroundStyle(.secondary)
            HStack {
                Button("Enable") { confirmEnablePolicy = true }
                    .buttonStyle(.borderedProminent)
                    .disabled(store.busyAction != nil)
                Button("Observe") { confirmObservePolicy = true }
                    .buttonStyle(.bordered)
                    .disabled(store.busyAction != nil)
            }
        }
    }

    private func tierCard(_ title: String, providers: [StorageProviderReport], help: String) -> some View {
        SectionCard(title: title, systemImage: "square.stack.3d.up", help: help) {
            if providers.isEmpty {
                Text("No providers in this tier.")
                    .font(.caption)
                    .foregroundStyle(.secondary)
            } else {
                ForEach(providers) { provider in
                    providerRow(provider)
                }
            }
        }
    }

    private func providerRow(_ provider: StorageProviderReport) -> some View {
        HStack(alignment: .top, spacing: 10) {
            VStack(alignment: .leading, spacing: 3) {
                HStack(spacing: 8) {
                    Text(provider.id)
                        .font(.body.monospaced().weight(.semibold))
                    if provider.isFactoryOnly {
                        StatusChip(label: "factory-only", ok: false)
                    } else if provider.isActionable {
                        StatusChip(label: "actionable", ok: true)
                    } else {
                        StatusChip(label: "blocked", ok: false)
                    }
                }
                Text(provider.summary)
                    .font(.caption)
                    .foregroundStyle(.secondary)
                if let reason = provider.blockedReason, !reason.isEmpty {
                    Text(reason)
                        .font(.caption.monospaced())
                        .foregroundStyle(.orange)
                        .textSelection(.enabled)
                }
            }
            Spacer()
            if provider.estimatedBytes > 0 {
                Text(PressureFormat.bytes(provider.estimatedBytes))
                    .font(PressureTheme.monoCaption)
                    .foregroundStyle(.secondary)
            }
            if provider.tier == .operatorProvider && provider.isActionable {
                Button("Apply…") {
                    Task { await store.previewSafeReclaim(autoSafe: false, provider: provider.id) }
                }
                .disabled(store.isStorageApplying || store.busyAction != nil)
                .help("Preview then confirm typed --provider \(provider.id).")
            }
        }
        .padding(.vertical, 4)
    }

    private var receiptCard: some View {
        SectionCard(title: "Typed apply receipt", systemImage: "text.alignleft", help: PressureHelp.storageReceipt) {
            if store.storageReceipt.isEmpty {
                Text("Receipt lines appear when you preview or confirm a typed apply. This is the command's own output, not an agent terminal.")
                    .font(.caption)
                    .foregroundStyle(.secondary)
            } else {
                VStack(alignment: .leading, spacing: 4) {
                    ForEach(store.storageReceipt) { line in
                        Text(line.text)
                            .font(.caption.monospaced())
                            .foregroundStyle(receiptColor(line.kind))
                            .textSelection(.enabled)
                    }
                }
            }
            if store.isStorageApplying {
                ProgressView("Streaming typed apply…")
                    .controlSize(.small)
            }
        }
    }

    private func receiptColor(_ kind: StorageReceiptLine.Kind) -> Color {
        switch kind {
        case .error, .blocked: return PressureTheme.levelColor(.warning)
        case .command, .status: return .secondary
        case .result: return PressureTheme.levelColor(.normal)
        case .stdout, .provider: return .primary
        }
    }

    private func providers(in tier: StorageReclaim.Tier) -> [StorageProviderReport] {
        store.storageProviders.filter { $0.tier == tier }
    }

    private var storagePolicyCaption: String {
        guard let policy = store.storagePolicy else {
            return "Storage policy not loaded yet. Refresh inventory. The CLI is still the apply authority."
        }
        return policy.enforceAdmission
            ? "Enforcement on. --auto-safe apply is allowed after confirmation."
            : "Enforcement off. Enable before Begin safe reclaim can mutate. Named provider apply remains available."
    }
}
