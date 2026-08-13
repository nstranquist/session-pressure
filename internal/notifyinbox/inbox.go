// Package notifyinbox owns the small append-only desktop toast wire contract.
// It deliberately has no scheduler, Slack, Obsidian, or CLI dependencies so
// low-RSS resident helpers can queue a notification without linking the full
// notification control plane.
package notifyinbox

import (
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/nstranquist/session-pressure/internal/jsonl"
	"github.com/nstranquist/session-pressure/internal/jsonutil"
)

type Toast struct {
	Title          string
	Body           string
	Severity       string
	URL            string
	Source         string
	Seconds        string
	ExecuteCommand string
}

type toastWire struct {
	Timestamp      string  `json:"ts"`
	Title          string  `json:"title"`
	Message        string  `json:"message"`
	Severity       string  `json:"severity"`
	URL            *string `json:"url"`
	ExecuteCommand *string `json:"execute_command"`
	Source         *string `json:"source"`
	DisplaySeconds *int    `json:"display_seconds"`
}

// Path resolves the canonical toast inbox with the same precedence as the
// public notify dispatcher.
func Path(home, override string) string {
	if override != "" {
		return override
	}
	if value := os.Getenv("NDEV_NOTIFY_TEST_NOTIFICATIONS_FILE"); value != "" {
		return value
	}
	if value := os.Getenv("NDEV_TOAST_INBOX"); value != "" {
		return value
	}
	return filepath.Join(home, ".nicos-dev", "notifications", "inbox.jsonl")
}

// Append queues one schema-compatible toast using the shared locked JSONL
// appender. Text fields are data, never command-line arguments or telemetry.
func Append(path string, toast Toast, now func() time.Time) error {
	if now == nil {
		now = time.Now
	}
	var displaySeconds *int
	if seconds, ok := unsignedDecimal(toast.Seconds); ok {
		displaySeconds = &seconds
	}
	payload := toastWire{
		Timestamp:      now().UTC().Format("2006-01-02T15:04:05Z"),
		Title:          toast.Title,
		Message:        toast.Body,
		Severity:       toast.Severity,
		URL:            pointerUnlessEmpty(toast.URL),
		ExecuteCommand: pointerUnlessEmpty(toast.ExecuteCommand),
		Source:         pointerUnlessEmpty(toast.Source),
		DisplaySeconds: displaySeconds,
	}
	line, err := jsonutil.Marshal(payload)
	if err != nil {
		return err
	}
	return jsonl.AppendLine(path, line, 0o644)
}

func unsignedDecimal(value string) (int, bool) {
	if value == "" {
		return 0, false
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			return 0, false
		}
	}
	result, err := strconv.Atoi(value)
	return result, err == nil
}

func pointerUnlessEmpty(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}
