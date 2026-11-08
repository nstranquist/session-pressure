package telemetry

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFactoryWALHealthAfterWriterCloses(t *testing.T) {
	if _, err := os.Stat("/usr/bin/sqlite3"); err != nil {
		t.Skip("platform sqlite client unavailable")
	}
	root := t.TempDir()
	t.Setenv("SESSION_PRESSURE_TELEMETRY_DIR", root)
	dir := filepath.Dir(DefaultSourceTelemetryPathAt(CLIInvocationSourceID, time.Now()))
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	authority := fmt.Sprintf(`{"schema_version":3,"started_at":%q}`, time.Now().Add(-48*time.Hour).UTC().Format(time.RFC3339))
	if err := os.WriteFile(filepath.Join(dir, ".writer-health-v3.json"), []byte(authority), 0600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, ".writer-health-v4-s00.sqlite")
	columns := []string{"hour TEXT", "synthetic INTEGER", "attempted INTEGER", "written INTEGER", "failed INTEGER", "failure_contended INTEGER DEFAULT 0", "failure_permission INTEGER DEFAULT 0", "failure_privacy INTEGER DEFAULT 0", "failure_io INTEGER DEFAULT 0", "latency_max_us INTEGER DEFAULT 0"}
	for i := 0; i < 12; i++ {
		columns = append(columns, fmt.Sprintf("latency_%d INTEGER DEFAULT 0", i))
	}
	sql := "PRAGMA journal_mode=WAL; CREATE TABLE writer_hourly (" + strings.Join(columns, ",") + "); INSERT INTO writer_hourly(hour,synthetic,attempted,written,failed,latency_0,latency_max_us) VALUES ('" + time.Now().UTC().Truncate(time.Hour).Format(time.RFC3339) + "',0,1,1,0,1,500);"
	if raw, err := exec.Command("/usr/bin/sqlite3", path, sql).CombinedOutput(); err != nil {
		t.Fatalf("fixture: %v %s", err, raw)
	}
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	// sqlite closes its last connection and removes the WAL coordination files.
	if _, err := os.Stat(path + "-wal"); !os.IsNotExist(err) {
		t.Fatalf("fixture WAL still present: %v", err)
	}
	snapshot := loadFactorySnapshot(dir)
	if !snapshot.known || len(snapshot.buckets) != 1 || snapshot.buckets[0].Real.Written != 1 {
		t.Fatalf("closed-writer health: %+v", snapshot)
	}
}
