package frontdoor

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func sample(t *testing.T) Registration {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "wrapper")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	return Registration{
		Name:      "wrapper",
		Invoke:    []string{bin, "session", "pressure"},
		Republish: []string{"go", "run", "./scripts/build-wrapper"},
	}
}

func TestEnvValueRoundTripsAndParseValidates(t *testing.T) {
	want := sample(t)
	value, err := want.EnvValue()
	if err != nil {
		t.Fatal(err)
	}
	got, err := Parse(value)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("Parse(EnvValue) = %+v, %v; want %+v", got, err, want)
	}

	for name, value := range map[string]string{
		"relative invoke": `{"name":"w","invoke":["wrapper"],"republish":["make"]}`,
		"empty invoke":    `{"name":"w","invoke":[],"republish":["make"]}`,
		"empty republish": `{"name":"w","invoke":["/bin/w"],"republish":[]}`,
		"blank name":      `{"name":" ","invoke":["/bin/w"],"republish":["make"]}`,
		"unknown field":   `{"name":"w","invoke":["/bin/w"],"republish":["make"],"shell":"rm -rf /"}`,
		"not json":        `wrapper`,
		"nul argument":    "{\"name\":\"w\",\"invoke\":[\"/bin/w\"],\"republish\":[\"make\\u0000x\"]}",
	} {
		if _, err := Parse(value); err == nil {
			t.Errorf("%s: Parse accepted %s", name, value)
		}
	}
}

func TestRecordWritesOnlyWhenTheRegistrationChanges(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	r := sample(t)
	r.Revision = strings.Repeat("a", 40)

	wrote, err := Record(dir, r)
	if err != nil || !wrote {
		t.Fatalf("first Record = %v, %v; want a write", wrote, err)
	}
	info, err := os.Stat(Path(dir))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("registration mode = %v, want 0600", info.Mode().Perm())
	}
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(Path(dir), old, old); err != nil {
		t.Fatal(err)
	}

	if wrote, err := Record(dir, r); err != nil || wrote {
		t.Fatalf("same Record = %v, %v; want no write", wrote, err)
	}
	if info, _ := os.Stat(Path(dir)); info.ModTime().After(old.Add(time.Second)) {
		t.Fatalf("an unchanged registration was rewritten (mtime %v)", info.ModTime())
	}

	r.Revision = strings.Repeat("b", 40)
	if wrote, err := Record(dir, r); err != nil || !wrote {
		t.Fatalf("changed Record = %v, %v; want a write", wrote, err)
	}
	got, found, err := Load(dir)
	if err != nil || !found || got.Revision != r.Revision {
		t.Fatalf("Load = %+v, %v, %v; want revision %s", got, found, err, r.Revision)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("state dir holds %d entries, want only %s (temp files leaked)", len(entries), FileName)
	}

	if _, err := Record(dir, Registration{Name: "w"}); err == nil {
		t.Fatal("Record accepted an invalid registration")
	}
}

func TestInspectReportsBinaryAndUnreadableRecords(t *testing.T) {
	dir := t.TempDir()
	if Inspect(dir) != nil {
		t.Fatal("Inspect reported a front door that was never recorded")
	}

	r := sample(t)
	if _, err := Record(dir, r); err != nil {
		t.Fatal(err)
	}
	status := Inspect(dir)
	if status == nil || !status.BinaryPresent || status.Name != "wrapper" || status.Path != Path(dir) {
		t.Fatalf("Inspect = %+v; want the recorded wrapper with its binary present", status)
	}

	if err := os.Remove(r.Invoke[0]); err != nil {
		t.Fatal(err)
	}
	status = Inspect(dir)
	if status == nil || status.BinaryPresent || !strings.Contains(status.Detail, "gone") {
		t.Fatalf("Inspect after removal = %+v; want binary_present false with a detail", status)
	}

	if err := os.WriteFile(Path(dir), []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	status = Inspect(dir)
	if status == nil || status.Name != "" || status.Detail == "" {
		t.Fatalf("Inspect of a corrupt record = %+v; want an unnamed status with a detail", status)
	}
}

func TestWithoutWrapperEnvDropsOnlyWrapperVariables(t *testing.T) {
	got := WithoutWrapperEnv([]string{
		"PATH=/bin",
		MarkerEnv + "=1",
		RevisionEnv + "=" + strings.Repeat("a", 40),
		Env + `={"name":"w"}`,
		"SESSION_PRESSURE_FRONT_DOOR_OTHER=kept",
		"LANG=C",
	})
	want := []string{"PATH=/bin", "SESSION_PRESSURE_FRONT_DOOR_OTHER=kept", "LANG=C"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("WithoutWrapperEnv = %#v, want %#v", got, want)
	}
}
