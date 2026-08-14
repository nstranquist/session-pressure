package sessionpressure

// CompactWorkEvent is the agent-safe history row: identity, class, outcome, and
// timing without digests, shadow selector fields, or coordinated-work forensics.
// Full WorkEvent remains available via history --full.
type CompactWorkEvent struct {
	Timestamp         string        `json:"timestamp"`
	Event             WorkEventType `json:"event"`
	OperationID       string        `json:"operation_id"`
	Class             WorkClass     `json:"class"`
	Weight            int           `json:"weight,omitempty"`
	Blocker           WorkBlocker   `json:"blocker,omitempty"`
	WaitMilliseconds  int64         `json:"wait_ms,omitempty"`
	RuntimeMillis     int64         `json:"runtime_ms,omitempty"`
	Outcome           string        `json:"outcome,omitempty"`
	ExitCode          *int          `json:"exit_code,omitempty"`
	PressureLevel     Level         `json:"pressure_level,omitempty"`
	PressureDimension string        `json:"pressure_dimension,omitempty"`
	AdmissionDecision string        `json:"admission_decision,omitempty"`
	Capacity          int           `json:"capacity,omitempty"`
	Used              int           `json:"used,omitempty"`
	QueueDepth        int           `json:"queue_depth,omitempty"`
	ReuseStatus       string        `json:"reuse_status,omitempty"`
}

// CompactWorkEvents projects a full lifecycle ledger into the bounded history
// shape agents should read by default.
func CompactWorkEvents(events []WorkEvent) []CompactWorkEvent {
	out := make([]CompactWorkEvent, 0, len(events))
	for _, event := range events {
		out = append(out, CompactWorkEventFrom(event))
	}
	return out
}

// CompactWorkEventFrom keeps decision-grade fields from one durable work event.
func CompactWorkEventFrom(event WorkEvent) CompactWorkEvent {
	return CompactWorkEvent{
		Timestamp:         event.Timestamp.UTC().Format("2006-01-02T15:04:05.000000Z"),
		Event:             event.Event,
		OperationID:       event.OperationID,
		Class:             event.Class,
		Weight:            event.Weight,
		Blocker:           event.Blocker,
		WaitMilliseconds:  event.WaitMilliseconds,
		RuntimeMillis:     event.RuntimeMillis,
		Outcome:           event.Outcome,
		ExitCode:          event.ExitCode,
		PressureLevel:     event.PressureLevel,
		PressureDimension: event.PressureDimension,
		AdmissionDecision: event.AdmissionDecision,
		Capacity:          event.Capacity,
		Used:              event.Used,
		QueueDepth:        event.QueueDepth,
		ReuseStatus:       event.ReuseStatus,
	}
}
