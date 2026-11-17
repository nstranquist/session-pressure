package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nstranquist/session-pressure/runnerfleet"
	"github.com/nstranquist/session-pressure/sessionpressure"
)

func TestRecordingProbeKeepsEveryObservation(t *testing.T) {
	rec := &recordingProbe{}
	// An invalid endpoint never reaches ssh, so the test makes no network call.
	o := rec.probe(context.Background(), runnerfleet.Host{Name: "bad host;", User: "builder"})
	if o.Error != "invalid endpoint" || len(rec.seen) != 1 || rec.seen[0].Error != o.Error {
		t.Fatalf("observation = %+v seen = %+v", o, rec.seen)
	}
}

func TestRecordProbesWritesToSessionPressureDataDir(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(sessionpressure.DataDirEnv, dir)
	recordProbes([]runnerfleet.Observation{{Host: runnerfleet.Host{Name: "runner-3"}, ObservedAt: time.Now().UTC(), Healthy: true, ProbeMS: 1600, ProbeAttempts: 1}})
	if _, err := os.Stat(filepath.Join(dir, "runner-capacity.sqlite")); err != nil {
		t.Fatalf("probe history store: %v", err)
	}
}
