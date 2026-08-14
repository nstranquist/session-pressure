package hostcleanup

import (
	"context"
	"errors"
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

func TestPlanSurfacesPlanDockerError(t *testing.T) {
	sessionpressurecleanup.Reset()
	t.Cleanup(sessionpressurecleanup.Reset)
	sessionpressurecleanup.Install(sessionpressurecleanup.Providers{
		PlanDocker: func(context.Context, int, int) ([]sessionpressurecleanup.DockerAction, error) {
			return nil, errors.New("collect failed")
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
	if report.ProviderErrors["docker_workspace"] != "collect failed" {
		t.Fatalf("provider errors = %#v", report.ProviderErrors)
	}
}

func TestPlanDockerHonorsCallerContext(t *testing.T) {
	sessionpressurecleanup.Reset()
	t.Cleanup(sessionpressurecleanup.Reset)
	sessionpressurecleanup.Install(sessionpressurecleanup.Providers{
		PlanDocker: func(ctx context.Context, _, _ int) ([]sessionpressurecleanup.DockerAction, error) {
			return nil, ctx.Err()
		},
	})
	dir := t.TempDir()
	if err := SavePolicy(dir, DefaultPolicy()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	report, err := NewManager(dir).Plan(ctx, sessionpressure.Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	if report.ProviderErrors["docker_workspace"] != context.Canceled.Error() {
		t.Fatalf("provider errors = %#v", report.ProviderErrors)
	}
}

func TestApplyDockerKeepsLastUsedAtThroughHooks(t *testing.T) {
	sessionpressurecleanup.Reset()
	t.Cleanup(sessionpressurecleanup.Reset)
	const stamp = "2026-08-01T00:00:00Z"
	var applied sessionpressurecleanup.DockerAction
	sessionpressurecleanup.Install(sessionpressurecleanup.Providers{
		PlanDocker: func(context.Context, int, int) ([]sessionpressurecleanup.DockerAction, error) {
			return []sessionpressurecleanup.DockerAction{{
				WorkspaceID: "nicos-api",
				Workspace:   "feat/api",
				Action:      "would_stop",
				LastUsedAt:  stamp,
			}}, nil
		},
		ApplyDocker: func(_ context.Context, action sessionpressurecleanup.DockerAction) (sessionpressurecleanup.DockerAction, error) {
			applied = action
			action.Action = "stopped"
			return action, nil
		},
	})
	manager := NewManager(t.TempDir())
	candidate := Candidate{
		ResourceKind: ResourceDockerWorkspace,
		ResourceID:   "feat/api",
		Eligible:     true,
		private: dockerCandidate{Action: trimFromDockerAction(sessionpressurecleanup.DockerAction{
			WorkspaceID: "nicos-api",
			Workspace:   "feat/api",
			Action:      "would_stop",
			LastUsedAt:  stamp,
		})},
	}
	result, detail := manager.applyDocker(context.Background(), candidate, DefaultPolicy())
	if result != "docker_workspace_stopped" || detail != "" {
		t.Fatalf("result=%q detail=%q", result, detail)
	}
	if applied.LastUsedAt != stamp {
		t.Fatalf("apply lost LastUsedAt: %#v", applied)
	}
}
