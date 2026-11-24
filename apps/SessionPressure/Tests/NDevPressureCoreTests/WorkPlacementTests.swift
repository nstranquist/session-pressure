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

/// Offscreen evidence render: no window is created. Runs only when
/// PLACEMENT_SNAPSHOT_PATH is set; PLACEMENT_SNAPSHOT_FIXTURE selects a live
/// payload instead of the recorded one.
@Suite("Work placement offscreen render")
@MainActor
struct WorkPlacementRenderTests {
    @Test("renders the stall banner and placement card to PNG")
    func render() throws {
        let env = ProcessInfo.processInfo.environment
        guard let out = env["PLACEMENT_SNAPSHOT_PATH"] else { return }
        let url = try env["PLACEMENT_SNAPSHOT_FIXTURE"].map { URL(fileURLWithPath: $0) }
            ?? #require(Bundle.module.url(forResource: "work-placement", withExtension: "json", subdirectory: "Fixtures"))
        let report = try #require(try PressureJSON.decode(WorkPlacementEnvelope.self, from: Data(contentsOf: url)).placement)
        let view = VStack(alignment: .leading, spacing: 16) {
            if let stall = report.activeStall { WorkStallBanner(stall: stall) }
            WorkPlacementCard(report: report, error: nil, loadedAt: Date())
        }
        .frame(width: 720)
        .padding(16)
        .background(Color(nsColor: .windowBackgroundColor))
        let renderer = ImageRenderer(content: view)
        renderer.scale = 2
        let image = try #require(renderer.nsImage)
        let tiff = try #require(image.tiffRepresentation)
        let png = try #require(NSBitmapImageRep(data: tiff)?.representation(using: .png, properties: [:]))
        try png.write(to: URL(fileURLWithPath: out))
    }
}

/// Layout contract: the Work Queue pane is flexible down to 480 pt
/// (ResizableSplit flexibleMinWidth), so its cards must fit what they are
/// offered. A child wider than its pane pushes the whole page off screen
/// (2026-11-24: a 1024-slot runner drew 1024 SlotBar segments, ~3,070 pt).
@Suite("Work placement layout")
@MainActor
struct WorkPlacementLayoutTests {
    static func width(of view: some View, offered: CGFloat) -> CGFloat {
        NSHostingController(rootView: view).sizeThatFits(in: CGSize(width: offered, height: 10_000)).width
    }

    static func report(heavySlots: Int, lightSlots: Int) throws -> WorkPlacementReport {
        let url = try #require(Bundle.module.url(forResource: "work-placement", withExtension: "json", subdirectory: "Fixtures"))
        var json = try #require(try JSONSerialization.jsonObject(with: Data(contentsOf: url)) as? [String: Any])
        var placement = try #require(json["placement"] as? [String: Any])
        var runners = try #require(placement["runners"] as? [[String: Any]])
        for i in runners.indices {
            runners[i]["heavy_slots"] = heavySlots
            runners[i]["light_slots"] = lightSlots
        }
        placement["runners"] = runners
        json["placement"] = placement
        let data = try JSONSerialization.data(withJSONObject: json)
        return try #require(try PressureJSON.decode(WorkPlacementEnvelope.self, from: data).placement)
    }

    @Test("a slot bar fits its offer at any slot count", arguments: [0, 2, 8, SlotBar.maxSegments, SlotBar.maxSegments + 1, 1024, 100_000])
    func slotBarFits(slots: Int) {
        for jobs in [0, 3, slots, slots + 5] {
            let width = Self.width(of: SlotBar(jobs: jobs, slots: slots), offered: 200)
            #expect(width <= 200.5, "jobs=\(jobs) slots=\(slots) needs \(width) pt")
        }
    }

    @Test("the placement card fits the narrowest Work Queue pane", arguments: [8, 1024])
    func placementCardFits(slots: Int) throws {
        let card = WorkPlacementCard(report: try Self.report(heavySlots: slots, lightSlots: slots), error: nil, loadedAt: Date())
        let width = Self.width(of: card, offered: 480)
        #expect(width <= 480.5, "slots=\(slots) needs \(width) pt")
    }
}
