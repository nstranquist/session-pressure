package sessionpressure

import (
	"strings"
	"testing"
)

func footprintBand(warning, red, critical float64) *LevelBand {
	return &LevelBand{Warning: warning, Red: red, Critical: critical}
}

// A tree sums phys_footprint; a process without a reading counts with RSS.
func TestAgentTreeSumsFootprintWithRSSFallback(t *testing.T) {
	root := Process{PID: 5, PPID: 1, Agent: "claude", Executable: "claude", RSSKB: 400 << 10, FootprintKB: 300 << 10, FootprintValid: true}
	helper := Process{PID: 6, PPID: 5, RSSKB: 200 << 10, FootprintKB: 50 << 10, FootprintValid: true}
	foreign := Process{PID: 7, PPID: 5, RSSKB: 100 << 10} // e.g. another user's process: no footprint
	trees := buildAgentTrees([]Process{root, helper, foreign})
	if len(trees) != 1 {
		t.Fatalf("trees: %+v", trees)
	}
	tree := trees[0]
	if tree.RSSSumMB != 700 || tree.FootprintMB != 450 || tree.FootprintProcessCount != 2 {
		t.Fatalf("tree rss %g footprint %g readings %d", tree.RSSSumMB, tree.FootprintMB, tree.FootprintProcessCount)
	}
	// Linux /proc rows carry no footprint: the tree records none.
	linux := buildAgentTrees([]Process{{PID: 5, PPID: 1, Agent: "claude", Executable: "claude", RSSKB: 400 << 10}})
	if linux[0].FootprintMB != 0 || linux[0].FootprintProcessCount != 0 {
		t.Fatalf("no readings must leave footprint unset: %+v", linux[0])
	}
}

func TestAnnotateTreeLSPFootprint(t *testing.T) {
	trees := []AgentTree{{RootPID: 5, PIDs: []int{5, 6}, FootprintMB: 900, FootprintProcessCount: 2}}
	processes := []Process{
		{PID: 5, RSSKB: 100 << 10, FootprintKB: 500 << 10, FootprintValid: true},
		{PID: 6, RSSKB: 300 << 10, FootprintKB: 400 << 10, FootprintValid: true},
	}
	annotateTreeLSP(trees, processes, map[int]string{6: MemoryFamilyLSP})
	if trees[0].LSPRSSSumMB != 300 || trees[0].LSPFootprintMB != 400 {
		t.Fatalf("lsp rss %g footprint %g", trees[0].LSPRSSSumMB, trees[0].LSPFootprintMB)
	}
	if got := treeRungFootprintMB(trees[0]); got != 500 {
		t.Fatalf("footprint rung size %g, want 500", got)
	}
}

func withoutFootprintBands(policy Policy) Policy {
	t := &policy.Thresholds
	t.TreeFootprintWarningMB, t.TreeFootprintRedMB, t.TreeFootprintCriticalMB = 0, 0, 0
	t.AgentTotalFootprintWarningMB, t.AgentTotalFootprintRedMB, t.AgentTotalFootprintCriticalMB = 0, 0, 0
	return policy
}

func footprintPolicy() Policy {
	policy := DefaultPolicy(16 * 1024)
	t := &policy.Thresholds
	t.TreeFootprintWarningMB, t.TreeFootprintRedMB, t.TreeFootprintCriticalMB = 6000, 8000, 10000
	t.AgentTotalFootprintWarningMB, t.AgentTotalFootprintRedMB, t.AgentTotalFootprintCriticalMB = 9000, 11000, 13000
	return policy
}

func footprintSnapshot(freePercent int, rssMB, footprintMB float64, available bool) Snapshot {
	tree := AgentTree{Agent: "claude", RootPID: 749, RSSSumMB: rssMB}
	if available {
		tree.FootprintMB, tree.FootprintProcessCount = footprintMB, 100
	}
	return Snapshot{
		FreePercent: freePercent, AgentRSSSumMB: rssMB,
		AgentFootprintMB: footprintMB, AgentFootprintAvailable: available,
		TopAgentTrees: []AgentTree{tree},
	}
}

