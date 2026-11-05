package sessionpressure

import (
	"strings"
	"testing"
)

func TestGoBenchmarkResourceClass(t *testing.T) {
	for _, args := range [][]string{
		{"go", "test", "-run", "^$", "-bench", "CandidateVerifyPath", "-benchtime", "10x", "./internal/commitdelivery"},
		{"go", "-C", "module", "test", "-bench=CandidateVerifyPath", "./pkg"},
	} {
		for _, requested := range []WorkClass{"", WorkClassTest, WorkClassExpressTest, WorkClassBuild} {
			if got := ResolveWorkClass(requested, false, args); got != WorkClassBenchmark {
				t.Fatalf("%v requested=%s got=%s", args, requested, got)
			}
		}
	}
	for _, args := range [][]string{{"-args", "-bench=Fake"}, {"-run", "-bench=Fake"}, {"-ldflags", "-bench=Fake"}, {"-bench="}} {
		if goTestRunsBenchmark(args) {
			t.Fatalf("false benchmark: %v", args)
		}
	}
}

func TestUnknownWorkloadListsRegisteredIDs(t *testing.T) {
	err := ValidateWorkload("docgraph.ask", "warm")
	if err == nil || !strings.Contains(err.Error(), "docgraph-ask") || !strings.Contains(err.Error(), "browser-mcp-script") {
		t.Fatalf("unhelpful workload error: %v", err)
	}
}
