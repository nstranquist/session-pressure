package telemetry

import "time"

const CLIInvocationSourceID = "cli-invocations"

type Event struct {
	Event string         `json:"event"`
	TS    string         `json:"ts"`
	Attrs map[string]any `json:"attrs"`
}

type PrivacyViolation struct {
	Path string
	Kind string
}

type CLIWriterHealthReport struct {
	Known          bool
	Source         string
	CoverageSince  time.Time
	Attempted      int64
	Written        int64
	Failed         int64
	Gaps           int64
	FailureClasses map[string]int64
	LatencyScope   string
	LatencySamples int64
	LatencyP95US   int64
	LatencyMaxUS   int64
}

func ReadCLIWriterHealthSince(time.Time) CLIWriterHealthReport { return CLIWriterHealthReport{} }
func DefaultSourceTelemetryPathAt(string, time.Time) string    { return "" }
func ValidateEventPrivacy(Event) []PrivacyViolation            { return nil }
