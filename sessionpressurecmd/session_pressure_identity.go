package sessionpressurecmd

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/nstranquist/session-pressure/sessionpressure"
)

const sessionPressureIdentityHelp = `Usage: session-pressure [--json] identity show [--live]

Show the agent process-identity catalog used to project Codex/Claude/Grok/Kimi
trees. Includes shipped rules, optional operator overlay
(~/.nicos-dev/session-pressure/agent-identity.json), install-root presence, tree
counts, privacy-safe agent-shaped process summary, and miss diagnostics.

Flags:
  --live   Sample a live process inventory (enriched with session-state + open-
           transcript ownership hints) instead of latest trees only
`

func cmdSessionPressureIdentity(g *Flags, args []string) int {
	if len(args) == 0 {
		return sessionPressureError("identity requires a subcommand; try: session-pressure identity show", 2)
	}
	switch args[0] {
	case "help", "--help", "-h":
		fmt.Print(sessionPressureIdentityHelp)
		return 0
	case "show":
		return cmdSessionPressureIdentityShow(g, args[1:])
	default:
		return sessionPressureError("unknown identity subcommand "+args[0]+"; try: show", 2)
	}
}

func cmdSessionPressureIdentityShow(g *Flags, args []string) int {
	live := false
	for len(args) > 0 {
		switch args[0] {
		case "--live":
			live = true
			args = args[1:]
		case "help", "--help", "-h":
			fmt.Print(sessionPressureIdentityHelp)
			return 0
		default:
			return sessionPressureError("unknown identity show flag "+args[0], 2)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	runtime, err := loadPressureRuntime(ctx)
	if err != nil {
		return sessionPressureError(err.Error(), 1)
	}
	sessionpressure.ResetAgentIdentityCatalogCache()
	catalog := sessionpressure.ActiveAgentIdentityCatalog()
	home, _ := os.UserHomeDir()

	var trees []sessionpressure.AgentTree
	var processes []sessionpressure.Process
	inventorySource := ""
	if live {
		sampleCtx, sampleCancel := context.WithTimeout(ctx, 5*time.Second)
		var invErr error
		// Empty sessionStateDir skips ownership-hint enrichment; still returns live process inventory.
		processes, trees, inventorySource, invErr = sessionpressure.CollectIdentityInventory(sampleCtx, "")
		sampleCancel()
		if invErr != nil {
			// Fall back to full snapshot trees if native inventory fails.
			sampleCtx2, cancel2 := context.WithTimeout(ctx, 5*time.Second)
			snapshot, sampleErr := SampleSnapshot(sampleCtx2, runtime.sampler, runtime.policy)
			cancel2()
			if sampleErr != nil {
				return sessionPressureError(invErr.Error()+"; snapshot fallback: "+sampleErr.Error(), 1)
			}
			trees = snapshot.TopAgentTrees
			inventorySource = "snapshot-fallback"
		}
	} else if latest, ok := runtime.store.ReadLatest(); ok {
		trees = latest.TopAgentTrees
		inventorySource = "resident-latest"
	}

	report := sessionpressure.BuildAgentIdentityReport(catalog, home, trees, processes)
	report.InventorySource = inventorySource
	payload := map[string]any{
		"ok":      report.OverlayError == "",
		"action":  "identity.show",
		"live":    live,
		"report":  report,
		"overlay": sessionpressure.AgentIdentityOverlayPath(runtime.dir),
	}
	var b strings.Builder
	fmt.Fprintf(&b, "identity agents=%s overlay_loaded=%v overlay_error=%q inventory=%s\n",
		strings.Join(report.Agents, ","), report.OverlayLoaded, report.OverlayError, inventorySource)
	if len(report.TreeCounts) > 0 {
		parts := make([]string, 0, len(report.TreeCounts))
		for agent, count := range report.TreeCounts {
			parts = append(parts, fmt.Sprintf("%s=%d", agent, count))
		}
		fmt.Fprintf(&b, "trees %s\n", strings.Join(parts, " "))
	}
	for _, row := range report.ProcessSummary {
		fmt.Fprintf(&b, "process agent=%s exec=%s count=%d labeled=%d unlabeled=%d with_session=%d\n",
			row.Agent, row.Executable, row.ProcessCount, row.LabeledCount, row.UnlabeledCount, row.WithSessionCount)
	}
	for _, miss := range report.Misses {
		fmt.Fprintf(&b, "miss agent=%s reason=%s detail=%s\n", miss.Agent, miss.Reason, miss.Detail)
	}
	if len(report.Misses) == 0 {
		fmt.Fprintln(&b, "misses none")
	}
	return emitPressure(g, payload, b.String(), 0)
}
