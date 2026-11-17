package sessionpressure

import (
	"testing"
	"time"
)

func TestPooledChargeMB(t *testing.T) {
	// One lease is charged its peak at any correlation.
	for _, rho := range []float64{0, headroomPoolRho, 1} {
		if got := pooledChargeMB([][2]int64{{4845, 498}}, rho); got != 4845 {
			t.Fatalf("rho %.2f: one lease charged %d, want its peak 4845", rho, got)
		}
	}
	// rho 1 is the plain sum of peaks.
	bands := [][2]int64{{4845, 498}, {4845, 498}, {1367, 323}}
	if got := pooledChargeMB(bands, 1); got != 4845+4845+1367 {
		t.Fatalf("rho 1 charged %d, want the sum of peaks", got)
	}
	// Pooled sits between the sum of medians and the sum of peaks.
	pooled := pooledChargeMB(bands, headroomPoolRho)
	if pooled <= 498+498+323 || pooled >= 4845+4845+1367 {
		t.Fatalf("pooled charge %d is not between medians and peaks", pooled)
	}
	// A median above its peak is clipped to the peak.
	if got := pooledChargeMB([][2]int64{{1000, 5000}, {1000, 5000}}, headroomPoolRho); got != 2000 {
		t.Fatalf("median above peak charged %d, want 2000", got)
	}
	if got := pooledChargeMB(nil, headroomPoolRho); got != 0 {
		t.Fatalf("no leases charged %d", got)
	}
}

func TestComputeHeadroomPoolsRampingLeases(t *testing.T) {
	now := time.Date(2026, 11, 17, 16, 0, 0, 0, time.UTC)
	snapshot := Snapshot{Timestamp: now, Level: LevelNormal, PhysicalMemoryMB: 49152, FreePercent: 50}
	ramping := func(peak, median int64) WorkLeaseStatus {
		return WorkLeaseStatus{Class: WorkClassTest, StartedAt: now.Add(-time.Minute), PredictedPeakMB: peak, PredictedMedianMB: median}
	}
	leases := []WorkLeaseStatus{ramping(4845, 498), ramping(4845, 498), ramping(4845, 498),
		// Past the ramp: its memory is in the sample, not charged.
		{Class: WorkClassTest, StartedAt: now.Add(-10 * time.Minute), PredictedPeakMB: 4845, PredictedMedianMB: 498}}
	headroom := computeHeadroom(snapshot, true, leases, peakRSSHistory{}, now)
	if headroom.Rule != HeadroomRuleVersion || headroom.RampingLeaseCount != 3 || headroom.LeaseCount != 4 {
		t.Fatalf("headroom = %+v", headroom)
	}
	if headroom.LeasePeakSumMB != 3*4845 {
		t.Fatalf("peak sum = %d", headroom.LeasePeakSumMB)
	}
	want := pooledChargeMB([][2]int64{{4845, 498}, {4845, 498}, {4845, 498}}, headroomPoolRho)
	if headroom.LeasePredictedMB != want || headroom.InflightMB != want || want >= headroom.LeasePeakSumMB {
		t.Fatalf("charge = %d inflight = %d, want pooled %d below the peak sum %d", headroom.LeasePredictedMB, headroom.InflightMB, want, headroom.LeasePeakSumMB)
	}
	if headroom.HeadroomMB != headroom.AvailableMB-headroom.ReserveMB-want {
		t.Fatalf("headroom = %d", headroom.HeadroomMB)
	}
	// Leases from helpers before v5 carry no median: they are charged their
	// peak, as v4 did.
	old := []WorkLeaseStatus{ramping(3000, 0), ramping(3000, 0)}
	if got := computeHeadroom(snapshot, true, old, peakRSSHistory{}, now); got.LeasePredictedMB != 6000 {
		t.Fatalf("leases without a median charged %d, want 6000", got.LeasePredictedMB)
	}
}

func TestHeadroomV4RowsStayValid(t *testing.T) {
	event := validWorkEvent(resourceTestNow, WorkEventQueued)
	event.EventID = workEventID(event.OperationID, event.Event, event.LeaseID)
	for _, rule := range []string{HeadroomRuleVersion, headroomRuleVersionV4} {
		event.ShadowMemoryRule = rule
		if err := event.Validate(); err != nil {
			t.Fatalf("%s: %v", rule, err)
		}
	}
}

func TestLeaseRecordCarriesThePredictedMedian(t *testing.T) {
	coordinator := testWorkCoordinator(t)
	coordinator.PredictedPeakMB, coordinator.PredictedMedianMB = 4845, 498
	lease, status, err := coordinator.AcquireOperation(t.Context(), WorkClassTest, "00000000000000000000000000000042")
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release(t.Context())
	if len(status.Leases) != 1 || status.Leases[0].PredictedPeakMB != 4845 || status.Leases[0].PredictedMedianMB != 498 {
		t.Fatalf("leases = %+v", status.Leases)
	}
}
