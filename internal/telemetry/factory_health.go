package telemetry

// The factory owns its SQLite counters. Read them through the platform's
// query-only sqlite client so the small resident does not load a database engine.
import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type factoryCounters struct {
	Attempted      int64            `json:"attempted"`
	Written        int64            `json:"written"`
	Failed         int64            `json:"failed"`
	FailureClasses map[string]int64 `json:"failure_classes"`
	Latency        []int64          `json:"latency_histogram_us"`
	Max            int64            `json:"latency_max_us"`
}
type factoryHour struct {
	Hour string          `json:"hour"`
	Real factoryCounters `json:"real"`
}
type factorySnapshot struct {
	directory string
	at        time.Time
	started   time.Time
	known     bool
	reason    string
	buckets   []factoryHour
}

var factoryCache struct {
	sync.Mutex
	snapshot factorySnapshot
}
var factoryLatencyUpper = []int64{1000, 2500, 5000, 10000, 25000, 50000, 100000, 250000, 500000, 1000000, 5000000, math.MaxInt64}

func privateRegular(path string) bool {
	info, err := os.Lstat(path)
	return err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0077 == 0
}
func boundedPrivateJSON(path string, value any) error {
	if !privateRegular(path) {
		return fmt.Errorf("not a private regular authority")
	}
	info, err := os.Stat(path)
	if err != nil || info.Size() > 4<<20 {
		return fmt.Errorf("oversized authority")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, value)
}
func validFactoryHour(row factoryHour) bool {
	at, err := time.Parse(time.RFC3339, row.Hour)
	c := row.Real
	if err != nil || !at.Equal(at.UTC().Truncate(time.Hour)) || at.After(time.Now().Add(5*time.Minute)) || c.Attempted < 0 || c.Written < 0 || c.Failed < 0 || c.Written > c.Attempted-c.Failed || c.Max < 0 {
		return false
	}
	if len(c.Latency) != 0 && len(c.Latency) != 12 {
		return false
	}
	var samples int64
	for _, n := range c.Latency {
		if n < 0 {
			return false
		}
		samples += n
	}
	return samples <= c.Written+c.Failed
}

func loadFactorySnapshot(dir string) factorySnapshot {
	s := factorySnapshot{directory: dir, at: time.Now(), reason: "authority_invalid"}
	var authority struct {
		Schema  int    `json:"schema_version"`
		Started string `json:"started_at"`
	}
	if boundedPrivateJSON(filepath.Join(dir, ".writer-health-v3.json"), &authority) != nil || authority.Schema != 3 {
		return s
	}
	started, err := time.Parse(time.RFC3339Nano, authority.Started)
	if err != nil || started.After(s.at.Add(5*time.Minute)) {
		return s
	}
	s.started = started
	parent, err := os.Lstat(dir)
	if err != nil || !parent.IsDir() || parent.Mode()&os.ModeSymlink != 0 || parent.Mode().Perm()&0077 != 0 {
		return s
	}
	paths, _ := filepath.Glob(filepath.Join(dir, ".writer-health-v2*.json"))
	if len(paths) > 65 {
		return s
	}
	for _, p := range paths {
		var state struct {
			Schema  int           `json:"schema_version"`
			Buckets []factoryHour `json:"buckets"`
		}
		if boundedPrivateJSON(p, &state) != nil || state.Schema != 2 || len(state.Buckets) > 900 {
			return s
		}
		for _, row := range state.Buckets {
			if !validFactoryHour(row) {
				return s
			}
			s.buckets = append(s.buckets, row)
		}
	}
	sqlitePaths, _ := filepath.Glob(filepath.Join(dir, ".writer-health-v4*.sqlite"))
	if len(sqlitePaths) > 65 {
		return s
	}
	// Four readers at a time, a shared deadline, bounded stdout, no shell.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	type result struct {
		rows []factoryHour
		err  error
	}
	results := make(chan result, len(sqlitePaths))
	slots := make(chan struct{}, 4)
	for _, path := range sqlitePaths {
		go func(p string) {
			slots <- struct{}{}
			defer func() { <-slots }()
			if !privateRegular(p) {
				results <- result{err: fmt.Errorf("invalid sqlite authority")}
				return
			}
			cols := []string{"hour", "attempted", "written", "failed", "failure_contended", "failure_permission", "failure_privacy", "failure_io", "latency_max_us"}
			for i := 0; i < 12; i++ {
				cols = append(cols, fmt.Sprintf("latency_%d", i))
			}
			query := "SELECT " + strings.Join(cols, ",") + " FROM writer_hourly WHERE synthetic = 0 AND hour >= '" + time.Now().UTC().Add(-35*24*time.Hour).Truncate(time.Hour).Format(time.RFC3339) + "' ORDER BY hour LIMIT 901;"
			// Query-only prevents data changes while allowing SQLite to create its
			// WAL coordination files after the last writer has closed. A read-only
			// open cannot initialize absent -shm/-wal files on the macOS client.
			cmd := exec.CommandContext(ctx, "/usr/bin/sqlite3", "-bail", "-cmd", ".timeout 100", "-cmd", "PRAGMA query_only=ON;", "-json", p, query)
			output := &boundedOutput{limit: 2 << 20}
			cmd.Stdout = output
			cmd.Stderr = &boundedOutput{limit: 4096}
			if err := cmd.Run(); err != nil {
				results <- result{err: err}
				return
			}
			var raw []map[string]any
			if len(bytes.TrimSpace(output.Bytes())) == 0 {
				results <- result{}
				return
			}
			if err := json.Unmarshal(output.Bytes(), &raw); err != nil || len(raw) > 900 {
				results <- result{err: fmt.Errorf("invalid sqlite counters")}
				return
			}
			var rows []factoryHour
			for _, r := range raw {
				number := func(key string) int64 {
					v, ok := r[key].(float64)
					if !ok || v < 0 || v > float64(math.MaxInt64/2) || math.Trunc(v) != v {
						return -1
					}
					return int64(v)
				}
				hour, _ := r["hour"].(string)
				c := factoryCounters{Attempted: number("attempted"), Written: number("written"), Failed: number("failed"), Max: number("latency_max_us"), FailureClasses: map[string]int64{}}
				for _, class := range []string{"contended", "permission", "privacy", "io"} {
					n := number("failure_" + class)
					if n < 0 {
						results <- result{err: fmt.Errorf("invalid failure counters")}
						return
					}
					if n > 0 {
						c.FailureClasses["write_"+class] = n
					}
				}
				for i := 0; i < 12; i++ {
					c.Latency = append(c.Latency, number(fmt.Sprintf("latency_%d", i)))
				}
				row := factoryHour{Hour: hour, Real: c}
				if !validFactoryHour(row) {
					results <- result{err: fmt.Errorf("invalid counters")}
					return
				}
				rows = append(rows, row)
			}
			results <- result{rows: rows}
		}(path)
	}
	for range sqlitePaths {
		r := <-results
		if r.err != nil {
			s.reason = "sqlite_read_failed"
			return s
		}
		s.buckets = append(s.buckets, r.rows...)
	}
	s.known = true
	s.reason = ""
	return s
}

