package sessionpressure

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDefaultCatalogRecognizesCoreAgents(t *testing.T) {
	catalog := CompileAgentIdentityCatalog(DefaultAgentIdentityRules())
	for _, name := range []string{"codex", "claude", "grok", "kimi", "muse"} {
		agent, executable, ok := catalog.MatchExactBasename(name)
		if !ok || agent != name || executable != name {
			t.Fatalf("exact %q => %q %q %v", name, agent, executable, ok)
		}
		if !catalog.IsAgentExecutable(name) {
			t.Fatalf("%q not marked agent executable", name)
		}
	}
	if agent, _, ok := catalog.MatchNodeScript("codex"); !ok || agent != "codex" {
		t.Fatalf("node codex script not recognized")
	}
	if catalog.NeedsPathProbe("2.1.211") != true {
		t.Fatal("semver path probe expected for Claude")
	}
	if catalog.NeedsPathProbe("grok-0.2.118-mac") != true {
		t.Fatal("grok version path probe expected")
	}
	if catalog.NeedsPathProbe("grok-helper") {
		t.Fatal("grok-helper must not path-probe")
	}
}

func TestCatalogMatchPathTrustedRootsOnly(t *testing.T) {
	catalog := CompileAgentIdentityCatalog(DefaultAgentIdentityRules())
	home := "/Users/user"
	cases := []struct {
		path  string
		agent string
		ok    bool
	}{
		{"/Users/user/.local/share/claude/versions/2.1.211", "claude", true},
		{"/Users/user/.grok/downloads/grok-0.2.118-macos-aarch64", "grok", true},
		{"/Users/user/.grok/bin/grok", "grok", true},
		{"/Users/user/.grok/bin/agent", "grok", true},
		{"/Users/user/.grok/downloads/not-grok-helper", "", false},
		{"/Users/user/.local/bin/muse-bin-1.4.14-R7309.1", "muse", true},
		{"/Users/user/.local/bin/muse-helper", "", false},
		{"/tmp/muse-bin-1.4.14-R7309.1", "", false},
		{"/tmp/grok/downloads/grok-0.2.118-macos-aarch64", "", false},
		{"/Users/user/.other/bin/grok", "", false},
	}
	for _, tc := range cases {
		agent, _, ok := catalog.MatchPath(tc.path, home)
		if ok != tc.ok || agent != tc.agent {
			t.Fatalf("path %q => agent=%q ok=%v want %q/%v", tc.path, agent, ok, tc.agent, tc.ok)
		}
	}
}

func TestMatchCommandCoversVersionedGrokAndNode(t *testing.T) {
	catalog := CompileAgentIdentityCatalog(DefaultAgentIdentityRules())
	for _, command := range []string{
		"grok --resume 019f5be0-7d38-7271-ba7d-8ade4a407bf0",
		"node /opt/bin/grok --continue",
		"/Users/user/.grok/bin/grok",
		"/Users/user/.grok/downloads/grok-0.2.118-macos-aarch64",
		"grok-0.2.118-mac",
	} {
		agent, executable, ok := catalog.MatchCommand(command)
		if !ok || agent != "grok" || executable != "grok" {
			t.Fatalf("MatchCommand(%q) = %q %q %v", command, agent, executable, ok)
		}
	}
}

