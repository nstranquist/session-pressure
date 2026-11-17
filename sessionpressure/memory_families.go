package sessionpressure

import (
	"fmt"
	"math"
	"path/filepath"
	"sort"
	"strings"
)

// Memory families answer "what would free memory" in operator terms. On
// 2026-11-15 the monitor named only "claude tree 92881 RSS" and total swap,
// while the cause was about 6.1 GB of per-session language servers (gopls x25,
// the gopls telemetry sidecar x17, tsserver, pyright) inside ~21 Claude
// desktop sessions. Executable buckets (TopHostConsumers) could not show it:
// tsserver and pyright are "node", and the LSPs hide inside the agent trees.
//
// A family is a privacy-safe aggregate. It carries basenames and counts only:
// no PIDs, arguments, paths, or prompt text. Families are diagnostic evidence
// for operators and the delivery doctor. They never raise a level and grant no
// automatic relief authority.

// Memory family names. The set is closed so telemetry stays comparable.
const (
	MemoryFamilyLSP       = "lsp"
	MemoryFamilyAgentHost = "agent_host"
	MemoryFamilyBuildTest = "build_test"
	MemoryFamilyBrowser   = "browser"
	MemoryFamilyOther     = "other"
)

// Relief hints say who can free a family's memory and how. They are advice
// for a human or an operator agent, never an actuator.
const (
	// MemoryReliefRestartOnDemand: the process restarts itself when an editor
	// or agent needs it again, so stopping it loses no work.
	MemoryReliefRestartOnDemand = "restart_on_demand"
	// MemoryReliefFinishes: the memory returns when the build or test ends.
	MemoryReliefFinishes = "finishes_on_its_own"
	// MemoryReliefCloseIdleSessions: only closing agent sessions frees it.
	MemoryReliefCloseIdleSessions = "close_idle_sessions"
	// MemoryReliefOwnerOnly: the owner's own apps; agents leave them alone.
	MemoryReliefOwnerOnly = "owner_only"
)

const (
	maxProjectedMemoryFamilies = 5
	maxMemoryFamilyMembers     = 4
)

// MemoryFamilyMember is one basename inside a family.
type MemoryFamilyMember struct {
	Name         string  `json:"name"`
	ProcessCount int     `json:"process_count"`
	RSSSumMB     float64 `json:"rss_sum_mb"`
}

// MemoryFamily is one process family's share of resident memory. RSSSumMB can
// double count shared pages, like every RSS sum here. ReclaimableMB is the
// part a restart_on_demand stop would free; other reliefs report 0 because
// freeing them costs work or belongs to the owner.
type MemoryFamily struct {
	Family            string               `json:"family"`
	Relief            string               `json:"relief"`
	ProcessCount      int                  `json:"process_count"`
	RSSSumMB          float64              `json:"rss_sum_mb"`
	ReclaimableMB     float64              `json:"reclaimable_mb"`
	AgentProcessCount int                  `json:"agent_process_count"`
	AgentRSSSumMB     float64              `json:"agent_rss_sum_mb"`
	Members           []MemoryFamilyMember `json:"members"`
}

func memoryFamilyRelief(family string) string {
	switch family {
	case MemoryFamilyLSP:
		return MemoryReliefRestartOnDemand
	case MemoryFamilyBuildTest:
		return MemoryReliefFinishes
	case MemoryFamilyAgentHost:
		return MemoryReliefCloseIdleSessions
	case MemoryFamilyBrowser:
		return MemoryReliefOwnerOnly
	}
	return ""
}

// lspNativeExecutables are language servers that run as their own binary.
// p_comm truncates to 15 bytes, so long names also match by prefix below.
var lspNativeExecutables = map[string]string{
	"gopls":           "gopls",
	"rust-analyzer":   "rust-analyzer",
	"sourcekit-lsp":   "sourcekit-lsp",
	"clangd":          "clangd",
	"zls":             "zls",
	"marksman":        "marksman",
	"taplo":           "taplo",
	"jdtls":           "jdtls",
	"ruby-lsp":        "ruby-lsp",
	"solargraph":      "solargraph",
	"elixir-ls":       "elixir-ls",
	"kotlin-language": "kotlin-language-server",
	"lua-language-se": "lua-language-server",
	"terraform-ls":    "terraform-ls",
	"omnisharp":       "omnisharp",
	"csharp-ls":       "csharp-ls",
	"basedpyright":    "basedpyright",
	"pylsp":           "pylsp",
	"ruff":            "ruff",
	"buf":             "buf",
}

