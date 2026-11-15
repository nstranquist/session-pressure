package sessionpressure

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeGoModule(t *testing.T, root, module string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, "pkg", "a"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module "+module+"\n\ngo 1.24\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestPlacementFamilyKeyCollapsesCheckoutsAndSelection(t *testing.T) {
	base := t.TempDir()
	one, two, other := filepath.Join(base, "checkout-1"), filepath.Join(base, "checkout-2"), filepath.Join(base, "other")
	writeGoModule(t, one, "example.com/svc")
	writeGoModule(t, two, "example.com/svc")
	writeGoModule(t, other, "example.com/other")

	key := PlacementFamilyKey(one, []string{"go", "test", "-count=1", "-run", "TestA|TestB", "./pkg/b", "./pkg/a"})
	if !validSHA256Digest(key) {
		t.Fatalf("not a digest: %s", key)
	}
	same := map[string]string{
		"another checkout":        PlacementFamilyKey(two, []string{"go", "test", "./pkg/a", "./pkg/b"}),
		"selection and verbosity": PlacementFamilyKey(two, []string{"/usr/local/go/bin/go", "test", "-v", "-skip=TestC", "-timeout", "5m", "./pkg/a", "./pkg/b"}),
		"absolute package path":   PlacementFamilyKey(two, []string{"go", "test", filepath.Join(two, "pkg", "a"), "./pkg/b"}),
	}
	for name, got := range same {
		if got != key {
			t.Errorf("%s: family split", name)
		}
	}
	different := map[string]string{
		"other module":       PlacementFamilyKey(other, []string{"go", "test", "./pkg/a", "./pkg/b"}),
		"other packages":     PlacementFamilyKey(one, []string{"go", "test", "./..."}),
		"race build":         PlacementFamilyKey(one, []string{"go", "test", "-race", "./pkg/a", "./pkg/b"}),
		"other subdirectory": PlacementFamilyKey(filepath.Join(one, "pkg"), []string{"go", "test", "./pkg/a", "./pkg/b"}),
		"other subcommand":   PlacementFamilyKey(one, []string{"go", "vet", "./pkg/a", "./pkg/b"}),
	}
	for name, got := range different {
		if got == key {
			t.Errorf("%s: distinct checks share a family", name)
		}
	}
	// The exact key still separates what the family merges.
	if PlacementKey(one, []string{"go", "test", "./pkg/a"}) == PlacementKey(two, []string{"go", "test", "./pkg/a"}) {
		t.Fatal("exact key merged two checkouts")
	}
}

func TestPlacementFamilyKeyNamesNodePackages(t *testing.T) {
	base := t.TempDir()
	for _, dir := range []string{"a", "b"} {
		if err := os.MkdirAll(filepath.Join(base, dir), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(base, dir, "package.json"), []byte(`{"name":"web"}`), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if PlacementFamilyKey(filepath.Join(base, "a"), []string{"pnpm", "test", "-t", "renders"}) != PlacementFamilyKey(filepath.Join(base, "b"), []string{"pnpm", "test"}) {
		t.Fatal("same package in two checkouts split")
	}
}

func TestPlacerFallsBackToFamilyHistory(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC()
	keys := PlacementKeys{Exact: PlacementKey("/tmp/c1", []string{"go", "test", "-run", "X", "./a"}), Family: PlacementFamilyKey("/tmp/c1", []string{"go", "test", "./a"})}
	siblingExact := PlacementKey("/tmp/c2", []string{"go", "test", "./a"})
	store := NewWorkEventStore(dir)
	for i := range 2 {
		event := validWorkEvent(now.Add(time.Duration(i)*time.Second), WorkEventCompleted)
		event.OperationID = event.OperationID[:len(event.OperationID)-1] + string(rune('a'+i))
		event.EventID = workEventID(event.OperationID, event.Event, event.LeaseID)
		event.PlacementKey, event.PlacementFamilyKey, event.RuntimeMillis = siblingExact, keys.Family, 4_000
		if err := store.AppendDurable(event); err != nil {
			t.Fatal(err)
		}
	}
	var body bytes.Buffer
	for range 2 {
		line, _ := json.Marshal(map[string]any{"event": "remote_work_finished", "ts": now.Format(time.RFC3339Nano),
			"attrs": map[string]any{"duration_ms": 30_000, "placement_key": siblingExact, "placement_family_key": keys.Family}})
		body.Write(append(line, '\n'))
	}
	if err := os.WriteFile(filepath.Join(dir, "work-control-events.jsonl"), body.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	exact, family := readPlacementHistory(dir, keys, now)
	if len(exact.localMS)+len(exact.remoteMS) != 0 || len(family.localMS) != 2 || len(family.remoteMS) != 2 {
		t.Fatalf("exact %+v family %+v", exact, family)
	}
	history, scope := chooseHistory(exact, family)
	if scope != "family" || !decidePlacement(true, "", history).Local {
		t.Fatalf("family fallback not used: %s %+v", scope, decidePlacement(true, "", history))
	}
	// Enough exact samples win over the family.
	exact = placementHistory{localMS: []int64{90_000, 90_000}, remoteMS: []int64{30_000, 30_000}}
	if history, scope := chooseHistory(exact, family); scope != "key" || decidePlacement(true, "", history).Local {
		t.Fatalf("exact history ignored: %s", scope)
	}
}
