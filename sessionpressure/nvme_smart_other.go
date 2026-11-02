//go:build !darwin || !cgo

package sessionpressure

import (
	"context"
	"runtime"
	"time"
)

// nativeReadNVMeSMART is the fallback used when the IOKit NVMe SMART reader is
// unavailable: on non-darwin platforms, or on darwin builds with CGO_ENABLED=0.
// It reports an unavailable snapshot rather than failing so callers degrade
// gracefully.
func nativeReadNVMeSMART(context.Context) (NVMeSMARTSnapshot, error) {
	reason := "platform_unsupported"
	if runtime.GOOS == "darwin" {
		reason = "cgo_disabled"
	}
	return NVMeSMARTSnapshot{
		SchemaVersion: nvmeSMARTSnapshotSchemaVersion,
		CapturedAt:    time.Now().UTC(),
		Source:        "unsupported",
		Reason:        reason,
	}, nil
}
