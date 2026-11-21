import Foundation
import Testing
@testable import NDevPressure

@Suite("Process incident navigation")
struct ProcessIncidentNavigationTests {
    @Test @MainActor func notificationOnlyNavigates() {
        let store = PressureStore()
        store.handleDeepLink(URL(string: "ndev-pressure://process-incidents?id=fixture")!)
        #expect(store.selectedSection == .processIncidents)
        #expect(PressureStore.Section.allCases.contains(.dayGate))
        #expect(PressureStore.Section.allCases.contains(.work))
        store.stop()
    }
}
