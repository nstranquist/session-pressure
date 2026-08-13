import Foundation

/// Host utilization for the overview ring and menu-bar gauge.
///
/// Policy language stays in free-memory percent (warning ≤25% free, …). The
/// progress arc must still fill to memory *used* (`100 − free`), not to a
/// discrete level stub (normal=18%, warning=45%, red=72%, critical=92%).
public struct PressureUtilization: Equatable, Sendable {
    public var freePercent: Int
    public var memoryUsedPercent: Int
    public var hostCPUPercent: Double
    public var hostCPUAvailable: Bool

    public init(freePercent: Int, hostCPUPercent: Double = 0, hostCPUAvailable: Bool = true) {
        let free = min(max(freePercent, 0), 100)
        self.freePercent = free
        self.memoryUsedPercent = 100 - free
        self.hostCPUPercent = hostCPUAvailable ? max(0, hostCPUPercent) : 0
        self.hostCPUAvailable = hostCPUAvailable
    }

    /// 0...1 fill for the overview ring. Equals memory used, not policy level.
    public var fraction: Double {
        Double(memoryUsedPercent) / 100
    }

    public static func memoryUsedPercent(freePercent: Int) -> Int {
        PressureUtilization(freePercent: freePercent).memoryUsedPercent
    }

    public static func fraction(freePercent: Int) -> Double {
        PressureUtilization(freePercent: freePercent).fraction
    }

    /// Menu-bar needle bucket from used percent, independent of policy level.
    public var menuBarGaugeSymbol: String {
        switch memoryUsedPercent {
        case ..<25:
            return "gauge.with.dots.needle.33percent"
        case ..<50:
            return "gauge.with.dots.needle.50percent"
        case ..<75:
            return "gauge.with.dots.needle.67percent"
        default:
            return "gauge.with.dots.needle.100percent"
        }
    }
}

public extension PressureSnapshot {
    var utilization: PressureUtilization {
        PressureUtilization(
            freePercent: freePercent,
            hostCPUPercent: hostCPUPercent,
            hostCPUAvailable: hostCPUAvailable
        )
    }
}
