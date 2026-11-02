package sessionpressure

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWorkOutputTeeIsInspectableAndPrivate(t *testing.T) {
	dir := t.TempDir()
	id := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	tee, err := openWorkOutputTee(dir, id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tee.Write([]byte("hello from leased command\n")); err != nil {
		t.Fatal(err)
	}

	out := collectWorkInspectOutput(dir, id, WorkInspectOrigin{ScheduleSnippet: "older schedule log"})
	if out.Source != WorkOutputWorkRun || !strings.Contains(out.Text, "hello from leased command") {
		t.Fatalf("output=%#v", out)
	}

	events, _ := filepath.Glob(filepath.Join(dir, "work-events-*.jsonl"))
	if len(events) != 0 {
		t.Fatalf("tee must not create work-events: %v", events)
	}

	removeWorkOutput(dir, id)
	gone := collectWorkInspectOutput(dir, id, WorkInspectOrigin{})
	if gone.Source != WorkOutputNone || gone.Text != "" {
		t.Fatalf("removed output=%#v", gone)
	}
}

func TestWorkOutputTeeKeepsTail(t *testing.T) {
	dir := t.TempDir()
	id := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	tee, err := openWorkOutputTee(dir, id)
	if err != nil {
		t.Fatal(err)
	}
	payload := strings.Repeat("a", workOutputTeeMax) + "TAIL-MARKER"
	if _, err := tee.Write([]byte(payload)); err != nil {
		t.Fatal(err)
	}
	out := collectWorkInspectOutput(dir, id, WorkInspectOrigin{})
	if out.Source != WorkOutputWorkRun || !strings.Contains(out.Text, "TAIL-MARKER") {
		t.Fatalf("output=%#v", out)
	}
	if !out.Truncated {
		t.Fatal("expected inspect payload to report truncation")
	}
	raw, err := os.ReadFile(workOutputPath(dir, id))
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) > workOutputTeeMax {
		t.Fatalf("tee file %d exceeds cap %d", len(raw), workOutputTeeMax)
	}
}

func TestPruneWorkOutputRemovesStalePreviewFiles(t *testing.T) {
	dir := t.TempDir()
	id := "dddddddddddddddddddddddddddddddd"
	tee, err := openWorkOutputTee(dir, id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tee.Write([]byte("stale\n")); err != nil {
		t.Fatal(err)
	}
	path := workOutputPath(dir, id)
	old := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	if err := pruneWorkOutput(dir, time.Hour); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("stale preview still present: %v", err)
	}
}

func TestWorkInspectOutputFallsBackToScheduleSnippet(t *testing.T) {
	out := collectWorkInspectOutput(t.TempDir(), "cccccccccccccccccccccccccccccccc", WorkInspectOrigin{
		ScheduleSnippet: "building standalone docs-puller",
	})
	if out.Source != WorkOutputSchedule || out.Text != "building standalone docs-puller" {
		t.Fatalf("output=%#v", out)
	}
}
