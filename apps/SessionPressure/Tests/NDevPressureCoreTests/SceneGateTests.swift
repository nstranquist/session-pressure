import AppKit
import SwiftUI
import Testing
@testable import NDevPressure

/// Every app scene, built from recorded fixtures without a window, must fit
/// the narrowest detail pane and must draw. This catches a card that widens
/// its page and a page that renders blank.
/// Look at a scene with `make render SCENES=<name>`.
@Suite("Scene gate")
@MainActor
struct SceneGateTests {
    /// Share of pixels a real page draws. The emptiest page with recorded
    /// data measures about 4% at the narrowest width.
    static let minimumInk = 0.01

    @Test("each scene fits the narrowest pane and draws", arguments: SceneCatalog.all.map(\.name))
    func sceneFitsAndDraws(name: String) async throws {
        let scene = try #require(try SceneCatalog.named([name]).first)
        let view = try await scene.make(.fixtures)

        let needed = ViewProbe.fit(view, offered: scene.minWidth)
        #expect(needed <= scene.minWidth + 0.5, "\(name) needs \(Int(needed)) pt; the narrowest pane is \(Int(scene.minWidth)) pt")

        let host = await ViewProbe.host(view, size: CGSize(width: scene.minWidth, height: 900))
        #expect(host.window == nil, "the probe must not create a window")
        let escapes = ViewProbe.horizontalEscapes(in: host)
        #expect(escapes.isEmpty, "\(name): \(escapes)")

        let ink = ViewProbe.ink(try ViewProbe.bitmap(host, scale: 1))
        #expect(ink >= Self.minimumInk, "\(name) drew almost nothing (ink \(ink))")
    }

    @Test("every sidebar page has a scene")
    func catalogCoversEveryPage() {
        let covered = Set(SceneCatalog.all.compactMap(\.section))
        let missing = PressureStore.Section.allCases.filter { !covered.contains($0) }
        #expect(missing.isEmpty, "add a scene for \(missing.map(\.rawValue))")
    }

    @Test("scene names are unique and an unknown name is refused")
    func namesAreUnique() {
        let names = SceneCatalog.all.map(\.name)
        #expect(Set(names).count == names.count)
        #expect(throws: SceneError.self) { try SceneCatalog.named(["no-such-scene"]) }
    }
}
