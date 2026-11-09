// Package runnerfleet assigns a request to one eligible runner before admission.
// The assignment is durable: a reconnect never selects another host, including
// when the original host is drained, unhealthy, or removed from the inventory.
package runnerfleet

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

const Schema = "nicos.gaterunner-fleet.v1"

type Host struct {
	Name         string   `json:"name"`
	User         string   `json:"user"`
	State        string   `json:"state"`    // active, staging, draining, reserve, disabled
	Provider     string   `json:"provider"` // hetzner-cloud, hetzner-dedicated, other
	Platform     string   `json:"platform"`
	SourceCommit string   `json:"source_commit"`
	Weight       int      `json:"weight"`
	HeavySlots   int      `json:"heavy_slots"`
	LightSlots   int      `json:"light_slots"`
	MaxShards    int      `json:"max_shards"`
	Repositories []string `json:"repositories"`
	Profiles     []string `json:"profiles"`
}

type Config struct {
	Schema     string `json:"schema"`
	Generation string `json:"generation"`
	Hosts      []Host `json:"hosts"`
}

var namePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9.-]{0,252}$`)
var userPattern = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_-]{0,63}$`)
var commitPattern = regexp.MustCompile(`^[a-f0-9]{40}$`)

func DefaultPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".nicos-dev", "gaterunner-fleet", "v1", "fleet.json")
}

func strictJSON(body []byte, value any) error {
	d := json.NewDecoder(bytes.NewReader(body))
	d.DisallowUnknownFields()
	if err := d.Decode(value); err != nil {
		return err
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return errors.New("trailing JSON data")
	}
	return nil
}

func Load(path string) (Config, string, error) {
	var c Config
	f, err := os.Open(path)
	if err != nil {
		return c, "", err
	}
	defer f.Close()
	body, err := io.ReadAll(io.LimitReader(f, 1<<20+1))
	if err != nil {
		return c, "", err
	}
	if len(body) > 1<<20 {
		return c, "", errors.New("fleet inventory exceeds 1 MiB")
	}
	if err = strictJSON(body, &c); err != nil {
		return c, "", err
	}
	if err = c.Validate(); err != nil {
		return c, "", err
	}
	sum := sha256.Sum256(body)
	return c, hex.EncodeToString(sum[:]), nil
}

func (c Config) Validate() error {
	if c.Schema != Schema || strings.TrimSpace(c.Generation) == "" || len(c.Hosts) == 0 || len(c.Hosts) > 64 {
		return errors.New("invalid fleet schema, generation, or host count")
	}
	seen := map[string]bool{}
	for _, h := range c.Hosts {
		if !namePattern.MatchString(h.Name) || !userPattern.MatchString(h.User) || seen[h.Name] {
			return fmt.Errorf("invalid or duplicate fleet endpoint %q", h.Name)
		}
		seen[h.Name] = true
		switch h.State {
		case "active", "staging", "draining", "reserve", "disabled":
		default:
			return fmt.Errorf("invalid state for %s", h.Name)
		}
		switch h.Provider {
		case "hetzner-cloud", "hetzner-dedicated", "other":
		default:
			return fmt.Errorf("invalid provider for %s", h.Name)
		}
		if h.Platform != "linux/amd64" && h.Platform != "linux/arm64" {
			return fmt.Errorf("unsupported runner platform for %s", h.Name)
		}
		if !commitPattern.MatchString(h.SourceCommit) || h.Weight < 0 || h.Weight > 10000 || h.HeavySlots < 0 || h.LightSlots < 0 || h.MaxShards < 0 || h.MaxShards > 128 {
			return fmt.Errorf("invalid identity or limits for %s", h.Name)
		}
		if h.State == "active" && (h.Weight == 0 || h.HeavySlots+h.LightSlots == 0 || len(h.Repositories) == 0 || len(h.Profiles) == 0) {
			return fmt.Errorf("active runner %s lacks weight, capacity, or capabilities", h.Name)
		}
		for _, list := range [][]string{h.Repositories, h.Profiles} {
			for _, s := range list {
				if !namePattern.MatchString(s) {
					return fmt.Errorf("invalid capability %q", s)
				}
			}
		}
	}
	return nil
}

type Observation struct {
	Host         Host      `json:"host"`
	ObservedAt   time.Time `json:"observed_at"`
	Healthy      bool      `json:"healthy"`
	Drained      bool      `json:"drained"`
	Protocol     int       `json:"protocol"`
	SourceCommit string    `json:"source_commit"`
	Platform     string    `json:"platform"`
	HeavySlots   int       `json:"heavy_slots"`
	LightSlots   int       `json:"light_slots"`
	HeavyJobs    int       `json:"heavy_jobs"`
	LightJobs    int       `json:"light_jobs"`
	Error        string    `json:"error,omitempty"`
}

