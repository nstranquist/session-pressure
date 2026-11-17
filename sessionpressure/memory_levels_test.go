package sessionpressure

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	oldMacMB = 16 * 1024 // mac-16g, 16 GiB M1 Pro
	m4MB     = 48 * 1024 // mac-48g, 48 GiB M4 Pro
)

func stubHost(t *testing.T, memoryMB float64, name string) {
	t.Helper()
	oldMemory, oldName := hostMemoryMB, hostName
	hostMemoryMB = func() (float64, error) { return memoryMB, nil }
	hostName = func() (string, error) { return name, nil }
	t.Cleanup(func() { hostMemoryMB, hostName = oldMemory, oldName })
}

func TestEmbeddedMemoryLevelsValid(t *testing.T) {
	if _, err := ParseMemoryLevels(embeddedMemoryLevelsJSON); err != nil {
		t.Fatalf("embedded memory_levels.json: %v", err)
	}
}

// The 16 GiB laptop keeps exactly the ladder that protected it before the
// levels moved into memory_levels.json.
func TestDefaultPolicyOldMacKeepsTunedLadder(t *testing.T) {
	got := DefaultPolicy(oldMacMB)
	want := Thresholds{
		HostCPUWarningPercent: 85, HostCPURedPercent: 95,
		FreeWarningPercent: 25, FreeRedPercent: 15, FreeCriticalPercent: 8,
		SwapWarningMB: 4096, SwapRedMB: 8192, SwapCriticalMB: 12288,
		SwapOutWarningMBPerSecond: 8, SwapOutThrashMBPerSecond: 32, SwapOutReleaseMBPerSecond: 16,
		TreeWarningMB: 3072, TreeRedMB: 5120, TreeCriticalMB: 7168,
		AgentTotalWarningMB: 8192, AgentTotalRedMB: 11264, AgentTotalCriticalMB: 13312,
		RSSEscalationFreePercent: 0,
	}
	if got.Thresholds != want {
		t.Fatalf("16 GiB thresholds changed:\n got %+v\nwant %+v", got.Thresholds, want)
	}
	if got.ResourceBudgets.MaxSelfRSSMB != 30 {
		t.Fatalf("16 GiB guard RSS budget = %g, want 30", got.ResourceBudgets.MaxSelfRSSMB)
	}
	if err := got.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestDefaultPolicyM4UsesMeasuredAnchor(t *testing.T) {
	got := DefaultPolicy(m4MB).Thresholds
	if got.TreeWarningMB != 20480 || got.TreeRedMB != 26624 || got.TreeCriticalMB != 32768 {
		t.Fatalf("48 GiB tree ladder = %g/%g/%g", got.TreeWarningMB, got.TreeRedMB, got.TreeCriticalMB)
	}
	if got.AgentTotalWarningMB != 36864 || got.AgentTotalRedMB != 45056 || got.AgentTotalCriticalMB != 53248 {
		t.Fatalf("48 GiB agent ladder = %g/%g/%g", got.AgentTotalWarningMB, got.AgentTotalRedMB, got.AgentTotalCriticalMB)
	}
	if got.RSSEscalationFreePercent != 50 {
		t.Fatalf("48 GiB RSS escalation = %g, want 50", got.RSSEscalationFreePercent)
	}
	if budget := DefaultPolicy(m4MB).ResourceBudgets.MaxSelfRSSMB; budget != 36 {
		t.Fatalf("48 GiB guard RSS budget = %g, want 36", budget)
	}
}

func TestResolveInterpolatesAndScales(t *testing.T) {
	config := EmbeddedMemoryLevels()
	mid, err := config.Resolve(32*1024, "")
	if err != nil {
		t.Fatal(err)
	}
	// Halfway between the 16 and 48 GiB anchors.
	if mid.TreeMB.Red != (5120+26624)/2 || mid.RSSEscalationFreePercent != 75 || mid.GuardMaxSelfRSSMB != 33 {
		t.Fatalf("32 GiB = tree red %g, escalation %g, guard %g", mid.TreeMB.Red, mid.RSSEscalationFreePercent, mid.GuardMaxSelfRSSMB)
	}
	if !strings.Contains(mid.Rule, "interpolated") {
		t.Fatalf("rule %q", mid.Rule)
	}
	small, err := config.Resolve(8*1024, "")
	if err != nil {
		t.Fatal(err)
	}
	if small.TreeMB.Red != 2560 || small.FreePercent.Red != 15 || small.GuardMaxSelfRSSMB != 30 {
		t.Fatalf("8 GiB = tree red %g, free red %g, guard %g", small.TreeMB.Red, small.FreePercent.Red, small.GuardMaxSelfRSSMB)
	}
	big, err := config.Resolve(96*1024, "")
	if err != nil {
		t.Fatal(err)
	}
	if big.TreeMB.Red != 2*26624 || big.SwapMB.Red != 2*24576 || big.RSSEscalationFreePercent != 50 || big.GuardMaxSelfRSSMB != 36 {
		t.Fatalf("96 GiB = tree red %g, swap red %g, escalation %g, guard %g", big.TreeMB.Red, big.SwapMB.Red, big.RSSEscalationFreePercent, big.GuardMaxSelfRSSMB)
	}
}

func TestResolveHostOverride(t *testing.T) {
	tree := LevelBand{Warning: 4000, Red: 6000, Critical: 8000}
	guard := 32.0
	config := EmbeddedMemoryLevels()
	config.Hosts = map[string]MemoryLevelHost{"example-macbook-pro": {Reason: "test", TreeMB: &tree, GuardMaxSelfRSSMB: &guard}}
	if err := config.Validate(); err != nil {
		t.Fatal(err)
	}
	levels, err := config.Resolve(oldMacMB, "Example-MacBook-Pro.local")
	if err != nil {
		t.Fatal(err)
	}
	if !levels.HostOverride || levels.TreeMB != tree || levels.GuardMaxSelfRSSMB != 32 || levels.SwapMB.Red != 8192 {
		t.Fatalf("override not applied: %+v", levels)
	}
	other, _ := config.Resolve(oldMacMB, "mac-48g")
	if other.HostOverride || other.TreeMB.Red != 5120 {
		t.Fatalf("override leaked to another host: %+v", other)
	}
}

func TestParseMemoryLevelsRejectsBadConfig(t *testing.T) {
	valid := string(embeddedMemoryLevelsJSON)
	cases := []struct {
		name, body, want string
	}{
		{"unknown field", strings.Replace(valid, `"tree_mb"`, `"tree_mbx"`, 1), `unknown field "tree_mbx"`},
		{"ladder order", strings.Replace(valid, `"tree_mb": {"warning": 3072, "red": 5120`, `"tree_mb": {"warning": 6000, "red": 5120`, 1), "anchors[0] (16gib-laptop).tree_mb: must satisfy warning < red < critical"},
		{"free order", strings.Replace(valid, `"free_percent": {"warning": 25, "red": 15, "critical": 8},`, `"free_percent": {"warning": 8, "red": 15, "critical": 25},`, 1), "free memory must satisfy warning > red > critical"},
		{"anchor order", strings.Replace(valid, `"memory_gib": 48`, `"memory_gib": 12`, 1), "strictly ascending memory order"},
		{"escalation", strings.Replace(valid, `"rss_escalation_free_percent": 50`, `"rss_escalation_free_percent": 150`, 1), "rss_escalation_free_percent: must be in (0, 100]"},
		{"guard", strings.Replace(valid, `"guard_max_self_rss_mb": 36`, `"guard_max_self_rss_mb": 2`, 1), "guard_max_self_rss_mb: must be between 10 and 256"},
		{"schema", strings.Replace(valid, `"schema_version": 1`, `"schema_version": 2`, 1), "schema_version: want 1, got 2"},
		{"trailing", valid + "{}", "trailing data"},
		{"host key", strings.Replace(valid, `"hosts": {}`, `"hosts": {"Big.Host": {"reason": "x"}}`, 1), "key must be a lowercase short hostname"},
		{"host reason", strings.Replace(valid, `"hosts": {}`, `"hosts": {"big": {"reason": ""}}`, 1), `hosts["big"].reason: required`},
		{"host band", strings.Replace(valid, `"hosts": {}`, `"hosts": {"big": {"reason": "x", "swap_mb": {"warning": 3, "red": 2, "critical": 1}}}`, 1), `hosts["big"].swap_mb: must satisfy`},
		{"empty anchors", `{"schema_version": 1, "anchors": []}`, "at least one measured host shape"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.body == valid {
				t.Fatal("replacement did not apply; fixture drifted from memory_levels.json")
			}
			_, err := ParseMemoryLevels([]byte(tc.body))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %v, want it to contain %q", err, tc.want)
			}
		})
	}
	config, _ := ParseMemoryLevels(embeddedMemoryLevelsJSON)
	config.Anchors[1].Evidence = " "
	if err := config.Validate(); err == nil || !strings.Contains(err.Error(), "anchors[1] (48gib-workstation).evidence: required") {
		t.Fatalf("missing evidence error = %v", err)
	}
}

