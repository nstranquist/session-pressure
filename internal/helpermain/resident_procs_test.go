package helpermain

import "testing"

func TestResidentGOMAXPROCS(t *testing.T) {
	for _, tc := range []struct {
		env  string
		cpus int
		want int
	}{
		{"", 14, residentMaxProcs},
		{"", 1, 1},
		{"", 0, 1},
		{"8", 14, 0},
		{" ", 14, residentMaxProcs},
	} {
		if got := residentGOMAXPROCS(tc.env, tc.cpus); got != tc.want {
			t.Fatalf("residentGOMAXPROCS(%q, %d) = %d, want %d", tc.env, tc.cpus, got, tc.want)
		}
	}
}