// lspNodeScripts maps a node entry script basename (extension removed) to a
// member name. Only the first positional script is ever consulted.
var lspNodeScripts = map[string]string{
	"tsserver":                          "tsserver",
	"typescript-language-server":        "typescript-language-server",
	"pyright-langserver":                "pyright",
	"pyright":                           "pyright",
	"basedpyright-langserver":           "basedpyright",
	"langserver.index":                  "pyright",
	"vscode-json-language-server":       "vscode-json-language-server",
	"vscode-css-language-server":        "vscode-css-language-server",
	"vscode-html-language-server":       "vscode-html-language-server",
	"vscode-eslint-language-server":     "vscode-eslint-language-server",
	"yaml-language-server":              "yaml-language-server",
	"bash-language-server":              "bash-language-server",
	"tailwindcss-language-server":       "tailwindcss-language-server",
	"vue-language-server":               "vue-language-server",
	"svelteserver":                      "svelte-language-server",
	"intelephense":                      "intelephense",
	"dockerfile-language-server-nodejs": "dockerfile-language-server",
	"docker-langserver":                 "dockerfile-language-server",
}

// buildNodeScripts are node entry scripts that build, bundle, lint, or test.
var buildNodeScripts = map[string]string{
	"tsc": "tsc", "vite": "vite", "vitest": "vitest", "jest": "jest",
	"esbuild": "esbuild", "next": "next", "webpack": "webpack",
	"eslint": "eslint", "prettier": "prettier", "playwright": "playwright",
	"turbo": "turbo", "rollup": "rollup", "astro": "astro", "tsx": "tsx",
}

var buildTestExecutables = map[string]struct{}{
	"go": {}, "compile": {}, "link": {}, "asm": {}, "cgo": {}, "vet": {}, "cover": {},
	"gofmt": {}, "golangci-lint": {}, "staticcheck": {},
	"cc": {}, "clang": {}, "clang++": {}, "ld": {}, "ld64": {}, "swift": {}, "swiftc": {},
	"swift-frontend": {}, "swift-build": {}, "xcodebuild": {}, "ibtool": {}, "actool": {},
	"cargo": {}, "rustc": {}, "make": {}, "cmake": {}, "ninja": {},
	"npm": {}, "pnpm": {}, "yarn": {}, "bun": {}, "deno": {}, "esbuild": {},
}

// classifyNodeScript reports the family and member for a node entry script
// basename. It returns ok=false for anything it does not know.
func classifyNodeScript(script string) (family, member string, ok bool) {
	name := strings.ToLower(strings.TrimSpace(script))
	for _, suffix := range []string{".js", ".mjs", ".cjs"} {
		name = strings.TrimSuffix(name, suffix)
	}
	if name == "" {
		return "", "", false
	}
	if member, ok := lspNodeScripts[name]; ok {
		return MemoryFamilyLSP, member, true
	}
	if strings.Contains(name, "language-server") || strings.Contains(name, "langserver") {
		return MemoryFamilyLSP, privacySafeExecutable(name), true
	}
	if member, ok := buildNodeScripts[name]; ok {
		return MemoryFamilyBuildTest, member, true
	}
	return "", "", false
}

// classifyProcessFamily assigns one process to a family and member name.
// parentExecutable is the lower-case basename of the parent, used only to
// recognise the gopls telemetry sidecar (a gopls child of gopls).
func classifyProcessFamily(process Process, executable, parentExecutable string, inAgentTree bool, catalog *AgentIdentityCatalog) (family, member string) {
	if process.ScriptFamily != "" && process.ScriptMember != "" {
		return process.ScriptFamily, process.ScriptMember
	}
	lower := strings.ToLower(executable)
	if lower == "gopls" && parentExecutable == "gopls" {
		return MemoryFamilyLSP, "gopls-telemetry-sidecar"
	}
	if member, ok := lspNativeExecutables[lower]; ok {
		return MemoryFamilyLSP, member
	}
	if strings.Contains(lower, "language-se") || strings.Contains(lower, "langserver") || strings.HasSuffix(lower, "-lsp") {
		return MemoryFamilyLSP, executable
	}
	if _, ok := buildTestExecutables[lower]; ok {
		return MemoryFamilyBuildTest, executable
	}
	if strings.HasSuffix(lower, ".test") {
		return MemoryFamilyBuildTest, "go-test-binary"
	}
	category := hostConsumerCategoryWithCatalog(executable, catalog)
	switch {
	case category == "agent" || process.Agent != "":
		return MemoryFamilyAgentHost, executable
	case agentDesktopHelper(lower):
		return MemoryFamilyAgentHost, executable
	case category == "browser":
		// A browser an agent drives (inside its tree) is agent work, but it is
		// still a browser: relief differs from the agent host itself.
		return MemoryFamilyBrowser, executable
	}
	if inAgentTree && lower == "node" {
		// Unrecognised node under an agent is most often an MCP server or a
		// tool the agent spawned. Keep it with the agent host, honestly named.
		return MemoryFamilyAgentHost, "node"
	}
	return MemoryFamilyOther, executable
}

