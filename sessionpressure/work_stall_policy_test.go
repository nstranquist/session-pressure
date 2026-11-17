package sessionpressure

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func stallTestPolicy(capacity int) Policy {
	policy := DefaultPolicy(16384)
	w := &policy.WorkLimits
	w.Capacity, w.WarningCapacity, w.ExpressTestWeight, w.TestWeight, w.ExpressBuildWeight, w.BuildWeight = capacity, 4, 1, 2, 2, 5
	w.BrowserWeight, w.EmulatorWeight, w.HeavyWeight, w.BenchmarkWeight, w.ReclaimWeight = 2, 5, 6, 6, 1
	w.WarningCapacityEnabled = false
	// These tests pin the weighted stall rule; resource admission judges a
	// waiter by memory fit (TestStallGuardUnderResourceAdmission).
	w.Admission = WorkAdmissionWeighted
	return policy
}

func TestStallGuardPolicyFollowsThePersistedFile(t *testing.T) {
	dir := t.TempDir()
	monitor := &Monitor{Store: NewTelemetryStore(dir), Policy: stallTestPolicy(1000)}
	// No file: the in-memory policy.
	if got := monitor.stallGuardPolicy(16384).WorkLimits.Capacity; got != 1000 {
		t.Fatalf("no file: capacity %d", got)
	}
	if err := SavePolicy(PolicyPath(dir), stallTestPolicy(16)); err != nil {
		t.Fatal(err)
	}
	if got := monitor.stallGuardPolicy(16384).WorkLimits.Capacity; got != 16 {
		t.Fatalf("persisted edit not seen: capacity %d", got)
	}
	// A later edit is picked up (size changes with the capacity digits).
	if err := SavePolicy(PolicyPath(dir), stallTestPolicy(24)); err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(time.Minute)
	if err := os.Chtimes(PolicyPath(dir), future, future); err != nil {
		t.Fatal(err)
	}
	if got := monitor.stallGuardPolicy(16384).WorkLimits.Capacity; got != 24 {
		t.Fatalf("second edit not seen: capacity %d", got)
	}
	// A broken file keeps the last good copy.
	if err := os.WriteFile(PolicyPath(dir), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := monitor.stallGuardPolicy(16384).WorkLimits.Capacity; got != 24 {
		t.Fatalf("broken file: capacity %d", got)
	}
}

// Reproduces the 2026-11-16 00:50Z false alert: the resident still held
// capacity 1000 while the persisted policy said 16 and 16 units were in use.
func TestStallGuardUsesPersistedCapacity(t *testing.T) {
	run := func(persisted int) int {
		coordinator := testWorkCoordinator(t)
		limits := testWorkLimits()
		limits.Capacity = 1000
		coordinator.Limits = limits
		now := coordinator.Now()
		coordinator.Now = func() time.Time { return now }
		ctx := context.Background()
		for _, id := range []string{"00000000000000000000000000000021", "00000000000000000000000000000022", "00000000000000000000000000000023", "00000000000000000000000000000024"} {
			if _, _, err := coordinator.AcquireOperation(ctx, WorkClassBuild, id); err != nil { // 4 x 4 = 16 units
				t.Fatal(err)
			}
		}
		waiter, _, err := coordinator.RegisterWaiter(ctx, WorkClassTest, "00000000000000000000000000000031")
		if err != nil {
			t.Fatal(err)
		}
		if err := SavePolicy(PolicyPath(coordinator.Dir), stallTestPolicy(persisted)); err != nil {
			t.Fatal(err)
		}
		monitor := &Monitor{Store: NewTelemetryStore(coordinator.Dir), Policy: stallTestPolicy(1000), WorkCoordinator: coordinator}
		for step := 0; step <= 4; step++ {
			// The owner's retry at the live capacity refreshes its heartbeat.
			if _, _, err := waiter.TryAcquireWithCapacity(ctx, 16); err == nil {
				t.Fatal("waiter fitted at 16/16")
			}
			monitor.observeQueueStall(Snapshot{Timestamp: now, Level: LevelNormal, GuardRole: "resident", FreePercent: 60, PhysicalMemoryMB: 16384})
			now = now.Add(10 * time.Second)
		}
		body, _ := os.ReadFile(filepath.Join(coordinator.Dir, "work-control-events.jsonl"))
		return strings.Count(string(body), `"event":"work_queue_stall"`)
	}
	if got := run(16); got != 0 {
		t.Fatalf("false stall at persisted capacity 16: %d events", got)
	}
	if got := run(1000); got == 0 {
		t.Fatal("a real stall (capacity 1000, 16 used) was not detected")
	}
}

func TestResidentRestartsOnlyForARunnablePolicyChange(t *testing.T) {
	dir := t.TempDir()
	path := PolicyPath(dir)
	saves := 0
	save := func(policy Policy) {
		t.Helper()
		saves++
		if err := SavePolicy(path, policy); err != nil {
			t.Fatal(err)
		}
		// Each save gets a distinct mtime, as real edits seconds apart do.
		stamp := time.Now().Add(time.Duration(saves) * time.Minute)
		if err := os.Chtimes(path, stamp, stamp); err != nil {
			t.Fatal(err)
		}
	}
	base := stallTestPolicy(16)
	base.Enabled = true
	save(base)
	loaded, _, err := LoadPolicy(path, 16384)
	if err != nil {
		t.Fatal(err)
	}
	monitor := &Monitor{Store: NewTelemetryStore(dir), Policy: loaded}
	resident := Snapshot{GuardRole: "resident", PhysicalMemoryMB: 16384}
	if monitor.policyChangedOnDisk(resident) {
		t.Fatal("unchanged file restarts the resident")
	}
	bigger := base
	bigger.WorkLimits.Capacity = 24
	save(bigger)
	if monitor.policyChangedOnDisk(Snapshot{GuardRole: "manual", PhysicalMemoryMB: 16384}) {
		t.Fatal("a foreground monitor restarts")
	}
	if monitor.policyChangedOnDisk(Snapshot{GuardRole: "resident"}) {
		t.Fatal("restart without a measured memory size")
	}
	if !monitor.policyChangedOnDisk(resident) {
		t.Fatal("capacity edit does not restart the resident")
	}
	// A broken file keeps the resident on its last policy, even though the
	// last good copy (24) differs from the loaded one.
	if err := os.WriteFile(path, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if monitor.policyChangedOnDisk(resident) {
		t.Fatal("broken file restarts the resident")
	}
	disabled := bigger
	disabled.Enabled = false
	save(disabled)
	if monitor.policyChangedOnDisk(resident) {
		t.Fatal("disabled policy restarts the resident")
	}
}
