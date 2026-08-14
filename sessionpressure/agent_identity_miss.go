package sessionpressure

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

// AgentIdentityMiss is a diagnostic that install roots or basename shapes
// suggest an agent is running but no tree was projected for it.
type AgentIdentityMiss struct {
	Agent  string `json:"agent"`
	Reason string `json:"reason"`
	Detail string `json:"detail,omitempty"`
}

// AgentShapedProcessSummary is a privacy-safe rollup of processes that match
// identity shapes (exact / path-probe / known agent executable). No command
// lines, paths, or prompt text — only agent id, safe executable token, and counts.
type AgentShapedProcessSummary struct {
	Agent            string `json:"agent"`
	Executable       string `json:"executable"`
	ProcessCount     int    `json:"process_count"`
	LabeledCount     int    `json:"labeled_count"`
	UnlabeledCount   int    `json:"unlabeled_count"`
	WithSessionCount int    `json:"with_session_count"`
}

// AgentIdentityReport is a compact operator view of the identity plane.
type AgentIdentityReport struct {
	SchemaVersion   int                         `json:"schema_version"`
	OverlayLoaded   bool                        `json:"overlay_loaded"`
	OverlayPath     string                      `json:"overlay_path,omitempty"`
	OverlayError    string                      `json:"overlay_error,omitempty"`
	Agents          []string                    `json:"agents"`
	Rules           []AgentIdentityRule         `json:"rules"`
	Installs        []AgentInstallPresence      `json:"installs,omitempty"`
	TreeCounts      map[string]int              `json:"tree_counts,omitempty"`
	ProcessSummary  []AgentShapedProcessSummary `json:"process_summary,omitempty"`
	Misses          []AgentIdentityMiss         `json:"misses,omitempty"`
	InventorySource string                      `json:"inventory_source,omitempty"`
}

// BuildAgentIdentityReport composes catalog + optional live tree evidence.
func BuildAgentIdentityReport(catalog *AgentIdentityCatalog, home string, trees []AgentTree, processes []Process) AgentIdentityReport {
	if catalog == nil {
		catalog = CompileAgentIdentityCatalog(DefaultAgentIdentityRules())
	}
	report := AgentIdentityReport{
		SchemaVersion:  catalog.SchemaVersion,
		OverlayLoaded:  catalog.OverlayLoaded,
		OverlayPath:    catalog.OverlayPath,
		OverlayError:   catalog.OverlayError,
		Agents:         catalog.KnownAgents(),
		Rules:          catalog.Rules,
		Installs:       catalog.InstallPresences(home),
		TreeCounts:     countTreesByAgent(trees),
		ProcessSummary: SummarizeAgentShapedProcesses(catalog, processes),
	}
	report.Misses = DetectAgentIdentityMisses(catalog, home, trees, processes)
	return report
}

// SummarizeAgentShapedProcesses rolls up agent-shaped processes for live CLI.
func SummarizeAgentShapedProcesses(catalog *AgentIdentityCatalog, processes []Process) []AgentShapedProcessSummary {
	if catalog == nil || len(processes) == 0 {
		return nil
	}
	type key struct{ agent, exec string }
	byKey := map[key]*AgentShapedProcessSummary{}
	for _, process := range processes {
		agent, execToken, ok := classifyAgentShaped(catalog, process)
		if !ok {
			continue
		}
		k := key{agent: agent, exec: execToken}
		row, exists := byKey[k]
		if !exists {
			row = &AgentShapedProcessSummary{Agent: agent, Executable: execToken}
			byKey[k] = row
		}
		row.ProcessCount++
		if process.Agent != "" {
			row.LabeledCount++
		} else {
			row.UnlabeledCount++
		}
		if process.SessionID != "" {
			row.WithSessionCount++
		}
	}
	if len(byKey) == 0 {
		return nil
	}
	out := make([]AgentShapedProcessSummary, 0, len(byKey))
	for _, row := range byKey {
		out = append(out, *row)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Agent != out[j].Agent {
			return out[i].Agent < out[j].Agent
		}
		if out[i].ProcessCount != out[j].ProcessCount {
			return out[i].ProcessCount > out[j].ProcessCount
		}
		return out[i].Executable < out[j].Executable
	})
	// Bound for CLI/token budget.
	const maxSummaryRows = 32
	if len(out) > maxSummaryRows {
		out = out[:maxSummaryRows]
	}
	return out
}

func classifyAgentShaped(catalog *AgentIdentityCatalog, process Process) (agent, execToken string, ok bool) {
	if process.Agent != "" {
		agent = normalizeAgentID(process.Agent)
		execToken = privacySafeExecutable(process.Executable)
		if execToken == "unknown" || execToken == "" {
			execToken = agent
		}
		return agent, execToken, agent != ""
	}
	base := normalizeBasename(process.Executable)
	if base == "" || base == "unknown" {
		if process.Command != "" {
			fields := strings.Fields(process.Command)
			if len(fields) > 0 {
				base = normalizeBasename(filepath.Base(fields[0]))
			}
		}
	}
	if base == "" || base == "unknown" {
		return "", "", false
	}
	if a, _, matched := catalog.MatchExactBasename(base); matched {
		return a, privacySafeExecutable(base), true
	}
	if catalog.NeedsPathProbe(base) {
		if a := catalog.agentForPathProbeBasename(base); a != "" {
			return a, privacySafeExecutable(base), true
		}
	}
	if catalog.IsAgentExecutable(base) {
		return normalizeAgentID(base), privacySafeExecutable(base), true
	}
	return "", "", false
}

