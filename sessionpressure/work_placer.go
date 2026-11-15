package sessionpressure

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"time"
)

// Work queue v2 slice 4: the closed-loop placer.
//
// Auto placement used to send every eligible check to the runner. A small
// check then paid upload, runner queue and install time (Node p50 32 s on
// 2026-11-15) for work the idle Mac finished in about 5 s. The placer keeps
// such a check local only when all of these are true:
//
//   - the Mac can start it now: no waiters, no admission holds, capacity for
//     its weight, and normal host pressure;
//   - measured history for the same check (placement key) says local is
//     faster: local p75 runtime below remote p50 duration.
//
// Both sides learn from their own completions, so the loop is closed: a
// check that grows slow locally moves back to the runner. With no local
// history, a check whose remote p50 is at most placementExploreCeilingMS
// runs locally to measure it. Every decision is recorded as
// work_placement_decided, and a local decision prints one stderr line, so
// it never reads as a silent fallback. Busy or unknown means remote, which
// is the behaviour without the placer.

const (
	placementHistoryWindow    = 7 * 24 * time.Hour
	placementMinSamples       = 2
	placementMaxSamples       = 20
	placementExploreCeilingMS = 45_000
)

// PlacementKey is an opaque digest of the working directory and argv. It
// names "the same check" across local work events and remote completions.
func PlacementKey(cwd string, argv []string) string {
	hash := sha256.New()
	_, _ = io.WriteString(hash, filepath.Clean(cwd))
	for _, arg := range argv {
		_, _ = hash.Write([]byte{0})
		_, _ = io.WriteString(hash, arg)
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil))
}

type placementHistory struct {
	localMS  []int64
	remoteMS []int64
}

type placementDecision struct {
	Local         bool
	Reason        string
	LocalETAMS    int64
	RemoteETAMS   int64
	LocalSamples  int
	RemoteSamples int
}

// percentileMS returns the nearest-rank percentile of values.
func percentileMS(values []int64, percent int) int64 {
	if len(values) == 0 {
		return 0
	}
	sorted := slices.Clone(values)
	slices.Sort(sorted)
	rank := (percent*len(sorted) + 99) / 100
	return sorted[max(rank, 1)-1]
}

// decidePlacement is the pure placer. It never chooses local for a busy Mac.
func decidePlacement(idle bool, busyReason string, history placementHistory) placementDecision {
	decision := placementDecision{LocalSamples: len(history.localMS), RemoteSamples: len(history.remoteMS)}
	if len(history.localMS) >= placementMinSamples {
		decision.LocalETAMS = percentileMS(history.localMS, 75)
	}
	if len(history.remoteMS) >= placementMinSamples {
		decision.RemoteETAMS = percentileMS(history.remoteMS, 50)
	}
	switch {
	case !idle:
		decision.Reason = "mac_busy: " + busyReason
	case decision.LocalETAMS > 0 && decision.RemoteETAMS > 0:
		decision.Local = decision.LocalETAMS < decision.RemoteETAMS
		decision.Reason = "remote_faster"
		if decision.Local {
			decision.Reason = "local_faster"
		}
	case decision.LocalETAMS > 0:
		// Remote history aged out: refresh it rather than trust one side.
		decision.Reason = "no_remote_history"
	case decision.RemoteETAMS > 0 && decision.RemoteETAMS <= placementExploreCeilingMS:
		decision.Local, decision.Reason = true, "explore_local"
	default:
		decision.Reason = "no_local_history"
	}
	return decision
}

// readPlacementHistory collects the most recent successful durations of one
// check, by exact key and by family key: local runtimes from work events,
// remote durations from remote_work_finished control events. A read error
// yields no samples, which keeps the check remote.
func readPlacementHistory(dir string, keys PlacementKeys, now time.Time) (exact, family placementHistory) {
	since := now.Add(-placementHistoryWindow)
	if events, err := NewWorkEventStore(dir).completedRowsSince(since); err == nil {
		for _, event := range events {
			if event.Event != WorkEventCompleted || event.RuntimeMillis <= 0 {
				continue
			}
			if keys.Exact != "" && event.PlacementKey == keys.Exact {
				exact.localMS = append(exact.localMS, event.RuntimeMillis)
			}
			if keys.Family != "" && event.PlacementFamilyKey == keys.Family {
				family.localMS = append(family.localMS, event.RuntimeMillis)
			}
		}
	}
	exact.remoteMS, family.remoteMS = readRemoteDurations(filepath.Join(dir, "work-control-events.jsonl"), keys, since)
	for _, h := range []*placementHistory{&exact, &family} {
		h.localMS, h.remoteMS = lastSamples(h.localMS), lastSamples(h.remoteMS)
	}
	return exact, family
}

// chooseHistory prefers the exact check. The family stands in only when the
// exact key lacks samples on either side.
func chooseHistory(exact, family placementHistory) (placementHistory, string) {
	if len(exact.localMS) >= placementMinSamples && len(exact.remoteMS) >= placementMinSamples {
		return exact, "key"
	}
	if len(family.localMS) >= placementMinSamples || len(family.remoteMS) >= placementMinSamples {
		return family, "family"
	}
	return exact, "key"
}

