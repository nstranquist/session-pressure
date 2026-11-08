// Package telemetry owns the standalone product's privacy-safe command stream.
// Factory-wrapped invocations are recorded by the factory, never a second time here.
package telemetry

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/nstranquist/session-pressure/internal/filelock"
	"golang.org/x/sys/unix"
)

const CLIInvocationSourceID = "cli-invocations"
const ProductSourceID = "session-pressure"

type Event struct {
	SchemaVersion int               `json:"schema_version"`
	Event         string            `json:"event"`
	TS            string            `json:"ts"`
	Source        string            `json:"source"`
	Surface       string            `json:"surface,omitempty"`
	Command       string            `json:"command,omitempty"`
	DurationUS    int64             `json:"duration_us,omitempty"`
	OK            *bool             `json:"ok,omitempty"`
	Privacy       string            `json:"privacy"`
	Attrs         map[string]any    `json:"attrs"`
	Correlation   map[string]string `json:"correlation,omitempty"`
}
type PrivacyViolation struct {
	Path string
	Kind string
}
type CLIWriterHealthReport struct {
	ObservedAt     time.Time        `json:"observed_at"`
	Known          bool             `json:"known"`
	UnknownReason  string           `json:"unknown_reason,omitempty"`
	Source         string           `json:"source"`
	CoverageSince  time.Time        `json:"coverage_since"`
	Attempted      int64            `json:"attempted"`
	Written        int64            `json:"written"`
	Failed         int64            `json:"failed"`
	Gaps           int64            `json:"gaps"`
	FailureClasses map[string]int64 `json:"failure_classes"`
	LatencyScope   string           `json:"latency_scope"`
	LatencySamples int64            `json:"latency_samples"`
	LatencyP95US   int64            `json:"latency_p95_us"`
	LatencyMaxUS   int64            `json:"latency_max_us"`
}
type bucket struct {
	Hour      time.Time `json:"hour"`
	Attempted int64     `json:"attempted"`
	Written   int64     `json:"written"`
	Failed    int64     `json:"failed"`
	LatencyUS []int64   `json:"latency_us"`
}
type writerState struct {
	SchemaVersion    int       `json:"schema_version"`
	StartedAt        time.Time `json:"started_at"`
	Buckets          []bucket  `json:"buckets"`
	MaintenanceError bool      `json:"maintenance_error,omitempty"`
}

func DefaultSourceTelemetryPathAt(source string, at time.Time) string {
	if at.IsZero() {
		at = time.Now()
	}
	home, _ := os.UserHomeDir()
	root := filepath.Join(home, ".nicos-dev", "telemetry", "sources")
	if override := os.Getenv("SESSION_PRESSURE_TELEMETRY_DIR"); override != "" {
		root = override
	}
	return filepath.Join(root, source, "events-"+at.UTC().Format("2006-01-02")+".jsonl")
}

var safeLabel = regexp.MustCompile(`^[a-zA-Z0-9_.:-]+(?: [a-zA-Z0-9_.:-]+)*$`)
var unsafeValue = regexp.MustCompile(`(?i)(/Users/|/home/|/private/|/var/folders/|https?://|bearer |sk-[a-z0-9]|password|api[_-]?key|auth[_-]?token|session[_-]?token|raw prompt|transcript|clipboard|stdout|stderr)`)

func safeFactoryMode(value string) bool {
	switch value {
	case "/perfect-world", "/perfect-world-challenge", "/perfect-world-compete",
		"/perfect-world-dryrun", "/perfect-world-gaps", "/perfect-world-ideas",
		"/perfect-world-implement", "/perfect-world-introspect", "/perfect-world-offboard",
		"/perfect-world-research", "/perfect-world-review", "/perfect-world-specs", "/pw-quick":
		return true
	}
	return false
}

