package gaterunner

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The runner-live contract DDL (RUNNER-LIVENESS-DESIGN §2–§3): the poller
// owns the store; this package only reads it. The fixture builder below
// and the slice's make-fixture.sh encode the same schema.
const liveFixtureDDL = `
CREATE TABLE runner_hosts (
  host TEXT PRIMARY KEY,
  runner_version TEXT,
  load1 REAL,
  disk_free_gb REAL,
  cache_gb REAL,
  last_poll_at TEXT,
  poll_error TEXT
);
CREATE TABLE runner_jobs_current (
  host TEXT NOT NULL,
  job_id TEXT NOT NULL,
  delivery_id TEXT,
  repo TEXT,
  sha TEXT,
  candidate TEXT,
  class TEXT,
  phase TEXT,
  state TEXT,
  verdict TEXT,
  enqueued_at TEXT,
  started_at TEXT,
  finished_at TEXT,
  updated_at TEXT,
  elapsed_s INTEGER,
  queue_wait_s INTEGER,
  last_output_age_s INTEGER,
  output_bytes INTEGER,
  child_pid INTEGER,
  cpu_pct REAL,
  rss_mb REAL,
  exit_code INTEGER,
  detail TEXT,
  PRIMARY KEY (host, job_id)
);
CREATE TABLE runner_samples (
  host TEXT, job_id TEXT, ts TEXT, phase TEXT,
  elapsed_s INTEGER, last_output_age_s INTEGER,
  cpu_pct REAL, rss_mb REAL
);`

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
	recent := ts(liveNow.Add(-5 * time.Minute))
	killed := ts(liveNow.Add(-15 * time.Minute))
	return []string{
		`INSERT INTO runner_hosts VALUES ('gaterunner-1','6797d410',4.41,875.0,31.0,` + fresh1 + `,NULL);`,
		`INSERT INTO runner_hosts VALUES ('gaterunner-2','6797d410',1.20,640.0,12.0,` + fresh2 + `,NULL);`,
		`INSERT INTO runner_hosts VALUES ('gaterunner-3','6797d410',0.40,290.0,3.0,` + stale + `,'ssh timeout');`,
		// gaterunner-1: one healthy running job, two terminal receipts.
		`INSERT INTO runner_jobs_current (host,job_id,delivery_id,repo,class,phase,verdict,started_at,updated_at,elapsed_s,last_output_age_s,cpu_pct,rss_mb) VALUES ('gaterunner-1','job-h1','del-1','nicos-tools','pure','running','HEALTHY',` + fresh1 + `,` + fresh1 + `,95,12,63.0,812.0);`,
		`INSERT INTO runner_jobs_current (host,job_id,delivery_id,repo,class,phase,state,verdict,finished_at,updated_at,elapsed_s,exit_code) VALUES ('gaterunner-1','job-r1','del-2','nicos-tools','pure','terminal','passed','',` + recent + `,` + recent + `,210,0);`,
		`INSERT INTO runner_jobs_current (host,job_id,delivery_id,repo,class,phase,state,verdict,finished_at,updated_at,elapsed_s,exit_code) VALUES ('gaterunner-1','job-r2','del-3','nicos-tools','heavy','terminal','failed','',` + recent + `,` + recent + `,48,1);`,
		// gaterunner-2: one running job with raw samples only (verdict must
		// be evaluated here: pure, silent 342 s, cpu 0.4, elapsed 731 s is
		// STALLED by the design's rule table) and one stall-kill.
		`INSERT INTO runner_jobs_current (host,job_id,delivery_id,repo,class,phase,verdict,started_at,updated_at,elapsed_s,last_output_age_s,cpu_pct,rss_mb) VALUES ('gaterunner-2','job-s1','del-4','nicos-commit-delivery','pure','running','',` + fresh2 + `,` + fresh2 + `,731,342,0.4,128.0);`,
		`INSERT INTO runner_jobs_current (host,job_id,delivery_id,repo,class,phase,state,verdict,finished_at,updated_at,elapsed_s,last_output_age_s,cpu_pct,exit_code,detail) VALUES ('gaterunner-2','job-k1','del-5','nicos-tools','pure','terminal','stalled','',` + killed + `,` + killed + `,731,342,0.4,137,'stall_killed: silent=342 cpu=0.4 elapsed=731 class=pure thresholds_rev=1');`,
		// gaterunner-3: suspect + queued behind a stale feed.
		`INSERT INTO runner_jobs_current (host,job_id,delivery_id,repo,class,phase,verdict,started_at,updated_at,elapsed_s,last_output_age_s,cpu_pct) VALUES ('gaterunner-3','job-q1','del-6','nicos-tools','native','running','SUSPECT',` + stale + `,` + stale + `,200,150,2.0);`,
		`INSERT INTO runner_jobs_current (host,job_id,delivery_id,repo,class,phase,verdict,enqueued_at,updated_at,queue_wait_s) VALUES ('gaterunner-3','job-q2','del-7','nicos-tools','pure','queued','',` + stale + `,` + stale + `,420);`,
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
	if len(g1.LiveJobs) != 1 || g1.LiveJobs[0].Verdict != "HEALTHY" || len(g1.RecentRuns) != 2 || len(g1.Kills) != 0 {
		t.Fatalf("g1 split wrong: live=%d recent=%d kills=%d", len(g1.LiveJobs), len(g1.RecentRuns), len(g1.Kills))
	}
	if len(g2.LiveJobs) != 1 || g2.LiveJobs[0].Verdict != "STALLED" {
		t.Fatalf("g2 evaluated verdict wrong: %+v", g2.LiveJobs)
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
		`INSERT INTO runner_hosts VALUES ('gaterunner-2','6797d410',1.0,10.0,1.0,'`+now.Format(time.RFC3339)+`',NULL);`)
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
		`INSERT INTO runner_hosts VALUES ('gaterunner-1','6797d410',1.0,10.0,1.0,'`+sqliteForm+`',NULL);`)
	s := readLiveSection(context.Background(), path, liveNow)
	if s == nil || !s.Present || len(s.Hosts) != 1 || s.Hosts[0].FeedStale || s.Hosts[0].LastPollAt == nil {
		t.Fatalf("sqlite datetime form misread: %+v", s)
	}
}

