package processtree

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestCommandContextPreservesOutput(t *testing.T) {
	cmd := CommandContext(context.Background(), "sh", "-c", "printf ready")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("CombinedOutput: %v", err)
	}
	if string(out) != "ready" {
		t.Fatalf("output = %q, want ready", out)
	}
}

func TestCommandContextTimeoutDoesNotWaitOnDescendantOutput(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	cmd := CommandContext(ctx, "sh", "-c", "sleep 30 & wait")
	_, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if !strings.Contains(err.Error(), "killed") && ctx.Err() != context.DeadlineExceeded {
		t.Fatalf("expected cancellation error, got %v", err)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("timeout waited on descendant-owned output for %s", elapsed)
	}
}
