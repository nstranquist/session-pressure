package sessionpressure

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// The host-pressure admission gate runs before an operation ever registers with
// the weighted coordinator. Historically a process parked there was in neither
// leases nor waiters nor queue_depth, so the guard could hold seven agents while
// reporting an empty queue and mostly idle capacity. An admission hold is the
// typed record that makes that state observable.
//
// A hold never charges weighted capacity. It is evidence, not a reservation.

// WorkAdmissionHoldRecord is private durable state for one process parked at the
// host-pressure admission gate. Like leases and waiters it carries the owner's
// kernel start identity so a reused PID cannot inherit someone else's hold.
type WorkAdmissionHoldRecord struct {
	OperationID   string    `json:"operation_id"`
	Class         WorkClass `json:"class"`
	Weight        int       `json:"weight"`
	PID           int       `json:"pid"`
	OwnerIdentity string    `json:"owner_identity"`
	HeldSince     time.Time `json:"held_since"`
	HeartbeatAt   time.Time `json:"heartbeat_at"`
	Dimension     string    `json:"dimension,omitempty"`
	Reason        string    `json:"reason,omitempty"`
}

// WorkAdmissionHoldStatus is the public read projection of one hold.
type WorkAdmissionHoldStatus struct {
	OperationID string    `json:"operation_id"`
	Class       WorkClass `json:"class"`
	Weight      int       `json:"weight"`
	PID         int       `json:"pid"`
	HeldSince   time.Time `json:"held_since"`
	HeldForMS   int64     `json:"held_for_ms"`
	Dimension   string    `json:"dimension,omitempty"`
	Reason      string    `json:"reason,omitempty"`
}

// WorkAdmissionLatch is the shared CPU-latch consensus. Before this existed each
// `work run` built its own latch and its own release counter, so N waiters each
// had to independently observe recovery — and each burned a live host probe to do
// it, adding the very load they were waiting on. Counting advances on elapsed
// time, never on observer count, so ten waiters cannot latch ten times faster
// than one.
type WorkAdmissionLatch struct {
	Latched         bool       `json:"latched"`
	LatchedAt       *time.Time `json:"latched_at,omitempty"`
	Dimension       string     `json:"dimension,omitempty"`
	Reason          string     `json:"reason,omitempty"`
	RedSamples      int        `json:"red_samples"`
	RecoverySamples int        `json:"recovery_samples"`
	BlockRequired   int        `json:"block_required"`
	ReleaseRequired int        `json:"release_required"`
	ObservedAt      *time.Time `json:"observed_at,omitempty"`
}

// workAdmissionLatchSampleInterval is the minimum spacing between two counted
// latch observations. Concurrent waiters share one counter, so without a time
// floor a burst of pollers would satisfy the sustain requirement instantly.
const workAdmissionLatchSampleInterval = 1500 * time.Millisecond

// workAdmissionLatchStale bounds how long a latch survives without any observer.
// If every waiter exits, the next arrival must re-establish red rather than
// inheriting a stale block.
const workAdmissionLatchStale = 2 * time.Minute

// WorkAdmissionObservation is one waiter's view of the host at a poll.
type WorkAdmissionObservation struct {
	Red       bool
	Recovered bool
	Dimension string
	Reason    string
}

func (record WorkAdmissionHoldRecord) validate(limits WorkLimits, now time.Time) error {
	if !validPrivateID(record.OperationID) {
		return fmt.Errorf("invalid work admission hold operation id %q", record.OperationID)
	}
	if _, err := limits.Weight(record.Class); err != nil {
		return fmt.Errorf("invalid work admission hold %s: %w", record.OperationID, err)
	}
	if record.PID <= 0 || strings.TrimSpace(record.OwnerIdentity) == "" {
		return fmt.Errorf("invalid work admission hold %s owner", record.OperationID)
	}
	if record.HeldSince.IsZero() || record.HeldSince.After(now.Add(maximumWorkClockSkew)) {
		return fmt.Errorf("invalid work admission hold %s metadata", record.OperationID)
	}
	return nil
}

