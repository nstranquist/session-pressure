package sessionpressure

import (
	"encoding/json"
	"strings"
	"testing"
)

// incident20261115Processes models the 2026-11-15 shape: one Claude desktop
// tree hosting many sessions, each with gopls, its telemetry sidecar, and
// node language servers, next to a browser and a go build.
func incident20261115Processes() ([]Process, []AgentTree) {
	const desktop = 92881
	processes := []Process{
		{PID: desktop, PPID: 1, RSSKB: 386 << 10, Executable: "Claude", Agent: "claude"},
		{PID: 100, PPID: 1, RSSKB: 300 << 10, Executable: "Google Chrome Helper"},
		{PID: 101, PPID: 1, RSSKB: 900 << 10, Executable: "compile"},
		{PID: 102, PPID: 1, RSSKB: 50 << 10, Executable: "WindowServer"},
	}
	tree := AgentTree{Agent: "claude", RootPID: desktop, PIDs: []int{desktop}}
	pid := 1000
	next := func() int { pid++; return pid }
	for session := 0; session < 21; session++ {
		cli := next()
		processes = append(processes, Process{PID: cli, PPID: desktop, RSSKB: 120 << 10, Executable: "claude", Agent: "claude"})
		tree.PIDs = append(tree.PIDs, cli)
		{
			gopls := next()
			processes = append(processes, Process{PID: gopls, PPID: cli, RSSKB: 160 << 10, Executable: "gopls"})
			tree.PIDs = append(tree.PIDs, gopls)
			if session < 17 {
				sidecar := next()
				processes = append(processes, Process{PID: sidecar, PPID: gopls, RSSKB: 20 << 10, Executable: "gopls"})
				tree.PIDs = append(tree.PIDs, sidecar)
			}
		}
		if session < 6 {
			ts := next()
			processes = append(processes, Process{PID: ts, PPID: cli, RSSKB: 200 << 10, Executable: "node", ScriptFamily: MemoryFamilyLSP, ScriptMember: "tsserver"})
			py := next()
			processes = append(processes, Process{PID: py, PPID: cli, RSSKB: 150 << 10, Executable: "node", ScriptFamily: MemoryFamilyLSP, ScriptMember: "pyright"})
			tree.PIDs = append(tree.PIDs, ts, py)
		}
	}
	return processes, []AgentTree{tree}
}

func TestMemoryFamiliesNameLanguageServersInsideAgentTrees(t *testing.T) {
	processes, trees := incident20261115Processes()
	families := buildMemoryFamilies(processes, trees)
	if len(families) == 0 || families[0].Family != MemoryFamilyLSP {
		t.Fatalf("top family = %+v, want lsp first", families)
	}
	lsp := families[0]
	// 21 gopls x160 + 17 sidecars x20 + 6 tsserver x200 + 6 pyright x150.
	if lsp.ProcessCount != 21+17+12 || lsp.RSSSumMB != 21*160+17*20+6*200+6*150 {
		t.Fatalf("lsp = %+v", lsp)
	}
	if lsp.ReclaimableMB != lsp.RSSSumMB || lsp.Relief != MemoryReliefRestartOnDemand {
		t.Fatalf("lsp must be fully reclaimable on demand: %+v", lsp)
	}
	if lsp.AgentProcessCount != lsp.ProcessCount {
		t.Fatalf("every language server sits in the desktop tree: %+v", lsp)
	}
	names := map[string]MemoryFamilyMember{}
	for _, member := range lsp.Members {
		names[member.Name] = member
	}
	if names["gopls"].ProcessCount != 21 || names["gopls-telemetry-sidecar"].ProcessCount != 17 || names["tsserver"].ProcessCount != 6 || names["pyright"].ProcessCount != 6 {
		t.Fatalf("members = %+v", lsp.Members)
	}
	byFamily := map[string]MemoryFamily{}
	for _, family := range families {
		byFamily[family.Family] = family
	}
	if host := byFamily[MemoryFamilyAgentHost]; host.ProcessCount != 22 || host.ReclaimableMB != 0 || host.Relief != MemoryReliefCloseIdleSessions {
		t.Fatalf("agent host = %+v", host)
	}
	if build := byFamily[MemoryFamilyBuildTest]; build.RSSSumMB != 900 || build.Relief != MemoryReliefFinishes || build.ReclaimableMB != 0 {
		t.Fatalf("build = %+v", build)
	}
	if browser := byFamily[MemoryFamilyBrowser]; browser.RSSSumMB != 300 || browser.Relief != MemoryReliefOwnerOnly {
		t.Fatalf("browser = %+v", browser)
	}
	top, ok := TopReclaimableMemoryFamily(families)
	if !ok || top.Family != MemoryFamilyLSP {
		t.Fatalf("top reclaimable = %+v", top)
	}
	body, err := json.Marshal(families)
	if err != nil {
		t.Fatal(err)
	}
	for _, leak := range []string{"92881", "\"pid\"", "/Users"} {
		if strings.Contains(string(body), leak) {
			t.Fatalf("families leak %q: %s", leak, body)
		}
	}
	if got := compactMemoryFamilies(families, 3); !strings.HasPrefix(got, "lsp=5800/5800,agent_host=") || strings.Count(got, ",") != 2 {
		t.Fatalf("compact = %q", got)
	}
}

