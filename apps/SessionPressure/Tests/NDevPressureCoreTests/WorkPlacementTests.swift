import Foundation
import AppKit
import SwiftUI
import Testing
@testable import NDevPressure
@testable import NDevPressureCore

@Suite("Work placement view")
struct WorkPlacementTests {
    @Test("decodes the helper's placement payload")
    func decodesRecordedPayload() throws {
        let url = try #require(Bundle.module.url(forResource: "work-placement", withExtension: "json", subdirectory: "Fixtures"))
        let env = try PressureJSON.decode(WorkPlacementEnvelope.self, from: Data(contentsOf: url))
        let report = try #require(env.placement)
        #expect(env.action == "work.placement")
        #expect(report.remote.enabled && report.remote.placer)
        #expect(report.decisions.total == report.decisions.local + report.decisions.remote)
        #expect(report.decisions.recent.count == 2)
        #expect(report.runners.first?.heavySlots == 8)
        #expect(report.runners.first?.heavyJobs == 9)
        let stall = try #require(report.activeStall)
        #expect(stall.operationID == "op-stalled" && stall.durationMS == 95_000 && stall.effectiveCapacity == 8)
        #expect(report.eta(for: "op-stalled")?.etaMS == 40_000)
        let unknown = try #require(report.eta(for: "op-next"))
        #expect(!unknown.isKnown && unknown.basis?.contains("unmeasured") == true)
        #expect(report.eta(for: "absent") == nil)
    }

    @Test("an empty or partial payload never fails the Work view")
    func toleratesPartialPayload() throws {
        let env = try PressureJSON.decode(WorkPlacementEnvelope.self, from: Data(#"{"ok":true,"action":"work.placement","placement":{}}"#.utf8))
        let report = try #require(env.placement)
        #expect(report.decisions.total == 0 && report.runners.isEmpty && report.waiters.isEmpty && report.activeStall == nil)
    }
}

/// Layout contract: the Work Queue list pane keeps 480 pt (ResizableSplit
/// flexibleMinWidth) until the window is too narrow for both panes, so its
/// cards must fit what they are offered. A child wider than its pane pushes
/// the whole page off screen (2026-11-24: a 1024-slot runner drew 1024
/// SlotBar segments, ~3,070 pt). `SceneGateTests` checks whole pages at the
/// narrowest window.
@Suite("Work placement layout")
@MainActor
struct WorkPlacementLayoutTests {
    @Test("a slot bar fits its offer at any slot count", arguments: [0, 2, 8, SlotBar.maxSegments, SlotBar.maxSegments + 1, 1024, 100_000])
    func slotBarFits(slots: Int) {
        for jobs in [0, 3, slots, slots + 5] {
            let width = ViewProbe.fit(SlotBar(jobs: jobs, slots: slots), offered: 200)
            #expect(width <= 200.5, "jobs=\(jobs) slots=\(slots) needs \(width) pt")
        }
    }

    @Test("the placement card fits the narrowest Work Queue pane", arguments: [8, 1024])
    func placementCardFits(slots: Int) throws {
        let card = WorkPlacementCard(report: try SceneFixtures.workPlacement(heavySlots: slots, lightSlots: slots), error: nil, loadedAt: Date())
        let width = ViewProbe.fit(card, offered: 480)
        #expect(width <= 480.5, "slots=\(slots) needs \(width) pt")
    }
}