// agentDesktopHelper matches the Electron helpers of agent desktop apps. Their
// p_comm is "Claude Helper (Renderer)" and similar, normalised with "_".
func agentDesktopHelper(lower string) bool {
	for _, prefix := range []string{"claude_helper", "codex_", "codex_helper", "cursor_helper", "grok_helper"} {
		if strings.HasPrefix(lower, prefix) {
			return true
		}
	}
	return lower == "claude" || lower == "codex"
}

// buildMemoryFamilies reduces the full inventory to the top families by RSS.
func buildMemoryFamilies(processes []Process, trees []AgentTree) []MemoryFamily {
	families, _ := classifyMemoryFamilies(processes, trees)
	return families
}

// annotateTreeLSP records how much of each agent tree is language servers.
// The tree RSS rung corroborates sticky swap; this names its largest
// reclaimable part in the reason.
func annotateTreeLSP(trees []AgentTree, processes []Process, familyByPID map[int]string) {
	if len(familyByPID) == 0 {
		return
	}
	type memoryKB struct{ rss, footprint int64 }
	byPID := make(map[int]memoryKB, len(processes))
	for _, process := range processes {
		byPID[process.PID] = memoryKB{process.RSSKB, processFootprintKB(process)}
	}
	for index := range trees {
		count, rssKB, footprintKB := 0, int64(0), int64(0)
		for _, pid := range trees[index].PIDs {
			if familyByPID[pid] == MemoryFamilyLSP {
				count++
				rssKB += byPID[pid].rss
				footprintKB += byPID[pid].footprint
			}
		}
		trees[index].LSPProcessCount = count
		trees[index].LSPRSSSumMB = roundMB(float64(rssKB) / 1024)
		if trees[index].FootprintProcessCount > 0 {
			trees[index].LSPFootprintMB = roundMB(float64(footprintKB) / 1024)
		}
	}
}

// classifyMemoryFamilies returns the projected families and each PID's family.
func classifyMemoryFamilies(processes []Process, trees []AgentTree) ([]MemoryFamily, map[int]string) {
	return classifyMemoryFamiliesWithCatalog(processes, trees, ActiveAgentIdentityCatalog())
}

