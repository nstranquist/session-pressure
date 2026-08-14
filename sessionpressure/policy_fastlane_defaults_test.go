package sessionpressure

import "testing"

// A shipped default, once persisted, used to be permanent. The backfill only
// filled zeros, so it could not tell "the operator chose 60s" from "an older
// build's default was 60s" — and `policy migrate` persists the effective policy,
// so it could not repair either. The 60s runtime ceiling was inert against every
// real class and reached every host with no supported way back.

func TestShippedFastLaneDefaultsAreRepairable(t *testing.T) {
	defaults := defaultWorkLimits(10)

	t.Run("stale shipped revision is re-derived", func(t *testing.T) {
		// Exactly the state that was unrepairable: revision 1's 60s ceiling.
		stale := defaults
		stale.FastLaneMaxRuntimeMS = 60_000
		stale.FastLaneDefaultsRevision = 1

		repaired := reconcileFastLaneDefaults(stale, defaults)
		if repaired.FastLaneMaxRuntimeMS != defaults.FastLaneMaxRuntimeMS {
			t.Fatalf("stale shipped ceiling not repaired: %d", repaired.FastLaneMaxRuntimeMS)
		}
		if repaired.FastLaneDefaultsRevision != defaults.FastLaneDefaultsRevision {
			t.Fatalf("revision not advanced: %d", repaired.FastLaneDefaultsRevision)
		}
	})

	t.Run("operator bounds are never re-derived", func(t *testing.T) {
		owned := defaults
		owned.FastLaneMaxRuntimeMS = 45_000
		owned.FastLaneMaxWeight = 1
		owned.FastLaneDefaultsRevision = fastLaneDefaultsOperatorRevision

		repaired := reconcileFastLaneDefaults(owned, defaults)
		if repaired.FastLaneMaxRuntimeMS != 45_000 || repaired.FastLaneMaxWeight != 1 {
			t.Fatalf("operator bounds overwritten: %+v", repaired)
		}
		if repaired.FastLaneDefaultsRevision != fastLaneDefaultsOperatorRevision {
			t.Fatalf("operator revision changed: %d", repaired.FastLaneDefaultsRevision)
		}
	})

	t.Run("hand-edited bounds become operator-owned", func(t *testing.T) {
		// A policy edited by hand carries no revision. It must be claimed for the
		// operator, not treated as a stale shipped set that we may overwrite.
		handEdited := defaults
		handEdited.FastLaneMaxRuntimeMS = 90_000
		handEdited.FastLaneDefaultsRevision = 0

		repaired := reconcileFastLaneDefaults(handEdited, defaults)
		if repaired.FastLaneMaxRuntimeMS != 90_000 {
			t.Fatalf("hand-edited ceiling overwritten: %d", repaired.FastLaneMaxRuntimeMS)
		}
		if repaired.FastLaneDefaultsRevision != fastLaneDefaultsOperatorRevision {
			t.Fatalf("hand edit not claimed for the operator: %d", repaired.FastLaneDefaultsRevision)
		}
		// And it survives a second pass, which is what "never touched again" means.
		again := reconcileFastLaneDefaults(repaired, defaults)
		if again.FastLaneMaxRuntimeMS != 90_000 {
			t.Fatalf("hand edit lost on reload: %d", again.FastLaneMaxRuntimeMS)
		}
	})

	t.Run("disabling survives a defaults correction", func(t *testing.T) {
		// Turning the fast lane off is an operator decision even when the bounds
		// around it were ours. A revision bump must not switch it back on.
		off := defaults
		off.FastLaneEnabled = false
		off.FastLaneMaxRuntimeMS = 60_000
		off.FastLaneDefaultsRevision = 1

		repaired := reconcileFastLaneDefaults(off, defaults)
		if repaired.FastLaneEnabled {
			t.Fatal("defaults correction re-enabled a fast lane the operator turned off")
		}
		if repaired.FastLaneMaxRuntimeMS != defaults.FastLaneMaxRuntimeMS {
			t.Fatalf("bounds not repaired alongside a preserved opt-out: %d", repaired.FastLaneMaxRuntimeMS)
		}
	})

	t.Run("a policy predating the fast lane inherits everything", func(t *testing.T) {
		var pristine WorkLimits
		pristine.Capacity = defaults.Capacity

		repaired := reconcileFastLaneDefaults(pristine, defaults)
		if !repaired.FastLaneEnabled ||
			repaired.FastLaneMaxRuntimeMS != defaults.FastLaneMaxRuntimeMS ||
			repaired.FastLaneMaxWeight != defaults.FastLaneMaxWeight ||
			repaired.FastLaneDefaultsRevision != defaults.FastLaneDefaultsRevision {
			t.Fatalf("pre-fast-lane policy did not inherit defaults: %+v", repaired)
		}
	})

	t.Run("a current shipped revision is left alone", func(t *testing.T) {
		current := defaults
		repaired := reconcileFastLaneDefaults(current, defaults)
		if repaired != current {
			t.Fatalf("current revision was modified: %+v", repaired)
		}
	})
}
