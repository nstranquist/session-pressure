package gaterunner

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The runner-live contract DDL (RUNNER-LIVENESS-DESIGN §2–§3): the poller
// owns the store; this package only reads it. The fixture builder below
// uses the producer schema, not a schema inferred from this reader.
// Recorded from the production poller store on 2026-11-09.
const liveFixtureDDL = `CREATE TABLE runner_watermarks (
  host TEXT NOT NULL, stream TEXT NOT NULL,
  byte_offset INTEGER NOT NULL DEFAULT 0,
  updated_at INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (host, stream));
CREATE TABLE runner_samples (
  host TEXT NOT NULL, job_id TEXT NOT NULL, ts TEXT NOT NULL,
  ts_unix INTEGER NOT NULL DEFAULT 0,
  delivery_id TEXT, repo TEXT, sha TEXT, candidate TEXT, class TEXT,
  phase TEXT, elapsed_s REAL, queue_wait_s REAL, last_output_age_s REAL,
  output_bytes INTEGER, child_pid INTEGER, cpu_pct REAL, rss_mb REAL,
  unit TEXT, runner_version TEXT, thresholds_rev INTEGER, exit INTEGER,
  detail TEXT,
  PRIMARY KEY (host, job_id, ts));
CREATE INDEX runner_samples_ts ON runner_samples(ts_unix);
CREATE TABLE runner_jobs_current (
  host TEXT NOT NULL, job_id TEXT NOT NULL,
  delivery_id TEXT, repo TEXT, sha TEXT, candidate TEXT, class TEXT,
  phase TEXT, state TEXT NOT NULL,
  enqueued_at TEXT, started_at TEXT,
  sample_ts TEXT NOT NULL, sample_ts_unix INTEGER NOT NULL DEFAULT 0,
  elapsed_s REAL, queue_wait_s REAL, last_output_age_s REAL,
  output_bytes INTEGER, child_pid INTEGER, cpu_pct REAL, rss_mb REAL,
  unit TEXT, runner_version TEXT, thresholds_rev INTEGER, exit INTEGER,
  detail TEXT,
  PRIMARY KEY (host, job_id));
CREATE TABLE runner_hosts (
  host TEXT PRIMARY KEY,
  runner_version TEXT, load1 REAL, disk_free_gb REAL, build_cache_gb REAL,
  vitals_ts_unix INTEGER NOT NULL DEFAULT 0,
  last_poll_ok_unix INTEGER NOT NULL DEFAULT 0,
  last_poll_attempt_unix INTEGER NOT NULL DEFAULT 0,
  poll_error TEXT);
CREATE TABLE runner_runs (
  host TEXT NOT NULL, job_id TEXT NOT NULL,
  ts TEXT, ts_unix INTEGER NOT NULL DEFAULT 0,
  caller TEXT, repo TEXT, sha TEXT, candidate TEXT, class TEXT, lane TEXT,
  exit INTEGER, dur_s REAL, lock_wait_s REAL, run_s REAL,
  reason TEXT, delivery_id TEXT, cmd TEXT,
  PRIMARY KEY (host, job_id));
CREATE INDEX runner_runs_ts ON runner_runs(ts_unix);
CREATE TABLE runner_kills (
  host TEXT NOT NULL, job_id TEXT NOT NULL,
  delivery_id TEXT, class TEXT,
  killed_ts TEXT, killed_ts_unix INTEGER NOT NULL DEFAULT 0,
  elapsed_s REAL, last_output_age_s REAL, cpu_pct REAL,
  detail TEXT, outcome TEXT,
  PRIMARY KEY (host, job_id));
CREATE INDEX runner_kills_ts ON runner_kills(killed_ts_unix);
`

func buildLiveStore(t *testing.T, path string, inserts ...string) {
	t.Helper()
	if _, err := os.Stat("/usr/bin/sqlite3"); err != nil {
		t.Skip("sqlite3 CLI unavailable")
	}
	sql := liveFixtureDDL + strings.Join(inserts, "\n")
	cmd := exec.Command("/usr/bin/sqlite3", "-bail", path, sql)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("fixture build failed: %v\n%s", err, out)
	}
}

var liveNow = time.Date(2026, 11, 9, 7, 0, 0, 0, time.UTC)

func ts(t time.Time) string { return "'" + t.UTC().Format(time.RFC3339) + "'" }

