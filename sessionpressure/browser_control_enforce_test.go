package sessionpressure

import (
	"context"
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
		if browserControlStops(false, age, limit, false) {
			t.Fatalf("young browser (%s) stopped on an unpaused host", age)
		}
	}
	if !browserControlStops(false, 121*time.Second, limit, false) {
		t.Fatal("browser past its finite lifetime kept running")
	}
	if browserControlStops(false, 9*time.Minute, limit, true) {
		t.Fatal("idle-leased managed session stopped at the fixed lifetime")
	}
	if !browserControlStops(true, time.Second, limit, true) {
		t.Fatal("operator pause did not stop a young browser")
	}
}

func TestBrowserOperatorPauseStopsRunningWorkButAutoPauseDoesNot(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(DataDirEnv, dir)
	// Auto mode with no resident sample is blocked, the same state as a CPU
	// pause: it gates admission but leaves running work alone.
	if browserOperatorPaused(dir) {
		t.Fatal("automatic pause reported as an operator pause")
	}
	ctx := context.Background()
	if _, err := BrowserControlStatus(ctx, dir, "paused"); err != nil {
		t.Fatal(err)
	}
	if !browserOperatorPaused(dir) {
		t.Fatal("operator pause not reported")
	}
}
