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
	if len(args) > 0 && (args[0] == "inspect" || args[0] == "job-log") {
		return cmdSessionPressureGaterunner(g, args)
	}
	dir, err := sessionpressure.DataDir()
	if err != nil {
		return sessionPressureError(err.Error(), 1)
	}
	if len(args) > 1 {
		return sessionPressureError("remote accepts status, enable, or disable", 2)
	}
	if len(args) == 1 && args[0] != "status" {
		switch args[0] {
		case "enable":
			err = sessionpressure.SetRemoteWork(dir, true)
		case "disable":
			err = sessionpressure.SetRemoteWork(dir, false)
		default:
			return sessionPressureError("remote accepts status, enable, or disable", 2)
		}
	}
	if err != nil {
		return sessionPressureError(err.Error(), 1)
	}
	enabled, err := sessionpressure.RemoteWorkEnabled(dir)
	if err != nil {
		return sessionPressureError(err.Error(), 1)
	}
	return emitPressure(g, map[string]any{"ok": true, "remote": map[string]any{"enabled": enabled, "host": "gaterunner-1", "parallelism": 2}}, fmt.Sprintf("Automatic Hetzner Go routing: %t\n", enabled), 0)
}
