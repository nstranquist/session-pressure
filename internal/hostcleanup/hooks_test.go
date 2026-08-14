package hostcleanup

import (
	"context"
	"testing"
	"time"

	"github.com/nstranquist/session-pressure/sessionpressure"
	"github.com/nstranquist/session-pressure/sessionpressurecleanup"
)

func TestPlanSeesInstalledBrowserProvider(t *testing.T) {
	sessionpressurecleanup.Reset()
	t.Cleanup(sessionpressurecleanup.Reset)
	sessionpressurecleanup.Install(sessionpressurecleanup.Providers{
		ListBrowser: func() ([]sessionpressurecleanup.BrowserSession, error) {
			return []sessionpressurecleanup.BrowserSession{{
				Name:           "fixture-browser",
				PID:            4242,
				IdleTimeout:    "1s",
				Lifecycle:      "idle",
				StartedAt:      time.Now().Add(-2 * time.Hour),
				LastActivityAt: time.Now().Add(-2 * time.Hour),
			}}, nil
		},
	})
	dir := t.TempDir()
	if err := SavePolicy(dir, DefaultPolicy()); err != nil {
		t.Fatal(err)
	}
	report, err := NewManager(dir).Plan(context.Background(), sessionpressure.Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	for _, candidate := range report.Candidates {
		if candidate.Provider == "ndev_browser" && candidate.ResourceID == "fixture-browser" {
			return
		}
	}
	t.Fatalf("cleanup plan missing installed nicos browser: %+v", report.Candidates)
}

func TestPlanWithoutHooksHasNoNicosBrowserProvider(t *testing.T) {
	sessionpressurecleanup.Reset()
	dir := t.TempDir()
	if err := SavePolicy(dir, DefaultPolicy()); err != nil {
		t.Fatal(err)
	}
	report, err := NewManager(dir).Plan(context.Background(), sessionpressure.Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	for _, candidate := range report.Candidates {
		if candidate.Provider == "ndev_browser" {
			t.Fatalf("OSS manager leaked a browser candidate: %+v", candidate)
		}
	}
}