func classifyMemoryFamiliesWithCatalog(processes []Process, trees []AgentTree, catalog *AgentIdentityCatalog) ([]MemoryFamily, map[int]string) {
	if len(processes) == 0 {
		return nil, nil
	}
	familyByPID := make(map[int]string, len(processes))
	agentPIDs := make(map[int]struct{})
	for _, tree := range trees {
		for _, pid := range tree.PIDs {
			agentPIDs[pid] = struct{}{}
		}
	}
	executableByPID := make(map[int]string, len(processes))
	for _, process := range processes {
		executableByPID[process.PID] = strings.ToLower(processExecutableName(process))
	}
	type memberKey struct{ family, member string }
	members := make(map[memberKey]*MemoryFamilyMember)
	families := make(map[string]*MemoryFamily)
	for _, process := range processes {
		executable := processExecutableName(process)
		_, inTree := agentPIDs[process.PID]
		family, member := classifyProcessFamily(process, executable, executableByPID[process.PPID], inTree, catalog)
		familyByPID[process.PID] = family
		rssMB := float64(process.RSSKB) / 1024
		bucket := families[family]
		if bucket == nil {
			bucket = &MemoryFamily{Family: family, Relief: memoryFamilyRelief(family)}
			families[family] = bucket
		}
		bucket.ProcessCount++
		bucket.RSSSumMB += rssMB
		if inTree {
			bucket.AgentProcessCount++
			bucket.AgentRSSSumMB += rssMB
		}
		key := memberKey{family, member}
		entry := members[key]
		if entry == nil {
			entry = &MemoryFamilyMember{Name: member}
			members[key] = entry
		}
		entry.ProcessCount++
		entry.RSSSumMB += rssMB
	}
	for key, entry := range members {
		families[key.family].Members = append(families[key.family].Members, *entry)
	}
	out := make([]MemoryFamily, 0, len(families))
	for _, family := range families {
		sort.Slice(family.Members, func(i, j int) bool {
			if family.Members[i].RSSSumMB != family.Members[j].RSSSumMB {
				return family.Members[i].RSSSumMB > family.Members[j].RSSSumMB
			}
			return family.Members[i].Name < family.Members[j].Name
		})
		if len(family.Members) > maxMemoryFamilyMembers {
			family.Members = family.Members[:maxMemoryFamilyMembers]
		}
		for index := range family.Members {
			family.Members[index].RSSSumMB = roundMB(family.Members[index].RSSSumMB)
		}
		family.RSSSumMB = roundMB(family.RSSSumMB)
		family.AgentRSSSumMB = roundMB(family.AgentRSSSumMB)
		if family.Relief == MemoryReliefRestartOnDemand {
			family.ReclaimableMB = family.RSSSumMB
		}
		out = append(out, *family)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].RSSSumMB != out[j].RSSSumMB {
			return out[i].RSSSumMB > out[j].RSSSumMB
		}
		return out[i].Family < out[j].Family
	})
	if len(out) > maxProjectedMemoryFamilies {
		out = out[:maxProjectedMemoryFamilies]
	}
	return out, familyByPID
}

func processExecutableName(process Process) string {
	executable := privacySafeExecutable(process.Executable)
	if executable == "unknown" {
		executable = privacySafeCommandExecutable(process.Command)
	}
	return executable
}

func roundMB(value float64) float64 {
	return math.Round(value*10) / 10
}

// TopReclaimableMemoryFamily returns the family with the most reclaimable MB.
func TopReclaimableMemoryFamily(families []MemoryFamily) (MemoryFamily, bool) {
	best := MemoryFamily{}
	for _, family := range families {
		if family.ReclaimableMB > best.ReclaimableMB {
			best = family
		}
	}
	return best, best.ReclaimableMB > 0
}

// nodeEntryScript is the basename of the first positional argument after
// node, the same single position agent identity trusts. Later arguments can
// hold prompt text and are never read.
func nodeEntryScript(args []string) string {
	start := 0
	if len(args) > 0 && strings.EqualFold(filepath.Base(args[0]), "node") {
		start = 1
	}
	for index := start; index < len(args) && index < start+4; index++ {
		if strings.HasPrefix(args[index], "-") {
			continue
		}
		return filepath.Base(args[index])
	}
	return ""
}

// compactMemoryFamilies renders the top families as "family=rss/reclaimable"
// in whole MB for the bounded heartbeat.
func compactMemoryFamilies(families []MemoryFamily, limit int) string {
	parts := make([]string, 0, min(limit, len(families)))
	for _, family := range families {
		if len(parts) >= limit {
			break
		}
		parts = append(parts, fmt.Sprintf("%s=%.0f/%.0f", family.Family, family.RSSSumMB, family.ReclaimableMB))
	}
	return strings.Join(parts, ",")
}

// memoryFamiliesText renders families for operators, for example
// "lsp 5800 MB (gopls x21, tsserver x6), agent_host 2906 MB (claude x21)".
func memoryFamiliesText(families []MemoryFamily, limit int) string {
	parts := make([]string, 0, min(limit, len(families)))
	for _, family := range families {
		if len(parts) >= limit {
			break
		}
		members := make([]string, 0, len(family.Members))
		for _, member := range family.Members {
			members = append(members, fmt.Sprintf("%s x%d", member.Name, member.ProcessCount))
		}
		text := fmt.Sprintf("%s %.0f MB", family.Family, family.RSSSumMB)
		if len(members) > 0 {
			text += " (" + strings.Join(members, ", ") + ")"
		}
		parts = append(parts, text)
	}
	return strings.Join(parts, ", ")
}
