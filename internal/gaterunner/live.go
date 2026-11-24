package gaterunner

import (
	"context"
	"encoding/json"
	"math"
	"os"
	"sort"
	"time"
)

// Runner-live store (RUNNER-LIVENESS-DESIGN §3): the Mac-side poller keeps
// one SQLite store beside the lane state — runner_hosts (vitals, poll
// health), runner_jobs_current (latest heartbeat per job plus the poller's
// evaluated liveness verdict), runner_samples (raw history, not read
// here). This reader is query-only and bounded like every other read in
// the package, and it never invents data: an absent store omits the live
// section entirely so clients can show availability explicitly, and a failed read
// surfaces a stable error token instead of an empty fleet.

const liveStoreName = "runner-live.sqlite"
const liveFeedStaleAfter = 60 * time.Second
const maxLiveRowsPerHost = 50
const maxRecentRowsPerHost = 20
const maxKillRowsPerHost = 20

type LiveJob struct {
	JobID          string     `json:"job_id"`
	DeliveryID     *string    `json:"delivery_id,omitempty"`
	Repo           string     `json:"repo"`
	SHA            *string    `json:"sha,omitempty"`
	Class          *string    `json:"class,omitempty"`
	Phase          string     `json:"phase"`
	State          *string    `json:"state,omitempty"`
	Verdict        string     `json:"verdict"`
	EnqueuedAt     *time.Time `json:"enqueued_at,omitempty"`
	StartedAt      *time.Time `json:"started_at,omitempty"`
	FinishedAt     *time.Time `json:"finished_at,omitempty"`
	UpdatedAt      *time.Time `json:"updated_at,omitempty"`
	ElapsedS       *int64     `json:"elapsed_s,omitempty"`
	QueueWaitS     *int64     `json:"queue_wait_s,omitempty"`
	LastOutputAgeS *int64     `json:"last_output_age_s,omitempty"`
	CPUPct         *float64   `json:"cpu_pct,omitempty"`
	RSSMB          *float64   `json:"rss_mb,omitempty"`
	ExitCode       *int64     `json:"exit_code,omitempty"`
	Detail         *string    `json:"detail,omitempty"`
}

type KillEvent struct {
	JobID          string     `json:"job_id"`
	DeliveryID     *string    `json:"delivery_id,omitempty"`
	Class          *string    `json:"class,omitempty"`
	At             *time.Time `json:"at,omitempty"`
	ElapsedS       *int64     `json:"elapsed_s,omitempty"`
	LastOutputAgeS *int64     `json:"last_output_age_s,omitempty"`
	CPUPct         *float64   `json:"cpu_pct,omitempty"`
	Detail         *string    `json:"detail,omitempty"`
}

type LiveHost struct {
	Host          string      `json:"host"`
	RunnerVersion *string     `json:"runner_version,omitempty"`
	Load1         *float64    `json:"load1,omitempty"`
	DiskFreeGB    *float64    `json:"disk_free_gb,omitempty"`
	CacheGB       *float64    `json:"cache_gb,omitempty"`
	LastPollAt    *time.Time  `json:"last_poll_at,omitempty"`
	PollError     *string     `json:"poll_error,omitempty"`
	FeedStale     bool        `json:"feed_stale"`
	LiveJobs      []LiveJob   `json:"live_jobs"`
	RecentRuns    []LiveJob   `json:"recent_runs"`
	Kills         []KillEvent `json:"kills"`
}

type LiveSection struct {
	Present bool       `json:"present"`
	Error   string     `json:"error,omitempty"`
	Hosts   []LiveHost `json:"hosts"`
}