func ValidateEventPrivacy(e Event) []PrivacyViolation {
	var out []PrivacyViolation
	for path, v := range map[string]string{"event": e.Event, "source": e.Source, "command": e.Command, "surface": e.Surface} {
		if len(v) > 160 || (v != "" && !safeLabel.MatchString(v)) {
			out = append(out, PrivacyViolation{path, "unsafe_machine_label"})
		}
	}
	var check func(string, any, int)
	check = func(path string, v any, depth int) {
		if depth > 8 {
			out = append(out, PrivacyViolation{path, "excessive_depth"})
			return
		}
		switch value := v.(type) {
		case string:
			// The factory emits these closed command modes, not filesystem paths.
			if path == "attrs.pw_mode" && safeFactoryMode(value) {
				return
			}
			if len(value) > 300 || strings.ContainsAny(value, "\n\r\\") || unsafeValue.MatchString(value) || strings.HasPrefix(value, "/") || strings.HasPrefix(value, "~/") {
				out = append(out, PrivacyViolation{path, "unsafe_payload"})
			}
		case map[string]any:
			if len(value) > 64 {
				out = append(out, PrivacyViolation{path, "oversized_map"})
				return
			}
			for k, v := range value {
				if len(k) > 80 || !safeLabel.MatchString(k) {
					out = append(out, PrivacyViolation{path, "unsafe_key"})
				}
				check(path+"."+k, v, depth+1)
			}
		case []any:
			if len(value) > 64 {
				out = append(out, PrivacyViolation{path, "oversized_array"})
				return
			}
			for _, v := range value {
				check(path, v, depth+1)
			}
		case nil, bool, int, int64, float64:
		default:
			out = append(out, PrivacyViolation{path, "unsupported_value"})
		}
	}
	check("attrs", e.Attrs, 0)
	for k, v := range e.Correlation {
		check("correlation."+k, v, 0)
	}
	return out
}
func Disabled() bool {
	switch strings.ToLower(os.Getenv("NDEV_TELEMETRY")) {
	case "0", "false", "off", "no":
		return true
	}
	return false
}

// Spread concurrent calls across the bounded shard set, including calls from
// one process. PID-only sharding serializes every command behind three fsyncs.
var writerSequence atomic.Uint64

