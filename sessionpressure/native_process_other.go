//go:build !darwin

package sessionpressure

import (
	"context"
	"errors"
)

var errNativeProcessInventoryUnavailable = errors.New("native process inventory unavailable")

func nativeProcesses(context.Context) ([]Process, string, error) {
	return nil, "ps", errNativeProcessInventoryUnavailable
}

func refreshNativeProcessCPUTotals(_ context.Context, processes []Process) []Process {
	return append([]Process(nil), processes...)
}

func darwinEnvKeys(int, []string) map[string]string { return nil }