// readLiveSection reads the runner-live store at path. It returns nil when
// the store does not exist yet (the poller has not been installed) so the
// inspection simply omits the section. now is the inspection time, used
// only to judge feed staleness.
func readLiveSection(ctx context.Context, path string, now time.Time) *LiveSection {
	st, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil || !st.Mode().IsRegular() {
		return &LiveSection{Present: false, Error: "runner_live_store_invalid", Hosts: []LiveHost{}}
	}
	section := &LiveSection{Present: true, Hosts: []LiveHost{}}
	// Read one bounded snapshot with the poller's actual schema. Its state is
	// authoritative; the desktop must not reevaluate the liveness policy.
	rows, err := queryRunnerLive(ctx, path, liveSnapshotSQL)
	if err != nil {
		return &LiveSection{Present: false, Error: "runner_live_read_failed", Hosts: []LiveHost{}}
	}
	var hostRows, jobRows, runRows, killRows []map[string]any
	for _, row := range rows {
		raw, _ := liveStr(row, "payload")
		var value map[string]any
		if err := json.Unmarshal([]byte(raw), &value); err != nil {
			return &LiveSection{Present: false, Error: "runner_live_read_failed", Hosts: []LiveHost{}}
		}
		switch row["kind"] {
		case "host":
			hostRows = append(hostRows, value)
		case "job":
			jobRows = append(jobRows, value)
		case "run":
			runRows = append(runRows, value)
		case "kill":
			killRows = append(killRows, value)
		}
	}
	byHost := map[string]int{}
	for _, row := range hostRows {
		host, ok := liveStr(row, "host")
		if !ok || host == "" {
			continue
		}
		h := LiveHost{Host: host, LiveJobs: []LiveJob{}, RecentRuns: []LiveJob{}, Kills: []KillEvent{}}
		h.RunnerVersion = liveStrPtr(row, "runner_version")
		h.Load1 = liveNumPtr(row, "load1")
		h.DiskFreeGB = liveNumPtr(row, "disk_free_gb")
		h.CacheGB = liveNumPtr(row, "cache_gb")
		h.PollError = liveStrPtr(row, "poll_error")
		if raw, ok := liveStr(row, "last_poll_at"); ok {
			h.LastPollAt = parseStoreTime(raw)
		}
		// The watcher is watched (design §3): a host whose poller has gone
		// quiet renders feed-stale, never as an idle fleet.
		h.FeedStale = h.LastPollAt == nil || now.Sub(*h.LastPollAt) > liveFeedStaleAfter
		byHost[host] = len(section.Hosts)
		section.Hosts = append(section.Hosts, h)
	}
	for _, row := range jobRows {
		host, ok := liveStr(row, "host")
		if !ok {
			continue
		}
		idx, ok := byHost[host]
		if !ok {
			continue
		}
		h := &section.Hosts[idx]
		job := liveJobFromRow(row)
		if job.Phase != "terminal" && len(h.LiveJobs) < maxLiveRowsPerHost {
			h.LiveJobs = append(h.LiveJobs, job)
		}
	}
	for _, row := range runRows {
		host, _ := liveStr(row, "host")
		if idx, ok := byHost[host]; ok {
			section.Hosts[idx].RecentRuns = append(section.Hosts[idx].RecentRuns, liveJobFromRow(row))
		}
	}
	for _, row := range killRows {
		host, _ := liveStr(row, "host")
		if idx, ok := byHost[host]; ok {
			job := liveJobFromRow(row)
			section.Hosts[idx].Kills = append(section.Hosts[idx].Kills, KillEvent{
				JobID: job.JobID, DeliveryID: job.DeliveryID, Class: job.Class,
				At: job.FinishedAt, ElapsedS: job.ElapsedS,
				LastOutputAgeS: job.LastOutputAgeS, CPUPct: job.CPUPct, Detail: job.Detail,
			})
		}
	}

	for i := range section.Hosts {
		jobs := section.Hosts[i].LiveJobs
		sort.SliceStable(jobs, func(a, b int) bool {
			ra, rb := verdictRank(jobs[a].Verdict), verdictRank(jobs[b].Verdict)
			if ra != rb {
				return ra < rb
			}
			return int64Or(jobs[a].ElapsedS, 0) > int64Or(jobs[b].ElapsedS, 0)
		})
		section.Hosts[i].LiveJobs = jobs
	}
	sort.Slice(section.Hosts, func(i, j int) bool { return section.Hosts[i].Host < section.Hosts[j].Host })
	return section
}

