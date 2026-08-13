package sessionpressure

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"
)

func TestAttributeCoordinatedWorkUsesClosestLeaseRootWithoutIdentityProjection(t *testing.T) {
	now := time.Date(2026, 7, 16, 19, 30, 0, 0, time.UTC)
	processes := []Process{
		{PID: 100, PPID: 1, RSSKB: 1024, CPUPercent: 100, CPUAvailable: true},
		{PID: 101, PPID: 100, RSSKB: 2048, CPUPercent: 50, CPUAvailable: true},
		{PID: 200, PPID: 101, RSSKB: 1024, CPUPercent: 20, CPUAvailable: true},
		{PID: 201, PPID: 200, RSSKB: 1024, CPUPercent: 30, CPUAvailable: true},
		{PID: 300, PPID: 1, RSSKB: 4096, CPUPercent: 400, CPUAvailable: true},
	}
	leases := []WorkLeaseStatus{
		{PID: 100, Class: WorkClassTest},
		{PID: 200, Class: WorkClassBuild},
		{PID: 999, Class: WorkClassHeavy},
	}
	result := attributeCoordinatedWork(processes, leases, now, 10)
	if !result.Available || !result.Fresh || result.LeaseCount != 3 || result.AttributedLeaseCount != 2 || result.UnattributedLeaseCount != 1 {
		t.Fatalf("unexpected lease attribution: %+v", result)
	}
	if result.ProcessCount != 4 || math.Abs(result.RSSSumMB-5) > 0.001 || math.Abs(result.CPUPercent-20) > 0.001 || result.CPUAvailable {
		t.Fatalf("unexpected aggregate usage: %+v", result)
	}
	if len(result.ByClass) != 3 {
		t.Fatalf("expected three active class rows, got %+v", result.ByClass)
	}
	byClass := map[WorkClass]CoordinatedWorkClassUsage{}
	for _, usage := range result.ByClass {
		byClass[usage.Class] = usage
	}
	if usage := byClass[WorkClassTest]; usage.ProcessCount != 2 || math.Abs(usage.CPUPercent-15) > 0.001 || !usage.CPUAvailable {
		t.Fatalf("nested build leaked into test usage: %+v", usage)
	}
	if usage := byClass[WorkClassBuild]; usage.ProcessCount != 2 || math.Abs(usage.CPUPercent-5) > 0.001 || !usage.CPUAvailable {
		t.Fatalf("unexpected build usage: %+v", usage)
	}
	if usage := byClass[WorkClassHeavy]; usage.ProcessCount != 0 || usage.CPUAvailable {
		t.Fatalf("missing heavy root must remain unavailable: %+v", usage)
	}
	body, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{`"pid"`, `"operation_id"`, `"lease_id"`, `"command"`, `"path"`, `"environment"`} {
		if strings.Contains(string(body), forbidden) {
			t.Fatalf("privacy-safe attribution exposed %s: %s", forbidden, body)
		}
	}
}

func TestSampleCoordinatedWorkRefreshesLeasesWhileReusingProcessInventory(t *testing.T) {
	now := time.Now().UTC()
	calls := 0
	sampler := &Sampler{workStatusSource: func(context.Context) (WorkStatus, error) {
		calls++
		if calls > 1 {
			return WorkStatus{}, nil
		}
		return WorkStatus{Leases: []WorkLeaseStatus{{PID: 10, Class: WorkClassTest}}}, nil
	}}
	processes := []Process{{PID: 10, PPID: 1, RSSKB: 1024, CPUAvailable: true, CPUPercent: 10}}
	fresh := sampler.sampleCoordinatedWork(context.Background(), processes, now, true, 10)
	reused := sampler.sampleCoordinatedWork(context.Background(), processes, now, false, 10)
	if calls != 2 || !fresh.Fresh || reused.Fresh || !reused.Available || reused.LeaseCount != 0 || reused.ProcessCount != 0 {
		t.Fatalf("unexpected inventory/lease refresh behavior calls=%d fresh=%+v reused=%+v", calls, fresh, reused)
	}
}

func TestCoordinatedWorkStatusFailureIsDiagnosticOnly(t *testing.T) {
	now := time.Now().UTC()
	sampler := &Sampler{workStatusSource: func(context.Context) (WorkStatus, error) {
		return WorkStatus{}, fmt.Errorf("lease state unavailable")
	}}
	result := sampler.sampleCoordinatedWork(context.Background(), []Process{{PID: 1}}, now, true, 10)
	if result.Available || result.Error != "lease state unavailable" || !result.Fresh || result.ByClass == nil {
		t.Fatalf("unexpected unavailable projection: %+v", result)
	}
	policy := DefaultPolicy(16 * 1024)
	base := Snapshot{HostCPUAvailable: true, HostCPUPercent: 96, FreePercent: 50, MemoryMomentum: MemoryMomentumUnknown}
	without := Evaluate(base, policy)
	base.CoordinatedWork = CoordinatedWorkSnapshot{Available: true, CPUAvailable: true, CPUPercent: 95, LeaseCount: 2}
	with := Evaluate(base, policy)
	if with.Level != without.Level || strings.Join(with.Reasons, "\n") != strings.Join(without.Reasons, "\n") {
		t.Fatalf("diagnostic attribution changed admission evidence: without=%+v with=%+v", without, with)
	}
}

