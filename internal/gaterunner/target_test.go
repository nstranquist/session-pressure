package gaterunner

import (
	"context"
	"testing"
)

func TestInspectionEndpointIsBoundToContext(t *testing.T) {
	first, err := WithTarget(context.Background(), "runner-1", "builder")
	if err != nil {
		t.Fatal(err)
	}
	second, err := WithTarget(first, "runner-2", "builder")
	if err != nil {
		t.Fatal(err)
	}
	if runnerTarget(first) != "builder@runner-1" || runnerTarget(second) != "builder@runner-2" {
		t.Fatal("endpoint leaked across inspections")
	}
	if _, err := WithTarget(first, "-oProxyCommand=x", "builder"); err == nil {
		t.Fatal("unsafe host accepted")
	}
}
