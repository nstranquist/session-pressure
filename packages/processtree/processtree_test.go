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

func TestCredentialFreeEnvironmentRemovesSecretsWithoutChangingSafeValues(t *testing.T) {
	ambient := []string{
		"PATH=/usr/bin", "OPENAI_API_KEY=secret", "RENDER_AUTH_SECRET=secret",
		"SESSION_TOKEN=secret", "PORT=8788", "malformed",
	}
	got := CredentialFreeEnvironment(ambient)
	joined := strings.Join(got, "\n")
	for _, forbidden := range []string{"OPENAI_API_KEY", "RENDER_AUTH_SECRET", "SESSION_TOKEN", "malformed"} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("credential-free environment retained %q: %v", forbidden, got)
		}
	}
	for _, required := range []string{"PATH=/usr/bin", "PORT=8788"} {
		if !strings.Contains(joined, required) {
			t.Fatalf("credential-free environment removed %q: %v", required, got)
		}
	}
}
