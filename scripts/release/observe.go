package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/nstranquist/session-pressure/packages/processtree"
)

// observe keeps bounded evidence for one continuous run. Elapsed gates are never inferred
// from a short sample. Run under launchd to survive an agent session ending.
type readGate struct {
	Samples  int     `json:"samples"`
	Failures int     `json:"failures"`
	P95MS    float64 `json:"p95_ms"`
	BudgetMS int     `json:"budget_ms"`
	Passed   bool    `json:"passed"`
}
type observation struct {
	MonitoringStarted    time.Time           `json:"monitoring_started"`
	BinaryDigest         string              `json:"binary_sha256"`
	AppPath              string              `json:"app_path"`
	AppIdentity          appIdentity         `json:"app_identity"`
	Accepted24H          bool                `json:"accepted_24h"`
	Accepted72H          bool                `json:"accepted_72h"`
	AppRestarts          int                 `json:"app_restarts"`
	Started              time.Time           `json:"started"`
	Updated              time.Time           `json:"updated"`
	Deadline             time.Time           `json:"deadline"`
	Binary               string              `json:"binary"`
	Identity             json.RawMessage     `json:"identity,omitempty"`
	Reads                map[string]readGate `json:"reads"`
	Checks               int                 `json:"checks"`
	Failures             int                 `json:"failures"`
	HealthPassed         bool                `json:"health_passed"`
	LastFailure          string              `json:"last_failure,omitempty"`
	ResidentPID          int                 `json:"resident_pid"`
	ResidentRestarts     int                 `json:"resident_restarts"`
	MaxSampleGapSeconds  float64             `json:"max_sample_gap_seconds"`
	ContinuityPassed     bool                `json:"continuity_passed"`
	Observed24H          bool                `json:"observed_24h"`
	Observed72H          bool                `json:"observed_72h"`
	AppSampleOK          bool                `json:"app_sample_ok"`
	AppSampleFailures    int                 `json:"app_sample_failures"`
	LastAppSampleError   string              `json:"last_app_sample_error,omitempty"`
	LastAppSampleFailure time.Time           `json:"last_app_sample_failure,omitempty"`
	AppPID               int                 `json:"app_pid"`
	AppObservedSeconds   float64             `json:"app_observed_seconds"`
	AppCPUPercent        float64             `json:"app_cpu_percent"`
	AppPeakRSSMiB        float64             `json:"app_peak_rss_mib"`
	AppBudgetPassed      bool                `json:"app_30m_budget_passed"`
}

func readCLI(binary string, args ...string) ([]byte, error) {
	return readCLIReport(binary, false, args...)
}