func countTreesByAgent(trees []AgentTree) map[string]int {
	if len(trees) == 0 {
		return map[string]int{}
	}
	counts := make(map[string]int)
	for _, tree := range trees {
		agent := normalizeAgentID(tree.Agent)
		if agent == "" {
			continue
		}
		counts[agent]++
	}
	return counts
}

// DetectAgentIdentityMisses finds likely gaps between on-disk install roots /
// path-probe basenames and projected agent trees.
func DetectAgentIdentityMisses(catalog *AgentIdentityCatalog, home string, trees []AgentTree, processes []Process) []AgentIdentityMiss {
	if catalog == nil {
		return nil
	}
	treeCounts := countTreesByAgent(trees)
	// Processes that look like path-probe / exact basenames but were not labeled.
	unlabeled := map[string]int{}
	labeledLike := map[string]int{}
	for _, process := range processes {
		base := normalizeBasename(process.Executable)
		if base == "" || base == "unknown" {
			if process.Command != "" {
				fields := strings.Fields(process.Command)
				if len(fields) > 0 {
					base = normalizeBasename(filepath.Base(fields[0]))
				}
			}
		}
		if base == "" || base == "unknown" {
			continue
		}
		agent := ""
		if a, _, ok := catalog.MatchExactBasename(base); ok {
			agent = a
		} else if catalog.NeedsPathProbe(base) {
			agent = catalog.agentForPathProbeBasename(base)
		} else if _, ok := catalog.agents[base]; ok {
			agent = base
		}
		if agent == "" {
			continue
		}
		if process.Agent == "" {
			unlabeled[agent]++
		} else {
			labeledLike[agent]++
		}
	}

	var misses []AgentIdentityMiss
	// Unlabeled basenames that match identity shapes are hard misses.
	for agent, count := range unlabeled {
		misses = append(misses, AgentIdentityMiss{
			Agent:  agent,
			Reason: "unlabeled_agent_basename",
			Detail: fmt.Sprintf("%d process(es) match %s identity shapes but have empty Agent", count, agent),
		})
	}

	// Install root present + zero trees + no labeled processes → soft miss when
	// the operator may have agents running under opaque names not yet in catalog.
	presences := catalog.InstallPresences(home)
	presentAgents := map[string]bool{}
	for _, presence := range presences {
		if presence.Present {
			presentAgents[presence.Agent] = true
		}
	}
	for agent := range presentAgents {
		if treeCounts[agent] > 0 || labeledLike[agent] > 0 || unlabeled[agent] > 0 {
			continue
		}
		// Only emit soft miss when the install exact binary is executable —
		// empty download dirs are normal.
		hasExecutableRoot := false
		for _, presence := range presences {
			if presence.Agent == agent && presence.Kind == "exact" && presence.Present && isExecutableFile(presence.Path) {
				hasExecutableRoot = true
				break
			}
		}
		if !hasExecutableRoot {
			continue
		}
		misses = append(misses, AgentIdentityMiss{
			Agent:  agent,
			Reason: "install_present_no_trees",
			Detail: fmt.Sprintf("%s install root is present but no agent trees were projected", agent),
		})
	}

	sort.Slice(misses, func(i, j int) bool {
		if misses[i].Agent != misses[j].Agent {
			return misses[i].Agent < misses[j].Agent
		}
		return misses[i].Reason < misses[j].Reason
	})
	return misses
}

// AssessAgentIdentityCoverage adds a coverage surface for the identity plane.
func AssessAgentIdentityCoverage(catalog *AgentIdentityCatalog, home string, trees []AgentTree, processes []Process) CoverageSurface {
	surface := CoverageSurface{
		ID:     "agent_identity",
		Label:  "Agent process identity",
		Scope:  "coding-agent trees",
		State:  CoverageObserved,
		Detail: "shipped identity catalog classifies codex/claude/grok/kimi via exact names, install roots, and path probes",
	}
	if catalog == nil {
		catalog = CompileAgentIdentityCatalog(DefaultAgentIdentityRules())
	}
	if catalog.OverlayError != "" {
		surface.State = CoverageAttention
		surface.Detail = "agent identity overlay failed closed: " + catalog.OverlayError
		return surface
	}
	misses := DetectAgentIdentityMisses(catalog, home, trees, processes)
	hard := 0
	for _, miss := range misses {
		if miss.Reason == "unlabeled_agent_basename" {
			hard++
		}
	}
	if hard > 0 {
		surface.State = CoverageAttention
		surface.Detail = fmt.Sprintf("%d unlabeled agent-shaped process group(s); identity catalog or overlay needs expansion", hard)
		return surface
	}
	if catalog.OverlayLoaded {
		surface.State = CoverageEnforced
		surface.Detail = "shipped identity catalog plus operator overlay active"
		return surface
	}
	if len(misses) > 0 {
		surface.Detail = fmt.Sprintf("catalog active; %d soft install/tree mismatch(es) recorded for operator review", len(misses))
		return surface
	}
	surface.Detail = "shipped identity catalog active; no unlabeled agent-shaped processes"
	return surface
}
