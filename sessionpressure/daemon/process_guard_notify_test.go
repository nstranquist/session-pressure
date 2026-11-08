package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nstranquist/session-pressure/sessionpressure"
)

func TestProcessIncidentNotificationOpensDetailsOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "inbox.jsonl")
	t.Setenv("NDEV_NOTIFY_TEST_NOTIFICATIONS_FILE", path)
	if err := notifyProcessIncident(sessionpressure.ProcessIncident{PID: 8523, Executable: "python3", CPUPercent: 76, SustainedSeconds: 120}); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var toast struct {
		ExecuteCommand    string `json:"execute_command"`
		Message, Severity string
	}
	if err := json.Unmarshal(body, &toast); err != nil {
		t.Fatal(err)
	}
	if toast.ExecuteCommand != "/usr/bin/open ndev-pressure://process-incidents" || toast.Severity != "warning" || !strings.Contains(toast.Message, "PID 8523") || !strings.Contains(toast.Message, "76%") {
		t.Fatalf("unexpected toast: %s", body)
	}
}