func writeCalibratedPolicy(t *testing.T, dir, mode string) {
	t.Helper()
	policy := DefaultPolicy(oldMacMB)
	policy.MemoryLevels = mode
	// A stale ladder, as a 48 GiB host had from the old linear formula.
	policy.Thresholds.TreeWarningMB, policy.Thresholds.TreeRedMB, policy.Thresholds.TreeCriticalMB = 9216, 15360, 21504
	if err := SavePolicy(PolicyPath(dir), policy); err != nil {
		t.Fatal(err)
	}
}

func TestLoadPolicyCalibratedResolvesHostLevels(t *testing.T) {
	stubHost(t, m4MB, "mac-48g")
	dir := t.TempDir()
	writeCalibratedPolicy(t, dir, MemoryLevelsCalibrated)
	policy, persisted, err := LoadPolicy(PolicyPath(dir), 0)
	if err != nil || !persisted {
		t.Fatalf("LoadPolicy: persisted=%v err=%v", persisted, err)
	}
	if policy.Thresholds.TreeRedMB != 26624 || policy.Thresholds.RSSEscalationFreePercent != 50 || policy.ResourceBudgets.MaxSelfRSSMB != 36 {
		t.Fatalf("calibrated M4 policy = tree red %g, escalation %g, guard %g", policy.Thresholds.TreeRedMB, policy.Thresholds.RSSEscalationFreePercent, policy.ResourceBudgets.MaxSelfRSSMB)
	}
	// A caller that knows the memory size wins over the native read.
	policy, _, err = LoadPolicy(PolicyPath(dir), oldMacMB)
	if err != nil || policy.Thresholds.TreeRedMB != 5120 {
		t.Fatalf("explicit 16 GiB = %g, %v", policy.Thresholds.TreeRedMB, err)
	}
}