func RecordCommand(action string, started time.Time, exit int) error {
	if Disabled() || os.Getenv("NDEV_PRESSURE_WRAPPER") == "1" || (strings.HasSuffix(os.Args[0], ".test") && os.Getenv("SESSION_PRESSURE_TELEMETRY_DIR") == "") {
		return nil
	}
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return err
	}
	outcome := "failure"
	switch {
	case exit == 0:
		outcome = "success"
	case exit == 2:
		outcome = "usage_error"
	case exit >= 11 && exit <= 15:
		outcome = "policy_denied"
	}
	ok := exit == 0
	e := Event{SchemaVersion: 1, Event: "session.pressure.command", TS: started.UTC().Format(time.RFC3339Nano), Source: ProductSourceID, Surface: "session-pressure", Command: "session pressure", DurationUS: time.Since(started).Microseconds(), OK: &ok, Privacy: "local", Attrs: map[string]any{"action": action, "exit_code": exit, "outcome": outcome, "operation_id": "session.pressure", "invocation_id": hex.EncodeToString(id[:]), "duration_scope": commandDurationScope(action)}}
	if len(ValidateEventPrivacy(e)) > 0 {
		return fmt.Errorf("command event failed privacy validation")
	}
	now := time.Now().UTC()
	dir := filepath.Dir(DefaultSourceTelemetryPathAt(ProductSourceID, now))
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0077 != 0 {
		return fmt.Errorf("telemetry directory must be private")
	}
	shard := fmt.Sprintf("-s%02d", (uint64(os.Getpid())+writerSequence.Add(1)-1)%64)
	path := strings.TrimSuffix(DefaultSourceTelemetryPathAt(ProductSourceID, now), ".jsonl") + shard + ".jsonl"
	health := filepath.Join(dir, ".writer-health"+shard+".json")
	begin := time.Now()
	release, err := filelock.AcquireContext(context.Background(), health, 500*time.Millisecond)
	if err != nil {
		// No attempt authority was acquired. Persist a bounded fault marker so
		// readers report unknown coverage rather than a false zero-loss result.
		faultErr := writeAuthorityFault(health, now)
		return errors.Join(err, faultErr)
	}
	defer release()
	state := writerState{SchemaVersion: 1, StartedAt: now}
	if raw, err := readBounded(health, 4<<20); err == nil {
		if err = json.Unmarshal(raw, &state); err != nil || !validWriterState(state) {
			return fmt.Errorf("invalid writer authority")
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	hour := now.Truncate(time.Hour)
	retained := state.Buckets[:0]
	for _, b := range state.Buckets {
		if !b.Hour.Before(hour.Add(-35 * 24 * time.Hour)) {
			retained = append(retained, b)
		}
	}
	state.Buckets = retained
	index := -1
	for i, b := range state.Buckets {
		if b.Hour.Equal(hour) {
			index = i
			break
		}
	}
	if index < 0 {
		state.Buckets = append(state.Buckets, bucket{Hour: hour})
		index = len(state.Buckets) - 1
	}
	b := &state.Buckets[index]
	b.Attempted++
	if err = writeState(health, state); err != nil {
		return err
	}
	raw, _ := json.Marshal(e)
	raw = append(raw, '\n')
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0600)
	if err == nil {
		fi, statErr := f.Stat()
		if statErr != nil {
			err = statErr
		} else if !fi.Mode().IsRegular() || fi.Mode().Perm()&0077 != 0 {
			err = fmt.Errorf("telemetry stream must be a private regular file")
		}
		if err == nil {
			_, err = f.Write(raw)
		}
		if err == nil {
			err = f.Sync()
		}
		closeErr := f.Close()
		if err == nil {
			err = closeErr
		}
	}
	if err != nil {
		b.Failed++
	} else {
		b.Written++
	}
	b.LatencyUS = append(b.LatencyUS, time.Since(begin).Microseconds())
	if len(b.LatencyUS) > 256 {
		b.LatencyUS = b.LatencyUS[len(b.LatencyUS)-256:]
	}
	finishErr := writeState(health, state)
	if err != nil {
		return err
	}
	if finishErr != nil {
		return finishErr
	}
	if b.Attempted == 1 || state.MaintenanceError {
		maintenanceErr := pruneCommandStreams(dir, now.Add(-35*24*time.Hour))
		state.MaintenanceError = maintenanceErr != nil
		if err := writeState(health, state); err != nil {
			return err
		}
		return maintenanceErr
	}
	return nil
}