// Detectors use exit 2 and ok=false for a successfully emitted unsatisfied
// gate. Preserve that report; transport, timeout and other exits still fail.
func readCLIReport(binary string, detector bool, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	prefix := []string{"--json"}
	if filepath.Base(binary) == "ndev" {
		prefix = append(prefix, "session", "pressure")
	}
	c := processtree.CommandContext(ctx, binary, append(prefix, args...)...)
	// Read-only endpoints must remain within the same 2 MiB client contract.
	p, e := c.StdoutPipe()
	if e != nil {
		return nil, e
	}
	c.Stderr = io.Discard
	if e = c.Start(); e != nil {
		return nil, e
	}
	stopClose := context.AfterFunc(ctx, func() { _ = p.Close() })
	defer stopClose()
	raw, e := io.ReadAll(io.LimitReader(p, 2*1024*1024+1))
	if e != nil || len(raw) > 2*1024*1024 {
		_ = c.Cancel()
		_ = c.Wait()
		return nil, errors.New("bounded read failed")
	}
	if e = c.Wait(); e != nil {
		var exit interface{ ExitCode() int }
		if !detector || !errors.As(e, &exit) || exit.ExitCode() != 2 {
			return nil, e
		}
	}
	var envelope struct {
		OK bool `json:"ok"`
	}
	if e = json.Unmarshal(raw, &envelope); e != nil {
		return nil, e
	}
	if !detector && !envelope.OK {
		return nil, errors.New("read reported ok=false")
	}
	return raw, nil
}
func observe(args []string) error {
	f := flag.NewFlagSet("observe", flag.ContinueOnError)
	out := f.String("out", "", "absolute evidence JSON path")
	binary := f.String("binary", "", "installed product CLI")
	app := f.String("app", "/Applications/NDev Pressure.app", "installed native app bundle")
	duration := f.Duration("duration", 72*time.Hour, "elapsed observation window")
	interval := f.Duration("interval", time.Minute, "health sample interval (10s..5m)")
	samples := f.Int("samples", 100, "warm reads per endpoint (100..1000)")
	if e := f.Parse(args); e != nil {
		return e
	}
	if !filepath.IsAbs(*out) || !filepath.IsAbs(*binary) || !filepath.IsAbs(*app) || *duration <= 0 || *duration > 7*24*time.Hour || *interval < 10*time.Second || *interval > 5*time.Minute || *samples < 100 || *samples > 1000 || f.NArg() != 0 {
		return errors.New("absolute --out/--binary/--app, duration <=7d, interval 10s..5m, samples 100..1000 required")
	}
	if e := os.MkdirAll(filepath.Dir(*out), 0700); e != nil {
		return e
	}
	started := time.Now().UTC()
	s := observation{Started: started, Updated: started, Deadline: started.Add(*duration), Binary: *binary, AppPath: *app, Reads: map[string]readGate{}}
	// A launch agent can run again after login. Preserve the interrupted run's
	// evidence rather than silently replacing its failures and elapsed window.
	if e := startObservation(*out, s); e != nil {
		return e
	}
	identity, e := readCLI(*binary, "capabilities")
	if e != nil {
		return fmt.Errorf("identity: %w", e)
	}
	s.BinaryDigest, e = digest(*binary)
	if e != nil {
		return fmt.Errorf("binary identity: %w", e)
	}
	s.AppIdentity, e = readAppIdentity(*app)
	if e != nil || !s.AppIdentity.Exists {
		return fmt.Errorf("installed app identity unavailable: %v", e)
	}
	s.Identity = identity
	if e = writeJSON(*out, s); e != nil {
		return e
	}
	for _, endpoint := range []struct {
		name   string
		args   []string
		budget int
	}{{"status", []string{"status"}, 250}, {"board", []string{"board"}, 500}, {"rich", []string{"board", "--full", "--include", "all"}, 1000}} {
		_, _ = readCLI(*binary, endpoint.args...)
		var times []float64
		g := readGate{BudgetMS: endpoint.budget}
		for i := 0; i < *samples; i++ {
			t := time.Now()
			_, err := readCLI(*binary, endpoint.args...)
			times = append(times, float64(time.Since(t).Microseconds())/1000)
			g.Samples++
			if err != nil {
				g.Failures++
			}
		}
		sort.Float64s(times)
		g.P95MS = times[(len(times)*95+99)/100-1]
		g.Passed = g.Failures == 0 && g.P95MS <= float64(g.BudgetMS)
		s.Reads[endpoint.name] = g
		s.Updated = time.Now().UTC()
		if e = writeJSON(*out, s); e != nil {
			return e
		}
	}
	s.MonitoringStarted = time.Now().UTC()
	s.Deadline = s.MonitoringStarted.Add(*duration)
	lastSample := s.MonitoringStarted
	s.ContinuityPassed = true
	var window appObservationWindow
	for {
		now := time.Now().UTC()
		gap := now.Sub(lastSample)
		if gap.Seconds() > s.MaxSampleGapSeconds {
			s.MaxSampleGapSeconds = gap.Seconds()
		}
		if gap > 2*(*interval)+30*time.Second {
			s.ContinuityPassed = false
		}
		lastSample = now
		s.Updated = now
		current, err := readCLI(*binary, "capabilities")
		if err == nil {
			var before, after struct {
				Capabilities struct {
					Revision string `json:"source_revision"`
				} `json:"capabilities"`
			}
			_ = json.Unmarshal(s.Identity, &before)
			_ = json.Unmarshal(current, &after)
			if before.Capabilities.Revision == "" || before.Capabilities.Revision != after.Capabilities.Revision {
				err = errors.New("installed revision changed")
			}
		}
		if err == nil {
			err = s.verifyArtifacts()
		}
		if err == nil {
			var status []byte
			status, err = readCLI(*binary, "status")
			if err == nil {
				var pid int
				pid, err = observedResident(status)
				if err == nil {
					if s.ResidentPID != 0 && s.ResidentPID != pid {
						s.ResidentRestarts++
						err = errors.New("resident restarted during observation")
					}
					s.ResidentPID = pid
				}
			}
		}
		s.Checks++
		if err != nil {
			s.Failures++
			s.LastFailure = err.Error()
		}
		s.HealthPassed = s.Checks > 0 && s.Failures == 0 && s.ContinuityPassed
		pid, cpu, rss, sampleErr := appSample(filepath.Join(s.AppPath, "Contents", "MacOS", "NDevPressure"))
		sampleTime := time.Now().UTC()
		if sampleErr == nil {
			s.recordAppSample(sampleTime, pid, cpu, rss, &window)
		} else {
			s.recordAppSampleFailure(sampleTime, sampleErr)
		}
		s.updateAcceptance(sampleTime)
		if e = writeJSON(*out, s); e != nil {
			return e
		}
		if !now.Before(s.Deadline) {
			return nil
		}
		time.Sleep(min(*interval, time.Until(s.Deadline)))
	}
}

