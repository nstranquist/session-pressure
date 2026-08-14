package sessionpressure

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestPromoteControlArtifactSurvivesSourceRemoval(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "source", "ndev")
	if err := os.MkdirAll(filepath.Dir(source), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, []byte("control-revision"), 0o755); err != nil {
		t.Fatal(err)
	}
	artifact, err := promoteControlArtifact(source, filepath.Join(dir, "data"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(source); err != nil {
		t.Fatal(err)
	}
	if err := VerifyControlBinary(artifact.Path, artifact.SHA256); err != nil {
		t.Fatalf("promoted controller became invalid with its source gone: %v", err)
	}
	if !isPromotedControlArtifact(artifact.Path, filepath.Join(dir, "data"), artifact.SHA256) {
		t.Fatal("promoted controller was not recognized as managed")
	}
	if isPromotedControlArtifact(filepath.Join(dir, "source", "ndev"), filepath.Join(dir, "data"), artifact.SHA256) {
		t.Fatal("legacy source path was accepted as a managed controller")
	}
}

func TestPruneControlArtifactsRetainsActivePlusRollbackWindow(t *testing.T) {
	dir := t.TempDir()
	dataDir := filepath.Join(dir, "data")
	var artifacts []controlArtifact
	for index := 0; index < 4; index++ {
		source := filepath.Join(dir, fmt.Sprintf("ndev-%d", index))
		if err := os.WriteFile(source, []byte(fmt.Sprintf("control-revision-%d", index)), 0o755); err != nil {
			t.Fatal(err)
		}
		artifact, err := promoteControlArtifact(source, dataDir)
		if err != nil {
			t.Fatal(err)
		}
		artifacts = append(artifacts, artifact)
	}
	active := artifacts[len(artifacts)-1]
	if err := pruneControlArtifacts(dataDir, active.SHA256, 2); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(dataDir, "control-artifacts"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 {
		t.Fatalf("retained control artifacts=%d, want active plus two rollbacks", len(entries))
	}
	if _, err := os.Stat(active.Path); err != nil {
		t.Fatalf("active control artifact was pruned: %v", err)
	}
}
