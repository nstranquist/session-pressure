package sessionpressurecontrol

import (
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nstranquist/session-pressure/pkg/sessionpressurecontrol"
)

// History rule: audit rotation archives the older generation (gzip beside
// the live file) instead of replacing audit.jsonl.1.
func TestAuditRotationArchivesEveryGeneration(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	store, err := newPreviewStore(root, func() time.Time { now = now.Add(time.Second); return now })
	if err != nil {
		t.Fatal(err)
	}
	live := filepath.Join(store.root, "audit.jsonl")
	big := strings.Repeat("x", 4<<20+1)
	for gen := 0; gen < 3; gen++ {
		if err := os.WriteFile(live, []byte(`{"note":"gen`+string(rune('0'+gen))+`","pad":"`+big+"\"}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := store.appendAudit(sessionpressurecontrol.AuditRecord{AuditID: "a", Action: "x", Outcome: "ok"}); err != nil {
			t.Fatal(err)
		}
	}
	gens, _ := filepath.Glob(filepath.Join(store.root, "audit.jsonl.*.gz"))
	if len(gens) != 3 {
		t.Fatalf("archived generations=%v, want 3", gens)
	}
	seen := map[string]bool{}
	for _, g := range gens {
		f, err := os.Open(g)
		if err != nil {
			t.Fatal(err)
		}
		zr, err := gzip.NewReader(f)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := io.ReadAll(zr)
		f.Close()
		seen[string(raw[:len(`{"note":"gen0`)])] = true
	}
	for _, want := range []string{`{"note":"gen0`, `{"note":"gen1`, `{"note":"gen2`} {
		if !seen[want] {
			t.Fatalf("generation %s lost: %v", want, seen)
		}
	}
	if _, err := os.Stat(live + ".1"); !os.IsNotExist(err) {
		t.Fatal("legacy .1 generation still produced")
	}
}