func fixtureInserts() []string {
	fresh1 := ts(liveNow.Add(-10 * time.Second))
	fresh2 := ts(liveNow.Add(-20 * time.Second))
	stale := ts(liveNow.Add(-12 * time.Minute))
	return []string{
		`INSERT INTO runner_hosts (host,runner_version,load1,disk_free_gb,build_cache_gb,last_poll_ok_unix) VALUES ('gaterunner-1','6797d410',4.41,875,31,strftime('%s',` + fresh1 + `));`,
		`INSERT INTO runner_hosts (host,last_poll_ok_unix) VALUES ('gaterunner-2',strftime('%s',` + fresh2 + `));`,
		`INSERT INTO runner_hosts (host,last_poll_ok_unix,poll_error) VALUES ('gaterunner-3',strftime('%s',` + stale + `),'ssh timeout');`,
		`INSERT INTO runner_jobs_current (host,job_id,repo,class,phase,state,sample_ts,elapsed_s,last_output_age_s,cpu_pct) VALUES ('gaterunner-1','job-h1','nicos-tools','pure','running','HEALTHY',` + fresh1 + `,95.75,12.5,63);`,
		`INSERT INTO runner_runs (host,job_id,ts,repo,class,exit,dur_s) VALUES ('gaterunner-1','job-r1',` + fresh1 + `,'nicos-tools','pure',0,210),('gaterunner-1','job-r2',` + fresh2 + `,'nicos-tools','heavy',1,48);`,
		`INSERT INTO runner_jobs_current (host,job_id,repo,class,phase,state,sample_ts,elapsed_s,last_output_age_s,cpu_pct) VALUES ('gaterunner-2','job-s1','nicos-tools','pure','running','STALLED',` + fresh2 + `,731,342,0.4);`,
		`INSERT INTO runner_kills (host,job_id,killed_ts,elapsed_s,last_output_age_s,cpu_pct,detail) VALUES ('gaterunner-2','job-k1',` + fresh1 + `,731,342,0.4,'stall_killed');`,
		`INSERT INTO runner_jobs_current (host,job_id,repo,phase,state,sample_ts) VALUES ('gaterunner-3','job-q1','nicos-tools','running','SUSPECT',` + stale + `),('gaterunner-3','job-q2','nicos-tools','queued','HEALTHY',` + stale + `);`,
		// Killed/terminal current rows must not reappear as running or duplicate history.
		`INSERT INTO runner_jobs_current (host,job_id,phase,state,sample_ts) VALUES ('gaterunner-2','job-k1','running','KILLED',` + fresh1 + `),('gaterunner-1','job-r1','terminal','TERMINAL',` + fresh1 + `);`,
	}
}

func TestLiveSectionFromFixtureStore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runner-live.sqlite")
	buildLiveStore(t, path, fixtureInserts()...)
	s := readLiveSection(context.Background(), path, liveNow)
	if s == nil || !s.Present || s.Error != "" {
		t.Fatalf("section=%+v", s)
	}
	if len(s.Hosts) != 3 || s.Hosts[0].Host != "gaterunner-1" || s.Hosts[2].Host != "gaterunner-3" {
		t.Fatalf("hosts=%+v", s.Hosts)
	}
	g1, g2, g3 := s.Hosts[0], s.Hosts[1], s.Hosts[2]
	if g1.FeedStale || g2.FeedStale || !g3.FeedStale {
		t.Fatalf("stale flags g1=%v g2=%v g3=%v", g1.FeedStale, g2.FeedStale, g3.FeedStale)
	}
	if g3.PollError == nil || *g3.PollError != "ssh timeout" {
		t.Fatalf("poll error lost: %+v", g3.PollError)
	}
	if len(g1.LiveJobs) != 1 || g1.LiveJobs[0].ElapsedS == nil || *g1.LiveJobs[0].ElapsedS != 95 || g1.LiveJobs[0].Verdict != "HEALTHY" || len(g1.RecentRuns) != 2 || len(g1.Kills) != 0 {
		t.Fatalf("g1 split wrong: live=%d recent=%d kills=%d", len(g1.LiveJobs), len(g1.RecentRuns), len(g1.Kills))
	}
	if len(g2.LiveJobs) != 1 || g2.LiveJobs[0].Verdict != "STALLED" {
		t.Fatalf("g2 stored verdict wrong: %+v", g2.LiveJobs)
	}
	if len(g2.Kills) != 1 || len(g2.RecentRuns) != 0 {
		t.Fatalf("g2 kill split wrong: kills=%d recent=%d", len(g2.Kills), len(g2.RecentRuns))
	}
	k := g2.Kills[0]
	if k.JobID != "job-k1" || k.ElapsedS == nil || *k.ElapsedS != 731 || k.Detail == nil || !strings.Contains(*k.Detail, "stall_killed") {
		t.Fatalf("kill event fields lost: %+v", k)
	}
	if len(g3.LiveJobs) != 2 || g3.LiveJobs[0].Verdict != "SUSPECT" || g3.LiveJobs[1].Phase != "queued" || g3.LiveJobs[1].Verdict != "" {
		t.Fatalf("g3 live rows wrong: %+v", g3.LiveJobs)
	}
}

