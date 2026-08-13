package sessionpressure

import (
	"testing"
	"time"
)

// qualifyingFastLaneInputs is the shape the fast lane is meant to admit: a
// weight-1 express job with five seconds of measured p95 runtime, free capacity
// to fit it, and a red reading that unmanaged processes — not leased work — are
// responsible for.
func qualifyingFastLaneInputs() fastLaneInputs {
	return fastLaneInputs{
		Enabled:                  true,
		Dimension:                "cpu",
		Weight:                   1,
		MaxWeight:                2,
		CalibratedP95RuntimeMS:   5_000,
		CalibrationAvailable:     true,
		MaxRuntimeMS:             120_000,
		FreeCapacity:             6,
		Capacity:                 8,
		CoordinatedCPUAvailable:  true,
		CoordinatedCPUPercent:    12,
		CoordinatedCPUCeilingPct: 50,
	}
}

// TestFastLaneWithoutAttributionUsesIdleCapacityAsEvidence covers the common case:
// coordinated-work CPU attribution was measured available only 21% of the time, so
// failing closed on its absence left the fast lane inert. A majority-idle weighted
// ceiling answers the same question — the coordinator cannot burn CPU it never
// admitted — but a busy ceiling still refuses, so the inference stays honest.
func TestFastLaneWithoutAttributionUsesIdleCapacityAsEvidence(t *testing.T) {
	for _, testCase := range []struct {
		name     string
		free     int
		capacity int
		admit    bool
	}{
		{"majority idle admits", 6, 8, true},
		{"exactly half idle admits", 4, 8, true},
		{"minority idle refuses", 3, 8, false},
		{"unknown capacity refuses", 6, 0, false},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			inputs := qualifyingFastLaneInputs()
			inputs.CoordinatedCPUAvailable = false
			inputs.FreeCapacity = testCase.free
			inputs.Capacity = testCase.capacity
			admitted, refusal := evaluateFastLane(inputs)
			if admitted != testCase.admit {
				t.Fatalf("admitted=%v want=%v refusal=%q", admitted, testCase.admit, refusal)
			}
			if !admitted && refusal != fastLaneCoordinatedDominant {
				t.Fatalf("refusal=%q", refusal)
			}
		})
	}
}

func TestFastLaneAdmitsLightShortWorkWhenCapacityIsFree(t *testing.T) {
	admitted, refusal := evaluateFastLane(qualifyingFastLaneInputs())
	if !admitted || refusal != "" {
		t.Fatalf("qualifying express work refused: refusal=%q", refusal)
	}
}

// TestFastLaneAdmitsThisHostsMeasuredWorkload pins the shipped defaults against
// the real distribution they were calibrated from. The first implementation was
// inert against exactly these numbers — a 60 s ceiling refused every class
// (express-test p95 76.6 s) and absent attribution refused the remaining 79% — so
// the feature would have shipped doing nothing. If a future default drifts back
// past the workload, this fails instead of silently going quiet again.
func TestFastLaneAdmitsThisHostsMeasuredWorkload(t *testing.T) {
	shipped := defaultWorkLimits(10)
	for _, measured := range []struct {
		class     WorkClass
		weight    int
		p95MS     int64
		admit     bool
		rationale string
	}{
		{WorkClassExpressTest, 1, 76_600, true, "highest-volume class, 1053 ops"},
		{WorkClassExpressBuild, 2, 74_300, true, "second-highest, 946 ops"},
		{WorkClassBrowser, 2, 302_200, false, "genuinely long-lived sessions"},
	} {
		t.Run(string(measured.class), func(t *testing.T) {
			admitted, refusal := evaluateFastLane(fastLaneInputs{
				Enabled:                true,
				Dimension:              "cpu",
				Weight:                 measured.weight,
				MaxWeight:              shipped.FastLaneMaxWeight,
				CalibratedP95RuntimeMS: measured.p95MS,
				CalibrationAvailable:   true,
				MaxRuntimeMS:           shipped.FastLaneMaxRuntimeMS,
				// The observed shape when the guard bites: most of the ceiling idle
				// while the host is CPU-red, and attribution absent (21% available).
				FreeCapacity:             5,
				Capacity:                 shipped.Capacity,
				CoordinatedCPUAvailable:  false,
				CoordinatedCPUCeilingPct: shipped.FastLaneCoordinatedCPUCeilingPct,
			})
			if admitted != measured.admit {
				t.Fatalf("%s (%s): admitted=%v want=%v refusal=%q",
					measured.class, measured.rationale, admitted, measured.admit, refusal)
			}
		})
	}
}

