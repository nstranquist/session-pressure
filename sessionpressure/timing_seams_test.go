package sessionpressure

import (
	"context"
	"sync"
	"testing"
	"time"
)

// testWaitNotUnderTest is the --wait budget for runs whose test asserts an
// outcome other than the budget. A short budget there only measures host
// load: under the commit-delivery worker a 50 ms budget expired before
// admission on a busy Mac. Tests that assert a deadline outcome use
// manualDeadline instead.
const testWaitNotUnderTest = 10 * time.Minute

// manualDeadline is a work wait budget that ends only when the test calls
// expire. It reports context.DeadlineExceeded, like the production
// context.WithTimeout budget, so refusal classification is unchanged, but the
// stage at which it ends is fixed by the test, not by the host's speed.
type manualDeadline struct {
	done chan struct{}
	once sync.Once
}

func newManualDeadline() *manualDeadline { return &manualDeadline{done: make(chan struct{})} }

func (deadline *manualDeadline) Deadline() (time.Time, bool) { return time.Time{}, false }
func (deadline *manualDeadline) Done() <-chan struct{}       { return deadline.done }
func (deadline *manualDeadline) Value(any) any               { return nil }

func (deadline *manualDeadline) Err() error {
	select {
	case <-deadline.done:
		return context.DeadlineExceeded
	default:
		return nil
	}
}

func (deadline *manualDeadline) expire() { deadline.once.Do(func() { close(deadline.done) }) }

// factory is a WorkRunStreams.WaitDeadline.
func (deadline *manualDeadline) factory(time.Duration) (context.Context, context.CancelFunc) {
	return deadline, func() {}
}

// testWatchdog bounds a wait for an event the test expects to happen (a
// child publishing its PID, a queue entry appearing, a goroutine finishing).
// It is a hang guard, not a measurement: the test proceeds as soon as the
// condition holds, and reaching the bound is a failure. A short bound here
// only measured host load.
const testWatchdog = 2 * time.Minute

// liveHostBudgets reports whether this run asserts production latency and
// memory budgets measured on the live host (native inventory time, sampler
// duration, resident RSS). Under -short, which the commit-delivery Darwin
// gate uses, these tests keep every correctness check and drop only the
// budget: on a loaded worker host the measurement reports the host's load,
// not the code. make test and make publish-ready run without -short and keep
// the budgets.
func liveHostBudgets() bool { return !testing.Short() }
