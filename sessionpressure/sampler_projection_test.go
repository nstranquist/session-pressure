package sessionpressure

import (
	"context"
	"reflect"
	"testing"
	"time"
)

func projectionFixture(t *testing.T) []Process {
	t.Helper()
	processes, err := parseProcesses(processFixture)
	if err != nil {
		t.Fatal(err)
	}
	return processes
}

// The resident reuses the projection for an unchanged list and catalog,
// rebuilds it on any process or catalog change, and always matches a fresh
// build.
func TestSamplerProjectionReusesOnlyUnchangedInventory(t *testing.T) {
	catalog := CompileAgentIdentityCatalog(DefaultAgentIdentityRules())
	resident := &Sampler{role: "resident"}
	fresh := func(processes []Process, catalog *AgentIdentityCatalog) ([]AgentTree, []HostConsumer, []MemoryFamily) {
		return (&Sampler{role: "operator"}).projectInventory(processes, catalog)
	}
	check := func(label string, processes []Process, catalog *AgentIdentityCatalog) []AgentTree {
		t.Helper()
		trees, consumers, families := resident.projectInventory(processes, catalog)
		wantTrees, wantConsumers, wantFamilies := fresh(processes, catalog)
		if !reflect.DeepEqual(trees, wantTrees) || !reflect.DeepEqual(consumers, wantConsumers) || !reflect.DeepEqual(families, wantFamilies) {
			t.Fatalf("%s: projection differs from a fresh build", label)
		}
		return trees
	}
	processes := projectionFixture(t)
	first := check("first", processes, catalog)
	if len(first) == 0 {
		t.Fatal("fixture built no trees")
	}
	same := check("same list", append([]Process(nil), processes...), catalog)
	if &same[0] != &first[0] {
		t.Fatal("an unchanged inventory rebuilt the projection")
	}
	changed := append([]Process(nil), processes...)
	changed[1].RSSKB += 1_000_000
	grown := check("changed RSS", changed, catalog)
	if &grown[0] == &first[0] {
		t.Fatal("a changed process reused the old projection")
	}
	other := CompileAgentIdentityCatalog(DefaultAgentIdentityRules())
	recompiled := check("new catalog", changed, other)
	if &recompiled[0] == &grown[0] {
		t.Fatal("a new catalog reused the old projection")
	}
	// An append by a caller must not write into the shared projection.
	extended := append(recompiled, AgentTree{Agent: "appended"})
	again := check("after append", changed, other)
	if len(again) != len(recompiled) || &extended[0] == &again[0] {
		t.Fatal("an append wrote into the shared projection")
	}
}

// Sample enriches a copy of the top trees, never the shared projection.
func TestSamplerSampleDoesNotWriteSharedProjection(t *testing.T) {
	now := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
	sampler := testSampler(now)
	policy := DefaultPolicy(16 * 1024)
	first, err := sampler.Sample(context.Background(), policy)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.TopAgentTrees) == 0 {
		t.Fatal("fixture produced no top trees")
	}
	first.TopAgentTrees[0].SemanticState = "written-by-caller"
	second, err := sampler.Sample(context.Background(), policy)
	if err != nil {
		t.Fatal(err)
	}
	sampler.projectionMu.Lock()
	shared := sampler.projection.trees
	sampler.projectionMu.Unlock()
	for index := range shared {
		if shared[index].SemanticState == "written-by-caller" {
			t.Fatal("a snapshot write reached the shared projection")
		}
	}
	if len(second.TopAgentTrees) == 0 || &second.TopAgentTrees[0] == &shared[0] {
		t.Fatal("the snapshot aliases the shared projection")
	}
	if second.TopAgentTrees[0].SemanticState == "written-by-caller" {
		t.Fatal("a previous snapshot write leaked into the next sample")
	}
}
