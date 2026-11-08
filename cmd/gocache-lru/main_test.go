package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
)

func TestParseSizeStrict(t *testing.T) {
	for _, raw := range []string{"0", "-1", "1GiB trailing", "1.5GiB", "9223372036854775807GiB", "1foo", ""} {
		if _, err := parseSize(raw); err == nil {
			t.Errorf("accepted %q", raw)
		}
	}
	for raw, want := range map[string]int64{"1GiB": 1 << 30, "2MiB": 2 << 20, "1GB": 1000000000, "42": 42} {
		if got, err := parseSize(raw); err != nil || got != want {
			t.Errorf("%q = %d, %v", raw, got, err)
		}
	}
}
func TestActualGoCacheProtocol(t *testing.T) {
	if testing.Short() {
		t.Skip("nested Go build integration")
	}
	temp := t.TempDir()
	bin := filepath.Join(temp, "cache-helper")
	cmd := exec.Command("go", "build", "-o", bin, ".")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build helper: %v %s", err, out)
	}
	sample := filepath.Join(temp, "sample")
	if err := os.Mkdir(sample, 0700); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{"go.mod": "module cache-integration\n\ngo 1.26\n", "sample.go": "package sample\nfunc Value() int { return 42 }\n", "sample_test.go": "package sample\nimport \"testing\"\nfunc TestValue(t *testing.T) {if Value()!=42 {t.Fatal(Value())}}\n"} {
		if err := os.WriteFile(filepath.Join(sample, name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	cache := filepath.Join(temp, "objects")
	for i := 0; i < 2; i++ {
		cmd := exec.Command("go", "test", "./...")
		cmd.Dir = sample
		cmd.Env = append(os.Environ(), "GOWORK=off", "CGO_ENABLED=0", "GOCACHE="+filepath.Join(temp, "standard"), "GOCACHEPROG="+strconv.Quote(bin)+" -dir "+strconv.Quote(cache)+" -max 32MiB")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("Go protocol run %d: %v %s", i, err, out)
		}
	}
	rows, err := os.ReadDir(filepath.Join(cache, "objects"))
	if err != nil || len(rows) == 0 {
		t.Fatalf("Go did not populate external cache: %v", err)
	}
	clients, err := os.ReadDir(filepath.Join(cache, "clients"))
	if err != nil || len(clients) != 0 {
		t.Fatalf("Go close did not release pins: %v %d clients", err, len(clients))
	}
}
