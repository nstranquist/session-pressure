package gaterunner

import (
	"database/sql"
	"testing"
	"time"

	"github.com/nstranquist/session-pressure/runnerfleet"
)

func TestRecordProbesKeepsTimingAndOutcome(t *testing.T) {
	dir := t.TempDir()
	at := time.Now().UTC().Truncate(time.Millisecond)
	host := runnerfleet.Host{Name: "runner-3"}
	obs := []runnerfleet.Observation{
		{Host: host, ObservedAt: at, Healthy: true, ProbeMS: 1600, ProbeAttempts: 1},
		{Host: host, ObservedAt: at.Add(time.Second), Error: "SSH health probe: no answer within 22.0s: signal: killed (1 attempt(s), 22.0s)", ProbeMS: 22000, ProbeAttempts: 1},
		{Host: host, ObservedAt: at.Add(2 * time.Second), Error: "SSH health probe: exit status 255 (2 attempt(s), 3.1s)", ProbeMS: 3100, ProbeAttempts: 2},
		{Host: host, ObservedAt: at.Add(3 * time.Second), Error: "scheduler capacity differs from inventory", ProbeMS: 8100, ProbeAttempts: 1},
		// No ssh ran: invalid endpoint or a cancelled fan-out.
		{Host: host, ObservedAt: at.Add(4 * time.Second), Error: "invalid endpoint"},
		{Host: host, Error: "context deadline exceeded"},
	}
	if err := RecordProbes(dir, obs); err != nil {
		t.Fatal(err)
	}
	s, err := openCapacityStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	got, err := s.probeSamples("runner-3", at.Add(-time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	want := []ProbeSample{
		{Host: "runner-3", At: at, Outcome: "healthy", Attempts: 1, ProbeMS: 1600},
		{Host: "runner-3", At: at.Add(time.Second), Outcome: "timeout", Attempts: 1, ProbeMS: 22000},
		{Host: "runner-3", At: at.Add(2 * time.Second), Outcome: "ssh_failed", Attempts: 2, ProbeMS: 3100},
		{Host: "runner-3", At: at.Add(3 * time.Second), Outcome: "unhealthy", Attempts: 1, ProbeMS: 8100},
	}
	if len(got) != len(want) {
		t.Fatalf("samples = %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("sample %d = %+v, want %+v", i, got[i], want[i])
		}
	}
	// Nothing to record opens no store.
	empty := t.TempDir()
	if err := RecordProbes(empty, obs[4:]); err != nil {
		t.Fatal(err)
	}
}

func TestProbeHistoryPrunedWithNetSamples(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC()
	// A day past the retention: due for the archive (rows move about once a day).
	old := now.Add(-capacityStoreRetention - capacityOffloadSlack - time.Hour)
	host := runnerfleet.Host{Name: "runner-3"}
	if err := RecordProbes(dir, []runnerfleet.Observation{{Host: host, ObservedAt: old, Healthy: true, ProbeMS: 1, ProbeAttempts: 1}}); err != nil {
		t.Fatal(err)
	}
	// The next write moves rows past the retention to the verified archive.
	if err := RecordProbes(dir, []runnerfleet.Observation{{Host: host, ObservedAt: now, Healthy: true, ProbeMS: 2, ProbeAttempts: 1}}); err != nil {
		t.Fatal(err)
	}
	s, err := openCapacityStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	got, err := s.probeSamples("runner-3", old.Add(-time.Hour))
	if err != nil || len(got) != 1 || got[0].ProbeMS != 2 {
		t.Fatalf("after prune = %+v err=%v", got, err)
	}
	// History rule: the old probe is in the archive, with a receipt.
	var rows int64
	var archive, check string
	if err := s.db.QueryRow(`SELECT rows, archive, integrity FROM retention_offload_receipts WHERE tbl = 'probe_samples'`).Scan(&rows, &archive, &check); err != nil {
		t.Fatal(err)
	}
	if rows != 1 || check != "ok" {
		t.Fatalf("probe receipt rows=%d check=%s", rows, check)
	}
	db, err := sql.Open("sqlite", "file:"+archive+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var probeMS int64
	if err := db.QueryRow(`SELECT probe_ms FROM probe_samples WHERE host = 'runner-3' AND at_ms = ?`, old.UnixMilli()).Scan(&probeMS); err != nil || probeMS != 1 {
		t.Fatalf("archived probe_ms=%d err=%v", probeMS, err)
	}
}