// A marker has a fixed name per shard, so repeated failures cannot grow files.
// It remains visible for the retention window; later successful writes cannot
// retroactively prove the missing denominator.
func writeAuthorityFault(health string, at time.Time) error {
	raw, _ := json.Marshal(struct {
		At time.Time `json:"at"`
	}{at})
	f, err := os.CreateTemp(filepath.Dir(health), ".fault-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(raw); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(f.Name(), strings.TrimSuffix(health, ".json")+".fault")
}

func writeState(path string, state writerState) error {
	raw, err := json.Marshal(state)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".health-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(raw); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(f.Name(), path)
}
func ReadProductWriterHealthSince(since time.Time) CLIWriterHealthReport {
	// The source field states the exact authority. Absence or corruption is
	// unknown, never a zero-failure claim about the factory writer.
	result := CLIWriterHealthReport{ObservedAt: time.Now().UTC(), Source: ProductSourceID + "-writer-v1", CoverageSince: since.UTC().Truncate(time.Hour), LatencyScope: "lock_queue_through_primary_append; last 256 observations per hour", FailureClasses: map[string]int64{}}
	paths, err := filepath.Glob(filepath.Join(filepath.Dir(DefaultSourceTelemetryPathAt(ProductSourceID, time.Now())), ".writer-health-s*.json"))
	faults, faultErr := filepath.Glob(filepath.Join(filepath.Dir(DefaultSourceTelemetryPathAt(ProductSourceID, time.Now())), ".writer-health-s*.fault"))
	authorityFault := faultErr != nil
	for _, path := range faults {
		raw, e := readBounded(path, 1024)
		var fault struct {
			At time.Time `json:"at"`
		}
		if e != nil || json.Unmarshal(raw, &fault) != nil || fault.At.IsZero() || !fault.At.Before(since.UTC().Truncate(time.Hour)) {
			authorityFault = true
			result.FailureClasses["authority_unavailable"]++
		}
	}
	if authorityFault {
		result.UnknownReason = "authority_unavailable"
	}
	if err != nil || len(paths) == 0 {
		return result
	}
	var latency []int64
	result.Known = true
	for _, path := range paths {
		raw, err := readBounded(path, 4<<20)
		if err != nil {
			result.Known = false
			continue
		}
		var state writerState
		if json.Unmarshal(raw, &state) != nil || !validWriterState(state) {
			result.Known = false
			continue
		}
		if state.MaintenanceError {
			result.FailureClasses["retention_io"]++
		}
		if state.StartedAt.After(result.CoverageSince) {
			result.CoverageSince = state.StartedAt
		}
		for _, b := range state.Buckets {
			if b.Hour.Before(since.UTC().Truncate(time.Hour)) {
				continue
			}
			result.Attempted += b.Attempted
			result.Written += b.Written
			result.Failed += b.Failed
			latency = append(latency, b.LatencyUS...)
		}
	}
	if authorityFault {
		result.Known = false
	}
	result.Gaps = result.Attempted - result.Written - result.Failed
	if result.Failed > 0 {
		result.FailureClasses["write_io"] = result.Failed
	}
	sort.Slice(latency, func(i, j int) bool { return latency[i] < latency[j] })
	result.LatencySamples = int64(len(latency))
	if len(latency) > 0 {
		result.LatencyMaxUS = latency[len(latency)-1]
		result.LatencyP95US = latency[(len(latency)-1)*95/100]
	}
	return result
}

func readBounded(path string, limit int64) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return nil, fmt.Errorf("writer authority is not a private regular file")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return nil, fmt.Errorf("writer authority changed while opening")
	}
	raw, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > limit {
		return nil, fmt.Errorf("writer authority exceeds limit")
	}
	return raw, nil
}
func validWriterState(s writerState) bool {
	if s.SchemaVersion != 1 || len(s.Buckets) > 900 || s.StartedAt.IsZero() || s.StartedAt.After(time.Now().Add(5*time.Minute)) {
		return false
	}
	seen := map[time.Time]bool{}
	for _, b := range s.Buckets {
		if b.Hour.IsZero() || !b.Hour.Equal(b.Hour.UTC().Truncate(time.Hour)) || b.Hour.After(time.Now().Add(5*time.Minute)) || seen[b.Hour] || b.Attempted < 0 || b.Written < 0 || b.Failed < 0 || b.Written > b.Attempted-b.Failed || len(b.LatencyUS) > 256 {
			return false
		}
		seen[b.Hour] = true
		for _, n := range b.LatencyUS {
			if n < 0 {
				return false
			}
		}
	}
	return true
}
func pruneCommandStreams(dir string, cutoff time.Time) error {
	paths, err := filepath.Glob(filepath.Join(dir, "events-*.jsonl"))
	if err != nil {
		return err
	}
	for _, path := range paths {
		name := filepath.Base(path)
		if len(name) != len("events-2006-01-02-s00.jsonl") || name[17:19] != "-s" || name[19] < '0' || name[19] > '9' || name[20] < '0' || name[20] > '9' {
			continue
		}
		day, err := time.Parse("2006-01-02", name[7:17])
		if err != nil || !day.Before(cutoff.UTC().Truncate(24*time.Hour)) {
			continue
		}
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("command telemetry retention failed: %w", err)
		}
	}
	return nil
}

func commandDurationScope(action string) string {
	if action == "work.run" || action == "work.batch" {
		return "wait_and_child_runtime"
	}
	return "command_runtime"
}