func TestFootprintRungsReplaceRSSWhenMeasured(t *testing.T) {
	policy := footprintPolicy()
	if err := policy.Validate(); err != nil {
		t.Fatal(err)
	}
	// RSS alone (7008 MB) would be red on the 16 GiB ladder; the footprint
	// (5000 MB) is under its warning band, so the host is normal.
	got := EvaluateMemoryPressure(footprintSnapshot(60, 7008, 5000, true), policy)
	if got.Level != LevelNormal {
		t.Fatalf("footprint 5000 MB = %s %v", got.Level, got.Reasons)
	}
	got = EvaluateMemoryPressure(footprintSnapshot(60, 3000, 8500, true), policy)
	reasons := strings.Join(got.Reasons, "; ")
	if got.Level != LevelRed || !strings.Contains(reasons, "claude tree 749 footprint 8500 MB >= red 8000 MB") {
		t.Fatalf("footprint 8500 MB = %s %v", got.Level, got.Reasons)
	}
	got = EvaluateMemoryPressure(footprintSnapshot(60, 3000, 13500, true), policy)
	if got.Level != LevelCritical || !strings.Contains(strings.Join(got.Reasons, "; "), "agent footprint 13500 MB >= critical 13000 MB") {
		t.Fatalf("agent footprint 13500 MB = %s %v", got.Level, got.Reasons)
	}
	// Without readings the RSS bands judge, as before.
	got = EvaluateMemoryPressure(footprintSnapshot(60, 7008, 7008, false), policy)
	if got.Level != LevelRed || !strings.Contains(strings.Join(got.Reasons, "; "), "RSS sum 7008 MB >= red 5120 MB") {
		t.Fatalf("no footprint = %s %v", got.Level, got.Reasons)
	}
	// Bands off: RSS judges even with readings.
	off := withoutFootprintBands(DefaultPolicy(16 * 1024))
	got = EvaluateMemoryPressure(footprintSnapshot(60, 7008, 5000, true), off)
	if got.Level != LevelRed {
		t.Fatalf("bands off = %s %v", got.Level, got.Reasons)
	}
}

func TestFootprintRungsKeepEscalationHold(t *testing.T) {
	policy := footprintPolicy()
	policy.Thresholds.RSSEscalationFreePercent = 50
	got := EvaluateMemoryPressure(footprintSnapshot(80, 3000, 8500, true), policy)
	if got.Level != LevelWarning || !strings.Contains(strings.Join(got.Reasons, "; "), "held at warning: host free 80% > 50%") {
		t.Fatalf("held footprint = %s %v", got.Level, got.Reasons)
	}
}

func TestPolicyValidateFootprintBands(t *testing.T) {
	policy := footprintPolicy()
	policy.Thresholds.TreeFootprintRedMB = 5000
	if err := policy.Validate(); err == nil || !strings.Contains(err.Error(), "tree footprint thresholds") {
		t.Fatalf("error = %v", err)
	}
	policy = DefaultPolicy(16 * 1024)
	policy.Thresholds.AgentTotalFootprintRedMB = 5000
	if err := policy.Validate(); err == nil || !strings.Contains(err.Error(), "or all be 0 (off)") {
		t.Fatalf("partial bands error = %v", err)
	}
}

func footprintConfig() MemoryLevelsConfig {
	config := EmbeddedMemoryLevels()
	anchors := append([]MemoryLevelAnchor(nil), config.Anchors...)
	anchors[0].TreeFootprintMB, anchors[0].AgentTotalFootprintMB = footprintBand(6000, 8000, 10000), footprintBand(9000, 11000, 13000)
	anchors[1].TreeFootprintMB, anchors[1].AgentTotalFootprintMB = footprintBand(18000, 24000, 30000), footprintBand(27000, 33000, 39000)
	config.Anchors = anchors
	config.Hosts = nil
	return config
}

