package sessionpressurecmd

import (
	"testing"

	"github.com/nstranquist/session-pressure/internal/telemetry"
)

func TestAuditProductWriterIntegrity(t *testing.T) {
	for _, tc := range []struct {
		name     string
		writer   telemetry.CLIWriterHealthReport
		code     string
		severity string
	}{
		{"clean", telemetry.CLIWriterHealthReport{Known: true}, "", pressureAuditPass},
		{"unknown", telemetry.CLIWriterHealthReport{}, "product_writer_coverage_unknown", pressureAuditWarn},
		{"terminal failure", telemetry.CLIWriterHealthReport{Known: true, Failed: 1}, "product_writer_write_failed", pressureAuditFail},
		{"unresolved includes inflight", telemetry.CLIWriterHealthReport{Known: true, Gaps: 1}, "product_writer_unresolved_attempts", pressureAuditWarn},
		{"retention failure", telemetry.CLIWriterHealthReport{Known: true, FailureClasses: map[string]int64{"retention_io": 1}}, "product_writer_retention_failed", pressureAuditFail},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := auditProductWriterIntegrity(tc.writer)
			if tc.code == "" {
				if len(f) != 0 {
					t.Fatal(f)
				}
				return
			}
			if len(f) != 1 || f[0].Code != tc.code || f[0].Severity != tc.severity {
				t.Fatalf("%+v", f)
			}
		})
	}
}

func TestAuditProductWriterPerformanceUsesSameCeiling(t *testing.T) {
	w := telemetry.CLIWriterHealthReport{Known: true, LatencySamples: 100, LatencyP95US: 100_001}
	f := auditWriterPerformance(w, "product", "Standalone")
	if len(f) != 1 || f[0].Code != "product_writer_latency_failed" || f[0].Severity != pressureAuditFail {
		t.Fatal(f)
	}
	w.LatencyP95US = 20_000
	if f = auditWriterPerformance(w, "product", "Standalone"); len(f) != 0 {
		t.Fatal(f)
	}
}
