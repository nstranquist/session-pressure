package sessionpressure

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

func warningAdmission(reason string) Admission {
	dimension := "cpu"
	if !strings.HasPrefix(reason, "host CPU ") {
		dimension = "memory"
	}
	return Admission{
		Allowed:   true,
		Level:     LevelWarning,
		Dimension: dimension,
		Reasons:   []string{reason},
		Snapshot: &Snapshot{
			Level:   LevelWarning,
			Reasons: []string{reason},
		},
	}
}

func TestWarningCapacityDeratesNewAdmissions(t *testing.T) {
	limits := defaultWorkLimits(10)
	tests := []struct {
		name    string
		class   WorkClass
		free    int
		reason  string
		allowed bool
		label   string
	}{
		{"full build exceeds warning ceiling", WorkClassBuild, 8, "host CPU 86.0% >= warning 85.0%", false, WarningCapacityDeferredDecision},
		{"focused test fits warning ceiling", WorkClassTest, 8, "host CPU 86.0% >= warning 85.0%", true, WarningCapacityAdmittedDecision},
		{"arrival accounts for active leases", WorkClassBrowser, 4, "host CPU 86.0% >= warning 85.0%", false, WarningCapacityDeferredDecision},
		{"memory warning uses same headroom", WorkClassExpressBuild, 6, "host free memory 24% <= warning 25%", true, WarningCapacityAdmittedDecision},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			gate := &workAdmissionGate{
				limits: limits,
				class:  testCase.class,
				warningCapacityStatus: func() (WorkStatus, bool) {
					return WorkStatus{Used: limits.Capacity - testCase.free, Capacity: limits.Capacity}, true
				},
			}
			decision := gate.Observe(warningAdmission(testCase.reason), false)
			if decision.Allowed != testCase.allowed {
				t.Fatalf("allowed=%v want=%v decision=%+v", decision.Allowed, testCase.allowed, decision)
			}
			if label := gate.decisionLabel(); label != testCase.label {
				t.Fatalf("label=%q want=%q", label, testCase.label)
			}
			if !strings.Contains(decision.Reason, "effective_capacity=4 configured_capacity=8") {
				t.Fatalf("decision lacks capacity evidence: %q", decision.Reason)
			}
		})
	}
}

func TestAdmissionProjectsTypedWarningDimension(t *testing.T) {
	policy := DefaultPolicy(16 * 1024)
	cpu := AdmissionForSnapshot(Snapshot{
		FreePercent: 60, HostCPUAvailable: true,
		HostCPUPercent: policy.Thresholds.HostCPUWarningPercent,
	}, policy, "test")
	if cpu.Level != LevelWarning || cpu.Dimension != "cpu" {
		t.Fatalf("CPU warning admission=%+v", cpu)
	}
	memory := AdmissionForSnapshot(Snapshot{
		FreePercent:      int(policy.Thresholds.FreeWarningPercent),
		HostCPUAvailable: true, HostCPUPercent: policy.Thresholds.HostCPUWarningPercent,
	}, policy, "test")
	if memory.Level != LevelWarning || memory.Dimension != "memory" {
		t.Fatalf("memory warning admission=%+v", memory)
	}
}

func TestAdmissionKeepsUncorroboratedCPURedAdvisory(t *testing.T) {
	policy := DefaultPolicy(16 * 1024)
	policy.EnforceAdmission = true
	admission := AdmissionForWorkSnapshot(Snapshot{
		FreePercent: 60, HostCPUAvailable: true, HostCPUPercent: 99,
		ThermalState: ThermalStateNominal,
	}, policy, "test")
	if !admission.Allowed || admission.Level != LevelRed || admission.Dimension != "cpu" {
		t.Fatalf("uncorroborated CPU red admission=%+v", admission)
	}
	if admission.Controller == nil || admission.Controller.CPUStress || admission.Controller.BlockWork {
		t.Fatalf("uncorroborated CPU red controller=%+v", admission.Controller)
	}
}