func TestLoadPolicyManualKeepsOwnThresholds(t *testing.T) {
	stubHost(t, m4MB, "mac-48g")
	for _, mode := range []string{"", MemoryLevelsManual} {
		dir := t.TempDir()
		writeCalibratedPolicy(t, dir, mode)
		policy, _, err := LoadPolicy(PolicyPath(dir), 0)
		if err != nil || policy.Thresholds.TreeRedMB != 15360 {
			t.Fatalf("mode %q: tree red %g, err %v", mode, policy.Thresholds.TreeRedMB, err)
		}
	}
}

func TestLoadPolicyUsesDataDirMemoryLevelsFile(t *testing.T) {
	stubHost(t, m4MB, "mac-48g")
	dir := t.TempDir()
	writeCalibratedPolicy(t, dir, MemoryLevelsCalibrated)
	override := strings.Replace(string(embeddedMemoryLevelsJSON), `"hosts": {}`, `"hosts": {"mac-48g": {"reason": "test", "tree_mb": {"warning": 30000, "red": 40000, "critical": 50000}}}`, 1)
	if err := os.WriteFile(MemoryLevelsPath(dir), []byte(override), 0o600); err != nil {
		t.Fatal(err)
	}
	policy, _, err := LoadPolicy(PolicyPath(dir), 0)
	if err != nil || policy.Thresholds.TreeRedMB != 40000 {
		t.Fatalf("data-dir override = %g, %v", policy.Thresholds.TreeRedMB, err)
	}
	levels, err := ResolveHostMemoryLevels(dir, 0)
	if err != nil || levels.Source != MemoryLevelsPath(dir) || !levels.HostOverride {
		t.Fatalf("source %q override %v err %v", levels.Source, levels.HostOverride, err)
	}
	bad := strings.Replace(string(embeddedMemoryLevelsJSON), `"red": 26624`, `"red": 99999`, 1)
	if err := os.WriteFile(MemoryLevelsPath(dir), []byte(bad), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, err = LoadPolicy(PolicyPath(dir), 0)
	if err == nil || !strings.Contains(err.Error(), filepath.Join(dir, "memory-levels.json")) || !strings.Contains(err.Error(), "tree_mb: must satisfy warning < red < critical") {
		t.Fatalf("bad file error = %v", err)
	}
}

func memorySnapshot(freePercent int, swapMB, treeMB float64) Snapshot {
	return Snapshot{
		FreePercent:   freePercent,
		SwapUsedMB:    swapMB,
		AgentRSSSumMB: treeMB,
		TopAgentTrees: []AgentTree{{Agent: "claude", RootPID: 994, RSSSumMB: treeMB}},
	}
}

// The 2026-11-17 05:39Z M4 sample was red at the old linear ladder: claude
// tree 16143 MB, 90% free, no swap.
func TestM4EvidenceSampleIsNotRed(t *testing.T) {
	policy := DefaultPolicy(m4MB)
	got := EvaluateMemoryPressure(memorySnapshot(90, 0, 16143), policy)
	if got.Level != LevelNormal {
		t.Fatalf("M4 evidence sample level %s, reasons %v", got.Level, got.Reasons)
	}
	// The observed M4 maximum (21746 MB at 90% free) is a warning only.
	got = EvaluateMemoryPressure(memorySnapshot(90, 0, 21746), policy)
	if got.Level != LevelWarning {
		t.Fatalf("M4 max sample level %s, reasons %v", got.Level, got.Reasons)
	}
}

func TestM4RSSRungsNeedKernelAgreement(t *testing.T) {
	policy := DefaultPolicy(m4MB)
	held := EvaluateMemoryPressure(memorySnapshot(90, 0, 40000), policy)
	if held.Level != LevelWarning || !strings.Contains(strings.Join(held.Reasons, "; "), "held at warning: host free 90% > 50%") {
		t.Fatalf("held = %s %v", held.Level, held.Reasons)
	}
	agreed := EvaluateMemoryPressure(memorySnapshot(45, 0, 40000), policy)
	if agreed.Level != LevelCritical {
		t.Fatalf("40 GB tree at 45%% free = %s %v", agreed.Level, agreed.Reasons)
	}
	// Free memory and swap are still judged on their own.
	low := EvaluateMemoryPressure(memorySnapshot(12, 0, 4000), policy)
	if low.Level != LevelRed {
		t.Fatalf("12%% free = %s %v", low.Level, low.Reasons)
	}
	swapped := EvaluateMemoryPressure(memorySnapshot(90, 30000, 40000), policy)
	if swapped.Level != LevelRed {
		t.Fatalf("held tree with 30 GB swap = %s %v", swapped.Level, swapped.Reasons)
	}
}

// The old Mac still reaches red and critical from the tree rung at any free
// level, as on 2026-11-17 10:29Z (claude tree 7008 MB at 68% free).
func TestOldMacKeepsTreeProtection(t *testing.T) {
	policy := DefaultPolicy(oldMacMB)
	red := EvaluateMemoryPressure(memorySnapshot(68, 1415, 7008), policy)
	if red.Level != LevelRed {
		t.Fatalf("old Mac 7008 MB tree = %s %v", red.Level, red.Reasons)
	}
	critical := EvaluateMemoryPressure(memorySnapshot(90, 0, 7200), policy)
	if critical.Level != LevelCritical {
		t.Fatalf("old Mac 7200 MB tree at 90%% free = %s %v", critical.Level, critical.Reasons)
	}
	// The M4 evidence load on the old Mac stays critical.
	if got := EvaluateMemoryPressure(memorySnapshot(90, 0, 16143), policy); got.Level != LevelCritical {
		t.Fatalf("16 GB tree on the old Mac = %s", got.Level)
	}
}

func TestPolicyValidateMemoryLevelsMode(t *testing.T) {
	policy := DefaultPolicy(oldMacMB)
	policy.MemoryLevels = "auto"
	if err := policy.Validate(); err == nil || !strings.Contains(err.Error(), `memory_levels must be "calibrated" or "manual"`) {
		t.Fatalf("mode error = %v", err)
	}
	policy.MemoryLevels = ""
	policy.Thresholds.RSSEscalationFreePercent = 120
	if err := policy.Validate(); err == nil {
		t.Fatal("escalation 120 accepted")
	}
}

func TestProcMemTotalMB(t *testing.T) {
	path := filepath.Join(t.TempDir(), "meminfo")
	if err := os.WriteFile(path, []byte("MemFree:  100 kB\nMemTotal:       65536000 kB\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := procMemTotalMB(path)
	if err != nil || got != 64000 {
		t.Fatalf("MemTotal = %g, %v", got, err)
	}
	if _, err := procMemTotalMB(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("missing file accepted")
	}
}