func TestTreeReasonNamesLanguageServerShare(t *testing.T) {
	processes, trees := incident20261115Processes()
	_, familyByPID := classifyMemoryFamilies(processes, trees)
	annotateTreeLSP(trees, processes, familyByPID)
	if trees[0].LSPProcessCount != 50 || trees[0].LSPRSSSumMB != 5800 {
		t.Fatalf("tree lsp = %d / %.1f", trees[0].LSPProcessCount, trees[0].LSPRSSSumMB)
	}
	policy := DefaultPolicy(16 * 1024)
	trees[0].RSSSumMB = 6000
	got := Evaluate(Snapshot{FreePercent: 33, SwapUsedMB: 9724, TopAgentTrees: trees}, policy)
	joined := strings.Join(got.Reasons, "; ")
	if got.Level != LevelRed || !strings.Contains(joined, "claude tree 92881 RSS sum 6000 MB >= red 5120 MB (language servers 5800 MB in 50 processes)") {
		t.Fatalf("level=%s reasons=%s", got.Level, joined)
	}
	// A tree without language servers keeps the historical reason text.
	plain := Evaluate(Snapshot{FreePercent: 60, TopAgentTrees: []AgentTree{{Agent: "codex", RootPID: 7, RSSSumMB: 3100}}}, policy)
	if len(plain.Reasons) != 1 || plain.Reasons[0] != "codex tree 7 RSS sum 3100 MB >= warning 3072 MB" {
		t.Fatalf("plain reasons = %#v", plain.Reasons)
	}
}

func TestClassifyNodeScriptKnowsLanguageServersAndBuildTools(t *testing.T) {
	for script, want := range map[string][2]string{
		"tsserver.js":                  {MemoryFamilyLSP, "tsserver"},
		"typescript-language-server":   {MemoryFamilyLSP, "typescript-language-server"},
		"langserver.index.js":          {MemoryFamilyLSP, "pyright"},
		"pyright-langserver":           {MemoryFamilyLSP, "pyright"},
		"some-new-language-server.mjs": {MemoryFamilyLSP, "some-new-language-server"},
		"vitest.mjs":                   {MemoryFamilyBuildTest, "vitest"},
		"tsc":                          {MemoryFamilyBuildTest, "tsc"},
	} {
		family, member, ok := classifyNodeScript(script)
		if !ok || family != want[0] || member != want[1] {
			t.Fatalf("%s => %s/%s/%v, want %v", script, family, member, ok, want)
		}
	}
	if _, _, ok := classifyNodeScript("server.js"); ok {
		t.Fatal("an unknown script must stay unclassified")
	}
}

func TestNodeEntryScriptReadsOnlyTheFirstPositionalArgument(t *testing.T) {
	if got := nodeEntryScript([]string{"node", "--max-old-space-size=4096", "/x/typescript/lib/tsserver.js", "--logFile", "/tmp/secret"}); got != "tsserver.js" {
		t.Fatalf("entry = %q", got)
	}
	if got := nodeEntryScript([]string{"node", "app.js", "please tsserver.js my prompt"}); got != "app.js" {
		t.Fatalf("prompt text after the script must be ignored: %q", got)
	}
	if got := nodeEntryScript([]string{"node"}); got != "" {
		t.Fatalf("entry = %q", got)
	}
}

func TestGoplsSidecarNeedsGoplsParent(t *testing.T) {
	catalog := ActiveAgentIdentityCatalog()
	if family, member := classifyProcessFamily(Process{}, "gopls", "claude", true, catalog); family != MemoryFamilyLSP || member != "gopls" {
		t.Fatalf("gopls under claude = %s/%s", family, member)
	}
	if family, member := classifyProcessFamily(Process{}, "gopls", "gopls", true, catalog); family != MemoryFamilyLSP || member != "gopls-telemetry-sidecar" {
		t.Fatalf("gopls under gopls = %s/%s", family, member)
	}
	if family, _ := classifyProcessFamily(Process{}, "lua-language-se", "", false, catalog); family != MemoryFamilyLSP {
		t.Fatalf("truncated p_comm language server = %s", family)
	}
}

func TestDoctorNamesMemoryFamiliesUnderMemoryRed(t *testing.T) {
	processes, trees := incident20261115Processes()
	families, familyByPID := classifyMemoryFamilies(processes, trees)
	annotateTreeLSP(trees, processes, familyByPID)
	trees[0].RSSSumMB = 6000
	policy := DefaultPolicy(16 * 1024)
	latest := Snapshot{FreePercent: 33, SwapUsedMB: 9724, TopAgentTrees: trees, MemoryFamilies: families}
	warnings := memoryFamilyDoctorWarnings(PressureDoctorInput{HasLatest: true, Latest: latest, Policy: policy})
	if len(warnings) != 1 || !strings.Contains(warnings[0], "top memory families: lsp 5800 MB (gopls x21") || !strings.Contains(warnings[0], "5800 MB of lsp restarts on demand") {
		t.Fatalf("warnings = %#v", warnings)
	}
	latest.FreePercent, latest.SwapUsedMB, latest.TopAgentTrees = 60, 0, nil
	if got := memoryFamilyDoctorWarnings(PressureDoctorInput{HasLatest: true, Latest: latest, Policy: policy}); len(got) != 0 {
		t.Fatalf("no memory block, no warning: %#v", got)
	}
}