func TestFastLaneRefusesEverythingItShould(t *testing.T) {
	for _, testCase := range []struct {
		name    string
		mutate  func(*fastLaneInputs)
		refusal fastLaneRefusal
	}{
		{"disabled by policy", func(in *fastLaneInputs) { in.Enabled = false }, fastLaneDisabled},
		// Memory, swap, and storage are the dimensions this guard was actually
		// built for. They must never be fast-laned.
		{"memory pressure", func(in *fastLaneInputs) { in.Dimension = "memory" }, fastLaneWrongDimension},
		{"storage pressure", func(in *fastLaneInputs) { in.Dimension = "storage" }, fastLaneWrongDimension},
		{"weight above ceiling", func(in *fastLaneInputs) { in.Weight = 5 }, fastLaneTooHeavy},
		{"zero ceiling admits nothing", func(in *fastLaneInputs) { in.MaxWeight = 0 }, fastLaneTooHeavy},
		{"no calibration history", func(in *fastLaneInputs) { in.CalibrationAvailable = false }, fastLaneCalibrationMissing},
		{"runtime above ceiling", func(in *fastLaneInputs) { in.CalibratedP95RuntimeMS = 300_000 }, fastLaneTooLong},
		// The weighted ceiling stays authoritative: the fast lane decides who may
		// contend for capacity, never how much capacity exists.
		{"capacity cannot fit it", func(in *fastLaneInputs) { in.FreeCapacity = 0 }, fastLaneNoCapacity},
		// Attribution absent AND the ceiling mostly busy: neither source of
		// evidence clears the coordinator, so it stays refused.
		{"attribution unavailable on a busy ceiling", func(in *fastLaneInputs) {
			in.CoordinatedCPUAvailable = false
			in.FreeCapacity = 3
		}, fastLaneCoordinatedDominant},
		// If the coordinator's own admitted work is burning the CPU, admitting
		// more of it is precisely the wrong move.
		{"coordinated work dominates", func(in *fastLaneInputs) { in.CoordinatedCPUPercent = 88 }, fastLaneCoordinatedDominant},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			inputs := qualifyingFastLaneInputs()
			testCase.mutate(&inputs)
			admitted, refusal := evaluateFastLane(inputs)
			if admitted {
				t.Fatalf("fast lane admitted %s", testCase.name)
			}
			if refusal != testCase.refusal {
				t.Fatalf("refusal=%q want=%q", refusal, testCase.refusal)
			}
		})
	}
}

// TestFastLaneNeverExceedsWeightedCapacity is the invariant that keeps the fast
// lane from becoming a capacity increase: at every free-capacity level, it
// admits only what already fits.
func TestFastLaneNeverExceedsWeightedCapacity(t *testing.T) {
	for free := range 8 {
		for weight := 1; weight <= 2; weight++ {
			inputs := qualifyingFastLaneInputs()
			inputs.FreeCapacity = free
			inputs.Weight = weight
			admitted, _ := evaluateFastLane(inputs)
			if admitted && weight > free {
				t.Fatalf("fast lane admitted weight %d into %d free capacity", weight, free)
			}
			if !admitted && weight <= free && free*2 >= inputs.Capacity {
				t.Fatalf("fast lane refused weight %d that fits %d free capacity", weight, free)
			}
		}
	}
}

// TestFastLaneGateFallsThroughWithoutWiring proves the fail-closed direction: a
// gate missing its class, calibration, or capacity accessors keeps today's
// confirm-and-latch behaviour rather than admitting blind.
func TestFastLaneGateFallsThroughWithoutWiring(t *testing.T) {
	limits := testWorkLimits()
	limits.FastLaneEnabled = true
	limits.FastLaneMaxWeight = 2
	limits.FastLaneMaxRuntimeMS = 60_000
	limits.FastLaneCoordinatedCPUCeilingPct = 50

	for _, testCase := range []struct {
		name string
		gate *workAdmissionGate
	}{
		{"fast lane disabled", &workAdmissionGate{limits: testWorkLimits(), class: WorkClassExpressTest}},
		{"no class", &workAdmissionGate{limits: limits}},
		{"no calibration accessor", &workAdmissionGate{limits: limits, class: WorkClassExpressTest,
			freeWeightedCapacity: func() (int, bool) { return 8, true }}},
		{"no capacity accessor", &workAdmissionGate{limits: limits, class: WorkClassExpressTest,
			classRuntimeP95MS: func() (int64, bool) { return 5_000, true }}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if _, owned := testCase.gate.fastLaneDecision(Admission{}, "cpu"); owned {
				t.Fatal("unwired fast lane owned the decision instead of failing closed")
			}
		})
	}
}

