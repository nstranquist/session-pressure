package sessionpressure

import (
	"context"
	"errors"
	"os/signal"
	"sync"
	"syscall"
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
	done      chan struct{}
	once      sync.Once
	mu        sync.Mutex
	expiresAt time.Time
	expired   bool
	afters    map[*func()]struct{}
}

func newManualDeadline() *manualDeadline {
	return &manualDeadline{done: make(chan struct{}), expiresAt: time.Now().Add(testWatchdog), afters: map[*func()]struct{}{}}
}

// Deadline reports a deadline like context.WithTimeout does, because the
// runner derives lock timeouts from it: far away until expire, then the
// moment of expiry, so the remaining budget reads as spent.
func (deadline *manualDeadline) Deadline() (time.Time, bool) {
	deadline.mu.Lock()
	defer deadline.mu.Unlock()
	return deadline.expiresAt, true
}
func (deadline *manualDeadline) Done() <-chan struct{} { return deadline.done }
func (deadline *manualDeadline) Value(any) any         { return nil }

func (deadline *manualDeadline) Err() error {
	select {
	case <-deadline.done:
		return context.DeadlineExceeded
	default:
		return nil
	}
}

// AfterFunc makes the context package cancel derived contexts (the
// runner wraps this one in signal.NotifyContext) synchronously inside expire.
// Without it, a derived context of a custom parent is cancelled by a separate
// goroutine, so the runner could still see a live budget just after expire
// and the stage under test would race again.
func (deadline *manualDeadline) AfterFunc(f func()) (stop func() bool) {
	deadline.mu.Lock()
	defer deadline.mu.Unlock()
	if deadline.expired {
		go f()
		return func() bool { return false }
	}
	key := &f
	deadline.afters[key] = struct{}{}
	return func() bool {
		deadline.mu.Lock()
		defer deadline.mu.Unlock()
		_, pending := deadline.afters[key]
		delete(deadline.afters, key)
		return pending
	}
}

// expire ends the budget. When it returns, this context and every context
// derived from it report context.DeadlineExceeded.
func (deadline *manualDeadline) expire() {
	deadline.once.Do(func() {
		deadline.mu.Lock()
		deadline.expiresAt = time.Now()
		deadline.expired = true
		afters := deadline.afters
		deadline.afters = nil
		close(deadline.done)
		deadline.mu.Unlock()
		for f := range afters {
			(*f)()
		}
	})
}

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

// The runner wraps the wait budget in signal.NotifyContext. The stage under
// test must see the spent budget as soon as expire returns.
func TestManualDeadlineCancelsDerivedContextsSynchronously(t *testing.T) {
	deadline := newManualDeadline()
	derived, stop := signal.NotifyContext(deadline, syscall.SIGUSR2)
	defer stop()
	if derived.Err() != nil {
		t.Fatal("budget spent before expire")
	}
	deadline.expire()
	if !errors.Is(derived.Err(), context.DeadlineExceeded) || !errors.Is(deadline.Err(), context.DeadlineExceeded) {
		t.Fatalf("after expire: derived=%v deadline=%v", derived.Err(), deadline.Err())
	}
	if at, ok := deadline.Deadline(); !ok || time.Until(at) > 0 {
		t.Fatalf("deadline %v %v still in the future after expire", at, ok)
	}
	late, lateStop := signal.NotifyContext(deadline, syscall.SIGUSR2)
	defer lateStop()
	if !errors.Is(late.Err(), context.DeadlineExceeded) {
		t.Fatalf("context derived after expire: %v", late.Err())
	}
}