// ProbeFunc permits deterministic selection tests without a live SSH daemon.
type ProbeFunc func(context.Context, Host) Observation

type cappedBuffer struct{ bytes.Buffer }

func (b *cappedBuffer) Write(p []byte) (int, error) {
	if b.Len()+len(p) > 2<<20 {
		return 0, errors.New("fleet probe exceeds 2 MiB")
	}
	return b.Buffer.Write(p)
}

func Probe(ctx context.Context, h Host) (o Observation) {
	o.Host, o.ObservedAt = h, time.Now().UTC()
	ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	// Only validated endpoints enter argv. Strict host-key checking is inherited.
	if !namePattern.MatchString(h.Name) || !userPattern.MatchString(h.User) {
		o.Error = "invalid endpoint"
		return
	}
	cmd := exec.CommandContext(ctx, "ssh", "-o", "BatchMode=yes", "-o", "ConnectTimeout=5", h.User+"@"+h.Name,
		"/srv/build/gaterunner health && /srv/build/gaterunner status && uname -sm")
	var out cappedBuffer
	cmd.Stdout = &out
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		o.Error = "SSH health probe: " + err.Error()
		return
	}
	d := json.NewDecoder(&out)
	var health struct {
		Healthy      bool   `json:"healthy"`
		Protocol     int    `json:"protocol"`
		SourceCommit string `json:"source_commit"`
	}
	var status struct {
		Drained    bool `json:"drained"`
		Protocol   int  `json:"protocol"`
		HeavySlots int  `json:"heavy_slots"`
		LightSlots int  `json:"light_slots"`
		Jobs       []struct {
			State string `json:"state"`
			Class string `json:"class"`
		} `json:"jobs"`
	}
	if err := d.Decode(&health); err != nil {
		o.Error = "invalid health response"
		return
	}
	if err := d.Decode(&status); err != nil {
		o.Error = "invalid status response"
		return
	}
	rest, _ := io.ReadAll(io.MultiReader(d.Buffered(), &out))
	switch strings.TrimSpace(string(rest)) {
	case "Linux x86_64":
		o.Platform = "linux/amd64"
	case "Linux aarch64":
		o.Platform = "linux/arm64"
	default:
		o.Error = "unsupported live platform"
		return
	}
	o.Protocol, o.SourceCommit, o.Drained = health.Protocol, health.SourceCommit, status.Drained
	o.HeavySlots, o.LightSlots = status.HeavySlots, status.LightSlots
	for _, j := range status.Jobs {
		switch j.State {
		case "queued", "preparing", "running":
			if j.Class == "light" {
				o.LightJobs++
			} else {
				o.HeavyJobs++
			}
		}
	}
	switch {
	case !health.Healthy || health.Protocol != 1 || status.Protocol != 1:
		o.Error = "unhealthy scheduler or incompatible protocol"
	case health.SourceCommit != h.SourceCommit:
		o.Error = "scheduler source differs from inventory"
	case o.Platform != h.Platform:
		o.Error = "platform differs from inventory"
	case status.HeavySlots != h.HeavySlots || status.LightSlots != h.LightSlots:
		o.Error = "scheduler capacity differs from inventory"
	default:
		o.Healthy = true
	}
	return
}

func Observe(ctx context.Context, c Config, probe ProbeFunc) []Observation {
	if probe == nil {
		probe = Probe
	}
	observations := make([]Observation, len(c.Hosts))
	var wg sync.WaitGroup
	// A bounded fan-out avoids serial timeout amplification on a larger fleet.
	sem := make(chan struct{}, 8)
	for i, h := range c.Hosts {
		wg.Add(1)
		go func(i int, h Host) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
				observations[i] = probe(ctx, h)
			case <-ctx.Done():
				observations[i] = Observation{Host: h, Error: ctx.Err().Error()}
			}
		}(i, h)
	}
	wg.Wait()
	return observations
}

type Request struct {
	Key        string `json:"key"`
	Identity   string `json:"-"` // hashed before persistence; never stores argv or secrets
	Repository string `json:"repository"`
	Profile    string `json:"profile"`
	Lane       string `json:"lane"`
}

type Binding struct {
	Schema         string    `json:"schema"`
	Key            string    `json:"key"`
	IdentityDigest string    `json:"identity_digest"`
	Repository     string    `json:"repository"`
	Profile        string    `json:"profile"`
	Lane           string    `json:"lane"`
	Host           Host      `json:"host"`
	Generation     string    `json:"generation"`
	ConfigDigest   string    `json:"config_digest"`
	AssignedAt     time.Time `json:"assigned_at"`
}

