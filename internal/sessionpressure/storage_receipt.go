package sessionpressure

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/nstranquist/session-pressure/internal/filelock"
	"github.com/nstranquist/session-pressure/internal/jsonl"
)

const StorageReceiptSchemaVersion = 1

type StorageReceipt struct {
	SchemaVersion         int       `json:"schema_version"`
	AttemptID             string    `json:"attempt_id"`
	ProviderID            string    `json:"provider_id"`
	Mode                  string    `json:"mode"`
	StartedAt             time.Time `json:"started_at"`
	FinishedAt            time.Time `json:"finished_at"`
	BeforeAvailableBytes  int64     `json:"before_available_bytes"`
	AfterAvailableBytes   int64     `json:"after_available_bytes"`
	BeforeProviderBytes   int64     `json:"before_provider_bytes"`
	AfterProviderBytes    int64     `json:"after_provider_bytes"`
	ProviderBytesMeasured bool      `json:"provider_bytes_measured"`
	ReclaimedBytes        int64     `json:"reclaimed_bytes"`
	Outcome               string    `json:"outcome"`
	Error                 string    `json:"error,omitempty"`
}

func (receipt *StorageReceipt) Normalize() {
	if receipt.SchemaVersion == 0 {
		receipt.SchemaVersion = StorageReceiptSchemaVersion
	}
	receipt.ProviderID = boundedText(strings.TrimSpace(receipt.ProviderID), 64)
	receipt.Mode = boundedText(strings.TrimSpace(receipt.Mode), 32)
	receipt.Outcome = boundedText(strings.TrimSpace(receipt.Outcome), 64)
	receipt.Error = boundedText(strings.TrimSpace(receipt.Error), 512)
	if receipt.ReclaimedBytes < 0 {
		receipt.ReclaimedBytes = 0
	}
}

func (receipt StorageReceipt) Validate() error {
	if receipt.SchemaVersion != StorageReceiptSchemaVersion {
		return fmt.Errorf("unsupported storage receipt schema_version %d", receipt.SchemaVersion)
	}
	if !validPrivateID(receipt.AttemptID) {
		return errors.New("storage receipt requires a private attempt_id")
	}
	if receipt.ProviderID == "" || receipt.Mode == "" || receipt.Outcome == "" {
		return errors.New("storage receipt requires provider_id, mode, and outcome")
	}
	if receipt.StartedAt.IsZero() || receipt.FinishedAt.Before(receipt.StartedAt) {
		return errors.New("storage receipt timestamps are invalid")
	}
	for _, value := range []int64{receipt.BeforeAvailableBytes, receipt.AfterAvailableBytes, receipt.BeforeProviderBytes, receipt.AfterProviderBytes, receipt.ReclaimedBytes} {
		if value < 0 {
			return errors.New("storage receipt byte counts cannot be negative")
		}
	}
	return nil
}

type StorageReceiptStore struct {
	Dir string
	Now func() time.Time
}

func NewStorageReceiptStore(dir string) *StorageReceiptStore {
	return &StorageReceiptStore{Dir: dir, Now: time.Now}
}

func (store *StorageReceiptStore) dayPath(at time.Time) string {
	return filepath.Join(store.Dir, "storage-actions-"+at.Local().Format("20060102")+".jsonl")
}

func (store *StorageReceiptStore) Append(receipt StorageReceipt) error {
	if store == nil || strings.TrimSpace(store.Dir) == "" {
		return errors.New("storage receipt store directory is required")
	}
	receipt.Normalize()
	if err := receipt.Validate(); err != nil {
		return err
	}
	body, err := json.Marshal(receipt)
	if err != nil {
		return err
	}
	return jsonl.AppendLineDurable(store.dayPath(receipt.FinishedAt), body, 0o600)
}

func (store *StorageReceiptStore) Read(since time.Time, limit int) ([]StorageReceipt, error) {
	if store == nil || strings.TrimSpace(store.Dir) == "" {
		return nil, errors.New("storage receipt store directory is required")
	}
	if limit <= 0 {
		limit = 100
	}
	paths, err := filepath.Glob(filepath.Join(store.Dir, "storage-actions-*.jsonl"))
	if err != nil {
		return nil, err
	}
	sort.Strings(paths)
	rows := make([]StorageReceipt, 0)
	for _, path := range paths {
		file, openErr := os.Open(path)
		if openErr != nil {
			return nil, openErr
		}
		scanner := bufio.NewScanner(file)
		scanner.Buffer(make([]byte, 64*1024), 1024*1024)
		for scanner.Scan() {
			var row StorageReceipt
			if err := json.Unmarshal(scanner.Bytes(), &row); err != nil {
				_ = file.Close()
				return nil, fmt.Errorf("decode storage receipt %s: %w", path, err)
			}
			if err := row.Validate(); err != nil {
				_ = file.Close()
				return nil, fmt.Errorf("validate storage receipt %s: %w", path, err)
			}
			if row.FinishedAt.Before(since) {
				continue
			}
			rows = append(rows, row)
		}
		if err := scanner.Err(); err != nil {
			_ = file.Close()
			return nil, err
		}
		if err := file.Close(); err != nil {
			return nil, err
		}
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].FinishedAt.After(rows[j].FinishedAt) })
	if len(rows) > limit {
		rows = rows[:limit]
	}
	return rows, nil
}

func AcquireStorageReclaimLock(ctx context.Context, dir string, wait time.Duration) (func(), error) {
	if strings.TrimSpace(dir) == "" {
		return nil, errors.New("storage reclaim directory is required")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	return filelock.AcquireContext(ctx, filepath.Join(dir, "storage-reclaim"), wait)
}