// Store timestamps are RFC3339 by contract; the reader also tolerates
// SQLite's datetime() text form ("2006-01-02 15:04:05", UTC) so a poller
// that writes either shape still renders correct ages.
func parseStoreTime(s string) *time.Time {
	if t := parseDate(s); t != nil {
		return t
	}
	if t, err := time.Parse("2006-01-02 15:04:05", s); err == nil {
		return &t
	}
	return nil
}

func firstTime(values ...*time.Time) *time.Time {
	for _, v := range values {
		if v != nil {
			return v
		}
	}
	return nil
}

func int64Or(v *int64, fallback int64) int64 {
	if v == nil {
		return fallback
	}
	return *v
}

func verdictRank(verdict string) int {
	switch verdict {
	case "STALLED":
		return 0
	case "SUSPECT":
		return 1
	case "HEALTHY":
		return 2
	}
	return 3
}

func liveJobFromRow(row map[string]any) LiveJob {
	job := LiveJob{}
	job.JobID, _ = liveStr(row, "job_id")
	job.DeliveryID = liveStrPtr(row, "delivery_id")
	if v, ok := liveStr(row, "repo"); ok {
		job.Repo = v
	}
	job.SHA = liveStrPtr(row, "sha")
	job.Class = liveStrPtr(row, "class")
	job.Phase, _ = liveStr(row, "phase")
	job.State = liveStrPtr(row, "state")
	job.Verdict, _ = liveStr(row, "verdict")
	if raw, ok := liveStr(row, "enqueued_at"); ok {
		job.EnqueuedAt = parseStoreTime(raw)
	}
	if raw, ok := liveStr(row, "started_at"); ok {
		job.StartedAt = parseStoreTime(raw)
	}
	if raw, ok := liveStr(row, "finished_at"); ok {
		job.FinishedAt = parseStoreTime(raw)
	}
	if raw, ok := liveStr(row, "updated_at"); ok {
		job.UpdatedAt = parseStoreTime(raw)
	}
	job.ElapsedS = liveIntPtr(row, "elapsed_s")
	job.QueueWaitS = liveIntPtr(row, "queue_wait_s")
	job.LastOutputAgeS = liveIntPtr(row, "last_output_age_s")
	job.CPUPct = liveNumPtr(row, "cpu_pct")
	job.RSSMB = liveNumPtr(row, "rss_mb")
	job.ExitCode = liveIntPtr(row, "exit_code")
	job.Detail = liveStrPtr(row, "detail")
	return job
}

// queryRunnerLive reads one bounded snapshot from the runner-live store.
func queryRunnerLive(parent context.Context, path string, query string) ([]map[string]any, error) {
	return querySQLite(parent, path, query, 2<<20, 6000)
}

func liveStr(row map[string]any, key string) (string, bool) {
	v, ok := row[key].(string)
	return v, ok
}

func liveStrPtr(row map[string]any, key string) *string {
	v, ok := row[key].(string)
	if !ok || v == "" {
		return nil
	}
	return &v
}

func liveNumPtr(row map[string]any, key string) *float64 {
	v, ok := row[key].(float64)
	if !ok || v < 0 || math.IsNaN(v) || math.IsInf(v, 0) {
		return nil
	}
	return &v
}

func liveIntPtr(row map[string]any, key string) *int64 {
	v, ok := row[key].(float64)
	if !ok || v < 0 || v > float64(math.MaxInt64/2) || math.IsNaN(v) || math.IsInf(v, 0) {
		return nil
	}
	n := int64(v)
	return &n
}
