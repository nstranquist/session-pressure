package gaterunner

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/nstranquist/session-pressure/runnerfleet"
)

func TestFleetUsesInstalledInventoryWithoutProbing(t *testing.T) {
	home := t.TempDir()
	if got := readFleet(context.Background(), home, false); got != nil {
		t.Fatalf("missing default inventory: %+v", got)
	}
	path := filepath.Join(home, "custom.json")
	cfg := runnerfleet.Config{Schema: runnerfleet.Schema, Generation: "test-v1", Hosts: []runnerfleet.Host{
		{Name: "gaterunner-2", User: "builder", State: "active", Provider: "hetzner-dedicated", Platform: "linux/amd64", SourceCommit: "0123456789012345678901234567890123456789", Weight: 4, HeavySlots: 4, LightSlots: 2, MaxShards: 4, Repositories: []string{"session-pressure-work"}, Profiles: []string{"linux-native"}},
		{Name: "gaterunner-1", User: "reserve", State: "reserve", Provider: "hetzner-cloud", Platform: "linux/arm64", SourceCommit: "0123456789012345678901234567890123456789", HeavySlots: 1, LightSlots: 1, MaxShards: 1, Repositories: []string{"session-pressure-work"}, Profiles: []string{"linux-native"}},
	}}
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	got := readFleet(WithFleet(context.Background(), path), home, false)
	if got == nil || got.Error != "" || got.Generation != cfg.Generation || len(got.Hosts) != 2 || len(got.Digest) != 64 || got.Observations != nil {
		t.Fatalf("fleet=%+v", got)
	}
	if got.Hosts[1].State != "reserve" || got.Hosts[1].User != "reserve" {
		t.Fatalf("reserve endpoint lost: %+v", got.Hosts[1])
	}
	if err := os.WriteFile(path, []byte("invalid"), 0600); err != nil {
		t.Fatal(err)
	}
	got = readFleet(WithFleet(context.Background(), path), home, false)
	if got == nil || got.Error != "runner_fleet_inventory_invalid" || len(got.Hosts) != 0 {
		t.Fatalf("invalid inventory=%+v", got)
	}
}