// reconcileAdmissionHolds drops holds whose owner died or whose PID was reused.
// A hold carries no capacity, so an orphan is a reporting lie rather than a
// resource leak — but a reporting lie is exactly what this record exists to
// prevent, so it is pruned on the same pass as leases and waiters.
func (coordinator *WorkCoordinator) reconcileAdmissionHolds(state *workState) (pruned int, changed bool, err error) {
	if state == nil || len(state.AdmissionHolds) == 0 {
		return 0, false, nil
	}
	alive := coordinator.ProcessAlive
	if alive == nil {
		alive = processAlive
	}
	identity := coordinator.ProcessIdentity
	if identity == nil {
		identity = processStartIdentity
	}
	now := coordinator.now()
	limits := coordinator.limits()
	seen := make(map[string]struct{}, len(state.AdmissionHolds))
	kept := state.AdmissionHolds[:0]
	for _, hold := range state.AdmissionHolds {
		if err := hold.validate(limits, now); err != nil {
			return 0, false, err
		}
		if _, duplicate := seen[hold.OperationID]; duplicate {
			return 0, false, fmt.Errorf("duplicate work admission hold %q", hold.OperationID)
		}
		seen[hold.OperationID] = struct{}{}
		if !alive(hold.PID) {
			pruned++
			changed = true
			continue
		}
		current, identityErr := identity(hold.PID)
		if identityErr != nil || strings.TrimSpace(current) == "" || current != hold.OwnerIdentity {
			pruned++
			changed = true
			continue
		}
		kept = append(kept, hold)
	}
	state.AdmissionHolds = kept
	return pruned, changed, nil
}

func admissionHoldStatuses(state workState, now time.Time) []WorkAdmissionHoldStatus {
	if len(state.AdmissionHolds) == 0 {
		return []WorkAdmissionHoldStatus{}
	}
	holds := append([]WorkAdmissionHoldRecord{}, state.AdmissionHolds...)
	sort.Slice(holds, func(i, j int) bool {
		if holds[i].HeldSince.Equal(holds[j].HeldSince) {
			return holds[i].OperationID < holds[j].OperationID
		}
		return holds[i].HeldSince.Before(holds[j].HeldSince)
	})
	out := make([]WorkAdmissionHoldStatus, 0, len(holds))
	for _, hold := range holds {
		out = append(out, WorkAdmissionHoldStatus{
			OperationID: hold.OperationID, Class: hold.Class, Weight: hold.Weight, PID: hold.PID,
			HeldSince: hold.HeldSince, HeldForMS: max(int64(0), now.Sub(hold.HeldSince).Milliseconds()),
			Dimension: hold.Dimension, Reason: hold.Reason,
		})
	}
	return out
}

// HoldAdmission records or refreshes this process's admission hold. It is called
// on entry to the gate and on every poll, so held_for_ms stays truthful for an
// observer watching from the desktop app.
func (coordinator *WorkCoordinator) HoldAdmission(ctx context.Context, operationID string, class WorkClass, observation WorkAdmissionObservation) error {
	if coordinator == nil {
		return errors.New("work coordinator is required")
	}
	if !validPrivateID(operationID) {
		return errors.New("work operation_id must be a 32-character lowercase hex identity")
	}
	weight, err := coordinator.limits().Weight(class)
	if err != nil {
		return err
	}
	identity := coordinator.ProcessIdentity
	if identity == nil {
		identity = processStartIdentity
	}
	owner, err := identity(coordinator.PID)
	if err != nil || strings.TrimSpace(owner) == "" {
		return fmt.Errorf("resolve work admission hold owner identity: %w", err)
	}
	now := coordinator.now()
	_, err = coordinator.withState(ctx, func(state *workState) error {
		for index := range state.AdmissionHolds {
			if state.AdmissionHolds[index].OperationID != operationID {
				continue
			}
			state.AdmissionHolds[index].HeartbeatAt = now
			state.AdmissionHolds[index].Dimension = observation.Dimension
			state.AdmissionHolds[index].Reason = observation.Reason
			return nil
		}
		state.AdmissionHolds = append(state.AdmissionHolds, WorkAdmissionHoldRecord{
			OperationID: operationID, Class: class, Weight: weight, PID: coordinator.PID,
			OwnerIdentity: owner, HeldSince: now, HeartbeatAt: now,
			Dimension: observation.Dimension, Reason: observation.Reason,
		})
		return nil
	})
	return err
}

