package sessionpressure

import (
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"
)

// Benchmarks at production scale: 14 day shards of about 1,500 operations
// (6,000 events, ~1.5 MB) each; the live ledger held 20,710 events over 14
// days on 2026-11-09, with a 4.7 MB peak day.
const (
	benchShardDays   = 14
	benchOpsPerShard = 1500
)

func benchmarkWorkEventStore(b *testing.B, scanOnly bool) (*WorkEventStore, time.Time) {
	b.Helper()
	now := time.Date(2026, 11, 9, 22, 0, 0, 0, time.Local)
	store := NewWorkEventStore(b.TempDir())
	store.scanOnly = true // seeding through the scan path is the same for both
	for day := benchShardDays - 1; day >= 0; day-- {
		at := now.AddDate(0, 0, -day)
		store.Now = func() time.Time { return at }
		for op := 0; op < benchOpsPerShard; op++ {
			operationID := fmt.Sprintf("%08x%024x", day, op)
			for _, eventType := range []WorkEventType{WorkEventQueued, WorkEventAcquired, WorkEventStarted, WorkEventCompleted} {
				event := validWorkEvent(at.Add(-time.Duration(op)*time.Second), eventType)
				event.OperationID = operationID
				event.EventID = workEventIdentity(event)
				if err := appendBenchEvent(store, at, event); err != nil {
					b.Fatal(err)
				}
			}
		}
	}
	store.scanOnly = scanOnly
	store.Now = func() time.Time { return now }
	if !scanOnly {
		// Warm the index once, as a long-lived ledger would be.
		if _, err := store.Read(WorkEventFilter{Since: now.Add(-time.Hour)}); err != nil {
			b.Fatal(err)
		}
	}
	return store, now
}

func benchmarkPaths(b *testing.B, run func(b *testing.B, store *WorkEventStore, now time.Time)) {
	for _, mode := range []struct {
		name     string
		scanOnly bool
	}{{"scan", true}, {"index", false}} {
		b.Run(mode.name, func(b *testing.B) {
			store, now := benchmarkWorkEventStore(b, mode.scanOnly)
			b.ResetTimer()
			run(b, store, now)
		})
	}
}

func BenchmarkWorkEventAppendDurable(b *testing.B) {
	benchmarkPaths(b, func(b *testing.B, store *WorkEventStore, now time.Time) {
		for i := 0; i < b.N; i++ {
			event := validWorkEvent(now, WorkEventQueued)
			event.OperationID = fmt.Sprintf("ff%030x", i)
			if err := store.AppendDurable(event); err != nil {
				b.Fatal(err)
			}
		}
	})
}

func BenchmarkWorkEventOperationHasTerminal(b *testing.B) {
	benchmarkPaths(b, func(b *testing.B, store *WorkEventStore, now time.Time) {
		for i := 0; i < b.N; i++ {
			if _, err := store.operationHasTerminal(fmt.Sprintf("ee%030x", i)); err != nil {
				b.Fatal(err)
			}
		}
	})
}

func BenchmarkWorkEventRead24h(b *testing.B) {
	benchmarkPaths(b, func(b *testing.B, store *WorkEventStore, now time.Time) {
		for i := 0; i < b.N; i++ {
			if _, err := store.Read(WorkEventFilter{Since: now.Add(-24 * time.Hour)}); err != nil {
				b.Fatal(err)
			}
		}
	})
}

func BenchmarkWorkEventReadAll(b *testing.B) {
	benchmarkPaths(b, func(b *testing.B, store *WorkEventStore, now time.Time) {
		for i := 0; i < b.N; i++ {
			if _, err := store.Read(WorkEventFilter{}); err != nil {
				b.Fatal(err)
			}
		}
	})
}

// appendBenchEvent writes one ledger line without the dedupe scan, so seeding
// stays linear.
func appendBenchEvent(store *WorkEventStore, at time.Time, event WorkEvent) error {
	body, err := json.Marshal(event)
	if err != nil {
		return err
	}
	file, err := os.OpenFile(store.dayPath(at), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := file.Write(append(body, '\n')); err != nil {
		file.Close()
		return err
	}
	return file.Close()
}