func TestOverlayMergesInstallRootFailClosed(t *testing.T) {
	base := CompileAgentIdentityCatalog(DefaultAgentIdentityRules())
	overlay := &AgentIdentityOverlay{
		SchemaVersion: agentIdentitySchemaVersion,
		Rules: []AgentIdentityRule{{
			Agent:                        "grok",
			InstallPathExact:             []string{".local/opt/custom-grok/bin/grok"},
			ExactBasenames:               []string{"custom-grok"},
			PathProbePrefixes:            []string{"custom-grok-"},
			PathProbePrefixRequiresDigit: true,
		}},
	}
	merged, err := MergeAgentIdentityOverlay(base, overlay, "/tmp/agent-identity.json")
	if err != nil {
		t.Fatal(err)
	}
	if !merged.OverlayLoaded {
		t.Fatal("expected overlay loaded")
	}
	agent, _, ok := merged.MatchExactBasename("custom-grok")
	if !ok || agent != "grok" {
		t.Fatalf("custom basename not merged: %q %v", agent, ok)
	}
	agent, _, ok = merged.MatchPath("/Users/user/.local/opt/custom-grok/bin/grok", "/Users/user")
	if !ok || agent != "grok" {
		t.Fatalf("custom install path not merged: %q %v", agent, ok)
	}

	// Path probe without install roots rejected.
	bad := AgentIdentityOverlay{
		SchemaVersion: agentIdentitySchemaVersion,
		Rules: []AgentIdentityRule{{
			Agent:             "grok",
			PathProbePrefixes: []string{"weird-"},
		}},
	}
	if err := ValidateAgentIdentityOverlay(bad); err == nil {
		t.Fatal("expected path probe without install root to fail")
	}
	// Overly broad install root rejected.
	if _, ok := normalizeHomeRelativePath("Downloads", true); ok {
		t.Fatal("single-segment install root must be rejected")
	}
}

func TestLoadAgentIdentityOverlayFromDisk(t *testing.T) {
	dir := t.TempDir()
	path := AgentIdentityOverlayPath(dir)
	body := AgentIdentityOverlay{
		SchemaVersion: agentIdentitySchemaVersion,
		Rules: []AgentIdentityRule{{
			Agent:            "kimi",
			InstallPathExact: []string{".local/opt/kimi/bin/kimi"},
			ExactBasenames:   []string{"kimi-cli"},
		}},
	}
	raw, _ := json.Marshal(body)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	ResetAgentIdentityCatalogCache()
	catalog := loadAgentIdentityCatalogCached(dir)
	if !catalog.OverlayLoaded || catalog.OverlayError != "" {
		t.Fatalf("overlay not loaded: %+v", catalog)
	}
	if agent, _, ok := catalog.MatchExactBasename("kimi-cli"); !ok || agent != "kimi" {
		t.Fatalf("overlay basename missing: %q %v", agent, ok)
	}
	// Corrupt overlay falls back without expanding ownership.
	if err := os.WriteFile(path, []byte(`{not json`), 0o600); err != nil {
		t.Fatal(err)
	}
	ResetAgentIdentityCatalogCache()
	catalog = loadAgentIdentityCatalogCached(dir)
	if catalog.OverlayLoaded || catalog.OverlayError == "" {
		t.Fatalf("corrupt overlay should fail closed: %+v", catalog)
	}
	if _, _, ok := catalog.MatchExactBasename("kimi-cli"); ok {
		t.Fatal("corrupt overlay must not grant kimi-cli ownership")
	}
}

