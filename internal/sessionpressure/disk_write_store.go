package sessionpressure

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/nstranquist/session-pressure/internal/jsonl"
)

const (
	maxDiskWriteHistoryRecordBytes = 2560
	diskWriteAlertStateFile        = "disk-write-alert-state.json"
	diskWriteCheckpointFile        = "disk-write-current.json"
)

type diskWriteAlertStateRecord struct {
	SchemaVersion int       `json:"schema_version"`
	ModelVersion  string    `json:"model_version"`
	Day           string    `json:"day"`
	Alerts        int       `json:"alerts"`
	IncidentOpen  bool      `json:"incident_open"`
	UpdatedAt     time.Time `json:"updated_at"`
}

type diskHistogramBucket struct {
	Index int    `json:"index"`
	Count uint64 `json:"count"`
}

type diskHistogramRecord struct {
	Zero    uint64                `json:"zero,omitempty"`
	Buckets []diskHistogramBucket `json:"buckets,omitempty"`
	Count   uint64                `json:"count"`
	Sum     uint64                `json:"sum,omitempty"`
	FirstAt time.Time             `json:"first_at,omitempty,omitzero"`
	LastAt  time.Time             `json:"last_at,omitempty,omitzero"`
}

func (histogram diskHistogram) record() diskHistogramRecord {
	record := diskHistogramRecord{Zero: histogram.Zero, Count: histogram.Count, Sum: histogram.Sum, FirstAt: histogram.FirstAt, LastAt: histogram.LastAt}
	for index, count := range histogram.Buckets {
		if count > 0 {
			record.Buckets = append(record.Buckets, diskHistogramBucket{Index: index, Count: count})
		}
	}
	return record
}

func diskHistogramFromRecord(record diskHistogramRecord) diskHistogram {
	histogram := diskHistogram{}
	histogram.merge(record)
	return histogram
}

type diskExecutableHistory struct {
	Executable string              `json:"executable"`
	TotalBytes uint64              `json:"total_bytes,omitempty"`
	Histogram  diskHistogramRecord `json:"histogram"`
}

type diskWriteHistoryRecord struct {
	SchemaVersion    int                            `json:"schema_version"`
	ModelVersion     string                         `json:"model_version"`
	Hour             time.Time                      `json:"hour"`
	State            DiskWriteState                 `json:"state,omitempty"`
	BytesWritten     uint64                         `json:"bytes_written,omitempty"`
	UnscoredGapBytes uint64                         `json:"unscored_gap_bytes,omitempty"`
	SampleCount      uint64                         `json:"sample_count,omitempty"`
	BaselineP99Bytes uint64                         `json:"baseline_p99_bytes,omitempty"`
	Contexts         map[string]diskHistogramRecord `json:"contexts"`
	Executables      []diskExecutableHistory        `json:"executables,omitempty"`
	LastSampleAt     time.Time                      `json:"last_sample_at,omitempty,omitzero"`
	DeviceIdentity   string                         `json:"device_identity,omitempty"`
	DeviceBytes      uint64                         `json:"device_bytes,omitempty"`
	DeviceCount      int                            `json:"device_count,omitempty"`
	DeviceSource     string                         `json:"device_source,omitempty"`

	executableHistograms map[string]*diskHistogram
}

type DiskWriteHistoryPoint struct {
	Hour             time.Time      `json:"hour"`
	State            DiskWriteState `json:"state,omitempty"`
	BytesWritten     uint64         `json:"bytes_written,omitempty"`
	UnscoredGapBytes uint64         `json:"unscored_gap_bytes,omitempty"`
	BaselineP99Bytes uint64         `json:"baseline_p99_bytes,omitempty"`
	SampleCount      uint64         `json:"sample_count"`
}

type DiskWriteStore struct {
	Dir string
	Now func() time.Time
}

func NewDiskWriteStore(dir string) *DiskWriteStore {
	return &DiskWriteStore{Dir: dir, Now: time.Now}
}

func (store *DiskWriteStore) dayPath(at time.Time) string {
	return filepath.Join(store.Dir, "disk-writes-"+at.Local().Format("20060102")+".jsonl")
}

