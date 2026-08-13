package sessionpressure

import (
	"errors"
	"testing"
	"time"
)

func TestShouldEmitStorageDeadlockAdviceGate(t *testing.T) {
	// Before threshold
	if shouldEmitStorageDeadlockAdvice(storageDeadlockAdviceAfter-time.Second, false, "storage") {
		t.Fatal("before threshold must not emit")
	}
	// At threshold
	if !shouldEmitStorageDeadlockAdvice(storageDeadlockAdviceAfter, false, "storage") {
		t.Fatal("at threshold must emit")
	}
	// After threshold
	if !shouldEmitStorageDeadlockAdvice(storageDeadlockAdviceAfter+time.Minute, false, "storage") {
		t.Fatal("after threshold must emit")
	}
	// Already emitted
	if shouldEmitStorageDeadlockAdvice(storageDeadlockAdviceAfter+time.Minute, true, "storage") {
		t.Fatal("already emitted must not re-emit")
	}
	// Non-storage dimension
	if shouldEmitStorageDeadlockAdvice(storageDeadlockAdviceAfter+time.Minute, false, "cpu") {
		t.Fatal("cpu dimension must not emit")
	}
	if shouldEmitStorageDeadlockAdvice(storageDeadlockAdviceAfter+time.Minute, false, "memory") {
		t.Fatal("memory dimension must not emit")
	}
	if shouldEmitStorageDeadlockAdvice(storageDeadlockAdviceAfter+time.Minute, false, "") {
		t.Fatal("empty dimension must not emit")
	}
}

func TestWorkStorageGraceExpiresAndExtendsOnlyForMeaningfulProgress(t *testing.T) {
	current := time.Date(2026, 7, 21, 20, 0, 0, 0, time.UTC)
	available := int64(20 << 30)
	admission := func() Admission {
		return Admission{Allowed: false, Level: LevelRed, Snapshot: &Snapshot{Storage: StorageSnapshot{Available: true, AvailableBytes: available, Level: LevelRed}}}
	}
	gate := &workAdmissionGate{storageGrace: 60 * time.Second, now: func() time.Time { return current }}
	if err := gate.storageGraceError(admission(), "storage"); err != nil {
		t.Fatal(err)
	}
	current = current.Add(59 * time.Second)
	available += WorkStorageProgressBytes - 1
	if err := gate.storageGraceError(admission(), "storage"); err != nil {
		t.Fatalf("grace expired before deadline: %v", err)
	}
	current = current.Add(time.Second)
	if err := gate.storageGraceError(admission(), "storage"); !errors.Is(err, ErrWorkStorageGraceExceeded) {
		t.Fatalf("sub-threshold storage change extended grace: %v", err)
	}

	current = time.Date(2026, 7, 21, 21, 0, 0, 0, time.UTC)
	available = 20 << 30
	gate = &workAdmissionGate{storageGrace: 60 * time.Second, now: func() time.Time { return current }}
	_ = gate.storageGraceError(admission(), "storage")
	current = current.Add(30 * time.Second)
	available += WorkStorageProgressBytes
	if err := gate.storageGraceError(admission(), "storage"); err != nil {
		t.Fatalf("meaningful storage progress did not extend grace: %v", err)
	}
	current = current.Add(59 * time.Second)
	if err := gate.storageGraceError(admission(), "storage"); err != nil {
		t.Fatalf("extended grace expired early: %v", err)
	}
	current = current.Add(time.Second)
	if err := gate.storageGraceError(admission(), "storage"); !errors.Is(err, ErrWorkStorageGraceExceeded) {
		t.Fatalf("extended grace did not close: %v", err)
	}
}

func TestWorkStorageGraceExtendsWhileReclaimLeaseIsActive(t *testing.T) {
	current := time.Date(2026, 7, 21, 22, 0, 0, 0, time.UTC)
	active := false
	admission := Admission{Allowed: false, Level: LevelRed, Snapshot: &Snapshot{Storage: StorageSnapshot{Available: true, AvailableBytes: 20 << 30, Level: LevelRed}}}
	gate := &workAdmissionGate{
		storageGrace: 60 * time.Second, now: func() time.Time { return current },
		storageReclaimActive: func() bool { return active },
	}
	_ = gate.storageGraceError(admission, "storage")
	current = current.Add(60 * time.Second)
	active = true
	if err := gate.storageGraceError(admission, "storage"); err != nil {
		t.Fatalf("active reclaim did not extend grace: %v", err)
	}
	active = false
	current = current.Add(60 * time.Second)
	if err := gate.storageGraceError(admission, "storage"); !errors.Is(err, ErrWorkStorageGraceExceeded) {
		t.Fatalf("grace did not close after reclaim stopped: %v", err)
	}
}

func TestDefaultStorageWorkWaitGraceIsSixtySeconds(t *testing.T) {
	policy := DefaultPolicy(16 * 1024)
	if policy.Storage.WorkWaitGraceSeconds != 60 {
		t.Fatalf("storage work grace=%d, want 60", policy.Storage.WorkWaitGraceSeconds)
	}
}
