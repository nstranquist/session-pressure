import AppKit
import SwiftUI
import Testing

/// The probe must catch what the scene gate relies on it to catch. Each test
/// plants the failure and checks that the probe reports it.
@Suite("Windowless view probe")
@MainActor
struct ViewProbeTests {
    /// The Work Queue bug in miniature: one shape per slot inside a page
    /// scroll view.
    private struct PlantedOverflow: View {
        let segments: Int
        var body: some View {
            ScrollView {
                VStack(alignment: .leading) {
                    Text("Placement & offload")
                    HStack(spacing: 3) {
                        ForEach(0..<segments, id: \.self) { _ in
                            RoundedRectangle(cornerRadius: 2).fill(.green).frame(minWidth: 1).frame(height: 8)
                        }
                    }
                }
                .padding(20)
            }
        }
    }

    @Test("a page widened by too-wide content is caught by fit and by its escaping scroll view")
    func reportsPlantedOverflow() async throws {
        let page = PlantedOverflow(segments: 1024)
        #expect(ViewProbe.fit(page, offered: 700) > 3_000)
        let host = await ViewProbe.host(page, size: CGSize(width: 700, height: 400))
        #expect(host.window == nil)
        let escapes = ViewProbe.horizontalEscapes(in: host)
        #expect(escapes.count == 1, "\(escapes)")
        #expect((escapes.first?.frameInHost.width ?? 0) > 3_000)
    }

    @Test("a page that fits reports no escape")
    func fittingPageIsClean() async throws {
        let page = PlantedOverflow(segments: 24)
        #expect(ViewProbe.fit(page, offered: 700) <= 700.5)
        let host = await ViewProbe.host(page, size: CGSize(width: 700, height: 400))
        #expect(ViewProbe.horizontalEscapes(in: host).isEmpty)
    }

    @Test("fit reports the width a view needs")
    func fitMeasuresMinimumWidth() {
        let wide = HStack(spacing: 0) { Color.red.frame(minWidth: 900) }
        #expect(ViewProbe.fit(wide, offered: 700) >= 900)
        #expect(ViewProbe.fit(Text("short"), offered: 700) <= 700)
    }

    @Test("ink separates a drawn page from an empty one")
    func inkSeparatesBlankFromDrawn() async throws {
        let blank = await ViewProbe.host(Color.clear, size: CGSize(width: 300, height: 200))
        #expect(ViewProbe.ink(try ViewProbe.bitmap(blank)) < 0.001)
        let drawn = await ViewProbe.host(PlantedOverflow(segments: 8), size: CGSize(width: 300, height: 200))
        #expect(ViewProbe.ink(try ViewProbe.bitmap(drawn)) > 0.005)
    }

    @Test("a page with no background gets the window background, so light text stays visible")
    func pagesGetWindowBackground() async throws {
        let host = await ViewProbe.host(Text("title").foregroundStyle(.white), size: CGSize(width: 200, height: 60))
        let rep = try ViewProbe.bitmap(host, scale: 1)
        let corner = try #require(rep.colorAt(x: 1, y: 1))
        #expect(corner.alphaComponent == 1, "transparent pixels hide white text in dark mode")
        #expect(ViewProbe.ink(rep) > 0.005)
    }

    @Test("list rows draw without a window")
    func listRowsDraw() async throws {
        let list = List { ForEach(0..<5, id: \.self) { Text("row \($0)").font(.headline) } }
        let host = await ViewProbe.host(list, size: CGSize(width: 300, height: 300))
        #expect(host.window == nil)
        #expect(ViewProbe.ink(try ViewProbe.bitmap(host, scale: 1)) > 0.005)
    }

    @Test("bitmaps are drawn at the requested scale")
    func bitmapScale() async throws {
        let host = await ViewProbe.host(Text("scale"), size: CGSize(width: 120, height: 40))
        let rep = try ViewProbe.bitmap(host, scale: 2)
        #expect(rep.pixelsWide == 240 && rep.pixelsHigh == 80)
        #expect(ViewProbe.ink(rep) > 0)
    }
}
