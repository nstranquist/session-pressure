package sessionpressure

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nstranquist/session-pressure/frontdoor"
)

func TestRecordFrontDoorFromEnvStoresTheWrapperAndItsRevision(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(DataDirEnv, dir)
	bin := filepath.Join(t.TempDir(), "wrapper")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	value, err := frontdoor.Registration{
		Name: "wrapper", Invoke: []string{bin, "session", "pressure"}, Republish: []string{"make", "wrapper"},
	}.EnvValue()
	if err != nil {
		t.Fatal(err)
	}
	revision := strings.Repeat("c", 40)
	env := map[string]string{frontdoor.Env: value, FrontDoorRevisionEnv: revision}
	RecordFrontDoorFromEnv(func(name string) string { return env[name] })

	got, found, err := frontdoor.Load(dir)
	if err != nil || !found {
		t.Fatalf("no registration recorded: found=%v err=%v", found, err)
	}
	if got.Name != "wrapper" || got.Revision != revision || got.Invoke[0] != bin {
		t.Fatalf("recorded %+v", got)
	}

	// The doctor reports it.
	status := frontdoor.Inspect(dir)
	doc := BuildPressureDoctor(PressureDoctorInput{Dir: dir, FrontDoor: status})
	if doc.FrontDoor == nil || doc.FrontDoor.Name != "wrapper" || !doc.FrontDoor.BinaryPresent {
		t.Fatalf("doctor front_door = %+v", doc.FrontDoor)
	}
}

func TestRecordFrontDoorFromEnvIgnoresMissingAndBadValues(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(DataDirEnv, dir)
	for _, value := range []string{"", "not json", `{"name":"w","invoke":["relative"],"republish":["make"]}`} {
		RecordFrontDoorFromEnv(func(name string) string {
			if name == frontdoor.Env {
				return value
			}
			return ""
		})
	}
	if _, err := os.Stat(frontdoor.Path(dir)); !os.IsNotExist(err) {
		t.Fatalf("a missing or bad %s value wrote a registration (stat err %v)", frontdoor.Env, err)
	}
}
