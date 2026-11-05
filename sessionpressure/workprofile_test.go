package sessionpressure

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestGoProfileBoundedFragmentedStream(t *testing.T) {
	p := newGoTestProfile("test")
	input := []byte("{\"Action\":\"output\",\"Output\":\"secret output\"}\n{\"Action\":\"fail\",\"Package\":\"example/a\",\"Test\":\"TestFailure\",\"Elapsed\":1.25}\n")
	for _, b := range input {
		if n, err := p.Write([]byte{b}); n != 1 || err != nil {
			t.Fatal(n, err)
		}
	}
	p.Write(bytes.Repeat([]byte{'x'}, goProfileMaximumLine+1))
	p.Write([]byte("\ninvalid json\n{\"Action\":\"pass\",\"Package\":\"example/a\",\"Elapsed\":2}"))
	root := t.TempDir()
	if err := p.save(root); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(root, "work-profiles/test.json"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(body, []byte("secret")) || len(p.Records) != 2 || p.Dropped != 1 || p.Records[0].Elapsed != 2 {
		t.Fatalf("%s", body)
	}
	var decoded goTestProfile
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < goProfileMaximumRecords+2; i++ {
		p.Write([]byte("{\"Action\":\"skip\",\"Package\":\"a\"}\n"))
	}
	if len(p.Records) != goProfileMaximumRecords || len(p.line) != 0 {
		t.Fatal("unbounded profile")
	}
}
func TestGoProfileRetentionAndOptIn(t *testing.T) {
	root := t.TempDir()
	p := filepath.Join(root, "old.json")
	os.WriteFile(p, []byte("{}"), 0600)
	old := time.Now().Add(-8 * 24 * time.Hour)
	os.Chtimes(p, old, old)
	if err := pruneGoTestProfiles(root, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Fatal("expired profile retained")
	}
	if _, err := ParseWorkRunArgs([]string{"--class", "test", "--profile-go-test", "--", "go", "test", "./..."}); err == nil {
		t.Fatal("profile accepted without JSON")
	}
	if _, err := ParseWorkRunArgs([]string{"--class", "test", "--profile-go-test", "--", "go", "test", "-json", "./..."}); err != nil {
		t.Fatal(err)
	}
}
