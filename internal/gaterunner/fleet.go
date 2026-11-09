package gaterunner

import (
	"context"
	"os"
	"path/filepath"

	"github.com/nstranquist/session-pressure/runnerfleet"
)

type FleetSection struct {
	Generation   string                    `json:"generation"`
	Digest       string                    `json:"digest"`
	Hosts        []runnerfleet.Host        `json:"hosts"`
	Observations []runnerfleet.Observation `json:"observations,omitempty"`
	Error        string                    `json:"error,omitempty"`
}

type fleetKey struct{}

// WithFleet uses the same inventory as remote work policy, without selecting
// or binding any new work. An empty path uses the installed default inventory.
func WithFleet(ctx context.Context, path string) context.Context {
	return context.WithValue(ctx, fleetKey{}, path)
}

func readFleet(ctx context.Context, home string, probe bool) *FleetSection {
	path, _ := ctx.Value(fleetKey{}).(string)
	explicit := path != ""
	if !explicit {
		path = filepath.Join(home, ".nicos-dev", "gaterunner-fleet", "v1", "fleet.json")
	}
	c, digest, err := runnerfleet.Load(path)
	if os.IsNotExist(err) && !explicit {
		return nil
	}
	if err != nil {
		return &FleetSection{Hosts: []runnerfleet.Host{}, Error: "runner_fleet_inventory_invalid"}
	}
	s := &FleetSection{Generation: c.Generation, Digest: digest, Hosts: c.Hosts}
	if probe {
		s.Observations = runnerfleet.Observe(ctx, c, nil)
	}
	return s
}
