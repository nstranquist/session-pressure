package sessionpressurecmd

import (
	"testing"

	"github.com/nstranquist/session-pressure/sessionpressure"
)

func TestAuditCapacityAccountsForParkedLeases(t *testing.T) {
	lease := func(weight int, parked bool, class sessionpressure.WorkClass) sessionpressure.WorkLeaseStatus {
		return sessionpressure.WorkLeaseStatus{Weight: weight, Parked: parked, Class: class}
	}
	for _, tc := range []struct {
		name   string
		status sessionpressure.WorkStatus
		want   bool
	}{
		{"idle parked build", sessionpressure.WorkStatus{Capacity: 8, Used: 4, Available: 4, ParkedWeight: 5, Leases: []sessionpressure.WorkLeaseStatus{lease(5, true, sessionpressure.WorkClassBuild), lease(4, false, sessionpressure.WorkClassTest)}}, true},
		{"missing parked total", sessionpressure.WorkStatus{Capacity: 8, Used: 4, Available: 4, Leases: []sessionpressure.WorkLeaseStatus{lease(5, true, sessionpressure.WorkClassBuild), lease(4, false, sessionpressure.WorkClassTest)}}, false},
		{"parked charged twice", sessionpressure.WorkStatus{Capacity: 8, Used: 5, Available: 3, ParkedWeight: 5, Leases: []sessionpressure.WorkLeaseStatus{lease(5, true, sessionpressure.WorkClassBuild)}}, false},
		{"negative parked weight", sessionpressure.WorkStatus{Capacity: 8, Available: 8, ParkedWeight: -1, Leases: []sessionpressure.WorkLeaseStatus{lease(-1, true, sessionpressure.WorkClassBuild)}}, false},
		{"unattributed overcommit", sessionpressure.WorkStatus{Capacity: 8, Used: 9, Available: 0, Leases: []sessionpressure.WorkLeaseStatus{lease(9, false, sessionpressure.WorkClassBuild)}}, false},
		{"parked express does not excuse overcommit", sessionpressure.WorkStatus{Capacity: 8, Used: 9, Available: 0, ParkedWeight: 2, Leases: []sessionpressure.WorkLeaseStatus{lease(9, false, sessionpressure.WorkClassBuild), lease(2, true, sessionpressure.WorkClassExpressBuild)}}, false},
		{"bounded active express", sessionpressure.WorkStatus{Capacity: 8, Used: 9, Available: 0, Leases: []sessionpressure.WorkLeaseStatus{lease(7, false, sessionpressure.WorkClassBuild), lease(2, false, sessionpressure.WorkClassExpressBuild)}}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, got := pressureWorkCapacityInvariant(tc.status)
			if got != tc.want {
				t.Fatalf("capacity invariant=%v want %v: %+v", got, tc.want, tc.status)
			}
		})
	}
}
