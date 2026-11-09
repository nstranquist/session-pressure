import Foundation

/// Current calibration only. Heartbeats carry older rolling aggregates and must
/// never resurrect a nonzero count after the current count reaches zero.
public struct WorkCancellationTelemetry: Equatable, Sendable {
    public let signalCancellations: Int
    public let waitTimeouts: Int
    public let unspecifiedInterruptions: Int
    public var total: Int { signalCancellations + waitTimeouts + unspecifiedInterruptions }

    public init?(calibration: WorkCalibration?) {
        guard let calibration, let interrupts = calibration.wrapperInterruptOperations else { return nil }
        let signals = calibration.reviewSignals
        signalCancellations = signals?.signalCancelOperations ?? 0
        waitTimeouts = signals?.waitTimeoutOperations ?? 0
        // Older binaries have only the combined counter. Preserve its count
        // without assigning an unproven cause to those historical records.
        unspecifiedInterruptions = signals?.legacyInterruptOperations
            ?? max(0, interrupts - signalCancellations)
    }
}