// A successful read can still report a missing or stale monitor. Require the
// installed resident's actual health, identity verification and resource budget.
func observedResident(raw []byte) (int, error) {
	var status struct {
		HasLatest       bool `json:"has_latest_monitor"`
		PolicyPersisted bool `json:"policy_persisted"`
		Health          struct {
			Healthy bool `json:"monitor_healthy"`
			Fresh   bool `json:"latest_monitor_fresh"`
		} `json:"health"`
		Latest struct {
			BudgetOK bool `json:"guard_budget_ok"`
		} `json:"latest_monitor_summary"`
		Launchd struct {
			Installed bool `json:"installed"`
			Loaded    bool `json:"loaded"`
			Verified  bool `json:"artifact_verified"`
			PID       int  `json:"pid"`
		} `json:"launchd_summary"`
	}
	if e := json.Unmarshal(raw, &status); e != nil {
		return 0, e
	}
	if !status.HasLatest || !status.PolicyPersisted || !status.Health.Healthy || !status.Health.Fresh || !status.Latest.BudgetOK || !status.Launchd.Installed || !status.Launchd.Loaded || !status.Launchd.Verified || status.Launchd.PID <= 0 {
		return 0, errors.New("resident health, freshness, identity or resource budget failed")
	}
	return status.Launchd.PID, nil
}

func startObservation(path string, initial observation) error {
	raw, e := json.MarshalIndent(initial, "", "  ")
	if e != nil {
		return e
	}
	f, e := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		return fmt.Errorf("new observation requires an unused --out path: %w", e)
	}
	_, e = f.Write(append(raw, '\n'))
	if e == nil {
		e = f.Sync()
	}
	e = errors.Join(e, f.Close())
	if e != nil {
		return e
	}
	return syncDir(filepath.Dir(path))
}

// Sampling failures are separate from product health and survive recovery.
// Clear instantaneous values so a missing sample cannot show stale CPU as current.
func (s *observation) recordAppSampleFailure(now time.Time, err error) {
	s.AppSampleOK = false
	s.AppSampleFailures++
	s.LastAppSampleError = err.Error()
	s.LastAppSampleFailure = now
	s.AppPID = 0
	s.AppObservedSeconds = 0
	s.AppCPUPercent = 0
	s.AppPeakRSSMiB = 0
	s.AppBudgetPassed = false
}

func appSample(executable string) (int, float64, float64, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return sampleAppWith(func(name string, args ...string) ([]byte, error) {
		return processtree.CommandContext(ctx, name, args...).Output()
	}, executable)
}

func sampleAppWith(run func(string, ...string) ([]byte, error), executable ...string) (int, float64, float64, error) {
	raw, err := run("pgrep", "-x", "NDevPressure")
	if err != nil {
		return 0, 0, 0, errors.New("app_discovery_failed")
	}
	ids := strings.Fields(string(raw))
	if len(ids) != 1 {
		return 0, 0, 0, errors.New("app_discovery_requires_one_pid")
	}
	pid, err := strconv.Atoi(ids[0])
	if err != nil || pid <= 0 {
		return 0, 0, 0, errors.New("app_discovery_invalid_pid")
	}
	raw, err = run("ps", "-p", ids[0], "-o", "time=", "-o", "rss=")
	if err != nil {
		return 0, 0, 0, errors.New("app_process_read_failed")
	}
	fields := strings.Fields(string(raw))
	if len(fields) != 2 {
		return 0, 0, 0, errors.New("app_process_fields_invalid")
	}
	cpu, err := parseAppCPUTime(fields[0])
	if err != nil {
		return 0, 0, 0, err
	}
	rss, err := strconv.ParseFloat(fields[1], 64)
	if err != nil || rss < 0 || math.IsNaN(rss) || math.IsInf(rss, 0) {
		return 0, 0, 0, errors.New("app_rss_invalid")
	}
	if len(executable) > 0 {
		path, err := run("ps", "-p", ids[0], "-o", "comm=")
		if err != nil || strings.TrimSpace(string(path)) != executable[0] {
			return 0, 0, 0, errors.New("app_executable_identity_mismatch")
		}
	}
	return pid, cpu, rss / 1024, nil
}