func lastSamples(values []int64) []int64 {
	if len(values) > placementMaxSamples {
		return values[len(values)-placementMaxSamples:]
	}
	return values
}

func readRemoteDurations(path string, keys PlacementKeys, since time.Time) (exact, family []int64) {
	file, err := os.Open(path)
	if err != nil {
		return nil, nil
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64<<10), 1<<20)
	matches := func(line []byte, key string) bool { return key != "" && bytes.Contains(line, []byte(key)) }
	for scanner.Scan() {
		line := scanner.Bytes()
		if !matches(line, keys.Exact) && !matches(line, keys.Family) {
			continue
		}
		var row struct {
			Event string    `json:"event"`
			TS    time.Time `json:"ts"`
			Attrs struct {
				DurationMS         int64  `json:"duration_ms"`
				Failed             bool   `json:"failed"`
				PlacementKey       string `json:"placement_key"`
				PlacementFamilyKey string `json:"placement_family_key"`
			} `json:"attrs"`
		}
		if json.Unmarshal(line, &row) != nil || row.Event != "remote_work_finished" {
			continue
		}
		if row.Attrs.Failed || row.Attrs.DurationMS <= 0 || row.TS.Before(since) {
			continue
		}
		if keys.Exact != "" && row.Attrs.PlacementKey == keys.Exact {
			exact = append(exact, row.Attrs.DurationMS)
		}
		if keys.Family != "" && row.Attrs.PlacementFamilyKey == keys.Family {
			family = append(family, row.Attrs.DurationMS)
		}
	}
	return exact, family
}

// macIdleProbe answers "could this command start on the Mac right now". It
// reads the published queue snapshot without the mutation lock.
func macIdleProbe(coordinator *WorkCoordinator, class WorkClass, admission func() Admission) func() (bool, string) {
	return func() (bool, string) {
		weight, err := coordinator.limits().Weight(class)
		if err != nil {
			return false, "unknown class weight"
		}
		status, err := coordinator.StatusSnapshot(context.Background())
		if err != nil {
			return false, "queue status unavailable"
		}
		switch {
		case status.QueueDepth > 0:
			return false, fmt.Sprintf("%d waiters queued", status.QueueDepth)
		case status.AdmissionHoldCount > 0:
			return false, fmt.Sprintf("%d admission holds", status.AdmissionHoldCount)
		case status.Available < weight:
			return false, fmt.Sprintf("%d capacity free, needs %d", status.Available, weight)
		}
		if admission == nil {
			admission = func() Admission { return ConfiguredWorkHostAdmission(context.Background()) }
		}
		if host := admission(); !host.Allowed || host.Level != LevelNormal {
			return false, "host pressure " + string(host.Level)
		}
		// Admission reports normal in observe mode, whatever the host does.
		// The resident sample is the host's real level.
		resident, ok := NewTelemetryStore(coordinator.Dir).ReadLatest()
		if !ok || resident.Timestamp.IsZero() || time.Since(resident.Timestamp) > headroomSampleTTL {
			return false, "no fresh resident sample"
		}
		if resident.Level != LevelNormal {
			return false, "host level " + string(resident.Level)
		}
		return true, ""
	}
}

// placeLocally runs the placer for an eligible auto-placed command. True
// means the caller returns "not handled" so the command takes local
// admission. Every decision is recorded.
func placeLocally(dir string, policy RemoteWorkPolicy, options WorkRunOptions, keys PlacementKeys, family string, stderr io.Writer) bool {
	if !policy.Placer || options.Placement != WorkPlacementAuto || options.placementProbe == nil {
		return false
	}
	idle, busy := options.placementProbe()
	var history placementHistory
	scope := "key"
	if idle {
		history, scope = chooseHistory(readPlacementHistory(dir, keys, time.Now()))
	}
	decision := decidePlacement(idle, busy, history)
	target := "remote"
	if decision.Local {
		target = "local"
	}
	recordControlEvent(dir, map[string]any{
		"event": "work_placement_decided", "placement": target, "reason": boundedText(decision.Reason, 80),
		"local_eta_ms": decision.LocalETAMS, "remote_eta_ms": decision.RemoteETAMS,
		"local_samples": decision.LocalSamples, "remote_samples": decision.RemoteSamples,
		"tool_family": family, "class": string(options.Class), "placement_key": keys.Exact,
		"placement_family_key": keys.Family, "history_scope": scope,
	})
	if decision.Local && stderr != nil {
		fmt.Fprintf(stderr, "session-pressure: placer kept this check on the idle Mac (%s by %s history; local p75 %s, remote p50 %s)\n",
			decision.Reason, scope, etaText(decision.LocalETAMS), etaText(decision.RemoteETAMS))
	}
	return decision.Local
}

func etaText(ms int64) string {
	if ms <= 0 {
		return "unmeasured"
	}
	return (time.Duration(ms) * time.Millisecond).Round(100 * time.Millisecond).String()
}