func TestWorkStatsSeparateCalibrationCohortsAndCPUAttribution(t *testing.T) {
	events := make([]WorkEvent, 0, 80)
	appendCohort := func(weight, selector, count int, waitMS int64, attribution string) {
		for index := 0; index < count; index++ {
			operationID := fmt.Sprintf("%032x", len(events)+1)
			queued := WorkEvent{
				OperationID: operationID, Class: WorkClassBuild, Weight: weight, Event: WorkEventQueued,
				SchedulingPolicy: WorkSchedulingPolicy, SelectorSchemaVersion: selector,
			}
			if index == 0 && attribution != "" {
				queued.PressureDimension = "cpu"
				switch attribution {
				case "active":
					queued.CoordinatedWorkAttributionAvailable = true
					queued.CoordinatedWorkCPUAvailable = true
					queued.CoordinatedWorkCPUPercent = 40
				case "idle":
					queued.CoordinatedWorkAttributionAvailable = true
					queued.CoordinatedWorkCPUAvailable = true
				case "unavailable":
				}
			}
			events = append(events, queued, WorkEvent{
				OperationID: operationID, Class: WorkClassBuild, Weight: weight, Event: WorkEventCompleted,
				WaitMilliseconds: waitMS, RuntimeMillis: 10_000,
				SchedulingPolicy: WorkSchedulingPolicy, SelectorSchemaVersion: selector,
			})
		}
	}
	appendCohort(6, 2, 20, 100_000, "active")
	appendCohort(5, 4, 20, 0, "idle")
	appendCohort(5, 4, 1, 0, "unavailable")
	stats := SummarizeWorkEvents(events, time.Now().Add(-time.Hour), time.Now())
	if stats.ReviewSignals.CPUOnlyDeferrals != 3 || stats.ReviewSignals.CPUDeferralsWithCoordinatedWork != 1 || stats.ReviewSignals.CPUDeferralsWithoutCoordinatedWork != 1 || stats.ReviewSignals.CPUDeferralsAttributionUnavailable != 1 {
		t.Fatalf("unexpected CPU attribution signals: %+v", stats.ReviewSignals)
	}
	if len(stats.CalibrationCohorts) != 2 {
		t.Fatalf("expected two isolated cohorts, got %+v", stats.CalibrationCohorts)
	}
	var legacy, current WorkCalibrationCohort
	for _, cohort := range stats.CalibrationCohorts {
		if cohort.Weight == 6 {
			legacy = cohort
		} else if cohort.Weight == 5 {
			current = cohort
		}
	}
	if legacy.Status != "breached" || legacy.Current || legacy.TerminalRuntimeSamples != 20 || legacy.P95BoundedSlowdown != 11 {
		t.Fatalf("unexpected legacy cohort: %+v", legacy)
	}
	if current.Status != "met" || !current.Current || current.TerminalRuntimeSamples != 21 || current.P95BoundedSlowdown != 1 || current.SelectorSchemaVersion != 4 {
		t.Fatalf("unexpected current cohort: %+v", current)
	}
}

func TestWorkEventRejectsInvalidCoordinatedCPUEvidence(t *testing.T) {
	event := WorkEvent{
		SchemaVersion: WorkEventSchemaVersion, EventID: workEventID(workTestOperation, WorkEventQueued, ""),
		Timestamp: time.Now(), Event: WorkEventQueued, OperationID: workTestOperation,
		Class: WorkClassTest, Weight: 3, CoordinatedWorkCPUAvailable: true, CoordinatedWorkCPUPercent: 10,
	}
	if err := event.Validate(); err == nil || !strings.Contains(err.Error(), "requires available attribution") {
		t.Fatalf("expected attribution validation failure, got %v", err)
	}
	event.CoordinatedWorkAttributionAvailable = true
	event.CoordinatedWorkCPUPercent = math.Inf(1)
	if err := event.Validate(); err == nil || !strings.Contains(err.Error(), "between 0 and 100") {
		t.Fatalf("expected CPU range validation failure, got %v", err)
	}
}

func TestApplyCoordinatedWorkEvidenceOnlyRetainsCPUDeferrals(t *testing.T) {
	admission := Admission{Snapshot: &Snapshot{CoordinatedWork: CoordinatedWorkSnapshot{
		Available: true, CPUAvailable: true, CPUPercent: 42.5, LeaseCount: 2, ProcessCount: 7, InventoryAgeSeconds: 17,
	}}}
	cpuEvent := WorkEvent{}
	applyCoordinatedWorkEvidence(&cpuEvent, admission, "cpu")
	if !cpuEvent.CoordinatedWorkAttributionAvailable || !cpuEvent.CoordinatedWorkCPUAvailable || cpuEvent.CoordinatedWorkCPUPercent != 42.5 || cpuEvent.CoordinatedWorkLeaseCount != 2 || cpuEvent.CoordinatedWorkProcessCount != 7 || cpuEvent.CoordinatedWorkInventoryAgeSeconds != 17 {
		t.Fatalf("CPU deferral omitted bounded attribution: %+v", cpuEvent)
	}
	memoryEvent := WorkEvent{}
	applyCoordinatedWorkEvidence(&memoryEvent, admission, "memory")
	if memoryEvent.CoordinatedWorkAttributionAvailable || memoryEvent.CoordinatedWorkCPUPercent != 0 {
		t.Fatalf("non-CPU event carried unnecessary attribution: %+v", memoryEvent)
	}
}
