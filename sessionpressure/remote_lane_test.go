package sessionpressure

import "testing"

func TestRemoteLaneIsLightOnlyForExpressGoTest(t *testing.T) {
	for _, c := range []struct {
		class WorkClass
		argv  []string
		lane  string
	}{
		{WorkClassExpressTest, []string{"go", "test", "./pkg/"}, "light"},
		{WorkClassExpressTest, []string{"go", "vet", "./pkg/"}, "heavy"},
		{WorkClass("test"), []string{"go", "test", "./..."}, "heavy"},
		{WorkClass("build"), []string{"go", "build", "./..."}, "heavy"},
	} {
		lane, procs, memlimit, parallel := remoteLane(WorkRunOptions{Class: c.class, Command: c.argv})
		if lane != c.lane {
			t.Fatalf("%s %v: lane %s, want %s", c.class, c.argv, lane, c.lane)
		}
		if lane == "light" && (procs != "1" || memlimit != "768MiB" || parallel != "1") {
			t.Fatalf("light limits %s/%s/%s do not fit the 1 CPU, 1 GiB lane", procs, memlimit, parallel)
		}
	}
}
