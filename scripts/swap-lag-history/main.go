// Command swap-lag-history replays resident telemetry to back the swap lag
// release thresholds (Thresholds.SwapLagReleaseMinutes) with evidence.
//
// It reads snapshots-*.jsonl from the session-pressure data directory and
// reports:
//   - every red or critical record whose primary reason is the sticky swap
//     rung, with the swap-thrash latch state at that moment;
//   - the calm gaps between thrash episodes at swap >= the red line;
//   - for each candidate window W, how many calm gaps a W-minute release
//     would have released, the minutes released, and how many relapsed into
//     thrash within 10 minutes of the release.
//
// Usage: go run ./scripts/swap-lag-history [-dir DIR] [-red-mb 8192]
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"time"
)

type event struct {
	Timestamp time.Time `json:"timestamp"`
	Event     string    `json:"event"`
	Snapshot  *struct {
		Level       string   `json:"level"`
		Reasons     []string `json:"reasons"`
		FreePercent int      `json:"free_percent"`
		SwapUsedMB  float64  `json:"swap_used_mb"`
	} `json:"snapshot"`
	Summary *struct {
		Level         string  `json:"level"`
		PrimaryReason string  `json:"primary_reason"`
		FreePercent   int     `json:"free_percent"`
		SwapUsedMB    float64 `json:"swap_used_mb"`
	} `json:"summary"`
	SwapThrash *struct {
		State      string  `json:"state"`
		SwapUsedMB float64 `json:"swap_used_mb"`
	} `json:"swap_thrash"`
}

type levelRecord struct {
	at     time.Time
	level  string
	reason string
	free   int
	swapMB float64
}

type thrashRecord struct {
	at     time.Time
	state  string
	swapMB float64
}

var swapRungReason = regexp.MustCompile(`^swap [0-9]+ MB >= (red|critical) `)

func main() {
	home, _ := os.UserHomeDir()
	dir := flag.String("dir", filepath.Join(home, ".nicos-dev", "session-pressure"), "session-pressure data directory")
	redMB := flag.Float64("red-mb", 8192, "swap red line in MB")
	flag.Parse()
	levels, thrash, err := load(*dir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "swap-lag-history:", err)
		os.Exit(1)
	}
	report(os.Stdout, levels, thrash, *redMB)
}

func load(dir string) ([]levelRecord, []thrashRecord, error) {
	paths, err := filepath.Glob(filepath.Join(dir, "snapshots-*.jsonl"))
	if err != nil || len(paths) == 0 {
		return nil, nil, fmt.Errorf("no snapshots-*.jsonl in %s", dir)
	}
	var levels []levelRecord
	var thrash []thrashRecord
	for _, path := range paths {
		file, err := os.Open(path)
		if err != nil {
			return nil, nil, err
		}
		scanner := bufio.NewScanner(file)
		scanner.Buffer(make([]byte, 0, 64<<10), 4<<20)
		for scanner.Scan() {
			var row event
			if json.Unmarshal(scanner.Bytes(), &row) != nil {
				continue
			}
			switch {
			case row.Event == "swap_thrash_transition" && row.SwapThrash != nil:
				thrash = append(thrash, thrashRecord{at: row.Timestamp, state: row.SwapThrash.State, swapMB: row.SwapThrash.SwapUsedMB})
			case row.Snapshot != nil:
				reason := ""
				if len(row.Snapshot.Reasons) > 0 {
					reason = row.Snapshot.Reasons[0]
				}
				levels = append(levels, levelRecord{row.Timestamp, row.Snapshot.Level, reason, row.Snapshot.FreePercent, row.Snapshot.SwapUsedMB})
			case row.Summary != nil:
				levels = append(levels, levelRecord{row.Timestamp, row.Summary.Level, row.Summary.PrimaryReason, row.Summary.FreePercent, row.Summary.SwapUsedMB})
			}
		}
		file.Close()
		if err := scanner.Err(); err != nil {
			return nil, nil, fmt.Errorf("%s: %w", path, err)
		}
	}
	sort.Slice(levels, func(i, j int) bool { return levels[i].at.Before(levels[j].at) })
	sort.Slice(thrash, func(i, j int) bool { return thrash[i].at.Before(thrash[j].at) })
	return levels, thrash, nil
}

// latchAt reports the latch state at t and how long it has held.
func latchAt(thrash []thrashRecord, t time.Time) (string, time.Duration, bool) {
	var last *thrashRecord
	for index := range thrash {
		if thrash[index].at.After(t) {
			break
		}
		last = &thrash[index]
	}
	if last == nil {
		return "calm", 0, false
	}
	if last.state == "released" {
		return "calm", t.Sub(last.at), true
	}
	return "latched", t.Sub(last.at), true
}

type gap struct {
	length time.Duration
	swapMB float64
}

// calmGaps are the intervals from a release to the next entry.
func calmGaps(thrash []thrashRecord) []gap {
	var gaps []gap
	var released *thrashRecord
	for index := range thrash {
		row := &thrash[index]
		switch row.state {
		case "released":
			released = row
		case "entered":
			if released != nil {
				gaps = append(gaps, gap{length: row.at.Sub(released.at), swapMB: released.swapMB})
				released = nil
			}
		}
	}
	return gaps
}

func report(out *os.File, levels []levelRecord, thrash []thrashRecord, redMB float64) {
	fmt.Fprintf(out, "records: %d level, %d thrash transitions\n\n", len(levels), len(thrash))
	fmt.Fprintln(out, "sticky swap rung red/critical records (primary reason):")
	latched, calmMax := 0, time.Duration(0)
	count := 0
	for _, record := range levels {
		if record.level != "red" && record.level != "critical" || !swapRungReason.MatchString(record.reason) {
			continue
		}
		count++
		state, held, known := latchAt(thrash, record.at)
		if state == "latched" {
			latched++
		} else if known && held > calmMax {
			calmMax = held
		}
		fmt.Fprintf(out, "  %s %-8s free=%d%% swap=%.0f MB thrash=%s for %s\n", record.at.UTC().Format("01-02T15:04:05"), record.level, record.free, record.swapMB, state, held.Round(time.Second))
	}
	fmt.Fprintf(out, "  total=%d latched=%d longest_calm=%s\n\n", count, latched, calmMax.Round(time.Second))

	gaps := calmGaps(thrash)
	var high []time.Duration
	for _, g := range gaps {
		if g.swapMB >= redMB {
			high = append(high, g.length)
		}
	}
	sort.Slice(high, func(i, j int) bool { return high[i] < high[j] })
	fmt.Fprintf(out, "calm gaps between thrash episodes at swap >= %.0f MB: n=%d", redMB, len(high))
	if len(high) > 0 {
		pct := func(p float64) time.Duration { return high[min(len(high)-1, int(float64(len(high))*p))] }
		fmt.Fprintf(out, " p25=%s p50=%s p75=%s p90=%s p95=%s max=%s", pct(.25), pct(.5), pct(.75), pct(.9), pct(.95), high[len(high)-1])
	}
	fmt.Fprintln(out)
	fmt.Fprintln(out, "\nwindow  released_gaps  released_minutes  relapse_within_10m")
	for _, minutes := range []int{5, 10, 15, 20, 30} {
		window := time.Duration(minutes) * time.Minute
		released, relapse := 0, 0
		var minutesReleased float64
		for _, length := range high {
			if length < window {
				continue
			}
			released++
			minutesReleased += (length - window).Minutes()
			if length < window+10*time.Minute {
				relapse++
			}
		}
		fmt.Fprintf(out, "%4dm  %13d  %16.0f  %18d\n", minutes, released, minutesReleased, relapse)
	}
}
