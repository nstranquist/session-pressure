package sessionpressure

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStorageReceiptStorePersistsPrivateBoundedHistory(t *testing.T) {
	dir := t.TempDir()
	store := NewStorageReceiptStore(dir)
	now := time.Date(2026, 7, 14, 22, 0, 0, 0, time.UTC)
	id := "00000000000000000000000000000001"
	row := StorageReceipt{AttemptID: id, ProviderID: "pnpm-store", Mode: "result", StartedAt: now, FinishedAt: now.Add(time.Second), BeforeAvailableBytes: 10, AfterAvailableBytes: 20, BeforeProviderBytes: 30, AfterProviderBytes: 20, ReclaimedBytes: 10, Outcome: "completed"}
	if err := store.Append(row); err != nil {
		t.Fatal(err)
	}
	path := store.dayPath(row.FinishedAt)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o", info.Mode().Perm())
	}
	rows, err := store.Read(now.Add(-time.Hour), 10)
	if err != nil || len(rows) != 1 || rows[0].ProviderID != "pnpm-store" {
		t.Fatalf("rows=%+v err=%v", rows, err)
	}
}

func TestStorageReceiptStoreFailsClosedOnMalformedHistory(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "storage-actions-20260714.jsonl")
	if err := os.WriteFile(path, []byte("not-json\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewStorageReceiptStore(dir).Read(time.Time{}, 10); err == nil {
		t.Fatal("expected malformed history failure")
	}
}

func TestStorageReclaimLockIsExclusive(t *testing.T) {
	dir := t.TempDir()
	unlock, err := AcquireStorageReclaimLock(t.Context(), dir, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	if _, err := AcquireStorageReclaimLock(t.Context(), dir, 20*time.Millisecond); err == nil {
		t.Fatal("expected second reclaim lock to fail")
	}
}
