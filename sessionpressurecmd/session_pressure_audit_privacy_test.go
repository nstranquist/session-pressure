package sessionpressurecmd

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestAuditRevalidatesLegacyPrivacyVerdictsWithoutChangingHistory(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "events-2026-11-08-s01.jsonl")
	cache := filepath.Join(dir, "cache.json")
	now := time.Date(2026, 11, 8, 17, 0, 0, 0, time.UTC)
	raw := []byte(`{"ts":"2026-11-08T16:00:00Z","event":"fixture.mode","attrs":{"pw_mode":"/perfect-world-review"}}` + "\n")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	result, _, _, _ := scanCLIAuditPaths(cache, []string{path}, now.Add(-24*time.Hour), now)
	if result.ValidationErrors != 0 {
		t.Fatal(result)
	}
	stored, err := os.ReadFile(cache)
	if err != nil {
		t.Fatal(err)
	}
	var old cliAuditCache
	if err := json.Unmarshal(stored, &old); err != nil {
		t.Fatal(err)
	}
	old.SchemaVersion = 5
	for key, entry := range old.Entries {
		entry.JSONL.ValidationErrors = 1
		old.Entries[key] = entry
	}
	if err := writeCLIAuditCache(cache, old); err != nil {
		t.Fatal(err)
	}
	result, counts, _, _ := scanCLIAuditPaths(cache, []string{path}, now.Add(-24*time.Hour), now)
	if result.ValidationErrors != 0 || counts["fixture.mode"] != 1 {
		t.Fatalf("stale privacy verdict retained: %+v %v", result, counts)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(raw, after) {
		t.Fatal("source history was changed")
	}
}
