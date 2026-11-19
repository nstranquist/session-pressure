import Foundation
import Testing

/// macOS 26 runs the NavigationSplitView detail column under the floating
/// sidebar. `HSplitView` and `VSplitView` are `NSSplitView`s that lose the
/// safe-area inset after a pane toggles, so their content slides under the
/// sidebar and the page looks blank (corpus/app-split-panes-2026-11-19.md).
/// Use `ResizableSplit` in Components.swift instead.
@Suite("App layout guard")
struct LayoutGuardTests {
    static let banned = ["HSplitView", "VSplitView"]

    /// Returns "file:line: token" for each banned token outside a `//` comment.
    static func violations(in source: String, file: String) -> [String] {
        var found: [String] = []
        for (index, line) in source.components(separatedBy: "\n").enumerated() {
            let code = line.range(of: "//").map { String(line[..<$0.lowerBound]) } ?? line
            for token in banned where code.range(of: "\\b\(token)\\b", options: .regularExpression) != nil {
                found.append("\(file):\(index + 1): \(token)")
            }
        }
        return found
    }

    @Test("the scanner flags split views in code and ignores comments")
    func scannerFindsPlantedSplitView() {
        let planted = "var body: some View {\n    HSplitView { a; b }\n}\n// HSplitView is banned\n"
        #expect(Self.violations(in: planted, file: "Planted.swift") == ["Planted.swift:2: HSplitView"])
        #expect(Self.violations(in: "VSplitView { x }", file: "V.swift") == ["V.swift:1: VSplitView"])
        #expect(Self.violations(in: "ResizableSplit(sized: .leading)", file: "R.swift").isEmpty)
    }

    @Test("app views do not use AppKit split views")
    func appSourcesHaveNoSplitViews() throws {
        let appSources = URL(fileURLWithPath: #filePath)
            .deletingLastPathComponent() // NDevPressureCoreTests
            .deletingLastPathComponent() // Tests
            .deletingLastPathComponent() // SessionPressure
            .appendingPathComponent("Sources/NDevPressure")
        let enumerator = try #require(FileManager.default.enumerator(at: appSources, includingPropertiesForKeys: nil))
        var scanned = 0
        var found: [String] = []
        for case let url as URL in enumerator where url.pathExtension == "swift" {
            scanned += 1
            let source = try String(contentsOf: url, encoding: .utf8)
            found += Self.violations(in: source, file: url.lastPathComponent)
        }
        #expect(scanned > 10, "expected to scan the app sources at \(appSources.path)")
        #expect(found.isEmpty, "use ResizableSplit, not \(found.joined(separator: ", "))")
    }
}
