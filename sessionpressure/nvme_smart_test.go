package sessionpressure

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPersistNVMeSMARTDedupesUnchangedWear(t *testing.T) {
	dir := t.TempDir()
	snapshot := NVMeSMARTSnapshot{
		SchemaVersion:    nvmeSMARTSnapshotSchemaVersion,
		CapturedAt:       time.Date(2026, 8, 27, 18, 0, 0, 0, time.UTC),
		Source:           "test",
		Available:        true,
		Model:            "APPLE SSD AP1024R",
		Serial:           "test-serial",
		PercentageUsed:   10,
		DataUnitsWritten: 460343686,
		HostBytesWritten: dataUnitsToBytes(460343686),
	}
	appended, err := PersistNVMeSMART(dir, snapshot)
	if err != nil || !appended {
		t.Fatalf("first persist appended=%v err=%v", appended, err)
	}
	snapshot.CapturedAt = snapshot.CapturedAt.Add(time.Hour)
	appended, err = PersistNVMeSMART(dir, snapshot)
	if err != nil || appended {
		t.Fatalf("unchanged wear should not append jsonl: appended=%v err=%v", appended, err)
	}
	snapshot.DataUnitsWritten++
	snapshot.HostBytesWritten = dataUnitsToBytes(snapshot.DataUnitsWritten)
	snapshot.CapturedAt = snapshot.CapturedAt.Add(time.Hour)
	appended, err = PersistNVMeSMART(dir, snapshot)
	if err != nil || !appended {
		t.Fatalf("moved wear should append: appended=%v err=%v", appended, err)
	}
	body, err := os.ReadFile(nvmeSMARTHistoryPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	lines := 0
	for _, line := range splitLines(body) {
		if len(line) == 0 {
			continue
		}
		lines++
		var row NVMeSMARTSnapshot
		if err := json.Unmarshal(line, &row); err != nil {
			t.Fatal(err)
		}
	}
	if lines != 2 {
		t.Fatalf("history lines=%d want 2", lines)
	}
	latest, err := os.ReadFile(nvmeSMARTLatestPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	var latestSnapshot NVMeSMARTSnapshot
	if err := json.Unmarshal(latest, &latestSnapshot); err != nil {
		t.Fatal(err)
	}
	if latestSnapshot.DataUnitsWritten != snapshot.DataUnitsWritten {
		t.Fatalf("latest=%d want %d", latestSnapshot.DataUnitsWritten, snapshot.DataUnitsWritten)
	}
	if _, err := os.Stat(filepath.Join(dir, nvmeSMARTLatestFile)); err != nil {
		t.Fatal(err)
	}
}

func TestDataUnitsToBytesUsesNVMeSpec(t *testing.T) {
	if got := dataUnitsToBytes(2); got != 1_024_000 {
		t.Fatalf("dataUnitsToBytes(2)=%d want 1024000", got)
	}
}

func splitLines(body []byte) [][]byte {
	var lines [][]byte
	start := 0
	for i, b := range body {
		if b != '\n' {
			continue
		}
		lines = append(lines, body[start:i])
		start = i + 1
	}
	if start < len(body) {
		lines = append(lines, body[start:])
	}
	return lines
}
