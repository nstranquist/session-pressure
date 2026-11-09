import Testing
@testable import NDevPressureCore

@Suite("Current interruption telemetry")
struct WorkCancellationTelemetryTests {
    @Test("current zero is authoritative")
    func currentZero() {
        let counts = WorkCancellationTelemetry(calibration: WorkCalibration(wrapperInterruptOperations: 0))
        #expect(counts?.total == 0)
    }

    @Test("missing calibration is unavailable, not zero")
    func unavailable() {
        #expect(WorkCancellationTelemetry(calibration: nil) == nil)
        #expect(WorkCancellationTelemetry(calibration: WorkCalibration()) == nil)
    }

    @Test("old aggregate preserves count without inventing causes")
    func legacy() {
        let counts = WorkCancellationTelemetry(calibration: WorkCalibration(wrapperInterruptOperations: 35))
        #expect(counts?.signalCancellations == 0)
        #expect(counts?.waitTimeouts == 0)
        #expect(counts?.unspecifiedInterruptions == 35)
    }

    @Test("timeouts are separate from signal cancellations and legacy interruptions")
    func breakdown() {
        let counts = WorkCancellationTelemetry(calibration: WorkCalibration(
            wrapperInterruptOperations: 35,
            reviewSignals: WorkReviewSignals(signalCancelOperations: 20, waitTimeoutOperations: 3, legacyInterruptOperations: 15)
        ))
        #expect(counts?.signalCancellations == 20)
        #expect(counts?.waitTimeouts == 3)
        #expect(counts?.unspecifiedInterruptions == 15)
        #expect(counts?.total == 38)
    }
}
