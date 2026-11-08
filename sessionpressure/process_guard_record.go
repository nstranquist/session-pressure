package sessionpressure

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

// ResolvedProcessReceipt is an operator's historical record. It cannot grant
// permission to signal a PID: no kernel identity is imported and state is closed.
type ResolvedProcessReceipt struct {
	PID        int       `json:"pid"`
	PPID       int       `json:"ppid"`
	Executable string    `json:"executable"`
	At         time.Time `json:"at"`
	Signal     string    `json:"signal"`
}

func RecordResolvedProcessIncident(ctx context.Context, dir string, receipt ResolvedProcessReceipt) (ProcessIncident, error) {
	if receipt.PID <= 1 || receipt.PPID < 0 || receipt.Executable == "" || len(receipt.Executable) > 128 || filepath.Base(receipt.Executable) != receipt.Executable || strings.ContainsAny(receipt.Executable, "\r\n\t") || receipt.At.IsZero() || receipt.At.After(time.Now().Add(time.Minute)) || (receipt.Signal != "TERM" && receipt.Signal != "KILL" && receipt.Signal != "NONE") {
		return ProcessIncident{}, fmt.Errorf("receipt requires pid > 1, executable basename, past RFC3339 at, and TERM, KILL or NONE signal")
	}
	p := ProcessIncident{ID: incidentKey(receipt.PID, "operator:"+receipt.At.UTC().Format(time.RFC3339Nano)), PID: receipt.PID, PPID: receipt.PPID, Executable: receipt.Executable, Source: "operator_record", Reason: "operator_recorded_stop", State: "recorded_closed", FirstSeen: receipt.At, LastSeen: receipt.At, Actions: []ProcessIncidentAction{{At: receipt.At, Signal: receipt.Signal, Result: "operator_reported_exit"}}}
	if receipt.Signal == "NONE" {
		p.Reason = "operator_recorded_exit"
		p.Actions[0].Result = "operator_observed_exit"
	}
	err := updateProcessIncidents(ctx, dir, func(r *ProcessIncidentReport) error {
		for _, existing := range r.Incidents {
			if existing.ID == p.ID {
				p = existing
				return nil
			}
		}
		r.Incidents = append(r.Incidents, p)
		return nil
	})
	return p, err
}