func TestEvaluateVerdictRuleTable(t *testing.T) {
	str := func(s string) *string { return &s }
	num := func(f float64) *float64 { return &f }
	iv := func(n int64) *int64 { return &n }
	cases := []struct {
		name string
		job  LiveJob
		want string
	}{
		{"silent idle past bound", LiveJob{Class: str("pure"), ElapsedS: iv(731), LastOutputAgeS: iv(342), CPUPct: num(0.4)}, "STALLED"},
		{"busy compile is healthy", LiveJob{Class: str("pure"), ElapsedS: iv(100), LastOutputAgeS: iv(200), CPUPct: num(55)}, "HEALTHY"},
		{"cpu guard caps at suspect", LiveJob{Class: str("heavy"), ElapsedS: iv(900), LastOutputAgeS: iv(400), CPUPct: num(42)}, "SUSPECT"},
		{"elapsed past p99 is suspect", LiveJob{Class: str("native"), ElapsedS: iv(200), LastOutputAgeS: iv(10), CPUPct: num(50)}, "SUSPECT"},
		{"young silent job is healthy", LiveJob{Class: str("pure"), ElapsedS: iv(60), LastOutputAgeS: iv(30), CPUPct: num(0)}, "HEALTHY"},
		{"unknown class uses pure bounds", LiveJob{Class: str("mystery"), ElapsedS: iv(700), LastOutputAgeS: iv(301), CPUPct: num(1)}, "STALLED"},
	}
	for _, c := range cases {
		if got := evaluateVerdict(c.job); got != c.want {
			t.Errorf("%s: got %s want %s", c.name, got, c.want)
		}
	}
}
