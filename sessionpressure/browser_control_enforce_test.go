package sessionpressure

import (
	"testing"
	"time"
)

func TestBrowserControlStopsHasNoCountLimit(t *testing.T) {
	limit := browserControlMaxRuntime(BrowserControl{})
	if limit != 120*time.Second {
		t.Fatalf("default lifetime = %s", limit)
	}
	if got := browserControlMaxRuntime(BrowserControl{MaxRuntimeSeconds: 300}); got != 300*time.Second {
		t.Fatalf("configured lifetime = %s", got)
	}
	// Three young browsers on an unpaused host all keep running: a CSP
	// sweep's session, a screenshot and another agent's smoke run.
	for _, age := range []time.Duration{time.Second, 4 * time.Second, 21 * time.Second} {
		if browserControlStops(false, age, limit) {
			t.Fatalf("young browser (%s) stopped on an unpaused host", age)
		}
	}
	if !browserControlStops(false, 121*time.Second, limit) {
		t.Fatal("browser past its finite lifetime kept running")
	}
	if !browserControlStops(true, time.Second, limit) {
		t.Fatal("pause did not stop a young browser")
	}
}
