package sessionpressurecmd

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/nstranquist/session-pressure/sessionpressure"
	"time"
)

func cmdSessionPressureBrowser(g *Flags, args []string) int {
	mode := ""
	if len(args) > 1 {
		return sessionPressureError("browser accepts status, pause, or auto", 2)
	}
	if len(args) == 1 {
		switch args[0] {
		case "status":
		case "pause":
			mode = "paused"
		case "auto":
			mode = "auto"
		default:
			return sessionPressureError("browser accepts status, pause, or auto", 2)
		}
	}
	dir, err := sessionpressure.DataDir()
	if err != nil {
		return sessionPressureError(err.Error(), 1)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	state, err := sessionpressure.BrowserControlStatus(ctx, dir, mode)
	if err != nil {
		return sessionPressureError(err.Error(), 1)
	}
	if mode != "" {
		if err := sessionpressure.EnforceBrowserControl(ctx, dir); err != nil {
			return sessionPressureError(err.Error(), 1)
		}
	}
	body, _ := json.MarshalIndent(state, "", "  ")
	return emitPressure(g, map[string]any{"ok": true, "browser": state}, fmt.Sprintln(string(body)), 0)
}

func cmdSessionPressureRemote(g *Flags, args []string) int {
	for _, arg := range args {
		if arg == "--help" || arg == "-h" || arg == "help" {
			return emitPressure(g, map[string]any{"ok": true}, "Usage: session-pressure remote status|enable [--host HOST] [--user USER]|disable\n       session-pressure remote inspect [--host HOST] [--user USER] [--no-box] [--known-ledger SHA256]\n       session-pressure remote job-log [--host HOST] [--user USER] --id JOB\n", 0)
		}
	}
	if len(args) > 0 && (args[0] == "inspect" || args[0] == "job-log") {
		return cmdSessionPressureGaterunner(g, args)
	}
	dir, err := sessionpressure.DataDir()
	if err != nil {
		return sessionPressureError(err.Error(), 1)
	}
	policy, err := sessionpressure.RemoteWorkConfig(dir)
	if err != nil {
		return sessionPressureError(err.Error(), 1)
	}
	if len(args) > 1 {
		if args[0] != "enable" {
			return sessionPressureError("remote enable accepts --host HOST and --user USER", 2)
		}
		for i := 1; i < len(args); i += 2 {
			if i+1 >= len(args) {
				return sessionPressureError("remote enable requires a value after --host or --user", 2)
			}
			switch args[i] {
			case "--host":
				policy.Host = args[i+1]
			case "--user":
				policy.User = args[i+1]
			default:
				return sessionPressureError("remote enable accepts --host HOST and --user USER", 2)
			}
		}
	}
	if len(args) > 0 && args[0] != "status" {
		switch args[0] {
		case "enable":
			err = sessionpressure.ConfigureRemoteWork(dir, true, policy.Host, policy.User)
		case "disable":
			err = sessionpressure.SetRemoteWork(dir, false)
		default:
			return sessionPressureError("remote accepts status, enable, or disable", 2)
		}
	}
	if err != nil {
		return sessionPressureError(err.Error(), 1)
	}
	policy, err = sessionpressure.RemoteWorkConfig(dir)
	if err != nil {
		return sessionPressureError(err.Error(), 1)
	}
	return emitPressure(g, map[string]any{"ok": true, "remote": map[string]any{"enabled": policy.Enabled, "host": policy.Host, "user": policy.User, "parallelism": 2}}, fmt.Sprintf("Automatic Hetzner Go routing: %t (%s@%s)\n", policy.Enabled, policy.User, policy.Host), 0)
}
