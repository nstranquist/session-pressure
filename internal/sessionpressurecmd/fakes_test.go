package sessionpressurecmd

import (
	"context"
	"testing"
	"time"

	"github.com/nstranquist/session-pressure/internal/sessionpressure"
)

type fakePressureLaunchdController struct {
	status         sessionpressure.LaunchdStatus
	installErr     error
	ensureErr      error
	uninstallErr   error
	restartErr     error
	installCalls   int
	ensureCalls    int
	uninstallCalls int
	restartCalls   int
}

func (fake *fakePressureLaunchdController) Install(context.Context) (sessionpressure.LaunchdStatus, error) {
	fake.installCalls++
	return fake.status, fake.installErr
}

func (fake *fakePressureLaunchdController) Uninstall(context.Context) (sessionpressure.LaunchdStatus, error) {
	fake.uninstallCalls++
	if fake.uninstallErr != nil {
		return fake.status, fake.uninstallErr
	}
	result := fake.status
	result.OK = false
	result.Loaded = false
	result.PID = 0
	return result, nil
}

func (fake *fakePressureLaunchdController) Restart(context.Context) (sessionpressure.LaunchdStatus, error) {
	fake.restartCalls++
	return fake.status, fake.restartErr
}

func (fake *fakePressureLaunchdController) EnsureRunning(context.Context) (sessionpressure.LaunchdStatus, error) {
	fake.ensureCalls++
	return fake.status, fake.ensureErr
}

func (fake *fakePressureLaunchdController) Status(context.Context) sessionpressure.LaunchdStatus {
	return fake.status
}

func installFakePressureLaunchd(t *testing.T, fake *fakePressureLaunchdController) {
	t.Helper()
	previousFactory := NewLaunchdController
	previousAllowed := LaunchdManagementAllowed
	NewLaunchdController = func(string) (LaunchdController, error) { return fake, nil }
	LaunchdManagementAllowed = func() bool { return true }
	t.Cleanup(func() {
		NewLaunchdController = previousFactory
		LaunchdManagementAllowed = previousAllowed
	})
}

func installFakePressurePolicyMutationLock(t *testing.T, acquire func(context.Context, string, time.Duration) (func(), error)) {
	t.Helper()
	previous := AcquirePolicyMutationLockHook
	AcquirePolicyMutationLockHook = acquire
	t.Cleanup(func() { AcquirePolicyMutationLockHook = previous })
}

func installFakePressureSampler(t *testing.T, sample func(context.Context, *sessionpressure.Sampler, sessionpressure.Policy) (sessionpressure.Snapshot, error)) {
	t.Helper()
	previous := SampleSnapshot
	SampleSnapshot = sample
	t.Cleanup(func() { SampleSnapshot = previous })
}

func installFakePressureMonitorOnce(t *testing.T, run func(context.Context, *sessionpressure.Sampler, *sessionpressure.TelemetryStore, sessionpressure.Policy) (sessionpressure.Snapshot, error)) {
	t.Helper()
	previous := RunMonitorOnce
	RunMonitorOnce = run
	t.Cleanup(func() { RunMonitorOnce = previous })
}
