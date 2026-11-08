package main

import (
	"fmt"
	"github.com/nstranquist/session-pressure/internal/notifyinbox"
	"github.com/nstranquist/session-pressure/sessionpressure"
	"os"
	"time"
)

func notifyProcessIncident(incident sessionpressure.ProcessIncident) error {
	return notifyinbox.Append(notifyinbox.Path(os.Getenv("HOME"), ""), notifyinbox.Toast{
		Title:    "Process needs attention",
		Body:     fmt.Sprintf("%s (PID %d) has used %.0f%% of one CPU core for %d minutes. Open Process Incidents to inspect or stop it.", incident.Executable, incident.PID, incident.CPUPercent, incident.SustainedSeconds/60),
		Severity: "warning", Source: "session-pressure-helper", Seconds: "15",
		ExecuteCommand: "/usr/bin/open ndev-pressure://process-incidents",
	}, time.Now)
}
