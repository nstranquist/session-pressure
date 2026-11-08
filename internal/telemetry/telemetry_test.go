package telemetry

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestWriterHealthAndPrivacy(t *testing.T) {
	t.Setenv("SESSION_PRESSURE_TELEMETRY_DIR", t.TempDir())
	t.Setenv("NDEV_PRESSURE_WRAPPER", "")
	t.Setenv("NDEV_TELEMETRY", "1")
	if got := ReadProductWriterHealthSince(time.Now().Add(-time.Hour)); got.Known {
		t.Fatal("missing authority reported known")
	}
	if err := RecordCommand("work.status", time.Now(), 11); err != nil {
		t.Fatal(err)
	}
	h := ReadProductWriterHealthSince(time.Now().Add(-time.Hour))
	if !h.Known || h.Attempted != 1 || h.Written != 1 || h.Failed != 0 || h.Gaps != 0 || h.LatencySamples != 1 {
		t.Fatalf("health %+v", h)
	}
	paths, _ := filepath.Glob(filepath.Join(filepath.Dir(DefaultSourceTelemetryPathAt(ProductSourceID, time.Now())), "events-*.jsonl"))
	if len(paths) != 1 {
		t.Fatal(paths)
	}
	raw, err := os.ReadFile(paths[0])
	if err != nil {
		t.Fatal(err)
	}
	var e Event
	if err = json.Unmarshal(raw, &e); err != nil {
		t.Fatal(err)
	}
	if e.Attrs["outcome"] != "policy_denied" || e.Attrs["operation_id"] != "session.pressure" {
		t.Fatalf("event %+v", e)
	}
	if v := ValidateEventPrivacy(e); len(v) != 0 {
		t.Fatal(v)
	}
	e.Attrs["nested"] = map[string]any{"content": []any{"/Users/person/private", "bearer abc"}}
	if v := ValidateEventPrivacy(e); len(v) != 2 {
		t.Fatalf("privacy validation %+v", v)
	}
}
func TestWrapperRecordsExactlyOnce(t *testing.T) {
	t.Setenv("SESSION_PRESSURE_TELEMETRY_DIR", t.TempDir())
	t.Setenv("NDEV_PRESSURE_WRAPPER", "1")
	if err := RecordCommand("doctor", time.Now(), 0); err != nil {
		t.Fatal(err)
	}
	if got := ReadProductWriterHealthSince(time.Now().Add(-time.Hour)); got.Known {
		t.Fatal("factory-wrapped call was recorded by product")
	}
}
func TestCorruptWriterAuthorityIsUnknown(t *testing.T) {
	t.Setenv("SESSION_PRESSURE_TELEMETRY_DIR", t.TempDir())
	p := filepath.Join(filepath.Dir(DefaultSourceTelemetryPathAt(ProductSourceID, time.Now())), ".writer-health-s01.json")
	os.MkdirAll(filepath.Dir(p), 0700)
	os.WriteFile(p, []byte("broken"), 0600)
	if got := ReadProductWriterHealthSince(time.Now().Add(-time.Hour)); got.Known {
		t.Fatal("corrupt writer reported known")
	}
}

func TestConcurrentWriterCountsAndPrivateStream(t *testing.T) {
	t.Setenv("SESSION_PRESSURE_TELEMETRY_DIR", t.TempDir())
	t.Setenv("NDEV_PRESSURE_WRAPPER", "")
	t.Setenv("NDEV_TELEMETRY", "1")
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := RecordCommand("status", time.Now(), 0); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	h := ReadProductWriterHealthSince(time.Now().Add(-time.Hour))
	if !h.Known || h.Attempted != 12 || h.Written != 12 || h.Gaps != 0 {
		t.Fatalf("%+v", h)
	}
}
func TestWriterRefusesSymlinkAndCountsFailure(t *testing.T) {
	root := t.TempDir()
	t.Setenv("SESSION_PRESSURE_TELEMETRY_DIR", root)
	t.Setenv("NDEV_PRESSURE_WRAPPER", "")
	t.Setenv("NDEV_TELEMETRY", "1")
	target := filepath.Join(root, "private")
	os.WriteFile(target, []byte("preserve"), 0600)
	path := DefaultSourceTelemetryPathAt(ProductSourceID, time.Now())
	path = path[:len(path)-6] + fmt.Sprintf("-s%02d.jsonl", os.Getpid()%64)
	os.MkdirAll(filepath.Dir(path), 0700)
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	if err := RecordCommand("status", time.Now(), 0); err == nil {
		t.Fatal("symlink accepted")
	}
	raw, _ := os.ReadFile(target)
	if string(raw) != "preserve" {
		t.Fatal("target modified")
	}
	h := ReadProductWriterHealthSince(time.Now().Add(-time.Hour))
	if !h.Known || h.Failed != 1 || h.Attempted != 1 {
		t.Fatalf("%+v", h)
	}
}
func TestRetentionOnlyRemovesOwnedExpiredStreams(t *testing.T) {
	dir := t.TempDir()
	old := "events-2020-01-01-s01.jsonl"
	foreign := "events-2020-01-01-sXX.jsonl"
	for _, n := range []string{old, foreign, "events-2026-11-08-s01.jsonl"} {
		os.WriteFile(filepath.Join(dir, n), nil, 0600)
	}
	if err := pruneCommandStreams(dir, time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, old)); !os.IsNotExist(err) {
		t.Fatal("old stream retained")
	}
	if _, err := os.Stat(filepath.Join(dir, foreign)); err != nil {
		t.Fatal("foreign stream removed")
	}
}
