package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
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
	Started             time.Time           `json:"started"`
	Updated             time.Time           `json:"updated"`
	Deadline            time.Time           `json:"deadline"`
	Binary              string              `json:"binary"`
	Identity            json.RawMessage     `json:"identity,omitempty"`
	Reads               map[string]readGate `json:"reads"`
	Checks              int                 `json:"checks"`
	Failures            int                 `json:"failures"`
	HealthPassed        bool                `json:"health_passed"`
	LastFailure         string              `json:"last_failure,omitempty"`
	ResidentPID         int                 `json:"resident_pid"`
	ResidentRestarts    int                 `json:"resident_restarts"`
	MaxSampleGapSeconds float64             `json:"max_sample_gap_seconds"`
	ContinuityPassed    bool                `json:"continuity_passed"`
	Observed24H         bool                `json:"observed_24h"`
	Observed72H         bool                `json:"observed_72h"`
	AppPID              int                 `json:"app_pid"`
	AppObservedSeconds  float64             `json:"app_observed_seconds"`
	AppCPUPercent       float64             `json:"app_cpu_percent"`
	AppPeakRSSMiB       float64             `json:"app_peak_rss_mib"`
	AppBudgetPassed     bool                `json:"app_30m_budget_passed"`
}

func readCLI(binary string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	prefix := []string{"--json"}
	if filepath.Base(binary) == "ndev" {
		prefix = append(prefix, "session", "pressure")
	}
	c := exec.CommandContext(ctx, binary, append(prefix, args...)...)
	// Read-only endpoints must remain within the same 2 MiB client contract.
	p, e := c.StdoutPipe()
	if e != nil {
		return nil, e
	}
	c.Stderr = io.Discard
	if e = c.Start(); e != nil {
		return nil, e
	}
	raw, e := io.ReadAll(io.LimitReader(p, 2*1024*1024+1))
	if e != nil || len(raw) > 2*1024*1024 {
		_ = c.Process.Kill()
		_ = c.Wait()
		return nil, errors.New("bounded read failed")
	}
	if e = c.Wait(); e != nil {
		return nil, e
	}
	var envelope struct {
		OK bool `json:"ok"`
	}
	if e = json.Unmarshal(raw, &envelope); e != nil {
		return nil, e
	}
	if !envelope.OK {
		return nil, errors.New("read reported ok=false")
	}
	return raw, nil
}
func observe(args []string) error {
	f := flag.NewFlagSet("observe", flag.ContinueOnError)
	out := f.String("out", "", "absolute evidence JSON path")
	binary := f.String("binary", "", "installed product CLI")
	duration := f.Duration("duration", 72*time.Hour, "elapsed observation window")
	interval := f.Duration("interval", time.Minute, "health sample interval (at least 10s)")
	samples := f.Int("samples", 100, "warm reads per endpoint (100..1000)")
	if e := f.Parse(args); e != nil {
		return e
	}
	if !filepath.IsAbs(*out) || !filepath.IsAbs(*binary) || *duration <= 0 || *duration > 7*24*time.Hour || *interval < 10*time.Second || *samples < 100 || *samples > 1000 || f.NArg() != 0 {
		return errors.New("absolute --out/--binary, duration <=7d, interval >=10s, samples 100..1000 required")
	}
	if e := os.MkdirAll(filepath.Dir(*out), 0700); e != nil {
		return e
	}
	started := time.Now().UTC()
	s := observation{Started: started, Updated: started, Deadline: started.Add(*duration), Binary: *binary, Reads: map[string]readGate{}}
	// A launch agent can run again after login. Preserve the interrupted run's
	// evidence rather than silently replacing its failures and elapsed window.
	if e := startObservation(*out, s); e != nil {
		return e
	}
	identity, e := readCLI(*binary, "capabilities")
	if e != nil {
		return fmt.Errorf("identity: %w", e)
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
	lastSample := time.Now().UTC()
	s.ContinuityPassed = true
	var appStart time.Time
	var cpuStart float64
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
		if pid, cpu, rss, ok := appSample(); ok {
			if pid != s.AppPID {
				s.AppPID = pid
				appStart = now
				cpuStart = cpu
				s.AppPeakRSSMiB = 0
				s.AppBudgetPassed = false
			}
			s.AppObservedSeconds = now.Sub(appStart).Seconds()
			if rss > s.AppPeakRSSMiB {
				s.AppPeakRSSMiB = rss
			}
			if s.AppObservedSeconds > 0 {
				s.AppCPUPercent = 100 * (cpu - cpuStart) / s.AppObservedSeconds
			}
			s.AppBudgetPassed = s.ContinuityPassed && s.AppObservedSeconds >= 1800 && s.AppCPUPercent <= 1 && s.AppPeakRSSMiB <= 128
		} else {
			s.AppPID = 0
			s.AppObservedSeconds = 0
			s.AppBudgetPassed = false
		}
		elapsed := now.Sub(started)
		s.Observed24H = elapsed >= 24*time.Hour && s.ContinuityPassed
		s.Observed72H = elapsed >= 72*time.Hour && s.ContinuityPassed
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

func appSample() (int, float64, float64, bool) {
	raw, e := exec.Command("pgrep", "-x", "NDevPressure").Output()
	if e != nil {
		return 0, 0, 0, false
	}
	ids := strings.Fields(string(raw))
	if len(ids) != 1 {
		return 0, 0, 0, false
	}
	pid, e := strconv.Atoi(ids[0])
	if e != nil {
		return 0, 0, 0, false
	}
	raw, e = exec.Command("ps", "-p", ids[0], "-o", "time=", "-o", "rss=").Output()
	fields := strings.Fields(string(raw))
	if e != nil || len(fields) != 2 {
		return 0, 0, 0, false
	}
	var cpu float64
	for _, part := range strings.Split(fields[0], ":") {
		v, e := strconv.ParseFloat(part, 64)
		if e != nil {
			return 0, 0, 0, false
		}
		cpu = cpu*60 + v
	}
	rss, e := strconv.ParseFloat(fields[1], 64)
	return pid, cpu, rss / 1024, e == nil
}
