package sessionpressurecmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/nstranquist/session-pressure/sessionpressure"
)

// cmdSessionPressureMemoryLevels shows, checks or switches the memory-levels
// source. Memory levels live in memory-levels.json (embedded default, or a
// replacement in the data directory); see sessionpressure/memory_levels.go.
func cmdSessionPressureMemoryLevels(g *Flags, args []string) int {
	sub := "show"
	if len(args) > 0 {
		sub = args[0]
		args = args[1:]
	}
	dir, err := sessionpressure.DataDir()
	if err != nil {
		return sessionPressureError(err.Error(), 1)
	}
	switch sub {
	case "show":
		if len(args) != 0 {
			return sessionPressureError("policy memory-levels show accepts no arguments", 2)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		physicalMB, err := ReadPhysicalMemoryMB(ctx, sessionpressure.NewSampler())
		if err != nil {
			return sessionPressureError(err.Error(), 1)
		}
		levels, err := sessionpressure.ResolveHostMemoryLevels(dir, physicalMB)
		if err != nil {
			return sessionPressureError(err.Error(), 1)
		}
		mode := memoryLevelsMode(dir)
		payload := map[string]any{"ok": true, "action": "policy.memory_levels.show", "mode": mode, "levels": levels, "file": sessionpressure.MemoryLevelsPath(dir)}
		text := fmt.Sprintf("mode %s (policy.json memory_levels)\nsource %s\nhost %s, %.0f MB: %s\nfree %% warning/red/critical %g/%g/%g\nswap MB %g/%g/%g\ntree MB %g/%g/%g\nagent total MB %g/%g/%g\nRSS rungs escalate past warning only at free <= %g%%\nguard self RSS budget %g MB\n",
			mode, levels.Source, levels.Host, levels.PhysicalMemoryMB, levels.Rule,
			levels.FreePercent.Warning, levels.FreePercent.Red, levels.FreePercent.Critical,
			levels.SwapMB.Warning, levels.SwapMB.Red, levels.SwapMB.Critical,
			levels.TreeMB.Warning, levels.TreeMB.Red, levels.TreeMB.Critical,
			levels.AgentTotalMB.Warning, levels.AgentTotalMB.Red, levels.AgentTotalMB.Critical,
			levels.RSSEscalationFreePercent, levels.GuardMaxSelfRSSMB)
		if levels.TreeFootprintMB != nil && levels.AgentTotalFootprintMB != nil {
			text += fmt.Sprintf("tree footprint MB %g/%g/%g\nagent total footprint MB %g/%g/%g\n(footprint bands judge the tree and agent-total rungs where footprint is measured; RSS bands are the fallback)\n",
				levels.TreeFootprintMB.Warning, levels.TreeFootprintMB.Red, levels.TreeFootprintMB.Critical,
				levels.AgentTotalFootprintMB.Warning, levels.AgentTotalFootprintMB.Red, levels.AgentTotalFootprintMB.Critical)
		} else {
			text += "footprint bands: off (tree and agent-total rungs judge RSS)\n"
		}
		if mode != sessionpressure.MemoryLevelsCalibrated {
			text += "note: policy.json keeps its own memory thresholds until `policy memory-levels calibrated`\n"
		}
		return emitPressure(g, payload, text, 0)
	case "check":
		if len(args) > 1 {
			return sessionPressureError("policy memory-levels check accepts at most one FILE", 2)
		}
		var config sessionpressure.MemoryLevelsConfig
		source := ""
		if len(args) == 1 {
			source = args[0]
			body, err := os.ReadFile(source)
			if err != nil {
				return sessionPressureError(err.Error(), 1)
			}
			config, err = sessionpressure.ParseMemoryLevels(body)
			if err != nil {
				return emitPressure(g, map[string]any{"ok": false, "action": "policy.memory_levels.check", "file": source, "error": err.Error()}, "invalid "+source+": "+err.Error()+"\n", 1)
			}
		} else {
			var err error
			config, source, err = sessionpressure.LoadMemoryLevels(dir)
			if err != nil {
				return emitPressure(g, map[string]any{"ok": false, "action": "policy.memory_levels.check", "file": source, "error": err.Error()}, "invalid: "+err.Error()+"\n", 1)
			}
		}
		names := make([]string, 0, len(config.Anchors))
		for _, anchor := range config.Anchors {
			names = append(names, fmt.Sprintf("%s (%g GiB)", anchor.Name, anchor.MemoryGiB))
		}
		return emitPressure(g, map[string]any{"ok": true, "action": "policy.memory_levels.check", "file": source, "anchors": len(config.Anchors), "hosts": len(config.Hosts)},
			fmt.Sprintf("valid %s: anchors %s; host overrides %d\n", source, strings.Join(names, ", "), len(config.Hosts)), 0)
	case sessionpressure.MemoryLevelsCalibrated, sessionpressure.MemoryLevelsManual:
		if len(args) != 0 {
			return sessionPressureError("policy memory-levels "+sub+" accepts no arguments", 2)
		}
		mutation, err := beginPressurePolicyMutation(dir)
		if err != nil {
			return sessionPressureError(err.Error(), 1)
		}
		defer mutation.Close()
		runtime := mutation.Runtime
		runtime.policy.MemoryLevels = sub
		var levels *sessionpressure.MemoryLevels
		if sub == sessionpressure.MemoryLevelsCalibrated {
			physicalMB, err := ReadPhysicalMemoryMB(mutation.Context, runtime.sampler)
			if err != nil {
				return sessionPressureError(err.Error(), 1)
			}
			resolved, err := sessionpressure.ResolveHostMemoryLevels(runtime.dir, physicalMB)
			if err != nil {
				return sessionPressureError(err.Error(), 1)
			}
			// Persist the resolved values too, so an older binary that does
			// not know memory_levels reads the calibrated ladder from policy.json.
			resolved.Apply(&runtime.policy)
			levels = &resolved
		}
		if err := sessionpressure.SavePolicy(runtime.path, runtime.policy); err != nil {
			return sessionPressureError(err.Error(), 1)
		}
		if err := restartPressureMonitor(runtime.dir); err != nil {
			return sessionPressureError("policy saved but resident monitor did not confirm reload: "+err.Error(), 1)
		}
		return emitPressure(g, map[string]any{"ok": true, "action": "policy.memory_levels." + sub, "policy": runtime.policy, "path": runtime.path, "levels": levels},
			"memory levels: "+sub+"\n", 0)
	default:
		return sessionPressureError("unknown policy memory-levels subcommand "+fmt.Sprintf("%q", sub)+"; want show, check, calibrated or manual", 2)
	}
}

// memoryLevelsMode reads the raw policy.json mode without loading it, so show
// works even when a bad memory-levels.json makes the calibrated load fail.
func memoryLevelsMode(dir string) string {
	body, err := os.ReadFile(sessionpressure.PolicyPath(dir))
	if err != nil {
		return "none (no policy.json)"
	}
	var raw struct {
		MemoryLevels string `json:"memory_levels"`
	}
	if json.Unmarshal(body, &raw) != nil || raw.MemoryLevels == "" {
		return sessionpressure.MemoryLevelsManual
	}
	return raw.MemoryLevels
}
