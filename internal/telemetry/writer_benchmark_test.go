package telemetry

import (
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