func (store *DiskWriteStore) alertStatePath() string {
	return filepath.Join(store.Dir, diskWriteAlertStateFile)
}

func (store *DiskWriteStore) checkpointPath() string {
	return filepath.Join(store.Dir, diskWriteCheckpointFile)
}

func (store *DiskWriteStore) loadAlertState() (diskWriteAlertStateRecord, bool, error) {
	if store == nil || strings.TrimSpace(store.Dir) == "" {
		return diskWriteAlertStateRecord{}, false, nil
	}
	body, err := os.ReadFile(store.alertStatePath())
	if errors.Is(err, os.ErrNotExist) {
		return diskWriteAlertStateRecord{}, false, nil
	}
	if err != nil {
		return diskWriteAlertStateRecord{}, false, err
	}
	var record diskWriteAlertStateRecord
	if err := json.Unmarshal(body, &record); err != nil {
		return diskWriteAlertStateRecord{}, true, fmt.Errorf("decode disk-write alert state: %w", err)
	}
	if record.SchemaVersion != 1 || record.ModelVersion != diskWriteModelVersion ||
		len(record.Day) != 8 || record.Alerts < 0 || record.Alerts > diskWriteAlertLimitPerDay {
		return diskWriteAlertStateRecord{}, true, errors.New("disk-write alert state is invalid")
	}
	return record, true, nil
}

func (store *DiskWriteStore) saveAlertState(record diskWriteAlertStateRecord) error {
	if store == nil || strings.TrimSpace(store.Dir) == "" {
		return nil
	}
	record.SchemaVersion = 1
	record.ModelVersion = diskWriteModelVersion
	if record.UpdatedAt.IsZero() {
		now := time.Now
		if store.Now != nil {
			now = store.Now
		}
		record.UpdatedAt = now().UTC()
	}
	body, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return err
	}
	body = append(body, '\n')
	return atomicWrite(store.alertStatePath(), body, 0o600)
}

func boundedDiskWriteHistoryRecord(record diskWriteHistoryRecord) (diskWriteHistoryRecord, []byte, error) {
	if record.SchemaVersion == 0 {
		record.SchemaVersion = 1
	}
	if record.ModelVersion == "" {
		record.ModelVersion = diskWriteModelVersion
	}
	if record.Hour.IsZero() {
		return record, nil, errors.New("disk-write history hour is required")
	}
	for {
		body, err := json.Marshal(record)
		if err != nil {
			return record, nil, err
		}
		if len(body) <= maxDiskWriteHistoryRecordBytes {
			return record, body, nil
		}
		if len(record.Executables) == 0 {
			return record, nil, fmt.Errorf("disk-write history record is %d bytes above %d-byte ceiling", len(body), maxDiskWriteHistoryRecordBytes)
		}
		record.Executables = record.Executables[:len(record.Executables)-1]
	}
}

func (store *DiskWriteStore) appendRecord(record diskWriteHistoryRecord) error {
	if store == nil || strings.TrimSpace(store.Dir) == "" {
		return errors.New("disk-write store directory is required")
	}
	record, body, err := boundedDiskWriteHistoryRecord(record)
	if err != nil {
		return err
	}
	return jsonl.AppendLine(store.dayPath(record.Hour), body, 0o600)
}

func (store *DiskWriteStore) writeCheckpoint(record diskWriteHistoryRecord) error {
	if store == nil || strings.TrimSpace(store.Dir) == "" {
		return errors.New("disk-write store directory is required")
	}
	_, body, err := boundedDiskWriteHistoryRecord(record)
	if err != nil {
		return err
	}
	body = append(body, '\n')
	return atomicWrite(store.checkpointPath(), body, 0o600)
}

