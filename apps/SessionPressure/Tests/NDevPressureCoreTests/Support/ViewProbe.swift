import AppKit
import SwiftUI

/// Lays out, measures and draws SwiftUI views without a window.
///
/// The host is an `NSHostingView` that is never attached to a window, so
/// nothing appears on the desktop and no window-server window exists. It
/// draws scroll views, lazy stacks, text, shapes, buttons, menu and
/// segmented pickers, text fields, `NSViewRepresentable` text views, dark
/// and light appearance and environment objects. Two things a window
/// normally does are done here instead: `host` paints the window background
/// behind the page, and it builds `List` rows (`buildTableRows`).
@MainActor
enum ViewProbe {
    /// Width the view asks for when offered `offered` points of width and
    /// unbounded height. A result above `offered` means the view cannot fit.
    static func fit(_ view: some View, offered: CGFloat) -> CGFloat {
        NSHostingController(rootView: view)
            .sizeThatFits(in: CGSize(width: offered, height: 100_000))
            .width
    }

    /// A laid-out host for `view` at `size`. Lazy content and `.task` work
    /// settle over a few run-loop turns, so layout runs several times.
    static func host(
        _ view: some View,
        size: CGSize,
        appearance: NSAppearance.Name = .darkAqua,
        settle: Duration = .milliseconds(400)
    ) async -> NSHostingView<AnyView> {
        // A window paints its background behind the detail column. Without
        // one, pages that set no background are transparent, and light text
        // in dark mode vanishes in the PNG.
        let page = view
            .frame(width: size.width, height: size.height)
            .background(Color(nsColor: .windowBackgroundColor))
        let host = NSHostingView(rootView: AnyView(page))
        host.appearance = NSAppearance(named: appearance)
        host.frame = NSRect(origin: .zero, size: size)
        let turns = 4
        for _ in 0..<turns {
            host.layoutSubtreeIfNeeded()
            buildTableRows(in: host)
            try? await Task.sleep(for: settle / turns)
        }
        host.layoutSubtreeIfNeeded()
        buildTableRows(in: host)
        return host
    }

    /// A `List` is an `NSTableView`. A table builds row views only for its
    /// `visibleRect`, which is empty outside a window, so a windowless list
    /// draws no rows. This builds the rows inside each table's clip view.
    static func buildTableRows(in root: NSView) {
        if let table = root as? NSTableView, table.numberOfRows > 0 {
            let shown = table.enclosingScrollView?.contentView.bounds ?? table.bounds
            let rows = table.rows(in: shown)
            for row in rows.location..<(rows.location + rows.length) {
                _ = table.rowView(atRow: row, makeIfNecessary: true)
            }
        }
        root.subviews.forEach(buildTableRows)
    }

    /// A platform view that extends past the host's left or right edge.
    /// SwiftUI widens a page's scroll view to its content's minimum width
    /// and centres it, so a too-wide card pushes most of the page off
    /// screen.
    struct Escape: CustomStringConvertible {
        let view: String
        let frameInHost: CGRect
        let hostWidth: CGFloat
        var description: String {
            "\(view) spans x \(Int(frameInHost.minX))…\(Int(frameInHost.maxX)) in a \(Int(hostWidth)) pt host"
        }
    }

    /// Every visible platform view in `host` that reaches past its left or
    /// right edge. Views inside a scroll view's document are skipped: they
    /// scroll by design.
    static func horizontalEscapes(in host: NSView, tolerance: CGFloat = 0.5) -> [Escape] {
        var found: [Escape] = []
        func walk(_ view: NSView) {
            guard !view.isHidden else { return }
            if view !== host, view.frame.width > 0, view.frame.height > 0 {
                let frame = view.convert(view.bounds, to: host)
                if frame.minX < -tolerance || frame.maxX > host.bounds.width + tolerance {
                    found.append(Escape(view: String(describing: type(of: view)), frameInHost: frame, hostWidth: host.bounds.width))
                    return // its subviews escape with it
                }
            }
            if view is NSClipView { return }
            view.subviews.forEach(walk)
        }
        walk(host)
        return found
    }

    /// Draws `host` into a bitmap at `scale` pixels per point.
    static func bitmap(_ host: NSView, scale: CGFloat = 2) throws -> NSBitmapImageRep {
        let bounds = host.bounds
        guard let rep = NSBitmapImageRep(
            bitmapDataPlanes: nil,
            pixelsWide: Int((bounds.width * scale).rounded()),
            pixelsHigh: Int((bounds.height * scale).rounded()),
            bitsPerSample: 8,
            samplesPerPixel: 4,
            hasAlpha: true,
            isPlanar: false,
            colorSpaceName: .deviceRGB,
            bytesPerRow: 0,
            bitsPerPixel: 0
        ) else { throw ProbeError.bitmap }
        rep.size = bounds.size
        host.cacheDisplay(in: bounds, to: rep)
        return rep
    }

    static func png(_ rep: NSBitmapImageRep) throws -> Data {
        guard let data = rep.representation(using: .png, properties: [:]) else { throw ProbeError.png }
        return data
    }

    /// Share of sampled pixels that differ from the page background, taken
    /// as the most common sampled colour. A page that drew nothing scores 0.
    static func ink(_ rep: NSBitmapImageRep, step: Int = 4) -> Double {
        guard let data = rep.bitmapData else { return 0 }
        let width = rep.pixelsWide, height = rep.pixelsHigh, row = rep.bytesPerRow
        let bytesPerPixel = rep.bitsPerPixel / 8
        var counts: [UInt32: Int] = [:]
        var total = 0
        for y in stride(from: 0, to: height, by: step) {
            for x in stride(from: 0, to: width, by: step) {
                let p = data + y * row + x * bytesPerPixel
                // Quantise to 5 bits per channel so antialiasing noise does
                // not split the background into many colours.
                let key = UInt32(p[0] >> 3) << 10 | UInt32(p[1] >> 3) << 5 | UInt32(p[2] >> 3)
                counts[key, default: 0] += 1
                total += 1
            }
        }
        guard total > 0, let background = counts.values.max() else { return 0 }
        return Double(total - background) / Double(total)
    }

    enum ProbeError: Error { case bitmap, png }
}
