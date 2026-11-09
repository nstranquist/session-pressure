package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/nstranquist/session-pressure/runnerfleet"
)

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
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	switch args[0] {
	case "select":
		b, err := runnerfleet.Assign(ctx, *path, runnerfleet.Request{Key: *key, Identity: *identity, Repository: *repo, Profile: *profile, Lane: *lane}, nil)
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
		}
		if err = json.NewEncoder(os.Stdout).Encode(map[string]any{"config": c, "config_digest": d, "observations": observations}); err != nil {
			return err
		}
		for _, o := range observations {
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
