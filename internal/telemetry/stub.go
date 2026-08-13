package telemetry

import "time"

const CLIInvocationSourceID = "cli-invocations"

type Event struct {
	Event string         `json:"event"`
	Attrs map[string]any `json:"attrs"`
}
type CLIWriterHealthReport struct {
	Dropped int
	Errors  int
}

func ReadCLIWriterHealthSince(time.Time) CLIWriterHealthReport { return CLIWriterHealthReport{} }
func DefaultSourceTelemetryPathAt(string, time.Time) string     { return "" }
func ValidateEventPrivacy(Event) []string                       { return nil }