func TestMemoryLevelsFootprintBands(t *testing.T) {
	config := footprintConfig()
	if err := config.Validate(); err != nil {
		t.Fatal(err)
	}
	mid, err := config.Resolve(32*1024, "")
	if err != nil {
		t.Fatal(err)
	}
	if mid.TreeFootprintMB == nil || mid.TreeFootprintMB.Red != 16000 || mid.AgentTotalFootprintMB.Critical != 26000 {
		t.Fatalf("32 GiB footprint bands %+v %+v", mid.TreeFootprintMB, mid.AgentTotalFootprintMB)
	}
	big, _ := config.Resolve(96*1024, "")
	if big.TreeFootprintMB.Red != 48000 {
		t.Fatalf("96 GiB tree footprint red %g", big.TreeFootprintMB.Red)
	}
	policy := DefaultPolicy(16 * 1024)
	mid.Apply(&policy)
	if policy.Thresholds.TreeFootprintRedMB != 16000 || !policy.Thresholds.FootprintRungsEnabled() {
		t.Fatalf("applied %+v", policy.Thresholds)
	}
	// A config without footprint bands turns the rungs off again.
	plainConfig := EmbeddedMemoryLevels()
	plainAnchors := append([]MemoryLevelAnchor(nil), plainConfig.Anchors...)
	for index := range plainAnchors {
		plainAnchors[index].TreeFootprintMB, plainAnchors[index].AgentTotalFootprintMB, plainAnchors[index].FootprintEvidence = nil, nil, ""
	}
	plainConfig.Anchors = plainAnchors
	if err := plainConfig.Validate(); err != nil {
		t.Fatal(err)
	}
	plain, _ := plainConfig.Resolve(32*1024, "")
	plain.Apply(&policy)
	if policy.Thresholds.FootprintRungsEnabled() || policy.Thresholds.TreeFootprintWarningMB != 0 {
		t.Fatalf("footprint bands left on: %+v", policy.Thresholds)
	}
	if err := policy.Validate(); err != nil {
		t.Fatal(err)
	}

	half := footprintConfig()
	half.Anchors[1].TreeFootprintMB, half.Anchors[1].AgentTotalFootprintMB = nil, nil
	if err := half.Validate(); err == nil || !strings.Contains(err.Error(), "footprint bands are set on 1 of 2 anchors") {
		t.Fatalf("half error = %v", err)
	}
	one := footprintConfig()
	one.Anchors[0].AgentTotalFootprintMB = nil
	if err := one.Validate(); err == nil || !strings.Contains(err.Error(), "set both or neither") {
		t.Fatalf("one-band error = %v", err)
	}
	bad := footprintConfig()
	bad.Anchors[1].TreeFootprintMB = footprintBand(24000, 18000, 30000)
	if err := bad.Validate(); err == nil || !strings.Contains(err.Error(), "tree_footprint_mb: must satisfy warning < red < critical") {
		t.Fatalf("order error = %v", err)
	}
	host := plainConfig
	host.Hosts = map[string]MemoryLevelHost{"big": {Reason: "x", TreeFootprintMB: footprintBand(1, 2, 3)}}
	if err := host.Validate(); err == nil || !strings.Contains(err.Error(), "the anchors have none") {
		t.Fatalf("host without anchors error = %v", err)
	}
	override := footprintConfig()
	override.Hosts = map[string]MemoryLevelHost{"big": {Reason: "x", TreeFootprintMB: footprintBand(1000, 2000, 3000)}}
	levels, err := override.Resolve(16*1024, "big")
	if err != nil || levels.TreeFootprintMB.Red != 2000 || levels.AgentTotalFootprintMB.Red != 11000 {
		t.Fatalf("host override %+v %v", levels.TreeFootprintMB, err)
	}
}

func TestTelemetrySummaryCarriesCalibrationInputs(t *testing.T) {
	snapshot := footprintSnapshot(60, 7008, 5000, true)
	snapshot.TopAgentTrees[0].LSPRSSSumMB = 1000
	snapshot.TopAgentTrees[0].LSPFootprintMB = 400
	summary := compactTelemetrySummary(snapshot)
	if summary.AgentFootprintMB != 5000 || summary.TopTreeRSSMB != 6008 || summary.TopTreeFootprintMB != 4600 {
		t.Fatalf("summary footprint %g, tree rss %g, tree footprint %g", summary.AgentFootprintMB, summary.TopTreeRSSMB, summary.TopTreeFootprintMB)
	}
	linux := compactTelemetrySummary(footprintSnapshot(60, 7008, 7008, false))
	if linux.AgentFootprintMB != 0 || linux.TopTreeFootprintMB != 0 || linux.TopTreeRSSMB != 7008 {
		t.Fatalf("linux summary %+v", linux)
	}
}