func TestAdmissionBlocksCorroboratedCPURed(t *testing.T) {
	policy := DefaultPolicy(16 * 1024)
	policy.EnforceAdmission = true
	admission := AdmissionForSnapshot(Snapshot{
		FreePercent: 60, HostCPUAvailable: true, HostCPUPercent: 99,
		HostCPURollingAvailable: true, HostCPURollingPercent: 97,
		ThermalState: ThermalStateNominal,
	}, policy, "test")
	if admission.Allowed || admission.Level != LevelRed || admission.Dimension != "cpu" {
		t.Fatalf("corroborated CPU red admission=%+v", admission)
	}
	if admission.Controller == nil || !admission.Controller.CPUStress || !admission.Controller.BlockWork {
		t.Fatalf("corroborated CPU red controller=%+v", admission.Controller)
	}
}

func TestWorkGateDoesNotHoldUncorroboratedCPURed(t *testing.T) {
	limits := defaultWorkLimits(10)
	gate := &workAdmissionGate{limits: limits, cpuRedPercent: 95}
	admission := Admission{
		Allowed: true, Level: LevelRed, Dimension: "cpu",
		Snapshot: &Snapshot{HostCPUAvailable: true, HostCPUPercent: 99},
	}
	decision := gate.Observe(admission, false)
	if !decision.Allowed || gate.latched {
		t.Fatalf("uncorroborated CPU red was held: decision=%+v gate=%+v", decision, gate)
	}
}

func TestWarningCapacityLeavesActiveLeaseRunning(t *testing.T) {
	coordinator := testWorkCoordinator(t)
	coordinator.Limits = defaultWorkLimits(10)
	lease, _, err := coordinator.Acquire(context.Background(), WorkClassBuild)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lease.Release(context.Background()) }()

	gate := &workAdmissionGate{
		limits: coordinator.Limits,
		class:  WorkClassExpressTest,
		warningCapacityStatus: func() (WorkStatus, bool) {
			status, statusErr := coordinator.Status(context.Background())
			return status, statusErr == nil
		},
	}
	decision := gate.Observe(warningAdmission("host CPU 86.0% >= warning 85.0%"), false)
	if decision.Allowed {
		t.Fatalf("arrival admitted above derated ceiling: %+v", decision)
	}
	status, err := coordinator.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.Used != coordinator.Limits.BuildWeight || len(status.Leases) != 1 {
		t.Fatalf("active lease was disturbed: %+v", status)
	}
}

func TestWarningCapacityDoesNotClaimNormalRedOrStoragePolicy(t *testing.T) {
	limits := defaultWorkLimits(10)
	for _, admission := range []Admission{
		{Allowed: true, Level: LevelNormal, Snapshot: &Snapshot{Level: LevelNormal}},
		{Allowed: false, Level: LevelRed, Reasons: []string{"host CPU 96.0% >= red 95.0%"}, Snapshot: &Snapshot{Level: LevelRed}},
		{Allowed: true, Level: LevelWarning, Dimension: "storage", Reasons: []string{"storage free 19 GiB < warning 20 GiB"}, Snapshot: &Snapshot{Level: LevelWarning}},
	} {
		gate := &workAdmissionGate{
			limits: limits,
			class:  WorkClassBuild,
			warningCapacityStatus: func() (WorkStatus, bool) {
				return WorkStatus{Capacity: limits.Capacity}, true
			},
		}
		if _, owned := gate.warningCapacityDecision(admission, false); owned {
			t.Fatalf("warning derater claimed unrelated admission: %+v", admission)
		}
	}
}

func TestWarningCapacityFailsOpenWhenCoordinatorStatusUnavailable(t *testing.T) {
	gate := &workAdmissionGate{
		limits: defaultWorkLimits(10), class: WorkClassBuild,
		warningCapacityStatus: func() (WorkStatus, bool) { return WorkStatus{}, false },
	}
	if _, owned := gate.warningCapacityDecision(warningAdmission("host CPU 86.0% >= warning 85.0%"), false); owned {
		t.Fatal("warning derating must not turn missing coordinator evidence into a host-wide block")
	}
}

