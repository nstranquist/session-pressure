//go:build darwin

package sessionpressure

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestResidentComponentCosts is an explicitly selected, bounded native probe.
// It reads counters only: no browser enforcement, policy edits or signals.
func TestResidentComponentCosts(t *testing.T) {
	if os.Getenv("PRESSURE_COMPONENT_COSTS") != "1" {
		t.Skip("set PRESSURE_COMPONENT_COSTS=1 for three native reads per component")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	dir, err := DataDir()
	if err != nil {
		t.Fatal(err)
	}
	policy, _, err := LoadPolicy(PolicyPath(dir), 0)
	if err != nil {
		t.Fatal(err)
	}
	// Status reconciles recovery state. Measure it against private copies so
	// this diagnostic cannot write the host's browser controls.
	browserDir := t.TempDir()
	for _, name := range []string{"browser-control.json", "latest.json", "policy.json"} {
		body, err := os.ReadFile(filepath.Join(dir, name))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(browserDir, name), body, 0600); err != nil {
			t.Fatal(err)
		}
	}
	sampler := NewResidentSampler().WithWorkCoordinator(NewWorkCoordinator(dir, policy.WorkLimits))
	if _, err := sampler.Sample(ctx, policy); err != nil {
		t.Fatal(err)
	}
	for _, probe := range []struct {
		name string
		run  func() error
	}{
		{"cached_sample", func() error { _, e := sampler.Sample(ctx, policy); return e }},
		{"self_rss", func() error { _, e := platformCurrentRSSSource()(); return e }},
		{"disk_device", func() error { _, e := nativeDiskDeviceCounter(ctx); return e }},
		{"disk_processes", func() error { _, e := nativeDiskProcessCounters(ctx); return e }},
		{"browser_roots", func() error { _, e := automatedBrowserProcesses(ctx); return e }},
		{"browser_status", func() error { _, e := BrowserControlStatus(ctx, browserDir, ""); return e }},
	} {
		var total time.Duration
		for i := 0; i < 3; i++ {
			before := processCPUTime()
			if err := probe.run(); err != nil {
				t.Fatalf("%s: %v", probe.name, err)
			}
			total += processCPUTime() - before
		}
		t.Logf("%s mean process CPU %.3f ms (3 reads)", probe.name, float64(total.Microseconds())/3000)
	}
}

func TestNativeCurrentRSSCounter(t *testing.T) {
	read := platformCurrentRSSSource()
	rss, err := read()
	if err != nil || rss <= 0 || rss > processPeakRSSMB()+2 {
		t.Fatalf("current RSS=%v peak=%v err=%v", rss, processPeakRSSMB(), err)
	}
}