func TestSessionOwnershipHintsFillMissingAgent(t *testing.T) {
	catalog := CompileAgentIdentityCatalog(DefaultAgentIdentityRules())
	dir := t.TempDir()
	sessionID := "019f5be0-7d38-7271-ba7d-8ade4a407bf0"
	writeHook := func(tool string) {
		t.Helper()
		body, _ := json.Marshal(hookSessionState{
			SessionID: sessionID, Tool: tool, State: SemanticStateBusy, LastUserPromptAt: 100, LastStopAt: 50,
		})
		if err := os.WriteFile(filepath.Join(dir, sessionID+".json"), body, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeHook("grok")
	hints := LoadSessionOwnershipHints(dir, catalog)
	if len(hints) != 1 || hints[0].Agent != "grok" {
		t.Fatalf("hints=%+v", hints)
	}
	processes := []Process{{
		PID: 42, Command: "opaque-binary --session " + sessionID, SessionID: sessionID,
	}}
	out := ApplySessionOwnershipHints(processes, hints)
	if out[0].Agent != "grok" || out[0].Executable != "grok" {
		t.Fatalf("session hint did not fill agent: %+v", out[0])
	}
	// Conflict does not override.
	processes[0].Agent = "codex"
	processes[0].Executable = "codex"
	out = ApplySessionOwnershipHints(processes, hints)
	if out[0].Agent != "codex" {
		t.Fatalf("session hint overrode process identity: %+v", out[0])
	}
}

func TestDetectIdentityMissUnlabeledBasename(t *testing.T) {
	catalog := CompileAgentIdentityCatalog(DefaultAgentIdentityRules())
	processes := []Process{{
		PID: 7, Executable: "grok-0.2.118-mac", Command: "grok-0.2.118-mac",
	}}
	misses := DetectAgentIdentityMisses(catalog, "/Users/user", nil, processes)
	if len(misses) != 1 || misses[0].Reason != "unlabeled_agent_basename" || misses[0].Agent != "grok" {
		t.Fatalf("misses=%+v", misses)
	}
	// After labeling, no miss.
	processes[0].Agent = "grok"
	misses = DetectAgentIdentityMisses(catalog, "/Users/user", []AgentTree{{Agent: "grok", RootPID: 7}}, processes)
	for _, miss := range misses {
		if miss.Reason == "unlabeled_agent_basename" {
			t.Fatalf("unexpected unlabeled miss: %+v", miss)
		}
	}
}

func TestDetectIdentityMissIgnoresInstalledIdleAgent(t *testing.T) {
	catalog := CompileAgentIdentityCatalog(DefaultAgentIdentityRules())
	home := t.TempDir()
	kimi := filepath.Join(home, ".kimi-code", "bin", "kimi")
	if err := os.MkdirAll(filepath.Dir(kimi), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(kimi, []byte("idle fixture\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	report := BuildAgentIdentityReport(catalog, home, nil, nil)
	if len(report.Misses) != 0 {
		t.Fatalf("idle install created identity misses: %+v", report.Misses)
	}
	present := false
	for _, install := range report.Installs {
		if install.Agent == "kimi" && install.Present {
			present = true
		}
	}
	if !present {
		t.Fatal("idle install was not retained in inventory")
	}
}

func TestAssessAgentIdentityCoverageOverlayError(t *testing.T) {
	catalog := CompileAgentIdentityCatalog(DefaultAgentIdentityRules())
	catalog.OverlayError = "boom"
	surface := AssessAgentIdentityCoverage(catalog, "/Users/user", nil, nil)
	if surface.State != CoverageAttention || surface.ID != "agent_identity" {
		t.Fatalf("surface=%+v", surface)
	}
}

func TestSessionIDFromOpenPathGrokAndCodex(t *testing.T) {
	agent, id := sessionIDFromOpenPath("/Users/user/.grok/sessions/%2FUsers%2Fnico%2Fdev/019f5be0-7d38-7271-ba7d-8ade4a407bf0/summary.json")
	if agent != "grok" || id != "019f5be0-7d38-7271-ba7d-8ade4a407bf0" {
		t.Fatalf("grok path => %q %q", agent, id)
	}
	agent, id = sessionIDFromOpenPath("/Users/user/.codex/sessions/2026/08/04/rollout-2026-08-04T10-00-00-019f5be0-7d38-7271-ba7d-8ade4a407bf0.jsonl")
	if agent != "codex" || id != "019f5be0-7d38-7271-ba7d-8ade4a407bf0" {
		t.Fatalf("codex path => %q %q", agent, id)
	}
	if a, s := sessionIDFromOpenPath("/tmp/secret-prompt.jsonl"); a != "" || s != "" {
		t.Fatalf("untrusted path classified: %q %q", a, s)
	}
}

func TestApplyPIDOwnershipHintsAttachesSessionWithoutOverride(t *testing.T) {
	const sid = "019f5be0-7d38-7271-ba7d-8ade4a407bf0"
	processes := []Process{
		{PID: 10, Agent: "grok", Executable: "grok"},
		{PID: 11, Agent: "codex", Executable: "codex"},
		{PID: 12, Agent: "", Executable: "helper"},
	}
	hints := []PIDOwnershipHint{
		{PID: 10, Agent: "grok", SessionID: sid, Evidence: "open-transcript"},
		{PID: 11, Agent: "grok", SessionID: sid, Evidence: "open-transcript"}, // conflict
		{PID: 12, Agent: "grok", SessionID: sid, Evidence: "open-transcript"}, // unlabeled
	}
	out := ApplyPIDOwnershipHints(processes, hints)
	if out[0].SessionID != sid || out[0].Agent != "grok" {
		t.Fatalf("grok attach failed: %+v", out[0])
	}
	if out[1].SessionID != "" || out[1].Agent != "codex" {
		t.Fatalf("conflict must not re-label codex: %+v", out[1])
	}
	if out[2].Agent != "" || out[2].SessionID != "" {
		t.Fatalf("unlabeled process must not gain ownership from lsof: %+v", out[2])
	}
}

func TestPeekSessionHintsServesStaleWhileRevalidate(t *testing.T) {
	ResetSessionOwnershipHintCache()
	t.Cleanup(ResetSessionOwnershipHintCache)
	dir := t.TempDir()
	catalog := CompileAgentIdentityCatalog(DefaultAgentIdentityRules())
	residentSessionHints.Lock()
	residentSessionHints.key = dir + "\x00s1"
	residentSessionHints.at = time.Now().Add(-time.Minute)
	residentSessionHints.hints = []SessionOwnershipHint{{SessionID: "s1", Agent: "grok"}}
	residentSessionHints.Unlock()
	got := peekSessionOwnershipHints(dir, catalog, []string{"s1"})
	if len(got) != 1 || got[0].SessionID != "s1" || got[0].Agent != "grok" {
		t.Fatalf("expired cache must still be served: %+v", got)
	}
}

func TestLiveSessionHintIDsAreBoundedAndSafe(t *testing.T) {
	processes := []Process{{SessionID: "../escape"}, {SessionID: "live"}, {SessionID: "live"}, {Command: "opaque --session 019f5be0-7d38-7271-ba7d-8ade4a407bf0"}}
	ids := liveSessionHintIDs(processes)
	if len(ids) != 2 || ids[0] != "019f5be0-7d38-7271-ba7d-8ade4a407bf0" || ids[1] != "live" {
		t.Fatalf("IDs: %v", ids)
	}
	for i := 0; i < maxSessionHintFiles*2; i++ {
		processes = append(processes, Process{SessionID: fmt.Sprintf("session-%03d", i)})
	}
	if got := len(liveSessionHintIDs(processes)); got != maxSessionHintFiles {
		t.Fatalf("lookup limit: %d", got)
	}
}

func TestResidentHintsFindLiveIDBeyondRegistryPrefix(t *testing.T) {
	ResetSessionOwnershipHintCache()
	t.Cleanup(ResetSessionOwnershipHintCache)
	dir := t.TempDir()
	// Old enumeration stopped after the first 64 valid files, losing live IDs.
	for i := 0; i < maxSessionHintFiles+1; i++ {
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("old-%03d.json", i)), []byte(`{"tool":"codex"}`), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "z-live.json"), []byte(`{"session_id":"z-live","tool":"grok"}`), 0600); err != nil {
		t.Fatal(err)
	}
	catalog := CompileAgentIdentityCatalog(DefaultAgentIdentityRules())
	_ = peekSessionOwnershipHints(dir, catalog, []string{"z-live"})
	deadline := time.Now().Add(testWatchdog)
	for {
		residentSessionHints.Lock()
		done := !residentSessionHints.refreshing
		residentSessionHints.Unlock()
		if done {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("refresh did not finish")
		}
		time.Sleep(time.Millisecond)
	}
	got := peekSessionOwnershipHints(dir, catalog, []string{"z-live"})
	if len(got) != 1 || got[0].SessionID != "z-live" || got[0].Agent != "grok" {
		t.Fatalf("live hint: %+v", got)
	}
	// Missing entries are negatively cached, rather than reread each tick.
	_ = peekSessionOwnershipHints(dir, catalog, []string{"missing"})
	for {
		residentSessionHints.Lock()
		done := !residentSessionHints.refreshing
		residentSessionHints.Unlock()
		if done {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("refresh did not finish")
		}
		time.Sleep(time.Millisecond)
	}
	if err := os.WriteFile(filepath.Join(dir, "missing.json"), []byte(`{"tool":"grok"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if got := peekSessionOwnershipHints(dir, catalog, []string{"missing"}); len(got) != 0 {
		t.Fatalf("empty cache was not retained: %+v", got)
	}
	residentSessionHints.Lock()
	refreshing := residentSessionHints.refreshing
	residentSessionHints.Unlock()
	if refreshing {
		t.Fatal("fresh empty result triggered I/O")
	}
}

func TestResidentSessionHintsDoNotBlockSample(t *testing.T) {
	ResetSessionOwnershipHintCache()
	t.Cleanup(ResetSessionOwnershipHintCache)
	started := time.Now()
	out := applyResidentSessionHints([]Process{{PID: 1, Agent: "grok", Executable: "grok"}}, os.TempDir())
	if elapsed := time.Since(started); elapsed > 50*time.Millisecond {
		t.Fatalf("resident session hints blocked sample for %s", elapsed)
	}
	if out[0].SessionID != "" {
		t.Fatalf("cold resident hints must not invent a session: %+v", out[0])
	}
}

func TestEnrichSampleProcessesSkipsLSOFOnStaleResident(t *testing.T) {
	ResetPIDOwnershipCache()
	calls := 0
	pidOwnershipLSOF = func(ctx context.Context, pids []int) ([]byte, error) {
		calls++
		return []byte("p42\nn/Users/user/.grok/sessions/x/019f5be0-7d38-7271-ba7d-8ade4a407bf0/updates.jsonl\n"), nil
	}
	t.Cleanup(func() {
		pidOwnershipLSOF = runLSOFForPIDs
		ResetPIDOwnershipCache()
	})
	processes := []Process{{PID: 42, Agent: "grok", Executable: "grok"}}
	stale := enrichSampleProcesses(context.Background(), processes, "", false, "resident")
	if calls != 0 {
		t.Fatalf("stale resident inventory must not probe lsof, calls=%d", calls)
	}
	if stale[0].SessionID != "" {
		t.Fatalf("stale resident must not invent a session id: %+v", stale[0])
	}
	fresh := enrichSampleProcesses(context.Background(), processes, "", true, "resident")
	if calls != 0 {
		t.Fatalf("resident samples must never probe lsof, calls=%d", calls)
	}
	if fresh[0].SessionID != "" {
		t.Fatalf("resident must not invent a session id without session-state hints: %+v", fresh[0])
	}
	ResetPIDOwnershipCache()
	operator := enrichSampleProcesses(context.Background(), processes, "", false, "operator")
	if calls != 1 {
		t.Fatalf("operator samples may probe even on a stale flag, calls=%d", calls)
	}
	if operator[0].SessionID == "" {
		t.Fatal("operator sample should attach session id")
	}
}

func TestParseLSOFOwnershipAndCache(t *testing.T) {
	ResetPIDOwnershipCache()
	body := []byte("p42\nn/Users/user/.grok/sessions/x/019f5be0-7d38-7271-ba7d-8ade4a407bf0/updates.jsonl\n")
	hints := parseLSOFOwnership(body)
	if len(hints) != 1 || hints[0].PID != 42 || hints[0].Agent != "grok" || hints[0].SessionID != "019f5be0-7d38-7271-ba7d-8ade4a407bf0" {
		t.Fatalf("hints=%+v", hints)
	}
	// Inject lsof seam for LoadPIDOwnershipHints
	calls := 0
	pidOwnershipLSOF = func(ctx context.Context, pids []int) ([]byte, error) {
		calls++
		return body, nil
	}
	t.Cleanup(func() {
		pidOwnershipLSOF = runLSOFForPIDs
		ResetPIDOwnershipCache()
	})
	processes := []Process{{PID: 42, Agent: "grok", Executable: "grok"}}
	first := LoadPIDOwnershipHints(context.Background(), processes)
	second := LoadPIDOwnershipHints(context.Background(), processes)
	if calls != 1 {
		t.Fatalf("expected cached lsof, calls=%d", calls)
	}
	if len(first) != 1 || len(second) != 1 {
		t.Fatalf("first=%+v second=%+v", first, second)
	}
	enriched := ApplyPIDOwnershipHints(processes, first)
	if enriched[0].SessionID == "" {
		t.Fatal("session id not attached")
	}
}

func TestSummarizeAgentShapedProcessesIncludesUnlabeled(t *testing.T) {
	catalog := CompileAgentIdentityCatalog(DefaultAgentIdentityRules())
	processes := []Process{
		{PID: 1, Agent: "grok", Executable: "grok", SessionID: "019f5be0-7d38-7271-ba7d-8ade4a407bf0"},
		{PID: 2, Agent: "", Executable: "grok-0.2.118-mac"},
		{PID: 3, Agent: "codex", Executable: "codex"},
	}
	summary := SummarizeAgentShapedProcesses(catalog, processes)
	if len(summary) < 2 {
		t.Fatalf("summary=%+v", summary)
	}
	report := BuildAgentIdentityReport(catalog, "/Users/user", nil, processes)
	if len(report.ProcessSummary) == 0 {
		t.Fatal("report missing process summary")
	}
	var unlabeledGrok bool
	for _, row := range report.ProcessSummary {
		if row.Agent == "grok" && row.UnlabeledCount > 0 {
			unlabeledGrok = true
		}
	}
	if !unlabeledGrok {
		t.Fatalf("expected unlabeled grok-shaped process in summary: %+v", report.ProcessSummary)
	}
}

func TestDefaultCatalogIncludesKimiCodeInstall(t *testing.T) {
	catalog := CompileAgentIdentityCatalog(DefaultAgentIdentityRules())
	agent, _, ok := catalog.MatchPath("/Users/user/.kimi-code/bin/kimi", "/Users/user")
	if !ok || agent != "kimi" {
		t.Fatalf("kimi-code install path not recognized: %q %v", agent, ok)
	}
}

// The catalog checks the overlay at most once per interval and recompiles
// only when the overlay changed.
func TestAgentIdentityCatalogChecksOverlayOnIntervalAndReusesUnchanged(t *testing.T) {
	dir := t.TempDir()
	path := AgentIdentityOverlayPath(dir)
	write := func(basename string) {
		t.Helper()
		raw, _ := json.Marshal(AgentIdentityOverlay{SchemaVersion: agentIdentitySchemaVersion, Rules: []AgentIdentityRule{{Agent: "kimi", ExactBasenames: []string{basename}}}})
		if err := os.WriteFile(path, raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	expire := func() {
		agentIdentityMu.Lock()
		agentIdentityCacheAt = agentIdentityCacheAt.Add(-agentIdentityCheckInterval)
		agentIdentityMu.Unlock()
	}
	t.Cleanup(ResetAgentIdentityCatalogCache)
	write("kimi-cli")
	ResetAgentIdentityCatalogCache()
	first := loadAgentIdentityCatalogCached(dir)
	if _, _, ok := first.MatchExactBasename("kimi-cli"); !ok {
		t.Fatal("overlay not loaded")
	}
	if loadAgentIdentityCatalogCached(dir) != first {
		t.Fatal("a second lookup inside the interval recompiled the catalog")
	}
	expire()
	if loadAgentIdentityCatalogCached(dir) != first {
		t.Fatal("an unchanged overlay recompiled the catalog")
	}
	write("kimi-cli-two")
	if loadAgentIdentityCatalogCached(dir) != first {
		t.Fatal("the overlay was stat'ed inside the check interval")
	}
	expire()
	changed := loadAgentIdentityCatalogCached(dir)
	if changed == first {
		t.Fatal("a changed overlay was not reloaded after the interval")
	}
	if _, _, ok := changed.MatchExactBasename("kimi-cli-two"); !ok {
		t.Fatal("reloaded catalog lacks the new overlay rule")
	}
	if other := loadAgentIdentityCatalogCached(t.TempDir()); other == changed {
		t.Fatal("a different data dir reused the cached catalog")
	}
}
