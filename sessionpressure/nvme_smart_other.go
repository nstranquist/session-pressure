//go:build !darwin

package sessionpressure

import (
	"context"
	"time"
)

func nativeReadNVMeSMART(context.Context) (NVMeSMARTSnapshot, error) {
	return NVMeSMARTSnapshot{
		SchemaVersion: nvmeSMARTSnapshotSchemaVersion,
		CapturedAt:    time.Now().UTC(),
		Source:        "unsupported",
		Reason:        "platform_unsupported",
	}, nil
}
