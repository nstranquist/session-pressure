package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/nstranquist/session-pressure/internal/gaterunner"
	"github.com/nstranquist/session-pressure/runnerfleet"
	"github.com/nstranquist/session-pressure/sessionpressure"
)

// recordProbes keeps each probe's SSH time in the session-pressure probe
// history, so a slow login can later be matched with the runner's
// host_samples. A failed write only warns: it never changes the result.
var recordProbes = func(observations []runnerfleet.Observation) {
	dir, err := sessionpressure.DataDir()
	if err == nil {
		err = gaterunner.RecordProbes(dir, observations)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "gaterunner-fleet: probe history not recorded:", err)
	}
}

// recordingProbe wraps runnerfleet.Probe and keeps every observation.
type recordingProbe struct {
	mu   sync.Mutex
	seen []runnerfleet.Observation
}

func (r *recordingProbe) probe(ctx context.Context, h runnerfleet.Host) runnerfleet.Observation {
	o := runnerfleet.Probe(ctx, h)
	r.mu.Lock()
	r.seen = append(r.seen, o)
	r.mu.Unlock()
	return o
}

func run(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: gaterunner-fleet status|validate|select --config PATH")
	}
	fs := flag.NewFlagSet(args[0], flag.ContinueOnError)
	path := fs.String("config", runnerfleet.DefaultPath(), "versioned fleet inventory")
	key := fs.String("key", "", "durable request key")
	identity := fs.String("identity", "", "exact candidate/request identity")
	repo := fs.String("repo", "", "runner clone name")
	profile := fs.String("profile", "linux-native", "required execution profile")
	lane := fs.String("lane", "heavy", "heavy or light")
	format := fs.String("format", "json", "json or endpoint")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected positional arguments")
	}
	// Callers (the commit-delivery worker) give this command 30s. Finish first,
	// so a slow probe yields a result or a clear refusal, not a killed process.
	ctx, cancel := context.WithTimeout(context.Background(), 27*time.Second)
	defer cancel()
	switch args[0] {
	case "select":
		rec := &recordingProbe{}
		b, err := runnerfleet.Assign(ctx, *path, runnerfleet.Request{Key: *key, Identity: *identity, Repository: *repo, Profile: *profile, Lane: *lane}, rec.probe)
		recordProbes(rec.seen)
		if err != nil {
			return err
		}
		if *format == "endpoint" {
			fmt.Println(b.Host.User + "@" + b.Host.Name)
			return nil
		}
		if *format != "json" {
			return fmt.Errorf("unsupported format")
		}
		return json.NewEncoder(os.Stdout).Encode(b)
	case "status", "validate":
		c, d, err := runnerfleet.Load(*path)
		if err != nil {
			return err
		}
		var observations []runnerfleet.Observation
		if args[0] == "status" {
			observations = runnerfleet.Observe(ctx, c, nil)
			recordProbes(observations)
		}
		if err = json.NewEncoder(os.Stdout).Encode(map[string]any{"config": c, "config_digest": d, "observations": observations}); err != nil {
			return err
		}
		// Each observation carries transport (ok, slow, unreachable) and
		// probe_ms apart from healthy. Stdout stays one JSON document:
		// gaterunner-upgrade records the combined output as fleet-status.json.
		for _, o := range observations {
			if o.Host.State == "active" && o.Transport == runnerfleet.TransportUnreachable {
				return fmt.Errorf("active runner %s is unreachable (transport, %dms): %s", o.Host.Name, o.ProbeMS, o.Error)
			}
			if o.Host.State == "active" && (!o.Healthy || o.Drained) {
				return fmt.Errorf("active runner %s is unavailable: %s", o.Host.Name, o.Error)
			}
		}
		return nil
	default:
		return fmt.Errorf("unknown fleet command %q", args[0])
	}
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "gaterunner-fleet:", err)
		os.Exit(1)
	}
}