func TestWarningEffectiveCapacityHonorsProfile(t *testing.T) {
	admission := warningAdmission("host CPU 86.0% >= warning 85.0%")
	limits := defaultWorkLimits(10)
	if got := warningEffectiveCapacity(admission, limits); got != limits.WarningCapacity {
		t.Fatalf("interactive warning capacity=%d want=%d", got, limits.WarningCapacity)
	}
	limits.WarningCapacityEnabled = false
	if got := warningEffectiveCapacity(admission, limits); got != limits.Capacity {
		t.Fatalf("advisory warning capacity=%d want normal capacity=%d", got, limits.Capacity)
	}
}

func TestWarningCapacityNeverBypassesRedLatch(t *testing.T) {
	limits := defaultWorkLimits(10)
	latched := WorkAdmissionLatch{Latched: true}
	gate := &workAdmissionGate{
		limits: limits,
		class:  WorkClassExpressTest,
		warningCapacityStatus: func() (WorkStatus, bool) {
			return WorkStatus{Capacity: limits.Capacity, AdmissionLatch: &latched}, true
		},
	}
	if _, owned := gate.warningCapacityDecision(warningAdmission("host CPU 86.0% >= warning 85.0%"), false); owned {
		t.Fatal("warning derating bypassed the durable red latch")
	}
	gate.latched = true
	latched.Latched = false
	if _, owned := gate.warningCapacityDecision(warningAdmission("host CPU 86.0% >= warning 85.0%"), false); owned {
		t.Fatal("warning derating bypassed the process-local red latch")
	}
}

func TestWarningCapacityDecisionsAreCountedOncePerOperation(t *testing.T) {
	now := time.Date(2026, 7, 29, 18, 0, 0, 0, time.UTC)
	operationID := "00000000000000000000000000000001"
	events := []WorkEvent{
		{OperationID: operationID, Class: WorkClassBuild, Event: WorkEventQueued, AdmissionDecision: WarningCapacityDeferredDecision},
		{OperationID: operationID, Class: WorkClassBuild, Event: WorkEventAcquired, AdmissionDecision: WarningCapacityDeferredDecision},
		{OperationID: operationID, Class: WorkClassBuild, Event: WorkEventCompleted, AdmissionDecision: WarningCapacityDeferredDecision, RuntimeMillis: 1},
		{OperationID: "00000000000000000000000000000002", Class: WorkClassTest, Event: WorkEventCompleted, AdmissionDecision: WarningCapacityAdmittedDecision, RuntimeMillis: 1},
	}
	signals := SummarizeWorkEvents(events, now, now.Add(time.Second)).ReviewSignals
	if signals.WarningCapacityDeferrals != 1 || signals.WarningCapacityAdmissions != 1 {
		t.Fatalf("warning capacity telemetry=%+v", signals)
	}
}

func TestWarningCapacityOwnedLeaseThatFitsStarts(t *testing.T) {
	coordinator := testWorkCoordinator(t)
	coordinator.Limits = defaultWorkLimits(10)
	lease, status, err := coordinator.AcquireOperationWithCapacity(
		context.Background(), WorkClassTest, "00000000000000000000000000000011", coordinator.Limits.WarningCapacity,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lease.Release(context.Background()) }()
	gate := &workAdmissionGate{
		limits: coordinator.Limits, class: WorkClassTest,
		warningCapacityStatus: func() (WorkStatus, bool) { return status, true },
	}
	decision := gate.ObserveOwned(warningAdmission("host CPU 86.0% >= warning 85.0%"), false)
	if !decision.Allowed || status.Used != coordinator.Limits.TestWeight {
		t.Fatalf("fitting owned lease was derated twice: status=%+v decision=%+v", status, decision)
	}
}