// TestFastLaneCalibrationIsReadOnceNotPerPoll guards a real hazard: reading the
// event corpus costs a full parse of every retained day shard (24 MB / ~310 ms on
// the host this was measured on) and the gate polls every 2 s per waiting process.
// Reading per poll would burn more than a core across a handful of waiters exactly
// when the host is already CPU-red, so the guard would deepen the pressure it
// exists to relieve.
func TestFastLaneCalibrationIsReadOnceNotPerPoll(t *testing.T) {
	now := time.Date(2026, 7, 25, 15, 0, 0, 0, time.UTC)
	clock := now
	corpusReads := 0
	accessor := newMemoizedCalibration(func() (int64, bool) {
		corpusReads++
		return 5_000, true
	}, func() time.Time { return clock })

	for range 300 {
		if runtimeMS, ok := accessor(); !ok || runtimeMS != 5_000 {
			t.Fatalf("memoized value drifted: %d %v", runtimeMS, ok)
		}
	}
	if corpusReads != 1 {
		t.Fatalf("corpus read %d times across 300 polls; must be 1", corpusReads)
	}

	// Long waits refresh so they are not pinned to a stale estimate forever.
	clock = now.Add(fastLaneCalibrationTTL + time.Second)
	accessor()
	if corpusReads != 2 {
		t.Fatalf("TTL expiry did not refresh: reads=%d", corpusReads)
	}

	// A miss must be cached exactly like a hit. Otherwise a host with no
	// calibration history re-reads the whole corpus on every poll — the worst
	// case, reached precisely when the gate is busiest.
	missReads := 0
	missAccessor := newMemoizedCalibration(func() (int64, bool) {
		missReads++
		return 0, false
	}, func() time.Time { return clock })
	for range 50 {
		if _, ok := missAccessor(); ok {
			t.Fatal("unavailable calibration reported as available")
		}
	}
	if missReads != 1 {
		t.Fatalf("negative result was not cached: reads=%d", missReads)
	}
}

// TestAdmissionHoldSuppressedOncePresentInCoordinatorState keeps an operation in
// exactly one place: after it holds a lease or a pressure reservation it is
// already visible, and publishing a hold too would show it twice.
func TestAdmissionHoldSuppressedOncePresentInCoordinatorState(t *testing.T) {
	published := 0
	gate := &workAdmissionGate{hold: func(string, string) { published++ }}
	if !gate.recordHold("cpu", "red") || published != 1 {
		t.Fatalf("pre-queue hold must publish: published=%d", published)
	}
	gate.suppressHold = true
	if gate.recordHold("cpu", "red") || published != 1 {
		t.Fatalf("post-lease hold must not double-count: published=%d", published)
	}
}

// TestUnpressuredPollRecordsNoAdmissionVerdict is the regression for telemetry
// that lied: the predicate used to be consulted on every poll, including polls
// with no pressure at all, so a healthy run recorded
// `fast_lane_refused:non_cpu_dimension`. 261 such verdicts were logged before this
// was caught, and they read as the fast lane repeatedly declining real work when
// it had never actually been asked.
func TestUnpressuredPollRecordsNoAdmissionVerdict(t *testing.T) {
	limits := testWorkLimits()
	limits.FastLaneEnabled = true
	limits.FastLaneMaxWeight = 2
	limits.FastLaneMaxRuntimeMS = 60_000
	limits.FastLaneCoordinatedCPUCeilingPct = 50
	gate := &workAdmissionGate{
		limits: limits, class: WorkClassExpressTest,
		classRuntimeP95MS:    func() (int64, bool) { return 5_000, true },
		freeWeightedCapacity: func() (int, bool) { return 8, true },
	}

	// An allowed admission carries no pressure dimension at all.
	decision := gate.Observe(Admission{Allowed: true, Level: LevelNormal}, false)
	if !decision.Allowed {
		t.Fatalf("unpressured poll was blocked: %+v", decision)
	}
	if label := gate.decisionLabel(); label != "" {
		t.Fatalf("unpressured poll recorded an admission verdict: %q", label)
	}
}

// TestFastLaneDecisionLabelIsAuditable keeps the telemetry marker meaningful in
// both directions, so the population can be sized and the feature revoked from
// telemetry alone.
func TestFastLaneDecisionLabelIsAuditable(t *testing.T) {
	if label := (&workAdmissionGate{fastLaneAdmitted: true}).decisionLabel(); label != FastLaneDecisionReason {
		t.Fatalf("admitted label=%q", label)
	}
	gate := &workAdmissionGate{fastLaneRefusal: fastLaneTooHeavy}
	if label := gate.decisionLabel(); label != "fast_lane_refused:weight_above_ceiling" {
		t.Fatalf("refused label=%q", label)
	}
	if label := (&workAdmissionGate{}).decisionLabel(); label != "" {
		t.Fatalf("unevaluated gate label=%q", label)
	}
}
