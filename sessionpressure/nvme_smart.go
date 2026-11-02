package sessionpressure

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/nstranquist/session-pressure/internal/jsonl"
)

const (
	nvmeSMARTSnapshotSchemaVersion = 1
	nvmeSMARTLatestFile            = "nvme-smart-latest.json"
	nvmeSMARTHistoryFile           = "nvme-smart-health.jsonl"
	nvmeDataUnitBytes              = 1000 * 512
	maxNVMeSMARTHistoryRecordBytes = 4096
)

// NVMeSMARTSnapshot is a read-only lifetime wear sample from NVMe SMART.
// PercentageUsed is the vendor life estimate. HostBytesWritten is lifetime
// host writes (TBW), not the session-pressure 15-minute write-rate window.
type NVMeSMARTSnapshot struct {
	SchemaVersion                  int       `json:"schema_version"`
	CapturedAt                     time.Time `json:"captured_at"`
	Source                         string    `json:"source"`
	Available                      bool      `json:"available"`
	Reason                         string    `json:"reason,omitempty"`
	Model                          string    `json:"model,omitempty"`
	Serial                         string    `json:"serial,omitempty"`
	Firmware                       string    `json:"firmware,omitempty"`
	CriticalWarning                uint8     `json:"critical_warning"`
	TemperatureCelsius             float64   `json:"temperature_celsius,omitempty"`
	AvailableSparePercent          uint8     `json:"available_spare_percent,omitempty"`
	AvailableSpareThresholdPercent uint8     `json:"available_spare_threshold_percent,omitempty"`
	PercentageUsed                 uint8     `json:"percentage_used"`
	DataUnitsRead                  uint64    `json:"data_units_read"`
	DataUnitsWritten               uint64    `json:"data_units_written"`
	HostBytesRead                  uint64    `json:"host_bytes_read"`
	HostBytesWritten               uint64    `json:"host_bytes_written"`
	HostReadCommands               uint64    `json:"host_read_commands"`
	HostWriteCommands              uint64    `json:"host_write_commands"`
	ControllerBusyMinutes          uint64    `json:"controller_busy_minutes"`
	PowerCycles                    uint64    `json:"power_cycles"`
	PowerOnHours                   uint64    `json:"power_on_hours"`
	UnsafeShutdowns                uint64    `json:"unsafe_shutdowns"`
	MediaErrors                    uint64    `json:"media_errors"`
	ErrorInfoLogEntries            uint64    `json:"error_info_log_entries"`
	UnreliableFields               []string  `json:"unreliable_fields,omitempty"`
}

func nvmeSMARTLatestPath(dir string) string {
	return filepath.Join(dir, nvmeSMARTLatestFile)
}

func nvmeSMARTHistoryPath(dir string) string {
	return filepath.Join(dir, nvmeSMARTHistoryFile)
}

func dataUnitsToBytes(units uint64) uint64 {
	if units > ^uint64(0)/nvmeDataUnitBytes {
		return ^uint64(0)
	}
	return units * nvmeDataUnitBytes
}

func annotateUnreliableNVMeFields(snapshot *NVMeSMARTSnapshot) {
	if snapshot == nil {
		return
	}
	var unreliable []string
	if snapshot.ControllerBusyMinutes == 0 {
		unreliable = append(unreliable, "controller_busy_minutes")
	}
	// Apple ANS often leaves power-on hours far below calendar age.
	if snapshot.PowerOnHours == 0 || snapshot.ControllerBusyMinutes == 0 {
		unreliable = append(unreliable, "power_on_hours")
	}
	snapshot.UnreliableFields = unreliable
}

// ReadNVMeSMART reads the internal NVMe SMART log. It does not write to the
// drive. Unsupported platforms return Available=false without error.
func ReadNVMeSMART(ctx context.Context) (NVMeSMARTSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return NVMeSMARTSnapshot{}, err
	}
	snapshot, err := nativeReadNVMeSMART(ctx)
	if err != nil {
		return NVMeSMARTSnapshot{}, err
	}
	if snapshot.SchemaVersion == 0 {
		snapshot.SchemaVersion = nvmeSMARTSnapshotSchemaVersion
	}
	if snapshot.CapturedAt.IsZero() {
		snapshot.CapturedAt = time.Now().UTC()
	}
	if snapshot.Source == "" {
		snapshot.Source = "iokit.NVMeSMARTLib"
	}
	snapshot.HostBytesRead = dataUnitsToBytes(snapshot.DataUnitsRead)
	snapshot.HostBytesWritten = dataUnitsToBytes(snapshot.DataUnitsWritten)
	if snapshot.Available {
		annotateUnreliableNVMeFields(&snapshot)
	}
	return snapshot, nil
}

func lastNVMeSMARTHistoryLine(path string) (NVMeSMARTSnapshot, bool, error) {
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return NVMeSMARTSnapshot{}, false, nil
	}
	if err != nil {
		return NVMeSMARTSnapshot{}, false, err
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 4096), maxNVMeSMARTHistoryRecordBytes*2)
	var last []byte
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		last = append(last[:0], line...)
	}
	if err := scanner.Err(); err != nil {
		return NVMeSMARTSnapshot{}, false, err
	}
	if len(last) == 0 {
		return NVMeSMARTSnapshot{}, false, nil
	}
	var snapshot NVMeSMARTSnapshot
	if err := json.Unmarshal(last, &snapshot); err != nil {
		return NVMeSMARTSnapshot{}, false, err
	}
	return snapshot, true, nil
}

func nvmeSMARTWearUnchanged(previous, next NVMeSMARTSnapshot) bool {
	return previous.Serial == next.Serial &&
		previous.PercentageUsed == next.PercentageUsed &&
		previous.DataUnitsWritten == next.DataUnitsWritten &&
		previous.AvailableSparePercent == next.AvailableSparePercent &&
		previous.MediaErrors == next.MediaErrors &&
		previous.CriticalWarning == next.CriticalWarning
}

// PersistNVMeSMART writes latest.json always and appends jsonl only when wear
// counters moved. That keeps a cheap lifetime trend without duplicating idle samples.
func PersistNVMeSMART(dir string, snapshot NVMeSMARTSnapshot) (appended bool, err error) {
	if strings.TrimSpace(dir) == "" {
		return false, errors.New("nvme SMART store directory is required")
	}
	if snapshot.SchemaVersion == 0 {
		snapshot.SchemaVersion = nvmeSMARTSnapshotSchemaVersion
	}
	if snapshot.CapturedAt.IsZero() {
		snapshot.CapturedAt = time.Now().UTC()
	}
	body, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return false, err
	}
	body = append(body, '\n')
	if err := atomicWrite(nvmeSMARTLatestPath(dir), body, 0o600); err != nil {
		return false, err
	}
	historyPath := nvmeSMARTHistoryPath(dir)
	previous, found, err := lastNVMeSMARTHistoryLine(historyPath)
	if err != nil {
		return false, err
	}
	if found && nvmeSMARTWearUnchanged(previous, snapshot) {
		return false, nil
	}
	line, err := json.Marshal(snapshot)
	if err != nil {
		return false, err
	}
	if len(line) > maxNVMeSMARTHistoryRecordBytes {
		return false, fmt.Errorf("nvme SMART history record is %d bytes above %d-byte ceiling", len(line), maxNVMeSMARTHistoryRecordBytes)
	}
	if err := jsonl.AppendLine(historyPath, line, 0o600); err != nil {
		return false, err
	}
	return true, nil
}
