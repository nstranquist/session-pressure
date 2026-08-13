import AppKit
import SwiftUI
import NDevPressureCore

struct WorkView: View {
    @EnvironmentObject private var store: PressureStore

    private var work: WorkStatus? { store.board.work }

    var body: some View {
        HSplitView {
            queuePane
                .frame(minWidth: 480, idealWidth: 640)
            if store.workSelection != nil {
                WorkDetailDrawer()
                    .frame(minWidth: 320, idealWidth: 380, maxWidth: 480)
            }
        }
        .background(PressureTheme.bg)
    }

    private var queuePane: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 16) {
                if let work {
                    SectionCard(title: "Shared capacity", systemImage: "cpu") {
                        CapacityBar(used: work.used, capacity: work.capacity, available: work.available)
                        if let limits = store.board.policy?.workLimits {
                            LazyVGrid(columns: [GridItem(.adaptive(minimum: 120), spacing: 8)], spacing: 8) {
                                weightChip("express-test", limits.expressTestWeight)
                                weightChip("test", limits.testWeight)
                                weightChip("express-build", limits.expressBuildWeight)
                                weightChip("build", limits.buildWeight)
                                weightChip("browser", limits.browserWeight)
                                weightChip("emulator", limits.emulatorWeight)
                                weightChip("heavy", limits.heavyWeight)
                                weightChip("benchmark", limits.benchmarkWeight ?? limits.capacity)
                                weightChip("bench-exclusive", limits.capacity)
                            }
                            .padding(.top, 8)
                        }
                        HStack(spacing: 8) {
                            if store.workFocusPollActive {
                                Label("Queue live \(Int(store.workFocusPollInterval.rounded()))s", systemImage: "waveform.path.ecg")
                                    .font(.caption2.weight(.semibold))
                                    .foregroundStyle(PressureTheme.levelColor(.normal))
                                    .help("Visible active work polls about every 2.5s, an empty queue backs off to 10s, and inactive or minimized windows do not spawn status reads.")
                            }
                            if store.isWorkFocusRefreshing {
                                ProgressView().controlSize(.mini)
                            }
                            Spacer()
                        }
                        .padding(.top, 6)
                        Text("Waiters: Run now promotes one live queue item, Run all pins the whole queue in order (neither preempts leases).\nLeases are already executing — click a row for lifecycle detail and where process output lives.\nndev session pressure work run --class express-test -- go test ./pkg\nndev session pressure work override --all --confirm")
                            .font(PressureTheme.monoCaption)
                            .foregroundStyle(.tertiary)
                            .textSelection(.enabled)
                            .padding(.top, 4)
                    }

                    SectionCard(title: "Scheduler decision", systemImage: "arrow.triangle.branch") {
                        HStack {
                            Text(work.schedulingPolicy ?? "unknown")
                                .font(.callout.weight(.semibold))
                            if let schema = work.selectorSchemaVersion {
                                Text("selector v\(schema)")
                                    .font(PressureTheme.monoCaption)
                                    .foregroundStyle(.secondary)
                            }
                            Spacer()
                            if work.protectedOperationID != nil {
                                Label("protected head", systemImage: "shield.fill")
                                    .font(.caption.weight(.semibold))
                                    .foregroundStyle(PressureTheme.levelColor(.warning))
                            }
                            if work.overrideOperationID != nil {
                                Label("operator override", systemImage: "person.badge.key.fill")
                                    .font(.caption.weight(.semibold))
                                    .foregroundStyle(PressureTheme.levelColor(.red))
                            }
                        }
                        Text(work.decisionReason ?? "No queued selection decision.")
                            .font(.caption)
                            .foregroundStyle(.secondary)
                        if work.decisionReason == "protected_bounded_drain" {
                            Text("Admissions are draining capacity for the protected head; smaller arrivals cannot keep bypassing it.")
                                .font(.caption)
                                .foregroundStyle(PressureTheme.levelColor(.warning))
                        }
                    }

                    // Held work sits above leases and waiters because it is the
                    // state that used to be invisible: blocked before the queue,
                    // holding no capacity, and therefore absent from both lists.
                    if work.admissionHoldCount > 0 {
                        SectionCard(
                            title: "Held at host-pressure gate",
                            systemImage: "hand.raised.fill",
                            trailing: "\(work.admissionHoldCount)"
                        ) {
                            Text("Blocked before reaching the queue. These hold no capacity, so they do not appear in leases or waiters.")
                                .font(.caption)
                                .foregroundStyle(.secondary)
                                .padding(.bottom, 4)
                            VStack(spacing: 0) {
                                ForEach(work.admissionHolds) { hold in
                                    admissionHoldRow(hold)
                                    Divider().opacity(0.4)
                                }
                            }
                            if let latch = work.admissionLatch, latch.latched {
                                Text("CPU latch engaged — recovery \(latch.recoverySamples)/\(max(latch.releaseRequired, 1)) samples. \(latch.reason ?? "")")
                                    .font(.caption)
                                    .foregroundStyle(PressureTheme.levelColor(.warning))
                                    .padding(.top, 6)
                            }
                            if work.available > 0 {
                                Text("\(work.available) of \(work.capacity) weighted capacity is idle while this work is held.")
                                    .font(.caption.weight(.medium))
                                    .foregroundStyle(PressureTheme.levelColor(.red))
                                    .padding(.top, 4)
                            }
                        }
                    }

                    SectionCard(
                        title: "Active leases",
                        systemImage: "lock.fill",
                        trailing: "\(work.leases.count)"
                    ) {
                        if work.leases.isEmpty {
                            Text("No capacity held.")
                                .foregroundStyle(.secondary)
                        } else {
                            VStack(spacing: 0) {
                                ForEach(work.leases) { lease in
                                    leaseRow(lease)
                                    Divider().opacity(0.4)
                                }
                            }
                        }
                    }

                    SectionCard(
                        title: "Waiters",
                        systemImage: "hourglass",
                        trailing: "\(work.queueDepth)"
                    ) {
                        if work.waiters.isEmpty {
                            Text("Queue empty.")
                                .foregroundStyle(.secondary)
                        } else {
                            runAllRow(work)
                            VStack(spacing: 0) {
                                ForEach(work.waiters) { waiter in
                                    waiterRow(waiter)
                                    Divider().opacity(0.4)
                                }
                            }
                        }
                    }
                } else {
                    EmptyHint(title: "Work status unavailable", systemImage: "rectangle.stack")
                }
            }
            .padding(20)
        }
    }

    /// Bulk promotion for the whole queue. `--all` is one confirmed request
    /// resolved inside the coordinator's state lock, so the pinned order is the
    /// queue as it actually was, not the snapshot this view last rendered.
    @ViewBuilder
    private func runAllRow(_ work: WorkStatus) -> some View {
        let pinned = work.overrideQueueDepth
        let allPinned = pinned >= work.waiters.count
        // Below the sequence schema the coordinator fails --all closed. Say so
        // here instead of letting the click produce an error, and leave Run now
        // enabled because a single promotion is exactly what that state carries.
        let sequenceSupported = work.supportsOverrideSequence
        HStack(spacing: 10) {
            if !sequenceSupported {
                Label("Run all unavailable on this host's work state", systemImage: "exclamationmark.triangle")
                    .font(.caption.weight(.semibold))
                    .foregroundStyle(PressureTheme.levelColor(.warning))
                Text("single-slot override until the queue drains — Run now still works")
                    .font(.caption)
                    .foregroundStyle(.secondary)
            } else if pinned > 0 {
                Label("\(pinned) of \(work.waiters.count) pinned", systemImage: "bolt.fill")
                    .font(.caption.weight(.semibold))
                    .foregroundStyle(PressureTheme.levelColor(.red))
                Text("draining in pinned order")
                    .font(.caption)
                    .foregroundStyle(.secondary)
            } else {
                Text("Promote the whole queue in its current order.")
                    .font(.caption)
                    .foregroundStyle(.secondary)
            }
            Spacer(minLength: 8)
            if pinned > 0 {
                Button {
                    Task { await store.clearWorkOverride() }
                } label: {
                    Label("Clear pins", systemImage: "xmark.circle")
                }
                .buttonStyle(.bordered)
                .controlSize(.small)
                .disabled(store.busyAction != nil)
                .help(PressureHelp.workOverrideClear)
            }
            Button {
                Task { await store.overrideAllWork() }
            } label: {
                Label(allPinned ? "All pinned" : "Run all", systemImage: "forward.end.fill")
            }
            .buttonStyle(.borderedProminent)
            .controlSize(.small)
            .disabled(store.busyAction != nil || allPinned || !sequenceSupported)
            .help(sequenceSupported
                ? PressureHelp.workOverrideAll
                : "This host's persisted work state still uses a single-slot override, so an ordered sequence cannot be recorded yet. It advances on its own once the queue drains. Run now promotes one waiter and works today.")
        }
        .padding(.bottom, 4)
    }

    private func weightChip(_ name: String, _ weight: Int?) -> some View {
        HStack {
            Text(name)
                .font(.caption.weight(.medium))
            Spacer()
            Text(weight.map(String.init) ?? "—")
                .font(PressureTheme.monoCaption)
                .foregroundStyle(.secondary)
        }
        .padding(.horizontal, 10)
        .padding(.vertical, 6)
        .background(Color.primary.opacity(0.05), in: RoundedRectangle(cornerRadius: 8, style: .continuous))
    }

    private func admissionHoldRow(_ hold: WorkAdmissionHold) -> some View {
        HStack(spacing: 10) {
            Text(hold.className)
                .font(.callout.weight(.medium))
                .frame(width: 118, alignment: .leading)
            Text("w\(hold.weight)")
                .font(PressureTheme.monoCaption)
                .foregroundStyle(.secondary)
            if let dimension = hold.dimension, !dimension.isEmpty {
                Text(dimension)
                    .font(.caption2.weight(.semibold))
                    .padding(.horizontal, 6)
                    .padding(.vertical, 2)
                    .background(PressureTheme.levelFill(.warning), in: Capsule())
            }
            Text(hold.reason ?? "waiting for host pressure to clear")
                .font(.caption)
                .foregroundStyle(.secondary)
                .lineLimit(1)
            Spacer()
            Text("pid \(PressureFormat.pid(hold.pid))")
                .font(PressureTheme.monoCaption)
            Text(PressureFormat.durationMS(hold.heldForMS))
                .font(.caption.weight(.semibold))
                .foregroundStyle(hold.heldForMS > 30_000 ? PressureTheme.levelColor(.red) : .secondary)
                .frame(width: 72, alignment: .trailing)
        }
        .padding(.vertical, 7)
        .help("Held at the host-pressure admission gate for \(PressureFormat.durationMS(hold.heldForMS)). It has not entered the weighted queue and holds no capacity.")
    }

    private func leaseRow(_ lease: WorkLease) -> some View {
        let selected = {
            if case .lease(let current) = store.workSelection {
                return current.id == lease.id || current.operationID == lease.operationID
            }
            return false
        }()
        return HStack(spacing: 8) {
            Text(lease.className)
                .font(.callout.weight(.semibold))
                .padding(.horizontal, 8)
                .padding(.vertical, 3)
                .background(PressureTheme.levelFill(.normal), in: Capsule())
            Text("weight \(lease.weight)")
                .font(PressureTheme.monoCaption)
                .foregroundStyle(.secondary)
            if lease.review {
                Label("review", systemImage: "exclamationmark.triangle.fill")
                    .font(.caption2.weight(.semibold))
                    .foregroundStyle(PressureTheme.levelColor(.warning))
                    .help(lease.reviewReason ?? "Finite lease exceeded review age")
            }
            Spacer()
            Text("running")
                .font(.caption2.weight(.semibold))
                .foregroundStyle(PressureTheme.levelColor(.normal))
            Text("pid \(PressureFormat.pid(lease.pid))")
                .font(PressureTheme.monoCaption)
            Text(PressureFormat.relative(lease.startedAt))
                .font(.caption)
                .foregroundStyle(.secondary)
                .frame(width: 72, alignment: .trailing)
            Image(systemName: "chevron.right")
                .font(.caption2.weight(.semibold))
                .foregroundStyle(.tertiary)
        }
        .padding(.vertical, 8)
        .padding(.horizontal, 4)
        .background(selected ? PressureTheme.levelFill(.normal).opacity(0.55) : Color.clear, in: RoundedRectangle(cornerRadius: 8, style: .continuous))
        .contentShape(Rectangle())
        .onTapGesture {
            store.selectWorkItem(.lease(lease))
        }
        .help("\(PressureHelp.workLease)\nclass \(lease.className) · weight \(lease.weight) · pid \(PressureFormat.pid(lease.pid)) · tap for detail")
    }

    private func waiterRow(_ waiter: WorkWaiter) -> some View {
        let overridden = work?.overrideOperationID == waiter.operationID
        let selected = {
            if case .waiter(let current) = store.workSelection {
                return current.operationID == waiter.operationID
            }
            return false
        }()
        return HStack(spacing: 8) {
            HStack(spacing: 8) {
                Text("#\(waiter.position ?? 0)")
                    .font(PressureTheme.monoCaption)
                    .foregroundStyle(.tertiary)
                    .frame(width: 28, alignment: .leading)
                Text(waiter.className)
                    .font(.callout.weight(.semibold))
                Text("weight \(waiter.weight)")
                    .font(PressureTheme.monoCaption)
                    .foregroundStyle(.secondary)
                Spacer(minLength: 8)
                if waiter.protected {
                    Label("protected", systemImage: "shield.fill")
                        .font(.caption2.weight(.semibold))
                        .foregroundStyle(PressureTheme.levelColor(.warning))
                } else if waiter.bypassCount > 0 {
                    Text("bypassed \(waiter.bypassCount)")
                        .font(.caption2)
                        .foregroundStyle(.secondary)
                }
                Text("pid \(PressureFormat.pid(waiter.pid))")
                    .font(PressureTheme.monoCaption)
                Text(PressureFormat.relative(waiter.queuedAt))
                    .font(.caption)
                    .foregroundStyle(.secondary)
                    .frame(width: 72, alignment: .trailing)
                Image(systemName: "chevron.right")
                    .font(.caption2.weight(.semibold))
                    .foregroundStyle(.tertiary)
            }
            .contentShape(Rectangle())
            .onTapGesture {
                store.selectWorkItem(.waiter(waiter))
            }

            if overridden {
                Label("next", systemImage: "bolt.fill")
                    .font(.caption2.weight(.semibold))
                    .foregroundStyle(PressureTheme.levelColor(.red))
                    .frame(width: 96, alignment: .trailing)
                    .help("Active head of the operator promotion sequence. It runs as soon as pressure and capacity gates open.")
            } else if waiter.isOverrideQueued {
                // Already pinned behind the head. Offering "Run now" here would
                // let one click replace the whole sequence the operator just
                // confirmed, which is the opposite of what the row implies.
                Label("pinned #\(waiter.overridePosition)", systemImage: "bolt.horizontal.fill")
                    .font(.caption2.weight(.semibold))
                    .foregroundStyle(PressureTheme.levelColor(.warning))
                    .frame(width: 96, alignment: .trailing)
                    .help("Position \(waiter.overridePosition) in the confirmed promotion sequence. It inherits the reservation when the entries ahead of it acquire.")
            } else if waiter.isPressureReserved {
                // This operation already won its turn and is parked on host
                // pressure. Promoting it changes nothing, so offering "Run now"
                // would be a control that cannot do what it says.
                Label("held", systemImage: "pause.circle.fill")
                    .font(.caption2.weight(.semibold))
                    .foregroundStyle(PressureTheme.levelColor(.warning))
                    .frame(width: 96, alignment: .trailing)
                    .help("Reserved and waiting for host pressure to clear. It keeps its place; Run now cannot start it sooner.")
            } else {
                Button {
                    Task { await store.overrideWork(waiter: waiter) }
                } label: {
                    Label("Run now", systemImage: "play.fill")
                }
                .buttonStyle(.borderedProminent)
                .controlSize(.small)
                .disabled(store.busyAction != nil)
                .help(PressureHelp.workOverride)
                .frame(width: 96, alignment: .trailing)
            }
        }
        .padding(.vertical, 8)
        .padding(.horizontal, 4)
        .background(selected ? PressureTheme.levelFill(.warning).opacity(0.35) : Color.clear, in: RoundedRectangle(cornerRadius: 8, style: .continuous))
        .help("\(PressureHelp.workWaiter)\nclass \(waiter.className) · weight \(waiter.weight) · position \(waiter.position ?? 0) · wait \(PressureFormat.durationMS(waiter.waitMS))\(waiter.protectionReason.map { " · \($0)" } ?? "") · tap for detail")
    }
}

