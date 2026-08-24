package sessionpressurecmd

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestMeasureStorageHotnessCompletesPartitionedTree(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 8, 24, 2, 0, 0, 0, time.UTC)
	for partition := range 16 {
		directory := filepath.Join(root, string(rune('a'+partition)))
		if err := os.Mkdir(directory, 0o755); err != nil {
			t.Fatal(err)
		}
		for item := range 8 {
			path := filepath.Join(directory, string(rune('a'+item)))
			body := []byte{byte(partition), byte(item)}
			if err := os.WriteFile(path, body, 0o600); err != nil {
				t.Fatal(err)
			}
			modified := now.Add(-time.Hour)
			if item%2 == 0 {
				modified = now.Add(-8 * 24 * time.Hour)
			}
			if err := os.Chtimes(path, modified, modified); err != nil {
				t.Fatal(err)
			}
		}
	}

	evidence, err := measureStorageHotness(root, now)
	if err != nil {
		t.Fatalf("measureStorageHotness: %v", err)
	}
	if !evidence.Available || !evidence.Complete {
		t.Fatalf("evidence is incomplete: %+v", evidence)
	}
	if evidence.ItemCount != 128 || evidence.TotalBytes != 256 || evidence.HotBytes != 128 || evidence.ColdEligibleBytes != 128 {
		t.Fatalf("evidence totals = %+v", evidence)
	}
	if evidence.AgeBuckets[0].ItemCount != 64 || evidence.AgeBuckets[2].ItemCount != 64 {
		t.Fatalf("age buckets = %+v", evidence.AgeBuckets)
	}
}
