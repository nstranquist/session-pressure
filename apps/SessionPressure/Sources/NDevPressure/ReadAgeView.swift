import SwiftUI

/// A read age needs human-scale freshness, not a per-second layout timer.
struct ReadAgeView: View {
    let date: Date

    var body: some View {
        TimelineView(.periodic(from: .now, by: 15)) { _ in
            Text(Self.label(date: date, now: Date()))
        }
    }

    private static func label(date: Date, now: Date) -> String {
        if now.timeIntervalSince(date) < 1 { return "just now" }
        let formatter = RelativeDateTimeFormatter()
        formatter.unitsStyle = .abbreviated
        return formatter.localizedString(for: date, relativeTo: now)
    }
}