// ReleaseAdmission removes this process's hold once the gate opens, the wait is
// cancelled, or the process exits. Releasing an absent hold is not an error, so
// the caller can defer it unconditionally.
func (coordinator *WorkCoordinator) ReleaseAdmission(ctx context.Context, operationID string) error {
	if coordinator == nil {
		return errors.New("work coordinator is required")
	}
	if !validPrivateID(operationID) {
		return errors.New("work operation_id must be a 32-character lowercase hex identity")
	}
	_, err := coordinator.withState(ctx, func(state *workState) error {
		kept := state.AdmissionHolds[:0]
		for _, hold := range state.AdmissionHolds {
			if hold.OperationID == operationID {
				continue
			}
			kept = append(kept, hold)
		}
		state.AdmissionHolds = kept
		return nil
	})
	return err
}

// ObserveAdmissionLatch folds one waiter's observation into the shared latch and
// returns the resulting consensus. Counters advance at most once per
// workAdmissionLatchSampleInterval regardless of how many waiters are polling.
func (coordinator *WorkCoordinator) ObserveAdmissionLatch(ctx context.Context, observation WorkAdmissionObservation) (WorkAdmissionLatch, error) {
	if coordinator == nil {
		return WorkAdmissionLatch{}, errors.New("work coordinator is required")
	}
	limits := coordinator.limits()
	blockRequired := max(1, limits.CPUBlockSamples)
	releaseRequired := max(1, limits.CPUReleaseSamples)
	now := coordinator.now()
	var latch WorkAdmissionLatch
	_, err := coordinator.withState(ctx, func(state *workState) error {
		current := state.AdmissionLatch
		if current == nil {
			current = &WorkAdmissionLatch{}
		}
		if current.ObservedAt != nil && now.Sub(*current.ObservedAt) > workAdmissionLatchStale {
			// Nobody has polled for long enough that the block is no longer
			// evidence. Fail open rather than inheriting a stale latch.
			current = &WorkAdmissionLatch{}
		}
		counted := current.ObservedAt == nil || now.Sub(*current.ObservedAt) >= workAdmissionLatchSampleInterval
		if counted {
			switch {
			case observation.Red:
				current.RecoverySamples = 0
				current.RedSamples++
				if !current.Latched && current.RedSamples >= blockRequired {
					current.Latched = true
					latchedAt := now
					current.LatchedAt = &latchedAt
				}
			case observation.Recovered:
				current.RedSamples = 0
				current.RecoverySamples++
				if current.Latched && current.RecoverySamples >= releaseRequired {
					current.Latched = false
					current.LatchedAt = nil
					current.RecoverySamples = 0
				}
			default:
				// Warning-band oscillation: neither red nor below the release
				// floor. Hold the latch where it is without advancing either
				// counter, so a boundary flap cannot release it.
				current.RecoverySamples = 0
			}
			observedAt := now
			current.ObservedAt = &observedAt
		}
		if observation.Dimension != "" {
			current.Dimension = observation.Dimension
		}
		if observation.Reason != "" {
			current.Reason = observation.Reason
		}
		current.BlockRequired = blockRequired
		current.ReleaseRequired = releaseRequired
		state.AdmissionLatch = current
		latch = *current
		return nil
	})
	if err != nil {
		return WorkAdmissionLatch{}, err
	}
	return latch, nil
}