type boundedOutput struct {
	bytes.Buffer
	limit int
}

func (b *boundedOutput) Write(p []byte) (int, error) {
	if b.Len()+len(p) > b.limit {
		return 0, fmt.Errorf("authority output limit")
	}
	return b.Buffer.Write(p)
}

func ReadCLIWriterHealthSince(since time.Time) CLIWriterHealthReport {
	dir := filepath.Dir(DefaultSourceTelemetryPathAt(CLIInvocationSourceID, time.Now()))
	factoryCache.Lock()
	if factoryCache.snapshot.directory != dir || time.Since(factoryCache.snapshot.at) > 5*time.Second {
		factoryCache.snapshot = loadFactorySnapshot(dir)
	}
	s := factoryCache.snapshot
	factoryCache.Unlock()
	r := CLIWriterHealthReport{Known: s.known && !s.started.After(since.UTC().Truncate(time.Hour)) && !since.Before(s.at.Add(-35*24*time.Hour)), Source: "factory-sqlite-wal-v4+sidecar-v3", CoverageSince: since.UTC().Truncate(time.Hour), ObservedAt: s.at, FailureClasses: map[string]int64{}, LatencyScope: "authority_register_through_primary_append; histogram upper bound"}
	r.UnknownReason = s.reason
	if s.known && !r.Known {
		r.UnknownReason = "window_outside_retained_coverage"
	}
	histogram := make([]int64, 12)
	for _, row := range s.buckets {
		at, _ := time.Parse(time.RFC3339, row.Hour)
		if at.Before(r.CoverageSince) {
			continue
		}
		c := row.Real
		r.Attempted += c.Attempted
		r.Written += c.Written
		r.Failed += c.Failed
		r.LatencyMaxUS = max(r.LatencyMaxUS, c.Max)
		for k, n := range c.FailureClasses {
			r.FailureClasses[k] += n
		}
		for i, n := range c.Latency {
			histogram[i] += n
			r.LatencySamples += n
		}
	}
	r.Gaps = r.Attempted - r.Written - r.Failed
	if r.Gaps < 0 {
		r.Known = false
	}
	target := (r.LatencySamples*95 + 99) / 100
	var cumulative int64
	for i, n := range histogram {
		cumulative += n
		if target > 0 && cumulative >= target {
			r.LatencyP95US = min(factoryLatencyUpper[i], r.LatencyMaxUS)
			break
		}
	}
	return r
}
