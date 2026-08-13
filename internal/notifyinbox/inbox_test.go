package notifyinbox

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestAppendUsesStableToastWireContract(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "inbox.jsonl")
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	err := Append(path, Toast{
		Title: "Recovery", Body: "Inspect sessions", Severity: "warning",
		Source: "resident", Seconds: "12", ExecuteCommand: "ndev session recover",
	}, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var payload toastWire
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Timestamp != "2026-07-14T12:00:00Z" || payload.DisplaySeconds == nil || *payload.DisplaySeconds != 12 || payload.ExecuteCommand == nil || *payload.ExecuteCommand != "ndev session recover" {
		t.Fatalf("payload=%+v", payload)
	}
}

func TestUnsignedDecimalRejectsInvalidAndOverflow(t *testing.T) {
	for _, value := range []string{"", "-1", "+1", "1.5", "999999999999999999999999999999"} {
		if _, ok := unsignedDecimal(value); ok {
			t.Fatalf("accepted %q", value)
		}
	}
	if value, ok := unsignedDecimal("0"); !ok || value != 0 {
		t.Fatalf("zero=%d ok=%v", value, ok)
	}
}