func (store *DiskWriteStore) loadCheckpoint(since time.Time) (diskWriteHistoryRecord, bool, error) {
	if store == nil || strings.TrimSpace(store.Dir) == "" {
		return diskWriteHistoryRecord{}, false, nil
	}
	body, err := os.ReadFile(store.checkpointPath())
	if errors.Is(err, os.ErrNotExist) {
		return diskWriteHistoryRecord{}, false, nil
	}
	if err != nil {
		return diskWriteHistoryRecord{}, false, err
	}
	if len(body) > maxDiskWriteHistoryRecordBytes+1 {
		return diskWriteHistoryRecord{}, true, fmt.Errorf("disk-write checkpoint is %d bytes above %d-byte ceiling", len(body), maxDiskWriteHistoryRecordBytes)
	}
	var record diskWriteHistoryRecord
	if err := json.Unmarshal(body, &record); err != nil {
		return diskWriteHistoryRecord{}, true, fmt.Errorf("decode disk-write checkpoint: %w", err)
	}
	if record.SchemaVersion != 1 || record.ModelVersion != diskWriteModelVersion || record.Hour.IsZero() || record.Hour.Before(since) {
		return diskWriteHistoryRecord{}, false, nil
	}
	return record, true, nil
}

func (store *DiskWriteStore) removeCheckpoint() error {
	if store == nil || strings.TrimSpace(store.Dir) == "" {
		return nil
	}
	err := os.Remove(store.checkpointPath())
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func diskWriteCheckpointSuperseded(records []diskWriteHistoryRecord, checkpoint diskWriteHistoryRecord) bool {
	for _, record := range records {
		if !record.Hour.Equal(checkpoint.Hour) {
			continue
		}
		if (record.LastSampleAt.After(checkpoint.LastSampleAt) || record.LastSampleAt.Equal(checkpoint.LastSampleAt)) &&
			record.DeviceIdentity == checkpoint.DeviceIdentity &&
			record.DeviceBytes >= checkpoint.DeviceBytes {
			return true
		}
	}
	return false
}

func (store *DiskWriteStore) loadRecords(since time.Time) ([]diskWriteHistoryRecord, error) {
	if store == nil || strings.TrimSpace(store.Dir) == "" {
		return nil, nil
	}
	matches, err := filepath.Glob(filepath.Join(store.Dir, "disk-writes-*.jsonl"))
	if err != nil {
		return nil, err
	}
	sort.Strings(matches)
	records := make([]diskWriteHistoryRecord, 0, len(matches)*24)
	for _, path := range matches {
		file, openErr := os.Open(path)
		if openErr != nil {
			return nil, openErr
		}
		scanner := bufio.NewScanner(file)
		scanner.Buffer(make([]byte, 4096), maxDiskWriteHistoryRecordBytes*2)
		for scanner.Scan() {
			var record diskWriteHistoryRecord
			if json.Unmarshal(scanner.Bytes(), &record) != nil || record.SchemaVersion != 1 || record.ModelVersion != diskWriteModelVersion || record.Hour.Before(since) {
				continue
			}
			records = append(records, record)
		}
		scanErr := scanner.Err()
		_ = file.Close()
		if scanErr != nil {
			return nil, scanErr
		}
	}
	sort.Slice(records, func(i, j int) bool { return records[i].Hour.Before(records[j].Hour) })
	return records, nil
}

func (store *DiskWriteStore) loadRecordsWithCheckpoint(since time.Time) ([]diskWriteHistoryRecord, *diskWriteHistoryRecord, error) {
	records, err := store.loadRecords(since)
	if err != nil {
		return nil, nil, err
	}
	checkpoint, found, checkpointErr := store.loadCheckpoint(since)
	if checkpointErr != nil {
		return nil, nil, checkpointErr
	}
	if !found || diskWriteCheckpointSuperseded(records, checkpoint) {
		return records, nil, nil
	}
	records = append(records, checkpoint)
	sort.SliceStable(records, func(i, j int) bool {
		if records[i].Hour.Equal(records[j].Hour) {
			return records[i].LastSampleAt.Before(records[j].LastSampleAt)
		}
		return records[i].Hour.Before(records[j].Hour)
	})
	copy := checkpoint
	return records, &copy, nil
}

func (store *DiskWriteStore) ReadHistory(limit int, since time.Time) ([]DiskWriteHistoryPoint, int, error) {
	records, _, err := store.loadRecordsWithCheckpoint(since)
	if err != nil {
		return nil, 0, err
	}
	byHour := make(map[time.Time]*DiskWriteHistoryPoint, len(records))
	latestByHour := make(map[time.Time]diskWriteHistoryRecord, len(records))
	hours := make([]time.Time, 0, len(records))
	for _, record := range records {
		hour := record.Hour.UTC()
		point := byHour[hour]
		if point == nil {
			point = &DiskWriteHistoryPoint{Hour: hour}
			byHour[hour] = point
			hours = append(hours, hour)
		}
		if latest, ok := latestByHour[hour]; !ok || diskWriteHistoryRecordNewer(record, latest) {
			point.State = record.State
			point.BaselineP99Bytes = record.BaselineP99Bytes
			latestByHour[hour] = record
		}
		point.BytesWritten = saturatingAddUint64(point.BytesWritten, record.BytesWritten)
		point.UnscoredGapBytes = saturatingAddUint64(point.UnscoredGapBytes, record.UnscoredGapBytes)
		count := record.SampleCount
		if count == 0 {
			for _, histogram := range record.Contexts {
				count += histogram.Count
			}
		}
		point.SampleCount = saturatingAddUint64(point.SampleCount, count)
	}
	sort.Slice(hours, func(i, j int) bool { return hours[i].Before(hours[j]) })
	available := len(hours)
	if limit <= 0 {
		limit = 20
	}
	if limit > 200 {
		limit = 200
	}
	if len(hours) > limit {
		hours = hours[len(hours)-limit:]
	}
	points := make([]DiskWriteHistoryPoint, 0, len(hours))
	for index := len(hours) - 1; index >= 0; index-- {
		points = append(points, *byHour[hours[index]])
	}
	return points, available, nil
}

func diskWriteHistoryRecordNewer(candidate, current diskWriteHistoryRecord) bool {
	candidateAt := candidate.LastSampleAt
	if candidateAt.IsZero() {
		candidateAt = candidate.Hour
	}
	currentAt := current.LastSampleAt
	if currentAt.IsZero() {
		currentAt = current.Hour
	}
	if !candidateAt.Equal(currentAt) {
		return candidateAt.After(currentAt)
	}
	if candidate.DeviceBytes != current.DeviceBytes {
		return candidate.DeviceBytes > current.DeviceBytes
	}
	if candidate.SampleCount != current.SampleCount {
		return candidate.SampleCount > current.SampleCount
	}
	if candidate.BytesWritten != current.BytesWritten {
		return candidate.BytesWritten > current.BytesWritten
	}
	if candidate.UnscoredGapBytes != current.UnscoredGapBytes {
		return candidate.UnscoredGapBytes > current.UnscoredGapBytes
	}
	if candidate.State != current.State {
		return candidate.State > current.State
	}
	return candidate.BaselineP99Bytes > current.BaselineP99Bytes
}

func saturatingAddUint64(left, right uint64) uint64 {
	if left > ^uint64(0)-right {
		return ^uint64(0)
	}
	return left + right
}

func (store *DiskWriteStore) Prune(retentionDays int) error {
	if store == nil || retentionDays < 1 {
		return nil
	}
	now := time.Now
	if store.Now != nil {
		now = store.Now
	}
	cutoff := now().AddDate(0, 0, -retentionDays)
	cutoffDay := time.Date(cutoff.Year(), cutoff.Month(), cutoff.Day(), 0, 0, 0, 0, time.Local)
	matches, err := filepath.Glob(filepath.Join(store.Dir, "disk-writes-*.jsonl"))
	if err != nil {
		return err
	}
	for _, path := range matches {
		dateText := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(path), "disk-writes-"), ".jsonl")
		day, parseErr := time.ParseInLocation("20060102", dateText, time.Local)
		if parseErr != nil || !day.Before(cutoffDay) {
			continue
		}
		if removeErr := os.Remove(path); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			return removeErr
		}
	}
	return nil
}