func TestWarningCapacityConcurrentArrivalsCannotOversubscribe(t *testing.T) {
	coordinator := testWorkCoordinator(t)
	coordinator.Limits = defaultWorkLimits(10)
	type result struct {
		lease *WorkLease
		err   error
	}
	start := make(chan struct{})
	results := make(chan result, 2)
	var group sync.WaitGroup
	for _, operationID := range []string{
		"00000000000000000000000000000021",
		"00000000000000000000000000000022",
	} {
		group.Add(1)
		go func(id string) {
			defer group.Done()
			<-start
			lease, _, err := coordinator.AcquireOperationWithCapacity(context.Background(), WorkClassTest, id, coordinator.Limits.WarningCapacity)
			results <- result{lease: lease, err: err}
		}(operationID)
	}
	close(start)
	group.Wait()
	close(results)
	acquired, blocked := 0, 0
	for outcome := range results {
		if outcome.err == nil {
			acquired++
			defer func(lease *WorkLease) { _ = lease.Release(context.Background()) }(outcome.lease)
		} else if errors.Is(outcome.err, ErrWorkCapacity) {
			blocked++
		} else {
			t.Fatalf("unexpected concurrent acquisition error: %v", outcome.err)
		}
	}
	if acquired != 1 || blocked != 1 {
		t.Fatalf("concurrent warning acquisitions acquired=%d blocked=%d", acquired, blocked)
	}
	status, err := coordinator.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.Used > coordinator.Limits.WarningCapacity {
		t.Fatalf("warning ceiling oversubscribed: %+v", status)
	}
}

func TestWarningCapacityAllowsExactCeiling(t *testing.T) {
	coordinator := testWorkCoordinator(t)
	coordinator.Limits = defaultWorkLimits(10)
	first, _, err := coordinator.AcquireOperationWithCapacity(
		context.Background(), WorkClassExpressBuild, "00000000000000000000000000000031", coordinator.Limits.WarningCapacity,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = first.Release(context.Background()) }()
	second, status, err := coordinator.AcquireOperationWithCapacity(
		context.Background(), WorkClassExpressBuild, "00000000000000000000000000000032", coordinator.Limits.WarningCapacity,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = second.Release(context.Background()) }()
	if status.Used != coordinator.Limits.WarningCapacity {
		t.Fatalf("exact warning ceiling status=%+v", status)
	}
}

func TestWarningAppearingWhileQueuedCreatesNonConsumingReservation(t *testing.T) {
	coordinator := testWorkCoordinator(t)
	coordinator.Limits = defaultWorkLimits(10)
	active, _, err := coordinator.AcquireOperation(
		context.Background(), WorkClassTest, "00000000000000000000000000000041",
	)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = active.Release(context.Background()) }()
	waiter, _, err := coordinator.RegisterWaiter(
		context.Background(), WorkClassTest, "00000000000000000000000000000042",
	)
	if err != nil {
		t.Fatal(err)
	}
	queuedLease, status, err := waiter.TryAcquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	gate := &workAdmissionGate{
		limits: coordinator.Limits, class: WorkClassTest,
		warningCapacityStatus: func() (WorkStatus, bool) {
			current, statusErr := coordinator.Status(context.Background())
			return current, statusErr == nil
		},
	}
	if decision := gate.ObserveOwned(warningAdmission("host CPU 86.0% >= warning 85.0%"), false); decision.Allowed {
		t.Fatalf("queued lease above warning ceiling was admitted: status=%+v decision=%+v", status, decision)
	}
	reserved, reservedStatus, err := queuedLease.ReserveForPressure(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reserved.Cancel(context.Background()) }()
	if reservedStatus.Used != coordinator.Limits.TestWeight || reservedStatus.QueueDepth != 1 {
		t.Fatalf("pressure reservation consumed capacity: %+v", reservedStatus)
	}
}
