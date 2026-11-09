package telemetry

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Run compiled benchmarks on the target filesystem. A Linux result does not
// establish the latency of the Mac's writer or its disk contention.
func BenchmarkDurableCommandWriter(b *testing.B) {
	b.Setenv("SESSION_PRESSURE_TELEMETRY_DIR", b.TempDir())
	b.Setenv("NDEV_PRESSURE_WRAPPER", "")
	b.Setenv("NDEV_TELEMETRY", "1")
	since := time.Now().Add(-time.Minute)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := RecordCommand("status", time.Now(), 0); err != nil {
			b.Fatal(err)
		}
	}
	b.StopTimer()
	h := ReadProductWriterHealthSince(since)
	if !h.Known || h.Attempted != int64(b.N) || h.Written != int64(b.N) || h.Failed != 0 || h.Gaps != 0 {
		b.Fatalf("writer health: %+v", h)
	}
	b.ReportMetric(float64(h.LatencyP95US), "writer-p95-us")
	b.ReportMetric(float64(h.LatencyMaxUS), "writer-max-us")
}

// Isolate storage barriers on the same filesystem as the full writer. These
// measurements distinguish a slow durable flush from lock or accounting work.
func BenchmarkWriterStorageStages(b *testing.B) {
	b.Run("authority", func(b *testing.B) {
		path := filepath.Join(b.TempDir(), "health.json")
		state := writerState{SchemaVersion: 1, StartedAt: time.Now().UTC()}
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			if err := writeState(path, state); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("primary", func(b *testing.B) {
		path := filepath.Join(b.TempDir(), "events.jsonl")
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
			if err != nil {
				b.Fatal(err)
			}
			_, writeErr := f.Write([]byte("{\"event\":\"benchmark\"}\n"))
			syncErr := f.Sync()
			closeErr := f.Close()
			if writeErr != nil || syncErr != nil || closeErr != nil {
				b.Fatalf("write=%v sync=%v close=%v", writeErr, syncErr, closeErr)
			}
		}
	})
}
