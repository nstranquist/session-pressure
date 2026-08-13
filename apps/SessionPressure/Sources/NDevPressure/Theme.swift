import SwiftUI
import NDevPressureCore

enum PressureTheme {
    static let bg = Color(nsColor: .windowBackgroundColor)
    static let card = Color(nsColor: .controlBackgroundColor)
    static let hairline = Color.primary.opacity(0.08)
    static let mono = Font.system(.body, design: .monospaced)
    static let monoCaption = Font.system(.caption, design: .monospaced)
    static let monoTitle = Font.system(.title2, design: .monospaced).weight(.semibold)

    static func levelColor(_ level: PressureLevel) -> Color {
        switch level {
        case .normal: Color(red: 0.23, green: 0.78, blue: 0.45)
        case .warning: Color(red: 0.95, green: 0.72, blue: 0.18)
        case .red: Color(red: 0.95, green: 0.42, blue: 0.18)
        case .critical: Color(red: 0.92, green: 0.22, blue: 0.28)
        case .unknown: Color.secondary
        }
    }

    static func levelFill(_ level: PressureLevel) -> Color {
        levelColor(level).opacity(0.14)
    }

    static func agentColor(_ agent: String) -> Color {
        switch agent.lowercased() {
        case "codex": Color(red: 0.35, green: 0.72, blue: 0.95)
        case "claude": Color(red: 0.85, green: 0.55, blue: 0.35)
        case "grok": Color(red: 0.55, green: 0.85, blue: 0.55)
        case "kimi": Color(red: 0.75, green: 0.55, blue: 0.95)
        default: Color.secondary
        }
    }
}
