package runnerfleet

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func fixture(t *testing.T) (string, Config) {
	t.Helper()
	c := Config{Schema: Schema, Generation: "test-1", Hosts: []Host{}}
	for _, name := range []string{"runner-2", "runner-3", "runner-1"} {
		c.Hosts = append(c.Hosts, Host{Name: name, User: "builder", State: "active", Provider: "other", Platform: "linux/amd64", SourceCommit: strings.Repeat("a", 40), Weight: 1, HeavySlots: 2, LightSlots: 1, Repositories: []string{"repo"}, Profiles: []string{"linux-native"}})
	}
	c.Hosts[2].State = "reserve"
	path := filepath.Join(t.TempDir(), "fleet.json")
	writeConfig(t, path, c)
	return path, c
}
func writeConfig(t *testing.T, path string, c Config) {
	t.Helper()
	body, _ := json.Marshal(c)
	if err := os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
}
func healthy(_ context.Context, h Host) Observation {
	return Observation{Host: h, Healthy: true, HeavySlots: h.HeavySlots, LightSlots: h.LightSlots}
}
func req(key string) Request {
	return Request{Key: key, Identity: "candidate-identity", Repository: "repo", Profile: "linux-native", Lane: "heavy"}
}

func TestConcurrentAssignmentsSurviveInventoryAndHostLoss(t *testing.T) {
	path, _ := fixture(t)
	const n = 32
	results := make(chan Binding, n)
	errs := make(chan error, n)
	var wg sync.WaitGroup
	for range n {
		wg.Go(func() {
			b, err := Assign(context.Background(), path, req("same-request"), healthy)
			results <- b
			errs <- err
		})
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	host := ""
	for b := range results {
		if host == "" {
			host = b.Host.Name
		}
		if b.Host.Name != host {
			t.Fatal("concurrent request migrated", b)
		}
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	b, err := Assign(context.Background(), path, req("same-request"), func(context.Context, Host) Observation {
		t.Fatal("reconnect must not probe or reselect")
		return Observation{}
	})
	if err != nil || b.Host.Name != host {
		t.Fatal(b, err)
	}
	r := req("same-request")
	r.Identity = "different-candidate"
	if _, err := Assign(context.Background(), path, r, healthy); err == nil {
		t.Fatal("reused key accepted a new identity")
	}
}

func TestFleetDistributionAndExclusions(t *testing.T) {
	path, c := fixture(t)
	counts := map[string]int{}
	for i := range 200 {
		b, err := Assign(context.Background(), path, req(fmt.Sprint("distribution-", i)), healthy)
		if err != nil {
			t.Fatal(err)
		}
		counts[b.Host.Name]++
	}
	if counts["runner-1"] != 0 || counts["runner-2"] < 60 || counts["runner-3"] < 60 {
		t.Fatal("fleet distribution or reserve exclusion", counts)
	}
	c.Hosts[0].State = "draining"
	writeConfig(t, path, c)
	b, err := Assign(context.Background(), path, req("new-after-drain"), healthy)
	if err != nil || b.Host.Name != "runner-3" {
		t.Fatal(b, err)
	}
	for _, r := range []Request{{Key: "unsupported-profile", Identity: "x", Repository: "repo", Profile: "darwin-arm64-cgo", Lane: "heavy"}, {Key: "unsupported-repo", Identity: "x", Repository: "unknown", Profile: "linux-native", Lane: "heavy"}} {
		if _, err := Assign(context.Background(), path, r, healthy); err == nil {
			t.Fatal("unsupported work assigned", r)
		}
	}
	_, err = Assign(context.Background(), path, req("unhealthy"), func(_ context.Context, h Host) Observation { return Observation{Host: h, Error: "unreachable"} })
	if err == nil {
		t.Fatal("unhealthy host admitted")
	}
	_, err = Assign(context.Background(), path, req("drained"), func(ctx context.Context, h Host) Observation { o := healthy(ctx, h); o.Drained = true; return o })
	if err == nil {
		t.Fatal("live-drained host admitted")
	}
}

func TestInvalidInventoryFailsClosed(t *testing.T) {
	path, c := fixture(t)
	for _, mutate := range []func(*Config){func(c *Config) { c.Hosts[1].Name = c.Hosts[0].Name }, func(c *Config) { c.Hosts[0].Name = "-oProxyCommand=x" }, func(c *Config) { c.Hosts[0].Weight = 0 }, func(c *Config) { c.Hosts[0].SourceCommit = "unknown" }, func(c *Config) { c.Hosts[0].State = "typo" }} {
		copyC := c
		copyC.Hosts = append([]Host(nil), c.Hosts...)
		mutate(&copyC)
		writeConfig(t, path, copyC)
		if _, err := Assign(context.Background(), path, req("invalid"), healthy); err == nil {
			t.Fatal("invalid inventory accepted", copyC)
		}
	}
	for _, body := range []string{`{"schema":"nicos.gaterunner-fleet.v1","generation":"x","hosts":[],"typo":true}`, `{} {}`, strings.Repeat("x", (1<<20)+1)} {
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		if _, _, err := Load(path); err == nil {
			t.Fatal("invalid JSON accepted")
		}
	}
}

func TestQueuePressureAndWeightsInfluenceNewAssignments(t *testing.T) {
	path, c := fixture(t)
	c.Hosts[0].Weight = 4
	writeConfig(t, path, c)
	counts := map[string]int{}
	for i := range 200 {
		b, err := Assign(context.Background(), path, req(fmt.Sprint("weighted-", i)), healthy)
		if err != nil {
			t.Fatal(err)
		}
		counts[b.Host.Name]++
	}
	if counts["runner-2"] < 130 || counts["runner-3"] < 15 {
		t.Fatal(counts)
	}
	counts = map[string]int{}
	probe := func(ctx context.Context, h Host) Observation {
		o := healthy(ctx, h)
		if h.Name == "runner-2" {
			o.HeavyJobs = 100
		}
		return o
	}
	for i := range 100 {
		b, err := Assign(context.Background(), path, req(fmt.Sprint("loaded-", i)), probe)
		if err != nil {
			t.Fatal(err)
		}
		counts[b.Host.Name]++
	}
	if counts["runner-3"] < 80 {
		t.Fatal("queue penalty ineffective", counts)
	}
}