func digest(s string) string { sum := sha256.Sum256([]byte(s)); return hex.EncodeToString(sum[:]) }
func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

func readBinding(path string, r Request) (Binding, error) {
	var b Binding
	f, err := os.Open(path)
	if err != nil {
		return b, err
	}
	defer f.Close()
	body, err := io.ReadAll(io.LimitReader(f, (1<<20)+1))
	if err != nil {
		return b, err
	}
	if len(body) > 1<<20 {
		return b, errors.New("oversized fleet binding")
	}
	if err = strictJSON(body, &b); err != nil {
		return b, err
	}
	if b.Schema != Schema || b.Key != r.Key || b.IdentityDigest != digest(r.Identity) || b.Repository != r.Repository || b.Profile != r.Profile || b.Lane != r.Lane {
		return b, errors.New("fleet request key already bound to a different identity")
	}
	if !namePattern.MatchString(b.Host.Name) || !userPattern.MatchString(b.Host.User) {
		return b, errors.New("invalid bound endpoint")
	}
	return b, nil
}

// Assign reads an existing binding BEFORE loading inventory or probing hosts.
// Only a genuinely new request may select an eligible healthy active host.
// Atomic link publication makes concurrent callers agree across processes.
func Assign(ctx context.Context, path string, r Request, probe ProbeFunc) (Binding, error) {
	var empty Binding
	if r.Key == "" || len(r.Key) > 1024 || r.Identity == "" || !namePattern.MatchString(r.Repository) || !namePattern.MatchString(r.Profile) || (r.Lane != "heavy" && r.Lane != "light") {
		return empty, errors.New("invalid fleet request")
	}
	dir := filepath.Join(filepath.Dir(path), "bindings")
	file := filepath.Join(dir, digest(r.Key)+".json")
	if b, err := readBinding(file, r); err == nil {
		return b, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return empty, err
	}
	c, configDigest, err := Load(path)
	if err != nil {
		return empty, fmt.Errorf("fleet inventory: %w", err)
	}
	eligible := Config{Schema: c.Schema, Generation: c.Generation}
	for _, h := range c.Hosts {
		if h.State == "active" && contains(h.Repositories, r.Repository) && contains(h.Profiles, r.Profile) && ((r.Lane == "heavy" && h.HeavySlots > 0) || (r.Lane == "light" && h.LightSlots > 0)) {
			eligible.Hosts = append(eligible.Hosts, h)
		}
	}
	observations := Observe(ctx, eligible, probe)
	best := math.Inf(1)
	var selected Host
	reasons := []string{}
	for i, o := range observations {
		h := eligible.Hosts[i]
		if !o.Healthy || o.Drained {
			reasons = append(reasons, h.Name+": "+o.Error+fmt.Sprintf(" drained=%t", o.Drained))
			continue
		}
		sum := sha256.Sum256([]byte(r.Key + "\x00" + h.Name))
		u := (float64(binary.BigEndian.Uint64(sum[:8])>>11) + 1) / (1<<53 + 1.0)
		slots, jobs := o.HeavySlots, o.HeavyJobs
		if r.Lane == "light" {
			slots, jobs = o.LightSlots, o.LightJobs
		}
		if slots <= 0 {
			continue
		}
		// Weighted rendezvous with a live queue penalty. This is advisory load
		// balancing; each host scheduler remains the sole admission authority.
		score := -math.Log(u) / float64(h.Weight) * (1 + float64(jobs)/float64(slots))
		if score < best {
			best, selected = score, h
		}
	}
	if selected.Name == "" {
		return empty, fmt.Errorf("no eligible healthy fleet runner for %s/%s/%s (%s); request remains unassigned", r.Repository, r.Profile, r.Lane, strings.Join(reasons, "; "))
	}
	b := Binding{Schema: Schema, Key: r.Key, IdentityDigest: digest(r.Identity), Repository: r.Repository, Profile: r.Profile, Lane: r.Lane, Host: selected, Generation: c.Generation, ConfigDigest: configDigest, AssignedAt: time.Now().UTC()}
	if err = os.MkdirAll(dir, 0700); err != nil {
		return empty, err
	}
	f, err := os.CreateTemp(dir, ".assignment-")
	if err != nil {
		return empty, err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	body, _ := json.MarshalIndent(b, "", "  ")
	body = append(body, '\n')
	_, err = f.Write(body)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return empty, err
	}
	if err = os.Link(tmp, file); err != nil {
		if errors.Is(err, os.ErrExist) {
			return readBinding(file, r)
		}
		return empty, err
	}
	df, err := os.Open(dir)
	if err != nil {
		return empty, err
	}
	err = df.Sync()
	_ = df.Close()
	if err != nil {
		return empty, err
	}
	return b, nil
}
