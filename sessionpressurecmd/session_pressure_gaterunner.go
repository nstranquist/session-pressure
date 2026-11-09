package sessionpressurecmd

import (
	"context"
	"encoding/hex"
	"fmt"
	"os"
	"time"

	"github.com/nstranquist/session-pressure/internal/gaterunner"
	"github.com/nstranquist/session-pressure/sessionpressure"
)

func cmdSessionPressureGaterunner(g *Flags, args []string) int {
	if len(args) == 0 {
		return sessionPressureError("remote inspection requires inspect or job-log", 2)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 14*time.Second)
	defer cancel()
	dir, err := sessionpressure.DataDir()
	if err != nil {
		return sessionPressureError(err.Error(), 1)
	}
	policy, err := sessionpressure.RemoteWorkConfig(dir)
	if err != nil {
		return sessionPressureError(err.Error(), 1)
	}
	// An explicit endpoint permits reading retained jobs after primary cutover.
	filtered := []string{args[0]}
	for i := 1; i < len(args); i++ {
		if args[i] == "--host" || args[i] == "--user" {
			if i+1 >= len(args) {
				return sessionPressureError("--host and --user require values", 2)
			}
			if args[i] == "--host" {
				policy.Host = args[i+1]
			} else {
				policy.User = args[i+1]
			}
			i++
		} else {
			filtered = append(filtered, args[i])
		}
	}
	args = filtered
	ctx, err = gaterunner.WithTarget(ctx, policy.Host, policy.User)
	if err != nil {
		return sessionPressureError(err.Error(), 1)
	}
	switch args[0] {
	case "inspect":
		box := true
		known := ""
		for i := 1; i < len(args); i++ {
			switch args[i] {
			case "--no-box":
				box = false
			case "--known-ledger":
				i++
				if i >= len(args) || len(args[i]) != 64 {
					return sessionPressureError("remote inspect requires a 64-character ledger signature", 2)
				}
				if _, e := hex.DecodeString(args[i]); e != nil {
					return sessionPressureError("invalid ledger signature", 2)
				}
				known = args[i]
			default:
				return sessionPressureError("remote inspect accepts --no-box and --known-ledger SHA256", 2)
			}
		}
		home, e := os.UserHomeDir()
		if e != nil {
			return sessionPressureError(e.Error(), 1)
		}
		report := gaterunner.InspectSince(ctx, home, box, known)
		return emitPressure(g, map[string]any{"ok": true, "action": "remote.inspect", "inspection": report}, fmt.Sprintf("Gate runner: reachable=%t runs=%d jobs=%d local_error=%s box_error=%s\n", report.BoxReachable, len(report.Runs), len(report.Jobs), report.LocalError, report.BoxError), 0)
	case "job-log":
		if len(args) != 3 || args[1] != "--id" {
			return sessionPressureError("remote job-log requires --id JOB", 2)
		}
		log, e := gaterunner.JobLog(ctx, args[2])
		if e != nil {
			return sessionPressureError(e.Error(), 1)
		}
		return emitPressure(g, map[string]any{"ok": true, "action": "remote.job-log", "log": log}, log, 0)
	default:
		return sessionPressureError("remote inspection requires inspect or job-log", 2)
	}
}
