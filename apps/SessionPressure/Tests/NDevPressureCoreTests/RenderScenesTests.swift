import AppKit
import Foundation
import SwiftUI
import Testing
@testable import NDevPressure

/// `make render` entry point. Draws chosen scenes to PNG without a window and
/// writes `manifest.json` beside them. It does nothing unless
/// SCENE_RENDER_DIR is set, so plain `swift test` skips it.
///
/// Environment (set by the Makefile):
/// - SCENE_RENDER_DIR: output directory (required)
/// - SCENES: scene names, comma or space separated; empty means all
/// - SCENE_SOURCE: `fixtures` (default) or `live`
/// - SCENE_APPEARANCE: `dark` (default) or `light`
/// - SCENE_WIDTH, SCENE_HEIGHT, SCENE_SCALE: render size in points and scale
///   (defaults 1000, the scene's own height, 2)
/// - SCENE_REVISION: source revision recorded in the manifest
@Suite("Render scenes")
@MainActor
struct RenderScenesTests {
    struct Entry: Encodable {
        let scene: String
        let summary: String
        let file: String
        let width: Double
        let height: Double
        let scale: Double
        let appearance: String
        let source: String
        let minWidth: Double
        let neededWidth: Double
        let fits: Bool
        let escapes: [String]
        let ink: Double
        let fixtures: [String]
        let fixtureDigest: String?
    }

    struct Manifest: Encodable {
        let renderedAt: Date
        let revision: String
        let host: String
        let scenes: [Entry]
    }

    @Test("render scenes to PNG")
    func render() async throws {
        let env = ProcessInfo.processInfo.environment
        guard let dir = env["SCENE_RENDER_DIR"], !dir.isEmpty else { return }
        let out = URL(fileURLWithPath: dir, isDirectory: true)
        try FileManager.default.createDirectory(at: out, withIntermediateDirectories: true)

        let requested = (env["SCENES"] ?? "")
            .split(whereSeparator: { $0 == "," || $0 == " " })
            .map(String.init)
        let scenes = requested.isEmpty ? SceneCatalog.all : try SceneCatalog.named(requested)
        let source = SceneSource(rawValue: env["SCENE_SOURCE"] ?? "") ?? .fixtures
        let light = env["SCENE_APPEARANCE"] == "light"
        let width = env["SCENE_WIDTH"].flatMap(Double.init) ?? 1000
        let scale = env["SCENE_SCALE"].flatMap(Double.init) ?? 2

        var entries: [Entry] = []
        for scene in scenes {
            let height = env["SCENE_HEIGHT"].flatMap(Double.init) ?? scene.height
            let view = try await scene.make(source)
            let needed = ViewProbe.fit(view, offered: scene.minWidth)
            let host = await ViewProbe.host(view, size: CGSize(width: width, height: height), appearance: light ? .aqua : .darkAqua)
            let rep = try ViewProbe.bitmap(host, scale: scale)
            let file = "\(scene.name).png"
            try ViewProbe.png(rep).write(to: out.appendingPathComponent(file))
            entries.append(Entry(
                scene: scene.name,
                summary: scene.summary,
                file: file,
                width: width,
                height: height,
                scale: scale,
                appearance: light ? "light" : "dark",
                source: source.rawValue,
                minWidth: scene.minWidth,
                neededWidth: needed,
                fits: needed <= scene.minWidth + 0.5,
                escapes: ViewProbe.horizontalEscapes(in: host).map(\.description),
                ink: ViewProbe.ink(rep),
                fixtures: source == .fixtures ? scene.fixtures : [],
                fixtureDigest: source == .fixtures ? try SceneFixtures.digest(scene.fixtures) : nil
            ))
        }

        let encoder = JSONEncoder()
        encoder.keyEncodingStrategy = .convertToSnakeCase
        encoder.dateEncodingStrategy = .iso8601
        encoder.outputFormatting = [.prettyPrinted, .sortedKeys]
        let manifest = Manifest(
            renderedAt: Date(),
            revision: env["SCENE_REVISION"] ?? "unknown",
            host: "windowless NSHostingView (no window)",
            scenes: entries
        )
        try encoder.encode(manifest).write(to: out.appendingPathComponent("manifest.json"))
    }
}