// MARK: - Detail drawer

struct WorkDetailDrawer: View {
    @EnvironmentObject private var store: PressureStore

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            header
            Divider()
            ScrollView {
                VStack(alignment: .leading, spacing: 14) {
                    if let selection = store.workSelection {
                        identityCard(selection)
                        actionsCard(selection)
                        outputCard
                        lifecycleCard
                    }
                }
                .padding(14)
            }
        }
        .background(.ultraThinMaterial)
        .help(PressureHelp.workDetail)
    }

    private var header: some View {
        HStack {
            Label("Work detail", systemImage: "doc.text.magnifyingglass")
                .font(.headline)
            Spacer()
            if store.isWorkDetailLoading {
                ProgressView().controlSize(.mini)
            }
            Button {
                Task { await store.reloadWorkDetail() }
            } label: {
                Image(systemName: "arrow.clockwise")
            }
            .buttonStyle(.borderless)
            .help("Reload lifecycle events for this operation")
            .disabled(store.isWorkDetailLoading)
            Button {
                store.clearWorkSelection()
            } label: {
                Image(systemName: "xmark.circle.fill")
                    .foregroundStyle(.secondary)
            }
            .buttonStyle(.borderless)
            .help("Close detail drawer")
        }
        .padding(.horizontal, 14)
        .padding(.vertical, 10)
    }

    @ViewBuilder
    private func identityCard(_ selection: PressureStore.WorkSelection) -> some View {
        SectionCard(
            title: selection.isLease ? "Active lease" : selection.isHistorical ? "Lifecycle history" : "Queued waiter",
            systemImage: selection.isLease ? "lock.fill" : selection.isHistorical ? "clock.arrow.circlepath" : "hourglass"
        ) {
            labeledRow("Class", selection.className)
            labeledRow("Weight", "\(selection.weight)")
            labeledRow("PID", PressureFormat.pid(selection.pid))
            labeledRow("Operation", selection.operationID)
                .textSelection(.enabled)
            switch selection {
            case .lease(let lease):
                labeledRow("Lease", lease.id)
                    .textSelection(.enabled)
                labeledRow("Started", PressureFormat.relative(lease.startedAt))
                if lease.ageMS > 0 {
                    labeledRow("Age", PressureFormat.durationMS(lease.ageMS))
                }
                if lease.review {
                    Text(lease.reviewReason ?? "Finite lease exceeded review age")
                        .font(.caption)
                        .foregroundStyle(PressureTheme.levelColor(.warning))
                        .padding(.top, 4)
                }
                Text("Already executing under the owner PID. There is no separate “submit” step — capacity is held and the child is running.")
                    .font(.caption)
                    .foregroundStyle(.secondary)
                    .padding(.top, 6)
            case .waiter(let waiter):
                labeledRow("Position", "#\(waiter.position ?? 0)")
                labeledRow("Waited", PressureFormat.durationMS(waiter.waitMS))
                if waiter.protected {
                    labeledRow("Protection", waiter.protectionReason ?? "protected")
                }
                if store.board.work?.overrideOperationID == waiter.operationID {
                    Label("Marked Run now (awaiting capacity)", systemImage: "bolt.fill")
                        .font(.caption.weight(.semibold))
                        .foregroundStyle(PressureTheme.levelColor(.red))
                        .padding(.top, 4)
                }
                if waiter.overridePosition > 1 {
                    labeledRow("Pinned", "#\(waiter.overridePosition) of \(store.board.work?.overrideQueueDepth ?? waiter.overridePosition)")
                }
                Text("Use Run now to promote this live waiter, or Run all to pin the whole queue in order. Active leases are not preempted; host-pressure and capacity gates remain enforced.")
                    .font(.caption)
                    .foregroundStyle(.secondary)
                    .padding(.top, 6)
            case .historical(let work):
                labeledRow("Previous state", work.previousState)
                Label("No longer active in the live queue", systemImage: "checkmark.circle")
                    .font(.caption.weight(.semibold))
                    .foregroundStyle(PressureTheme.levelColor(.normal))
                    .padding(.top, 4)
                Text("This inspector is retained for lifecycle evidence only. Run now is unavailable because the operation has completed, cancelled, expired, or otherwise left live ownership.")
                    .font(.caption)
                    .foregroundStyle(.secondary)
                    .padding(.top, 6)
            }
        }
    }

    @ViewBuilder
    private func actionsCard(_ selection: PressureStore.WorkSelection) -> some View {
        SectionCard(title: "Actions", systemImage: "hammer") {
            VStack(alignment: .leading, spacing: 8) {
                if case .waiter(let waiter) = selection {
                    let overridden = store.board.work?.overrideOperationID == waiter.operationID
                    if overridden {
                        Label("Already promoted for next admission", systemImage: "checkmark.circle.fill")
                            .font(.caption.weight(.semibold))
                            .foregroundStyle(PressureTheme.levelColor(.normal))
                    } else if waiter.isOverrideQueued {
                        Label("Pinned #\(waiter.overridePosition) in the promotion sequence", systemImage: "bolt.horizontal.fill")
                            .font(.caption.weight(.semibold))
                            .foregroundStyle(PressureTheme.levelColor(.warning))
                    } else {
                        Button {
                            Task { await store.overrideWork(waiter: waiter) }
                        } label: {
                            Label("Run now", systemImage: "play.fill")
                        }
                        .buttonStyle(.borderedProminent)
                        .controlSize(.small)
                        .disabled(store.busyAction != nil)
                        .help(PressureHelp.workOverride)
                    }
                    Button {
                        copyToPasteboard(
                            "ndev --json session pressure work override --operation-id \(waiter.operationID) --confirm"
                        )
                    } label: {
                        Label("Copy override CLI", systemImage: "terminal")
                    }
                    .buttonStyle(.borderless)
                    Button {
                        copyToPasteboard("ndev --json session pressure work override --all --confirm")
                    } label: {
                        Label("Copy run-all CLI", systemImage: "terminal")
                    }
                    .buttonStyle(.borderless)
                }

                Button {
                    copyToPasteboard(selection.operationID)
                } label: {
                    Label("Copy operation ID", systemImage: "doc.on.doc")
                }
                .buttonStyle(.borderless)

                Button {
                    revealPressureDir()
                } label: {
                    Label("Reveal pressure data dir", systemImage: "folder")
                }
                .buttonStyle(.borderless)
                .help("Opens ~/.nicos-dev/session-pressure in Finder")
            }
        }
    }

    private var outputCard: some View {
        SectionCard(title: "Where is the output?", systemImage: "text.alignleft") {
            Text(PressureHelp.workOutputLocation)
                .font(.caption)
                .foregroundStyle(.secondary)
                .fixedSize(horizontal: false, vertical: true)
            if let path = store.board.work?.statePath, !path.isEmpty {
                labeledRow("State", path)
                    .textSelection(.enabled)
                    .padding(.top, 6)
            }
            labeledRow("Events", workEventsPathHint)
                .textSelection(.enabled)
            if let loaded = store.workDetailLoadedAt {
                Text("Lifecycle loaded \(PressureFormat.relative(loaded))")
                    .font(.caption2)
                    .foregroundStyle(.tertiary)
                    .padding(.top, 4)
            }
        }
        .help(PressureHelp.workOutputLocation)
    }

    private var lifecycleCard: some View {
        SectionCard(
            title: "Lifecycle",
            systemImage: "list.bullet.rectangle",
            trailing: "\(store.workDetailEvents.count)"
        ) {
            if let error = store.workDetailError {
                Text(error)
                    .font(.caption)
                    .foregroundStyle(PressureTheme.levelColor(.warning))
            } else if store.isWorkDetailLoading && store.workDetailEvents.isEmpty {
                HStack {
                    ProgressView().controlSize(.small)
                    Text("Loading work history…")
                        .font(.caption)
                        .foregroundStyle(.secondary)
                }
            } else if store.workDetailEvents.isEmpty {
                Text("No lifecycle events for this operation in the last 48h ledger window. The process may still be new, or history was rotated.")
                    .font(.caption)
                    .foregroundStyle(.secondary)
            } else {
                VStack(alignment: .leading, spacing: 0) {
                    ForEach(store.workDetailEvents) { event in
                        eventRow(event)
                        Divider().opacity(0.35)
                    }
                }
            }
        }
    }

    private func eventRow(_ event: WorkLifecycleEvent) -> some View {
        VStack(alignment: .leading, spacing: 4) {
            HStack {
                Text(event.event)
                    .font(.caption.weight(.semibold))
                if let outcome = event.outcome, !outcome.isEmpty {
                    Text(outcome)
                        .font(.caption2)
                        .foregroundStyle(.secondary)
                }
                Spacer()
                Text(PressureFormat.relative(event.timestamp))
                    .font(.caption2)
                    .foregroundStyle(.tertiary)
            }
            HStack(spacing: 10) {
                if let blocker = event.blocker, !blocker.isEmpty, blocker != "none" {
                    Text("blocker \(blocker)")
                        .font(PressureTheme.monoCaption)
                        .foregroundStyle(PressureTheme.levelColor(.warning))
                }
                if let wait = event.waitMS, wait > 0 {
                    Text("wait \(PressureFormat.durationMS(wait))")
                        .font(PressureTheme.monoCaption)
                        .foregroundStyle(.secondary)
                }
                if let runtime = event.runtimeMS, runtime > 0 {
                    Text("runtime \(PressureFormat.durationMS(runtime))")
                        .font(PressureTheme.monoCaption)
                        .foregroundStyle(.secondary)
                }
                if let code = event.exitCode {
                    Text("exit \(code)")
                        .font(PressureTheme.monoCaption)
                        .foregroundStyle(code == 0 ? PressureTheme.levelColor(.normal) : PressureTheme.levelColor(.red))
                }
            }
            if let digest = event.commandDigest, !digest.isEmpty {
                Text("cmd \(PressureFormat.shortDigest(digest))")
                    .font(PressureTheme.monoCaption)
                    .foregroundStyle(.tertiary)
                    .help("Opaque command digest only — argv is never stored.")
            }
            if let requestID = event.requestID, !requestID.isEmpty {
                Text("request \(PressureFormat.shortOperationID(requestID))")
                    .font(PressureTheme.monoCaption)
                    .foregroundStyle(.tertiary)
                    .help("Opaque operator request identity; no command content is stored.")
            }
            if let reason = event.pressureReason, !reason.isEmpty {
                Text(reason)
                    .font(.caption2)
                    .foregroundStyle(.secondary)
            }
        }
        .padding(.vertical, 8)
    }

    private func labeledRow(_ title: String, _ value: String) -> some View {
        HStack(alignment: .firstTextBaseline) {
            Text(title)
                .font(.caption)
                .foregroundStyle(.secondary)
                .frame(width: 72, alignment: .leading)
            Text(value)
                .font(PressureTheme.monoCaption)
                .textSelection(.enabled)
            Spacer(minLength: 0)
        }
    }

    /// Name the ledger files the loaded events actually came from. Deriving this
    /// from `Date()` confidently pointed at today's file for an operation whose
    /// events were written yesterday — the 48h window spans two files.
    private var workEventsPathHint: String {
        let home = FileManager.default.homeDirectoryForCurrentUser.path
        let directory = "\(home)/.nicos-dev/session-pressure"
        let day = DateFormatter()
        day.locale = Locale(identifier: "en_US_POSIX")
        day.timeZone = TimeZone(identifier: "UTC")
        day.dateFormat = "yyyyMMdd"

        let days = Set(store.workDetailEvents.compactMap { $0.timestamp }.map { day.string(from: $0) })
        guard !days.isEmpty else {
            // Nothing loaded: describe the pattern rather than assert a file.
            return "\(directory)/work-events-YYYYMMDD.jsonl"
        }
        return days.sorted().map { "\(directory)/work-events-\($0).jsonl" }.joined(separator: "\n")
    }

    private func copyToPasteboard(_ string: String) {
        NSPasteboard.general.clearContents()
        NSPasteboard.general.setString(string, forType: .string)
        store.statusMessage = "Copied"
        Task {
            try? await Task.sleep(nanoseconds: 1_200_000_000)
            if store.statusMessage == "Copied" {
                store.statusMessage = nil
            }
        }
    }

    private func revealPressureDir() {
        let home = FileManager.default.homeDirectoryForCurrentUser
        let dir = home.appendingPathComponent(".nicos-dev/session-pressure", isDirectory: true)
        NSWorkspace.shared.activateFileViewerSelecting([dir])
    }
}
