package sessionpressurecmd

import (
	"context"
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/nstranquist/session-pressure/internal/sessionpressure"
)

func cmdSessionPressureArtifact(g *Flags, args []string) int {
	dir, err := sessionpressure.DataDir()
	if err != nil {
		return sessionPressureError(err.Error(), 1)
	}
	subcommand := "status"
	if len(args) > 0 {
		subcommand = args[0]
		args = args[1:]
	}
	switch subcommand {
	case "status", "plan":
		if len(args) != 0 {
			return sessionPressureError("artifact "+subcommand+" accepts no arguments", 2)
		}
		artifact, found, loadErr := sessionpressure.LoadInstalledArtifact(dir)
		if loadErr != nil {
			return sessionPressureError(loadErr.Error(), 1)
		}
		report, planErr := sessionpressure.PruneArtifacts(context.Background(), dir, false)
		if planErr != nil {
			return sessionPressureError(planErr.Error(), 1)
		}
		drift := sessionpressure.ArtifactDrift{Detail: "promotion candidate unresolved"}
		if manager, managerErr := sessionpressure.NewLaunchdManager("", dir); managerErr == nil {
			drift = sessionpressure.InspectArtifactDrift(manager.Binary, dir)
		}
		payload := map[string]any{"ok": true, "action": "artifact." + subcommand, "artifact_found": found, "artifact": artifact, "retention": report, "drift": drift}
		text := fmt.Sprintf("artifact: active=%t revisions=%d prune=%d reclaim=%d bytes\n", found, len(report.Entries), report.PruneCount, report.ReclaimBytes)
		if drift.Drifted {
			text += "artifact DRIFT: a newer build is not promoted; coordinated work still runs the installed revision.\n" +
				"  promote with: ndev session pressure monitor install\n"
		}
		return emitPressure(g, payload, text, 0)
	case "prune":
		flags := flag.NewFlagSet("session pressure artifact prune", flag.ContinueOnError)
		flags.SetOutput(io.Discard)
		apply := flags.Bool("apply", false, "remove revisions outside the active plus two rollback retention set")
		if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
			return sessionPressureError("usage: ndev session pressure artifact prune [--apply]", 2)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		report, pruneErr := sessionpressure.PruneArtifacts(ctx, dir, *apply)
		if pruneErr != nil {
			return sessionPressureError(pruneErr.Error(), 1)
		}
		payload := map[string]any{"ok": true, "action": "artifact.prune", "retention": report}
		return emitPressure(g, payload, fmt.Sprintf("artifact prune: applied=%t candidates=%d pruned=%d reclaim=%d reclaimed=%d bytes\n", report.Applied, report.PruneCount, report.PrunedCount, report.ReclaimBytes, report.ReclaimedBytes), 0)
	case "help", "--help", "-h":
		fmt.Print("Usage: ndev [--json] session pressure artifact status|plan|prune [--apply]\n")
		return 0
	default:
		return sessionPressureError("unknown artifact subcommand "+subcommand, 2)
	}
}