func TestLiveSectionAbsentAndInvalid(t *testing.T) {
	dir := t.TempDir()
	if s := readLiveSection(context.Background(), filepath.Join(dir, "nope.sqlite"), liveNow); s != nil {
		t.Fatalf("absent store must omit the section: %+v", s)
	}
	bad := filepath.Join(dir, "bad.sqlite")
	if err := os.WriteFile(bad, []byte("not a database"), 0600); err != nil {
		t.Fatal(err)
	}
	s := readLiveSection(context.Background(), bad, liveNow)
	if s == nil || s.Present || s.Error != "runner_live_read_failed" {
		t.Fatalf("invalid store must degrade with a token: %+v", s)
	}
	link := filepath.Join(dir, "link.sqlite")
	if err := os.Symlink(bad, link); err != nil {
		t.Fatal(err)
	}
	s = readLiveSection(context.Background(), link, liveNow)
	if s == nil || s.Present || s.Error != "runner_live_store_invalid" {
		t.Fatalf("symlink store must be rejected: %+v", s)
	}
}

func TestInspectOmitsLiveWhenStoreAbsent(t *testing.T) {
	home := t.TempDir()
	s := Inspect(context.Background(), home, false)
	if s.Live != nil {
		t.Fatalf("live section present without a store: %+v", s.Live)
	}
	dir := filepath.Join(home, ".nicos-dev", "commit-delivery", "v1")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	buildLiveStore(t, filepath.Join(dir, "runner-live.sqlite"),
		`INSERT INTO runner_hosts (host,last_poll_ok_unix) VALUES ('gaterunner-2',strftime('%s','`+now.Format(time.RFC3339)+`'));`)
	s = Inspect(context.Background(), home, false)
	if s.Live == nil || !s.Live.Present || len(s.Live.Hosts) != 1 || s.LocalError != "" {
		t.Fatalf("live section not surfaced: %+v local_error=%q", s.Live, s.LocalError)
	}
}

func TestLiveStoreTimeFormats(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runner-live.sqlite")
	// SQLite datetime() text form (space-separated, UTC) must parse too:
	// a fresh poll in that shape is not feed-stale.
	sqliteForm := liveNow.Add(-10 * time.Second).Format("2006-01-02 15:04:05")
	buildLiveStore(t, path,
		`INSERT INTO runner_hosts (host,last_poll_ok_unix) VALUES ('gaterunner-1',strftime('%s','`+sqliteForm+`'));`)
	s := readLiveSection(context.Background(), path, liveNow)
	if s == nil || !s.Present || len(s.Hosts) != 1 || s.Hosts[0].FeedStale || s.Hosts[0].LastPollAt == nil {
		t.Fatalf("sqlite datetime form misread: %+v", s)
	}
}

func TestLiveLimitsArePerHostAndVerdictsAreAuthoritative(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runner-live.sqlite")
	inserts := fixtureInserts()
	for i := 0; i < 80; i++ {
		inserts = append(inserts, fmt.Sprintf(`INSERT INTO runner_jobs_current (host,job_id,phase,state,sample_ts,sample_ts_unix,elapsed_s,last_output_age_s,cpu_pct) VALUES ('gaterunner-1','busy-%d','running','HEALTHY',%s,%d,9999,9999,0);`, i, ts(liveNow), liveNow.Unix()+int64(i)))
		inserts = append(inserts, fmt.Sprintf(`INSERT INTO runner_runs (host,job_id,ts,ts_unix,exit) VALUES ('gaterunner-1','run-%d',%s,%d,0);`, i, ts(liveNow), liveNow.Unix()+int64(i)))
	}
	inserts = append(inserts, `UPDATE runner_hosts SET poll_error='' WHERE host='gaterunner-1';`)
	buildLiveStore(t, path, inserts...)
	got := readLiveSection(context.Background(), path, liveNow)
	if got == nil || got.Error != "" {
		t.Fatalf("section=%+v", got)
	}
	if len(got.Hosts[0].LiveJobs) != 50 || len(got.Hosts[0].RecentRuns) != 20 || len(got.Hosts[1].LiveJobs) != 1 || len(got.Hosts[2].LiveJobs) != 2 {
		t.Fatalf("per-host limits lost: %+v", got.Hosts)
	}
	if got.Hosts[0].PollError != nil {
		t.Fatalf("empty poll error is not a failure")
	}
	for _, job := range got.Hosts[0].LiveJobs {
		if job.Verdict != "HEALTHY" {
			t.Fatalf("must trust producer verdict even with old output: %+v", job)
		}
	}
}