// BSD ps uses [[dd-]hh:]mm:ss, with fractional seconds on macOS.
func parseAppCPUTime(value string) (float64, error) {
	invalid := errors.New("app_cpu_time_invalid")
	days := 0
	hasDays := strings.Contains(value, "-")
	if before, after, ok := strings.Cut(value, "-"); ok {
		var err error
		days, err = strconv.Atoi(before)
		if err != nil || days < 0 || days > 36500 {
			return 0, invalid
		}
		value = after
	}
	parts := strings.Split(value, ":")
	if len(parts) < 2 || len(parts) > 3 || (hasDays && len(parts) != 3) {
		return 0, invalid
	}
	cpu := float64(days) * 86400
	// Without a day prefix ps may report hours above 23; minutes can exceed
	// 59 in the two-field representation.
	for i, part := range parts {
		v, err := strconv.ParseFloat(part, 64)
		if err != nil || v < 0 || math.IsNaN(v) || math.IsInf(v, 0) ||
			(i > 0 && v >= 60) || (i < len(parts)-1 && math.Trunc(v) != v) ||
			(hasDays && i == 0 && v >= 24) {
			return 0, invalid
		}
		if i == 0 {
			if len(parts) == 3 {
				cpu += v * 3600
			} else {
				cpu += v * 60
			}
		} else if i == len(parts)-1 {
			cpu += v
		} else {
			cpu += v * 60
		}
	}
	if math.IsInf(cpu, 0) {
		return 0, invalid
	}
	return cpu, nil
}

// Artifact failures latch through the run's health failure count, even after
// the original files return. A revision string alone does not bind installed bytes.
func (s *observation) verifyArtifacts() error {
	actual, err := digest(s.Binary)
	if err != nil || actual != s.BinaryDigest {
		return errors.New("installed binary identity changed")
	}
	app, err := readAppIdentity(s.AppPath)
	if err != nil || !app.Exists || app != s.AppIdentity {
		return errors.New("installed app identity changed")
	}
	return nil
}

type appObservationWindow struct {
	started           time.Time
	firstCPU, lastCPU float64
}

func (s *observation) recordAppSample(now time.Time, pid int, cpu, rss float64, w *appObservationWindow) {
	if pid == s.AppPID && !w.started.IsZero() && (cpu < w.lastCPU || !now.After(w.started)) {
		s.recordAppSampleFailure(now, errors.New("app_sample_counter_regressed"))
		return
	}
	if pid != s.AppPID {
		if s.AppPID != 0 {
			s.AppRestarts++
		}
		s.AppPID = pid
		*w = appObservationWindow{started: now, firstCPU: cpu, lastCPU: cpu}
		s.AppPeakRSSMiB = 0
		s.AppCPUPercent = 0
	}
	s.AppSampleOK = true
	w.lastCPU = cpu
	s.AppObservedSeconds = now.Sub(w.started).Seconds()
	s.AppPeakRSSMiB = max(s.AppPeakRSSMiB, rss)
	if s.AppObservedSeconds > 0 {
		s.AppCPUPercent = 100 * (cpu - w.firstCPU) / s.AppObservedSeconds
	}
	s.AppBudgetPassed = s.ContinuityPassed && s.AppObservedSeconds >= 1800 && s.AppCPUPercent >= 0 && s.AppCPUPercent <= 1 && s.AppPeakRSSMiB <= 128
}

func (s *observation) updateAcceptance(now time.Time) {
	elapsed := time.Duration(0)
	if !s.MonitoringStarted.IsZero() {
		elapsed = now.Sub(s.MonitoringStarted)
	}
	s.Observed24H = elapsed >= 24*time.Hour && s.ContinuityPassed
	s.Observed72H = elapsed >= 72*time.Hour && s.ContinuityPassed
	ready := s.HealthPassed && s.Failures == 0 && s.Checks > 0 && s.ContinuityPassed && s.ResidentRestarts == 0 && s.AppSampleOK && s.AppSampleFailures == 0 && s.AppRestarts == 0 && s.AppBudgetPassed && s.BinaryDigest != "" && s.AppIdentity.Exists
	for _, name := range []string{"status", "board", "rich"} {
		g, ok := s.Reads[name]
		ready = ready && ok && g.Passed && g.Failures == 0 && g.Samples >= 100
	}
	s.Accepted24H = s.Observed24H && ready
	s.Accepted72H = s.Observed72H && ready
}
